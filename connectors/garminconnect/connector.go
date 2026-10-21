// SPDX-License-Identifier: AGPL-3.0-only

// Package garminconnect is the Garmin Connect account connector: the user
// signs in with the email and password of their own Garmin Connect account,
// and the connector pulls what the Garmin Connect app and website show (daily
// activity, sleep with stages, heart rate, HRV, stress and activities) and
// maps it to canonical records.
//
// Garmin's public Health and Activity APIs need a Garmin-approved partner app
// (the separate "garmin" connector). This connector instead uses the private
// Connect API the Garmin Connect app and website use: sign in over Garmin's
// SSO for the Connect mobile client, exchange the service ticket for an
// OAuth2 bearer, and read the Connect service endpoints. It follows the open
// source python-garminconnect library (MIT license,
// https://github.com/cyberjunky/python-garminconnect) for the flow and the
// endpoints. This is a Go re-implementation; no python-garminconnect code is
// copied.
//
// This path is not approved by Garmin and is not supported: it can break
// whenever Garmin changes its sign-in or its site, and Garmin's anti-bot
// protection may refuse a non-browser client. docs/connectors/garmin-connect.md
// says so plainly. No operator keys: the client ids are the Garmin Connect
// app's own public values.
package garminconnect

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Slug is the provider name of this connector.
const Slug = "garmin_connect"

// Production endpoints of Garmin's Connect client.
const (
	// DefaultSSOBase is the SSO host of the Connect mobile client.
	DefaultSSOBase = "https://sso.garmin.com"
	// DefaultTokenURL exchanges a service ticket for an OAuth2 bearer.
	DefaultTokenURL = "https://diauth.garmin.com/di-oauth2-service/oauth/token"
	// DefaultAPIBase serves the Connect service endpoints.
	DefaultAPIBase = "https://connectapi.garmin.com"
)

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the production defaults.
type Options struct {
	// SSOBase, TokenURL and APIBase point at the Garmin hosts. Tests point
	// them at an httptest fake.
	SSOBase, TokenURL, APIBase string
	HTTPClient                 *http.Client
	// RetryBase is the first wait of the transport error retry ladder (5 s).
	RetryBase time.Duration
	// ChunkDelay paces consecutive Connect requests, so a backfill does not
	// hammer the site. Negative disables the pause; zero takes the default
	// (1 s).
	ChunkDelay time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
	// Sleep waits between transport retries and between paced requests; tests
	// replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Connector implements connector.OAuthConnector and
// connector.PasswordConnector for Garmin Connect.
type Connector struct {
	ssoBase, tokenURL, apiBase string
	httpc                      *http.Client
	retryBase                  time.Duration
	chunkDelay                 time.Duration
	now                        func() time.Time
	sleep                      func(ctx context.Context, d time.Duration) error

	mapper *mapper
}

var (
	_ connector.OAuthConnector    = (*Connector)(nil)
	_ connector.PasswordConnector = (*Connector)(nil)
)

// New returns a Garmin Connect account connector.
func New(o Options) *Connector {
	c := &Connector{
		ssoBase:    o.SSOBase,
		tokenURL:   o.TokenURL,
		apiBase:    o.APIBase,
		httpc:      o.HTTPClient,
		retryBase:  o.RetryBase,
		chunkDelay: o.ChunkDelay,
		now:        o.Now,
		sleep:      o.Sleep,
		mapper:     newMapper(),
	}
	if c.ssoBase == "" {
		c.ssoBase = DefaultSSOBase
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
		c.chunkDelay = time.Second
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.sleep == nil {
		c.sleep = pause
	}
	return c
}

// newHTTPClient builds the outbound client. A cookie jar is not kept: the
// sign-in uses the JSON mobile endpoint, which answers with the ticket in the
// body rather than a redirect cookie.
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

// Slug returns "garmin_connect".
func (c *Connector) Slug() string { return Slug }

// Enabled is always true: the connector needs no operator keys, so the
// console or tenant settings decide whether to offer it.
func (c *Connector) Enabled(connector.Config) bool { return true }

// Map turns one stored raw line into canonical records. Pure. A line of an
// unknown data type gives an empty result and no error.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	res, err := c.mapper.mapRecord(raw.DataType, raw.Fields, raw.UserID)
	if err != nil || res == nil {
		return model.MapResult{}, err
	}
	return *res, nil
}
