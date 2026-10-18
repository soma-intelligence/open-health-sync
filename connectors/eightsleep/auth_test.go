// SPDX-License-Identifier: AGPL-3.0-only

package eightsleep

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/eightsleep/eightsleepfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestSignIn(t *testing.T) {
	f := newFake(t)
	c := newConnector(f)
	tok, err := c.SignIn(context.Background(), eightsleepfake.Email, eightsleepfake.Password)
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-1", tok.Access)
	assert.Equal(t, eightsleepfake.UserID, tok.ProviderUserID)
	assert.Equal(t, fakeNow.Add(72000*time.Second), tok.Expiry)
	assert.True(t, strings.HasPrefix(tok.Refresh, credentialPrefix), "the credentials ride in the sealed refresh token")

	reqs := f.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, map[string]string{
		"client_id": testClientID, "client_secret": testClientSecret, "grant_type": "password",
		"username": eightsleepfake.Email, "password": eightsleepfake.Password,
	}, reqs[0].Body)
	assert.Empty(t, reqs[0].Auth)
}

func TestSignInBadPassword(t *testing.T) {
	c := newConnector(newFake(t))
	_, err := c.SignIn(context.Background(), eightsleepfake.Email, "wrong")
	require.ErrorIs(t, err, connector.ErrBadCredentials)
	assert.NotContains(t, err.Error(), "wrong", "the password never lands in an error")
	assert.NotContains(t, err.Error(), eightsleepfake.Email)
}

func TestSignInStatuses(t *testing.T) {
	for _, tc := range []struct {
		status int
		check  func(t *testing.T, err error)
	}{
		{http.StatusUnauthorized, func(t *testing.T, err error) { require.ErrorIs(t, err, connector.ErrBadCredentials) }},
		{http.StatusForbidden, func(t *testing.T, err error) { require.ErrorIs(t, err, connector.ErrBadCredentials) }},
		{http.StatusTooManyRequests, func(t *testing.T, err error) {
			rl, ok := connector.AsRateLimit(err)
			require.True(t, ok)
			assert.Equal(t, 90*time.Second, rl.RetryAfter)
		}},
		{http.StatusBadGateway, func(t *testing.T, err error) {
			require.Error(t, err)
			assert.False(t, errors.Is(err, connector.ErrBadCredentials))
		}},
	} {
		f := newFake(t)
		f.TokenStatus, f.RetryAfter = tc.status, "90"
		_, err := newConnector(f).SignIn(context.Background(), eightsleepfake.Email, eightsleepfake.Password)
		tc.check(t, err)
	}
}

func TestSignInDefaultKeys(t *testing.T) {
	f := eightsleepfake.New(t)
	f.ClientID, f.ClientSecret = DefaultClientID, DefaultClientSecret
	_, err := newConnector(f, func(o *Options) { o.ClientID, o.ClientSecret = "", "" }).
		SignIn(context.Background(), eightsleepfake.Email, eightsleepfake.Password)
	require.NoError(t, err)
}

func TestRefreshSignsInAgain(t *testing.T) {
	f := newFake(t)
	c := newConnector(f)
	first, err := c.SignIn(context.Background(), eightsleepfake.Email, eightsleepfake.Password)
	require.NoError(t, err)
	again, err := c.Refresh(context.Background(), first)
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-1", again.Access)
	assert.Equal(t, first.Refresh, again.Refresh)
	assert.Equal(t, 2, f.SignIns())
}

func TestRefreshPasswordChanged(t *testing.T) {
	f := newFake(t)
	c := newConnector(f)
	tok, err := c.SignIn(context.Background(), eightsleepfake.Email, eightsleepfake.Password)
	require.NoError(t, err)
	f.Password = "a new password"
	_, err = c.Refresh(context.Background(), tok)
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestRefreshWithoutCredentials(t *testing.T) {
	c := newConnector(newFake(t))
	for _, r := range []string{"", "some-refresh-token", credentialPrefix + "{}", credentialPrefix + "not json"} {
		_, err := c.Refresh(context.Background(), connector.Tokens{Refresh: r})
		require.ErrorIs(t, err, connector.ErrNeedsReauth, r)
	}
}

func TestNoConsentPage(t *testing.T) {
	c := newConnector(newFake(t))
	_, err := c.AuthorizeURL("state", "https://ohs.example.org/oauth/eight_sleep/callback")
	require.ErrorIs(t, err, ErrNoConsentPage)
	_, err = c.Exchange(context.Background(), "code", "https://ohs.example.org/oauth/eight_sleep/callback")
	require.ErrorIs(t, err, ErrNoConsentPage)
	_, ok := connector.AsPassword(c)
	assert.True(t, ok)
}

func TestKeys(t *testing.T) {
	c := New(Options{})
	assert.Empty(t, c.RequiredKeys())
	assert.Equal(t, []string{KeyClientID, KeyClientSecret}, c.OptionalKeys())
	id, secret := c.clientCredentials()
	assert.Equal(t, DefaultClientID, id, "no keys: the published defaults")
	assert.Equal(t, DefaultClientSecret, secret)

	assert.True(t, c.Enabled(nil))
	assert.False(t, c.Enabled(connector.Config{KeyClientID: "x"}))
	assert.False(t, c.Enabled(connector.Config{KeyClientSecret: "y"}))
	assert.True(t, c.Enabled(connector.Config{KeyClientID: "x", KeyClientSecret: "y"}))
	require.Error(t, c.Configure(connector.Config{KeyClientID: "x"}))
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "x", KeyClientSecret: "y"}))
	id, secret = c.clientCredentials()
	assert.Equal(t, "x", id)
	assert.Equal(t, "y", secret)
	require.NoError(t, c.Configure(connector.Config{}))
	id, _ = c.clientCredentials()
	assert.Equal(t, DefaultClientID, id, "keys removed: back to the defaults")
	assert.False(t, connector.SecretKey(KeyClientID))
	assert.True(t, connector.SecretKey(KeyClientSecret))
}
