// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestKeysAndEnabled(t *testing.T) {
	c := New(Options{})
	assert.Equal(t, []string{"CLIENT_ID", "CLIENT_SECRET"}, c.RequiredKeys())
	assert.False(t, c.Enabled(connector.Config{"CLIENT_ID": "a"}))
	assert.False(t, c.Enabled(connector.Config{"CLIENT_ID": "a", "CLIENT_SECRET": ""}))
	assert.True(t, c.Enabled(connector.Config{"CLIENT_ID": "a", "CLIENT_SECRET": "b"}))

	reg := connector.NewRegistry()
	reg.Register(c)
	assert.Empty(t, reg.Enabled(map[string]config.ProviderKeys{"oura": {"CLIENT_ID": "a"}}))
	assert.Len(t, reg.Enabled(map[string]config.ProviderKeys{"oura": {"CLIENT_ID": "a", "CLIENT_SECRET": "b"}}), 1)
}

// The connector registers itself, as a pulling connector and not a push one.
func TestRegistered(t *testing.T) {
	c, ok := connector.Get(Slug)
	require.True(t, ok)
	_, isOAuth := connector.AsOAuth(c)
	assert.True(t, isOAuth)
	_, isPush := connector.AsPush(c)
	assert.False(t, isPush, "Oura webhooks do not fit the push contract yet")
	_, isSub := c.(connector.Subscriber)
	assert.False(t, isSub)
}

// Keys come from Configure (or Options), never from the environment.
func TestCredentials_OnlyConfigured(t *testing.T) {
	t.Setenv("OHS_OURA_CLIENT_ID", "env-id")
	t.Setenv("OHS_OURA_CLIENT_SECRET", "env-secret")
	c := New(Options{})
	id, secret := c.credentials()
	assert.Empty(t, id)
	assert.Empty(t, secret)
	_, err := c.AuthorizeURL("s", "r")
	assert.ErrorIs(t, err, errNotConfigured)

	assert.Error(t, c.Configure(connector.Config{"CLIENT_ID": "cfg-id"}), "both keys are required")
	require.NoError(t, c.Configure(connector.Config{"CLIENT_ID": "cfg-id", "CLIENT_SECRET": "cfg-secret"}))
	id, secret = c.credentials()
	assert.Equal(t, "cfg-id", id)
	assert.Equal(t, "cfg-secret", secret)
}

func TestBaseURLDefaultsAndOverride(t *testing.T) {
	c := New(Options{})
	assert.Equal(t, DefaultAPIBase, c.apiBase)
	assert.Equal(t, DefaultAccountBase, c.accountBase)
	c = New(Options{APIBase: "http://127.0.0.1:9999", AccountBase: "http://127.0.0.1:9998"})
	assert.Equal(t, "http://127.0.0.1:9999", c.apiBase)
	assert.Equal(t, "http://127.0.0.1:9998", c.accountBase)
}

func TestAuthorizeURL(t *testing.T) {
	f := newFakeOura(t)
	raw, err := f.connector().AuthorizeURL("state-123", "http://localhost:8080/oauth/oura/callback")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "/oauth/authorize", u.Path)
	q := u.Query()
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "test-client-id", q.Get("client_id"))
	assert.Equal(t, "state-123", q.Get("state"))
	assert.Equal(t, "http://localhost:8080/oauth/oura/callback", q.Get("redirect_uri"))
	assert.Equal(t, "personal daily heartrate workout session spo2", q.Get("scope"), "space separated")
	assert.Empty(t, q.Get("client_secret"), "the secret never reaches the browser")
}

func TestAuthorizeURL_NotConfigured(t *testing.T) {
	_, err := New(Options{}).AuthorizeURL("s", "r")
	assert.Error(t, err)
}

func TestExchange(t *testing.T) {
	f := newFakeOura(t)
	tok, err := f.connector().Exchange(context.Background(), "code-1", "http://localhost/cb")
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-1", tok.Access)
	assert.Equal(t, "fake-refresh-token-1", tok.Refresh)
	assert.Equal(t, "fake-oura-user-0001", tok.ProviderUserID, "from personal_info")
	assert.Equal(t, []string{"personal", "daily", "heartrate", "workout", "session", "spo2"}, tok.Scopes)
	assert.Equal(t, fakeNow.Add(24*time.Hour), tok.Expiry, "now + expires_in")

	calls := f.Calls("/oauth/token")
	require.Len(t, calls, 1)
	form := calls[0].Form
	assert.Equal(t, "authorization_code", form.Get("grant_type"))
	assert.Equal(t, "code-1", form.Get("code"))
	assert.Equal(t, "http://localhost/cb", form.Get("redirect_uri"))
	assert.Equal(t, "test-client-id", form.Get("client_id"))
	assert.Equal(t, "test-client-secret", form.Get("client_secret"))

	info := f.Calls("/v2/usercollection/personal_info")
	require.Len(t, info, 1)
	assert.Equal(t, "Bearer fake-access-token-1", info[0].Auth)
}

// A failed personal_info lookup (the scope refused) does not fail the
// connect.
func TestExchange_PersonalInfoForbidden(t *testing.T) {
	f := newFakeOura(t)
	f.Forbidden = map[string]bool{"personal_info": true}
	tok, err := f.connector().Exchange(context.Background(), "code-1", "http://localhost/cb")
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-1", tok.Access)
	assert.Empty(t, tok.ProviderUserID)
}

func TestRefresh(t *testing.T) {
	f := newFakeOura(t)
	tok, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "old-refresh", ProviderUserID: "fake-oura-user-0001"})
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-1", tok.Access)
	assert.Equal(t, "fake-refresh-token-1", tok.Refresh, "Oura refresh tokens are single use: the new one is returned")
	assert.Equal(t, "fake-oura-user-0001", tok.ProviderUserID)
	calls := f.Calls("/oauth/token")
	require.Len(t, calls, 1)
	assert.Equal(t, "refresh_token", calls[0].Form.Get("grant_type"))
	assert.Equal(t, "old-refresh", calls[0].Form.Get("refresh_token"))
	assert.Equal(t, "test-client-secret", calls[0].Form.Get("client_secret"))
	assert.Empty(t, f.Calls("/v2/usercollection/personal_info"), "a refresh does not look the user up again")
}

func TestRefresh_KeepsScopesAndRefreshWhenAbsent(t *testing.T) {
	f := newFakeOura(t)
	f.TokenBody = `{"access_token":"a2","expires_in":86400}`
	tok, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "r", Scopes: []string{"daily"}})
	require.NoError(t, err)
	assert.Equal(t, "a2", tok.Access)
	assert.Equal(t, "r", tok.Refresh)
	assert.Equal(t, []string{"daily"}, tok.Scopes)
}

// A refresh token Oura rejects for good sends the connection to
// needs_reauth: an OAuth invalid_grant, or HTTP 401.
func TestRevokedRefreshToken(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP 400 invalid_grant", http.StatusBadRequest, `{"error":"invalid_grant","error_description":"refresh token is invalid"}`},
		{"invalid_grant on a 200", http.StatusOK, `{"error":"invalid_grant"}`},
		{"HTTP 401", http.StatusUnauthorized, `{"error":"invalid_client"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeOura(t)
			f.TokenStatus, f.TokenBody = c.status, c.body
			_, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "revoked"})
			require.ErrorIs(t, err, connector.ErrNeedsReauth)
		})
	}
}

func TestRefresh_RateLimited(t *testing.T) {
	f := newFakeOura(t)
	f.RateLimit = func(p string) bool { return p == "/oauth/token" }
	f.RetryAfter = "30"
	_, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "r"})
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 30*time.Second, rl.RetryAfter)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
}

// Transient token endpoint failures must not look like a revoked grant.
func TestRefresh_TransientIsNotReauth(t *testing.T) {
	cases := []struct {
		status int
		body   string
	}{
		{http.StatusBadGateway, `bad gateway`},
		{http.StatusInternalServerError, `{"error":"server_error"}`},
		{http.StatusBadRequest, `{"error":"invalid_request"}`},
		{http.StatusOK, `not json`},
		{http.StatusOK, `{"token_type":"bearer"}`},
	}
	for _, c := range cases {
		f := newFakeOura(t)
		f.TokenStatus, f.TokenBody = c.status, c.body
		_, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "r"})
		require.Error(t, err, c.body)
		assert.NotErrorIs(t, err, connector.ErrNeedsReauth, c.body)
	}
}

func TestRefresh_NoRefreshTokenNeedsReauth(t *testing.T) {
	_, err := New(Options{ClientID: "a", ClientSecret: "b"}).Refresh(context.Background(), connector.Tokens{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}

// Errors never carry a token.
func TestRefresh_ErrorsCarryNoToken(t *testing.T) {
	f := newFakeOura(t)
	f.TokenStatus, f.TokenBody = http.StatusInternalServerError, `{"error":"server_error"}`
	_, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "secret-refresh-value"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-refresh-value")
	assert.NotContains(t, err.Error(), "test-client-secret")
}
