// SPDX-License-Identifier: AGPL-3.0-only

// Package dexcom is the Dexcom connector: OAuth connect and pulls of
// estimated glucose values (EGVs) from the official Dexcom API v3, and the
// mapping of the stored raw lines to blood_glucose samples.
//
// API docs: https://developer.dexcom.com/docs/dexcomv3/endpoint-overview
//
// Keys: OHS_DEXCOM_CLIENT_ID and OHS_DEXCOM_CLIENT_SECRET (required), and
// OHS_DEXCOM_REGION (optional): "us" (the default, api.dexcom.com), "eu"
// (api.dexcom.eu, every country outside the US except Japan), "jp"
// (api.dexcom.jp) or "sandbox" (sandbox-api.dexcom.com, the test users of
// the developer portal). A Dexcom app is registered for one region; a user
// of another region cannot connect to it.
//
// Dexcom sends no notifications: data is pulled. Readings uploaded by the
// Dexcom mobile apps reach the API with a delay (1 hour in the US, 3 hours
// elsewhere), so the latest hours of a pull are always empty. Access tokens
// last 2 hours; each refresh token works once and is replaced by the
// refresh. See docs/connectors/dexcom.md.
package dexcom

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
const Slug = "dexcom"

// Key names (OHS_DEXCOM_<KEY>).
const (
	KeyClientID     = "CLIENT_ID"
	KeyClientSecret = "CLIENT_SECRET"
	// KeyRegion is optional: us (default), eu, jp or sandbox.
	KeyRegion = "REGION"
)

// Regions maps OHS_DEXCOM_REGION to the API base of that region.
var Regions = map[string]string{
	"us":      "https://api.dexcom.com",
	"eu":      "https://api.dexcom.eu",
	"jp":      "https://api.dexcom.jp",
	"sandbox": "https://sandbox-api.dexcom.com",
}

// DefaultRegion is the region used when OHS_DEXCOM_REGION is unset.
const DefaultRegion = "us"

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the production defaults.
type Options struct {
	ClientID, ClientSecret string
	// Region picks the API base (see Regions). APIBase, when set, wins: tests
	// point it at an httptest fake.
	Region, APIBase string
	HTTPClient      *http.Client
	// RetryBase is the first wait of the transport error retry ladder (10 s).
	RetryBase time.Duration
	// ChunkDelay paces consecutive request windows. Negative disables the
	// pause; zero takes the default.
	ChunkDelay time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector for Dexcom.
type Connector struct {
	mu      sync.RWMutex
	keys    connector.Config
	opts    Options // what New was given, for NewInstance
	apiBase string
	fixed   bool // apiBase came from Options.APIBase: Configure keeps it

	httpc      *http.Client
	retryBase  time.Duration
	chunkDelay time.Duration
	now        func() time.Time
	sleep      func(ctx context.Context, d time.Duration) error
}

var (
	_ connector.OAuthConnector = (*Connector)(nil)
	_ connector.KeyedConnector = (*Connector)(nil)
	_ connector.Configurable   = (*Connector)(nil)
)

// New returns a Dexcom connector.
func New(o Options) *Connector {
	c := &Connector{
		opts:       o,
		apiBase:    o.APIBase,
		fixed:      o.APIBase != "",
		httpc:      o.HTTPClient,
		retryBase:  o.RetryBase,
		chunkDelay: o.ChunkDelay,
		now:        o.Now,
		sleep:      o.Sleep,
	}
	if c.apiBase == "" {
		c.apiBase = Regions[DefaultRegion]
		if b, ok := Regions[o.Region]; ok {
			c.apiBase = b
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
	if c.retryBase <= 0 {
		c.retryBase = 10 * time.Second
	}
	if c.chunkDelay == 0 {
		c.chunkDelay = 500 * time.Millisecond
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

// Slug returns "dexcom".
func (c *Connector) Slug() string { return Slug }

// SyncPolicy implements connector.SyncPolicied.
// Readings from the Dexcom apps reach the API 1 hour late in the US and 3
// hours late elsewhere: each pull re-reads 6 hours, and the history is long.
func (c *Connector) SyncPolicy() connector.SyncPolicy {
	return connector.SyncPolicy{
		Lookback:  6 * time.Hour,
		Backfill:  90 * 24 * time.Hour,
		Reconcile: 2 * 24 * time.Hour,
	}
}

// RequiredKeys lists the operator keys: CLIENT_ID and CLIENT_SECRET. REGION
// is optional and not listed.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyClientSecret} }

// Enabled reports whether cfg holds both required keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyClientSecret] != ""
}

// OptionalKeys lists the keys it also reads and runs without: REGION (default us).
func (c *Connector) OptionalKeys() []string { return []string{KeyRegion} }

// Configure hands the connector its keys. The client id and secret are
// required; an unknown region is an error, so a typo never sends users to the
// wrong Dexcom.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyClientSecret] == "" {
		return errors.New("dexcom: OHS_DEXCOM_CLIENT_ID and OHS_DEXCOM_CLIENT_SECRET are both required")
	}
	region := cfg[KeyRegion]
	if region == "" {
		region = DefaultRegion
	}
	base, ok := Regions[region]
	if !ok {
		return fmt.Errorf("dexcom: OHS_DEXCOM_REGION %q is not one of us, eu, jp, sandbox", region)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = connector.Config{KeyClientID: cfg[KeyClientID], KeyClientSecret: cfg[KeyClientSecret], KeyRegion: region}
	if !c.fixed {
		c.apiBase = base
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

// base returns the API base of the configured region.
func (c *Connector) base() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiBase
}

// Map turns one stored raw line into canonical records. Pure. A line of an
// unknown data type gives an empty result and no error.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	if raw.DataType != DataTypeEGV {
		return model.MapResult{}, nil
	}
	res, err := mapEGV(raw.Fields, raw.UserID)
	if err != nil || res == nil {
		return model.MapResult{}, err
	}
	return *res, nil
}
