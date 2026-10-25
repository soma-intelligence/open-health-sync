// SPDX-License-Identifier: AGPL-3.0-only

// Package oura is the Oura connector: OAuth connect, pulls from the Oura Cloud
// API v2, application webhooks, and the mapping of the stored raw lines to
// canonical records.
//
// API docs: https://cloud.ouraring.com/v2/docs
//
// Keys: OHS_OURA_CLIENT_ID and OHS_OURA_CLIENT_SECRET, handed over by the
// collect role with Configure (or set with Options in tests).
//
// Optional: OHS_OURA_VERIFICATION_TOKEN, the token the operator passes when
// creating the application's webhook subscriptions. With it the webhook
// routes (connector.PushConnector and connector.ChallengeResponder, see
// inbound.go) are on: create and update notifications queue a pull of the
// recent days, and a deleted sleep, workout or session is removed. Without
// it they refuse everything, the 6 hour pull collects the data, and the only
// deletions are the sleep periods Oura lists with type "deleted" or "rest".
//
// Updates and deletes. A daily document (activity, sleep score, readiness,
// SpO2, stress, VO2 max) is re-sent with new values while its day is open:
// its samples are Replaceable, hashed on the day, so the newest value
// replaces the stored one.
// A sleep or a workout is identified by its Oura id and start and is
// Replaceable: a corrected copy replaces the stored figures (and, for a sleep,
// its phases), and a copy whose start moved supersedes the stored one. The
// figures of a main sleep (resting heart rate, respiratory rate) are also
// emitted as Replaceable samples keyed by the Oura id. A sleep Oura marks as
// deleted (type "deleted") or the user rejected (type "rest") maps to a
// model.Deletion of its Oura id, which removes a stored copy and keeps any
// later one out.
package oura

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
const Slug = "oura"

// Production endpoints.
const (
	DefaultAccountBase = "https://cloud.ouraring.com"
	DefaultAPIBase     = "https://api.ouraring.com"
)

// Key names (OHS_OURA_<KEY>).
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
	// VerificationToken is the optional webhook verification token.
	VerificationToken string
	// AccountBase serves the consent page (/oauth/authorize), APIBase the
	// token endpoint and the data. Tests point both at an httptest fake.
	AccountBase, APIBase string
	HTTPClient           *http.Client
	// RetryBase is the first wait of the transport error retry ladder (10 s).
	RetryBase time.Duration
	// PageDelay paces consecutive requests. Negative disables the pause;
	// zero takes the default.
	PageDelay time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries; tests replace it so retries do
	// not sleep for real.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector and connector.PushConnector
// for Oura.
type Connector struct {
	mu   sync.RWMutex
	keys connector.Config
	opts Options // what New was given, for NewInstance

	accountBase, apiBase string
	httpc                *http.Client
	retryBase            time.Duration
	pageDelay            time.Duration
	now                  func() time.Time
	sleep                func(ctx context.Context, d time.Duration) error

	mapper *mapper
}

var (
	_ connector.OAuthConnector     = (*Connector)(nil)
	_ connector.Revoker            = (*Connector)(nil)
	_ connector.PushConnector      = (*Connector)(nil)
	_ connector.ChallengeResponder = (*Connector)(nil)
	_ connector.KeyedConnector     = (*Connector)(nil)
	_ connector.Configurable       = (*Connector)(nil)
)

// New returns an Oura connector.
func New(o Options) *Connector {
	c := &Connector{
		opts:        o,
		accountBase: o.AccountBase,
		apiBase:     o.APIBase,
		httpc:       o.HTTPClient,
		retryBase:   o.RetryBase,
		pageDelay:   o.PageDelay,
		now:         o.Now,
		sleep:       o.Sleep,
		mapper:      newMapper(),
	}
	if c.accountBase == "" {
		c.accountBase = DefaultAccountBase
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
		c.pageDelay = 100 * time.Millisecond
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.sleep == nil {
		c.sleep = pause
	}
	if o.ClientID != "" || o.ClientSecret != "" {
		c.keys = connector.Config{KeyClientID: o.ClientID, KeyClientSecret: o.ClientSecret, KeyVerificationToken: o.VerificationToken}
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

// Slug returns "oura".
func (c *Connector) Slug() string { return Slug }

// SyncPolicy implements connector.SyncPolicied.
// The ring syncs when the app opens, so the night arrives in the morning and
// scores are revised; the history is long.
func (c *Connector) SyncPolicy() connector.SyncPolicy {
	return connector.SyncPolicy{
		Lookback:  24 * time.Hour,
		Backfill:  90 * 24 * time.Hour,
		Reconcile: 7 * 24 * time.Hour,
	}
}

// RequiredKeys lists the operator keys: CLIENT_ID and CLIENT_SECRET.
// VERIFICATION_TOKEN is optional: pulls work without it.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// Enabled reports whether cfg holds both keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != ""
}

// OptionalKeys lists the keys it also reads and runs without: VERIFICATION_TOKEN (webhooks).
func (c *Connector) OptionalKeys() []string { return []string{KeyVerificationToken} }

// Configure hands the connector its keys (the OHS_OURA_* values of the
// process configuration). It may be called again; the last call wins. Both
// keys are required; VERIFICATION_TOKEN turns webhooks on.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyClientSecret] == "" {
		return errors.New("oura: OHS_OURA_CLIENT_ID and OHS_OURA_CLIENT_SECRET are both required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = connector.Config{
		KeyClientID: cfg[KeyClientID], KeyClientSecret: cfg[KeyClientSecret],
		KeyVerificationToken: cfg[KeyVerificationToken],
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
