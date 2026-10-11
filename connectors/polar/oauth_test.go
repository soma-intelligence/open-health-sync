// SPDX-License-Identifier: AGPL-3.0-only

package polar

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

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestConfigureAndKeys(t *testing.T) {
	c := New(Options{})
	assert.Equal(t, []string{KeyClientID, KeyClientSecret}, c.RequiredKeys())
	assert.False(t, c.Enabled(connector.Config{KeyClientID: "a"}))
	assert.True(t, c.Enabled(connector.Config{KeyClientID: "a", KeyClientSecret: "b"}))
	require.Error(t, c.Configure(connector.Config{KeyClientID: "a"}))
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "a", KeyClientSecret: "b", KeyWebhookSecret: "w"}))
	id, secret := c.credentials()
	assert.Equal(t, "a", id)
	assert.Equal(t, "b", secret)
	assert.Equal(t, "w", c.webhookSecret())
	assert.Equal(t, Slug, c.Slug())
}

func TestRegistered(t *testing.T) {
	c, ok := connector.Get(Slug)
	require.True(t, ok)
	_, ok = connector.AsOAuth(c)
	assert.True(t, ok)
	_, ok = connector.AsPush(c)
	assert.True(t, ok)
	_, ok = c.(connector.Revoker)
	assert.True(t, ok)
	_, ok = c.(connector.Subscriber)
	assert.False(t, ok, "webhooks are per client, not per user")
}

func TestAuthorizeURL(t *testing.T) {
	c := New(Options{ClientID: testClientID, ClientSecret: testClientSecret})
	raw, err := c.AuthorizeURL("state-1", "https://ohs.example.org/oauth/polar/callback")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "flow.polar.com", u.Host)
	assert.Equal(t, "/oauth2/authorization", u.Path)
	q := u.Query()
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, testClientID, q.Get("client_id"))
	assert.Equal(t, "accesslink.read_all", q.Get("scope"))
	assert.Equal(t, "state-1", q.Get("state"))
	assert.Equal(t, "https://ohs.example.org/oauth/polar/callback", q.Get("redirect_uri"))

	_, err = New(Options{}).AuthorizeURL("s", "r")
	assert.ErrorIs(t, err, errNotConfigured)
}

func TestExchange_RegistersUser(t *testing.T) {
	f := newFakePolar(t)
	c := f.connector()
	tok, err := c.Exchange(context.Background(), "code-1", "https://ohs.example.org/oauth/polar/callback")
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-1", tok.Access)
	assert.Empty(t, tok.Refresh)
	assert.Equal(t, testPolarUser, tok.ProviderUserID)
	assert.Equal(t, []string{"accesslink.read_all"}, tok.Scopes)
	assert.Equal(t, fakeNow.Add(461375999*time.Second), tok.Expiry)

	calls := f.Calls(http.MethodPost, "/v2/oauth2/token")
	require.Len(t, calls, 1)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(testClientID+":"+testClientSecret))
	assert.Equal(t, want, calls[0].Auth)
	form, _ := url.ParseQuery(calls[0].Body)
	assert.Equal(t, "authorization_code", form.Get("grant_type"))
	assert.Equal(t, "code-1", form.Get("code"))
	assert.Equal(t, "https://ohs.example.org/oauth/polar/callback", form.Get("redirect_uri"))
	assert.Empty(t, form.Get("client_secret"), "the secret goes in the Basic header only")

	reg := f.Calls(http.MethodPost, "/v3/users")
	require.Len(t, reg, 1)
	assert.Equal(t, "Bearer fake-access-token-1", reg[0].Auth)
	assert.JSONEq(t, `{"member-id":"ohs-10101010"}`, reg[0].Body)
}

func TestExchange_AlreadyRegistered409IsFine(t *testing.T) {
	f := newFakePolar(t)
	c := f.connector()
	_, err := c.Exchange(context.Background(), "code-1", "")
	require.NoError(t, err)
	_, err = c.Exchange(context.Background(), "code-2", "")
	require.NoError(t, err, "a second registration answers 409")
	assert.Len(t, f.Calls(http.MethodPost, "/v3/users"), 2)

	f2 := newFakePolar(t)
	f2.RegisterStatus = http.StatusConflict
	_, err = f2.connector().Exchange(context.Background(), "code-1", "")
	require.NoError(t, err)
}

func TestExchange_RegistrationRefused(t *testing.T) {
	f := newFakePolar(t)
	f.RegisterStatus = http.StatusForbidden
	_, err := f.connector().Exchange(context.Background(), "code-1", "")
	require.ErrorIs(t, err, connector.ErrNeedsReauth)

	f = newFakePolar(t)
	f.RegisterStatus = http.StatusInternalServerError
	_, err = f.connector().Exchange(context.Background(), "code-1", "")
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestExchange_TokenErrors(t *testing.T) {
	f := newFakePolar(t)
	f.TokenStatus = http.StatusBadRequest
	f.TokenBody = `{"error":"invalid_grant"}`
	_, err := f.connector().Exchange(context.Background(), "code-1", "")
	require.ErrorIs(t, err, connector.ErrNeedsReauth)

	f = newFakePolar(t)
	f.RateLimit = func(_, p string) bool { return p == "/v2/oauth2/token" }
	f.RetryAfter = "30"
	_, err = f.connector().Exchange(context.Background(), "code-1", "")
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok)
	assert.Equal(t, 30*time.Second, rl.RetryAfter)

	f = newFakePolar(t)
	f.TokenBody = `{"access_token":"x","token_type":"bearer"}`
	_, err = f.connector().Exchange(context.Background(), "code-1", "")
	require.ErrorContains(t, err, "x_user_id")

	f = newFakePolar(t)
	f.ClientSecret = "another-secret"
	_, err = f.connector().Exchange(context.Background(), "code-1", "")
	require.Error(t, err)

	_, err = New(Options{}).Exchange(context.Background(), "code-1", "")
	require.ErrorIs(t, err, errNotConfigured)
}

func TestRefresh(t *testing.T) {
	f := newFakePolar(t)
	c := f.connector()
	in := connector.Tokens{Access: "fake-access-token-1", ProviderUserID: testPolarUser, Expiry: fakeNow.Add(time.Hour), Scopes: Scopes}
	out, err := c.Refresh(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, in, out, "Polar issues no refresh token: the same tokens come back")
	calls := f.Calls(http.MethodGet, "/v3/users/"+testPolarUser)
	require.Len(t, calls, 1)
	assert.Equal(t, "Bearer fake-access-token-1", calls[0].Auth)

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNoContent, http.StatusNotFound} {
		f := newFakePolar(t)
		f.UserStatus = status
		_, err := f.connector().Refresh(context.Background(), in)
		assert.ErrorIs(t, err, connector.ErrNeedsReauth, "status %d", status)
	}

	f = newFakePolar(t)
	f.UserStatus = http.StatusInternalServerError
	_, err = f.connector().Refresh(context.Background(), in)
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)

	expired := in
	expired.Expiry = fakeNow.Add(-time.Second)
	_, err = c.Refresh(context.Background(), expired)
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)

	_, err = c.Refresh(context.Background(), connector.Tokens{ProviderUserID: testPolarUser})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestRevoke(t *testing.T) {
	f := newFakePolar(t)
	c := f.connector()
	require.NoError(t, c.Revoke(context.Background(), testConn().Tokens))
	calls := f.Calls(http.MethodDelete, "/v3/users/"+testPolarUser)
	require.Len(t, calls, 1)
	assert.Equal(t, "Bearer fake-access-token-1", calls[0].Auth)
	require.Error(t, c.Revoke(context.Background(), connector.Tokens{}))
}

func TestAfterConnectRegisters(t *testing.T) {
	f := newFakePolar(t)
	c := f.connector()
	require.NoError(t, c.AfterConnect(context.Background(), testConn()))
	require.NoError(t, c.AfterConnect(context.Background(), testConn()), "409 on the second call")
	assert.Len(t, f.Calls(http.MethodPost, "/v3/users"), 2)
	err := c.AfterConnect(context.Background(), connector.Connection{})
	require.Error(t, err)
	assert.False(t, errors.Is(err, connector.ErrNeedsReauth))
}
