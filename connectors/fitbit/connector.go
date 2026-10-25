// SPDX-License-Identifier: AGPL-3.0-only

// Package fitbit is the Fitbit connector, built on the Google Health API v4
// (health.googleapis.com), which replaces the legacy Fitbit Web API
// (api.fitbit.com, turned off on 2026-10-30). It does Google OAuth connect,
// pulls data points per data type, receives the Google Health API webhook
// notifications, and maps the stored raw lines to canonical records.
//
// API docs: https://developers.google.com/health
//
// Keys: OHS_FITBIT_CLIENT_ID and OHS_FITBIT_CLIENT_SECRET (a Google OAuth
// client of type Web application), handed over by the collect role with
// Configure (or set with Options in tests).
//
// Optional: OHS_FITBIT_WEBHOOK_SECRET, the endpointAuthorization value of the
// project's webhook subscriber, which Google sends verbatim in the
// Authorization header. Without it the webhook route refuses everything and
// the 6 hour pull collects the data. OHS_FITBIT_WEBHOOK_SIGNATURE sets how
// the GOOGLE-HEALTH-API-SIGNATURE header is checked (see signature.go).
//
// Raw lines. One raw object holds the data points of one data type and one
// page, each data point (the API's DataPoint JSON, compacted) one line, and
// the raw object's data type is the Google data type id ("heart-rate").
package fitbit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Slug is the provider name of this connector.
const Slug = "fitbit"

// Production endpoints.
const (
	DefaultAuthURL   = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultTokenURL  = "https://oauth2.googleapis.com/token"
	DefaultRevokeURL = "https://oauth2.googleapis.com/revoke"
	DefaultAPIBase   = "https://health.googleapis.com"
	DefaultKeysetURL = "https://www.gstatic.com/googlehealthapi/webhooks/webhooks_public_keyset.json"
)

const (
	defaultPageDelay   = 50 * time.Millisecond
	defaultRetryBase   = 5 * time.Second
	defaultKeysetTTL   = time.Hour
	defaultHTTPTimeout = 30 * time.Second
)

// Key names (OHS_FITBIT_<KEY>).
const (
	KeyClientID         = "CLIENT_ID"
	KeyClientSecret     = "CLIENT_SECRET"
	KeyWebhookSecret    = "WEBHOOK_SECRET"
	KeyWebhookSignature = "WEBHOOK_SIGNATURE"
)

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the production defaults.
type Options struct {
	ClientID, ClientSecret string
	// WebhookSecret is the expected Authorization header of notifications.
	WebhookSecret string
	// WebhookSignature is the signature mode: "required" (default),
	// "if_available" or "off".
	WebhookSignature string
	// AuthURL is the consent page, TokenURL and RevokeURL the OAuth
	// endpoints, APIBase the Google Health API, KeysetURL the webhook
	// public keyset. Tests point them at an httptest fake.
	AuthURL, TokenURL, RevokeURL, APIBase, KeysetURL string
	HTTPClient                                       *http.Client
	// RetryBase is the first wait of the server error retry ladder.
	RetryBase time.Duration
	// PageDelay paces consecutive requests. Negative disables the pause.
	PageDelay time.Duration
	// KeysetTTL is how long a fetched keyset is trusted before a refetch.
	KeysetTTL time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between retries; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector and connector.PushConnector
// for Fitbit through the Google Health API.
type Connector struct {
	mu   sync.RWMutex
	keys connector.Config
	opts Options // what New was given, for NewInstance

	authURL, tokenURL, revokeURL, apiBase string
	httpc                                 *http.Client
	retryBase, pageDelay                  time.Duration
	now                                   func() time.Time
	sleep                                 func(ctx context.Context, d time.Duration) error

	keyset *keysetCache
}

var (
	_ connector.OAuthConnector  = (*Connector)(nil)
	_ connector.PushConnector   = (*Connector)(nil)
	_ connector.NotifiedFetcher = (*Connector)(nil)
	_ connector.KeyedConnector  = (*Connector)(nil)
	_ connector.Configurable    = (*Connector)(nil)
	_ connector.Revoker         = (*Connector)(nil)
)

// New returns a Fitbit connector.
func New(o Options) *Connector {
	c := &Connector{
		opts:    o,
		authURL: o.AuthURL, tokenURL: o.TokenURL, revokeURL: o.RevokeURL, apiBase: o.APIBase,
		httpc: o.HTTPClient, retryBase: o.RetryBase, pageDelay: o.PageDelay,
		now: o.Now, sleep: o.Sleep,
	}
	if c.authURL == "" {
		c.authURL = DefaultAuthURL
	}
	if c.tokenURL == "" {
		c.tokenURL = DefaultTokenURL
	}
	if c.revokeURL == "" {
		c.revokeURL = DefaultRevokeURL
	}
	if c.apiBase == "" {
		c.apiBase = DefaultAPIBase
	}
	c.apiBase = strings.TrimRight(c.apiBase, "/")
	if c.httpc == nil {
		c.httpc = newHTTPClient()
	}
	if c.retryBase <= 0 {
		c.retryBase = defaultRetryBase
	}
	if c.pageDelay == 0 {
		c.pageDelay = defaultPageDelay
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.sleep == nil {
		c.sleep = pause
	}
	keysetURL := o.KeysetURL
	if keysetURL == "" {
		keysetURL = DefaultKeysetURL
	}
	ttl := o.KeysetTTL
	if ttl <= 0 {
		ttl = defaultKeysetTTL
	}
	c.keyset = newKeysetCache(keysetURL, ttl, c.httpc, c.now)
	if o.ClientID != "" || o.ClientSecret != "" {
		c.keys = connector.Config{
			KeyClientID: o.ClientID, KeyClientSecret: o.ClientSecret,
			KeyWebhookSecret: o.WebhookSecret, KeyWebhookSignature: o.WebhookSignature,
		}
	}
	return c
}

// newHTTPClient builds the outbound client, keeping idle connections to the
// API host across the many requests of one pull.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: defaultHTTPTimeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

// Slug returns "fitbit".
func (c *Connector) Slug() string { return Slug }

// SyncPolicy implements connector.SyncPolicied.
// Devices sync to the app hours after a measurement; daily figures settle
// over the next day.
func (c *Connector) SyncPolicy() connector.SyncPolicy {
	return connector.SyncPolicy{
		Lookback:  24 * time.Hour,
		Reconcile: 3 * 24 * time.Hour,
	}
}

// RequiredKeys lists the operator keys: CLIENT_ID and CLIENT_SECRET.
// WEBHOOK_SECRET and WEBHOOK_SIGNATURE are optional.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// Enabled reports whether cfg holds both client keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != ""
}

// OptionalKeys lists the keys it also reads and runs without: WEBHOOK_SECRET and WEBHOOK_SIGNATURE (notifications).
func (c *Connector) OptionalKeys() []string { return []string{KeyWebhookSecret, KeyWebhookSignature} }

// Configure hands the connector its keys (the OHS_FITBIT_* values). It may
// be called again; the last call wins. An unknown WEBHOOK_SIGNATURE mode is
// an error.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyClientSecret] == "" {
		return errors.New("fitbit: OHS_FITBIT_CLIENT_ID and OHS_FITBIT_CLIENT_SECRET are both required")
	}
	if _, err := parseSignatureMode(cfg[KeyWebhookSignature]); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = connector.Config{
		KeyClientID: cfg[KeyClientID], KeyClientSecret: cfg[KeyClientSecret],
		KeyWebhookSecret: cfg[KeyWebhookSecret], KeyWebhookSignature: cfg[KeyWebhookSignature],
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

// webhookKeys returns the webhook secret and the signature mode.
func (c *Connector) webhookKeys() (string, signatureMode) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	mode, err := parseSignatureMode(c.keys[KeyWebhookSignature])
	if err != nil {
		mode = SignatureRequired
	}
	return c.keys[KeyWebhookSecret], mode
}

// Map turns one stored raw line (one DataPoint) into canonical records. Pure.
// A line of an unknown data type gives an empty result and no error.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	res, err := mapRecord(raw.DataType, raw.Fields, raw.UserID)
	if err != nil {
		return model.MapResult{}, fmt.Errorf("fitbit: %s: %w", raw.DataType, err)
	}
	if res == nil {
		return model.MapResult{}, nil
	}
	return *res, nil
}
