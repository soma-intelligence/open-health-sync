// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// Sync run statuses, derived from the pull job.
const (
	RunQueued      = "queued"       // waiting for a worker
	RunRunning     = "running"      // a worker is pulling
	RunRetrying    = "retrying"     // the last attempt failed, another is due at next_attempt_at
	RunRateLimited = "rate_limited" // the provider asked to wait until next_attempt_at
	RunSucceeded   = "succeeded"    // pulled; the records follow through the ETL
	RunFailed      = "failed"       // gave up: attempts spent, or the grant was refused
	RunCancelled   = "cancelled"    // the connection was disconnected or the user deleted
)

// RunRetention is how long finished sync runs stay in the history.
const RunRetention = 30 * 24 * time.Hour

// ManualSyncMax is the longest window a manual sync may ask for.
const ManualSyncMax = BackfillWindow

// manualSyncDefault is how far back a manual sync reaches for a connection
// that never synced (its backfill covers the rest).
const manualSyncDefault = 24 * time.Hour

// SyncRun is one pull of one connection: what started it, its window, how it
// went. A run is retried on its own; attempts counts the failed tries.
type SyncRun struct {
	ID       int64  `json:"id"`
	Provider string `json:"provider"`
	// Trigger is what started it: backfill, scheduled, notification or manual.
	Trigger string `json:"trigger"`
	Status  string `json:"status"`
	// DataType is set when only that provider data type is pulled (a
	// notification named it).
	DataType    *string   `json:"data_type"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	Attempts    int       `json:"attempts"`
	// ObjectsStored is the number of raw provider payloads the last attempt
	// stored. Records are written from them by the ETL; data.written
	// webhooks announce them.
	ObjectsStored *int `json:"objects_stored"`
	// Error is the provider error of the last failed attempt, made safe to
	// show (no tokens, no URLs with queries, no health values).
	Error         *string    `json:"error"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	NextAttemptAt *time.Time `json:"next_attempt_at"`
}

// SyncRunList is a page of sync runs, newest first.
type SyncRunList struct {
	Data       []SyncRun `json:"data"`
	NextCursor *string   `json:"next_cursor"`
}

const runCols = `id, provider, trigger, status, outcome, data_type, from_time, to_time, attempts,
	objects_stored, last_error, created_at, coalesce(started_at, claimed_at), finished_at, not_before`

// scanRun reads the runCols of row, then the extra columns that follow them.
func scanRun(row pgx.Row, extra ...any) (SyncRun, error) {
	var r SyncRun
	var status string
	var outcome *string
	var notBefore time.Time
	if err := row.Scan(append([]any{&r.ID, &r.Provider, &r.Trigger, &status, &outcome, &r.DataType, &r.WindowStart,
		&r.WindowEnd, &r.Attempts, &r.ObjectsStored, &r.Error, &r.CreatedAt, &r.StartedAt, &r.FinishedAt,
		&notBefore}, extra...)...); err != nil {
		return SyncRun{}, err
	}
	r.WindowStart, r.WindowEnd, r.CreatedAt = r.WindowStart.UTC(), r.WindowEnd.UTC(), r.CreatedAt.UTC()
	r.StartedAt, r.FinishedAt = utcPtr(r.StartedAt), utcPtr(r.FinishedAt)
	r.Status = runStatus(status, outcome)
	switch r.Status {
	case RunQueued, RunRetrying, RunRateLimited:
		nb := notBefore.UTC()
		r.NextAttemptAt = &nb
	case RunSucceeded, RunRunning:
		r.Error = nil
	}
	if r.Status == RunRateLimited {
		msg := "the provider asked to slow down; the pull resumes at next_attempt_at"
		r.Error = &msg
	}
	return r, nil
}

// runStatus maps a pull job's status and outcome to a run status.
func runStatus(status string, outcome *string) string {
	o := ""
	if outcome != nil {
		o = *outcome
	}
	switch status {
	case "running":
		return RunRunning
	case "done":
		return RunSucceeded
	case "parked":
		if o == queue.OutcomeInactive {
			// The connection stopped (disconnected, refused elsewhere) or
			// the user is being deleted.
			return RunCancelled
		}
		return RunFailed
	}
	switch o {
	case queue.OutcomeRateLimited:
		return RunRateLimited
	case queue.OutcomeFailed:
		return RunRetrying
	}
	return RunQueued
}

// SyncRuns lists the sync runs of a user, newest first, optionally of one
// provider. before (an id) continues a page; limit is 1 to 100.
func (d Deps) SyncRuns(ctx context.Context, tenantID, userID uuid.UUID, provider string, before int64, limit int) (SyncRunList, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+runCols+` FROM ohs_sync.pull_jobs
		WHERE tenant_id = $1 AND user_id = $2 AND ($3 = '' OR provider = $3) AND ($4 = 0 OR id < $4)
		ORDER BY id DESC LIMIT $5`, tenantID, userID, provider, before, limit+1)
	if err != nil {
		return SyncRunList{}, fmt.Errorf("sync runs: %w", err)
	}
	runs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (SyncRun, error) { return scanRun(r) })
	if err != nil {
		return SyncRunList{}, fmt.Errorf("sync runs: %w", err)
	}
	out := SyncRunList{Data: runs}
	if len(runs) > limit {
		out.Data = runs[:limit]
		c := strconv.FormatInt(runs[limit-1].ID, 10)
		out.NextCursor = &c
	}
	if out.Data == nil {
		out.Data = []SyncRun{}
	}
	return out, nil
}

// TenantSyncRun is a sync run with the user it belongs to.
type TenantSyncRun struct {
	UserID uuid.UUID `json:"user_id"`
	SyncRun
}

// TenantSyncRunList is a page of a tenant's sync runs, newest first.
type TenantSyncRunList struct {
	Data       []TenantSyncRun `json:"data"`
	NextCursor *string         `json:"next_cursor"`
}

// runStatusSQL is the condition on a pull job for each run status.
var runStatusSQL = map[string]string{
	RunQueued: `status = 'pending' AND coalesce(outcome, '') NOT IN ('` + queue.OutcomeFailed + `', '` +
		queue.OutcomeRateLimited + `')`,
	RunRetrying:    `status = 'pending' AND outcome = '` + queue.OutcomeFailed + `'`,
	RunRateLimited: `status = 'pending' AND outcome = '` + queue.OutcomeRateLimited + `'`,
	RunRunning:     `status = 'running'`,
	RunSucceeded:   `status = 'done'`,
	RunFailed:      `status = 'parked' AND coalesce(outcome, '') <> '` + queue.OutcomeInactive + `'`,
	RunCancelled:   `status = 'parked' AND outcome = '` + queue.OutcomeInactive + `'`,
}

// ValidRunStatus reports whether status names a run status.
func ValidRunStatus(status string) bool {
	_, ok := runStatusSQL[status]
	return ok
}

// TenantSyncRuns lists the sync runs of every user of a tenant, newest
// first, optionally of one status (ValidRunStatus) and one provider. before
// (an id) continues a page; limit is 1 to 100.
func (d Deps) TenantSyncRuns(ctx context.Context, tenantID uuid.UUID, status, provider string, before int64, limit int) (TenantSyncRunList, error) {
	cond := "true"
	if status != "" {
		c, ok := runStatusSQL[status]
		if !ok {
			return TenantSyncRunList{}, fmt.Errorf("sync runs: unknown status %q", status)
		}
		cond = c
	}
	rows, err := d.Pool.Query(ctx, `SELECT `+runCols+`, user_id FROM ohs_sync.pull_jobs
		WHERE tenant_id = $1 AND (`+cond+`) AND ($2 = '' OR provider = $2) AND ($3 = 0 OR id < $3)
		ORDER BY id DESC LIMIT $4`, tenantID, provider, before, limit+1)
	if err != nil {
		return TenantSyncRunList{}, fmt.Errorf("sync runs: %w", err)
	}
	runs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (TenantSyncRun, error) {
		var t TenantSyncRun
		var err error
		t.SyncRun, err = scanRun(r, &t.UserID)
		return t, err
	})
	if err != nil {
		return TenantSyncRunList{}, fmt.Errorf("sync runs: %w", err)
	}
	out := TenantSyncRunList{Data: runs}
	if len(runs) > limit {
		out.Data = runs[:limit]
		c := strconv.FormatInt(runs[limit-1].ID, 10)
		out.NextCursor = &c
	}
	if out.Data == nil {
		out.Data = []TenantSyncRun{}
	}
	return out, nil
}

// SyncRun returns one run of a user. pgx.ErrNoRows when there is none.
func (d Deps) SyncRun(ctx context.Context, tenantID, userID uuid.UUID, id int64) (SyncRun, error) {
	return scanRun(d.Pool.QueryRow(ctx, `SELECT `+runCols+` FROM ohs_sync.pull_jobs
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, tenantID, userID, id))
}

// pruneRuns deletes finished runs older than RunRetention, at most batch.
func pruneRuns(ctx context.Context, q execer, now time.Time, batch int) error {
	if _, err := q.Exec(ctx, `
		DELETE FROM ohs_sync.pull_jobs WHERE id IN (
			SELECT id FROM ohs_sync.pull_jobs
			WHERE status IN ('done', 'parked') AND created_at < $1
			LIMIT $2)`, now.Add(-RunRetention), batch); err != nil {
		return fmt.Errorf("prune sync runs: %w", err)
	}
	return nil
}

// ListSyncRuns serves GET /v1/users/{id}/sync-runs. Mount behind
// auth.Middleware with auth.TenantKey. Query: provider, limit (default 50,
// at most 100), cursor.
func (h *Handlers) ListSyncRuns() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID, userID, ok := h.caller(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		limit := 50
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 100 {
				writeBadRequest(w, "limit must be an integer from 1 to 100")
				return
			}
			limit = n
		}
		var before int64
		if v := q.Get("cursor"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				writeBadRequest(w, "cursor is not one this API returned")
				return
			}
			before = n
		}
		list, err := h.d.SyncRuns(r.Context(), tenantID, userID, q.Get("provider"), before, limit)
		if err != nil {
			slog.ErrorContext(r.Context(), "collect: sync runs", "tenant_id", tenantID, "user_id", userID, "error", err)
			writeInternal(w)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
}

// GetSyncRun serves GET /v1/users/{id}/sync-runs/{run}. Mount behind
// auth.Middleware with auth.TenantKey.
func (h *Handlers) GetSyncRun() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID, userID, ok := h.caller(w, r)
		if !ok {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("run"), 10, 64)
		if err != nil || id <= 0 {
			writeNotFound(w, "sync run not found")
			return
		}
		run, err := h.d.SyncRun(r.Context(), tenantID, userID, id)
		if errors.Is(err, pgx.ErrNoRows) {
			writeNotFound(w, "sync run not found")
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "collect: sync run", "tenant_id", tenantID, "user_id", userID, "error", err)
			writeInternal(w)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})
}

// SyncRequest is the optional body of a manual sync.
type SyncRequest struct {
	// From starts the window; it ends now. Default: where the connection's
	// data ends (minus an hour of overlap), or a day back for a connection
	// that never synced. At most ManualSyncMax back.
	From *time.Time `json:"from"`
}

// SyncNow serves POST /v1/users/{id}/connections/{provider}/sync. Mount
// behind auth.Middleware with auth.TenantKey. It queues a pull of the
// connection ahead of routine work and answers 202 with the run. While a
// manual run of the connection is still queued or running, it answers 200
// with that run instead of queueing another. 409 when the connection does not
// sync (needs_reauth, disconnected) or its provider is not enabled here, or
// is turned off for the tenant.
func (h *Handlers) SyncNow() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID, userID, ok := h.caller(w, r)
		if !ok {
			return
		}
		provider := r.PathValue("provider")
		var req SyncRequest
		if r.ContentLength != 0 {
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil {
				writeBadRequest(w, "the body must be a JSON object with an optional from (RFC 3339)")
				return
			}
		}
		ctx := r.Context()
		run, created, err := h.d.syncNow(ctx, tenantID, userID, provider, req)
		var pe *syncProblem
		switch {
		case errors.As(err, &pe):
			auth.WriteProblem(w, pe.status, http.StatusText(pe.status), pe.detail)
			return
		case err != nil:
			slog.ErrorContext(ctx, "collect: sync now", "tenant_id", tenantID, "user_id", userID, "provider", provider, "error", err)
			writeInternal(w)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/v1/users/%s/sync-runs/%d", userID, run.ID))
		code := http.StatusOK
		if created {
			code = http.StatusAccepted
		}
		writeJSON(w, code, run)
	})
}

// syncProblem is a refusal of a manual sync the caller can act on.
type syncProblem struct {
	status int
	detail string
}

func (p *syncProblem) Error() string { return p.detail }

func (d Deps) syncNow(ctx context.Context, tenantID, userID uuid.UUID, provider string, req SyncRequest) (SyncRun, bool, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return SyncRun{}, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockActiveUser(ctx, tx, tenantID, userID); err != nil {
		if errors.Is(err, errInactive) {
			return SyncRun{}, false, &syncProblem{http.StatusNotFound, "user not found"}
		}
		return SyncRun{}, false, err
	}
	var status string
	var syncedThrough *time.Time
	err = tx.QueryRow(ctx, `SELECT status, synced_through FROM ohs_sync.connections
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 FOR SHARE`,
		tenantID, userID, provider).Scan(&status, &syncedThrough)
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncRun{}, false, &syncProblem{http.StatusNotFound, "no connection to this provider"}
	}
	if err != nil {
		return SyncRun{}, false, fmt.Errorf("load connection: %w", err)
	}
	switch status {
	case StatusNeedsReauth:
		return SyncRun{}, false, &syncProblem{http.StatusConflict,
			"the provider refused this connection's grant: connect the user again (see the connection's health)"}
	case StatusDisconnected:
		return SyncRun{}, false, &syncProblem{http.StatusConflict, "the connection is disconnected: connect the user again"}
	}
	if _, ok := d.oauth(provider); !ok {
		return SyncRun{}, false, &syncProblem{http.StatusConflict, "this provider is not enabled on this instance"}
	}
	if !d.allowed(tenantID, provider) {
		return SyncRun{}, false, &syncProblem{http.StatusConflict, ProviderOffDetail}
	}

	now := d.now().UTC().Truncate(time.Second)
	from := now.Add(-manualSyncDefault)
	if syncedThrough != nil {
		from = syncedThrough.Add(-pullOverlap)
	}
	if req.From != nil {
		from = *req.From
	}
	if !from.Before(now) {
		if req.From != nil {
			return SyncRun{}, false, &syncProblem{http.StatusBadRequest, "from must be in the past"}
		}
		from = now.Add(-pullOverlap)
	}
	if now.Sub(from) > ManualSyncMax {
		if req.From != nil {
			return SyncRun{}, false, &syncProblem{http.StatusBadRequest,
				fmt.Sprintf("from may reach at most %d days back", int(ManualSyncMax/(24*time.Hour)))}
		}
		from = now.Add(-maxCatchUp)
	}
	// One manual run at a time per connection.
	var existing int64
	err = tx.QueryRow(ctx, `SELECT id FROM ohs_sync.pull_jobs
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND trigger = 'manual'
		  AND status IN ('pending', 'running')
		ORDER BY id DESC LIMIT 1`, tenantID, userID, provider).Scan(&existing)
	switch {
	case err == nil:
		run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runCols+` FROM ohs_sync.pull_jobs WHERE id = $1`, existing))
		return run, false, err
	case !errors.Is(err, pgx.ErrNoRows):
		return SyncRun{}, false, fmt.Errorf("find manual run: %w", err)
	}

	id, created, err := queue.EnqueuePullID(ctx, tx, queue.PullJob{
		TenantID: tenantID, UserID: userID, Provider: provider, From: from.UTC(), To: now,
		Inbound: true, Trigger: queue.TriggerManual,
	})
	if err != nil {
		return SyncRun{}, false, err
	}
	run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runCols+` FROM ohs_sync.pull_jobs WHERE id = $1`, id))
	if err != nil {
		return SyncRun{}, false, fmt.Errorf("read run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return SyncRun{}, false, fmt.Errorf("commit: %w", err)
	}
	return run, created, nil
}
