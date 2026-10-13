// SPDX-License-Identifier: AGPL-3.0-only

// Package deletion hard-deletes an end user, or the data an end user holds
// from one provider: the HTTP requests that start it and the worker that
// finishes it.
package deletion

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
)

// Deps is what the delete handlers need.
type Deps struct {
	Pool *pgxpool.Pool
	// KnownProvider reports whether a slug names a connector of this build.
	// The provider data route answers 404 for any other slug. Nil accepts
	// every well-formed slug.
	KnownProvider func(slug string) bool
}

// NewDeleteHandler serves DELETE /v1/users/{id}. It expects to run behind
// auth.Middleware with auth.TenantKey and a mux that sets the "id" path value.
//
// In one transaction it marks the user deleting (tokens stop working, so
// uploads get 401, reads 404), deletes the stored idempotent responses that
// name the user and queues the deletion job. It answers 202 with the
// job id. Asking again while the job is unfinished returns the same job; an
// unknown user, another tenant's user and a malformed id are 404.
func NewDeleteHandler(d Deps) http.Handler {
	if d.Pool == nil {
		panic("deletion: NewDeleteHandler needs Pool")
	}
	return &deleteHandler{d: d}
}

type deleteHandler struct{ d Deps }

func (h *deleteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	ctx := r.Context()
	jobID, found, err := h.request(ctx, p.TenantID, userID)
	if err != nil {
		slog.ErrorContext(ctx, "deletion: request failed", "tenant_id", p.TenantID, "user_id", userID, "error", err)
		auth.WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "the deletion could not be started, retry later")
		return
	}
	if !found {
		auth.WriteProblem(w, http.StatusNotFound, "Not Found", "no such user")
		return
	}
	slog.InfoContext(ctx, "deletion: requested", "tenant_id", p.TenantID, "user_id", userID, "job_id", jobID)
	WriteAccepted(w, jobID)
}

func (h *deleteHandler) request(ctx context.Context, tenantID, userID uuid.UUID) (uuid.UUID, bool, error) {
	tx, err := h.d.Pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// The update locks the row, so concurrent requests for one user serialize.
	tag, err := tx.Exec(ctx,
		`UPDATE ohs_core.end_users SET status = 'deleting' WHERE tenant_id = $1 AND id = $2`, tenantID, userID)
	if err != nil {
		return uuid.Nil, false, err
	}
	if tag.RowsAffected() == 0 {
		return uuid.Nil, false, nil
	}
	// A stored POST /v1/users response would replay the user's id and
	// external_ref after the deletion: it goes now.
	if _, err := tx.Exec(ctx,
		`DELETE FROM ohs_core.idempotency_keys WHERE tenant_id = $1 AND user_id = $2`, tenantID, userID); err != nil {
		return uuid.Nil, false, err
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id FROM ohs_core.deletion_jobs
		WHERE tenant_id = $1 AND user_id = $2 AND provider IS NULL AND status IN ('pending', 'running')
		ORDER BY created_at LIMIT 1`, tenantID, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx,
			`INSERT INTO ohs_core.deletion_jobs (tenant_id, user_id) VALUES ($1, $2) RETURNING id`,
			tenantID, userID).Scan(&id)
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}
