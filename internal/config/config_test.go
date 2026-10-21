// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

var goodKey = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

func load(t *testing.T, kv map[string]string) (Config, error) {
	t.Helper()
	var environ []string
	for k, v := range kv {
		environ = append(environ, k+"="+v)
	}
	return Load(func(k string) (string, bool) { v, ok := kv[k]; return v, ok }, environ)
}

func base() map[string]string {
	return map[string]string{"OHS_DATABASE_URL": "postgres://x", "OHS_ENCRYPTION_KEY": goodKey}
}

func TestLoadDefaults(t *testing.T) {
	c, err := load(t, base())
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicURL != "http://localhost:8080" || c.RawStore != "file:///var/lib/ohs/raw" ||
		c.Listen != ":8080" || c.LogLevel != "info" || !c.Metrics || c.RetentionDays != 0 {
		t.Fatalf("bad defaults: %+v", c)
	}
	if len(c.Roles) != 3 || !c.Roles["api"] || !c.Roles["collect"] || !c.Roles["etl"] {
		t.Fatalf("roles: %v", c.Roles)
	}
	if len(c.EncryptionKey) != 32 || len(c.Providers) != 0 {
		t.Fatalf("key/providers: %d %v", len(c.EncryptionKey), c.Providers)
	}
}

// render.yaml lets Render generate OHS_ENCRYPTION_KEY (generateValue: a
// base64-encoded 256-bit value). The example its Blueprint spec shows must load.
func TestLoadAcceptsPlatformGeneratedKey(t *testing.T) {
	m := base()
	m["OHS_ENCRYPTION_KEY"] = "B0jrphAPOY7pg92AN0c9MN4yecczLMdwnx4OkA1KFUk="
	c, err := load(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.EncryptionKey) != 32 {
		t.Fatalf("key length %d", len(c.EncryptionKey))
	}
}

func TestLoadRejectsBadKey(t *testing.T) {
	for _, k := range []string{"!!notbase64!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		m := base()
		m["OHS_ENCRYPTION_KEY"] = k
		_, err := load(t, m)
		if err == nil || !strings.Contains(err.Error(), "OHS_ENCRYPTION_KEY") {
			t.Fatalf("key %q: %v", k, err)
		}
	}
	m := base()
	delete(m, "OHS_ENCRYPTION_KEY")
	if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_ENCRYPTION_KEY") {
		t.Fatalf("missing key: %v", err)
	}
}

func TestLoadMissingDB(t *testing.T) {
	m := base()
	delete(m, "OHS_DATABASE_URL")
	if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_DATABASE_URL") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadUnknownRole(t *testing.T) {
	m := base()
	m["OHS_ROLES"] = "api,foo"
	if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "foo") {
		t.Fatalf("got %v", err)
	}
	m["OHS_ROLES"] = "etl, api"
	c, err := load(t, m)
	if err != nil || len(c.Roles) != 2 || !c.Roles["etl"] || !c.Roles["api"] {
		t.Fatalf("roles %v %v", c.Roles, err)
	}
}

func TestLoadProviderKeys(t *testing.T) {
	m := base()
	m["OHS_WITHINGS_CLIENT_ID"] = "a"
	m["OHS_WITHINGS_CLIENT_SECRET"] = "b"
	m["OHS_ROLES"] = "api"
	m["OHS_LISTEN"] = ":9"
	m["OHS_RETENTION_DAYS"] = "30"
	m["OHS_MCP_URL"] = "http://x"
	m["OHS_MCP_KEY"] = "k"
	m["OHS_METRICS"] = "false"
	m["OHS_BOOTSTRAP_TENANT"] = "acme"
	m["OHS_HTTP_PORT"] = "8081"    // compose only
	m["OHS_HTTP_BIND"] = "0.0.0.0" // compose only
	m["OHS_VERSION"] = "0.0.1"     // compose only
	m["OHS_WEBHOOK_ALLOW_PRIVATE"] = "true"
	m["OTHER"] = "z"
	c, err := load(t, m)
	if err != nil {
		t.Fatal(err)
	}
	w := c.Providers["withings"]
	if w["CLIENT_ID"] != "a" || w["CLIENT_SECRET"] != "b" {
		t.Fatalf("withings: %v", w)
	}
	if len(c.Providers) != 1 {
		t.Fatalf("reserved names leaked as providers: %v", c.Providers)
	}
	if c.Listen != ":9" || c.RetentionDays != 30 || c.Metrics || c.BootstrapTenant != "acme" || !c.WebhookAllowPrivate {
		t.Fatalf("%+v", c)
	}
}

func TestLoadWebhookAllowPrivate(t *testing.T) {
	c, err := load(t, base())
	if err != nil || c.WebhookAllowPrivate {
		t.Fatalf("default: %v %v", c.WebhookAllowPrivate, err)
	}
	m := base()
	m["OHS_WEBHOOK_ALLOW_PRIVATE"] = "maybe"
	if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_WEBHOOK_ALLOW_PRIVATE") {
		t.Fatalf("bad value: %v", err)
	}
}

func TestLoadTracing(t *testing.T) {
	c, err := load(t, base())
	if err != nil || c.Tracing || c.TracingProtocol != "" {
		t.Fatalf("off by default: %v %q %v", c.Tracing, c.TracingProtocol, err)
	}
	for _, tc := range []struct {
		env      map[string]string
		on       bool
		protocol string
	}{
		{map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318"}, true, TracingHTTP},
		{map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://collector:4318/v1/traces"}, true, TracingHTTP},
		{map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf"}, true, TracingHTTP},
		{map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4317", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"}, true, TracingGRPC},
		{map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4317", "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
			"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "grpc"}, true, TracingGRPC},
		{map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_SDK_DISABLED": "TRUE"}, false, ""},
		{map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_SDK_DISABLED": "false"}, true, TracingHTTP},
		{map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ""}, false, ""},
		{map[string]string{"OTEL_SERVICE_NAME": "ohs-eu", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"}, false, ""},
	} {
		m := base()
		for k, v := range tc.env {
			m[k] = v
		}
		c, err := load(t, m)
		if err != nil || c.Tracing != tc.on || c.TracingProtocol != tc.protocol {
			t.Fatalf("%v: got %v %q %v", tc.env, c.Tracing, c.TracingProtocol, err)
		}
	}
	m := base()
	m["OTEL_EXPORTER_OTLP_ENDPOINT"] = "http://c:4318"
	m["OTEL_EXPORTER_OTLP_PROTOCOL"] = "http/json"
	if _, err := load(t, m); !errors.Is(err, ErrTracing) || !strings.Contains(err.Error(), "OTEL_EXPORTER_OTLP_PROTOCOL") {
		t.Fatalf("http/json: %v", err)
	}
}

func TestLoadRetentionDays(t *testing.T) {
	for _, v := range []string{"-1", "abc", "1.5"} {
		m := base()
		m["OHS_RETENTION_DAYS"] = v
		if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_RETENTION_DAYS") {
			t.Fatalf("%q: %v", v, err)
		}
	}
}

func TestLoadConsole(t *testing.T) {
	c, err := load(t, base())
	if err != nil || c.Console {
		t.Fatalf("default: %v %v", c.Console, err)
	}
	m := base()
	m["OHS_CONSOLE"] = "true"
	c, err = load(t, m)
	if err != nil || !c.Console {
		t.Fatalf("true: %v %v", c.Console, err)
	}
	if len(c.Providers) != 0 {
		t.Fatalf("OHS_CONSOLE leaked as a provider: %v", c.Providers)
	}
	if c.ConsoleReadOnly {
		t.Fatal("true is the managing console")
	}
	m["OHS_CONSOLE"] = "read-only"
	c, err = load(t, m)
	if err != nil || !c.Console || !c.ConsoleReadOnly {
		t.Fatalf("read-only: %v %v %v", c.Console, c.ConsoleReadOnly, err)
	}
	m["OHS_CONSOLE"] = "false"
	c, err = load(t, m)
	if err != nil || c.Console || c.ConsoleReadOnly {
		t.Fatalf("false: %v %v %v", c.Console, c.ConsoleReadOnly, err)
	}
	m["OHS_CONSOLE"] = "maybe"
	if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_CONSOLE") ||
		!strings.Contains(err.Error(), "read-only") {
		t.Fatalf("bad value: %v", err)
	}
}

func TestLoadBadMetrics(t *testing.T) {
	m := base()
	m["OHS_METRICS"] = "maybe"
	if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_METRICS") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadIngestConcurrency(t *testing.T) {
	c, err := load(t, base())
	if err != nil || c.IngestConcurrency != DefaultIngestConcurrency {
		t.Fatalf("default: %d %v", c.IngestConcurrency, err)
	}
	m := base()
	m["OHS_INGEST_CONCURRENCY"] = "2"
	c, err = load(t, m)
	if err != nil || c.IngestConcurrency != 2 {
		t.Fatalf("set: %d %v", c.IngestConcurrency, err)
	}
	if len(c.Providers) != 0 {
		t.Fatalf("OHS_INGEST_CONCURRENCY leaked as a provider: %v", c.Providers)
	}
	for _, v := range []string{"0", "-1", "abc"} {
		m["OHS_INGEST_CONCURRENCY"] = v
		if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_INGEST_CONCURRENCY") {
			t.Fatalf("%q: %v", v, err)
		}
	}
}

func TestLoadPublicURL(t *testing.T) {
	for _, v := range []string{"localhost:8080", "ftp://x", "https://", "https://x/?a=1", "https://x/#f", "not a url"} {
		m := base()
		m["OHS_PUBLIC_URL"] = v
		if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_PUBLIC_URL") {
			t.Errorf("%q: got %v, want an OHS_PUBLIC_URL error", v, err)
		}
	}
	for _, v := range []string{"https://health.example.org", "https://example.org/ohs/", "http://192.168.1.20:8080"} {
		m := base()
		m["OHS_PUBLIC_URL"] = v
		if _, err := load(t, m); err != nil {
			t.Errorf("%q: %v", v, err)
		}
	}
}

func TestLoadLogLevel(t *testing.T) {
	m := base()
	m["OHS_LOG_LEVEL"] = "verbose"
	if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_LOG_LEVEL") {
		t.Fatalf("got %v", err)
	}
	m["OHS_LOG_LEVEL"] = "DEBUG"
	if _, err := load(t, m); err != nil {
		t.Fatal(err)
	}
}

// A key error tells the operator how to make a key.
func TestLoadKeyErrorsSayHowToFix(t *testing.T) {
	for _, k := range []string{"", "!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		m := base()
		m["OHS_ENCRYPTION_KEY"] = k
		_, err := load(t, m)
		if err == nil || !strings.Contains(err.Error(), "openssl rand -base64 32") {
			t.Errorf("key %q: %v", k, err)
		}
	}
}

func TestLoadSentinels(t *testing.T) {
	m := base()
	m["OHS_PUBLIC_URL"] = "localhost:1"
	if _, err := load(t, m); !errors.Is(err, ErrPublicURL) {
		t.Fatalf("public url: %v", err)
	}
	m = base()
	m["OHS_LOG_LEVEL"] = "loud"
	if _, err := load(t, m); !errors.Is(err, ErrLogLevel) {
		t.Fatalf("log level: %v", err)
	}
	if CheckPublicURL("https://x/?q") == nil || CheckPublicURL("https://x") != nil {
		t.Fatal("CheckPublicURL")
	}
}

func TestLoadMultiWordSlug(t *testing.T) {
	RegisterSlug("multi_word")
	RegisterSlug("multi_word_longer")
	RegisterSlug("single") // no underscore: nothing to declare
	m := base()
	m["OHS_MULTI_WORD_CLIENT_ID"] = "a"
	m["OHS_MULTI_WORD_LONGER_CLIENT_ID"] = "b"
	m["OHS_MULTI_OTHER"] = "c"
	m["OHS_WITHINGS_CLIENT_ID"] = "d"
	c, err := load(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Providers["multi_word"]["CLIENT_ID"]; got != "a" {
		t.Fatalf("multi_word: %v", c.Providers["multi_word"])
	}
	if got := c.Providers["multi_word_longer"]["CLIENT_ID"]; got != "b" {
		t.Fatalf("multi_word_longer (longest slug wins): %v", c.Providers)
	}
	if c.Providers["multi"]["OTHER"] != "c" || c.Providers["withings"]["CLIENT_ID"] != "d" {
		t.Fatalf("first underscore split: %v", c.Providers)
	}
	if _, ok := c.Providers["multi"]["WORD_CLIENT_ID"]; ok {
		t.Fatalf("multi-word key filed under its first word: %v", c.Providers)
	}
}

func TestLoadMetricsToken(t *testing.T) {
	c, err := load(t, base())
	if err != nil || c.MetricsToken != "" || c.MetricsListen != "" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	m := base()
	m["OHS_METRICS_TOKEN"] = "short"
	if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_METRICS_TOKEN") {
		t.Fatalf("short token: %v", err)
	}
	m["OHS_METRICS_TOKEN"] = "0123456789abcdef"
	m["OHS_METRICS_LISTEN"] = "127.0.0.1:9464"
	c, err = load(t, m)
	if err != nil || c.MetricsToken != "0123456789abcdef" || c.MetricsListen != "127.0.0.1:9464" {
		t.Fatalf("token and listen: %+v %v", c, err)
	}
	if len(c.Providers) != 0 {
		t.Fatalf("metrics settings read as a provider: %v", c.Providers)
	}
	m["OHS_METRICS_LISTEN"] = ":8080"
	if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_METRICS_LISTEN") {
		t.Fatalf("same address as OHS_LISTEN: %v", err)
	}
}

func TestLoadSetupToken(t *testing.T) {
	c, err := load(t, base())
	if err != nil || c.SetupToken != "" {
		t.Fatalf("default: %q %v", c.SetupToken, err)
	}
	m := base()
	m["OHS_SETUP_TOKEN"] = "fifteen-chars.."
	_, err = load(t, m)
	if err == nil || !strings.Contains(err.Error(), "OHS_SETUP_TOKEN") {
		t.Fatalf("short token: %v", err)
	}
	if strings.Contains(err.Error(), m["OHS_SETUP_TOKEN"]) {
		t.Fatalf("the error repeats the secret: %v", err)
	}
	m["OHS_SETUP_TOKEN"] = "0123456789abcdef"
	c, err = load(t, m)
	if err != nil || c.SetupToken != "0123456789abcdef" {
		t.Fatalf("token: %q %v", c.SetupToken, err)
	}
	if len(c.Providers) != 0 {
		t.Fatalf("OHS_SETUP_TOKEN read as a provider setting: %v", c.Providers)
	}
}

func TestLoadRateLimits(t *testing.T) {
	c, err := load(t, base())
	if err != nil {
		t.Fatal(err)
	}
	if c.RateLimitInbound != DefaultRateLimitInbound || c.RateLimitInboundProvider != DefaultRateLimitInboundProvider ||
		c.RateLimitOAuth != DefaultRateLimitOAuth || len(c.TrustedProxies) != 0 {
		t.Fatalf("defaults: %+v %+v %+v %v", c.RateLimitInbound, c.RateLimitInboundProvider, c.RateLimitOAuth, c.TrustedProxies)
	}
	m := base()
	m["OHS_RATE_LIMIT_INBOUND"] = "10/50"
	m["OHS_RATE_LIMIT_INBOUND_PROVIDER"] = "0"
	m["OHS_RATE_LIMIT_OAUTH"] = "0.5/5"
	m["OHS_TRUSTED_PROXIES"] = " 10.0.0.0/8, 192.0.2.7 ,fd00::/8"
	c, err = load(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if c.RateLimitInbound != (RateLimit{Rate: 10, Burst: 50}) || c.RateLimitInboundProvider != (RateLimit{}) ||
		c.RateLimitOAuth != (RateLimit{Rate: 0.5, Burst: 5}) {
		t.Fatalf("set: %+v %+v %+v", c.RateLimitInbound, c.RateLimitInboundProvider, c.RateLimitOAuth)
	}
	want := []string{"10.0.0.0/8", "192.0.2.7/32", "fd00::/8"}
	if len(c.TrustedProxies) != len(want) {
		t.Fatalf("proxies: %v", c.TrustedProxies)
	}
	for i, p := range c.TrustedProxies {
		if p.String() != want[i] {
			t.Fatalf("proxies: %v", c.TrustedProxies)
		}
	}
	if len(c.Providers) != 0 {
		t.Fatalf("rate limit settings leaked as providers: %v", c.Providers)
	}
	for name, bad := range map[string][]string{
		"OHS_RATE_LIMIT_INBOUND":          {"30", "30/", "/5", "-1/5", "30/0", "30/1.5", "abc/5", "NaN/5", "Inf/5", "30/5/1"},
		"OHS_RATE_LIMIT_INBOUND_PROVIDER": {"x"},
		"OHS_RATE_LIMIT_OAUTH":            {"2/-1"},
		"OHS_TRUSTED_PROXIES":             {"10.0.0.0/33", "proxy.local", "10.0.0.0/8,,x"},
	} {
		for _, v := range bad {
			m := base()
			m[name] = v
			if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("%s=%q: %v", name, v, err)
			}
		}
	}
}

func TestLoadPreviousKeys(t *testing.T) {
	other := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	third := base64.StdEncoding.EncodeToString([]byte("ABCDEF0123456789ABCDEF0123456789"))
	m := base()
	c, err := load(t, m)
	if err != nil || len(c.PreviousEncryptionKeys) != 0 {
		t.Fatalf("unset: %v %d", err, len(c.PreviousEncryptionKeys))
	}
	m["OHS_PREVIOUS_ENCRYPTION_KEYS"] = " " + other + " , " + third + ","
	c, err = load(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.PreviousEncryptionKeys) != 2 || string(c.PreviousEncryptionKeys[0]) != "abcdef0123456789abcdef0123456789" {
		t.Fatalf("previous keys: %d", len(c.PreviousEncryptionKeys))
	}
	if len(c.Providers) != 0 {
		t.Fatalf("OHS_PREVIOUS_ENCRYPTION_KEYS read as a provider: %v", c.Providers)
	}
}

func TestLoadRejectsBadPreviousKeys(t *testing.T) {
	other := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	for _, v := range []string{
		"!!notbase64!!",
		other + "," + base64.StdEncoding.EncodeToString([]byte("short")),
		goodKey,             // the active key again
		other + "," + other, // twice
	} {
		m := base()
		m["OHS_PREVIOUS_ENCRYPTION_KEYS"] = v
		_, err := load(t, m)
		if !errors.Is(err, ErrPreviousEncryptionKeys) {
			t.Fatalf("%q: %v", v, err)
		}
		// An error names the entry, never its value.
		if strings.Contains(err.Error(), other) || strings.Contains(err.Error(), goodKey) {
			t.Fatalf("the error echoes a key: %v", err)
		}
	}
	m := base()
	m["OHS_PREVIOUS_ENCRYPTION_KEYS"] = other + ",xx"
	_, err := load(t, m)
	if err == nil || !strings.Contains(err.Error(), "entry 2") {
		t.Fatalf("the error names the entry: %v", err)
	}
}

// The key hint points at rotation, not at keeping the key forever.
func TestKeyHintPointsAtRotation(t *testing.T) {
	m := base()
	m["OHS_ENCRYPTION_KEY"] = "!!"
	_, err := load(t, m)
	if err == nil || !strings.Contains(err.Error(), "docs/key-rotation.md") {
		t.Fatalf("got %v", err)
	}
}
