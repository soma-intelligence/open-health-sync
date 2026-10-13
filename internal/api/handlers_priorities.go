// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/fusion"
)

func (s *server) mountSourcePriorities(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/users/{id}/source-priorities", s.getSourcePriorities)
	s.tenantRoute(mux, "PUT /v1/users/{id}/source-priorities", s.putSourcePriorities)
}

// sourcePriorities is the body of both routes: the user's own orders, and
// the built-in ones that apply where the user set none.
type sourcePriorities struct {
	Priorities fusion.Priorities `json:"priorities"`
	Defaults   fusion.Priorities `json:"defaults"`
}

type putSourcePrioritiesRequest struct {
	Priorities fusion.Priorities `json:"priorities"`
}

func (s *server) getSourcePriorities(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	if err := read.CheckUser(r.Context(), s.d.Pool, p.TenantID, user); err != nil {
		readError(w, r, err, "user")
		return
	}
	s.writeSourcePriorities(w, r, p.TenantID, user)
}

// putSourcePriorities replaces every order of the user; an empty object goes
// back to the defaults.
func (s *server) putSourcePriorities(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	var req putSourcePrioritiesRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.Priorities == nil {
		badRequest(w, "priorities is required (an empty object goes back to the defaults)")
		return
	}
	pr, err := req.Priorities.Normalize()
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if err := read.CheckUser(r.Context(), s.d.Pool, p.TenantID, user); err != nil {
		readError(w, r, err, "user")
		return
	}
	if err := fusion.SavePriorities(r.Context(), s.d.Pool, p.TenantID, user, pr); err != nil {
		internalError(w, r, "api: save source priorities", err)
		return
	}
	s.writeSourcePriorities(w, r, p.TenantID, user)
}

func (s *server) writeSourcePriorities(w http.ResponseWriter, r *http.Request, tenantID, user uuid.UUID) {
	pr, err := fusion.LoadPriorities(r.Context(), s.d.Pool, tenantID, user)
	if err != nil {
		internalError(w, r, "api: load source priorities", err)
		return
	}
	writeJSON(w, http.StatusOK, sourcePriorities{Priorities: pr, Defaults: fusion.Defaults()})
}

// mergePolicy sets q.Policy from the scorer when one is configured; without
// one the read layer loads the user's orders itself. A scorer that fails is
// logged and the read goes on with the orders alone.
func (s *server) mergePolicy(r *http.Request, tenantID, user uuid.UUID, q *read.Query) {
	if s.d.Scorer == nil || q.View == read.ViewAll {
		return
	}
	pol, err := fusion.Load(r.Context(), s.d.Pool, tenantID, user, s.d.Scorer)
	switch {
	case errors.Is(err, fusion.ErrScores):
		slog.WarnContext(r.Context(), "api: source scores unavailable", "error", err)
	case err != nil:
		return // the read layer loads the orders again and reports it
	}
	q.Policy = &pol
}
