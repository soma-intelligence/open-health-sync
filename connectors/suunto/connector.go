// SPDX-License-Identifier: AGPL-3.0-only

// Package suunto is the Suunto connector: OAuth connect, pulls of the user's
// workouts (and, stored raw only, 24/7 sleep, recovery and daily activity)
// from the Suunto Cloud API, signed workout notifications, and the mapping of
// the stored raw lines to canonical workouts.
//
// API docs: https://apizone.suunto.com (How to start, FAQ, and the API
// reference behind the partner login). Access is granted through Suunto's
// partner program: companies and organizations apply, individuals cannot.
//
// Keys: OHS_SUUNTO_CLIENT_ID, OHS_SUUNTO_CLIENT_SECRET (the OAuth app
// configured in API Zone) and OHS_SUUNTO_SUBSCRIPTION_KEY (the API Zone
// subscription key, sent as Ocp-Apim-Subscription-Key on every API call), all
// required, handed over by the collect role with Configure (or set with
// Options in tests). Optional: OHS_SUUNTO_NOTIFICATION_SECRET, the secret
// Suunto signs notifications with; without it notifications are refused and
// data still arrives with the scheduled pulls.
//
// Suunto notifies one URL per application, for every user, so the operator
// sets it once in API Zone (see docs/connectors/suunto.md); the connector
// does not subscribe per connection.
//
// Only workout summaries are mapped. The FIT export of a workout (samples,
// laps, GPS) costs one request per workout and needs a FIT decoder; it is not
// fetched.
package suunto

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
const Slug = "suunto"

// Production endpoints.
const (
	DefaultOAuthBase = "https://cloudapi-oauth.suunto.com"
	DefaultAPIBase   = "https://cloudapi.suunto.com"
)

// Key names (OHS_SUUNTO_<KEY>).
const (
	KeyClientID           = "CLIENT_ID"
	KeyClientSecret       = "CLIENT_SECRET"
	KeySubscriptionKey    = "SUBSCRIPTION_KEY"
	KeyNotificationSecret = "NOTIFICATION_SECRET"
)

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the production defaults.
type Options struct {
	ClientID, ClientSecret, SubscriptionKey string
	// NotificationSecret is the optional notification signing secret.
	NotificationSecret string
	// OAuthBase serves the consent page and the token endpoints, APIBase
	// the data API. Tests point both at an httptest fake.
	OAuthBase, APIBase string
	HTTPClient         *http.Client
	// RetryBase is the first wait of the transport error retry ladder (10 s).
	RetryBase time.Duration
	// PageDelay paces consecutive requests. Negative disables the pause;
	// zero takes the default.
	PageDelay time.Duration
	// PerPage is the page size of the workout list (100 when zero); tests
	// shrink it to exercise paging.
	PerPage int
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries; tests replace it so retries do
	// not sleep for real.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector and connector.PushConnector
// for Suunto.
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
	_ connector.OAuthConnector  = (*Connector)(nil)
	_ connector.PushConnector   = (*Connector)(nil)
	_ connector.KeyedConnector  = (*Connector)(nil)
	_ connector.Configurable    = (*Connector)(nil)
	_ connector.Revoker         = (*Connector)(nil)
	_ connector.NotifiedFetcher = (*Connector)(nil)
)

// New returns a Suunto connector.
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
	if o.ClientID != "" || o.ClientSecret != "" || o.SubscriptionKey != "" {
		c.keys = connector.Config{
			KeyClientID: o.ClientID, KeyClientSecret: o.ClientSecret,
			KeySubscriptionKey: o.SubscriptionKey, KeyNotificationSecret: o.NotificationSecret,
		}
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

// Slug returns "suunto".
func (c *Connector) Slug() string { return Slug }

// RequiredKeys lists the operator keys: CLIENT_ID, CLIENT_SECRET and
// SUBSCRIPTION_KEY. NOTIFICATION_SECRET is optional: pulls work without it.
func (c *Connector) RequiredKeys() []string {
	return []string{KeyClientID, KeyClientSecret, KeySubscriptionKey}
}

// OptionalKeys lists the keys it also reads and runs without: NOTIFICATION_SECRET (notifications).
func (c *Connector) OptionalKeys() []string { return []string{KeyNotificationSecret} }

// Enabled reports whether cfg holds every required key.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != "" && cfg[KeySubscriptionKey] != ""
}

// Configure hands the connector its keys (the OHS_SUUNTO_* values of the
// process configuration). It may be called again; the last call wins.
func (c *Connector) Configure(cfg connector.Config) error {
	if !c.Enabled(cfg) {
		return errors.New("suunto: OHS_SUUNTO_CLIENT_ID, OHS_SUUNTO_CLIENT_SECRET and OHS_SUUNTO_SUBSCRIPTION_KEY are all required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = connector.Config{
		KeyClientID: cfg[KeyClientID], KeyClientSecret: cfg[KeyClientSecret],
		KeySubscriptionKey: cfg[KeySubscriptionKey], KeyNotificationSecret: cfg[KeyNotificationSecret],
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

// subscriptionKey returns the API Zone subscription key.
func (c *Connector) subscriptionKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[KeySubscriptionKey]
}

// notificationSecret returns the notification signing secret, or "".
func (c *Connector) notificationSecret() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[KeyNotificationSecret]
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
