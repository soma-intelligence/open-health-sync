// SPDX-License-Identifier: AGPL-3.0-only

package whoop

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
	assert.Empty(t, reg.Enabled(map[string]config.ProviderKeys{"whoop": {"CLIENT_ID": "a"}}))
	assert.Len(t, reg.Enabled(map[string]config.ProviderKeys{"whoop": {"CLIENT_ID": "a", "CLIENT_SECRET": "b"}}), 1)
}

// Keys come from Configure (or Options), never from the environment.
func TestCredentials_OnlyConfigured(t *testing.T) {
	t.Setenv("OHS_WHOOP_CLIENT_ID", "env-id")
	t.Setenv("OHS_WHOOP_CLIENT_SECRET", "env-secret")
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
	assert.Equal(t, DefaultAuthBase, c.authBase)
	c = New(Options{APIBase: "http://127.0.0.1:9999", AuthBase: "http://127.0.0.1:9998"})
	assert.Equal(t, "http://127.0.0.1:9999", c.apiBase)
	assert.Equal(t, "http://127.0.0.1:9998", c.authBase)
}

func TestAuthorizeURL(t *testing.T) {
	f := newFakeWhoop(t)
	raw, err := f.connector().AuthorizeURL("state-12345678", "http://localhost:8080/oauth/whoop/callback")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "/oauth2/auth", u.Path)
	q := u.Query()
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "test-client-id", q.Get("client_id"))
	assert.Equal(t, "state-12345678", q.Get("state"))
	assert.Equal(t, "http://localhost:8080/oauth/whoop/callback", q.Get("redirect_uri"))
	assert.Equal(t, "read:recovery read:cycles read:sleep read:workout read:profile read:body_measurement offline", q.Get("scope"))

	prod, err := New(Options{ClientID: "a", ClientSecret: "b"}).AuthorizeURL("s", "r")
	require.NoError(t, err)
	assert.Contains(t, prod, "https://api.prod.whoop.com/oauth/oauth2/auth?")
}

func TestAuthorizeURL_NotConfigured(t *testing.T) {
	_, err := New(Options{}).AuthorizeURL("s", "r")
	assert.Error(t, err)
}

func TestExchange(t *testing.T) {
	f := newFakeWhoop(t)
	tok, err := f.connector().Exchange(context.Background(), "code-1", "http://localhost/cb")
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-1", tok.Access)
	assert.Equal(t, "fake-refresh-token-1", tok.Refresh)
	assert.Equal(t, "10001", tok.ProviderUserID, "read from the profile, a number in the answer")
	assert.Equal(t, Scopes, tok.Scopes)
	assert.Equal(t, fakeNow.Add(time.Hour), tok.Expiry, "now + expires_in")

	calls := f.Calls("/oauth2/token")
	require.Len(t, calls, 1)
	form := calls[0].Form
	assert.Equal(t, "authorization_code", form.Get("grant_type"))
	assert.Equal(t, "code-1", form.Get("code"))
	assert.Equal(t, "http://localhost/cb", form.Get("redirect_uri"))
	assert.Equal(t, "test-client-id", form.Get("client_id"))
	assert.Equal(t, "test-client-secret", form.Get("client_secret"))

	prof := f.Calls("/v2/user/profile/basic")
	require.Len(t, prof, 1)
	assert.Equal(t, "Bearer fake-access-token-1", prof[0].Auth)
}

// A failed profile read does not fail the connection.
func TestExchange_ProfileFailureKeepsTokens(t *testing.T) {
	f := newFakeWhoop(t)
	f.ProfileStatus = http.StatusInternalServerError
	tok, err := f.connector().Exchange(context.Background(), "code-1", "http://localhost/cb")
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-1", tok.Access)
	assert.Empty(t, tok.ProviderUserID)
}

func TestRefresh(t *testing.T) {
	f := newFakeWhoop(t)
	tok, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "old-refresh", ProviderUserID: "10001"})
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-1", tok.Access)
	assert.Equal(t, "fake-refresh-token-1", tok.Refresh, "WHOOP rotates the refresh token")
	assert.Equal(t, "10001", tok.ProviderUserID)
	calls := f.Calls("/oauth2/token")
	require.Len(t, calls, 1)
	assert.Equal(t, "refresh_token", calls[0].Form.Get("grant_type"))
	assert.Equal(t, "old-refresh", calls[0].Form.Get("refresh_token"))
	assert.Equal(t, "offline", calls[0].Form.Get("scope"))
	assert.Empty(t, f.Calls("/v2/user/profile/basic"), "the user id is known: no profile read")
}

// A connection that never got its user id gets it on the next refresh.
func TestRefresh_FillsMissingUserID(t *testing.T) {
	f := newFakeWhoop(t)
	tok, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "r"})
	require.NoError(t, err)
	assert.Equal(t, "10001", tok.ProviderUserID)
}

func TestRefresh_KeepsScopesWhenAbsent(t *testing.T) {
	f := newFakeWhoop(t)
	f.TokenBody = `{"access_token":"a2","refresh_token":"r2","expires_in":3600}`
	tok, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "r", ProviderUserID: "10001", Scopes: []string{"offline"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"offline"}, tok.Scopes)
}

// A refresh token WHOOP rejects for good sends the connection to
// needs_reauth.
func TestRevokedRefreshToken(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP 400 invalid_grant", http.StatusBadRequest, `{"error":"invalid_grant","error_description":"The provided authorization grant is invalid"}`},
		{"HTTP 401 invalid_grant", http.StatusUnauthorized, `{"error":"invalid_grant"}`},
		{"HTTP 401 without a body", http.StatusUnauthorized, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeWhoop(t)
			f.TokenStatus, f.TokenBody = c.status, c.body
			if c.body == "" {
				f.TokenBody = " "
			}
			_, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "revoked"})
			require.ErrorIs(t, err, connector.ErrNeedsReauth)
		})
	}
}

// A rejected client is the operator's problem: no connection must go to
// needs_reauth for it, nor for a transient failure.
func TestRefresh_NotReauth(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
	}{
		{http.StatusUnauthorized, `{"error":"invalid_client"}`},
		{http.StatusBadGateway, `bad gateway`},
		{http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`},
		{http.StatusOK, `{"token_type":"bearer"}`},
	} {
		f := newFakeWhoop(t)
		f.TokenStatus, f.TokenBody = c.status, c.body
		_, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "r"})
		require.Error(t, err, c.body)
		assert.NotErrorIs(t, err, connector.ErrNeedsReauth, c.body)
	}
}

func TestRefresh_RateLimited(t *testing.T) {
	f := newFakeWhoop(t)
	f.TokenStatus, f.TokenBody = http.StatusTooManyRequests, `{}`
	_, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "r"})
	_, ok := connector.AsRateLimit(err)
	assert.True(t, ok, "got %v", err)
}

func TestRefresh_NoRefreshTokenNeedsReauth(t *testing.T) {
	_, err := New(Options{ClientID: "a", ClientSecret: "b"}).Refresh(context.Background(), connector.Tokens{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}
