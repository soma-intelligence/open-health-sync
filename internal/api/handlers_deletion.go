// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/deletion"
)

// mountDeletion serves DELETE /v1/users/{id} and DELETE
// /v1/users/{id}/data/{provider}: 202 with the job id; the etl role's
// deletion worker finishes the job. GET /v1/deletions/{job_id} reads either
// kind of job back.
func (s *server) mountDeletion(mux *http.ServeMux) {
	tenantKey := auth.Middleware(s.lookup, auth.TenantKey)
	deps := deletion.Deps{Pool: s.d.Pool, KnownProvider: knownProvider}
	mux.Handle("DELETE /v1/users/{id}", tenantKey(deletion.NewDeleteHandler(deps)))
	mux.Handle("DELETE /v1/users/{id}/data/{provider}", tenantKey(deletion.NewProviderDeleteHandler(deps)))
	s.tenantRoute(mux, "GET /v1/deletions/{job_id}", s.getDeletion)
}

// knownProvider accepts every connector of this build, enabled on this
// instance or not: data collected before a provider was turned off can still
// be erased.
func knownProvider(slug string) bool {
	_, ok := connector.Get(slug)
	return ok
}

type deletionJob struct {
	JobID      uuid.UUID  `json:"job_id"`
	UserID     uuid.UUID  `json:"user_id"`
	Provider   *string    `json:"provider"` // null: the whole user
	Status     string     `json:"status"`   // pending, running, done or failed
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// getDeletion answers one deletion job of the caller's tenant. Another
// tenant's job, an unknown id and a malformed id are all 404.
func (s *server) getDeletion(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.From(r.Context())
	if !ok {
		notFound(w, "deletion job")
		return
	}
	id, err := uuid.Parse(r.PathValue("job_id"))
	if err != nil {
		notFound(w, "deletion job")
		return
	}
	var j deletionJob
	err = s.d.Pool.QueryRow(r.Context(), `SELECT id, user_id, provider, status, created_at, finished_at
		FROM ohs_core.deletion_jobs WHERE tenant_id = $1 AND id = $2`, p.TenantID, id).
		Scan(&j.JobID, &j.UserID, &j.Provider, &j.Status, &j.CreatedAt, &j.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, "deletion job")
		return
	}
	if err != nil {
		internalError(w, r, "api: get deletion job", err)
		return
	}
	j.CreatedAt = j.CreatedAt.UTC()
	if j.FinishedAt != nil {
		t := j.FinishedAt.UTC()
		j.FinishedAt = &t
	}
	writeJSON(w, http.StatusOK, j)
}
