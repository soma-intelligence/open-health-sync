// SPDX-License-Identifier: AGPL-3.0-only

// Package mcp is the read-only Model Context Protocol server: Streamable HTTP
// at /mcp, on the API listener, behind the API's bearer authentication. Its
// tools mirror the REST read endpoints and call the same query layer
// (internal/api/read), so tenant isolation is enforced in one place.
package mcp

import (
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// Version is reported to clients in the initialize handshake (set by ohs serve).
var Version = "dev"

// Deps is what the tools read from.
type Deps struct {
	// DB runs the read queries (a *pgxpool.Pool).
	DB read.Querier
	// Store lists users for list_users.
	Store *tenant.Store
}

// NewHandler returns the Streamable HTTP MCP handler. Mount it behind
// auth.MiddlewareAny(lookup, auth.TenantKey, auth.UserRead): every request
// needs the Principal that middleware puts on the context, and a request
// without one is refused with 401.
//
// The server is stateless: each HTTP request gets a server bound to its
// caller, so no session outlives the credential that opened it, and any
// instance can answer any request. Responses are plain JSON, no event stream.
func NewHandler(d Deps) http.Handler {
	cache := sdk.NewSchemaCache()
	var h http.Handler = sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		p, _ := auth.From(r.Context())
		return newServer(d, p, cache)
	}, &sdk.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// The localhost guard rejects requests that reach a loopback address
		// with another Host header, which is every reverse proxy in front of
		// a self-hosted instance. It defends unauthenticated local servers
		// against DNS rebinding; here every request carries a bearer secret
		// a rebinding page cannot know.
		DisableLocalhostProtection: true,
	})
	// A browser page on another site must not drive /mcp with a credential
	// the browser would attach: refuse cross-origin non-safe requests (by
	// Sec-Fetch-Site, else Origin against Host). Agents send neither header.
	h = http.NewCrossOriginProtection().Handler(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.From(r.Context()); !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			auth.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid API key or user token is required")
			return
		}
		h.ServeHTTP(w, r)
	})
}

const instructions = `Open Health Sync: read-only access to health data (quantity samples, ` +
	`category samples, sleep, workouts, nutrition, daily summaries, sync coverage). ` +
	`With a tenant API key, call list_users first and pass user_id to every other tool; ` +
	`with a personal user token, omit user_id. Times are RFC 3339 UTC or YYYY-MM-DD; ` +
	`start is inclusive, end exclusive. Lists return at most limit records (default 100, ` +
	`max 1000): when truncated is true, call again with cursor set to next_cursor. ` +
	`For trends over days or weeks start with get_daily (one row per day); raw samples ` +
	`can number thousands a day, so call get_samples with a narrow start and end. ` +
	`get_coverage shows which days and metrics hold data at all.`

func newServer(d Deps, p auth.Principal, cache *sdk.SchemaCache) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "open-health-sync", Title: "Open Health Sync", Version: Version},
		&sdk.ServerOptions{Instructions: instructions, SchemaCache: cache})
	addTools(s, &tools{d: d, p: p})
	return s
}
