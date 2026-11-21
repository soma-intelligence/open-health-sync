// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadAPILimits(t *testing.T) {
	c, err := load(t, base())
	if err != nil {
		t.Fatal(err)
	}
	if c.RateLimitAPI != DefaultRateLimitAPI || c.RateLimitAuthFailures != DefaultRateLimitAuthFailures || c.CompressionOff {
		t.Fatalf("defaults: %+v %+v %v", c.RateLimitAPI, c.RateLimitAuthFailures, c.CompressionOff)
	}
	if DefaultRateLimitAPI != (RateLimit{Rate: 200, Burst: 2000}) || DefaultRateLimitAuthFailures != (RateLimit{Rate: 1, Burst: 60}) {
		t.Fatalf("defaults changed: %+v %+v", DefaultRateLimitAPI, DefaultRateLimitAuthFailures)
	}
	m := base()
	m["OHS_RATE_LIMIT_API"] = "50/100"
	m["OHS_RATE_LIMIT_AUTH_FAILURES"] = "0"
	m["OHS_COMPRESSION"] = "false"
	c, err = load(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if c.RateLimitAPI != (RateLimit{Rate: 50, Burst: 100}) || c.RateLimitAuthFailures != (RateLimit{}) || !c.CompressionOff {
		t.Fatalf("set: %+v %+v %v", c.RateLimitAPI, c.RateLimitAuthFailures, c.CompressionOff)
	}
	m = base()
	m["OHS_RATE_LIMIT_API"] = "0"
	m["OHS_COMPRESSION"] = "true"
	c, err = load(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if c.RateLimitAPI != (RateLimit{}) || c.RateLimitAuthFailures != DefaultRateLimitAuthFailures || c.CompressionOff {
		t.Fatalf("api off: %+v %+v %v", c.RateLimitAPI, c.RateLimitAuthFailures, c.CompressionOff)
	}
	for name, bad := range map[string][]string{
		"OHS_RATE_LIMIT_API":           {"200", "200/0", "x/5"},
		"OHS_RATE_LIMIT_AUTH_FAILURES": {"1", "-1/60"},
		"OHS_COMPRESSION":              {"gzip", "2"},
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

func TestLoadWorkerTuning(t *testing.T) {
	c, err := load(t, base())
	if err != nil {
		t.Fatal(err)
	}
	if c.ETLConcurrency != 4 || c.ETLJobsPerUser != 1 || c.TrackingRetentionDays != DefaultTrackingRetentionDays ||
		c.PullConcurrency != 4 || c.ScheduleBatch != 1000 || c.BackfillDays != 0 {
		t.Fatalf("defaults: %+v", c)
	}
	if c.WebhookConcurrency != 8 || c.WebhookPerEndpoint != 2 || c.WebhookPauseAfter != 5 || c.WebhookRotationGrace != 24*time.Hour {
		t.Fatalf("webhook defaults: %d %d %d %s", c.WebhookConcurrency, c.WebhookPerEndpoint, c.WebhookPauseAfter, c.WebhookRotationGrace)
	}
	if c.WebhookPerTenant != 4 {
		t.Fatalf("OHS_WEBHOOK_PER_TENANT default = %d, want half of OHS_WEBHOOK_CONCURRENCY", c.WebhookPerTenant)
	}

	m := base()
	for k, v := range map[string]string{
		"OHS_ETL_CONCURRENCY":              "8",
		"OHS_ETL_JOBS_PER_USER":            "2",
		"OHS_TRACKING_RETENTION_DAYS":      "30",
		"OHS_PULL_CONCURRENCY":             "16",
		"OHS_SCHEDULE_BATCH":               "5000",
		"OHS_BACKFILL_DAYS":                "730",
		"OHS_WEBHOOK_CONCURRENCY":          "32",
		"OHS_WEBHOOK_PER_ENDPOINT":         "4",
		"OHS_WEBHOOK_PER_TENANT":           "20",
		"OHS_WEBHOOK_PAUSE_AFTER":          "10",
		"OHS_WEBHOOK_ROTATION_GRACE_HOURS": "1",
	} {
		m[k] = v
	}
	c, err = load(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if c.ETLConcurrency != 8 || c.ETLJobsPerUser != 2 || c.TrackingRetentionDays != 30 ||
		c.PullConcurrency != 16 || c.ScheduleBatch != 5000 || c.BackfillDays != 730 {
		t.Fatalf("set: %+v", c)
	}
	if c.WebhookConcurrency != 32 || c.WebhookPerEndpoint != 4 || c.WebhookPauseAfter != 10 || c.WebhookRotationGrace != time.Hour {
		t.Fatalf("webhook set: %d %d %d %s", c.WebhookConcurrency, c.WebhookPerEndpoint, c.WebhookPauseAfter, c.WebhookRotationGrace)
	}
	if c.WebhookPerTenant != 20 {
		t.Fatalf("OHS_WEBHOOK_PER_TENANT = %d, want 20", c.WebhookPerTenant)
	}
	if len(c.Providers) != 0 {
		t.Fatalf("settings leaked as providers: %v", c.Providers)
	}

	// The values that turn a feature off, as the packages behind them read
	// them: a negative number, since their zero means the default.
	m = base()
	m["OHS_ETL_JOBS_PER_USER"] = "0"
	m["OHS_TRACKING_RETENTION_DAYS"] = "-1"
	m["OHS_WEBHOOK_PAUSE_AFTER"] = "0"
	m["OHS_WEBHOOK_ROTATION_GRACE_HOURS"] = "0"
	c, err = load(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if c.ETLJobsPerUser >= 0 || c.TrackingRetentionDays >= 0 || c.WebhookPauseAfter >= 0 || c.WebhookRotationGrace >= 0 {
		t.Fatalf("off: %d %d %d %s", c.ETLJobsPerUser, c.TrackingRetentionDays, c.WebhookPauseAfter, c.WebhookRotationGrace)
	}

	// The largest values.
	m = base()
	m["OHS_BACKFILL_DAYS"] = "1825"
	m["OHS_WEBHOOK_CONCURRENCY"] = "3"
	m["OHS_WEBHOOK_PER_ENDPOINT"] = "3"
	m["OHS_WEBHOOK_PER_TENANT"] = "3"
	if _, err = load(t, m); err != nil {
		t.Fatal(err)
	}
	// Half the slots by default, at least one.
	for conc, want := range map[string]int{"1": 1, "3": 1, "32": 16} {
		m = base()
		m["OHS_WEBHOOK_CONCURRENCY"] = conc
		if c, err = load(t, m); err != nil || c.WebhookPerTenant != want {
			t.Fatalf("OHS_WEBHOOK_CONCURRENCY=%s: per tenant %d, %v, want %d", conc, c.WebhookPerTenant, err, want)
		}
	}

	for name, bad := range map[string][]string{
		"OHS_ETL_CONCURRENCY":              {"0", "-1", "65", "four", "1.5"},
		"OHS_ETL_JOBS_PER_USER":            {"-1", "65", "x"},
		"OHS_TRACKING_RETENTION_DAYS":      {"x", "1.5", "36501"},
		"OHS_PULL_CONCURRENCY":             {"0", "257", "x"},
		"OHS_SCHEDULE_BATCH":               {"0", "100001", "x"},
		"OHS_BACKFILL_DAYS":                {"0", "1826", "-30", "x"},
		"OHS_WEBHOOK_CONCURRENCY":          {"0", "257", "x"},
		"OHS_WEBHOOK_PER_ENDPOINT":         {"0", "9", "x"}, // over OHS_WEBHOOK_CONCURRENCY (8)
		"OHS_WEBHOOK_PER_TENANT":           {"0", "9", "x"},
		"OHS_WEBHOOK_PAUSE_AFTER":          {"-1", "1001", "x"},
		"OHS_WEBHOOK_ROTATION_GRACE_HOURS": {"-1", "169", "x"},
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

// Every OHS_ setting Load reads is reserved, so none is taken for the key
// of a provider.
func TestLoadSettingsAreNotProviders(t *testing.T) {
	m := base()
	m["OHS_MAX_CONNECTIONS"] = "100"
	m["OHS_INGEST_TENANT_CONCURRENCY"] = "2"
	m["OHS_INGEST_MAX_DECOMPRESSED_MIB"] = "32"
	c, err := load(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 0 {
		t.Fatalf("settings leaked as providers: %v", c.Providers)
	}
}
