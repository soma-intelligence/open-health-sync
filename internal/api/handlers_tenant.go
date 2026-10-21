// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

type tenantJSON struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Admin     bool      `json:"admin"`
	CreatedAt time.Time `json:"created_at"`
	// Key is the API key making the call.
	Key keyRefJSON `json:"key"`
}

type keyRefJSON struct {
	ID     uuid.UUID `json:"id"`
	Prefix string    `json:"prefix"`
}

type apiKeyJSON struct {
	ID         uuid.UUID  `json:"id"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	// Current is true for the key making the call.
	Current bool `json:"current"`
}

type createdKeyJSON struct {
	apiKeyJSON
	// Key is the whole secret, returned this once.
	Key string `json:"key"`
}

func toAPIKeyJSON(k tenant.APIKey, current string) apiKeyJSON {
	return apiKeyJSON{ID: k.ID, Prefix: k.Prefix, CreatedAt: k.CreatedAt.UTC(), LastUsedAt: utcPtr(k.LastUsedAt),
		RevokedAt: utcPtr(k.RevokedAt), Current: k.ID.String() == current}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// mountTenant serves the tenant of the key and its API keys. A tenant key
// manages its own tenant's keys, as `ohs admin key` does from a shell.
func (s *server) mountTenant(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/tenant", s.getTenant)
	s.tenantRoute(mux, "GET /v1/keys", s.listKeys)
	s.tenantRoute(mux, "POST /v1/keys", s.createKey)
	s.tenantRoute(mux, "DELETE /v1/keys/{key_id}", s.revokeKey)
}

func (s *server) getTenant(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	t, err := s.d.Store.GetTenant(r.Context(), p.TenantID)
	if err != nil {
		internalError(w, r, "api: get tenant", err)
		return
	}
	keys, err := s.d.Store.ListKeys(r.Context(), p.TenantID)
	if err != nil {
		internalError(w, r, "api: get tenant", err)
		return
	}
	out := tenantJSON{ID: t.ID, Name: t.Name, Admin: t.Admin, CreatedAt: t.CreatedAt.UTC()}
	for _, k := range keys {
		if k.ID.String() == p.KeyID {
			out.Key = keyRefJSON{ID: k.ID, Prefix: k.Prefix}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) listKeys(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	keys, err := s.d.Store.ListKeys(r.Context(), p.TenantID)
	if err != nil {
		internalError(w, r, "api: list keys", err)
		return
	}
	out := make([]apiKeyJSON, 0, len(keys))
	for _, k := range keys {
		out = append(out, toAPIKeyJSON(k, p.KeyID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *server) createKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	var req struct{}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	full, k, err := s.d.Store.CreateKeyInfo(r.Context(), p.TenantID)
	if err != nil {
		internalError(w, r, "api: create key", err)
		return
	}
	// The secret is in this answer only: nothing on the way may keep it.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, createdKeyJSON{apiKeyJSON: toAPIKeyJSON(k, p.KeyID), Key: full})
}

func (s *server) revokeKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	id, err := uuid.Parse(r.PathValue("key_id"))
	if err != nil {
		notFound(w, "API key")
		return
	}
	if id.String() == p.KeyID {
		problem(w, http.StatusConflict, "this is the key making the call: revoke it with another key of the tenant")
		return
	}
	err = s.d.Store.RevokeKeyByID(r.Context(), p.TenantID, id)
	switch {
	case errors.Is(err, tenant.ErrNotFound):
		notFound(w, "API key")
		return
	case err != nil:
		internalError(w, r, "api: revoke key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
