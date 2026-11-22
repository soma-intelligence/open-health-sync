// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"
	"strings"
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
	// Scopes are what the key may do; ["*"] is everything.
	Scopes []string `json:"scopes"`
}

type apiKeyJSON struct {
	ID uuid.UUID `json:"id"`
	// Name says what the key is for; null for a key created without one.
	Name       *string    `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	// ExpiresAt is when the key stops working by itself; null means never.
	ExpiresAt *time.Time `json:"expires_at"`
	// Scopes are what the key may do; ["*"] is everything.
	Scopes []string `json:"scopes"`
	// Current is true for the key making the call.
	Current bool `json:"current"`
}

type createdKeyJSON struct {
	apiKeyJSON
	// Key is the whole secret, returned this once.
	Key string `json:"key"`
}

// createKeyRequest is the optional body of the routes that create a key.
type createKeyRequest struct {
	// Name says what the key is for: at most 100 characters, trimmed.
	// Absent, null or blank creates the key without one.
	Name *string `json:"name"`
	// ExpiresAt is when the key stops working. Absent or null means it
	// lives until it is revoked.
	ExpiresAt *time.Time `json:"expires_at"`
	// Scopes are what the key may do (auth.KeyScopes). Absent, null or
	// empty means the default of the route.
	Scopes []string `json:"scopes"`
}

const errExpiryPast = "expires_at must be a time in the future"

const errKeyName = "name is at most 100 characters"

// keyName is the name of a createKeyRequest, empty when absent.
func (r createKeyRequest) keyName() string {
	if r.Name == nil {
		return ""
	}
	return *r.Name
}

// errKeyScopes is the detail for a scope that does not exist.
var errKeyScopes = "scopes must be a list of: " + strings.Join(auth.KeyScopes, ", ")

// forbidScope answers 403 for a caller whose key does not hold scope and so
// cannot hand it on (to a new key, or to a user token).
func forbidScope(w http.ResponseWriter, scope string) {
	problem(w, http.StatusForbidden, "this API key does not hold the scope "+scope+" and cannot grant it")
}

func toAPIKeyJSON(k tenant.APIKey, current string) apiKeyJSON {
	var name *string
	if k.Name != "" {
		n := k.Name
		name = &n
	}
	return apiKeyJSON{ID: k.ID, Name: name, Prefix: k.Prefix, CreatedAt: k.CreatedAt.UTC(), LastUsedAt: utcPtr(k.LastUsedAt),
		RevokedAt: utcPtr(k.RevokedAt), ExpiresAt: utcPtr(k.ExpiresAt), Scopes: k.Scopes, Current: k.ID.String() == current}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// mountTenant serves the tenant of the key and its API keys. A tenant key
// manages its own tenant's keys, as `ohs admin key` does from a shell, and
// never creates a key that holds a scope it lacks itself.
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
			out.Key = keyRefJSON{ID: k.ID, Prefix: k.Prefix, Scopes: k.Scopes}
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
	var req createKeyRequest
	if !decodeJSON(w, r, &req, true) {
		return
	}
	// Without scopes the new key holds what the calling key holds. With
	// them, none may be one the calling key lacks: keys:write alone must
	// not be a way to full access.
	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = p.KeyScopes
	}
	scopes, err := auth.NormalizeKeyScopes(scopes)
	if err != nil {
		badRequest(w, errKeyScopes)
		return
	}
	for _, sc := range scopes {
		if !p.Can(sc) {
			forbidScope(w, sc)
			return
		}
	}
	full, k, err := s.d.Store.CreateKeyWith(r.Context(), p.TenantID,
		tenant.KeyOptions{Name: req.keyName(), ExpiresAt: req.ExpiresAt, Scopes: scopes})
	switch {
	case errors.Is(err, tenant.ErrExpiryPast):
		badRequest(w, errExpiryPast)
		return
	case errors.Is(err, tenant.ErrKeyName):
		badRequest(w, errKeyName)
		return
	case err != nil:
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
