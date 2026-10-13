// SPDX-License-Identifier: AGPL-3.0-only

package deletion

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
)

// providerSlug is the form of a connector slug. It is also a raw key segment
// (raw/<tenant>/<user>/<provider>/...), so it never holds a "/" or a dot.
var providerSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidProvider reports whether s has the form of a connector slug.
func ValidProvider(s string) bool { return providerSlug.MatchString(s) }

// Querier is QueryRow of a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Pending reports whether the erasure of the user's data from provider is
// queued or running. While it is, nothing new of that provider is stored for
// the user and the user cannot connect the provider again.
func Pending(ctx context.Context, q Querier, tenantID, userID uuid.UUID, provider string) (bool, error) {
	var pending bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ohs_core.deletion_jobs
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status IN ('pending', 'running'))`,
		tenantID, userID, provider).Scan(&pending)
	return pending, err
}

// QueueProvider queues, inside the caller's transaction, the erasure of
// everything the user holds from provider, and returns the job id. An
// unfinished job for the same user and provider is returned instead of a
// second one. The caller has checked the user is active and the provider slug
// is valid.
func QueueProvider(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, provider string) (uuid.UUID, error) {
	if !ValidProvider(provider) {
		return uuid.Nil, errors.New("deletion: invalid provider slug")
	}
	// Two concurrent requests for one user and provider queue one job.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"ohs_deletion|"+tenantID.String()+"|"+userID.String()+"|"+provider); err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM ohs_core.deletion_jobs
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status IN ('pending', 'running')
		ORDER BY created_at LIMIT 1`, tenantID, userID, provider).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx,
			`INSERT INTO ohs_core.deletion_jobs (tenant_id, user_id, provider) VALUES ($1, $2, $3) RETURNING id`,
			tenantID, userID, provider).Scan(&id)
	}
	return id, err
}

// NewProviderDeleteHandler serves DELETE /v1/users/{id}/data/{provider}. It
// expects to run behind auth.Middleware with auth.TenantKey and a mux that
// sets the "id" and "provider" path values.
//
// It queues the erasure of the data the user holds from one provider and
// answers 202 with the job id, without touching any connection: it is the
// route for device providers (no connection) and for a provider already
// disconnected. A provider the user is still connected to (active or
// needs_reauth) is a 409: disconnect it with delete_data=true instead, so no
// pull brings the data back. Asking again while the job is unfinished returns
// the same job. An unknown user, another tenant's user, a user being deleted
// and an unknown provider are 404.
func NewProviderDeleteHandler(d Deps) http.Handler {
	if d.Pool == nil {
		panic("deletion: NewProviderDeleteHandler needs Pool")
	}
	return &providerHandler{d: d}
}

type providerHandler struct{ d Deps }

var (
	errNoUser    = errors.New("no such user")
	errConnected = errors.New("connected")
)

func (h *providerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.From(r.Context())
	if !ok || p.UserID != nil {
		auth.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "a tenant API key is required")
		return
	}
	userID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		auth.WriteProblem(w, http.StatusNotFound, "Not Found", "no such user")
		return
	}
	provider := r.PathValue("provider")
	if !ValidProvider(provider) || (h.d.KnownProvider != nil && !h.d.KnownProvider(provider)) {
		auth.WriteProblem(w, http.StatusNotFound, "Not Found", "no such provider")
		return
	}
	ctx := r.Context()
	jobID, err := h.request(ctx, p.TenantID, userID, provider)
	switch {
	case errors.Is(err, errNoUser):
		auth.WriteProblem(w, http.StatusNotFound, "Not Found", "no such user")
		return
	case errors.Is(err, errConnected):
		auth.WriteProblem(w, http.StatusConflict, "Conflict",
			"the user is still connected to this provider: disconnect it with DELETE /v1/users/{id}/connections/{provider}?delete_data=true")
		return
	case err != nil:
		slog.ErrorContext(ctx, "deletion: provider request failed", "tenant_id", p.TenantID, "user_id", userID, "provider", provider, "error", err)
		auth.WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "the deletion could not be started, retry later")
		return
	}
	slog.InfoContext(ctx, "deletion: provider data deletion requested", "tenant_id", p.TenantID, "user_id", userID, "provider", provider, "job_id", jobID)
	WriteAccepted(w, jobID)
}

func (h *providerHandler) request(ctx context.Context, tenantID, userID uuid.UUID, provider string) (uuid.UUID, error) {
	tx, err := h.d.Pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR SHARE`,
		tenantID, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "active") {
		return uuid.Nil, errNoUser
	}
	if err != nil {
		return uuid.Nil, err
	}
	// The connection row stays locked until the job is queued, so a
	// reconnect cannot slip in between.
	err = tx.QueryRow(ctx, `SELECT status FROM ohs_sync.connections
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 FOR UPDATE`,
		tenantID, userID, provider).Scan(&status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return uuid.Nil, err
	case status != "disconnected":
		return uuid.Nil, errConnected
	}
	id, err := QueueProvider(ctx, tx, tenantID, userID, provider)
	if err != nil {
		return uuid.Nil, err
	}
	return id, tx.Commit(ctx)
}

// WriteAccepted answers 202 {"job_id": ...}.
func WriteAccepted(w http.ResponseWriter, jobID uuid.UUID) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"job_id": jobID.String()})
}
