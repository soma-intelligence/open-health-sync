// SPDX-License-Identifier: AGPL-3.0-only

// Package whoop is the WHOOP connector: OAuth connect, pulls from the WHOOP
// Developer API v2, webhooks, and the mapping of the stored raw lines to
// canonical records.
//
// API docs: https://developer.whoop.com/api and
// https://developer.whoop.com/docs/developing/webhooks
//
// Keys: OHS_WHOOP_CLIENT_ID and OHS_WHOOP_CLIENT_SECRET, handed over by the
// collect role with Configure (or set with Options in tests).
//
// The client secret also keys the webhook signature (see inbound.go). WHOOP
// signs with whatever secret the dashboard holds, so rotating it there and in
// the instance configuration is enough: no per-user registration exists.
package whoop

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
const Slug = "whoop"

// Production endpoints. The OAuth server lives under AuthBase
// (/oauth2/auth, /oauth2/token), the data API under APIBase (/v2/...).
const (
	DefaultAuthBase = "https://api.prod.whoop.com/oauth"
	DefaultAPIBase  = "https://api.prod.whoop.com/developer"
)

// Key names (OHS_WHOOP_<KEY>).
const (
	KeyClientID     = "CLIENT_ID"
	KeyClientSecret = "CLIENT_SECRET"
)

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the production defaults.
type Options struct {
	ClientID, ClientSecret string
	// AuthBase serves the consent page and the token endpoint, APIBase
	// everything else. Tests point both at an httptest fake.
	AuthBase, APIBase string
	HTTPClient        *http.Client
	// RetryBase is the first wait of the transport error retry ladder (10 s).
	RetryBase time.Duration
	// PageDelay spaces the API requests of this process, across all the
	// pulls running at once. WHOOP allows 100 requests a minute per
	// application, so the default is 600 ms. Negative disables the pause;
	// zero takes the default.
	PageDelay time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries; tests replace it so retries do
	// not sleep for real.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector and connector.PushConnector
// for WHOOP.
type Connector struct {
	mu   sync.RWMutex
	keys connector.Config
	opts Options // what New was given, for NewInstance

	authBase, apiBase string
	httpc             *http.Client
	retryBase         time.Duration
	pageDelay         time.Duration
	now               func() time.Time
	sleep             func(ctx context.Context, d time.Duration) error

	// paceMu guards nextSlot, the earliest time the next API request of this
	// process may be sent (see pace).
	paceMu   sync.Mutex
	nextSlot time.Time
	// onSlot, when set (tests), is told each slot pace hands out.
	onSlot func(time.Time)

	mapper *mapper
}

var (
	_ connector.OAuthConnector = (*Connector)(nil)
	_ connector.PushConnector  = (*Connector)(nil)
	_ connector.KeyedConnector = (*Connector)(nil)
	_ connector.Configurable   = (*Connector)(nil)
	_ connector.Revoker        = (*Connector)(nil)
)

// New returns a WHOOP connector.
func New(o Options) *Connector {
	c := &Connector{
		opts:      o,
		authBase:  o.AuthBase,
		apiBase:   o.APIBase,
		httpc:     o.HTTPClient,
		retryBase: o.RetryBase,
		pageDelay: o.PageDelay,
		now:       o.Now,
		sleep:     o.Sleep,
		mapper:    newMapper(),
	}
	if c.authBase == "" {
		c.authBase = DefaultAuthBase
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
	if c.pageDelay == 0 {
		c.pageDelay = 600 * time.Millisecond
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.sleep == nil {
		c.sleep = pause
	}
	if o.ClientID != "" || o.ClientSecret != "" {
		c.keys = connector.Config{KeyClientID: o.ClientID, KeyClientSecret: o.ClientSecret}
	}
	return c
}

// newHTTPClient builds the outbound client. http.DefaultTransport keeps only
// two idle connections per host, which re-handshakes TLS on most requests of a
// pull fanning out over several endpoints.
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

// Slug returns "whoop".
func (c *Connector) Slug() string { return Slug }

// SyncPolicy implements connector.SyncPolicied.
// Sleeps and recoveries are scored after waking and re-scored later; the
// history is long.
func (c *Connector) SyncPolicy() connector.SyncPolicy {
	return connector.SyncPolicy{
		Lookback:  24 * time.Hour,
		Backfill:  90 * 24 * time.Hour,
		Reconcile: 7 * 24 * time.Hour,
	}
}

// RequiredKeys lists the operator keys: CLIENT_ID and CLIENT_SECRET.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// Enabled reports whether cfg holds both keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != ""
}

// Configure hands the connector its keys (the OHS_WHOOP_* values of the
// process configuration). It may be called again; the last call wins. Both
// keys are required.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyClientSecret] == "" {
		return errors.New("whoop: OHS_WHOOP_CLIENT_ID and OHS_WHOOP_CLIENT_SECRET are both required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = connector.Config{KeyClientID: cfg[KeyClientID], KeyClientSecret: cfg[KeyClientSecret]}
	return nil
}

// NewInstance returns a connector like c without keys, sharing its HTTP
// client, for a tenant's own keys (connector.Instancer).
func (c *Connector) NewInstance() connector.Connector {
	o := c.opts
	o.HTTPClient = c.httpc
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
