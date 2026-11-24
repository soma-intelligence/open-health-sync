// SPDX-License-Identifier: AGPL-3.0-only

// Package config loads Open Health Sync configuration from environment variables.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// ProviderKeys holds the OHS_<SLUG>_<FIELD> values of one provider, keyed by FIELD
// (for example "CLIENT_ID").
type ProviderKeys map[string]string

// Config is the full process configuration.
type Config struct {
	DatabaseURL, PublicURL, RawStore, Listen, LogLevel, BootstrapTenant string
	// BootstrapKeyFile (OHS_BOOTSTRAP_KEY_FILE) is where the first API key of OHS_BOOTSTRAP_TENANT
	// is written, mode 0600. Empty means inside the raw store directory.
	BootstrapKeyFile string
	EncryptionKey    []byte // decoded, len 32
	// PreviousEncryptionKeys still open values sealed before a rotation but
	// never seal (OHS_PREVIOUS_ENCRYPTION_KEYS, decoded, each len 32).
	PreviousEncryptionKeys [][]byte
	Roles                  map[string]bool // "api", "collect", "etl"
	Metrics                bool
	// Console serves the admin console at /console (OHS_CONSOLE true or
	// read-only, default false). ConsoleReadOnly (read-only) hides its write
	// actions.
	Console, ConsoleReadOnly bool
	// MetricsToken (OHS_METRICS_TOKEN) is the bearer secret a scrape of
	// /metrics presents. MetricsListen (OHS_METRICS_LISTEN) serves /metrics
	// on an address of its own instead of the API listener.
	MetricsToken, MetricsListen string
	// OpsListen (OHS_OPS_LISTEN) serves /healthz, /readyz and /metrics on an
	// address of its own, without a credential: the only HTTP a process
	// without the api role has. Empty is off. Load sets DefaultOpsListen for
	// a process without the api role when the variable is unset, and
	// OpsListenDefault with it, so a taken port there is not fatal.
	OpsListen        string
	OpsListenDefault bool
	// SetupToken (OHS_SETUP_TOKEN) opens the browser first run: POST
	// /v1/setup creates the first tenant and its API key for whoever
	// presents it, once, while the instance has no tenant. Empty (the
	// default) leaves the setup routes out. A secret: never logged.
	SetupToken string
	// EncryptionKeySource and SetupTokenSource say where each secret came
	// from: the variable, or the file its _FILE variable names (read, or
	// created with a random value). For `ohs doctor`.
	EncryptionKeySource, SetupTokenSource SecretSource
	// RetentionDays prunes raw files and normalized data older than this
	// many days; 0 keeps everything.
	RetentionDays int
	// AuditRetentionDays prunes audit events older than this many days
	// (OHS_AUDIT_RETENTION_DAYS, default 365); 0 keeps every event.
	AuditRetentionDays int
	// IngestConcurrency bounds the device uploads one api process handles at
	// once (OHS_INGEST_CONCURRENCY, default 8).
	IngestConcurrency int
	// IngestTenantConcurrency is the share of those one tenant is sure to
	// get (OHS_INGEST_TENANT_CONCURRENCY, default half of IngestConcurrency,
	// rounded up): past it a tenant takes a slot only while more than a
	// small reserve stays free for the others. IngestConcurrency turns the
	// share off.
	IngestTenantConcurrency int
	// IngestMaxDecompressedMiB caps how far one uploaded part may expand, in
	// MiB (OHS_INGEST_MAX_DECOMPRESSED_MIB, default 64, at most 512).
	IngestMaxDecompressedMiB int
	// MaxConnections caps the HTTP connections the api role holds open at
	// once (OHS_MAX_CONNECTIONS, default 4096); one over it is closed at
	// once. 0 in a Config not built by Load means no cap.
	MaxConnections int
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
	// RateLimitAPI bounds the requests of one credential on every
	// authenticated route (OHS_RATE_LIMIT_API, default 200/2000), and
	// RateLimitAuthFailures the refused credentials of one client address
	// (OHS_RATE_LIMIT_AUTH_FAILURES, default 1/60). The zero value disables
	// a limit.
	RateLimitAPI, RateLimitAuthFailures RateLimit
	// CompressionOff leaves every response uncompressed (OHS_COMPRESSION
	// false), for an instance behind a proxy that compresses.
	CompressionOff bool
	// TrustedProxies are the reverse proxies whose X-Forwarded-For names the
	// client address (OHS_TRUSTED_PROXIES, default none).
	TrustedProxies []netip.Prefix
	// ETLConcurrency is how many jobs each ETL tier of one etl process runs
	// at once (OHS_ETL_CONCURRENCY, default 4). 0 in a Config not built by
	// Load means the default.
	ETLConcurrency int
	// ETLJobsPerUser caps the ETL jobs of one user and tier running at once
	// across every instance (OHS_ETL_JOBS_PER_USER, default 1). Negative
	// means no cap (the setting's 0); 0 in a Config not built by Load means
	// the default.
	ETLJobsPerUser int
	// TrackingRetentionDays is how long sync log rows and finished ETL jobs
	// are kept (OHS_TRACKING_RETENTION_DAYS, default 90). 0 in a Config not
	// built by Load means the default, a negative value keeps them for ever.
	TrackingRetentionDays int
	// PullConcurrency is how many pulls one collect process runs at once
	// (OHS_PULL_CONCURRENCY, default 4), and ScheduleBatch how many pulls
	// one scheduling pass queues (OHS_SCHEDULE_BATCH, default 1000). 0 in a
	// Config not built by Load means the default.
	PullConcurrency, ScheduleBatch int
	// BackfillDays is how far back a new connection is pulled, in days
	// (OHS_BACKFILL_DAYS, at most MaxBackfillDays). 0, the default, leaves
	// each provider its own window.
	BackfillDays int
	// WebhookConcurrency is how many webhook deliveries one etl process
	// posts at once (OHS_WEBHOOK_CONCURRENCY, default 8), and
	// WebhookPerEndpoint how many of them go to one endpoint, across every
	// instance (OHS_WEBHOOK_PER_ENDPOINT, default 2), and WebhookPerTenant
	// how many go to all the endpoints of one tenant, across every instance
	// (OHS_WEBHOOK_PER_TENANT, default half of WebhookConcurrency, at least
	// 1). 0 in a Config not built by Load means the default.
	WebhookConcurrency, WebhookPerEndpoint, WebhookPerTenant int
	// WebhookPauseAfter is how many attempts in a row an endpoint may fail
	// before it is paused (OHS_WEBHOOK_PAUSE_AFTER, default 5). Negative
	// means never (the setting's 0); 0 in a Config not built by Load means
	// the default.
	WebhookPauseAfter int
	// WebhookRotationGrace is how long the secret a rotation replaced keeps
	// signing next to the new one (OHS_WEBHOOK_ROTATION_GRACE_HOURS, default
	// 24). Negative means it stops at once (the setting's 0); 0 in a Config
	// not built by Load means the default.
	WebhookRotationGrace time.Duration
	// StreamMax caps the event streams one api process holds open at once
	// (OHS_STREAM_MAX, default 1000), and StreamMaxPerTenant those of one
	// tenant (OHS_STREAM_MAX_PER_TENANT, default 10, at most StreamMax).
	// Negative StreamMax turns the event stream off (the setting's 0); 0 in
	// a Config not built by Load means the default.
	StreamMax, StreamMaxPerTenant int
	Providers                     map[string]ProviderKeys // key = lower-case slug
	// Vault holds the key provider and key cache settings of the health data
	// vault (the OHS_VAULT_ variables).
	Vault Vault
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

// ConsoleReadOnly is the OHS_CONSOLE value of the console without write
// actions.
const ConsoleReadOnly = "read-only"

// DefaultOpsListen is OHS_OPS_LISTEN for a process without the api role:
// the loopback interface, because the listener asks for no credential, and
// the port next to the API's 8080 (9090 is Prometheus' own).
const DefaultOpsListen = "127.0.0.1:8081"

// MinMetricsToken is the shortest OHS_METRICS_TOKEN accepted.
const MinMetricsToken = 16

// MinSetupToken is the shortest OHS_SETUP_TOKEN accepted.
const MinSetupToken = 16

// DefaultAuditRetentionDays is OHS_AUDIT_RETENTION_DAYS when unset: a year
// of audit events.
const DefaultAuditRetentionDays = 365

// DefaultIngestConcurrency is OHS_INGEST_CONCURRENCY when unset.
const DefaultIngestConcurrency = 8

// DefaultIngestMaxDecompressedMiB is OHS_INGEST_MAX_DECOMPRESSED_MIB when
// unset, and MaxIngestMaxDecompressedMiB its largest value (the ETL reader's
// cap).
const (
	DefaultIngestMaxDecompressedMiB = 64
	MaxIngestMaxDecompressedMiB     = 512
)

// DefaultMaxConnections is OHS_MAX_CONNECTIONS when unset: about 140 MB of
// idle connections at most, well inside the compose file's 1 GB limit.
const DefaultMaxConnections = 4096

// The rate limits when unset. Providers batch notifications and a user's
// backfill comes in bursts, so the inbound limits stay well above what one
// provider sends; a person completes an OAuth flow a few times at most.
var (
	DefaultRateLimitInbound         = RateLimit{Rate: 30, Burst: 120}
	DefaultRateLimitInboundProvider = RateLimit{Rate: 300, Burst: 600}
	DefaultRateLimitOAuth           = RateLimit{Rate: 2, Burst: 20}
	// One credential (an API key, the tokens of one user): far above what a
	// backend or a device sends. Refused credentials per client address: a
	// burst covers a deploy with a stale key, then one a second.
	DefaultRateLimitAPI          = RateLimit{Rate: 200, Burst: 2000}
	DefaultRateLimitAuthFailures = RateLimit{Rate: 1, Burst: 60}
)

// The worker settings when unset, and their largest values. The defaults
// are those of the packages that run the work (internal/app checks they
// agree).
const (
	DefaultETLConcurrency = 4
	MaxETLConcurrency     = 64
	DefaultETLJobsPerUser = 1
	// DefaultTrackingRetentionDays is OHS_TRACKING_RETENTION_DAYS when unset
	// (internal/app checks it agrees with the scheduler's), and
	// MaxTrackingRetentionDays its largest value: a century.
	DefaultTrackingRetentionDays = 90
	MaxTrackingRetentionDays     = 36500
	DefaultPullConcurrency       = 4
	MaxPullConcurrency           = 256
	DefaultScheduleBatch         = 1000
	MaxScheduleBatch             = 100_000
	// MaxBackfillDays bounds OHS_BACKFILL_DAYS: five years.
	MaxBackfillDays                  = 5 * 365
	DefaultWebhookConcurrency        = 8
	MaxWebhookConcurrency            = 256
	DefaultWebhookPerEndpoint        = 2
	DefaultWebhookPerTenantDivisor   = 2
	DefaultWebhookPauseAfter         = 5
	MaxWebhookPauseAfter             = 1000
	DefaultWebhookRotationGraceHours = 24
	// MaxWebhookRotationGraceHours bounds OHS_WEBHOOK_ROTATION_GRACE_HOURS:
	// a week.
	MaxWebhookRotationGraceHours = 7 * 24
)

// notServer are the OHS_ variables the server never reads and that are no
// provider settings either: those of the CLI and the MCP server, and those of
// docker compose. Every other server variable is in the registry (Registry).
var notServer = map[string]bool{
	"OHS_URL":       true, // read by `ohs import` and `ohs seed`
	"OHS_TOKEN":     true,
	"OHS_MCP_URL":   true, // client side settings of `ohs mcp`
	"OHS_MCP_KEY":   true,
	"OHS_VERSION":   true, // read by docker compose only (image tag)
	"OHS_HTTP_BIND": true, // read by docker compose only (host address)
	"OHS_HTTP_PORT": true, // read by docker compose only (host port)
}

// ErrEncryptionKey is wrapped by every Load error about OHS_ENCRYPTION_KEY
// (missing, not base64, wrong length).
var ErrEncryptionKey = errors.New("OHS_ENCRYPTION_KEY")

// ErrPreviousEncryptionKeys is wrapped by every Load error about
// OHS_PREVIOUS_ENCRYPTION_KEYS.
var ErrPreviousEncryptionKeys = errors.New("OHS_PREVIOUS_ENCRYPTION_KEYS")

// keyHint ends every OHS_ENCRYPTION_KEY error. Replacing the key of an
// instance that already stores OAuth tokens makes them unreadable, unless the
// old key moves to OHS_PREVIOUS_ENCRYPTION_KEYS.
const keyHint = ": generate one with `openssl rand -base64 32` (or run `ohs doctor`); " +
	"to change the key of a running instance, follow docs/key-rotation.md"

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

// defaultRoles is OHS_ROLES when unset.
const defaultRoles = "api,collect,etl"

// noLowerBound is the lo of intIn for a setting that takes any negative value.
const noLowerBound = math.MinInt32

// The single-value checks of the settings. Load runs them on the value it
// reads and the registry (Setting.Validate) on the value a caller offers, so
// both refuse the same things with the same message, which starts with the
// name of the variable.

// intIn checks an integer from lo to hi. lo of noLowerBound and hi of
// math.MaxInt leave that side open.
func intIn(name string, lo, hi int) func(string) (int, error) {
	var want string
	switch {
	case lo == noLowerBound:
		want = fmt.Sprintf("an integer, at most %d", hi)
	case hi == math.MaxInt && lo == 1:
		want = "a positive integer"
	case hi == math.MaxInt && lo == 0:
		want = "a non-negative integer"
	case hi == math.MaxInt:
		want = fmt.Sprintf("an integer of at least %d", lo)
	default:
		want = fmt.Sprintf("an integer from %d to %d", lo, hi)
	}
	return func(v string) (int, error) {
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return 0, fmt.Errorf("%s: %q is not %s", name, v, want)
		}
		return n, nil
	}
}

// setInt sets *dst to the variable's integer value, or def when unset.
func setInt(get func(name, def string) string, name string, dst *int, def, lo, hi int) error {
	*dst = def
	v := get(name, "")
	if v == "" {
		return nil
	}
	n, err := intIn(name, lo, hi)(v)
	if err != nil {
		return err
	}
	*dst = n
	return nil
}

// boolValue checks true or false (any strconv.ParseBool spelling).
func boolValue(name string) func(string) (bool, error) {
	return func(v string) (bool, error) {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("%s: %q is not a boolean", name, v)
		}
		return b, nil
	}
}

// rateValue checks "rate/burst", or 0 for no limit.
func rateValue(name string) func(string) (RateLimit, error) {
	return func(v string) (RateLimit, error) {
		rl, err := parseRateLimit(v)
		if err != nil {
			return RateLimit{}, fmt.Errorf("%s: %w", name, err)
		}
		return rl, nil
	}
}

// enumValue checks one of values.
func enumValue(name string, values ...string) func(string) (string, error) {
	return func(v string) (string, error) {
		if !slices.Contains(values, v) {
			return "", fmt.Errorf("%s: %q is not one of %s", name, v, strings.Join(values, ", "))
		}
		return v, nil
	}
}

// proxyList checks comma separated IP addresses or CIDRs and returns them.
func proxyList(name string) func(string) ([]netip.Prefix, error) {
	return func(v string) ([]netip.Prefix, error) {
		var out []netip.Prefix
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			p, err := parseProxy(s)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not an IP address or CIDR (for example 10.0.0.0/8)", name, s)
			}
			out = append(out, p)
		}
		return out, nil
	}
}

// roleSet checks comma separated roles, at least one.
func roleSet(name string) func(string) (map[string]bool, error) {
	return func(v string) (map[string]bool, error) {
		roles := map[string]bool{}
		for _, r := range strings.Split(v, ",") {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if !validRoles[r] {
				return nil, fmt.Errorf("%s: unknown role %q (want api, collect, etl)", name, r)
			}
			roles[r] = true
		}
		if len(roles) == 0 {
			return nil, fmt.Errorf("%s: no role selected", name)
		}
		return roles, nil
	}
}

// consoleValue checks true, false or read-only, and returns whether the
// console is on and whether it is read only.
func consoleValue(name string) func(string) (on, readOnly bool, err error) {
	return func(v string) (bool, bool, error) {
		if strings.EqualFold(v, ConsoleReadOnly) {
			return true, true, nil
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false, false, fmt.Errorf("%s: %q is not true, false or %s", name, v, ConsoleReadOnly)
		}
		return b, false, nil
	}
}

// ParseLogLevel reads an OHS_LOG_LEVEL value: debug, info, warn or error, in any case. Anything
// else is an error wrapping ErrLogLevel; the process refuses to start rather than guess.
func ParseLogLevel(v string) (zerolog.Level, error) {
	switch strings.ToLower(v) {
	case "debug":
		return zerolog.DebugLevel, nil
	case "info":
		return zerolog.InfoLevel, nil
	case "warn":
		return zerolog.WarnLevel, nil
	case "error":
		return zerolog.ErrorLevel, nil
	}
	return zerolog.NoLevel, fmt.Errorf("%w: %q is not one of debug, info, warn, error", ErrLogLevel, v)
}

// logLevel checks a level (debug, info, warn, error).
func logLevel(v string) (zerolog.Level, error) { return ParseLogLevel(v) }

// metricsToken checks the length of a bearer token for /metrics.
func metricsToken(v string) error {
	if len(v) < MinMetricsToken {
		return fmt.Errorf("OHS_METRICS_TOKEN must be at least %d characters (generate one with `openssl rand -hex 32`)", MinMetricsToken)
	}
	return nil
}

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
		DatabaseURL:      get("OHS_DATABASE_URL", ""),
		PublicURL:        get("OHS_PUBLIC_URL", "http://localhost:8080"),
		RawStore:         get("OHS_RAW_STORE", "file:///var/lib/ohs/raw"),
		Listen:           get("OHS_LISTEN", ":8080"),
		LogLevel:         get("OHS_LOG_LEVEL", "info"),
		BootstrapTenant:  get("OHS_BOOTSTRAP_TENANT", ""),
		BootstrapKeyFile: get("OHS_BOOTSTRAP_KEY_FILE", ""),
		Roles:            map[string]bool{},
		Providers:        map[string]ProviderKeys{},
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("OHS_DATABASE_URL is required (postgres://user:password@host:5432/db?sslmode=disable)")
	}

	raw, src, err := secret(get, "OHS_ENCRYPTION_KEY", newKey)
	if err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrEncryptionKey, err)
	}
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
	c.EncryptionKey, c.EncryptionKeySource = key, src
	if c.PreviousEncryptionKeys, err = previousKeys(get("OHS_PREVIOUS_ENCRYPTION_KEYS", ""), key); err != nil {
		return Config{}, err
	}
	if c.Vault, err = loadVault(get, c.EncryptionKey, c.PreviousEncryptionKeys); err != nil {
		return Config{}, err
	}

	if err := CheckPublicURL(c.PublicURL); err != nil {
		return Config{}, err
	}
	if _, err := logLevel(c.LogLevel); err != nil {
		return Config{}, err
	}

	roles, err := roleSet("OHS_ROLES")(get("OHS_ROLES", defaultRoles))
	if err != nil {
		return Config{}, err
	}
	c.Roles = roles

	c.Metrics = true
	if v := get("OHS_METRICS", ""); v != "" {
		b, err := boolValue("OHS_METRICS")(v)
		if err != nil {
			return Config{}, err
		}
		c.Metrics = b
	}
	if c.MetricsToken = get("OHS_METRICS_TOKEN", ""); c.MetricsToken != "" {
		if err := metricsToken(c.MetricsToken); err != nil {
			return Config{}, err
		}
	}
	c.MetricsListen = get("OHS_METRICS_LISTEN", "")
	if c.MetricsListen != "" && c.MetricsListen == c.Listen {
		return Config{}, fmt.Errorf("OHS_METRICS_LISTEN %q must differ from OHS_LISTEN", c.MetricsListen)
	}
	switch c.OpsListen = get("OHS_OPS_LISTEN", ""); {
	case c.OpsListen == "off":
		c.OpsListen = ""
	case c.OpsListen == "" && !c.Roles["api"]:
		c.OpsListen, c.OpsListenDefault = DefaultOpsListen, true
	case c.OpsListen != "" && c.Roles["api"] && (c.OpsListen == c.Listen || c.OpsListen == c.MetricsListen):
		return Config{}, fmt.Errorf("OHS_OPS_LISTEN %q must differ from OHS_LISTEN and OHS_METRICS_LISTEN", c.OpsListen)
	}

	if c.SetupToken, c.SetupTokenSource, err = secret(get, "OHS_SETUP_TOKEN", newToken); err != nil {
		return Config{}, fmt.Errorf("OHS_SETUP_TOKEN: %w", err)
	}
	if c.SetupToken != "" && len(c.SetupToken) < MinSetupToken {
		return Config{}, fmt.Errorf("OHS_SETUP_TOKEN must be at least %d characters (generate one with `openssl rand -hex 32`)", MinSetupToken)
	}

	if v := get("OHS_CONSOLE", ""); v != "" {
		on, readOnly, err := consoleValue("OHS_CONSOLE")(v)
		if err != nil {
			return Config{}, err
		}
		c.Console, c.ConsoleReadOnly = on, readOnly
	}

	for _, x := range []struct {
		name string
		dst  *int
		def  int
		lo   int
		hi   int
	}{
		{"OHS_RETENTION_DAYS", &c.RetentionDays, 0, 0, math.MaxInt},
		{"OHS_AUDIT_RETENTION_DAYS", &c.AuditRetentionDays, DefaultAuditRetentionDays, 0, math.MaxInt},
		{"OHS_INGEST_CONCURRENCY", &c.IngestConcurrency, DefaultIngestConcurrency, 1, math.MaxInt},
	} {
		if err := setInt(get, x.name, x.dst, x.def, x.lo, x.hi); err != nil {
			return Config{}, err
		}
	}
	// The share of the uploads one tenant is sure to get: half of them
	// unless set, and never more than the process takes in all.
	c.IngestTenantConcurrency = max(1, (c.IngestConcurrency+1)/2)
	if v := get("OHS_INGEST_TENANT_CONCURRENCY", ""); v != "" {
		n, err := intIn("OHS_INGEST_TENANT_CONCURRENCY", 1, math.MaxInt)(v)
		if err == nil && n > c.IngestConcurrency {
			err = fmt.Errorf("OHS_INGEST_TENANT_CONCURRENCY: %q is not an integer from 1 to OHS_INGEST_CONCURRENCY (%d)", v, c.IngestConcurrency)
		}
		if err != nil {
			return Config{}, err
		}
		c.IngestTenantConcurrency = n
	}
	for _, x := range []struct {
		name string
		dst  *int
		def  int
		lo   int
		hi   int
	}{
		{"OHS_INGEST_MAX_DECOMPRESSED_MIB", &c.IngestMaxDecompressedMiB, DefaultIngestMaxDecompressedMiB, 1, MaxIngestMaxDecompressedMiB},
		{"OHS_MAX_CONNECTIONS", &c.MaxConnections, DefaultMaxConnections, 1, math.MaxInt},
		{"OHS_PULLS_PER_PROVIDER", &c.PullsPerProvider, 4, 1, math.MaxInt},
		{"OHS_BACKFILLS_PER_PROVIDER", &c.BackfillsPerProvider, 2, 1, math.MaxInt},
	} {
		if err := setInt(get, x.name, x.dst, x.def, x.lo, x.hi); err != nil {
			return Config{}, err
		}
	}

	if v := get("OHS_WEBHOOK_ALLOW_PRIVATE", ""); v != "" {
		b, err := boolValue("OHS_WEBHOOK_ALLOW_PRIVATE")(v)
		if err != nil {
			return Config{}, err
		}
		c.WebhookAllowPrivate = b
	}

	if c.Tracing, c.TracingProtocol, err = TracingFromEnv(env); err != nil {
		return Config{}, err
	}
	if v := get("OHS_CONSENT_REQUIRED", ""); v != "" {
		b, err := boolValue("OHS_CONSENT_REQUIRED")(v)
		if err != nil {
			return Config{}, err
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
		{"OHS_RATE_LIMIT_API", &c.RateLimitAPI, DefaultRateLimitAPI},
		{"OHS_RATE_LIMIT_AUTH_FAILURES", &c.RateLimitAuthFailures, DefaultRateLimitAuthFailures},
	} {
		*l.dst = l.def
		if v := get(l.name, ""); v != "" {
			rl, err := rateValue(l.name)(v)
			if err != nil {
				return Config{}, err
			}
			*l.dst = rl
		}
	}

	if v := get("OHS_COMPRESSION", ""); v != "" {
		b, err := boolValue("OHS_COMPRESSION")(v)
		if err != nil {
			return Config{}, err
		}
		c.CompressionOff = !b
	}

	if err := loadWorkers(get, &c); err != nil {
		return Config{}, err
	}
	if err := loadStream(get, &c); err != nil {
		return Config{}, err
	}

	if v := get("OHS_TRUSTED_PROXIES", ""); v != "" {
		if c.TrustedProxies, err = proxyList("OHS_TRUSTED_PROXIES")(v); err != nil {
			return Config{}, err
		}
	}

	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, "OHS_") {
			continue
		}
		rest := strings.TrimPrefix(k, "OHS_")
		if _, ok := LookupSetting(k); ok || notServer[k] {
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

// loadWorkers reads the settings of the etl and collect workers and of the
// webhook deliveries.
func loadWorkers(get func(name, def string) string, c *Config) error {
	// off is what a setting's 0 becomes where it turns a feature off: the
	// packages behind these fields read their own zero as the default.
	const off = -1
	var graceHours int
	for _, x := range []struct {
		name         string
		dst          *int
		def, lo, hi  int
		zeroTurnsOff bool
	}{
		{"OHS_ETL_CONCURRENCY", &c.ETLConcurrency, DefaultETLConcurrency, 1, MaxETLConcurrency, false},
		{"OHS_ETL_JOBS_PER_USER", &c.ETLJobsPerUser, DefaultETLJobsPerUser, 0, MaxETLConcurrency, true},
		{"OHS_TRACKING_RETENTION_DAYS", &c.TrackingRetentionDays, DefaultTrackingRetentionDays, noLowerBound, MaxTrackingRetentionDays, false},
		{"OHS_PULL_CONCURRENCY", &c.PullConcurrency, DefaultPullConcurrency, 1, MaxPullConcurrency, false},
		{"OHS_SCHEDULE_BATCH", &c.ScheduleBatch, DefaultScheduleBatch, 1, MaxScheduleBatch, false},
		{"OHS_BACKFILL_DAYS", &c.BackfillDays, 0, 1, MaxBackfillDays, false},
		{"OHS_WEBHOOK_CONCURRENCY", &c.WebhookConcurrency, DefaultWebhookConcurrency, 1, MaxWebhookConcurrency, false},
		{"OHS_WEBHOOK_PAUSE_AFTER", &c.WebhookPauseAfter, DefaultWebhookPauseAfter, 0, MaxWebhookPauseAfter, true},
		{"OHS_WEBHOOK_ROTATION_GRACE_HOURS", &graceHours, DefaultWebhookRotationGraceHours, 0, MaxWebhookRotationGraceHours, true},
	} {
		*x.dst = x.def
		v := get(x.name, "")
		if v == "" {
			continue
		}
		n, err := intIn(x.name, x.lo, x.hi)(v)
		if err != nil {
			return err
		}
		if n == 0 && x.zeroTurnsOff {
			n = off
		}
		*x.dst = n
	}
	c.WebhookRotationGrace = time.Duration(graceHours) * time.Hour

	// Of the deliveries one process posts, those to one endpoint: never more
	// than the process posts in all.
	c.WebhookPerEndpoint = min(DefaultWebhookPerEndpoint, c.WebhookConcurrency)
	if v := get("OHS_WEBHOOK_PER_ENDPOINT", ""); v != "" {
		n, err := intIn("OHS_WEBHOOK_PER_ENDPOINT", 1, MaxWebhookConcurrency)(v)
		if err == nil && n > c.WebhookConcurrency {
			err = fmt.Errorf("OHS_WEBHOOK_PER_ENDPOINT: %q is not an integer from 1 to OHS_WEBHOOK_CONCURRENCY (%d)", v, c.WebhookConcurrency)
		}
		if err != nil {
			return err
		}
		c.WebhookPerEndpoint = n
	}
	// And those to all the endpoints of one tenant: half of them unless set.
	c.WebhookPerTenant = max(c.WebhookConcurrency/DefaultWebhookPerTenantDivisor, 1)
	if v := get("OHS_WEBHOOK_PER_TENANT", ""); v != "" {
		n, err := intIn("OHS_WEBHOOK_PER_TENANT", 1, MaxWebhookConcurrency)(v)
		if err == nil && n > c.WebhookConcurrency {
			err = fmt.Errorf("OHS_WEBHOOK_PER_TENANT: %q is not an integer from 1 to OHS_WEBHOOK_CONCURRENCY (%d)", v, c.WebhookConcurrency)
		}
		if err != nil {
			return err
		}
		c.WebhookPerTenant = n
	}
	return nil
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

// previousKeys decodes OHS_PREVIOUS_ENCRYPTION_KEYS: comma separated base64
// keys of 32 bytes, none equal to the active key or to another. Errors name
// the entry by position, never by value.
func previousKeys(raw string, active []byte) ([][]byte, error) {
	return keyList(raw, active, ErrPreviousEncryptionKeys, "OHS_ENCRYPTION_KEY")
}

// keyList decodes a comma separated list of base64 keys of 32 bytes, none
// equal to the active key (named activeName in errors) or to another. Errors
// wrap sentinel, name the entry by position and never carry a value.
func keyList(raw string, active []byte, sentinel error, activeName string) ([][]byte, error) {
	var keys [][]byte
	n := 0
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		n++
		k, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("%w: entry %d is not valid base64 (comma separated keys from `openssl rand -base64 32`)", sentinel, n)
		}
		if len(k) != 32 {
			return nil, fmt.Errorf("%w: entry %d must decode to 32 bytes, got %d", sentinel, n, len(k))
		}
		if string(k) == string(active) {
			return nil, fmt.Errorf("%w: entry %d is %s itself; list only the keys it replaced", sentinel, n, activeName)
		}
		for i, o := range keys {
			if string(o) == string(k) {
				return nil, fmt.Errorf("%w: entries %d and %d are the same key", sentinel, i+1, n)
			}
		}
		keys = append(keys, k)
	}
	return keys, nil
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
