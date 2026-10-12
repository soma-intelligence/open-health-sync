// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/intervalsicu/icufake"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestAuthorizeURL(t *testing.T) {
	c, f, _ := newTestConnector(t, 0)
	raw, err := c.AuthorizeURL("st-1", "https://ohs.example.org/oauth/intervals_icu/callback")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, f.URL()+"/oauth/authorize", u.Scheme+"://"+u.Host+u.Path)
	q := u.Query()
	assert.Equal(t, "test-client-id", q.Get("client_id"))
	assert.Equal(t, "https://ohs.example.org/oauth/intervals_icu/callback", q.Get("redirect_uri"))
	assert.Equal(t, "ACTIVITY:READ,WELLNESS:READ", q.Get("scope"))
	assert.Equal(t, "st-1", q.Get("state"))
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Empty(t, q.Get("client_secret"))
}

func TestAuthorizeURLNotConfigured(t *testing.T) {
	_, err := New(Options{}).AuthorizeURL("s", "r")
	assert.ErrorIs(t, err, errNotConfigured)
}

func TestExchange(t *testing.T) {
	c, f, _ := newTestConnector(t, 0)
	tok, err := c.Exchange(context.Background(), "code-1", "https://r")
	require.NoError(t, err)
	assert.Equal(t, icufake.AccessToken, tok.Access)
	assert.Empty(t, tok.Refresh, "intervals.icu issues no refresh token")
	assert.True(t, tok.Expiry.IsZero(), "no stated expiry: the core never refreshes ahead")
	assert.Equal(t, "i900001", tok.ProviderUserID)
	assert.Equal(t, []string{"ACTIVITY:READ", "WELLNESS:READ"}, tok.Scopes)

	calls := f.Calls("/api/oauth/token")
	require.Len(t, calls, 1)
	assert.Equal(t, http.MethodPost, calls[0].Method)
	assert.Equal(t, "test-client-id", calls[0].Form.Get("client_id"))
	assert.Equal(t, "test-client-secret", calls[0].Form.Get("client_secret"))
	assert.Equal(t, "code-1", calls[0].Form.Get("code"))
}

func TestExchangeNumericAthleteIDAndExpiry(t *testing.T) {
	c, f, _ := newTestConnector(t, 0)
	f.TokenBody = `{"access_token":"a","athlete":{"id":2049151},"expires_in":3600,"refresh_token":"r","scope":"ACTIVITY:READ"}`
	tok, err := c.Exchange(context.Background(), "code", "")
	require.NoError(t, err)
	assert.Equal(t, "2049151", tok.ProviderUserID)
	assert.Equal(t, "r", tok.Refresh)
	assert.Equal(t, fakeNow.Add(time.Hour), tok.Expiry)
}

func TestExchangeErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		check  func(t *testing.T, err error)
	}{
		"no athlete": {body: `{"access_token":"a"}`, check: func(t *testing.T, err error) {
			assert.ErrorContains(t, err, "athlete id")
		}},
		"no token": {body: `{"athlete":{"id":"i1"}}`, check: func(t *testing.T, err error) {
			assert.ErrorContains(t, err, "no access token")
		}},
		"not json": {body: `<html>`, check: func(t *testing.T, err error) {
			assert.ErrorContains(t, err, "decode")
		}},
		"401": {status: 401, body: `{}`, check: func(t *testing.T, err error) {
			assert.ErrorIs(t, err, connector.ErrNeedsReauth)
		}},
		"400": {status: 400, body: `{}`, check: func(t *testing.T, err error) {
			assert.ErrorContains(t, err, "HTTP 400")
			assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
		}},
		"429": {status: 429, body: `{}`, check: func(t *testing.T, err error) {
			_, ok := connector.AsRateLimit(err)
			assert.True(t, ok)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			c, f, _ := newTestConnector(t, 0)
			f.TokenStatus, f.TokenBody = tc.status, tc.body
			_, err := c.Exchange(context.Background(), "code", "")
			require.Error(t, err)
			tc.check(t, err)
		})
	}
}

func TestRefreshAsksForReauth(t *testing.T) {
	c, f, _ := newTestConnector(t, 0)
	_, err := c.Refresh(context.Background(), testConn().Tokens)
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
	assert.Empty(t, f.Requests(), "no refresh endpoint exists")
}

func TestRevoke(t *testing.T) {
	c, f, _ := newTestConnector(t, 0)
	require.NoError(t, c.Revoke(context.Background(), testConn().Tokens))
	calls := f.Calls("/api/v1/disconnect-app")
	require.Len(t, calls, 1)
	assert.Equal(t, http.MethodDelete, calls[0].Method)
	assert.Equal(t, "Bearer "+icufake.AccessToken, calls[0].Auth)

	// A token intervals.icu no longer knows is revoked already.
	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{Access: "gone"}))
	// No token: nothing to do.
	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{}))
	assert.Len(t, f.Calls("/api/v1/disconnect-app"), 2)

	f.DisconnectStatus = http.StatusInternalServerError
	assert.Error(t, c.Revoke(context.Background(), testConn().Tokens))
}

func TestConfigure(t *testing.T) {
	c := New(Options{})
	assert.Error(t, c.Configure(connector.Config{KeyClientID: "id"}))
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "id", KeyClientSecret: "s", KeyWebhookSecret: "w"}))
	id, secret := c.credentials()
	assert.Equal(t, "id", id)
	assert.Equal(t, "s", secret)
	assert.Equal(t, "w", c.webhookSecret())
	assert.Equal(t, []string{"CLIENT_ID", "CLIENT_SECRET"}, c.RequiredKeys())
	assert.True(t, c.Enabled(connector.Config{KeyClientID: "id", KeyClientSecret: "s"}))
	assert.False(t, c.Enabled(connector.Config{KeyClientID: "id"}))
}

// TestRegisteredAndConfiguredFromEnvironment checks the slug end to end: the
// connector is registered, and OHS_INTERVALS_ICU_* variables reach it under
// the whole slug, not as keys of a provider "intervals".
func TestRegisteredAndConfiguredFromEnvironment(t *testing.T) {
	got, ok := connector.Get(Slug)
	require.True(t, ok)
	_, isOAuth := connector.AsOAuth(got)
	_, isPush := connector.AsPush(got)
	assert.True(t, isOAuth)
	assert.True(t, isPush)

	env := map[string]string{
		"OHS_DATABASE_URL":                 "postgres://x",
		"OHS_ENCRYPTION_KEY":               "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"OHS_INTERVALS_ICU_CLIENT_ID":      "id",
		"OHS_INTERVALS_ICU_CLIENT_SECRET":  "secret",
		"OHS_INTERVALS_ICU_WEBHOOK_SECRET": "hook",
	}
	var environ []string
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, environ)
	require.NoError(t, err)
	assert.Equal(t, "secret", cfg.Providers[Slug][KeyClientSecret])
	assert.Equal(t, "hook", cfg.Providers[Slug][KeyWebhookSecret])
	assert.Nil(t, cfg.Providers["intervals"])

	var slugs []string
	for _, c := range connector.Enabled(cfg.Providers) {
		slugs = append(slugs, c.Slug())
	}
	assert.Contains(t, strings.Join(slugs, ","), Slug)
}

func TestExchangeRespectsContext(t *testing.T) {
	c, _, _ := newTestConnector(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Exchange(ctx, "code", "")
	assert.True(t, errors.Is(err, context.Canceled))
}
