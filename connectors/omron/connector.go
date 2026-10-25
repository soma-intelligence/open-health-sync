// SPDX-License-Identifier: AGPL-3.0-only

// Package omron is the OMRON connector: OAuth connect and pulls of blood
// pressure and weight measurements from the official OMRON Wellness (Partner)
// API, and the mapping of the stored raw lines to canonical records.
//
// API docs: https://omronhealthcare-ohi.atlassian.net/wiki/spaces/API/pages/2480308225/Omron+Partner+API+for+US
// (access is requested at https://omronhealthcare.com/api/).
//
// Keys: OHS_OMRON_CLIENT_ID and OHS_OMRON_CLIENT_SECRET (required), and
// OHS_OMRON_ENVIRONMENT (optional): "production" (the default) or "staging",
// for the staging credentials OMRON hands out first. Staging and production
// users are separate.
//
// OMRON sends a notification to the URI registered with the app when a user
// uploads readings, and asks partners to pull after it. Its payload is not in
// the public documentation, so this connector does not receive it yet: data
// arrives with the scheduled pulls. See docs/connectors/omron.md.
package omron

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Slug is the provider name of this connector.
const Slug = "omron"

// Key names (OHS_OMRON_<KEY>).
const (
	KeyClientID     = "CLIENT_ID"
	KeyClientSecret = "CLIENT_SECRET"
	// KeyEnvironment is optional: production (default) or staging.
	KeyEnvironment = "ENVIRONMENT"
)

// Endpoints are the three URLs of one OMRON environment.
type Endpoints struct {
	Authorize, Token, Measurement string
}

// Environments maps OHS_OMRON_ENVIRONMENT to its endpoints.
var Environments = map[string]Endpoints{
	"production": {
		Authorize:   "https://prd-oauth-website.ohiomron.com/connect/authorize",
		Token:       "https://prd-oauth.ohiomron.com/prd/connect/token",
		Measurement: "https://prd-oauth.ohiomron.com/prd/api/measurement",
	},
	"staging": {
		Authorize:   "https://stg-oauth-website.ohiomron.com/connect/authorize",
		Token:       "https://stg-oauth.ohiomron.com/stg/connect/token",
		Measurement: "https://stg-oauth.ohiomron.com/stg/api/measurement",
	},
}

// DefaultEnvironment is used when OHS_OMRON_ENVIRONMENT is unset.
const DefaultEnvironment = "production"

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the production defaults.
type Options struct {
	ClientID, ClientSecret string
	// Environment picks the endpoints. Endpoints, when its Token is set,
	// wins: tests point it at an httptest fake.
	Environment string
	Endpoints   Endpoints
	HTTPClient  *http.Client
	// PageSize is the limit of one measurement request (default 500).
	PageSize int
	// RetryBase is the first wait of the transport error retry ladder (10 s).
	RetryBase time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector for OMRON.
type Connector struct {
	mu    sync.RWMutex
	keys  connector.Config
	opts  Options // what New was given, for NewInstance
	ep    Endpoints
	fixed bool // ep came from Options.Endpoints: Configure keeps it

	httpc     *http.Client
	pageSize  int
	retryBase time.Duration
	now       func() time.Time
	sleep     func(ctx context.Context, d time.Duration) error
}

var (
	_ connector.OAuthConnector = (*Connector)(nil)
	_ connector.KeyedConnector = (*Connector)(nil)
	_ connector.Configurable   = (*Connector)(nil)
)

// New returns an OMRON connector.
func New(o Options) *Connector {
	c := &Connector{
		opts:      o,
		ep:        o.Endpoints,
		fixed:     o.Endpoints.Token != "",
		httpc:     o.HTTPClient,
		pageSize:  o.PageSize,
		retryBase: o.RetryBase,
		now:       o.Now,
		sleep:     o.Sleep,
	}
	if !c.fixed {
		c.ep = Environments[DefaultEnvironment]
		if e, ok := Environments[o.Environment]; ok {
			c.ep = e
		}
	}
	if c.httpc == nil {
		c.httpc = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		}
	}
	if c.pageSize <= 0 {
		c.pageSize = 500
	}
	if c.retryBase <= 0 {
		c.retryBase = 10 * time.Second
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

// Slug returns "omron".
func (c *Connector) Slug() string { return Slug }

// SyncPolicy implements connector.SyncPolicied.
// Cuff readings reach OMRON when the phone app syncs, often days later, and
// the measurement API is rate limited.
func (c *Connector) SyncPolicy() connector.SyncPolicy {
	return connector.SyncPolicy{
		PullEveryWithoutPush: 3 * time.Hour,
		Lookback:             72 * time.Hour,
		Reconcile:            14 * 24 * time.Hour,
	}
}

// RequiredKeys lists the operator keys: CLIENT_ID and CLIENT_SECRET.
// ENVIRONMENT is optional and not listed.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// Enabled reports whether cfg holds both required keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != ""
}

// OptionalKeys lists the keys it also reads and runs without: ENVIRONMENT (default production).
func (c *Connector) OptionalKeys() []string { return []string{KeyEnvironment} }

// Configure hands the connector its keys. An unknown environment is an
// error.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyClientSecret] == "" {
		return errors.New("omron: OHS_OMRON_CLIENT_ID and OHS_OMRON_CLIENT_SECRET are both required")
	}
	env := cfg[KeyEnvironment]
	if env == "" {
		env = DefaultEnvironment
	}
	ep, ok := Environments[env]
	if !ok {
		return fmt.Errorf("omron: OHS_OMRON_ENVIRONMENT %q is not one of production, staging", env)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = connector.Config{KeyClientID: cfg[KeyClientID], KeyClientSecret: cfg[KeyClientSecret], KeyEnvironment: env}
	if !c.fixed {
		c.ep = ep
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

// endpoints returns the URLs of the configured environment.
func (c *Connector) endpoints() Endpoints {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ep
}

// Map turns one stored raw line into canonical records. Pure. A line of an
// unknown data type gives an empty result and no error.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	var (
		res *model.MapResult
		err error
	)
	switch raw.DataType {
	case DataTypeBloodPressure:
		res, err = mapBloodPressure(raw.Fields, raw.UserID)
	case DataTypeWeight:
		res, err = mapWeight(raw.Fields, raw.UserID)
	default:
		return model.MapResult{}, nil
	}
	if err != nil || res == nil {
		return model.MapResult{}, err
	}
	return *res, nil
}
