// SPDX-License-Identifier: AGPL-3.0-only

// Package wahoo is the Wahoo connector: OAuth connect, pulls of the user's
// workouts and workout summaries from the Wahoo Cloud API v1, workout summary
// webhooks, and the mapping of the stored raw lines to canonical workouts and
// body measurements.
//
// API docs: https://cloud-api.wahooligan.com/ (developer portal:
// https://developers.wahooligan.com/). Terms: the Wahoo API Agreement,
// https://www.wahoofitness.com/wahoo-api-agreement.
//
// Keys: OHS_WAHOO_CLIENT_ID and OHS_WAHOO_CLIENT_SECRET (required), from an
// application registered on the developer portal. Optional:
// OHS_WAHOO_WEBHOOK_TOKEN, the webhook token set on that application; Wahoo
// puts it in every webhook body, and without it webhooks are refused (data
// still arrives with the scheduled pulls).
//
// A new application runs in Wahoo's sandbox at low rate limits (25 requests
// per 5 minutes, 250 a day); production limits need Wahoo's review of the
// application. Access tokens last 2 hours and every refresh returns a new
// pair. Only workout summaries are read: the FIT files they link to hold GPS
// tracks, which the API Agreement forbids aggregating or storing, so they are
// not downloaded.
package wahoo

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
const Slug = "wahoo"

// DefaultBase serves the consent page, the token endpoint and the API v1.
const DefaultBase = "https://api.wahooligan.com"

// Key names (OHS_WAHOO_<KEY>).
const (
	KeyClientID     = "CLIENT_ID"
	KeyClientSecret = "CLIENT_SECRET"
	KeyWebhookToken = "WEBHOOK_TOKEN"
)

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the production defaults.
type Options struct {
	ClientID, ClientSecret string
	// WebhookToken is the optional webhook key.
	WebhookToken string
	// Base serves the consent page, the token endpoint and the API. Tests
	// point it at an httptest fake.
	Base       string
	HTTPClient *http.Client
	// RetryBase is the first wait of the transport error retry ladder (5 s).
	RetryBase time.Duration
	// PerPage is the page size of GET /v1/workouts (30, Wahoo's default,
	// when zero); tests shrink it to exercise pagination.
	PerPage int
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector and connector.PushConnector
// for Wahoo.
type Connector struct {
	mu   sync.RWMutex
	keys connector.Config
	opts Options // what New was given, for NewInstance

	base      string
	httpc     *http.Client
	retryBase time.Duration
	perPage   int
	now       func() time.Time
	sleep     func(ctx context.Context, d time.Duration) error
}

var (
	_ connector.OAuthConnector = (*Connector)(nil)
	_ connector.PushConnector  = (*Connector)(nil)
	_ connector.KeyedConnector = (*Connector)(nil)
	_ connector.OptionalKeyed  = (*Connector)(nil)
	_ connector.Configurable   = (*Connector)(nil)
	_ connector.Instancer      = (*Connector)(nil)
	_ connector.Revoker        = (*Connector)(nil)
	_ connector.SyncPolicied   = (*Connector)(nil)
)

// New returns a Wahoo connector.
func New(o Options) *Connector {
	c := &Connector{
		opts:      o,
		base:      o.Base,
		httpc:     o.HTTPClient,
		retryBase: o.RetryBase,
		perPage:   o.PerPage,
		now:       o.Now,
		sleep:     o.Sleep,
	}
	if c.base == "" {
		c.base = DefaultBase
	}
	if c.httpc == nil {
		c.httpc = newHTTPClient()
	}
	if c.retryBase <= 0 {
		c.retryBase = 5 * time.Second
	}
	if c.perPage <= 0 {
		c.perPage = defaultPerPage
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
			KeyWebhookToken: o.WebhookToken,
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

// Slug returns "wahoo".
func (c *Connector) Slug() string { return Slug }

// SyncPolicy implements connector.SyncPolicied.
// A sandbox application gets 250 requests a day, and a pull costs at least
// one: 12 hours while webhooks arrive, 6 without, and webhooks carry the
// summaries themselves. Workouts are uploaded from head units late, so each
// pull reads the day before again.
func (c *Connector) SyncPolicy() connector.SyncPolicy {
	return connector.SyncPolicy{
		PullEvery:            12 * time.Hour,
		PullEveryWithoutPush: 6 * time.Hour,
		Lookback:             24 * time.Hour,
	}
}

// RequiredKeys lists the operator keys: CLIENT_ID and CLIENT_SECRET.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// OptionalKeys lists the keys it also reads and runs without: WEBHOOK_TOKEN.
func (c *Connector) OptionalKeys() []string { return []string{KeyWebhookToken} }

// Enabled reports whether cfg holds both required keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != ""
}

// Configure hands the connector its keys (the OHS_WAHOO_* values). It may be
// called again; the last call wins.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyClientSecret] == "" {
		return errors.New("wahoo: OHS_WAHOO_CLIENT_ID and OHS_WAHOO_CLIENT_SECRET are both required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = connector.Config{
		KeyClientID: cfg[KeyClientID], KeyClientSecret: cfg[KeyClientSecret],
		KeyWebhookToken: cfg[KeyWebhookToken],
	}
	return nil
}

// NewInstance returns a connector like c without keys, sharing its HTTP
// client, for a tenant's own keys (connector.Instancer).
func (c *Connector) NewInstance() connector.Connector {
	o := c.opts
	o.HTTPClient = c.httpc
	o.Base = c.base
	n := New(o)
	n.keys = nil
	return n
}

// credentials returns the configured client id and secret.
func (c *Connector) credentials() (id, secret string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[KeyClientID], c.keys[KeyClientSecret]
}

// webhookToken returns the configured webhook token.
func (c *Connector) webhookToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[KeyWebhookToken]
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
