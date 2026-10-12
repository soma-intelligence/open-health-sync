// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/fitbit/fitbitfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestKeysAndConfigure(t *testing.T) {
	c := New(Options{})
	assert.Equal(t, "fitbit", c.Slug())
	assert.Equal(t, []string{"CLIENT_ID", "CLIENT_SECRET"}, c.RequiredKeys())
	assert.False(t, c.Enabled(connector.Config{KeyClientID: "x"}))
	assert.True(t, c.Enabled(connector.Config{KeyClientID: "x", KeyClientSecret: "y"}))
	require.Error(t, c.Configure(connector.Config{KeyClientID: "x"}))
	require.Error(t, c.Configure(connector.Config{KeyClientID: "x", KeyClientSecret: "y", KeyWebhookSignature: "maybe"}))
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "x", KeyClientSecret: "y", KeyWebhookSignature: "IF_AVAILABLE"}))
	_, mode := c.webhookKeys()
	assert.Equal(t, SignatureIfAvailable, mode)

	reg, ok := connector.Get(Slug)
	require.True(t, ok, "registered in init")
	_, push := connector.AsPush(reg)
	assert.True(t, push)
}

func TestAuthorizeURL(t *testing.T) {
	f := newFake(t)
	c := newConnector(f)
	raw, err := c.AuthorizeURL("st4te", "https://ohs.example.org/oauth/fitbit/callback")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "/auth", u.Path)
	q := u.Query()
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, testClientID, q.Get("client_id"))
	assert.Equal(t, "st4te", q.Get("state"))
	assert.Equal(t, "offline", q.Get("access_type"))
	assert.Equal(t, "consent", q.Get("prompt"))
	assert.Equal(t, "true", q.Get("include_granted_scopes"))
	assert.Equal(t, "https://ohs.example.org/oauth/fitbit/callback", q.Get("redirect_uri"))
	assert.Equal(t, []string{
		"https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly",
		"https://www.googleapis.com/auth/googlehealth.health_metrics_and_measurements.readonly",
		"https://www.googleapis.com/auth/googlehealth.sleep.readonly",
	}, strings.Fields(q.Get("scope")))

	_, err = New(Options{}).AuthorizeURL("s", "r")
	require.Error(t, err, "not configured")
}

func TestExchange(t *testing.T) {
	f := newFake(t)
	f.Scope = ScopeMetrics + " " + ScopeSleep + " openid"
	c := newConnector(f)
	tok, err := c.Exchange(context.Background(), "good-code", "https://ohs.example.org/oauth/fitbit/callback")
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-1", tok.Access)
	assert.Equal(t, "fake-refresh-token", tok.Refresh)
	assert.Equal(t, fitbitfake.HealthUserID, tok.ProviderUserID)
	assert.Equal(t, fakeNow.Add(3599e9), tok.Expiry)
	assert.Equal(t, []string{ScopeMetrics, ScopeSleep, "openid"}, tok.Scopes)

	calls := f.Calls("/token")
	require.Len(t, calls, 1)
	assert.Equal(t, "authorization_code", calls[0].Form.Get("grant_type"))
	assert.Equal(t, testClientSecret, calls[0].Form.Get("client_secret"))
	assert.Equal(t, "Bearer fake-access-token-1", f.Calls("/v4/users/me/identity")[0].Auth)
}

func TestExchangeAccountNotLinked(t *testing.T) {
	f := newFake(t)
	f.IdentityStatus = http.StatusBadRequest
	f.IdentityBody = `{"error":{"code":400,"message":"Account not linked","status":"FAILED_PRECONDITION",
		"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"ACCOUNT_NOT_LINKED"}]}}`
	c := newConnector(f)
	_, err := c.Exchange(context.Background(), "good-code", "r")
	require.ErrorIs(t, err, ErrAccountNotLinked)
	assert.Contains(t, err.Error(), "Fitbit app")
	revokes := f.Calls("/revoke")
	require.Len(t, revokes, 1, "the refused grant is revoked")
	assert.Equal(t, "fake-refresh-token", revokes[0].Form.Get("token"))
}

func TestExchangeIdentityFailureStillConnects(t *testing.T) {
	f := newFake(t)
	f.IdentityStatus = http.StatusInternalServerError
	c := newConnector(f)
	tok, err := c.Exchange(context.Background(), "good-code", "r")
	require.NoError(t, err)
	assert.Empty(t, tok.ProviderUserID)
}

func TestExchangeNoScopeGranted(t *testing.T) {
	f := newFake(t)
	f.Scope = "openid email"
	c := newConnector(f)
	_, err := c.Exchange(context.Background(), "good-code", "r")
	require.ErrorIs(t, err, ErrNoScopeGranted)
	assert.Len(t, f.Calls("/revoke"), 1)
}

func TestExchangeErrors(t *testing.T) {
	f := newFake(t)
	c := newConnector(f)
	_, err := c.Exchange(context.Background(), "bad-code", "r")
	require.ErrorIs(t, err, connector.ErrNeedsReauth)

	f.ClientSecret = "other"
	_, err = c.Exchange(context.Background(), "good-code", "r")
	require.ErrorIs(t, err, ErrClientRejected)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)

	f.TokenStatus, f.TokenBody = http.StatusTooManyRequests, ""
	_, err = c.Exchange(context.Background(), "good-code", "r")
	_, limited := connector.AsRateLimit(err)
	assert.True(t, limited)
}

func TestRefresh(t *testing.T) {
	f := newFake(t)
	f.Scope = ""
	c := newConnector(f)
	in := connector.Tokens{Refresh: "fake-refresh-token", Scopes: []string{ScopeSleep}, ProviderUserID: "kept"}
	tok, err := c.Refresh(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-2", tok.Access)
	assert.Equal(t, "fake-refresh-token", tok.Refresh, "Google keeps the refresh token")
	assert.Equal(t, []string{ScopeSleep}, tok.Scopes, "no scope in the answer keeps the stored ones")
	assert.Equal(t, "kept", tok.ProviderUserID)
	assert.Empty(t, f.Calls("/v4/users/me/identity"))

	// A connection without the user id gets it read.
	tok, err = c.Refresh(context.Background(), connector.Tokens{Refresh: "fake-refresh-token"})
	require.NoError(t, err)
	assert.Equal(t, fitbitfake.HealthUserID, tok.ProviderUserID)

	_, err = c.Refresh(context.Background(), connector.Tokens{Refresh: "expired"})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
	_, err = c.Refresh(context.Background(), connector.Tokens{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestRevoke(t *testing.T) {
	f := newFake(t)
	c := newConnector(f)
	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{Access: "a", Refresh: "r"}))
	assert.Equal(t, "r", f.Calls("/revoke")[0].Form.Get("token"))

	f.RevokeStatus = http.StatusBadRequest
	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{Access: "a"}), "an unknown token counts as revoked")
	f.RevokeStatus = http.StatusInternalServerError
	require.Error(t, c.Revoke(context.Background(), connector.Tokens{Access: "a"}))
	require.Error(t, c.Revoke(context.Background(), connector.Tokens{}))
}

func TestGrantedTypes(t *testing.T) {
	assert.Equal(t, AllDataTypes, grantedTypes(nil))
	assert.Equal(t, []string{DataTypeSleep}, grantedTypes([]string{ScopeSleep, "openid"}))
	got := grantedTypes([]string{ScopeActivity})
	assert.Contains(t, got, DataTypeSteps)
	assert.Contains(t, got, DataTypeExercise)
	assert.NotContains(t, got, DataTypeHeartRate)
	assert.Empty(t, grantedTypes([]string{"email"}))
}
