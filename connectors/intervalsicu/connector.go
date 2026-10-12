// SPDX-License-Identifier: AGPL-3.0-only

// Package intervalsicu is the intervals.icu connector: OAuth connect, pulls
// of the athlete's activities and daily wellness records from the
// intervals.icu API v1, webhook notifications, and the mapping of the stored
// raw lines to canonical workouts and quantity samples.
//
// intervals.icu is itself an aggregator: athletes sync Garmin, Wahoo, Zwift,
// Coros, Suunto, Polar and others into it, so one connection reaches data from
// many devices. The upstream an activity came from is kept in its metadata.
//
// API docs: https://intervals.icu/api-docs.html and the forum guides "API
// access to Intervals.icu", "Intervals.icu OAuth support" and "Intervals.icu
// API Integration Cookbook" (forum.intervals.icu).
//
// Keys: OHS_INTERVALS_ICU_CLIENT_ID and OHS_INTERVALS_ICU_CLIENT_SECRET
// (required). Optional: OHS_INTERVALS_ICU_WEBHOOK_SECRET, the secret shown on
// the app's "Manage App" page, which intervals.icu puts in every webhook body;
// without it webhooks are refused and data still arrives with the scheduled
// pulls.
//
// intervals.icu issues access tokens without a refresh token and without a
// stated expiry: a token lasts until the athlete revokes the app or connects
// again. Refresh therefore always asks for a new connection.
package intervalsicu

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Slug is the provider name of this connector.
const Slug = "intervals_icu"

// Production endpoints. The consent page and the token endpoint live on the
// site, the API under /api/v1.
const (
	DefaultOAuthBase = "https://intervals.icu"
	DefaultAPIBase   = "https://intervals.icu/api/v1"
)

// Key names (OHS_INTERVALS_ICU_<KEY>).
const (
	KeyClientID      = "CLIENT_ID"
	KeyClientSecret  = "CLIENT_SECRET"
	KeyWebhookSecret = "WEBHOOK_SECRET"
)

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the production defaults.
type Options struct {
	ClientID, ClientSecret string
	// WebhookSecret is the optional webhook key.
	WebhookSecret string
	// OAuthBase serves the consent page and the token endpoint, APIBase the
	// API v1. Tests point both at an httptest fake.
	OAuthBase, APIBase string
	HTTPClient         *http.Client
	// RetryBase is the first wait of the transport error retry ladder (5 s).
	RetryBase time.Duration
	// ChunkDays is the span of one list request (30 days when zero).
	ChunkDays int
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector, connector.PushConnector
// and connector.NotifiedFetcher for intervals.icu.
type Connector struct {
	mu   sync.RWMutex
	keys connector.Config

	oauthBase, apiBase string
	httpc              *http.Client
	retryBase          time.Duration
	chunkDays          int
	now                func() time.Time
	sleep              func(ctx context.Context, d time.Duration) error
}

var (
	_ connector.OAuthConnector  = (*Connector)(nil)
	_ connector.PushConnector   = (*Connector)(nil)
	_ connector.NotifiedFetcher = (*Connector)(nil)
	_ connector.KeyedConnector  = (*Connector)(nil)
	_ connector.Configurable    = (*Connector)(nil)
	_ connector.Revoker         = (*Connector)(nil)
)

// New returns an intervals.icu connector.
func New(o Options) *Connector {
	c := &Connector{
		oauthBase: o.OAuthBase,
		apiBase:   o.APIBase,
		httpc:     o.HTTPClient,
		retryBase: o.RetryBase,
		chunkDays: o.ChunkDays,
		now:       o.Now,
		sleep:     o.Sleep,
	}
	if c.oauthBase == "" {
		c.oauthBase = DefaultOAuthBase
	}
	if c.apiBase == "" {
		c.apiBase = DefaultAPIBase
	}
	if c.httpc == nil {
		c.httpc = newHTTPClient()
	}
	if c.retryBase <= 0 {
		c.retryBase = 5 * time.Second
	}
	if c.chunkDays <= 0 {
		c.chunkDays = 30
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.sleep == nil {
		c.sleep = pause
	}
	if o.ClientID != "" || o.ClientSecret != "" {
		c.keys = connector.Config{
			KeyClientID: o.ClientID, KeyClientSecret: o.ClientSecret,
			KeyWebhookSecret: o.WebhookSecret,
		}
	}
	return c
}

// newHTTPClient builds the outbound client.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// Slug returns "intervals_icu".
func (c *Connector) Slug() string { return Slug }

// RequiredKeys lists the operator keys: CLIENT_ID and CLIENT_SECRET. The
// webhook secret is optional: pulls work without it.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// Enabled reports whether cfg holds both required keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != ""
}

// Configure hands the connector its keys (the OHS_INTERVALS_ICU_* values).
// It may be called again; the last call wins.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyClientSecret] == "" {
		return errors.New("intervals_icu: OHS_INTERVALS_ICU_CLIENT_ID and OHS_INTERVALS_ICU_CLIENT_SECRET are both required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = connector.Config{
		KeyClientID: cfg[KeyClientID], KeyClientSecret: cfg[KeyClientSecret],
		KeyWebhookSecret: cfg[KeyWebhookSecret],
	}
	return nil
}

// credentials returns the configured client id and secret.
func (c *Connector) credentials() (id, secret string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[KeyClientID], c.keys[KeyClientSecret]
}

// webhookSecret returns the configured webhook secret.
func (c *Connector) webhookSecret() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[KeyWebhookSecret]
}

// Map turns one stored raw line into canonical records. Pure. A line of an
// unknown data type gives an empty result and no error.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	res, err := mapRecord(raw.DataType, raw.Fields, raw.UserID)
	if err != nil {
		return model.MapResult{}, err
	}
	if res == nil {
		return model.MapResult{}, nil
	}
	return *res, nil
}
