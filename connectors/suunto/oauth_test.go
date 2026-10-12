// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/suunto/suuntofake"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestKeysAndEnabled(t *testing.T) {
	c := New(Options{})
	assert.Equal(t, []string{"CLIENT_ID", "CLIENT_SECRET", "SUBSCRIPTION_KEY"}, c.RequiredKeys())
	assert.False(t, c.Enabled(connector.Config{"CLIENT_ID": "a", "CLIENT_SECRET": "b"}))
	assert.True(t, c.Enabled(connector.Config{"CLIENT_ID": "a", "CLIENT_SECRET": "b", "SUBSCRIPTION_KEY": "k"}))

	reg := connector.NewRegistry()
	reg.Register(c)
	assert.Empty(t, reg.Enabled(map[string]config.ProviderKeys{"suunto": {"CLIENT_ID": "a", "CLIENT_SECRET": "b"}}))
	assert.Len(t, reg.Enabled(map[string]config.ProviderKeys{"suunto": {"CLIENT_ID": "a", "CLIENT_SECRET": "b", "SUBSCRIPTION_KEY": "k"}}), 1,
		"NOTIFICATION_SECRET is optional")
}

func TestRegistered(t *testing.T) {
	c, ok := connector.Get(Slug)
	require.True(t, ok)
	_, oauth := connector.AsOAuth(c)
	_, push := connector.AsPush(c)
	_, revoker := c.(connector.Revoker)
	_, notified := c.(connector.NotifiedFetcher)
	_, subscriber := c.(connector.Subscriber)
	_, pkce := c.(connector.StateExchanger)
	assert.True(t, oauth && push && revoker && notified)
	assert.False(t, subscriber, "Suunto notifies one URL per application, set in API Zone")
	assert.False(t, pkce)
}

// Keys come from Configure (or Options), never from the environment.
func TestConfigure(t *testing.T) {
	t.Setenv("OHS_SUUNTO_CLIENT_ID", "env-id")
	c := New(Options{})
	id, _ := c.credentials()
	assert.Empty(t, id)
	_, err := c.AuthorizeURL("s", "r")
	assert.ErrorIs(t, err, errNotConfigured)

	assert.Error(t, c.Configure(connector.Config{"CLIENT_ID": "i", "CLIENT_SECRET": "s"}), "the subscription key is required")
	require.NoError(t, c.Configure(connector.Config{"CLIENT_ID": "i", "CLIENT_SECRET": "s", "SUBSCRIPTION_KEY": "k"}))
	assert.Empty(t, c.notificationSecret())
	require.NoError(t, c.Configure(connector.Config{"CLIENT_ID": "i2", "CLIENT_SECRET": "s2", "SUBSCRIPTION_KEY": "k2", "NOTIFICATION_SECRET": "n"}))
	id, secret := c.credentials()
	assert.Equal(t, "i2", id)
	assert.Equal(t, "s2", secret)
	assert.Equal(t, "k2", c.subscriptionKey())
	assert.Equal(t, "n", c.notificationSecret())
}

func TestBaseURLDefaults(t *testing.T) {
	c := New(Options{})
	assert.Equal(t, "https://cloudapi.suunto.com", c.apiBase)
	assert.Equal(t, "https://cloudapi-oauth.suunto.com", c.oauthBase)
	assert.Equal(t, perPage, c.perPage)
}

func TestAuthorizeURL(t *testing.T) {
	f := newFakeSuunto(t)
	raw, err := f.connector(0).AuthorizeURL("state-123", "https://ohs.example.test/oauth/suunto/callback")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "/oauth/authorize", u.Path)
	q := u.Query()
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, suuntofake.ClientID, q.Get("client_id"))
	assert.Equal(t, "state-123", q.Get("state"))
	assert.Equal(t, "https://ohs.example.test/oauth/suunto/callback", q.Get("redirect_uri"))
	assert.False(t, q.Has("scope"), "Suunto grants its default scope")
}

func TestExchange(t *testing.T) {
	f := newFakeSuunto(t)
	c := f.connector(0)
	tok, err := c.Exchange(context.Background(), "the-code", "https://ohs.example.test/oauth/suunto/callback")
	require.NoError(t, err)
	assert.Equal(t, suuntofake.Username, tok.ProviderUserID, "the JWT user claim")
	assert.Equal(t, suuntofake.Token(suuntofake.Username), tok.Access)
	assert.Equal(t, suuntofake.RefreshToken, tok.Refresh)
	assert.Equal(t, fakeNow.Add(86399*time.Second), tok.Expiry)
	assert.Equal(t, []string{"workout"}, tok.Scopes)

	calls := f.Calls("/oauth/token")
	require.Len(t, calls, 1)
	r := calls[0]
	assert.Equal(t, http.MethodPost, r.Method)
	assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte(suuntofake.ClientID+":"+suuntofake.ClientSecret)), r.Auth)
	assert.Equal(t, "authorization_code", r.Form.Get("grant_type"))
	assert.Equal(t, "the-code", r.Form.Get("code"))
	assert.Equal(t, "https://ohs.example.test/oauth/suunto/callback", r.Form.Get("redirect_uri"))
	assert.Empty(t, r.Form.Get("client_secret"), "the secret travels in Basic auth only")
}

func TestExchangeUserFallbackAndMissing(t *testing.T) {
	f := newFakeSuunto(t)
	f.TokenBody = `{"access_token":"opaque-not-a-jwt","refresh_token":"r","expires_in":86400,"user":"body-user"}`
	tok, err := f.connector(0).Exchange(context.Background(), "c", "r")
	require.NoError(t, err)
	assert.Equal(t, "body-user", tok.ProviderUserID)

	g := newFakeSuunto(t)
	g.TokenBody = `{"access_token":"` + suuntofake.JWT(map[string]any{"sub": "x"}) + `","expires_in":86400}`
	_, err = g.connector(0).Exchange(context.Background(), "c", "r")
	assert.ErrorContains(t, err, "names no user")
}

func TestJWTUser(t *testing.T) {
	assert.Equal(t, "jane", jwtUser(suuntofake.Token("jane")))
	assert.Equal(t, "12345", jwtUser(suuntofake.JWT(map[string]any{"user": 12345})))
	assert.Empty(t, jwtUser(suuntofake.JWT(map[string]any{"user": map[string]any{"a": 1}})))
	assert.Empty(t, jwtUser("not-a-jwt"))
	assert.Empty(t, jwtUser("a.!!!.c"))
	assert.Empty(t, jwtUser("a."+base64.RawURLEncoding.EncodeToString([]byte("not json"))+".c"))
	// A padded payload segment is tolerated.
	padded := "h." + base64.URLEncoding.EncodeToString([]byte(`{"user":"padded"}`)) + ".s"
	assert.Equal(t, "padded", jwtUser(padded))
}

func TestRefresh(t *testing.T) {
	f := newFakeSuunto(t)
	c := f.connector(0)
	nt, err := c.Refresh(context.Background(), connector.Tokens{Refresh: suuntofake.RefreshToken, ProviderUserID: "kept"})
	require.NoError(t, err)
	assert.Equal(t, suuntofake.Username, nt.ProviderUserID)
	calls := f.Calls("/oauth/token")
	require.Len(t, calls, 1)
	assert.Equal(t, "refresh_token", calls[0].Form.Get("grant_type"))
	assert.Equal(t, suuntofake.RefreshToken, calls[0].Form.Get("refresh_token"))

	_, err = c.Refresh(context.Background(), connector.Tokens{Refresh: "revoked"})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth, "400 invalid_grant")

	_, err = c.Refresh(context.Background(), connector.Tokens{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestRefreshKeepsOldValues(t *testing.T) {
	f := newFakeSuunto(t)
	f.TokenBody = `{"access_token":"opaque","expires_in":86400}`
	nt, err := f.connector(0).Refresh(context.Background(), connector.Tokens{Refresh: "old", ProviderUserID: "jane", Scopes: []string{"workout"}})
	require.NoError(t, err)
	assert.Equal(t, "old", nt.Refresh)
	assert.Equal(t, "jane", nt.ProviderUserID)
	assert.Equal(t, []string{"workout"}, nt.Scopes)
}

func TestTokenErrors(t *testing.T) {
	cases := []struct {
		status int
		body   string
		reauth bool
		rate   bool
	}{
		{http.StatusUnauthorized, `{"error":"unauthorized"}`, true, false},
		{http.StatusBadRequest, `{"error":"invalid_grant"}`, true, false},
		{http.StatusBadRequest, `{"error":"invalid_request"}`, false, false},
		{http.StatusTooManyRequests, ``, false, true},
		{http.StatusInternalServerError, ``, false, false},
	}
	for _, tc := range cases {
		f := newFakeSuunto(t)
		f.TokenStatus, f.TokenBody = tc.status, tc.body
		_, err := f.connector(0).Refresh(context.Background(), connector.Tokens{Refresh: "r"})
		require.Error(t, err)
		assert.Equal(t, tc.reauth, err != nil && isReauth(err), "status %d body %s", tc.status, tc.body)
		_, rl := connector.AsRateLimit(err)
		assert.Equal(t, tc.rate, rl, "status %d", tc.status)
	}
}

func isReauth(err error) bool { return errors.Is(err, connector.ErrNeedsReauth) }

func TestRevoke(t *testing.T) {
	f := newFakeSuunto(t)
	c := f.connector(0)
	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{Access: "acc"}))
	calls := f.Calls("/oauth/deauthorize")
	require.Len(t, calls, 1)
	assert.Equal(t, http.MethodGet, calls[0].Method)
	assert.Equal(t, "Bearer acc", calls[0].Auth)
	assert.Equal(t, suuntofake.ClientID, calls[0].Query.Get("client_id"))

	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{}), "nothing to revoke")
	assert.Len(t, f.Calls("/oauth/deauthorize"), 1)

	f.DeauthStatus = http.StatusUnauthorized
	assert.NoError(t, c.Revoke(context.Background(), connector.Tokens{Access: "gone"}), "already revoked")
	f.DeauthStatus = http.StatusInternalServerError
	assert.Error(t, c.Revoke(context.Background(), connector.Tokens{Access: "acc"}))
}
