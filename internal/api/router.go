// SPDX-License-Identifier: AGPL-3.0-only

// Package api is the public HTTP API (v1) and the ops endpoints. NewRouter
// mounts every route; each area has its own mount function and handler file,
// so a new area is one file and one line in NewRouter.
package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/console"
	"github.com/soma-intelligence/open-health-sync/internal/etl/automation"
	"github.com/soma-intelligence/open-health-sync/internal/mcp"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// DefaultIngestLimit is how many device uploads one instance processes at once.
const DefaultIngestLimit = 8

// Deps is what the router needs.
type Deps struct {
	Pool  *pgxpool.Pool
	Store *tenant.Store
	Raw   rawstore.Store
	// Lookup authenticates bearer secrets. Nil means Store.LookupKey.
	Lookup auth.Lookup
	// Providers are the connectors enabled on this instance (GET /v1/providers).
	Providers []connector.Connector
	// Device handles POST /v1/ingest/apple-health. Nil leaves the route out.
	Device connector.DeviceConnector
	// HealthConnect handles POST /v1/ingest/health-connect. Nil leaves the
	// route out.
	HealthConnect connector.DeviceConnector
	// IngestLimit bounds uploads in flight (0 means DefaultIngestLimit).
	IngestLimit int
	// Metrics mounts GET /metrics (OHS_METRICS).
	Metrics bool
	// Console mounts the read-only admin console at /console (OHS_CONSOLE).
	Console bool
	// MetricsToken (OHS_METRICS_TOKEN) is the bearer secret /metrics asks
	// for. Empty means /metrics on the API listener refuses every scrape.
	MetricsToken string
	// MetricsMux, when set (OHS_METRICS_LISTEN), takes GET /metrics instead
	// of the API listener; there the token is asked for only when set.
	MetricsMux *http.ServeMux
	// ETLMetrics is exported on /metrics when set (the etl role runs here).
	ETLMetrics *automation.Metrics
	// Collect serves the connection, OAuth callback and notification routes.
	// Nil leaves them out.
	Collect *collect.Handlers
	// PublicURL (OHS_PUBLIC_URL) starts the absolute links of FHIR
	// responses. Empty means the request's own scheme and host.
	PublicURL string
	// Webhooks manages outgoing webhook endpoints (/v1/webhooks). Nil
	// leaves the routes out.
	Webhooks *webhook.Store
	// WebhookAllowPrivate (OHS_WEBHOOK_ALLOW_PRIVATE) accepts http and
	// private hosts in endpoint URLs.
	WebhookAllowPrivate bool
}

type server struct {
	d       Deps
	metrics *httpMetrics
	lookup  auth.Lookup
}

// NewRouter returns the handler for every route of the public API and the ops
// endpoints.
func NewRouter(d Deps) http.Handler {
	if d.Pool == nil || d.Store == nil {
		panic("api: NewRouter needs Pool and Store")
	}
	s := &server{d: d, metrics: newHTTPMetrics(), lookup: d.Lookup}
	if s.lookup == nil {
		s.lookup = d.Store.LookupKey
	}
	mux := http.NewServeMux()
	s.mountOps(mux)
	s.mountProviders(mux)
	s.mountUsers(mux)
	s.mountTokens(mux)
	s.mountWebhooks(mux)
	s.mountDeletion(mux)
	s.mountReads(mux)
	s.mountTags(mux)
	s.mountFHIR(mux)
	s.mountOMH(mux)
	s.mountExports(mux)
	s.mountIngest(mux)
	s.mountCollect(mux)
	s.mountMCP(mux)
	if d.Console {
		console.Mount(mux)
	}
	s.mountAudit(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// The catch-all matches every method, so the mux never answers 405
		// itself: a known path called with the wrong method lands here.
		if allow := allowedMethods(mux, r); len(allow) > 0 {
			list := strings.Join(allow, ", ")
			w.Header().Set("Allow", list)
			problem(w, http.StatusMethodNotAllowed, r.Method+" is not allowed here; this route takes "+list)
			return
		}
		notFound(w, "route")
	})
	return s.instrument(mux)
}

// allowedMethods lists the methods a route other than the catch-all serves
// for r's path, sorted.
func allowedMethods(mux *http.ServeMux, r *http.Request) []string {
	var allow []string
	for _, m := range []string{http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodPatch, http.MethodPost, http.MethodPut} {
		if m == r.Method {
			continue
		}
		probe := r.Clone(r.Context())
		probe.Method = m
		if _, pattern := mux.Handler(probe); pattern != "" && pattern != "/" {
			allow = append(allow, m)
		}
	}
	return allow
}

// mountMCP serves the read-only MCP server at /mcp to a tenant key or a user
// token with scope read; a user token without it gets 403.
func (s *server) mountMCP(mux *http.ServeMux) {
	h := mcp.NewHandler(mcp.Deps{DB: s.d.Pool, Store: s.d.Store, Webhooks: s.d.Webhooks})
	mux.Handle("/mcp", auth.MiddlewareAny(s.lookup, auth.TenantKey, auth.UserRead)(h))
}

// tenantRoute mounts h behind a tenant API key.
func (s *server) tenantRoute(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	mux.Handle(pattern, auth.Middleware(s.lookup, auth.TenantKey)(h))
}

// instrument gives every request an id (X-Request-Id, kept from the caller
// when well formed), counts it by its matched pattern, logs it at debug level
// (route pattern, never the query string or a header), records an
// authenticated one in the audit log and turns a panic into a 500.
func (s *server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := reqid.Sanitize(r.Header.Get(reqid.Header))
		w.Header().Set(reqid.Header, id)
		ctx, slot := auth.WithSlot(reqid.With(r.Context(), id))
		r = r.WithContext(ctx)
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.ErrorContext(r.Context(), "api: panic", "panic", v, "route", r.Pattern)
				if sw.code == 0 {
					problem(sw, http.StatusInternalServerError, "an internal error occurred")
				}
			}
			code := sw.code
			if code == 0 {
				code = http.StatusOK
			}
			s.metrics.count(r.Pattern, code)
			if p, ok := slot.Principal(); ok {
				s.audit(r, p, code)
			}
			slog.DebugContext(r.Context(), "http request", "route", r.Pattern, "status", code,
				"duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(sw, r)
	})
}
