// SPDX-License-Identifier: AGPL-3.0-only

// Package polar is the Polar connector: OAuth connect, user registration and
// pulls from the Polar AccessLink API v3, AccessLink webhooks, and the
// mapping of the stored raw lines to canonical records.
//
// API docs: https://www.polar.com/accesslink-api/
//
// Keys: OHS_POLAR_CLIENT_ID and OHS_POLAR_CLIENT_SECRET (required), and
// OHS_POLAR_WEBHOOK_SECRET (optional: the signature_secret_key Polar returns
// once, when the client's webhook is created). They are handed over by the
// collect role with Configure (or set with Options in tests).
//
// Polar access tokens do not expire in practice (their lifetime is years)
// and there is no refresh token: a token Polar rejects means the user has to
// connect again. A user must also be registered with the client
// (POST /v3/users) before any data endpoint answers; Exchange does that
// right after the token is issued.
//
// Only the non-transactional endpoints are used. They return a rolling
// window of recent data (exercises: 30 days, sleep and Nightly Recharge: 28
// days, daily activity: the last 365 days), so a backfill cannot reach
// further back than that. See docs/connectors/polar.md.
package polar

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
const Slug = "polar"

// Production endpoints.
const (
	// DefaultAuthBase serves the consent page.
	DefaultAuthBase = "https://flow.polar.com"
	// DefaultTokenBase serves the token endpoint.
	DefaultTokenBase = "https://polarremote.com"
	// DefaultAPIBase serves AccessLink v3.
	DefaultAPIBase = "https://www.polaraccesslink.com"
)

// Key names (OHS_POLAR_<KEY>).
const (
	KeyClientID     = "CLIENT_ID"
	KeyClientSecret = "CLIENT_SECRET"
	// KeyWebhookSecret is optional. When set, every webhook delivery must
	// carry a valid Polar-Webhook-Signature.
	KeyWebhookSecret = "WEBHOOK_SECRET"
)

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the production defaults.
type Options struct {
	ClientID, ClientSecret, WebhookSecret string
	// AuthBase serves the consent page, TokenBase the token endpoint,
	// APIBase AccessLink. Tests point all three at an httptest fake.
	AuthBase, TokenBase, APIBase string
	HTTPClient                   *http.Client
	// RetryBase is the first wait of the transport error retry ladder (10 s).
	RetryBase time.Duration
	// ChunkDelay paces consecutive request windows. Negative disables the
	// pause; zero takes the default.
	ChunkDelay time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries; tests replace it so retries do
	// not sleep for real.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector, connector.PushConnector and
// connector.Revoker for Polar.
type Connector struct {
	mu   sync.RWMutex
	keys connector.Config

	authBase, tokenBase, apiBase string
	httpc                        *http.Client
	retryBase                    time.Duration
	chunkDelay                   time.Duration
	now                          func() time.Time
	sleep                        func(ctx context.Context, d time.Duration) error

	mapper *mapper
}

var (
	_ connector.OAuthConnector = (*Connector)(nil)
	_ connector.PushConnector  = (*Connector)(nil)
	_ connector.KeyedConnector = (*Connector)(nil)
	_ connector.Configurable   = (*Connector)(nil)
	_ connector.Revoker        = (*Connector)(nil)
)

// New returns a Polar connector.
func New(o Options) *Connector {
	c := &Connector{
		authBase:   o.AuthBase,
		tokenBase:  o.TokenBase,
		apiBase:    o.APIBase,
		httpc:      o.HTTPClient,
		retryBase:  o.RetryBase,
		chunkDelay: o.ChunkDelay,
		now:        o.Now,
		sleep:      o.Sleep,
		mapper:     newMapper(),
	}
	if c.authBase == "" {
		c.authBase = DefaultAuthBase
	}
	if c.tokenBase == "" {
		c.tokenBase = DefaultTokenBase
	}
	if c.apiBase == "" {
		c.apiBase = DefaultAPIBase
	}
	if c.httpc == nil {
		c.httpc = newHTTPClient()
	}
	if c.retryBase <= 0 {
		c.retryBase = 10 * time.Second
	}
	if c.chunkDelay == 0 {
		c.chunkDelay = 500 * time.Millisecond
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.sleep == nil {
		c.sleep = pause
	}
	if o.ClientID != "" || o.ClientSecret != "" || o.WebhookSecret != "" {
		c.keys = connector.Config{KeyClientID: o.ClientID, KeyClientSecret: o.ClientSecret, KeyWebhookSecret: o.WebhookSecret}
	}
	return c
}

// newHTTPClient builds the outbound client, with more idle connections per
// host than http.DefaultTransport keeps.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
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

// Slug returns "polar".
func (c *Connector) Slug() string { return Slug }

// RequiredKeys lists the operator keys: CLIENT_ID and CLIENT_SECRET.
// WEBHOOK_SECRET is optional and not listed.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// Enabled reports whether cfg holds both required keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != ""
}

// Configure hands the connector its keys (the OHS_POLAR_* values of the
// process configuration). It may be called again; the last call wins. The
// client id and secret are required, the webhook secret is optional.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyClientSecret] == "" {
		return errors.New("polar: OHS_POLAR_CLIENT_ID and OHS_POLAR_CLIENT_SECRET are both required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = connector.Config{
		KeyClientID:      cfg[KeyClientID],
		KeyClientSecret:  cfg[KeyClientSecret],
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

// webhookSecret returns the configured webhook signature key, or "".
func (c *Connector) webhookSecret() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[KeyWebhookSecret]
}

// Map turns one stored raw line into canonical records. Pure. A line of an
// unknown data type gives an empty result and no error.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	res, err := c.mapper.mapRecord(raw.DataType, raw.Fields, raw.UserID)
	if err != nil {
		return model.MapResult{}, err
	}
	if res == nil {
		return model.MapResult{}, nil
	}
	return *res, nil
}
