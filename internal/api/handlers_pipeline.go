// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// pipelineJSON is the tenant's pipeline in one answer, stage by stage.
type pipelineJSON struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Users       pipelineUsers      `json:"users"`
	Sources     []pipelineSource   `json:"sources"`
	Sync        pipelineSync       `json:"sync"`
	Processing  pipelineProcessing `json:"processing"`
	Storage     pipelineStorage    `json:"storage"`
	Delivery    pipelineDelivery   `json:"delivery"`
}

type pipelineUsers struct {
	Active   int64 `json:"active"`
	Deleting int64 `json:"deleting"`
}

// pipelineSource is one enabled provider, or one with connections of the
// tenant.
type pipelineSource struct {
	Slug    string   `json:"slug"`
	Name    string   `json:"name"`
	Kinds   []string `json:"kinds"`
	Enabled bool     `json:"enabled"`
	// Connections counts the tenant's connections by status (active,
	// needs_reauth, disconnected); Broken counts the active ones whose
	// health is not ok.
	Connections map[string]int64 `json:"connections"`
	Broken      int64            `json:"broken"`
	// PausedUntil is set while the provider's rate limit pauses every pull.
	PausedUntil *time.Time `json:"paused_until"`
}

type pipelineSync struct {
	Queued      int64 `json:"queued"`
	Running     int64 `json:"running"`
	Retrying    int64 `json:"retrying"`
	RateLimited int64 `json:"rate_limited"`
	// Failed24h and Succeeded24h count the runs created in the last 24
	// hours that ended so.
	Failed24h    int64 `json:"failed_24h"`
	Succeeded24h int64 `json:"succeeded_24h"`
}

type pipelineProcessing struct {
	Pending int64 `json:"pending"`
	Running int64 `json:"running"`
	Parked  int64 `json:"parked"`
	// RejectedRecords counts lines the processing could not store and has
	// not recovered, as rejected_records of the coverage report does.
	RejectedRecords int64 `json:"rejected_records"`
}

type pipelineStorage struct {
	RawObjects24h int64 `json:"raw_objects_24h"`
	RawBytes24h   int64 `json:"raw_bytes_24h"`
}

type pipelineDelivery struct {
	Endpoints         int64 `json:"endpoints"`
	EndpointsFailing  int64 `json:"endpoints_failing"`
	EndpointsDisabled int64 `json:"endpoints_disabled"`
	Pending           int64 `json:"pending"`
	Parked            int64 `json:"parked"`
}

type etlJobJSON struct {
	ID            int64      `json:"id"`
	UserID        uuid.UUID  `json:"user_id"`
	Provider      string     `json:"provider"`
	Tier          string     `json:"tier"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	Error         *string    `json:"error"`
	CreatedAt     time.Time  `json:"created_at"`
	NextAttemptAt *time.Time `json:"next_attempt_at"`
}

func toETLJobJSON(j queue.ETLJobInfo) etlJobJSON {
	out := etlJobJSON{ID: j.ID, UserID: j.UserID, Provider: j.Provider, Tier: j.Tier, Status: j.Status,
		Attempts: j.Attempts, Error: j.LastError, CreatedAt: j.CreatedAt}
	if j.Status == "pending" {
		nb := j.NotBefore
		out.NextAttemptAt = &nb
	}
	return out
}

// etlStatuses are the job statuses GET /v1/pipeline/etl-jobs lists. Done
// jobs are history the coverage report already sums up.
var etlStatuses = []string{"pending", "running", "parked"}

func (s *server) mountPipeline(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/pipeline", s.getPipeline)
	s.tenantRoute(mux, "GET /v1/pipeline/sync-runs", s.listTenantSyncRuns)
	s.tenantRoute(mux, "GET /v1/pipeline/etl-jobs", s.listETLJobs)
	s.tenantRoute(mux, "POST /v1/pipeline/etl-jobs/retry", s.retryParkedETLJobs)
	s.tenantRoute(mux, "POST /v1/pipeline/etl-jobs/{job_id}/retry", s.retryETLJob)
}

func (s *server) getPipeline(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	out, err := s.pipeline(r.Context(), p.TenantID)
	if err != nil {
		internalError(w, r, "api: pipeline", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// pipeline counts the tenant's work at each stage. Every count is of the
// tenant's own rows; the providers enabled and their rate limit pauses are
// instance facts every tenant sees through its sync runs already.
func (s *server) pipeline(ctx context.Context, tenantID uuid.UUID) (pipelineJSON, error) {
	db := s.d.Pool
	out := pipelineJSON{GeneratedAt: time.Now().UTC()}
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'active'), count(*) FILTER (WHERE status = 'deleting')
		FROM ohs_core.end_users WHERE tenant_id = $1`, tenantID).Scan(&out.Users.Active, &out.Users.Deleting); err != nil {
		return out, fmt.Errorf("users: %w", err)
	}

	sources := map[string]*pipelineSource{}
	source := func(slug string) *pipelineSource {
		if src, ok := sources[slug]; ok {
			return src
		}
		src := &pipelineSource{Slug: slug, Name: collect.ProviderName(slug), Kinds: []string{}, Connections: map[string]int64{}}
		sources[slug] = src
		return src
	}
	for _, c := range s.enabled() {
		src := source(c.Slug())
		src.Enabled, src.Kinds = true, kinds(c)
	}
	rows, err := db.Query(ctx, `SELECT provider, status, count(*), count(*) FILTER (WHERE broken_reason IS NOT NULL)
		FROM ohs_sync.connections WHERE tenant_id = $1 GROUP BY provider, status`, tenantID)
	if err != nil {
		return out, fmt.Errorf("connections: %w", err)
	}
	for rows.Next() {
		var provider, status string
		var n, broken int64
		if err := rows.Scan(&provider, &status, &n, &broken); err != nil {
			rows.Close()
			return out, fmt.Errorf("connections: %w", err)
		}
		src := source(provider)
		src.Connections[status] = n
		if status == "active" {
			src.Broken += broken
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("connections: %w", err)
	}
	rows, err = db.Query(ctx, `SELECT provider, paused_until FROM ohs_core.provider_limits WHERE paused_until > now()`)
	if err != nil {
		return out, fmt.Errorf("provider limits: %w", err)
	}
	for rows.Next() {
		var provider string
		var until time.Time
		if err := rows.Scan(&provider, &until); err != nil {
			rows.Close()
			return out, fmt.Errorf("provider limits: %w", err)
		}
		if src, ok := sources[provider]; ok {
			u := until.UTC()
			src.PausedUntil = &u
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("provider limits: %w", err)
	}
	out.Sources = make([]pipelineSource, 0, len(sources))
	for _, slug := range sortedSlugs(sources) {
		out.Sources = append(out.Sources, *sources[slug])
	}

	// The 24 hour counts read the finished runs through their created_at
	// index; the open ones are few.
	if err := db.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE status = 'pending' AND coalesce(outcome, '') NOT IN ($2, $3)),
			count(*) FILTER (WHERE status = 'running'),
			count(*) FILTER (WHERE status = 'pending' AND outcome = $2),
			count(*) FILTER (WHERE status = 'pending' AND outcome = $3)
		FROM ohs_sync.pull_jobs WHERE tenant_id = $1 AND status IN ('pending', 'running')`,
		tenantID, queue.OutcomeFailed, queue.OutcomeRateLimited).
		Scan(&out.Sync.Queued, &out.Sync.Running, &out.Sync.Retrying, &out.Sync.RateLimited); err != nil {
		return out, fmt.Errorf("sync runs: %w", err)
	}
	if err := db.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE status = 'parked' AND coalesce(outcome, '') <> $2),
			count(*) FILTER (WHERE status = 'done')
		FROM ohs_sync.pull_jobs
		WHERE status IN ('done', 'parked') AND created_at >= now() - interval '24 hours' AND tenant_id = $1`,
		tenantID, queue.OutcomeInactive).Scan(&out.Sync.Failed24h, &out.Sync.Succeeded24h); err != nil {
		return out, fmt.Errorf("sync runs: %w", err)
	}

	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'pending'), count(*) FILTER (WHERE status = 'running'),
			count(*) FILTER (WHERE status = 'parked')
		FROM ohs_sync.etl_jobs WHERE tenant_id = $1 AND status IN ('pending', 'running', 'parked')`, tenantID).
		Scan(&out.Processing.Pending, &out.Processing.Running, &out.Processing.Parked); err != nil {
		return out, fmt.Errorf("etl jobs: %w", err)
	}
	// As in the coverage report: one per rejected line, a job-level row
	// counting only when its object has no line rows.
	if err := db.QueryRow(ctx, `SELECT count(*) FROM (
			SELECT DISTINCT f.raw_key, f.line_number FROM ohs_data.failed_records f
			WHERE f.tenant_id = $1 AND f.status <> 'recovered'
			  AND (f.line_number IS NOT NULL OR NOT EXISTS (
				SELECT 1 FROM ohs_data.failed_records l
				WHERE l.tenant_id = f.tenant_id AND l.user_id = f.user_id AND l.raw_key = f.raw_key
				  AND l.line_number IS NOT NULL AND l.status <> 'recovered'))
		) rejected`, tenantID).Scan(&out.Processing.RejectedRecords); err != nil {
		return out, fmt.Errorf("failed records: %w", err)
	}

	if err := db.QueryRow(ctx, `SELECT count(*), coalesce(sum(size_bytes), 0)::bigint FROM ohs_sync.raw_objects
		WHERE created_at >= now() - interval '24 hours' AND tenant_id = $1`, tenantID).
		Scan(&out.Storage.RawObjects24h, &out.Storage.RawBytes24h); err != nil {
		return out, fmt.Errorf("raw objects: %w", err)
	}

	if err := db.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status = 'active' AND failing_since IS NOT NULL),
			count(*) FILTER (WHERE status = 'disabled')
		FROM ohs_core.webhook_endpoints WHERE tenant_id = $1`, tenantID).
		Scan(&out.Delivery.Endpoints, &out.Delivery.EndpointsFailing, &out.Delivery.EndpointsDisabled); err != nil {
		return out, fmt.Errorf("webhook endpoints: %w", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('pending', 'running')),
			count(*) FILTER (WHERE status = 'parked')
		FROM ohs_core.webhook_deliveries WHERE tenant_id = $1 AND status <> 'done'`, tenantID).
		Scan(&out.Delivery.Pending, &out.Delivery.Parked); err != nil {
		return out, fmt.Errorf("webhook deliveries: %w", err)
	}
	return out, nil
}

func sortedSlugs[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// pageQuery reads cursor (an id this API returned) and limit (1 to 100,
// default 50) of the pipeline lists.
func pageQuery(r *http.Request) (before int64, limit int, err error) {
	q := r.URL.Query()
	limit = 50
	if v := q.Get("limit"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > 100 {
			return 0, 0, errors.New("limit must be an integer from 1 to 100")
		}
		limit = n
	}
	if v := q.Get("cursor"); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n <= 0 {
			return 0, 0, errors.New("cursor is not one this API returned")
		}
		before = n
	}
	return before, limit, nil
}

func (s *server) listTenantSyncRuns(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	before, limit, err := pageQuery(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && !collect.ValidRunStatus(status) {
		badRequest(w, "status must be one of queued, running, retrying, rate_limited, succeeded, failed, cancelled")
		return
	}
	list, err := collect.Deps{Pool: s.d.Pool}.TenantSyncRuns(r.Context(), p.TenantID, status,
		r.URL.Query().Get("provider"), before, limit)
	if err != nil {
		internalError(w, r, "api: tenant sync runs", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) listETLJobs(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	before, limit, err := pageQuery(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "parked"
	}
	if !slices.Contains(etlStatuses, status) {
		badRequest(w, "status must be one of pending, running, parked")
		return
	}
	jobs, err := queue.ListETL(r.Context(), s.d.Pool, p.TenantID, status, r.URL.Query().Get("provider"), before, limit)
	if err != nil {
		internalError(w, r, "api: list etl jobs", err)
		return
	}
	next := ""
	if len(jobs) > limit {
		jobs = jobs[:limit]
		next = strconv.FormatInt(jobs[limit-1].ID, 10)
	}
	out := make([]etlJobJSON, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, toETLJobJSON(j))
	}
	writeJSON(w, http.StatusOK, newPage(out, next))
}

func (s *server) retryETLJob(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	id, err := strconv.ParseInt(r.PathValue("job_id"), 10, 64)
	if err != nil || id <= 0 {
		notFound(w, "job")
		return
	}
	j, err := queue.RetryETL(r.Context(), s.d.Pool, p.TenantID, id)
	switch {
	case errors.Is(err, queue.ErrNoJob):
		notFound(w, "job")
		return
	case errors.Is(err, queue.ErrNotParked):
		problem(w, http.StatusConflict, "only a parked job can be retried; this one is pending, running or done")
		return
	case err != nil:
		internalError(w, r, "api: retry etl job", err)
		return
	}
	writeJSON(w, http.StatusOK, toETLJobJSON(j))
}

func (s *server) retryParkedETLJobs(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	var req struct {
		Provider string `json:"provider"`
	}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	n, err := queue.RetryParkedETL(r.Context(), s.d.Pool, p.TenantID, req.Provider)
	if err != nil {
		internalError(w, r, "api: retry parked etl jobs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"retried": n})
}
