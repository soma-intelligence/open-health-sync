// SPDX-License-Identifier: AGPL-3.0-only

// Package sandbox is a provider that does not exist: it runs the whole OAuth
// connect flow against an authorization server and a data API that live in
// this package (Server), and serves synthetic sleep, heart rate, steps and
// workouts from the demo generator (internal/demo). It lets a developer try
// connections, backfills, pulls, token refresh and the read API in the first
// minutes, without a developer account at any vendor.
//
// It is off unless the operator sets OHS_SANDBOX_ENABLED=true. It is never
// turned on by the absence of other providers: an instance whose operator
// has not configured a vendor yet (or only takes device uploads) is not a
// test instance, and synthetic records next to real ones there would be a
// mistake nobody chose. Optional: OHS_SANDBOX_SECRET, the key that signs the
// sandbox's codes and tokens; without it a built-in key is used, which is
// enough because the tokens only ever unlock invented data.
//
// The consent page is served by the instance itself, on the provider-facing
// route GET /inbound/sandbox (connectors cannot mount routes of their own;
// this route hands a GET to the connector's AnswerChallenge). The token
// endpoint and the data API are reached in process, through an http.Client
// whose transport calls Server directly: the requests, answers, status codes
// and refusals are real HTTP, with no listener.
//
// Nothing it stores can pass for real data: the provider and the source are
// "sandbox", the source name says "Sandbox (synthetic)", and every record's
// metadata holds "synthetic": true (demo.MetadataSynthetic) and the persona.
package sandbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Slug is the provider name of this connector.
const Slug = "sandbox"

// Key names (OHS_SANDBOX_<KEY>).
const (
	KeyEnabled = "ENABLED"
	KeySecret  = "SECRET"
)

// ClientID is the sandbox application's client id. Its client secret is the
// signing key.
const ClientID = "ohs-sandbox"

// defaultSecret signs codes and tokens when OHS_SANDBOX_SECRET is unset. It
// is public on purpose: a forged sandbox token reads invented data only, and
// the core's single-use OAuth state still guards every connection.
const defaultSecret = "open-health-sync sandbox provider: synthetic data only"

// inProcessBase is the base URL the connector calls Server under. The host
// is never resolved: the transport serves every request in process.
const inProcessBase = "http://sandbox.invalid"

func init() {
	connector.Register(New(Options{}))
}

// Options configures a Connector. Zero values take the defaults.
type Options struct {
	// Secret signs codes and tokens (defaultSecret when empty).
	Secret string
	// Base and HTTPClient, when set, reach a Server elsewhere (a test's
	// httptest server). By default the connector's own Server is called
	// in process.
	Base       string
	HTTPClient *http.Client
	// AccessTTL is the lifetime of an access token (DefaultAccessTTL when
	// zero).
	AccessTTL time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
}

// Connector implements connector.OAuthConnector and connector.PushConnector
// (for the consent page only) for the sandbox provider.
type Connector struct {
	mu     sync.RWMutex
	opts   Options
	server *Server
	base   string
	httpc  *http.Client
	now    func() time.Time
}

var (
	_ connector.OAuthConnector     = (*Connector)(nil)
	_ connector.PushConnector      = (*Connector)(nil)
	_ connector.ChallengeResponder = (*Connector)(nil)
	_ connector.Configurable       = (*Connector)(nil)
	_ connector.SyncPolicied       = (*Connector)(nil)
)

// New returns a sandbox connector.
func New(o Options) *Connector {
	c := &Connector{opts: o, now: o.Now}
	if c.now == nil {
		c.now = time.Now
	}
	c.setSecret(o.Secret)
	return c
}

// setSecret builds the server for a key and, unless the options name another
// one, the in-process client that reaches it.
func (c *Connector) setSecret(secret string) {
	if secret == "" {
		secret = defaultSecret
	}
	srv := newServer([]byte(secret), ClientID, secret, c.now, c.opts.AccessTTL)
	base, httpc := c.opts.Base, c.opts.HTTPClient
	if base == "" || httpc == nil {
		base, httpc = inProcessBase, &http.Client{Timeout: 30 * time.Second, Transport: inProcess{srv}}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.server, c.base, c.httpc = srv, base, httpc
}

// Server returns the sandbox provider the connector talks to, to serve it
// elsewhere (a test's httptest server).
func (c *Connector) Server() *Server {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.server
}

func (c *Connector) state() (*Server, string, *http.Client) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.server, c.base, c.httpc
}

// inProcess is an http.RoundTripper that serves every request with a
// handler, without a network.
type inProcess struct{ h http.Handler }

func (t inProcess) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, r)
	res := rec.Result()
	res.Request = r
	return res, nil
}

// Slug returns "sandbox".
func (c *Connector) Slug() string { return Slug }

// Enabled reports whether the operator turned the sandbox on:
// OHS_SANDBOX_ENABLED set to a true value (true, 1, t, yes).
func (c *Connector) Enabled(cfg connector.Config) bool { return enabled(cfg[KeyEnabled]) }

func enabled(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "yes" || v == "on" {
		return true
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}

// Configure takes OHS_SANDBOX_SECRET. A configuration that does not turn the
// sandbox on is refused, so it never runs by mistake.
func (c *Connector) Configure(cfg connector.Config) error {
	if !enabled(cfg[KeyEnabled]) {
		return errors.New("sandbox: OHS_SANDBOX_ENABLED is not true")
	}
	c.setSecret(cfg[KeySecret])
	return nil
}

// SyncPolicy implements connector.SyncPolicied: a pull every hour, as the
// sandbox sends no notifications, re-reading the day before (the demo data
// of a day appears once the day is over).
func (c *Connector) SyncPolicy() connector.SyncPolicy {
	return connector.SyncPolicy{
		PullEvery:            time.Hour,
		PullEveryWithoutPush: time.Hour,
		Lookback:             24 * time.Hour,
	}
}

// Map turns one stored raw line into canonical records. Pure.
func (c *Connector) Map(raw connector.RawRecord) (model.MapResult, error) {
	res, err := mapRecord(raw.DataType, raw.Fields, raw.UserID)
	if err != nil {
		return model.MapResult{}, err
	}
	if res == nil {
		return model.MapResult{}, nil
	}
	return *res, nil
}

// errNoNotifications answers a POST to /inbound/sandbox: the sandbox sends
// none.
var errNoNotifications = errors.New("sandbox: the sandbox provider sends no notifications")

// HandleInbound refuses every notification: the sandbox is a PushConnector
// only so that GET /inbound/sandbox serves its consent page.
func (c *Connector) HandleInbound(_ context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		return nil, nil
	}
	return nil, errNoNotifications
}
