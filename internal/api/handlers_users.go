// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

type userJSON struct {
	ID          uuid.UUID `json:"id"`
	ExternalRef *string   `json:"external_ref"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	// Timezone is the user's IANA time zone, null when none is known, and
	// TimezoneSource where it came from: "explicit" (set through this API)
	// or "learnt" (taken from a device upload).
	Timezone       *string `json:"timezone"`
	TimezoneSource *string `json:"timezone_source"`
}

func toUserJSON(u tenant.User) userJSON {
	out := userJSON{ID: u.ID, Status: u.Status, CreatedAt: u.CreatedAt.UTC()}
	if u.ExternalRef != "" {
		ref := u.ExternalRef
		out.ExternalRef = &ref
	}
	if u.Timezone != "" {
		zone, source := u.Timezone, u.TimezoneSource
		out.Timezone, out.TimezoneSource = &zone, &source
	}
	return out
}

type createUserRequest struct {
	ExternalRef string `json:"external_ref"`
	// Timezone is an IANA name; null or absent creates the user without one.
	Timezone *string `json:"timezone"`
}

// updateUserRequest is the body of PATCH /v1/users/{id}. timezone is the
// only field: an IANA name sets the user's zone, null clears it.
type updateUserRequest struct {
	Timezone json.RawMessage `json:"timezone"`
}

// invalidTimezone is the 400 of a time zone that is not an IANA name.
const invalidTimezone = "timezone: not an IANA time zone name such as Europe/Paris"

// maxExternalRef bounds a tenant's own user reference.
const maxExternalRef = 255

func (s *server) mountUsers(mux *http.ServeMux) {
	mux.Handle("POST /v1/users", auth.Middleware(s.lookup, auth.TenantKey)(s.idempotent(http.HandlerFunc(s.createUser))))
	s.tenantRoute(mux, "GET /v1/users", s.listUsers)
	s.tenantRoute(mux, "GET /v1/users/{id}", s.getUser)
	s.tenantRoute(mux, "PATCH /v1/users/{id}", s.updateUser)
}

// pathUser parses {id}. A malformed id is a user that does not exist.
func pathUser(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, bool) {
	p, _ := auth.From(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		notFound(w, "user")
		return p, uuid.Nil, false
	}
	return p, id, true
}

func (s *server) createUser(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	var req createUserRequest
	if !decodeJSON(w, r, &req, true) {
		return
	}
	if len(req.ExternalRef) > maxExternalRef {
		badRequest(w, "external_ref is at most 255 characters")
		return
	}
	zone := ""
	if req.Timezone != nil {
		if zone = *req.Timezone; !tenant.ValidTimezone(zone) {
			badRequest(w, invalidTimezone)
			return
		}
	}
	u, err := s.d.Store.CreateUserIn(r.Context(), p.TenantID, req.ExternalRef, zone)
	switch {
	case errors.Is(err, tenant.ErrInvalidTimezone):
		badRequest(w, invalidTimezone)
		return
	case errors.Is(err, tenant.ErrConflict):
		problem(w, http.StatusConflict, "a user with this external_ref already exists")
		return
	case err != nil:
		internalError(w, r, "api: create user", err)
		return
	}
	// A stored replay of this response names the user: it is deleted with them.
	tenant.SetIdempotencyUser(r.Context(), u.ID)
	w.Header().Set("Location", "/v1/users/"+u.ID.String())
	writeJSON(w, http.StatusCreated, toUserJSON(u))
}

func (s *server) listUsers(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	limit, err := parseLimit(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	users, next, err := s.d.Store.ListUsers(r.Context(), p.TenantID, tenant.UserFilter{ExternalRef: r.URL.Query().Get("external_ref")}, r.URL.Query().Get("cursor"), limit)
	switch {
	case errors.Is(err, tenant.ErrInvalidCursor):
		badRequest(w, "the cursor is not valid")
		return
	case err != nil:
		internalError(w, r, "api: list users", err)
		return
	}
	out := make([]userJSON, 0, len(users))
	for _, u := range users {
		out = append(out, toUserJSON(u))
	}
	writeJSON(w, http.StatusOK, newPage(out, next))
}

func (s *server) getUser(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathUser(w, r)
	if !ok {
		return
	}
	u, err := s.d.Store.GetActiveUser(r.Context(), p.TenantID, id)
	switch {
	case errors.Is(err, tenant.ErrNotFound):
		notFound(w, "user")
		return
	case err != nil:
		internalError(w, r, "api: get user", err)
		return
	}
	writeJSON(w, http.StatusOK, toUserJSON(u))
}

// updateUser sets or clears the user's time zone. The zone applies to what
// the ETL writes from then on; stored samples keep the day they were
// attributed to.
func (s *server) updateUser(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathUser(w, r)
	if !ok {
		return
	}
	var req updateUserRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.Timezone == nil {
		badRequest(w, "timezone is required: an IANA time zone name, or null to clear it")
		return
	}
	var zone *string
	if err := json.Unmarshal(req.Timezone, &zone); err != nil {
		badRequest(w, invalidTimezone)
		return
	}
	name := ""
	if zone != nil {
		if name = *zone; !tenant.ValidTimezone(name) {
			badRequest(w, invalidTimezone)
			return
		}
	}
	u, err := s.d.Store.SetUserTimezone(r.Context(), p.TenantID, id, name)
	switch {
	case errors.Is(err, tenant.ErrInvalidTimezone):
		badRequest(w, invalidTimezone)
		return
	case errors.Is(err, tenant.ErrNotFound):
		notFound(w, "user")
		return
	case err != nil:
		internalError(w, r, "api: update user", err)
		return
	}
	writeJSON(w, http.StatusOK, toUserJSON(u))
}
