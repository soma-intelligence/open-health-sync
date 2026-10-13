// SPDX-License-Identifier: AGPL-3.0-only

// Package api is the public HTTP API (v1) and the ops endpoints. NewRouter
// mounts every route; each area has its own mount function and handler file,
// so a new area is one file and one line in NewRouter.
package api

import (
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/console"
	"github.com/soma-intelligence/open-health-sync/internal/etl/automation"
	"github.com/soma-intelligence/open-health-sync/internal/fusion"
	"github.com/soma-intelligence/open-health-sync/internal/mcp"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/tracing"
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
	// Scorer, when set, ranks the sources of a metric a user has not ordered
	// in merged reads (see fusion.Scorer). Nil uses the built-in defaults.
	Scorer fusion.Scorer
	// Version is the instance version the user archive's manifest states.
	// Empty leaves it out.
	Version string
	// RateLimitInbound bounds POST and GET /inbound/{provider} per client
	// address and RateLimitInboundProvider per provider, RateLimitOAuth the
	// OAuth callback per client address. The zero value is no limit.
	RateLimitInbound, RateLimitInboundProvider, RateLimitOAuth RateLimit
	// TrustedProxies (OHS_TRUSTED_PROXIES) are the reverse proxies whose
	// X-Forwarded-For names the client address of a rate limited request.
	TrustedProxies []netip.Prefix
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
	s.mountSourcePriorities(mux)
	s.mountFHIR(mux)
	s.mountOMH(mux)
	s.mountExports(mux)
	s.mountArchive(mux)
	s.mountIngest(mux)
	s.mountCollect(mux)
	s.mountMCP(mux)
	s.mountConsent(mux)
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
// authenticated one in the audit log and turns a panic into a 500. With
// tracing on, the request is a server span named by its method and route
// pattern, child of the caller's W3C traceparent when one came.
func (s *server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := reqid.Sanitize(r.Header.Get(reqid.Header))
		w.Header().Set(reqid.Header, id)
		method := tracing.Method(r.Method)
		// The route is only known once the mux has matched: the span is
		// renamed when the request ends.
		ctx, span := tracing.Tracer().Start(tracing.Extract(r.Context(), r.Header), tracing.SpanName(method, ""),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(semconv.HTTPRequestMethodKey.String(method), attribute.String(tracing.AttrRequestID, id)))
		ctx, slot := auth.WithSlot(reqid.With(ctx, id))
		r = r.WithContext(ctx)
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == http.ErrAbortHandler {
				span.SetStatus(codes.Error, "")
				span.End()
				panic(v)
			}
			if v != nil {
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
			if span.IsRecording() {
				route := tracing.Route(r.Pattern)
				span.SetName(tracing.SpanName(method, route))
				span.SetAttributes(semconv.HTTPResponseStatusCode(code))
				if route != "" {
					span.SetAttributes(semconv.HTTPRoute(route))
				}
				if code >= 500 {
					span.SetStatus(codes.Error, "")
				}
			}
			span.End()
		}()
		next.ServeHTTP(sw, r)
	})
}
