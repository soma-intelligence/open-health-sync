// SPDX-License-Identifier: AGPL-3.0-only

// Package config loads Open Health Sync configuration from environment variables.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// ProviderKeys holds the OHS_<SLUG>_<FIELD> values of one provider, keyed by FIELD
// (for example "CLIENT_ID").
type ProviderKeys map[string]string

// Config is the full process configuration.
type Config struct {
	DatabaseURL, PublicURL, RawStore, Listen, LogLevel, BootstrapTenant string
	EncryptionKey                                                       []byte          // decoded, len 32
	Roles                                                               map[string]bool // "api", "collect", "etl"
	Metrics                                                             bool
	// Console serves the read-only admin console at /console (OHS_CONSOLE,
	// default false).
	Console bool
	// MetricsToken (OHS_METRICS_TOKEN) is the bearer secret a scrape of
	// /metrics presents. MetricsListen (OHS_METRICS_LISTEN) serves /metrics
	// on an address of its own instead of the API listener.
	MetricsToken, MetricsListen string
	// RetentionDays prunes raw files and normalized data older than this
	// many days; 0 keeps everything.
	RetentionDays int
	// AuditRetentionDays prunes audit events older than this many days
	// (OHS_AUDIT_RETENTION_DAYS, default 365); 0 keeps every event.
	AuditRetentionDays int
	// IngestConcurrency bounds the device uploads one api process handles at
	// once (OHS_INGEST_CONCURRENCY, default 8).
	IngestConcurrency int
	// PullsPerProvider caps the pulls of one provider running at once across
	// every instance (OHS_PULLS_PER_PROVIDER, default 4); BackfillsPerProvider
	// caps its backfill pulls (OHS_BACKFILLS_PER_PROVIDER, default 2).
	PullsPerProvider, BackfillsPerProvider int
	// WebhookAllowPrivate lets webhook endpoints use plain http and reach
	// private and loopback addresses (OHS_WEBHOOK_ALLOW_PRIVATE, default false).
	WebhookAllowPrivate bool
	// Tracing exports OpenTelemetry traces over OTLP: on when
	// OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is
	// set and OTEL_SDK_DISABLED is not true (see TracingFromEnv).
	Tracing bool
	// TracingProtocol is the OTLP protocol, TracingHTTP or TracingGRPC.
	TracingProtocol string
	// ConsentRequired stores nothing from a source a user has recorded no
	// consent for (OHS_CONSENT_REQUIRED, default false: such a source
	// stores everything).
	ConsentRequired bool
	// RateLimitInbound bounds the notification routes per client address,
	// RateLimitInboundProvider per provider whatever the address, and
	// RateLimitOAuth the OAuth callback per client address
	// (OHS_RATE_LIMIT_INBOUND, OHS_RATE_LIMIT_INBOUND_PROVIDER,
	// OHS_RATE_LIMIT_OAUTH). The zero value disables a limit.
	RateLimitInbound, RateLimitInboundProvider, RateLimitOAuth RateLimit
	// TrustedProxies are the reverse proxies whose X-Forwarded-For names the
	// client address (OHS_TRUSTED_PROXIES, default none).
	TrustedProxies []netip.Prefix
	Providers      map[string]ProviderKeys // key = lower-case slug
}

// OTLP protocols, as OTEL_EXPORTER_OTLP_PROTOCOL names them.
const (
	TracingHTTP = "http/protobuf"
	TracingGRPC = "grpc"
)

// ErrTracing is wrapped by the Load errors about the OTEL_ variables.
var ErrTracing = errors.New("OpenTelemetry tracing")

// TracingFromEnv reports whether the standard OpenTelemetry variables ask
// for traces (an OTLP endpoint is set and the SDK is not disabled) and over
// which protocol: OTEL_EXPORTER_OTLP_TRACES_PROTOCOL, else
// OTEL_EXPORTER_OTLP_PROTOCOL, else http/protobuf. http/json is not
// supported: asking for it is an error rather than spans silently lost.
func TracingFromEnv(env func(string) (string, bool)) (enabled bool, protocol string, err error) {
	get := func(name string) string {
		v, _ := env(name)
		return strings.TrimSpace(v)
	}
	if strings.EqualFold(get("OTEL_SDK_DISABLED"), "true") {
		return false, "", nil
	}
	if get("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && get("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return false, "", nil
	}
	name, proto := "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", get("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL")
	if proto == "" {
		name, proto = "OTEL_EXPORTER_OTLP_PROTOCOL", get("OTEL_EXPORTER_OTLP_PROTOCOL")
	}
	switch proto {
	case "", TracingHTTP:
		return true, TracingHTTP, nil
	case TracingGRPC:
		return true, TracingGRPC, nil
	}
	return false, "", fmt.Errorf("%w: %s=%q is not supported: use http/protobuf (port 4318) or grpc (port 4317)", ErrTracing, name, proto)
}

// RateLimit is a token bucket: Rate requests per second sustained, Burst at
// once.
type RateLimit struct {
	Rate  float64
	Burst int
}

// MinMetricsToken is the shortest OHS_METRICS_TOKEN accepted.
const MinMetricsToken = 16

// DefaultAuditRetentionDays is OHS_AUDIT_RETENTION_DAYS when unset: a year
// of audit events.
const DefaultAuditRetentionDays = 365

// DefaultIngestConcurrency is OHS_INGEST_CONCURRENCY when unset.
const DefaultIngestConcurrency = 8

// The rate limits when unset. Providers batch notifications and a user's
// backfill comes in bursts, so the inbound limits stay well above what one
// provider sends; a person completes an OAuth flow a few times at most.
var (
	DefaultRateLimitInbound         = RateLimit{Rate: 30, Burst: 120}
	DefaultRateLimitInboundProvider = RateLimit{Rate: 300, Burst: 600}
	DefaultRateLimitOAuth           = RateLimit{Rate: 2, Burst: 20}
)

// reserved are the OHS_ variables (without prefix) that are never provider settings.
var reserved = map[string]bool{
	"DATABASE_URL":                true,
	"ENCRYPTION_KEY":              true,
	"PUBLIC_URL":                  true,
	"RAW_STORE":                   true,
	"ROLES":                       true,
	"BOOTSTRAP_TENANT":            true,
	"LISTEN":                      true,
	"LOG_LEVEL":                   true,
	"METRICS":                     true,
	"CONSOLE":                     true,
	"METRICS_TOKEN":               true,
	"METRICS_LISTEN":              true,
	"RETENTION_DAYS":              true,
	"AUDIT_RETENTION_DAYS":        true,
	"INGEST_CONCURRENCY":          true,
	"PULLS_PER_PROVIDER":          true,
	"BACKFILLS_PER_PROVIDER":      true,
	"WEBHOOK_ALLOW_PRIVATE":       true,
	"RATE_LIMIT_INBOUND":          true,
	"RATE_LIMIT_INBOUND_PROVIDER": true,
	"RATE_LIMIT_OAUTH":            true,
	"TRUSTED_PROXIES":             true,
	"CONSENT_REQUIRED":            true,
	"MCP_URL":                     true, // client side settings of `ohs mcp`
	"MCP_KEY":                     true,
	"HTTP_PORT":                   true, // read by docker compose only (host port)
	"HTTP_BIND":                   true, // read by docker compose only (host address)
	"VERSION":                     true, // read by docker compose only (image tag)
}

// ErrEncryptionKey is wrapped by every Load error about OHS_ENCRYPTION_KEY
// (missing, not base64, wrong length).
var ErrEncryptionKey = errors.New("OHS_ENCRYPTION_KEY")

// keyHint ends every OHS_ENCRYPTION_KEY error. Changing the key of an
// instance that already stores OAuth tokens makes them unreadable.
const keyHint = ": generate one with `openssl rand -base64 32` (or run `ohs doctor`), " +
	"and keep the same key for the life of the instance"

// ErrPublicURL and ErrLogLevel are wrapped by the Load errors about
// OHS_PUBLIC_URL and OHS_LOG_LEVEL, settings only serve reads.
var (
	ErrPublicURL = errors.New("OHS_PUBLIC_URL")
	ErrLogLevel  = errors.New("OHS_LOG_LEVEL")
)

// CheckPublicURL accepts an absolute http(s) URL with a host and no query or
// fragment: OAuth callbacks are built by appending a path to it.
func CheckPublicURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w %q must be an absolute http or https URL, for example https://health.example.org", ErrPublicURL, raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(raw, "?") || strings.HasSuffix(raw, "#") {
		return fmt.Errorf("%w %q must not have a query or fragment: callback paths are appended to it", ErrPublicURL, raw)
	}
	return nil
}

var validRoles = map[string]bool{"api": true, "collect": true, "etl": true}

// Load builds a Config. env looks up one variable; environ is the full
// "KEY=value" list used to discover OHS_<SLUG>_<FIELD> provider settings.
func Load(env func(string) (string, bool), environ []string) (Config, error) {
	get := func(name, def string) string {
		if v, ok := env(name); ok && v != "" {
			return v
		}
		return def
	}
	c := Config{
		DatabaseURL:     get("OHS_DATABASE_URL", ""),
		PublicURL:       get("OHS_PUBLIC_URL", "http://localhost:8080"),
		RawStore:        get("OHS_RAW_STORE", "file:///var/lib/ohs/raw"),
		Listen:          get("OHS_LISTEN", ":8080"),
		LogLevel:        get("OHS_LOG_LEVEL", "info"),
		BootstrapTenant: get("OHS_BOOTSTRAP_TENANT", ""),
		Roles:           map[string]bool{},
		Providers:       map[string]ProviderKeys{},
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("OHS_DATABASE_URL is required (postgres://user:password@host:5432/db?sslmode=disable)")
	}

	raw := get("OHS_ENCRYPTION_KEY", "")
	if raw == "" {
		return Config{}, fmt.Errorf("%w is required (32 random bytes, base64)%s", ErrEncryptionKey, keyHint)
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return Config{}, fmt.Errorf("%w is not valid base64%s", ErrEncryptionKey, keyHint)
	}
	if len(key) != 32 {
		return Config{}, fmt.Errorf("%w must decode to 32 bytes, got %d%s", ErrEncryptionKey, len(key), keyHint)
	}
	c.EncryptionKey = key

	if err := CheckPublicURL(c.PublicURL); err != nil {
		return Config{}, err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.LogLevel)); err != nil {
		return Config{}, fmt.Errorf("%w: %q is not one of debug, info, warn, error", ErrLogLevel, c.LogLevel)
	}

	for _, r := range strings.Split(get("OHS_ROLES", "api,collect,etl"), ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if !validRoles[r] {
			return Config{}, fmt.Errorf("OHS_ROLES: unknown role %q (want api, collect, etl)", r)
		}
		c.Roles[r] = true
	}
	if len(c.Roles) == 0 {
		return Config{}, fmt.Errorf("OHS_ROLES: no role selected")
	}

	c.Metrics = true
	if v := get("OHS_METRICS", ""); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("OHS_METRICS: %q is not a boolean", v)
		}
		c.Metrics = b
	}
	c.MetricsToken = get("OHS_METRICS_TOKEN", "")
	if c.MetricsToken != "" && len(c.MetricsToken) < MinMetricsToken {
		return Config{}, fmt.Errorf("OHS_METRICS_TOKEN must be at least %d characters (generate one with `openssl rand -hex 32`)", MinMetricsToken)
	}
	c.MetricsListen = get("OHS_METRICS_LISTEN", "")
	if c.MetricsListen != "" && c.MetricsListen == c.Listen {
		return Config{}, fmt.Errorf("OHS_METRICS_LISTEN %q must differ from OHS_LISTEN", c.MetricsListen)
	}

	if v := get("OHS_CONSOLE", ""); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("OHS_CONSOLE: %q is not a boolean", v)
		}
		c.Console = b
	}

	if v := get("OHS_RETENTION_DAYS", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("OHS_RETENTION_DAYS: %q is not a non-negative integer", v)
		}
		c.RetentionDays = n
	}

	c.AuditRetentionDays = DefaultAuditRetentionDays
	if v := get("OHS_AUDIT_RETENTION_DAYS", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("OHS_AUDIT_RETENTION_DAYS: %q is not a non-negative integer", v)
		}
		c.AuditRetentionDays = n
	}

	c.IngestConcurrency = DefaultIngestConcurrency
	if v := get("OHS_INGEST_CONCURRENCY", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("OHS_INGEST_CONCURRENCY: %q is not a positive integer", v)
		}
		c.IngestConcurrency = n
	}

	c.PullsPerProvider, c.BackfillsPerProvider = 4, 2
	for _, x := range []struct {
		name string
		dst  *int
	}{{"OHS_PULLS_PER_PROVIDER", &c.PullsPerProvider}, {"OHS_BACKFILLS_PER_PROVIDER", &c.BackfillsPerProvider}} {
		if v := get(x.name, ""); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return Config{}, fmt.Errorf("%s: %q is not a positive integer", x.name, v)
			}
			*x.dst = n
		}
	}

	if v := get("OHS_WEBHOOK_ALLOW_PRIVATE", ""); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("OHS_WEBHOOK_ALLOW_PRIVATE: %q is not a boolean", v)
		}
		c.WebhookAllowPrivate = b
	}

	if c.Tracing, c.TracingProtocol, err = TracingFromEnv(env); err != nil {
		return Config{}, err
	}
	if v := get("OHS_CONSENT_REQUIRED", ""); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("OHS_CONSENT_REQUIRED: %q is not a boolean", v)
		}
		c.ConsentRequired = b
	}

	for _, l := range []struct {
		name string
		dst  *RateLimit
		def  RateLimit
	}{
		{"OHS_RATE_LIMIT_INBOUND", &c.RateLimitInbound, DefaultRateLimitInbound},
		{"OHS_RATE_LIMIT_INBOUND_PROVIDER", &c.RateLimitInboundProvider, DefaultRateLimitInboundProvider},
		{"OHS_RATE_LIMIT_OAUTH", &c.RateLimitOAuth, DefaultRateLimitOAuth},
	} {
		*l.dst = l.def
		if v := get(l.name, ""); v != "" {
			rl, err := parseRateLimit(v)
			if err != nil {
				return Config{}, fmt.Errorf("%s: %w", l.name, err)
			}
			*l.dst = rl
		}
	}

	for _, s := range strings.Split(get("OHS_TRUSTED_PROXIES", ""), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, err := parseProxy(s)
		if err != nil {
			return Config{}, fmt.Errorf("OHS_TRUSTED_PROXIES: %q is not an IP address or CIDR (for example 10.0.0.0/8)", s)
		}
		c.TrustedProxies = append(c.TrustedProxies, p)
	}

	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, "OHS_") {
			continue
		}
		rest := strings.TrimPrefix(k, "OHS_")
		if reserved[rest] {
			continue
		}
		slug, field, ok := splitProviderKey(rest)
		if !ok {
			continue
		}
		if c.Providers[slug] == nil {
			c.Providers[slug] = ProviderKeys{}
		}
		c.Providers[slug][field] = v
	}
	return c, nil
}

// parseRateLimit reads "rate/burst" (requests per second, a positive
// number, and requests at once, a positive integer), or "0" for no limit.
func parseRateLimit(v string) (RateLimit, error) {
	if v == "0" {
		return RateLimit{}, nil
	}
	bad := fmt.Errorf("%q is not rate/burst (for example 30/120) or 0 to disable", v)
	rs, bs, ok := strings.Cut(v, "/")
	if !ok {
		return RateLimit{}, bad
	}
	rate, err := strconv.ParseFloat(strings.TrimSpace(rs), 64)
	if err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 {
		return RateLimit{}, bad
	}
	burst, err := strconv.Atoi(strings.TrimSpace(bs))
	if err != nil || burst < 1 {
		return RateLimit{}, bad
	}
	return RateLimit{Rate: rate, Burst: burst}, nil
}

// parseProxy reads a CIDR, or one address as a single-address prefix.
func parseProxy(s string) (netip.Prefix, error) {
	if a, err := netip.ParseAddr(s); err == nil {
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	if p.Addr().Is4In6() && p.Bits() >= 96 {
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
	}
	return p.Masked(), nil
}

// multiWordSlugs are the provider slugs that hold an underscore
// ("intervals_icu"), declared by RegisterSlug. OHS_INTERVALS_ICU_CLIENT_ID
// cannot be split on its first underscore like OHS_WITHINGS_CLIENT_ID: it
// would read as field ICU_CLIENT_ID of a provider "intervals".
var (
	slugMu         sync.RWMutex
	multiWordSlugs = map[string]bool{}
)

// RegisterSlug declares a provider slug, so Load files the
// OHS_<SLUG>_<FIELD> variables of a slug holding an underscore under it. A
// slug without one needs no declaration. The connector registry calls it for
// every registered connector, before Load runs (registration happens in
// init).
func RegisterSlug(slug string) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !strings.Contains(slug, "_") {
		return
	}
	slugMu.Lock()
	defer slugMu.Unlock()
	multiWordSlugs[slug] = true
}

// splitProviderKey splits OHS_ less a prefix into a lower-case slug and a
// field. The longest declared multi-word slug that prefixes the name wins;
// otherwise the name splits on its first underscore.
func splitProviderKey(rest string) (slug, field string, ok bool) {
	upper := strings.ToUpper(rest)
	best := ""
	slugMu.RLock()
	for s := range multiWordSlugs {
		p := strings.ToUpper(s) + "_"
		if len(s) > len(best) && strings.HasPrefix(upper, p) && len(rest) > len(p) {
			best = s
		}
	}
	slugMu.RUnlock()
	if best != "" {
		return best, rest[len(best)+1:], true
	}
	slug, field, ok = strings.Cut(rest, "_")
	if !ok || slug == "" || field == "" {
		return "", "", false
	}
	return strings.ToLower(slug), field, true
}
