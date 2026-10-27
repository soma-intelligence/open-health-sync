// SPDX-License-Identifier: AGPL-3.0-only

// Package garmin is the Garmin connector: OAuth 2.0 with PKCE against Garmin
// Connect, pulls from the Health API summary endpoints, Garmin's ping and push
// notifications, and the mapping of the stored raw lines to canonical
// records.
//
// Program: https://developer.garmin.com/gc-developer-program/overview/
// OAuth 2.0 PKCE: https://developerportal.garmin.com/sites/default/files/OAuth2PKCE.pdf
//
// It needs the keys of a Garmin business app (Starter or Enterprise tier of
// the program Garmin reopened on 2026-10-21); it does not use the Personal
// API. This connector follows public sources
// (Garmin's PKCE specification and API pages, payload examples published by
// third parties); docs/connectors/garmin.md marks the points no public Garmin
// page confirms.
//
// Keys: OHS_GARMIN_CLIENT_ID and OHS_GARMIN_CLIENT_SECRET (required), and
// OHS_GARMIN_INBOUND_TOKEN (optional: a secret the notification URLs carry
// as ?token=..., since Garmin does not sign its notifications; without it
// every notification, ping included, is refused and only the scheduled
// pulls bring data).
package garmin

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Slug is the provider name of this connector.
const Slug = "garmin"

// Production endpoints.
const (
	// DefaultAuthURL is the consent page.
	DefaultAuthURL = "https://connect.garmin.com/oauth2Confirm"
	// DefaultTokenURL is the token endpoint.
	DefaultTokenURL = "https://connectapi.garmin.com/di-oauth2-service/oauth/token"
	// DefaultAPIBase serves the Health API (wellness-api).
	DefaultAPIBase = "https://apis.garmin.com"
)

// Key names (OHS_GARMIN_<KEY>).
const (
	KeyClientID     = "CLIENT_ID"
	KeyClientSecret = "CLIENT_SECRET"
	// KeyInboundToken is optional. Every notification must carry
	// it as the "token" query parameter of the notification URL; while it
	// is unset, notifications are refused.
	KeyInboundToken = "INBOUND_TOKEN"
)

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the production defaults.
type Options struct {
	ClientID, ClientSecret, InboundToken string
	// AuthURL is the consent page, TokenURL the token endpoint, APIBase the
	// Health API. Tests point them at an httptest fake.
	AuthURL, TokenURL, APIBase string
	HTTPClient                 *http.Client
	// RetryBase is the first wait of the transport error retry ladder (5 s).
	RetryBase time.Duration
	// ChunkDelay paces consecutive Health API requests. Negative disables
	// the pause; zero takes the default (600 ms, about 100 requests a
	// minute).
	ChunkDelay time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector, connector.StateExchanger,
// connector.PushConnector, connector.NotifiedFetcher and connector.Revoker
// for Garmin.
type Connector struct {
	mu   sync.RWMutex
	keys connector.Config
	opts Options // what New was given, for NewInstance

	authURL, tokenURL, apiBase string
	httpc                      *http.Client
	retryBase                  time.Duration
	chunkDelay                 time.Duration
	now                        func() time.Time
	sleep                      func(ctx context.Context, d time.Duration) error

	mapper *mapper

	// refusedWarnAt is when a refusal for a missing inbound token was last
	// logged (Unix nanoseconds), for refusalWarnEvery.
	refusedWarnAt atomic.Int64
}

var (
	_ connector.OAuthConnector  = (*Connector)(nil)
	_ connector.StateExchanger  = (*Connector)(nil)
	_ connector.PushConnector   = (*Connector)(nil)
	_ connector.NotifiedFetcher = (*Connector)(nil)
	_ connector.KeyedConnector  = (*Connector)(nil)
	_ connector.OptionalKeyed   = (*Connector)(nil)
	_ connector.Configurable    = (*Connector)(nil)
	_ connector.Revoker         = (*Connector)(nil)
)

// New returns a Garmin connector.
func New(o Options) *Connector {
	c := &Connector{
		opts:       o,
		authURL:    o.AuthURL,
		tokenURL:   o.TokenURL,
		apiBase:    o.APIBase,
		httpc:      o.HTTPClient,
		retryBase:  o.RetryBase,
		chunkDelay: o.ChunkDelay,
		now:        o.Now,
		sleep:      o.Sleep,
		mapper:     newMapper(),
	}
	if c.authURL == "" {
		c.authURL = DefaultAuthURL
	}
	if c.tokenURL == "" {
		c.tokenURL = DefaultTokenURL
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
		c.chunkDelay = 600 * time.Millisecond
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.sleep == nil {
		c.sleep = pause
	}
	if o.ClientID != "" || o.ClientSecret != "" || o.InboundToken != "" {
		c.keys = connector.Config{KeyClientID: o.ClientID, KeyClientSecret: o.ClientSecret, KeyInboundToken: o.InboundToken}
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

// Slug returns "garmin".
func (c *Connector) Slug() string { return Slug }

// RequiredKeys lists the operator keys: CLIENT_ID and CLIENT_SECRET.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// OptionalKeys lists the keys it also reads and runs without: INBOUND_TOKEN
// (the secret notification URLs carry).
func (c *Connector) OptionalKeys() []string { return []string{KeyInboundToken} }

// Enabled reports whether cfg holds both required keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != ""
}

// Configure hands the connector its keys (the OHS_GARMIN_* values of the
// process configuration). It may be called again; the last call wins.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyClientSecret] == "" {
		return errors.New("garmin: OHS_GARMIN_CLIENT_ID and OHS_GARMIN_CLIENT_SECRET are both required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = connector.Config{
		KeyClientID:     cfg[KeyClientID],
		KeyClientSecret: cfg[KeyClientSecret],
		KeyInboundToken: cfg[KeyInboundToken],
	}
	if cfg[KeyInboundToken] == "" {
		slog.Warn("garmin: OHS_GARMIN_INBOUND_TOKEN is not set: every notification is refused (401), "+
			"pings included, so only the scheduled pulls bring data", "provider", Slug)
	}
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

// inboundToken returns the configured notification token, or "".
func (c *Connector) inboundToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[KeyInboundToken]
}

// Map turns one stored raw line into canonical records. Pure. A line of an
// unknown or raw-only data type gives an empty result and no error.
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
