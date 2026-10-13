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
	m["OHS_CONSOLE"] = "maybe"
	if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_CONSOLE") {
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
