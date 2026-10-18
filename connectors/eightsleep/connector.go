// SPDX-License-Identifier: AGPL-3.0-only

// Package eightsleep is the Eight Sleep connector: the user signs in with the
// email and password of their Eight Sleep account, the connector pulls the
// nightly trends of the Pod (sleep stages, heart rate, HRV, breathing rate,
// bed and room temperature) and maps the stored raw lines to canonical
// records.
//
// Eight Sleep has no public API and no partner program. The connector uses
// the account sign-in and the API the Eight Sleep app uses, as the
// open source pyEight library (MIT license, https://github.com/mezz64/pyEight
// and its maintained fork https://github.com/lukas-clarke/eight_sleep)
// documents them. This is a Go re-implementation; no pyEight code is copied.
// docs/connectors/eight-sleep.md marks the points those sources leave open.
//
// Keys: OHS_EIGHT_SLEEP_CLIENT_ID and OHS_EIGHT_SLEEP_CLIENT_SECRET
// (optional): the client credentials of the Eight Sleep app, which Eight
// Sleep does not issue to third parties. Without them the connector uses the
// values pyEight publishes (DefaultClientID, DefaultClientSecret); set both
// to override them.
package eightsleep

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
const Slug = "eight_sleep"

// Production endpoints.
const (
	// DefaultAuthURL is the token endpoint of the account sign-in.
	DefaultAuthURL = "https://auth-api.8slp.net/v1/tokens"
	// DefaultAPIBase serves the user and trends endpoints.
	DefaultAPIBase = "https://client-api.8slp.net/v1"
)

// The Eight Sleep app's client credentials, as published by pyEight
// (KNOWN_CLIENT_ID and KNOWN_CLIENT_SECRET in pyEight/constants.py of
// https://github.com/lukas-clarke/eight_sleep). They belong to Eight Sleep's
// app, not to this project, and stop working if Eight Sleep changes them.
const (
	DefaultClientID     = "0894c7f33bb94800a03f1f4df13a4f38"
	DefaultClientSecret = "f0954a3ed5763ba3d06834c73731a32f15f168f47d4f164751275def86db0c76"
)

// Key names (OHS_EIGHT_SLEEP_<KEY>).
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
	// AuthURL is the token endpoint and APIBase the client API. Tests point
	// them at an httptest fake.
	AuthURL, APIBase string
	HTTPClient       *http.Client
	// RetryBase is the first wait of the transport error retry ladder (5 s).
	RetryBase time.Duration
	// ChunkDelay paces consecutive trends requests. Negative disables the
	// pause; zero takes the default (1 s).
	ChunkDelay time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector and
// connector.PasswordConnector for Eight Sleep.
type Connector struct {
	mu   sync.RWMutex
	keys connector.Config

	authURL, apiBase string
	httpc            *http.Client
	retryBase        time.Duration
	chunkDelay       time.Duration
	now              func() time.Time
	sleep            func(ctx context.Context, d time.Duration) error
}

var (
	_ connector.OAuthConnector    = (*Connector)(nil)
	_ connector.PasswordConnector = (*Connector)(nil)
	_ connector.KeyedConnector    = (*Connector)(nil)
	_ connector.Configurable      = (*Connector)(nil)
)

// New returns an Eight Sleep connector.
func New(o Options) *Connector {
	c := &Connector{
		authURL:    o.AuthURL,
		apiBase:    o.APIBase,
		httpc:      o.HTTPClient,
		retryBase:  o.RetryBase,
		chunkDelay: o.ChunkDelay,
		now:        o.Now,
		sleep:      o.Sleep,
	}
	if c.authURL == "" {
		c.authURL = DefaultAuthURL
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
	if c.chunkDelay == 0 {
		c.chunkDelay = time.Second
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

// Slug returns "eight_sleep".
func (c *Connector) Slug() string { return Slug }

// RequiredKeys is empty: the connector runs on the published defaults.
func (c *Connector) RequiredKeys() []string { return nil }

// OptionalKeys lists the keys it also reads and runs without: CLIENT_ID and
// CLIENT_SECRET, which override the defaults together.
func (c *Connector) OptionalKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// Enabled reports whether cfg sets both overriding keys or neither.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return (cfg[KeyClientID] == "") == (cfg[KeyClientSecret] == "")
}

// Configure hands the connector its keys (the OHS_EIGHT_SLEEP_* values of
// the process configuration): both, or neither for the defaults. It may be
// called again; the last call wins.
func (c *Connector) Configure(cfg connector.Config) error {
	if !c.Enabled(cfg) {
		return errors.New("eight_sleep: set both OHS_EIGHT_SLEEP_CLIENT_ID and OHS_EIGHT_SLEEP_CLIENT_SECRET, or neither")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = nil
	if cfg[KeyClientID] != "" {
		c.keys = connector.Config{KeyClientID: cfg[KeyClientID], KeyClientSecret: cfg[KeyClientSecret]}
	}
	return nil
}

// clientCredentials returns the configured client id and secret, or the
// published defaults.
func (c *Connector) clientCredentials() (id, secret string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.keys[KeyClientID] == "" {
		return DefaultClientID, DefaultClientSecret
	}
	return c.keys[KeyClientID], c.keys[KeyClientSecret]
}

// Map turns one stored raw line into canonical records. Pure. A line of an
// unknown data type gives an empty result and no error.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	if raw.DataType != DataTypeTrends {
		return model.MapResult{}, nil
	}
	res, err := mapTrendDay(raw.Fields, raw.UserID)
	if err != nil || res == nil {
		return model.MapResult{}, err
	}
	return *res, nil
}
