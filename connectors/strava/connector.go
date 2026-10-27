// SPDX-License-Identifier: AGPL-3.0-only

// Package strava is the Strava connector: OAuth connect, pulls of the
// athlete's activities from the Strava API v3, push subscription events, and
// the mapping of the stored raw lines to canonical workouts.
//
// API docs: https://developers.strava.com/docs/reference/ and
// https://developers.strava.com/docs/webhooks/
//
// Keys: OHS_STRAVA_CLIENT_ID and OHS_STRAVA_CLIENT_SECRET (required), handed
// over by the collect role with Configure (or set with Options in tests).
// Optional: OHS_STRAVA_VERIFY_TOKEN, the string the operator passes as
// verify_token when creating the push subscription; without it the
// subscription challenge is refused and no event is accepted. Optional:
// OHS_STRAVA_SUBSCRIPTION_ID, the id Strava returned for that subscription;
// when set, an event naming another subscription is refused, and activity
// deletes are applied (see inbound.go).
//
// Strava keeps one push subscription per application, for every athlete, so
// the operator creates it once (see docs/connectors/strava.md); the connector
// does not subscribe per connection.
//
// Only summary activities are pulled (GET /athlete/activities). Detailed
// activities and streams cost one request each, and Strava's default limits
// (100 read requests per 15 minutes, 1,000 per day, per application) would
// not survive a backfill of several users.
package strava

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
const Slug = "strava"

// Production endpoints.
const (
	DefaultOAuthBase = "https://www.strava.com"
	DefaultAPIBase   = "https://www.strava.com/api/v3"
)

// Key names (OHS_STRAVA_<KEY>).
const (
	KeyClientID       = "CLIENT_ID"
	KeyClientSecret   = "CLIENT_SECRET"
	KeyVerifyToken    = "VERIFY_TOKEN"
	KeySubscriptionID = "SUBSCRIPTION_ID"
)

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the production defaults.
type Options struct {
	ClientID, ClientSecret string
	// VerifyToken and SubscriptionID are the optional push subscription keys.
	VerifyToken, SubscriptionID string
	// OAuthBase serves the consent page and the token endpoints, APIBase
	// the API v3. Tests point both at an httptest fake.
	OAuthBase, APIBase string
	HTTPClient         *http.Client
	// RetryBase is the first wait of the transport error retry ladder (10 s).
	RetryBase time.Duration
	// PageDelay paces consecutive pages. Negative disables the pause; zero
	// takes the default.
	PageDelay time.Duration
	// PerPage is the page size of GET /athlete/activities (200, Strava's
	// maximum, when zero); tests shrink it to exercise pagination.
	PerPage int
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries; tests replace it so retries do
	// not sleep for real.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector and connector.PushConnector
// for Strava.
type Connector struct {
	mu   sync.RWMutex
	keys connector.Config
	opts Options // what New was given, for NewInstance

	oauthBase, apiBase string
	httpc              *http.Client
	retryBase          time.Duration
	pageDelay          time.Duration
	perPage            int
	now                func() time.Time
	sleep              func(ctx context.Context, d time.Duration) error

	mapper *mapper
}

var (
	_ connector.OAuthConnector     = (*Connector)(nil)
	_ connector.PushConnector      = (*Connector)(nil)
	_ connector.KeyedConnector     = (*Connector)(nil)
	_ connector.Configurable       = (*Connector)(nil)
	_ connector.Revoker            = (*Connector)(nil)
	_ connector.ChallengeResponder = (*Connector)(nil)
)

// New returns a Strava connector.
func New(o Options) *Connector {
	c := &Connector{
		opts:      o,
		oauthBase: o.OAuthBase,
		apiBase:   o.APIBase,
		httpc:     o.HTTPClient,
		retryBase: o.RetryBase,
		pageDelay: o.PageDelay,
		perPage:   o.PerPage,
		now:       o.Now,
		sleep:     o.Sleep,
		mapper:    newMapper(),
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
		c.retryBase = 10 * time.Second
	}
	if c.pageDelay == 0 {
		c.pageDelay = 200 * time.Millisecond
	}
	if c.perPage <= 0 {
		c.perPage = perPage
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
			KeyVerifyToken: o.VerifyToken, KeySubscriptionID: o.SubscriptionID,
		}
	}
	return c
}

// newHTTPClient builds the outbound client, with a pool that keeps the
// connection to the single Strava host between pages.
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

// Slug returns "strava".
func (c *Connector) Slug() string { return Slug }

// SyncPolicy implements connector.SyncPolicied.
// Application rate limits are low (1,000 reads a day by default), activities
// are uploaded and edited late, and notifications cover new ones.
func (c *Connector) SyncPolicy() connector.SyncPolicy {
	return connector.SyncPolicy{
		PullEvery:            12 * time.Hour,
		PullEveryWithoutPush: 3 * time.Hour,
		Lookback:             24 * time.Hour,
		Reconcile:            7 * 24 * time.Hour,
	}
}

// RequiredKeys lists the operator keys: CLIENT_ID and CLIENT_SECRET. The
// webhook keys are optional: pulls work without them.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// Enabled reports whether cfg holds both required keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != ""
}

// OptionalKeys lists the keys it also reads and runs without: VERIFY_TOKEN and SUBSCRIPTION_ID (push subscription).
func (c *Connector) OptionalKeys() []string { return []string{KeyVerifyToken, KeySubscriptionID} }

// Configure hands the connector its keys (the OHS_STRAVA_* values of the
// process configuration). It may be called again; the last call wins. Client
// id and secret are required.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyClientSecret] == "" {
		return errors.New("strava: OHS_STRAVA_CLIENT_ID and OHS_STRAVA_CLIENT_SECRET are both required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = connector.Config{
		KeyClientID: cfg[KeyClientID], KeyClientSecret: cfg[KeyClientSecret],
		KeyVerifyToken: cfg[KeyVerifyToken], KeySubscriptionID: cfg[KeySubscriptionID],
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

// webhookKeys returns the configured verify token and subscription id.
func (c *Connector) webhookKeys() (verifyToken, subscriptionID string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[KeyVerifyToken], c.keys[KeySubscriptionID]
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
