// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"net"
	"net/netip"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// The settings of config handed to the packages that do the work. A Config
// built by Load holds every default; in one built by hand (tests) a zero
// field is the package's own default, except for the rate limits, where it
// is no limit.

// apiTuning sets the rate limits per credential and the compression of d.
func apiTuning(cfg config.Config, d *api.Deps) {
	d.RateLimitAPI = api.RateLimit(cfg.RateLimitAPI)
	d.RateLimitAPIOff = cfg.RateLimitAPI == config.RateLimit{}
	d.RateLimitAuthFailures = api.RateLimit(cfg.RateLimitAuthFailures)
	d.RateLimitAuthFailuresOff = cfg.RateLimitAuthFailures == config.RateLimit{}
	d.CompressionOff = cfg.CompressionOff
}

// etlConcurrency is how many jobs each ETL tier runs at once.
func etlConcurrency(cfg config.Config) int {
	if cfg.ETLConcurrency > 0 {
		return cfg.ETLConcurrency
	}
	return ETLConcurrency
}

// schedulerConfig is the pull scheduler's configuration.
func schedulerConfig(cfg config.Config, grace time.Duration) collect.SchedulerConfig {
	return collect.SchedulerConfig{
		ShutdownGrace: grace,
		Concurrency:   cfg.PullConcurrency,
		Batch:         cfg.ScheduleBatch,
		PerProvider:   cfg.PullsPerProvider, BackfillPerProvider: cfg.BackfillsPerProvider,
	}
}

// backfillWindow is how far back a new connection is pulled, zero to leave
// each provider its own window.
func backfillWindow(cfg config.Config) time.Duration {
	return time.Duration(max(cfg.BackfillDays, 0)) * 24 * time.Hour
}

// webhookWorkerConfig is the delivery worker's configuration.
func webhookWorkerConfig(cfg config.Config) webhook.WorkerConfig {
	return webhook.WorkerConfig{
		AllowPrivate: cfg.WebhookAllowPrivate,
		Concurrency:  cfg.WebhookConcurrency,
		PerEndpoint:  cfg.WebhookPerEndpoint,
		PerTenant:    cfg.WebhookPerTenant,
		PauseAfter:   cfg.WebhookPauseAfter,
	}
}

// webhookStoreOptions carries the rotation grace to the endpoint store. A
// negative grace ends the replaced secret at once.
func webhookStoreOptions(cfg config.Config) []webhook.StoreOption {
	if cfg.WebhookRotationGrace == 0 {
		return nil
	}
	return []webhook.StoreOption{webhook.WithRotationGrace(cfg.WebhookRotationGrace)}
}

// untrustedProxyWarning is what to log at start when the API listens for
// traffic from other hosts (listen is not a loopback address) and no reverse
// proxy is trusted: behind a proxy every client then has the proxy's
// address, so the limits per client address (refused credentials, inbound
// notifications, the OAuth callback) count them all as one. Empty when
// there is nothing to say.
func untrustedProxyWarning(listen string, trusted []netip.Prefix) string {
	if len(trusted) > 0 {
		return ""
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		host = listen
	}
	if host == "localhost" {
		return ""
	}
	if a, err := netip.ParseAddr(host); err == nil && a.IsLoopback() {
		return ""
	}
	return "OHS_TRUSTED_PROXIES is not set: if a reverse proxy stands in front of this server, every client " +
		"shares the proxy's address in the rate limits per client address, and one client presenting refused " +
		"credentials uses up the failures of all of them; list the proxy's address or network in OHS_TRUSTED_PROXIES"
}
