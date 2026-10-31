// SPDX-License-Identifier: AGPL-3.0-only

// Package smartfhir is the SMART on FHIR connector: a patient connects the
// patient portal of a health system (Epic MyChart, Oracle Health, any
// certified EHR) and the server pulls their record from its FHIR R4 API,
// following the US Core profiles every certified EHR exposes to patients.
//
// Specs: SMART App Launch 2.x (standalone launch, PKCE S256),
// https://hl7.org/fhir/smart-app-launch/ and US Core,
// https://hl7.org/fhir/us/core/.
//
// One instance talks to one FHIR server, named by OHS_SMART_FHIR_FHIR_BASE_URL
// (the "iss" of the health system, for example the Epic sandbox
// https://fhir.epic.com/interconnect-fhir-oauth/api/FHIR/R4). Its OAuth
// endpoints are read from <base>/.well-known/smart-configuration unless
// OHS_SMART_FHIR_AUTHORIZE_URL and OHS_SMART_FHIR_TOKEN_URL name them.
//
// Keys: CLIENT_ID and FHIR_BASE_URL (required). CLIENT_SECRET (optional) makes
// it a confidential client; without it the client is public and PKCE alone
// protects the exchange. PKCE_SECRET (optional) keys the derivation of the
// PKCE verifier when there is no client secret. SCOPES (optional) replaces the
// default scopes.
//
// Raw first: every resource is stored verbatim, one FHIR resource per line.
// Map turns Observations into records: the vital signs Open Health Sync knows
// by their LOINC code become quantity samples, everything else (labs, scores,
// coded results) a generic observation of type_concept "ehr". The other
// resources (Patient, Condition, AllergyIntolerance, MedicationRequest,
// Immunization, Procedure, DiagnosticReport) are kept raw only for now.
// See docs/connectors/smart_fhir.md.
package smartfhir

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Slug is the provider name of this connector.
const Slug = "smart_fhir"

// Key names (OHS_SMART_FHIR_<KEY>).
const (
	KeyClientID     = "CLIENT_ID"
	KeyClientSecret = "CLIENT_SECRET"
	KeyFHIRBase     = "FHIR_BASE_URL"
	KeyAuthorizeURL = "AUTHORIZE_URL"
	KeyTokenURL     = "TOKEN_URL"
	KeyScopes       = "SCOPES"
	KeyPKCESecret   = "PKCE_SECRET"
)

// DefaultScopes are the SMART v1 scopes asked at consent: the patient
// context, the read of each resource Fetch pulls, and offline_access for a
// refresh token. Epic grants what the app registration lists whatever the
// request says; other servers grant what is asked.
var DefaultScopes = []string{
	"launch/patient", "openid", "fhirUser", "offline_access",
	"patient/Patient.read", "patient/Observation.read", "patient/Condition.read",
	"patient/AllergyIntolerance.read", "patient/MedicationRequest.read",
	"patient/Immunization.read", "patient/Procedure.read", "patient/DiagnosticReport.read",
}

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the production defaults.
type Options struct {
	HTTPClient *http.Client
	// PageDelay paces consecutive pages of a search. Negative disables the
	// pause; zero takes the default.
	PageDelay time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
}

// endpoints are the OAuth endpoints of the configured server.
type endpoints struct {
	authorize, token string
}

// Connector implements connector.OAuthConnector for one SMART on FHIR server.
type Connector struct {
	mu   sync.RWMutex
	keys connector.Config
	opts Options
	eps  *endpoints // discovered or configured; nil until known
	// pkceKey keys the PKCE verifier when no secret is configured: random per
	// process, so a connect must finish on the instance that started it.
	pkceKey string

	httpc     *http.Client
	pageDelay time.Duration
	now       func() time.Time
}

var (
	_ connector.OAuthConnector = (*Connector)(nil)
	_ connector.StateExchanger = (*Connector)(nil)
	_ connector.KeyedConnector = (*Connector)(nil)
	_ connector.OptionalKeyed  = (*Connector)(nil)
	_ connector.Configurable   = (*Connector)(nil)
	_ connector.SyncPolicied   = (*Connector)(nil)
	_ connector.Instancer      = (*Connector)(nil)
)

// New returns a SMART on FHIR connector.
func New(o Options) *Connector {
	c := &Connector{opts: o, httpc: o.HTTPClient, pageDelay: o.PageDelay, now: o.Now}
	if c.httpc == nil {
		c.httpc = &http.Client{
			Timeout: 60 * time.Second,
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
	if c.pageDelay == 0 {
		c.pageDelay = 200 * time.Millisecond
	}
	if c.now == nil {
		c.now = time.Now
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	c.pkceKey = hex.EncodeToString(b)
	return c
}

// Slug returns "smart_fhir".
func (c *Connector) Slug() string { return Slug }

// SyncPolicy implements connector.SyncPolicied.
// Health systems send no notifications and a chart changes after visits, not
// by the minute: one pull a day. Results are often filed days after they
// were taken, so each pull re-reads a week. Backfill reaches the longest
// history the scheduler allows, one year, in monthly jobs.
func (c *Connector) SyncPolicy() connector.SyncPolicy {
	return connector.SyncPolicy{
		PullEvery:            24 * time.Hour,
		PullEveryWithoutPush: 24 * time.Hour,
		Lookback:             7 * 24 * time.Hour,
		MaxCatchUp:           30 * 24 * time.Hour,
		Backfill:             365 * 24 * time.Hour,
		BackfillChunk:        30 * 24 * time.Hour,
	}
}

// RequiredKeys lists the operator keys: CLIENT_ID and FHIR_BASE_URL.
func (c *Connector) RequiredKeys() []string { return []string{KeyClientID, KeyFHIRBase} }

// OptionalKeys lists the keys it reads and runs without.
func (c *Connector) OptionalKeys() []string {
	return []string{KeyClientSecret, KeyAuthorizeURL, KeyTokenURL, KeyScopes, KeyPKCESecret}
}

// Enabled reports whether cfg holds both required keys.
func (c *Connector) Enabled(cfg connector.Config) bool {
	return cfg[KeyClientID] != "" && cfg[KeyFHIRBase] != ""
}

// Configure hands the connector its keys. The base and any endpoint given
// must be absolute https URLs (http is accepted for a loopback host, for
// local test servers). Endpoints not given are discovered at the first
// connect.
func (c *Connector) Configure(cfg connector.Config) error {
	if cfg[KeyClientID] == "" || cfg[KeyFHIRBase] == "" {
		return errors.New("smart_fhir: OHS_SMART_FHIR_CLIENT_ID and OHS_SMART_FHIR_FHIR_BASE_URL are both required")
	}
	for _, k := range []string{KeyFHIRBase, KeyAuthorizeURL, KeyTokenURL} {
		if v := cfg[k]; v != "" {
			if err := checkURL(v); err != nil {
				return fmt.Errorf("smart_fhir: OHS_SMART_FHIR_%s: %w", k, err)
			}
		}
	}
	if (cfg[KeyAuthorizeURL] == "") != (cfg[KeyTokenURL] == "") {
		return errors.New("smart_fhir: set OHS_SMART_FHIR_AUTHORIZE_URL and OHS_SMART_FHIR_TOKEN_URL together, or neither")
	}
	keys := connector.Config{}
	for k, v := range cfg {
		keys[k] = v
	}
	keys[KeyFHIRBase] = strings.TrimRight(cfg[KeyFHIRBase], "/")
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = keys
	c.eps = nil
	if cfg[KeyAuthorizeURL] != "" {
		c.eps = &endpoints{authorize: cfg[KeyAuthorizeURL], token: cfg[KeyTokenURL]}
	}
	return nil
}

// NewInstance returns a connector like c without keys, sharing its HTTP
// client, for a tenant's own keys (connector.Instancer): a tenant can point
// its instance at another health system.
func (c *Connector) NewInstance() connector.Connector {
	o := c.opts
	o.HTTPClient = c.httpc
	return New(o)
}

// checkURL accepts an absolute https URL, or http on a loopback host.
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not an absolute URL", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" || net.ParseIP(host).IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("%q must use https", raw)
}

// key returns one configured key.
func (c *Connector) key(k string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[k]
}

// base returns the FHIR base URL, without a trailing slash.
func (c *Connector) base() string { return c.key(KeyFHIRBase) }

// scopes returns the scopes asked at consent.
func (c *Connector) scopes() []string {
	if s := strings.Fields(c.key(KeyScopes)); len(s) > 0 {
		return s
	}
	return append([]string(nil), DefaultScopes...)
}

// verifierKey is the HMAC key of the PKCE verifier: the client secret, else
// PKCE_SECRET, else the per-process random key.
func (c *Connector) verifierKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if s := c.keys[KeyClientSecret]; s != "" {
		return s
	}
	if s := c.keys[KeyPKCESecret]; s != "" {
		return s
	}
	return c.pkceKey
}

// Map turns one stored raw line into canonical records. Pure. Only
// Observation lines give records; every other resource type gives an empty
// result and no error.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	if raw.DataType != DataTypeObservation {
		return model.MapResult{}, nil
	}
	return mapObservation(raw.Fields, raw.UserID)
}

// discoverTimeout bounds the read of .well-known/smart-configuration.
const discoverTimeout = 15 * time.Second

// endpointsOf returns the OAuth endpoints, discovering them on first use.
func (c *Connector) endpointsOf(ctx context.Context) (endpoints, error) {
	c.mu.RLock()
	eps := c.eps
	c.mu.RUnlock()
	if eps != nil {
		return *eps, nil
	}
	base := c.base()
	if base == "" {
		return endpoints{}, errNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()
	var conf struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := c.getJSON(ctx, "", base+"/.well-known/smart-configuration", &conf); err != nil {
		return endpoints{}, fmt.Errorf("smart_fhir: discover endpoints: %w", err)
	}
	if conf.AuthorizationEndpoint == "" || conf.TokenEndpoint == "" {
		return endpoints{}, errors.New("smart_fhir: discover endpoints: smart-configuration names no authorization or token endpoint")
	}
	for _, u := range []string{conf.AuthorizationEndpoint, conf.TokenEndpoint} {
		if err := checkURL(u); err != nil {
			return endpoints{}, fmt.Errorf("smart_fhir: discover endpoints: %w", err)
		}
	}
	found := endpoints{authorize: conf.AuthorizationEndpoint, token: conf.TokenEndpoint}
	c.mu.Lock()
	if c.keys[KeyFHIRBase] == base { // keys unchanged meanwhile
		c.eps = &found
	}
	c.mu.Unlock()
	return found, nil
}
