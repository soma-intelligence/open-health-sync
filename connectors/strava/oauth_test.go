// SPDX-License-Identifier: AGPL-3.0-only

package strava

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
	assert.True(t, c.Enabled(connector.Config{"CLIENT_ID": "a", "CLIENT_SECRET": "b"}))

	reg := connector.NewRegistry()
	reg.Register(c)
	assert.Empty(t, reg.Enabled(map[string]config.ProviderKeys{"strava": {"CLIENT_ID": "a"}}))
	assert.Len(t, reg.Enabled(map[string]config.ProviderKeys{"strava": {"CLIENT_ID": "a", "CLIENT_SECRET": "b"}}), 1)
}

func TestRegistered(t *testing.T) {
	c, ok := connector.Get(Slug)
	require.True(t, ok)
	_, oauth := connector.AsOAuth(c)
	_, push := connector.AsPush(c)
	_, revoker := c.(connector.Revoker)
	_, challenge := c.(connector.ChallengeResponder)
	_, subscriber := c.(connector.Subscriber)
	assert.True(t, oauth && push && revoker && challenge)
	assert.False(t, subscriber, "Strava has one subscription per application, not per connection")
}

// Keys come from Configure (or Options), never from the environment.
func TestConfigure(t *testing.T) {
	t.Setenv("OHS_STRAVA_CLIENT_ID", "env-id")
	c := New(Options{})
	id, _ := c.credentials()
	assert.Empty(t, id)
	_, err := c.AuthorizeURL("s", "r")
	assert.ErrorIs(t, err, errNotConfigured)

	assert.Error(t, c.Configure(connector.Config{"CLIENT_ID": "cfg-id"}), "both keys are required")
	require.NoError(t, c.Configure(connector.Config{"CLIENT_ID": "cfg-id", "CLIENT_SECRET": "cfg-secret", "VERIFY_TOKEN": "vt", "SUBSCRIPTION_ID": "7"}))
	id, secret := c.credentials()
	assert.Equal(t, "cfg-id", id)
	assert.Equal(t, "cfg-secret", secret)
	vt, sub := c.webhookKeys()
	assert.Equal(t, "vt", vt)
	assert.Equal(t, "7", sub)
}

func TestBaseURLDefaults(t *testing.T) {
	c := New(Options{})
	assert.Equal(t, DefaultAPIBase, c.apiBase)
	assert.Equal(t, DefaultOAuthBase, c.oauthBase)
	assert.Equal(t, perPage, c.perPage)
}

func TestAuthorizeURL(t *testing.T) {
	f := newFakeStrava(t)
	raw, err := f.connector(0).AuthorizeURL("state-123", "http://localhost:8080/oauth/strava/callback")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "/oauth/authorize", u.Path)
	q := u.Query()
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "test-client-id", q.Get("client_id"))
	assert.Equal(t, "state-123", q.Get("state"))
	assert.Equal(t, "http://localhost:8080/oauth/strava/callback", q.Get("redirect_uri"))
	assert.Equal(t, "read,activity:read_all", q.Get("scope"))
	assert.Equal(t, "auto", q.Get("approval_prompt"))
}

func TestExchange(t *testing.T) {
	f := newFakeStrava(t)
	tok, err := f.connector(0).Exchange(context.Background(), "the-code", "http://localhost/cb")
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token", tok.Access)
	assert.Equal(t, "fake-refresh-token", tok.Refresh)
	assert.Equal(t, "1000001", tok.ProviderUserID)
	assert.True(t, tok.Expiry.Equal(time.Unix(1772517600, 0)), "expires_at wins")

	calls := f.Calls("/oauth/token")
	require.Len(t, calls, 1)
	form := calls[0].Form
	assert.Equal(t, "authorization_code", form.Get("grant_type"))
	assert.Equal(t, "the-code", form.Get("code"))
	assert.Equal(t, "test-client-id", form.Get("client_id"))
	assert.Equal(t, "test-client-secret", form.Get("client_secret"))
}

func TestExchange_NoAthleteIsAnError(t *testing.T) {
	f := newFakeStrava(t)
	f.TokenBody = `{"access_token":"a","refresh_token":"r","expires_in":21600}`
	_, err := f.connector(0).Exchange(context.Background(), "c", "r")
	assert.ErrorContains(t, err, "athlete")
}

func TestRefresh_KeepsUserAndScopes(t *testing.T) {
	f := newFakeStrava(t)
	f.TokenBody = `{"token_type":"Bearer","access_token":"new-a","refresh_token":"new-r","expires_in":21600}`
	old := connector.Tokens{Access: "a", Refresh: "r", ProviderUserID: "1000001", Scopes: []string{"read", "activity:read_all"}}
	nt, err := f.connector(0).Refresh(context.Background(), old)
	require.NoError(t, err)
	assert.Equal(t, "new-a", nt.Access)
	assert.Equal(t, "new-r", nt.Refresh)
	assert.Equal(t, "1000001", nt.ProviderUserID)
	assert.Equal(t, old.Scopes, nt.Scopes)
	assert.True(t, nt.Expiry.Equal(fakeNow.Add(6*time.Hour)), "expires_in when expires_at is absent")
	form := f.Calls("/oauth/token")[0].Form
	assert.Equal(t, "refresh_token", form.Get("grant_type"))
	assert.Equal(t, "r", form.Get("refresh_token"))
}

func TestRefresh_RejectedGrantNeedsReauth(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"401":                  {http.StatusUnauthorized, `{"message":"Authorization Error"}`},
		"invalid refresh":      {http.StatusBadRequest, `{"message":"Bad Request","errors":[{"resource":"RefreshToken","field":"refresh_token","code":"invalid"}]}`},
		"invalid_grant string": {http.StatusBadRequest, `invalid_grant`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeStrava(t)
			f.TokenStatus, f.TokenBody = tc.status, tc.body
			_, err := f.connector(0).Refresh(context.Background(), connector.Tokens{Refresh: "r"})
			assert.ErrorIs(t, err, connector.ErrNeedsReauth)
		})
	}
	_, err := New(Options{ClientID: "a", ClientSecret: "b"}).Refresh(context.Background(), connector.Tokens{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth, "no refresh token")
}

// A bad client id is the operator's mistake: no user must be sent to reconnect.
func TestRefresh_BadClientIsNotReauth(t *testing.T) {
	f := newFakeStrava(t)
	f.TokenStatus = http.StatusBadRequest
	f.TokenBody = `{"message":"Bad Request","errors":[{"resource":"Application","field":"client_id","code":"invalid"}]}`
	_, err := f.connector(0).Refresh(context.Background(), connector.Tokens{Refresh: "r"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
	assert.NotContains(t, err.Error(), "test-client-secret")
}

func TestRefresh_RateLimited(t *testing.T) {
	f := newFakeStrava(t)
	f.RateLimit = func(p string) bool { return p == "/oauth/token" }
	f.RateLimitHeaders = http.Header{"Retry-After": {"30"}}
	_, err := f.connector(0).Refresh(context.Background(), connector.Tokens{Refresh: "r"})
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok)
	assert.Equal(t, 30*time.Second, rl.RetryAfter)
}

func TestRevoke(t *testing.T) {
	f := newFakeStrava(t)
	c := f.connector(0)
	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{Access: "a", Refresh: "r"}))
	calls := f.Calls("/oauth/revoke")
	require.Len(t, calls, 1)
	assert.Equal(t, "r", calls[0].Form.Get("token"))
	assert.Equal(t, "refresh_token", calls[0].Form.Get("token_type_hint"))
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", calls[0].Auth)
	id, secret, ok := req.BasicAuth()
	require.True(t, ok)
	assert.Equal(t, "test-client-id", id)
	assert.Equal(t, "test-client-secret", secret)

	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{}), "nothing to revoke")
	f.RevokeStatus = http.StatusServiceUnavailable
	assert.Error(t, c.Revoke(context.Background(), connector.Tokens{Access: "a"}))
}
