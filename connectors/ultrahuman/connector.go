// SPDX-License-Identifier: AGPL-3.0-only

// Package ultrahuman is the Ultrahuman connector: OAuth 2.0 connect through
// the Ultrahuman Partnership API, a daily pull of the Ring AIR and M1 CGM
// metrics, and the mapping of the stored raw lines to canonical records.
//
// API docs: https://vision.ultrahuman.com/developer-docs?type=oauth
//
// Keys: OHS_ULTRAHUMAN_CLIENT_ID and OHS_ULTRAHUMAN_CLIENT_SECRET, issued by
// Ultrahuman through partner onboarding. They are handed over by the collect
// role with Configure (or set with Options in tests).
//
// The metrics endpoint is keyed by date: Fetch asks for one day per call over
// the pull window and stores each day's answer as one raw line. Ultrahuman
// documents no webhooks, so the connector is pull only. The legacy personal
// token API (/api/v1/partner/daily_metrics) is not used: see
// docs/connectors/ultrahuman.md.
package ultrahuman

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
const Slug = "ultrahuman"

// Production endpoints.
const (
	// DefaultAuthBase serves the consent page (/authorise).
	DefaultAuthBase = "https://auth.ultrahuman.com"
	// DefaultAPIBase serves the token, revoke and data endpoints.
	DefaultAPIBase = "https://partner.ultrahuman.com"
)

// Key names (OHS_ULTRAHUMAN_<KEY>).
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
	// AuthBase serves the consent page, APIBase everything else. Tests point
	// both at an httptest fake.
	AuthBase, APIBase string
	HTTPClient        *http.Client
	// RetryBase is the first wait of the transport error retry ladder (5 s).
	RetryBase time.Duration
	// DayDelay paces consecutive day requests. Negative disables the pause;
	// zero takes the default.
	DayDelay time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries and days; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector and connector.Revoker for
// Ultrahuman.
type Connector struct {
	mu   sync.RWMutex
	keys connector.Config

	authBase, apiBase string
	httpc             *http.Client
	retryBase         time.Duration
	dayDelay          time.Duration
	now               func() time.Time
	sleep             func(ctx context.Context, d time.Duration) error
}

var (
	_ connector.OAuthConnector = (*Connector)(nil)
	_ connector.KeyedConnector = (*Connector)(nil)
	_ connector.Configurable   = (*Connector)(nil)
	_ connector.Revoker        = (*Connector)(nil)
)

// New returns an Ultrahuman connector.
func New(o Options) *Connector {
	c := &Connector{
		authBase:  o.AuthBase,
		apiBase:   o.APIBase,
		httpc:     o.HTTPClient,
		retryBase: o.RetryBase,
		dayDelay:  o.DayDelay,
		now:       o.Now,
		sleep:     o.Sleep,
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
		c.retryBase = 5 * time.Second
	}
	if c.dayDelay == 0 {
		c.dayDelay = 250 * time.Millisecond
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

// newHTTPClient builds the outbound client.
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

// Slug returns "ultrahuman".
func (c *Connector) Slug() string { return Slug }

// RequiredKeys lists the operator keys: CLIENT_ID and CLIENT_SECRET.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// Enabled reports whether cfg holds both required keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != ""
}

// Configure hands the connector its keys. It may be called again; the last
// call wins.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyClientSecret] == "" {
		return errors.New("ultrahuman: OHS_ULTRAHUMAN_CLIENT_ID and OHS_ULTRAHUMAN_CLIENT_SECRET are both required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = connector.Config{KeyClientID: cfg[KeyClientID], KeyClientSecret: cfg[KeyClientSecret]}
	return nil
}

// credentials returns the configured client id and secret.
func (c *Connector) credentials() (id, secret string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[KeyClientID], c.keys[KeyClientSecret]
}

// Map turns one stored raw line (one day of metrics) into canonical records.
// Pure. A line of an unknown data type gives an empty result and no error.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	if raw.DataType != DataTypeMetrics {
		return model.MapResult{}, nil
	}
	res, err := mapMetricsDay(raw.Fields, raw.UserID)
	if err != nil || res == nil {
		return model.MapResult{}, err
	}
	return *res, nil
}
