// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

func loaded(t *testing.T, kv map[string]string) config.Config {
	t.Helper()
	env := map[string]string{
		"OHS_DATABASE_URL":   "postgres://x",
		"OHS_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}
	for k, v := range kv {
		env[k] = v
	}
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, nil)
	require.NoError(t, err)
	return cfg
}

// The defaults config documents are the ones of the packages that do the
// work: a default changed on one side only fails here.
func TestConfigDefaultsMatchThePackages(t *testing.T) {
	require.Equal(t, api.DefaultRateLimitAPI, api.RateLimit(config.DefaultRateLimitAPI))
	require.Equal(t, api.DefaultRateLimitAuthFailures, api.RateLimit(config.DefaultRateLimitAuthFailures))
	require.Equal(t, ETLConcurrency, config.DefaultETLConcurrency)
	require.Equal(t, queue.DefaultETLPerUser, config.DefaultETLJobsPerUser)
	require.Equal(t, collect.DefaultPullConcurrency, config.DefaultPullConcurrency)
	require.Equal(t, collect.DefaultScheduleBatch, config.DefaultScheduleBatch)
	require.Equal(t, collect.MaxBackfillWindow, time.Duration(config.MaxBackfillDays)*24*time.Hour)
	require.Equal(t, webhook.DefaultConcurrency, config.DefaultWebhookConcurrency)
	require.Equal(t, webhook.DefaultPerEndpoint, config.DefaultWebhookPerEndpoint)
	require.Equal(t, webhook.DefaultPerTenantDivisor, config.DefaultWebhookPerTenantDivisor)
	require.Equal(t, webhook.DefaultPauseAfter, config.DefaultWebhookPauseAfter)
	require.Equal(t, webhook.RotationGrace, time.Duration(config.DefaultWebhookRotationGraceHours)*time.Hour)
}

func TestAPITuning(t *testing.T) {
	// Defaults: both limits on at their default, responses compressed.
	var d api.Deps
	apiTuning(loaded(t, nil), &d)
	require.Equal(t, api.DefaultRateLimitAPI, d.RateLimitAPI)
	require.Equal(t, api.DefaultRateLimitAuthFailures, d.RateLimitAuthFailures)
	require.False(t, d.RateLimitAPIOff)
	require.False(t, d.RateLimitAuthFailuresOff)
	require.False(t, d.CompressionOff)

	d = api.Deps{}
	apiTuning(loaded(t, map[string]string{
		"OHS_RATE_LIMIT_API": "50/100", "OHS_RATE_LIMIT_AUTH_FAILURES": "0", "OHS_COMPRESSION": "false",
	}), &d)
	require.Equal(t, api.RateLimit{Rate: 50, Burst: 100}, d.RateLimitAPI)
	require.False(t, d.RateLimitAPIOff)
	require.True(t, d.RateLimitAuthFailuresOff)
	require.True(t, d.CompressionOff)

	d = api.Deps{}
	apiTuning(loaded(t, map[string]string{"OHS_RATE_LIMIT_API": "0"}), &d)
	require.True(t, d.RateLimitAPIOff)
	require.False(t, d.RateLimitAuthFailuresOff)

	// A Config not built by Load has no limit, like the other rate limits.
	d = api.Deps{}
	apiTuning(config.Config{}, &d)
	require.True(t, d.RateLimitAPIOff)
	require.True(t, d.RateLimitAuthFailuresOff)
	require.False(t, d.CompressionOff)
}

func TestWorkerTuning(t *testing.T) {
	// A Config not built by Load leaves every option at its zero, which each
	// package reads as its default.
	var zero config.Config
	require.Equal(t, ETLConcurrency, etlConcurrency(zero))
	require.Equal(t, collect.SchedulerConfig{ShutdownGrace: time.Second}, schedulerConfig(zero, time.Second))
	require.Zero(t, backfillWindow(zero))
	require.Equal(t, webhook.WorkerConfig{}, webhookWorkerConfig(zero))
	require.Empty(t, webhookStoreOptions(zero))

	cfg := loaded(t, map[string]string{
		"OHS_ETL_CONCURRENCY": "8", "OHS_PULL_CONCURRENCY": "16", "OHS_SCHEDULE_BATCH": "5000",
		"OHS_PULLS_PER_PROVIDER": "6", "OHS_BACKFILLS_PER_PROVIDER": "3", "OHS_BACKFILL_DAYS": "730",
		"OHS_WEBHOOK_CONCURRENCY": "32", "OHS_WEBHOOK_PER_ENDPOINT": "4", "OHS_WEBHOOK_PER_TENANT": "12", "OHS_WEBHOOK_PAUSE_AFTER": "0",
		"OHS_WEBHOOK_ALLOW_PRIVATE": "true", "OHS_WEBHOOK_ROTATION_GRACE_HOURS": "0",
	})
	require.Equal(t, 8, etlConcurrency(cfg))
	require.Equal(t, collect.SchedulerConfig{
		ShutdownGrace: time.Second, Concurrency: 16, Batch: 5000, PerProvider: 6, BackfillPerProvider: 3,
	}, schedulerConfig(cfg, time.Second))
	require.Equal(t, 730*24*time.Hour, backfillWindow(cfg))
	wc := webhookWorkerConfig(cfg)
	require.Equal(t, 32, wc.Concurrency)
	require.Equal(t, 4, wc.PerEndpoint)
	require.Equal(t, 12, wc.PerTenant)
	require.Negative(t, wc.PauseAfter, "0 turns the pause off: the worker reads a negative value so")
	require.True(t, wc.AllowPrivate)
	require.Len(t, webhookStoreOptions(cfg), 1)

	// The defaults of Load are the packages' own.
	def := loaded(t, nil)
	require.Equal(t, ETLConcurrency, etlConcurrency(def))
	require.Equal(t, collect.DefaultPullConcurrency, schedulerConfig(def, 0).Concurrency)
	require.Equal(t, collect.DefaultScheduleBatch, schedulerConfig(def, 0).Batch)
	require.Zero(t, backfillWindow(def))
	require.Equal(t, webhook.WorkerConfig{
		Concurrency: webhook.DefaultConcurrency, PerEndpoint: webhook.DefaultPerEndpoint,
		PerTenant: webhook.DefaultConcurrency / webhook.DefaultPerTenantDivisor, PauseAfter: webhook.DefaultPauseAfter,
	}, webhookWorkerConfig(def))
}

// An instance that takes traffic from other hosts with no trusted proxy
// listed is told so at start: behind a reverse proxy every client then
// shares the proxy's address in the limits per address.
func TestUntrustedProxyWarning(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}
	for listen, want := range map[string]bool{
		":8080": true, "0.0.0.0:8080": true, "[::]:8080": true, "10.0.0.5:8080": true, "ohs.internal:8080": true,
		"127.0.0.1:8080": false, "[::1]:8080": false, "localhost:8080": false,
	} {
		require.Equal(t, want, untrustedProxyWarning(listen, nil) != "", listen)
		require.Empty(t, untrustedProxyWarning(listen, trusted), listen+" with a trusted proxy")
	}
	require.Contains(t, untrustedProxyWarning(":8080", nil), "OHS_TRUSTED_PROXIES")
}
