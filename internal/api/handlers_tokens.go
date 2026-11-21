// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

type mintTokenRequest struct {
	Scopes []string `json:"scopes"`
	// TTLSeconds is the lifetime: 0 or absent means one hour. At most 86400,
	// or 2592000 (30 days) when read is the only scope.
	TTLSeconds int64 `json:"ttl_seconds"`
}

type mintTokenResponse struct {
	// ID names the token without revealing it (the audit log's actor id):
	// what DELETE /v1/users/{id}/tokens/{token_id} takes.
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Scopes    []string  `json:"scopes"`
}

// errTTLTooLong is the one message for a lifetime over the scope's limit,
// whichever check catches it.
const errTTLTooLong = "ttl_seconds is too long for these scopes: at most 86400, or 2592000 when read is the only scope"

func (s *server) mountTokens(mux *http.ServeMux) {
	// Not wrapped in idempotent: replaying would mean storing the plaintext
	// token. A retried mint simply mints another token; both are valid.
	s.tenantRoute(mux, "POST /v1/users/{id}/tokens", s.mintToken)
	s.tenantRoute(mux, "DELETE /v1/users/{id}/tokens", s.revokeTokens)
	s.tenantRoute(mux, "DELETE /v1/users/{id}/tokens/{token_id}", s.revokeToken)
}

// revokeToken revokes one token of the user, named by the id its mint
// response gave.
func (s *server) revokeToken(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathUser(w, r)
	if !ok {
		return
	}
	err := s.d.Store.RevokeUserToken(r.Context(), p.TenantID, id, r.PathValue("token_id"))
	switch {
	case errors.Is(err, tenant.ErrNotFound):
		notFound(w, "token")
		return
	case err != nil:
		internalError(w, r, "api: revoke token", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// revokeTokens revokes every token of the user that is still valid.
func (s *server) revokeTokens(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathUser(w, r)
	if !ok {
		return
	}
	n, err := s.d.Store.RevokeUserTokens(r.Context(), p.TenantID, id)
	switch {
	case errors.Is(err, tenant.ErrNotFound):
		notFound(w, "user")
		return
	case err != nil:
		internalError(w, r, "api: revoke tokens", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"revoked": n})
}

func (s *server) mintToken(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathUser(w, r)
	if !ok {
		return
	}
	var req mintTokenRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.TTLSeconds < 0 {
		badRequest(w, "ttl_seconds must not be negative")
		return
	}
	if req.TTLSeconds > int64(tenant.MaxReadTokenTTL/time.Second) {
		badRequest(w, errTTLTooLong)
		return
	}
	// A token never does more than the key that mints it: the key must
	// hold the key scope each token scope stands for. The lookup holds the
	// token to the key's scopes on every request as well.
	for _, sc := range req.Scopes {
		if need := auth.TokenNeeds(auth.Scope(sc)); need != "" && !p.Can(need) {
			forbidScope(w, need)
			return
		}
	}
	// The token remembers the key that minted it: revoking the key revokes
	// the token. An admin serving another tenant mints with a key that is
	// not this tenant's, and so does a console session acting on another
	// tenant; such a token is tied to no key.
	var mintedBy *uuid.UUID
	if k, err := uuid.Parse(p.KeyID); err == nil && ownTenant(r, p.TenantID) && p.TenantID == p.Actor() {
		mintedBy = &k
	}
	token, exp, err := s.d.Store.MintUserTokenBy(r.Context(), p.TenantID, id, req.Scopes, time.Duration(req.TTLSeconds)*time.Second, mintedBy)
	switch {
	case errors.Is(err, tenant.ErrInvalidScopes):
		badRequest(w, "scopes must be a non-empty list of upload, connect and read")
		return
	case errors.Is(err, tenant.ErrTTLTooLong):
		badRequest(w, errTTLTooLong)
		return
	case errors.Is(err, tenant.ErrTTLInvalid):
		badRequest(w, "ttl_seconds is too short")
		return
	case errors.Is(err, tenant.ErrNotFound):
		notFound(w, "user")
		return
	case err != nil:
		internalError(w, r, "api: mint token", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, mintTokenResponse{ID: audit.TokenID(auth.Hash(token)), Token: token, ExpiresAt: exp.UTC(), Scopes: dedup(req.Scopes)})
}

func dedup(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
