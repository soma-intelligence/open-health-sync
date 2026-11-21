// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/wahoo/wahoofake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestAuthorizeURL(t *testing.T) {
	f := wahoofake.New(t)
	u, err := testConnector(f, 0).AuthorizeURL("st-1", "https://ohs.example.test/oauth/wahoo/callback")
	require.NoError(t, err)
	p, err := url.Parse(u)
	require.NoError(t, err)
	assert.Equal(t, f.URL()+"/oauth/authorize", p.Scheme+"://"+p.Host+p.Path)
	q := p.Query()
	assert.Equal(t, "test-client-id", q.Get("client_id"))
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "user_read workouts_read offline_data", q.Get("scope"))
	assert.Equal(t, "st-1", q.Get("state"))
	assert.Equal(t, "https://ohs.example.test/oauth/wahoo/callback", q.Get("redirect_uri"))

	_, err = New(Options{}).AuthorizeURL("st", "https://x.test/cb")
	assert.ErrorIs(t, err, errNotConfigured)
}

func TestExchange_ReadsTheUserID(t *testing.T) {
	f := wahoofake.New(t)
	tok, err := testConnector(f, 0).Exchange(context.Background(), "code-1", "https://ohs.example.test/oauth/wahoo/callback")
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-0001", tok.Access)
	assert.Equal(t, "fake-refresh-token-0001", tok.Refresh)
	assert.Equal(t, "60462", tok.ProviderUserID)
	assert.True(t, tok.Expiry.Equal(fakeNow.Add(2*time.Hour)))
	assert.Equal(t, []string{"user_read", "workouts_read", "offline_data"}, tok.Scopes)

	calls := f.Calls("/oauth/token")
	require.Len(t, calls, 1)
	form := calls[0].Form
	assert.Equal(t, "authorization_code", form.Get("grant_type"))
	assert.Equal(t, "code-1", form.Get("code"))
	assert.Equal(t, "test-client-id", form.Get("client_id"))
	assert.Equal(t, "test-client-secret", form.Get("client_secret"))
	assert.Equal(t, "https://ohs.example.test/oauth/wahoo/callback", form.Get("redirect_uri"))
	require.Len(t, f.Calls("/v1/user"), 1)
	assert.Equal(t, "Bearer fake-access-token-0001", f.Calls("/v1/user")[0].Auth)
}

func TestRefresh_RotatesThePair(t *testing.T) {
	f := wahoofake.New(t)
	f.Rotate = true
	old := connector.Tokens{Access: "a0", Refresh: "r0", ProviderUserID: "60462", Scopes: []string{"user_read"}}
	nt, err := testConnector(f, 0).Refresh(context.Background(), old)
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-r1", nt.Access)
	assert.Equal(t, "fake-refresh-token-r1", nt.Refresh)
	assert.Equal(t, "60462", nt.ProviderUserID)
	form := f.Calls("/oauth/token")[0].Form
	assert.Equal(t, "refresh_token", form.Get("grant_type"))
	assert.Equal(t, "r0", form.Get("refresh_token"))
}

func TestRefresh_RejectedGrantNeedsReauth(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusUnauthorized, `{"error":"invalid_token"}`},
		{http.StatusBadRequest, `{"error":"invalid_grant","error_description":"The provided authorization grant is invalid"}`},
	} {
		f := wahoofake.New(t)
		f.TokenStatus, f.TokenBody = tc.status, tc.body
		_, err := testConnector(f, 0).Refresh(context.Background(), connector.Tokens{Refresh: "r0"})
		assert.ErrorIs(t, err, connector.ErrNeedsReauth, tc.status)
	}
	_, err := New(Options{ClientID: "a", ClientSecret: "b"}).Refresh(context.Background(), connector.Tokens{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth, "no refresh token")
}

func TestRefresh_RateLimitedUsesTheResetHeader(t *testing.T) {
	f := wahoofake.New(t)
	f.RateLimit = func(string) bool { return true }
	f.RateLimitHeaders = http.Header{"X-Ratelimit-Reset": {"120"}}
	_, err := testConnector(f, 0).Refresh(context.Background(), connector.Tokens{Refresh: "r0"})
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok, err)
	assert.Equal(t, 2*time.Minute, rl.RetryAfter)
}

func TestRevoke(t *testing.T) {
	f := wahoofake.New(t)
	require.NoError(t, testConnector(f, 0).Revoke(context.Background(), connector.Tokens{Access: "a1"}))
	calls := f.Calls("/v1/permissions")
	require.Len(t, calls, 1)
	assert.Equal(t, http.MethodDelete, calls[0].Method)
	assert.Equal(t, "Bearer a1", calls[0].Auth)

	f.DataHTTP = http.StatusUnauthorized
	assert.NoError(t, testConnector(f, 0).Revoke(context.Background(), connector.Tokens{Access: "gone"}), "an unknown token counts as revoked")
	assert.NoError(t, testConnector(f, 0).Revoke(context.Background(), connector.Tokens{}))
}

func TestConfigureAndInstance(t *testing.T) {
	c := New(Options{})
	assert.False(t, c.Enabled(connector.Config{}))
	assert.Error(t, c.Configure(connector.Config{KeyClientID: "id"}))
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "id", KeyClientSecret: "s", KeyWebhookToken: "w"}))
	assert.Equal(t, "w", c.webhookToken())
	inst := c.NewInstance().(*Connector)
	id, _ := inst.credentials()
	assert.Empty(t, id, "a new instance has no keys")
	assert.Equal(t, c.httpc, inst.httpc)
	assert.False(t, errors.Is(nil, errNotConfigured))
}
