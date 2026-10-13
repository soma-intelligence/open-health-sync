// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
)

// mountCollect mounts the provider connection routes, the hosted connection
// page, the OAuth callback and the provider notification routes. The callback and the notification routes
// face the providers and carry no bearer: the state (callback) or the
// connector's own verification (notifications) authenticates them. Their
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
	mux.Handle("POST /v1/users/{id}/connect-sessions", tenantKey(h.CreateConnectSession()))
	// The hosted connection page: the session token in the link (query or
	// form field) is its credential, so no bearer.
	mux.Handle("GET /connect", h.ConnectPage())
	mux.Handle("GET /connect/{provider}", h.ConnectPage())
	mux.Handle("POST /connect/{provider}", h.ConnectStart())
	mux.Handle("GET /oauth/{provider}/callback", h.OAuthCallback())
	mux.Handle("POST /inbound/{provider}", h.Inbound())
	// GET also answers HEAD (ServeMux).
	mux.Handle("GET /inbound/{provider}", h.InboundProbe())
}
