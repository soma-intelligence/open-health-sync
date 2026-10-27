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
	"github.com/soma-intelligence/open-health-sync/internal/providerset"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/tenantkeys"
	"github.com/soma-intelligence/open-health-sync/internal/tenantproviders"
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
	// Providers are the connectors of this instance: the enabled ones (GET
	// /v1/providers) and the console's provider settings (/v1/admin). Nil
	// means none is enabled.
	Providers *providerset.Set
	// TenantProviders are the providers an instance admin turned off for
	// one tenant: left out of GET /v1/providers and refused on the upload
	// routes for that tenant, and switched by
	// /v1/admin/tenants/{tenant_id}/providers. Nil means every provider is
	// on for every tenant and leaves the admin routes out.
	TenantProviders *tenantproviders.Set
	// TenantKeys are the provider keys tenants brought for themselves:
	// listed for the tenant in GET /v1/providers, and set by
	// /v1/admin/tenants/{tenant_id}/providers/{provider}/keys. Nil leaves
	// those admin routes out. The connection routes read the same set from
	// Collect's Deps.
	TenantKeys *tenantkeys.Set
	// Device handles POST /v1/ingest/apple-health. Nil leaves the route out.
	Device connector.DeviceConnector
	// HealthConnect handles POST /v1/ingest/health-connect. Nil leaves the
	// route out.
	HealthConnect connector.DeviceConnector
	// Xiaomi handles POST /v1/ingest/xiaomi (Mi Fitness exports). Nil leaves
	// the route out.
	Xiaomi connector.DeviceConnector
	// ActivityFiles handles POST /v1/ingest/activity-files (FIT, GPX and
	// TCX files). Nil leaves the route out.
	ActivityFiles connector.DeviceConnector
	// Bluetooth handles POST /v1/ingest/bluetooth (values of standard
	// Bluetooth health devices). Nil leaves the route out.
	Bluetooth connector.DeviceConnector
	// SelfReport handles POST /v1/users/{id}/self-reports. Nil leaves the
	// route out.
	SelfReport connector.DeviceConnector
	// IngestLimit bounds uploads in flight (0 means DefaultIngestLimit).
	IngestLimit int
	// IngestTenantLimit is the share of the upload slots one tenant is sure
	// to get; past it, it takes a slot only while more than the reserve
	// (ingestReserve) stays free (0 means DefaultIngestTenantLimit of
	// IngestLimit; IngestLimit or more means no share).
	IngestTenantLimit int
	// IngestMaxDecompressedBytes caps how far one uploaded part may expand
	// (0 means collect.DefaultMaxDecompressedBytes).
	IngestMaxDecompressedBytes int64
	// Metrics mounts GET /metrics (OHS_METRICS).
	Metrics bool
	// Console mounts the admin console at /console (OHS_CONSOLE);
	// ConsoleReadOnly serves it without write actions.
	Console, ConsoleReadOnly bool
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
	// SetupToken (OHS_SETUP_TOKEN) mounts the first-run setup, GET and POST
	// /v1/setup. Empty leaves the routes out.
	SetupToken string
	// RateLimitInbound bounds POST and GET /inbound/{provider} per client
	// address and RateLimitInboundProvider per provider, RateLimitOAuth the
	// OAuth callback and POST /v1/setup per client address, each route its
	// own bucket. The zero value is no limit.
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
	s.mountAdmin(mux)
	s.mountTenant(mux)
	s.mountPipeline(mux)
	s.mountUsers(mux)
	s.mountTokens(mux)
	s.mountWebhooks(mux)
	s.mountDeletion(mux)
	s.mountReads(mux)
	s.mountTags(mux)
	s.mountSelfReports(mux)
	s.mountSourcePriorities(mux)
	s.mountFHIR(mux)
	s.mountOMH(mux)
	s.mountExports(mux)
	s.mountArchive(mux)
	s.mountIngest(mux)
	s.mountCollect(mux)
	s.mountMCP(mux)
	s.mountConsent(mux)
	s.mountShares(mux)
	s.mountSetup(mux)
	if d.Console {
		console.Mount(mux, d.ConsoleReadOnly)
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
	return s.instrument(textOK(mux))
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
