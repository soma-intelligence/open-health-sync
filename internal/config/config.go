// SPDX-License-Identifier: AGPL-3.0-only

// Package config loads Open Health Sync configuration from environment variables.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
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
	// IngestConcurrency bounds the device uploads one api process handles at
	// once (OHS_INGEST_CONCURRENCY, default 8).
	IngestConcurrency int
	// WebhookAllowPrivate lets webhook endpoints use plain http and reach
	// private and loopback addresses (OHS_WEBHOOK_ALLOW_PRIVATE, default false).
	WebhookAllowPrivate bool
	Providers           map[string]ProviderKeys // key = lower-case slug
}

// MinMetricsToken is the shortest OHS_METRICS_TOKEN accepted.
const MinMetricsToken = 16

// DefaultIngestConcurrency is OHS_INGEST_CONCURRENCY when unset.
const DefaultIngestConcurrency = 8

// reserved are the OHS_ variables (without prefix) that are never provider settings.
var reserved = map[string]bool{
	"DATABASE_URL":          true,
	"ENCRYPTION_KEY":        true,
	"PUBLIC_URL":            true,
	"RAW_STORE":             true,
	"ROLES":                 true,
	"BOOTSTRAP_TENANT":      true,
	"LISTEN":                true,
	"LOG_LEVEL":             true,
	"METRICS":               true,
	"CONSOLE":               true,
	"METRICS_TOKEN":         true,
	"METRICS_LISTEN":        true,
	"RETENTION_DAYS":        true,
	"INGEST_CONCURRENCY":    true,
	"WEBHOOK_ALLOW_PRIVATE": true,
	"MCP_URL":               true, // client side settings of `ohs mcp`
	"MCP_KEY":               true,
	"HTTP_PORT":             true, // read by docker compose only (host port)
	"HTTP_BIND":             true, // read by docker compose only (host address)
	"VERSION":               true, // read by docker compose only (image tag)
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

	c.IngestConcurrency = DefaultIngestConcurrency
	if v := get("OHS_INGEST_CONCURRENCY", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("OHS_INGEST_CONCURRENCY: %q is not a positive integer", v)
		}
		c.IngestConcurrency = n
	}

	if v := get("OHS_WEBHOOK_ALLOW_PRIVATE", ""); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("OHS_WEBHOOK_ALLOW_PRIVATE: %q is not a boolean", v)
		}
		c.WebhookAllowPrivate = b
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
