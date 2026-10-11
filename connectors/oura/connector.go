// SPDX-License-Identifier: AGPL-3.0-only

// Package oura is the Oura connector: OAuth connect, pulls from the Oura Cloud
// API v2, and the mapping of the stored raw lines to canonical records.
//
// API docs: https://cloud.ouraring.com/v2/docs
//
// Keys: OHS_OURA_CLIENT_ID and OHS_OURA_CLIENT_SECRET, handed over by the
// collect role with Configure (or set with Options in tests).
//
// Pull only. Oura webhooks do not fit the core's push contract yet, so this
// connector implements neither connector.PushConnector nor
// connector.Subscriber, and the 6 hour pull collects everything:
//
//   - Oura subscribes a callback URL per application, not per user (one
//     subscription per data type and event type, created with the client id
//     and secret), while connector.Subscriber is called per connection and
//     Unsubscribe on one user's disconnect would cut every user off.
//   - Oura checks a new subscription with a GET carrying verification_token
//     and challenge, and expects {"challenge": ...} back. The core answers
//     GET /inbound/{provider} itself, with an empty 200, and never asks the
//     connector.
//   - A delete event names one Oura document (object_id). connector.InboundEvent
//     can only ask for a pull window, and the pipeline has no way to remove a
//     stored record by its provider id.
//
// Updates and deletes. A daily document (activity, readiness, SpO2) is
// re-sent with new values while its day is open: its samples are
// Replaceable, hashed on the day, so the newest value replaces the stored one.
// A sleep or a workout is identified by its Oura id and start and is
// Replaceable: a corrected copy replaces the stored figures (and, for a sleep,
// its phases), and a copy whose start moved supersedes the stored one. The
// figures of a main sleep (resting heart rate, respiratory rate) are also
// emitted as Replaceable samples keyed by the Oura id. A sleep Oura marks as
// deleted (type "deleted") maps to nothing; a record Oura removes after it
// was stored stays stored.
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

// Connector implements connector.OAuthConnector for Oura.
type Connector struct {
	mu   sync.RWMutex
	keys connector.Config

	accountBase, apiBase string
	httpc                *http.Client
	retryBase            time.Duration
	pageDelay            time.Duration
	now                  func() time.Time
	sleep                func(ctx context.Context, d time.Duration) error

	mapper *mapper
}

var (
	_ connector.OAuthConnector = (*Connector)(nil)
	_ connector.KeyedConnector = (*Connector)(nil)
	_ connector.Configurable   = (*Connector)(nil)
)

// New returns an Oura connector.
func New(o Options) *Connector {
	c := &Connector{
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

// Slug returns "oura".
func (c *Connector) Slug() string { return Slug }

// RequiredKeys lists the operator keys: CLIENT_ID and CLIENT_SECRET.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// Enabled reports whether cfg holds both keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != ""
}

// Configure hands the connector its keys (the OHS_OURA_* values of the
// process configuration). It may be called again; the last call wins. Both
// keys are required.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyClientSecret] == "" {
		return errors.New("oura: OHS_OURA_CLIENT_ID and OHS_OURA_CLIENT_SECRET are both required")
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
