// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
)

// mountCollect mounts the provider connection routes, the hosted connection
// page, the OAuth callback and the provider notification routes. The callback and the notification routes
// face the providers and carry no bearer: the state (callback) or the
// connector's own verification (notifications) authenticates them, and a
// rate limiter bounds the work anyone can cause through them. Their
// query strings hold codes, states and signatures; instrument logs only the
// route pattern, never the URL.
func (s *server) mountCollect(mux *http.ServeMux) {
	h := s.d.Collect
	if h == nil {
		return
	}
	tenantKey := auth.Middleware(s.lookup, auth.TenantKey)
	mux.Handle("GET /v1/users/{id}/connections", tenantKey(h.ListConnections()))
	mux.Handle("GET /v1/users/{id}/freshness", tenantKey(h.Freshness()))
	mux.Handle("POST /v1/users/{id}/connections/{provider}", tenantKey(h.StartConnection()))
	mux.Handle("DELETE /v1/users/{id}/connections/{provider}", tenantKey(h.DeleteConnection()))
	mux.Handle("POST /v1/users/{id}/connections/{provider}/sync", tenantKey(h.SyncNow()))
	mux.Handle("GET /v1/users/{id}/sync-runs", tenantKey(h.ListSyncRuns()))
	mux.Handle("GET /v1/users/{id}/sync-runs/{run}", tenantKey(h.GetSyncRun()))
	mux.Handle("POST /v1/users/{id}/connect-sessions", tenantKey(h.CreateConnectSession()))
	// The hosted connection page: the session token in the link (query or
	// form field) is its credential, so no bearer.
	mux.Handle("GET /connect", h.ConnectPage())
	mux.Handle("GET /connect/{provider}", h.ConnectPage())
	mux.Handle("POST /connect/{provider}", h.ConnectStart())
	// The provider-facing routes are rate limited, the notification routes
	// (both methods share the buckets) also per provider.
	callback := newRateLimiter(s.d.RateLimitOAuth, RateLimit{})
	inbound := newRateLimiter(s.d.RateLimitInbound, s.d.RateLimitInboundProvider)
	known := map[string]bool{}
	for _, c := range s.d.Providers {
		known[c.Slug()] = true
	}
	provider := func(r *http.Request) string {
		// Unknown slugs share one bucket, so the set of buckets is bounded.
		if p := r.PathValue("provider"); known[p] {
			return p
		}
		return ""
	}
	mux.Handle("GET /oauth/{provider}/callback", s.rateLimit(groupOAuthCallback, callback, nil, h.OAuthCallback()))
	mux.Handle("POST /inbound/{provider}", s.rateLimit(groupInbound, inbound, provider, h.Inbound()))
	// GET also answers HEAD (ServeMux).
	mux.Handle("GET /inbound/{provider}", s.rateLimit(groupInbound, inbound, provider, h.InboundProbe()))
}
