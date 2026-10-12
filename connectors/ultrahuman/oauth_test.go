// SPDX-License-Identifier: AGPL-3.0-only

package ultrahuman

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestKeysAndRegistration(t *testing.T) {
	c := New(Options{})
	require.Equal(t, "ultrahuman", c.Slug())
	require.Equal(t, []string{"CLIENT_ID", "CLIENT_SECRET"}, c.RequiredKeys())
	require.False(t, c.Enabled(connector.Config{KeyClientID: "a"}))
	require.True(t, c.Enabled(connector.Config{KeyClientID: "a", KeyClientSecret: "b"}))
	require.Error(t, c.Configure(connector.Config{KeyClientID: "a"}))
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "a", KeyClientSecret: "b"}))

	got, ok := connector.Get(Slug)
	require.True(t, ok, "init registers the connector")
	_, isOAuth := connector.AsOAuth(got)
	require.True(t, isOAuth)
	_, isPush := connector.AsPush(got)
	require.False(t, isPush, "Ultrahuman documents no webhooks")
}

func TestAuthorizeURL(t *testing.T) {
	_, err := New(Options{}).AuthorizeURL("s", "https://ohs.example.org/oauth/ultrahuman/callback")
	require.ErrorIs(t, err, errNotConfigured)

	c := New(Options{ClientID: testClientID, ClientSecret: testClientSecret})
	raw, err := c.AuthorizeURL("state-1", "https://ohs.example.org/oauth/ultrahuman/callback")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	require.Equal(t, "auth.ultrahuman.com", u.Host)
	require.Equal(t, "/authorise", u.Path)
	q := u.Query()
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, testClientID, q.Get("client_id"))
	require.Equal(t, "ring_data cgm_data profile", q.Get("scope"))
	require.Equal(t, "state-1", q.Get("state"))
	require.Equal(t, "https://ohs.example.org/oauth/ultrahuman/callback", q.Get("redirect_uri"))
	require.NotContains(t, raw, testClientSecret)
}

func TestExchange(t *testing.T) {
	f := newFake(t)
	c := f.connector()
	tok, err := c.Exchange(context.Background(), "code-1", "https://ohs.example.org/oauth/ultrahuman/callback")
	require.NoError(t, err)
	require.Equal(t, "fake-access-token-1", tok.Access)
	require.Equal(t, "fake-refresh-token-1", tok.Refresh)
	require.Equal(t, fakeNow.Add(86399*time.Second), tok.Expiry)
	require.ElementsMatch(t, []string{"profile", "ring_data", "cgm_data"}, tok.Scopes)
	require.Equal(t, "uh-user-42", tok.ProviderUserID)

	calls := f.Calls("/api/partners/oauth/token")
	require.Len(t, calls, 1)
	form := calls[0].Form
	require.Equal(t, "authorization_code", form.Get("grant_type"))
	require.Equal(t, "code-1", form.Get("code"))
	require.Equal(t, testClientID, form.Get("client_id"))
	require.Equal(t, testClientSecret, form.Get("client_secret"))
	require.Equal(t, "https://ohs.example.org/oauth/ultrahuman/callback", form.Get("redirect_uri"))

	info := f.Calls("/api/partners/v1/user_data/user_info")
	require.Len(t, info, 1)
	require.Equal(t, "Bearer fake-access-token-1", info[0].Auth)
}

func TestExchange_UserInfoFailureStillConnects(t *testing.T) {
	f := newFake(t)
	f.UserInfoStatus = http.StatusInternalServerError
	tok, err := f.connector().Exchange(context.Background(), "code-1", "https://r")
	require.NoError(t, err)
	require.Equal(t, "fake-access-token-1", tok.Access)
	require.Empty(t, tok.ProviderUserID)
}

func TestExchange_Errors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(t *testing.T, err error)
	}{
		{"invalid_grant", 400, `{"error":"invalid_grant","error_description":"Invalid authorization code."}`, func(t *testing.T, err error) {
			require.ErrorIs(t, err, connector.ErrNeedsReauth)
		}},
		{"invalid_client", 401, `{"error":"invalid_client"}`, func(t *testing.T, err error) {
			require.ErrorIs(t, err, ErrClientRejected)
			require.NotErrorIs(t, err, connector.ErrNeedsReauth)
		}},
		{"unauthorized_client", 400, `{"error":"unauthorized_client"}`, func(t *testing.T, err error) {
			require.ErrorIs(t, err, ErrClientRejected)
		}},
		{"rate limited", 429, ``, func(t *testing.T, err error) {
			var rl *connector.RateLimitError
			require.True(t, errors.As(err, &rl))
		}},
		{"server error", 502, `oops`, func(t *testing.T, err error) {
			require.ErrorContains(t, err, "HTTP 502")
			require.NotErrorIs(t, err, connector.ErrNeedsReauth)
		}},
		{"no access token", 200, `{"token_type":"Bearer"}`, func(t *testing.T, err error) {
			require.ErrorContains(t, err, "no access token")
		}},
		{"not json", 200, `<html>`, func(t *testing.T, err error) {
			require.ErrorContains(t, err, "not JSON")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.TokenStatus, f.TokenBody = tc.status, tc.body
			_, err := f.connector().Exchange(context.Background(), "code-1", "https://r")
			require.Error(t, err)
			tc.check(t, err)
		})
	}
}

func TestExchange_NotConfigured(t *testing.T) {
	_, err := New(Options{}).Exchange(context.Background(), "c", "r")
	require.ErrorIs(t, err, errNotConfigured)
}

func TestRefresh(t *testing.T) {
	f := newFake(t)
	c := f.connector()
	old := connector.Tokens{Access: "a1", Refresh: "fake-refresh-token-1", ProviderUserID: "uh-user-42"}
	nt, err := c.Refresh(context.Background(), old)
	require.NoError(t, err)
	require.Equal(t, "fake-access-token-2", nt.Access)
	require.Equal(t, "fake-refresh-token-2", nt.Refresh, "the newly issued refresh token is kept")
	require.Equal(t, "uh-user-42", nt.ProviderUserID)
	require.Equal(t, fakeNow.Add(86400*time.Second), nt.Expiry)
	form := f.Calls("/api/partners/oauth/token")[0].Form
	require.Equal(t, "refresh_token", form.Get("grant_type"))
	require.Equal(t, "fake-refresh-token-1", form.Get("refresh_token"))
	require.Equal(t, testClientSecret, form.Get("client_secret"))
}

func TestRefresh_KeepsOldRefreshTokenWhenNoneIssued(t *testing.T) {
	f := newFake(t)
	f.TokenBody = `{"access_token":"a2","expires_in":86400}`
	nt, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "r1", Scopes: []string{"ring_data"}})
	require.NoError(t, err)
	require.Equal(t, "r1", nt.Refresh)
	require.Equal(t, []string{"ring_data"}, nt.Scopes)
}

func TestRefresh_Refused(t *testing.T) {
	f := newFake(t)
	_, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "spent"})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)

	_, err = f.connector().Refresh(context.Background(), connector.Tokens{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestRevoke(t *testing.T) {
	f := newFake(t)
	require.NoError(t, f.connector().Revoke(context.Background(), connector.Tokens{Access: "a1", Refresh: "r1"}))
	calls := f.Calls("/api/partners/oauth/revoke")
	require.Len(t, calls, 2)
	require.Equal(t, "r1", calls[0].Form.Get("token"))
	require.Equal(t, "a1", calls[1].Form.Get("token"))
	require.Equal(t, testClientID, calls[0].Form.Get("client_id"))

	f2 := newFake(t)
	f2.ClientSecret = "other"
	err := f2.connector().Revoke(context.Background(), connector.Tokens{Access: "a1"})
	require.ErrorContains(t, err, "HTTP 400")

	require.Error(t, New(Options{ClientID: "a", ClientSecret: "b"}).Revoke(context.Background(), connector.Tokens{}))
}

func TestTokensNeverInErrors(t *testing.T) {
	f := newFake(t)
	f.TokenStatus, f.TokenBody = 500, `{"error":"server_error","access_token":"leak"}`
	_, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "secret-refresh"})
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), "secret-refresh") || strings.Contains(err.Error(), "leak"))
}
