// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"
)

// Type is the kind of value a setting holds, which says how a console edits
// it and how Validate reads it.
type Type string

// The setting types.
const (
	TypeBool     Type = "bool"
	TypeInt      Type = "int"
	TypeDuration Type = "duration"
	TypeEnum     Type = "enum"
	TypeURL      Type = "url"
	TypeList     Type = "list" // comma separated
	TypeRate     Type = "rate" // rate/burst
	TypeString   Type = "string"
)

// Apply says when a change of a setting reaches a running process.
type Apply string

// The apply modes (docs/specs/console-v2.md, 1.3).
const (
	// ApplyLive is applied by every process within moments, without a restart.
	ApplyLive Apply = "live"
	// ApplyRestart is stored at once and applied when a process next starts.
	ApplyRestart Apply = "restart"
	// ApplyBootstrap is read from the environment before the database opens:
	// never stored, never edited.
	ApplyBootstrap Apply = "bootstrap"
)

// Setting is one server setting: the variable, how a console shows it, and
// the check Load runs on its value.
type Setting struct {
	// Name is the variable, for example OHS_WEBHOOK_PAUSE_AFTER.
	Name, Label, Section, Help string
	Type                       Type
	// Min and Max bound a TypeInt; nil leaves that side open.
	Min, Max *int64
	// Values are the choices of a TypeEnum.
	Values []string
	// Default is what Load uses when the setting is unset, as the string Load
	// reads. Empty when it is derived or there is none: DefaultNote then says
	// how it is computed.
	Default     string
	DefaultNote string
	// Secret is write only: a caller learns whether it is set, never its value.
	Secret bool
	Apply  Apply
	// Validate is the single-value check of Load, run by the settings API
	// before anything is stored. Its error starts with Name.
	Validate func(v string) error
}

// The sections of the registry, in the order a console lists them.
var Sections = []string{
	"Server", "Storage", "Security", "Console", "Metrics", "Retention", "Consent",
	"Ingest", "Sync", "Processing", "Webhooks", "Event stream", "Rate limits", "Vault",
}

// check turns a value check that also returns what it read into the plain
// check of Setting.Validate.
func check[T any](f func(string) (T, error)) func(string) error {
	return func(v string) error { _, err := f(v); return err }
}

// anyValue accepts everything: the value has no single-value rule (the rules
// across settings stay in Load).
func anyValue(string) error { return nil }

func i64(n int) *int64 { v := int64(n); return &v }

// intSetting declares a TypeInt from lo to hi (noLowerBound and math.MaxInt
// for an open side).
func intSetting(name, label, section, help, def string, lo, hi int, apply Apply) Setting {
	s := Setting{Name: name, Label: label, Section: section, Help: help, Type: TypeInt,
		Default: def, Apply: apply, Validate: check(intIn(name, lo, hi))}
	if lo != noLowerBound {
		s.Min = i64(lo)
	}
	if hi != math.MaxInt {
		s.Max = i64(hi)
	}
	return s
}

func boolSetting(name, label, section, help, def string, apply Apply) Setting {
	return Setting{Name: name, Label: label, Section: section, Help: help, Type: TypeBool,
		Default: def, Apply: apply, Validate: check(boolValue(name))}
}

func rateSetting(name, label, help, def string) Setting {
	return Setting{Name: name, Label: label, Section: "Rate limits", Help: help, Type: TypeRate,
		Default: def, Apply: ApplyLive, Validate: check(rateValue(name))}
}

// plain declares an unchecked TypeString of the bootstrap kind or a secret.
func plain(name, label, section, help, def string, secret bool) Setting {
	return Setting{Name: name, Label: label, Section: section, Help: help, Type: TypeString,
		Default: def, Secret: secret, Apply: ApplyBootstrap, Validate: anyValue}
}

func positiveDuration(name string) func(string) (time.Duration, error) {
	return func(v string) (time.Duration, error) {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("%s: %q is not a positive duration (for example 10m)", name, v)
		}
		return d, nil
	}
}

var keyProviders = []string{"local", "hashicorp", "azurekv", "awskms"}

// registry lists every server setting once, grouped by section. Load reads
// the same names and runs the same checks; a test fails when the two differ.
var registry = []Setting{
	// Server
	{Name: "OHS_PUBLIC_URL", Label: "Public URL", Section: "Server", Type: TypeURL, Default: "http://localhost:8080", Apply: ApplyRestart,
		Help:     "Public base URL of this server. OAuth callbacks are built by adding a path to it.",
		Validate: CheckPublicURL},
	plain("OHS_LISTEN", "Listen address", "Server", "Address the API listens on.", ":8080", false),
	{Name: "OHS_ROLES", Label: "Roles", Section: "Server", Type: TypeList, Default: defaultRoles, Apply: ApplyBootstrap,
		Help:     "Roles this process runs, comma separated: api, collect, etl.",
		Validate: check(roleSet("OHS_ROLES"))},
	{Name: "OHS_LOG_LEVEL", Label: "Log level", Section: "Server", Type: TypeEnum, Values: []string{"debug", "info", "warn", "error"},
		Default: "info", Apply: ApplyLive, Help: "How much the server logs.",
		Validate: check(logLevel)},
	{Name: "OHS_TRUSTED_PROXIES", Label: "Trusted proxies", Section: "Server", Type: TypeList, Apply: ApplyRestart,
		Default: "", DefaultNote: "none",
		Help:     "Reverse proxies whose X-Forwarded-For names the client address, comma separated IP addresses or CIDRs.",
		Validate: check(proxyList("OHS_TRUSTED_PROXIES"))},
	boolSetting("OHS_COMPRESSION", "Compress responses", "Server", "Gzip JSON, NDJSON, FHIR, CSV and YAML responses for clients that accept it. Turn it off behind a proxy that compresses.", "true", ApplyRestart),
	intSetting("OHS_MAX_CONNECTIONS", "Maximum connections", "Server", "HTTP connections the api role holds open at once. One over it is closed at once.", "4096", 1, math.MaxInt, ApplyRestart),
	plain("OHS_BOOTSTRAP_TENANT", "Bootstrap tenant", "Server", "Name of a tenant created at first start so an API key can be issued right away. Empty creates none.", "", false),

	// Storage
	plain("OHS_DATABASE_URL", "Database URL", "Storage", "Postgres connection URL.", "", true),
	plain("OHS_RAW_STORE", "Raw store", "Storage", "Where raw files go: file:///abs/path or s3://bucket?endpoint=&region=&path_style=true.", "file:///var/lib/ohs/raw", false),

	// Security
	plain("OHS_ENCRYPTION_KEY", "Encryption key", "Security", "32 random bytes, base64. Seals stored OAuth tokens and settings.", "", true),
	plain("OHS_ENCRYPTION_KEY_FILE", "Encryption key file", "Security", "A file holding the key, instead of OHS_ENCRYPTION_KEY. Created with a random key when missing.", "", false),
	{Name: "OHS_PREVIOUS_ENCRYPTION_KEYS", Label: "Previous encryption keys", Section: "Security", Type: TypeList, Secret: true, Apply: ApplyBootstrap,
		Help:     "Only while rotating the key: the keys it replaced, comma separated. They still open, never seal.",
		Validate: anyValue},

	// Console
	{Name: "OHS_CONSOLE", Label: "Admin console", Section: "Console", Type: TypeEnum, Values: []string{"true", "false", ConsoleReadOnly},
		Default: "false", Apply: ApplyBootstrap, Help: "Serve the admin console at /console: true, read-only (no write action) or false.",
		Validate: func(v string) error { _, _, err := consoleValue("OHS_CONSOLE")(v); return err }},
	plain("OHS_SETUP_TOKEN", "Setup token", "Console", "Opens the first run from a browser, once, while the instance has no tenant. 16 characters or more.", "", true),
	plain("OHS_SETUP_TOKEN_FILE", "Setup token file", "Console", "A file holding the setup token, instead of OHS_SETUP_TOKEN. Created with a random token when missing.", "", false),

	// Metrics
	boolSetting("OHS_METRICS", "Prometheus metrics", "Metrics", "Expose Prometheus metrics on /metrics.", "true", ApplyRestart),
	{Name: "OHS_METRICS_TOKEN", Label: "Metrics token", Section: "Metrics", Type: TypeString, Secret: true, Apply: ApplyRestart,
		Help:     "Bearer token a scrape of /metrics presents. 16 characters or more. Without it, or a metrics listener, /metrics refuses every scrape.",
		Validate: metricsToken},
	plain("OHS_METRICS_LISTEN", "Metrics listen address", "Metrics", "Serve /metrics on an address of its own instead of the API listener.", "", false),
	{Name: "OHS_OPS_LISTEN", Label: "Ops listen address", Section: "Metrics", Type: TypeString, Apply: ApplyBootstrap,
		DefaultNote: "127.0.0.1:8081 for a process without the api role, off with it",
		Help:        "Serves /healthz, /readyz and /metrics without a credential. off turns it off.",
		Validate:    anyValue},

	// Retention
	intSetting("OHS_RETENTION_DAYS", "Data retention (days)", "Retention", "Delete raw files and normalized data older than this many days. 0 keeps everything.", "0", 0, math.MaxInt, ApplyLive),
	intSetting("OHS_AUDIT_RETENTION_DAYS", "Audit retention (days)", "Retention", "Delete audit events older than this many days, once a day. 0 keeps every event.", "365", 0, math.MaxInt, ApplyLive),
	intSetting("OHS_TRACKING_RETENTION_DAYS", "Sync log retention (days)", "Retention", "Delete sync log rows and finished processing jobs older than this many days, once a day. A value under 14 counts as 14, a negative value keeps them for ever.", strconv.Itoa(DefaultTrackingRetentionDays), noLowerBound, MaxTrackingRetentionDays, ApplyLive),

	// Consent
	boolSetting("OHS_CONSENT_REQUIRED", "Require consent", "Consent", "Store nothing from a source until the user's consent for it is recorded.", "false", ApplyLive),

	// Ingest
	intSetting("OHS_INGEST_CONCURRENCY", "Concurrent uploads", "Ingest", "Device uploads one api process handles at once. More answer 503 with Retry-After.", strconv.Itoa(DefaultIngestConcurrency), 1, math.MaxInt, ApplyRestart),
	withNote(intSetting("OHS_INGEST_TENANT_CONCURRENCY", "Uploads per tenant", "Ingest", "The share of the concurrent uploads one tenant is sure to get, at most the number of concurrent uploads.", "", 1, math.MaxInt, ApplyRestart),
		"half of OHS_INGEST_CONCURRENCY, rounded up"),
	intSetting("OHS_INGEST_MAX_DECOMPRESSED_MIB", "Upload expansion cap (MiB)", "Ingest", "How far one uploaded part may expand, in MiB.", strconv.Itoa(DefaultIngestMaxDecompressedMiB), 1, MaxIngestMaxDecompressedMiB, ApplyRestart),

	// Sync
	intSetting("OHS_PULLS_PER_PROVIDER", "Pulls per provider", "Sync", "Pulls of one provider running at once, across every collect instance.", "4", 1, math.MaxInt, ApplyRestart),
	intSetting("OHS_BACKFILLS_PER_PROVIDER", "Backfills per provider", "Sync", "How many of those pulls may be backfills of new connections.", "2", 1, math.MaxInt, ApplyRestart),
	intSetting("OHS_PULL_CONCURRENCY", "Pull concurrency", "Sync", "Pulls one collect process runs at once.", strconv.Itoa(DefaultPullConcurrency), 1, MaxPullConcurrency, ApplyRestart),
	intSetting("OHS_SCHEDULE_BATCH", "Schedule batch", "Sync", "Pulls one scheduling pass queues, each minute.", strconv.Itoa(DefaultScheduleBatch), 1, MaxScheduleBatch, ApplyRestart),
	withNote(intSetting("OHS_BACKFILL_DAYS", "Backfill window (days)", "Sync", "How far back a new connection is pulled, for every provider. Unset, each provider keeps its own window.", "", 1, MaxBackfillDays, ApplyLive),
		"each provider keeps its own window, 30 days for most"),

	// Processing
	intSetting("OHS_ETL_CONCURRENCY", "Processing concurrency", "Processing", "Processing jobs each tier of one etl process runs at once. There are two tiers.", strconv.Itoa(DefaultETLConcurrency), 1, MaxETLConcurrency, ApplyRestart),
	intSetting("OHS_ETL_JOBS_PER_USER", "Jobs per user", "Processing", "Processing jobs of one user running at once, across every etl process. 0 for no cap.", strconv.Itoa(DefaultETLJobsPerUser), 0, MaxETLConcurrency, ApplyRestart),

	// Webhooks
	boolSetting("OHS_WEBHOOK_ALLOW_PRIVATE", "Allow private webhook targets", "Webhooks", "Let outgoing webhooks use plain http and reach private or loopback addresses. Turns off the SSRF guard: keep it false on a server that faces the internet.", "false", ApplyRestart),
	intSetting("OHS_WEBHOOK_CONCURRENCY", "Webhook concurrency", "Webhooks", "Webhook deliveries one etl process posts at once.", strconv.Itoa(DefaultWebhookConcurrency), 1, MaxWebhookConcurrency, ApplyRestart),
	withNote(intSetting("OHS_WEBHOOK_PER_ENDPOINT", "Deliveries per endpoint", "Webhooks", "How many deliveries go to one endpoint at once, across every etl process, at most the webhook concurrency.", "", 1, MaxWebhookConcurrency, ApplyRestart),
		"2, at most OHS_WEBHOOK_CONCURRENCY"),
	withNote(intSetting("OHS_WEBHOOK_PER_TENANT", "Deliveries per tenant", "Webhooks", "How many deliveries go to all the endpoints of one tenant at once, across every etl process, at most the webhook concurrency.", "", 1, MaxWebhookConcurrency, ApplyRestart),
		"half of OHS_WEBHOOK_CONCURRENCY, at least 1"),
	intSetting("OHS_WEBHOOK_PAUSE_AFTER", "Pause after failures", "Webhooks", "Failed attempts in a row after which a webhook endpoint is paused. 0 never pauses.", strconv.Itoa(DefaultWebhookPauseAfter), 0, MaxWebhookPauseAfter, ApplyLive),
	intSetting("OHS_WEBHOOK_ROTATION_GRACE_HOURS", "Secret rotation grace (hours)", "Webhooks", "Hours the secret a rotation replaced keeps signing next to the new one. 0 ends it at once.", strconv.Itoa(DefaultWebhookRotationGraceHours), 0, MaxWebhookRotationGraceHours, ApplyLive),

	// Event stream
	intSetting("OHS_STREAM_MAX", "Maximum event streams", "Event stream", "Event streams one api process holds open at once. 0 turns the event stream off.", strconv.Itoa(DefaultStreamMax), 0, MaxStreamMax, ApplyRestart),
	withNote(intSetting("OHS_STREAM_MAX_PER_TENANT", "Event streams per tenant", "Event stream", "Event streams of one tenant, at most the maximum.", "", 1, MaxStreamMax, ApplyRestart),
		"10, at most OHS_STREAM_MAX"),

	// Rate limits
	rateSetting("OHS_RATE_LIMIT_INBOUND", "Notifications per address", "Provider notifications per client address, rate/burst in requests per second. 0 disables it.", "30/120"),
	rateSetting("OHS_RATE_LIMIT_INBOUND_PROVIDER", "Notifications per provider", "Provider notifications per provider, whatever the address, rate/burst. 0 disables it.", "300/600"),
	rateSetting("OHS_RATE_LIMIT_OAUTH", "OAuth callbacks per address", "OAuth callbacks and first-run setup tries per client address, rate/burst. 0 disables it.", "2/20"),
	rateSetting("OHS_RATE_LIMIT_API", "API requests per credential", "Requests of one credential, an API key or the tokens of one user, rate/burst. 0 disables it.", "200/2000"),
	rateSetting("OHS_RATE_LIMIT_AUTH_FAILURES", "Refused credentials per address", "Refused keys or tokens per client address, rate/burst: past it the refused ones answer 429 instead of 401. Behind a reverse proxy this needs the trusted proxies. 0 disables it.", "1/60"),

	// Vault
	{Name: "OHS_VAULT_KEY_PROVIDER", Label: "Key provider", Section: "Vault", Type: TypeEnum, Values: keyProviders, Default: "local", Apply: ApplyBootstrap,
		Help:     "Key provider that holds the vault root key.",
		Validate: check(enumValue("OHS_VAULT_KEY_PROVIDER", keyProviders...))},
	plain("OHS_VAULT_ROOT_KEY", "Root key", "Vault", "Local provider: a 32 byte base64 root key, instead of the one derived from the encryption key.", "", true),
	plain("OHS_VAULT_ROOT_KEY_FILE", "Root key file", "Vault", "Local provider: a file holding the root key.", "", false),
	{Name: "OHS_VAULT_ROOT_KEY_PREVIOUS", Label: "Previous root keys", Section: "Vault", Type: TypeList, Secret: true, Apply: ApplyBootstrap,
		Help:     "Only while rotating the root key: the keys it replaced, comma separated. They still open, never wrap.",
		Validate: anyValue},
	plain("OHS_VAULT_HASHICORP_ADDR", "HashiCorp address", "Vault", "HashiCorp provider: address of the Vault server.", "", false),
	plain("OHS_VAULT_HASHICORP_MOUNT", "HashiCorp mount", "Vault", "HashiCorp provider: mount of the transit engine.", "transit", false),
	plain("OHS_VAULT_HASHICORP_KEY", "HashiCorp key", "Vault", "HashiCorp provider: transit key, created with derived=true.", "", false),
	plain("OHS_VAULT_HASHICORP_TOKEN_FILE", "HashiCorp token file", "Vault", "HashiCorp provider: a file holding the token. Else VAULT_TOKEN is read.", "", false),
	plain("OHS_VAULT_AZURE_KEY_ID", "Azure key id", "Vault", "Azure Key Vault provider: identifier of the key.", "", false),
	plain("OHS_VAULT_AWS_KEY_ARN", "AWS key ARN", "Vault", "AWS KMS provider: ARN of the key.", "", false),
	intSetting("OHS_VAULT_CACHE_USERS", "Key cache size", "Vault", "Unwrapped user keys kept in memory.", strconv.Itoa(DefaultVaultCacheUsers), 1, math.MaxInt, ApplyBootstrap),
	{Name: "OHS_VAULT_CACHE_TTL", Label: "Key cache lifetime", Section: "Vault", Type: TypeDuration, Default: "10m", Apply: ApplyBootstrap,
		Help:     "How long an unwrapped user key stays in memory, for example 10m.",
		Validate: check(positiveDuration("OHS_VAULT_CACHE_TTL"))},
}

// withNote says how a derived default is computed.
func withNote(s Setting, note string) Setting {
	s.DefaultNote = note
	return s
}

// Registry returns every server setting, grouped by section in the order of
// Sections. The slice is a copy; the entries are not to be changed.
func Registry() []Setting { return slices.Clone(registry) }

// LookupSetting finds a setting by its variable name.
func LookupSetting(name string) (Setting, bool) {
	for _, s := range registry {
		if s.Name == name {
			return s, true
		}
	}
	return Setting{}, false
}

// fileTwin lists the settings Load reads through secret(), the only ones
// whose _FILE twin means anything. A test keeps it equal to the secret()
// calls in the package.
var fileTwin = map[string]bool{
	"OHS_ENCRYPTION_KEY": true,
	"OHS_SETUP_TOKEN":    true,
	"OHS_VAULT_ROOT_KEY": true,
}

// EnvSet reports whether the environment sets the setting: its variable, or
// for a setting Load reads through secret() its _FILE twin, set and not empty.
func EnvSet(env func(string) (string, bool), name string) bool {
	names := []string{name}
	if fileTwin[name] {
		names = append(names, name+"_FILE")
	}
	for _, n := range names {
		if v, ok := env(n); ok && v != "" {
			return true
		}
	}
	return false
}

// Stacked returns the lookup Load reads through: for a registry setting that
// is not bootstrap, the environment when it sets it (the variable or its
// _FILE twin, not empty), else the stored value, else nothing, which makes
// Load use the default. Every other name goes straight to env.
func Stacked(env func(string) (string, bool), stored map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		s, ok := LookupSetting(name)
		if !ok || EnvSet(env, name) || s.Apply == ApplyBootstrap {
			return env(name)
		}
		if v, ok := stored[name]; ok {
			return v, true
		}
		return env(name)
	}
}
