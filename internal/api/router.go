// SPDX-License-Identifier: AGPL-3.0-only

// Package api is the public HTTP API (v1) and the ops endpoints. NewRouter
// mounts every route; each area has its own mount function and handler file,
// so a new area is one file and one line in NewRouter.
package api

import (
	"context"
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

	"github.com/soma-intelligence/open-health-sync/internal/audit"
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
	"github.com/soma-intelligence/open-health-sync/internal/vault"
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
	// RateLimitAPI bounds the requests of one credential on every
	// authenticated route (an API key by its id, a user token by its user).
	// The zero value is DefaultRateLimitAPI; RateLimitAPIOff turns the limit
	// off. Buckets are per process: each replica counts on its own.
	RateLimitAPI    RateLimit
	RateLimitAPIOff bool
	// RateLimitAuthFailures bounds the refused credentials (401) of one
	// client address: past it, a refused credential of that address answers
	// 429 instead of 401. A valid credential is never refused by it. The
	// zero value is DefaultRateLimitAuthFailures; RateLimitAuthFailuresOff
	// turns the throttle off (failures are still recorded). Per process
	// too.
	RateLimitAuthFailures    RateLimit
	RateLimitAuthFailuresOff bool
	// Audit, when set, takes the audit events of the high volume actions
	// (audit.Buffered) and writes them in batches, so a request does not
	// wait for its audit row. The caller starts it (audit.NewWriter) and
	// closes it after the HTTP server stopped, which writes what is queued.
	// Nil writes every event before the request ends, as before.
	Audit *audit.Writer
	// CompressionOff leaves every response as it is, for an instance behind
	// a proxy that compresses. By default JSON, NDJSON, FHIR, CSV and YAML
	// responses are gzipped for a client that accepts it.
	CompressionOff bool
	// Vault holds the tenant and user keys. When set, /readyz checks that
	// its key provider opens keys and /metrics carries its counters.
	Vault *vault.Vault
}

type server struct {
	d       Deps
	metrics *httpMetrics
	lookup  auth.Lookup
	// authFail throttles refused credentials, unless that is turned off,
	// and bounds the audit events they leave.
	authFail *authFailures
	// whyRefused resolves a refused credential for the audit log.
	whyRefused func(ctx context.Context, hash string) (tenant.Refused, error)
	// sink, when set, takes every audit event instead of the database
	// (tests).
	sink func(audit.Event)
	// schema caches the schema version check of /readyz.
	schema schemaCheck
}

// NewRouter returns the handler for every route of the public API and the ops
// endpoints.
func NewRouter(d Deps) http.Handler {
	return newServer(d).handler()
}

func newServer(d Deps) *server {
	if d.Pool == nil || d.Store == nil {
		panic("api: NewRouter needs Pool and Store")
	}
	s := &server{d: d, metrics: newHTTPMetrics(), lookup: d.Lookup, whyRefused: d.Store.WhyRefused}
	if s.lookup == nil {
		s.lookup = d.Store.LookupKey
	}
	if !d.RateLimitAPIOff {
		l := d.RateLimitAPI
		if !l.on() {
			l = DefaultRateLimitAPI
		}
		s.lookup = s.limitedLookup(s.lookup, newRateLimiter(l, RateLimit{}))
	}
	var failures RateLimit // the zero value throttles nothing
	if !d.RateLimitAuthFailuresOff {
		if failures = d.RateLimitAuthFailures; !failures.on() {
			failures = DefaultRateLimitAuthFailures
		}
	}
	s.authFail = newAuthFailures(failures)
	if d.Audit != nil {
		s.metrics.auditDropped = d.Audit.Dropped
	}
	return s
}

// handler mounts every route. It is called once.
func (s *server) handler() http.Handler {
	d := s.d
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
	h := textOK(mux)
	if !d.CompressionOff {
		h = compress(h)
	}
	return s.instrument(h)
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
// token with scope read; a user token without it gets 403. Like every
// route, it asks the key (or the key that minted the token) for its key
// scope, data:read (routeScopes).
func (s *server) mountMCP(mux *http.ServeMux) {
	h := mcp.NewHandler(mcp.Deps{DB: s.d.Pool, Store: s.d.Store, Webhooks: s.d.Webhooks})
	mux.Handle("/mcp", auth.MiddlewareAny(s.lookup, auth.TenantKey, auth.UserRead)(h))
}

// tenantRoute mounts h behind a tenant API key. The key scope pattern needs
// is declared in routeScopes.
func (s *server) tenantRoute(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	mux.Handle(pattern, auth.Middleware(s.lookup, auth.TenantKey)(h))
}

// instrument gives every request an id (X-Request-Id, kept from the caller
// when well formed), counts it by its matched pattern, logs it at debug level
// (route pattern, never the query string or a header), records an
// authenticated one in the audit log, and a refused credential within
// bounds (authFailed), and turns a panic into a 500. It tells the auth
// middleware which key scope each route needs (routeScope): a credential
// without it answers 403, recorded with the scope it lacked. A credential the lookup
// refused, from an address that presented too many of them, answers 429
// instead of 401 (refused); a valid one is never refused for that. With
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
		r = r.WithContext(s.sealedReads(ctx))
		slot.OnFailure(func(auth.Failure) (time.Duration, bool) { return s.refused(r) })
		slot.RequireScope(routeScope)
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
				s.audit(r, p, code, slot.MissingScope())
			} else if f, ok := slot.Failure(); ok && code == http.StatusUnauthorized {
				// A throttled failure (429) leaves no event: it is counted.
				s.authFailed(r, f)
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
