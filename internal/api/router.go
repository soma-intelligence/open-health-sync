// SPDX-License-Identifier: AGPL-3.0-only

// Package api is the public HTTP API (v1) and the ops endpoints. NewRouter
// mounts every route; each area has its own mount function and handler file,
// so a new area is one file and one line in NewRouter.
package api

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"runtime/debug"
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
	"github.com/soma-intelligence/open-health-sync/internal/consolesession"
	"github.com/soma-intelligence/open-health-sync/internal/etl/automation"
	"github.com/soma-intelligence/open-health-sync/internal/fusion"
	"github.com/soma-intelligence/open-health-sync/internal/logging"
	"github.com/soma-intelligence/open-health-sync/internal/mcp"
	"github.com/soma-intelligence/open-health-sync/internal/providerset"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
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
	// Labs handles POST /v1/users/{id}/labs. Nil leaves the route
	// unmounted; the read routes are mounted either way.
	Labs connector.DeviceConnector
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
	// Settings are the live runtime settings: GET and PATCH /v1/admin/settings,
	// GET /v1/admin/settings/export and GET /v1/admin/processes. Nil leaves
	// the four routes out. The one Live of the process: never build a second.
	Settings *settings.Live
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
	// RateLimits, when set, wins over the RateLimit fields below and is read
	// per request: a limiter is rebuilt (counters full) when its limit
	// changes, and the zero RateLimit turns one off, with no default
	// standing in. Nil keeps the fixed fields.
	RateLimits func() RateLimits
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
	// Stream serves the event stream (GET /v1/events/stream and
	// /v1/users/{id}/events/stream); its Run must be running. Nil leaves
	// the routes answering 404 (OHS_STREAM_MAX=0).
	Stream *StreamHub
}

type server struct {
	d       Deps
	metrics *httpMetrics
	lookup  auth.Lookup
	// authFail throttles refused credentials, unless that is turned off,
	// and bounds the audit events they leave.
	authFail *authFailures
	// staticLimits are the limits of the fixed Deps fields, used without
	// Deps.RateLimits.
	staticLimits RateLimits
	// whyRefused resolves a refused credential for the audit log.
	whyRefused func(ctx context.Context, hash string) (tenant.Refused, error)
	// sink, when set, takes every audit event instead of the database
	// (tests).
	sink func(audit.Event)
	// schema caches the schema version check of /readyz.
	schema schemaCheck
	// sessions resolves the console cookie; nil when the console is off.
	sessions *consolesession.Store
	// apiLimit is the live limiter of limitedLookup, shared with the console
	// session lookup so a cookie and a key of one id count together.
	apiLimit *liveLimiter
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
	s := &server{d: d, metrics: newHTTPMetrics(), lookup: d.Lookup, whyRefused: d.Store.WhyRefused, staticLimits: staticLimitsOf(d)}
	if s.lookup == nil {
		s.lookup = d.Store.LookupKey
	}
	s.apiLimit = s.liveLimiter(func(r RateLimits) (RateLimit, RateLimit) { return r.API, RateLimit{} })
	s.lookup = s.limitedLookup(s.lookup, s.apiLimit)
	if d.Console {
		s.sessions = consolesession.New(d.Pool)
	}
	s.authFail = newLiveAuthFailures(func() RateLimit { return s.limits().AuthFailures })
	if d.Audit != nil {
		s.metrics.auditDropped = d.Audit.Dropped
	}
	return s
}

// limits are the rate limits in force: the live ones, or the fixed fields
// of Deps with their defaults.
func (s *server) limits() RateLimits {
	if s.d.RateLimits != nil {
		return s.d.RateLimits()
	}
	return s.staticLimits
}

// liveLimiter is the limiter of one bucket of the limits, following them.
func (s *server) liveLimiter(pick func(RateLimits) (perAddr, global RateLimit)) *liveLimiter {
	return newLiveLimiter(func() (RateLimit, RateLimit) { return pick(s.limits()) })
}

// staticLimitsOf are the limits of the fixed fields: the zero API and
// auth failure limits are the defaults unless turned off.
func staticLimitsOf(d Deps) RateLimits {
	r := RateLimits{Inbound: d.RateLimitInbound, InboundProvider: d.RateLimitInboundProvider, OAuth: d.RateLimitOAuth}
	if !d.RateLimitAPIOff {
		if r.API = d.RateLimitAPI; !r.API.on() {
			r.API = DefaultRateLimitAPI
		}
	}
	if !d.RateLimitAuthFailuresOff {
		if r.AuthFailures = d.RateLimitAuthFailures; !r.AuthFailures.on() {
			r.AuthFailures = DefaultRateLimitAuthFailures
		}
	}
	return r
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
	s.mountStream(mux)
	s.mountDeletion(mux)
	s.mountReads(mux)
	s.mountTags(mux)
	s.mountSelfReports(mux)
	s.mountLabs(mux)
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
		console.Mount(mux)
		s.mountConsoleSession(mux)
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
// when well formed), counts it by its matched pattern, writes its access line (matched
// route pattern, never the query string or a header; the level follows the
// status: 2xx and 3xx debug, 4xx info, 5xx error), records an
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
		ctx = tracing.LogContext(logging.WithRequestID(ctx, id))
		ctx, slot := auth.WithSlot(reqid.With(ctx, id))
		r = r.WithContext(s.sealedReads(withAuditNotes(ctx)))
		slot.OnFailure(func(auth.Failure) (time.Duration, bool) { return s.refused(r) })
		slot.RequireScope(routeScope)
		if s.sessions != nil {
			slot.AcceptSession(s.sessionAuth)
		}
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == http.ErrAbortHandler {
				span.SetStatus(codes.Error, "")
				span.End()
				panic(v)
			}
			if v != nil {
				logging.Ctx(r.Context()).Error().Str("route", r.Pattern).Str("panic_type", fmt.Sprintf("%T", v)).
					Bytes("stack", debug.Stack()).Msg("http_panic")
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
			accessLine(r, code, sw.bytes, time.Since(start))
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
