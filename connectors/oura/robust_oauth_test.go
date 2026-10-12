// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/oura/ourafake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Oura rotates refresh tokens: each refresh returns a new pair, and a spent
// refresh token is refused for good.
func TestRefresh_RotatingRefreshTokens(t *testing.T) {
	f := newFakeOura(t)
	f.Rotate = true
	c := f.connector()
	ctx := context.Background()
	t0, err := c.Exchange(ctx, "code-1", "http://localhost/cb")
	require.NoError(t, err)
	require.Equal(t, "fake-oura-user-0001", t0.ProviderUserID)

	t1, err := c.Refresh(ctx, t0)
	require.NoError(t, err)
	assert.NotEqual(t, t0.Refresh, t1.Refresh)
	assert.NotEqual(t, t0.Access, t1.Access)
	assert.Equal(t, t0.ProviderUserID, t1.ProviderUserID, "kept across refreshes")
	assert.Equal(t, fakeNow.Add(24*time.Hour), t1.Expiry)

	t2, err := c.Refresh(ctx, t1)
	require.NoError(t, err, "the newest refresh token works")
	assert.NotEqual(t, t1.Refresh, t2.Refresh)

	_, err = c.Refresh(ctx, t0)
	require.ErrorIs(t, err, connector.ErrNeedsReauth, "a spent refresh token is invalid_grant")
	_, err = c.Refresh(ctx, t1)
	require.ErrorIs(t, err, connector.ErrNeedsReauth)

	// An authorization code works once.
	_, err = c.Exchange(ctx, "code-1", "http://localhost/cb")
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}

// How the token endpoint's answers are read. Only a refused grant sends the
// user back through consent; refused client credentials are the operator's
// to fix; anything else is retried later.
func TestTokenEndpoint_ErrorClasses(t *testing.T) {
	type class int
	const (
		reauth class = iota
		client
		rateLimited
		transient
	)
	cases := []struct {
		name   string
		status int
		body   string
		header http.Header
		want   class
	}{
		{"invalid_grant 400", http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Invalid refresh token"}`, nil, reauth},
		{"invalid_grant 401", http.StatusUnauthorized, `{"error":"invalid_grant"}`, nil, reauth},
		{"invalid_grant 200", http.StatusOK, `{"error":"invalid_grant"}`, nil, reauth},
		{"401 without OAuth error", http.StatusUnauthorized, `{"detail":"Unauthorized"}`, nil, reauth},
		{"401 empty", http.StatusUnauthorized, ``, nil, reauth},
		{"invalid_client 401", http.StatusUnauthorized, `{"error":"invalid_client"}`, nil, client},
		{"invalid_client 400", http.StatusBadRequest, `{"error":"invalid_client"}`, nil, client},
		{"unauthorized_client", http.StatusBadRequest, `{"error":"unauthorized_client"}`, nil, client},
		{"429", http.StatusTooManyRequests, `{"detail":"slow down"}`, http.Header{"Retry-After": {"9"}}, rateLimited},
		{"500", http.StatusInternalServerError, `{"error":"server_error"}`, nil, transient},
		{"502 html", http.StatusBadGateway, `<html>bad gateway</html>`, nil, transient},
		{"503", http.StatusServiceUnavailable, ``, nil, transient},
		{"504", http.StatusGatewayTimeout, ``, nil, transient},
		{"400 invalid_request", http.StatusBadRequest, `{"error":"invalid_request"}`, nil, transient},
		{"400 invalid_scope", http.StatusBadRequest, `{"error":"invalid_scope"}`, nil, transient},
		{"200 not json", http.StatusOK, `not json`, nil, transient},
		{"200 no access token", http.StatusOK, `{"token_type":"bearer"}`, nil, transient},
		{"200 temporarily_unavailable", http.StatusOK, `{"error":"temporarily_unavailable"}`, nil, transient},
		{"200 error not a string", http.StatusOK, `{"error":{"code":1}}`, nil, transient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOura(t)
			f.Intercept = func(r *http.Request) *ourafake.Fault {
				if r.URL.Path == "/oauth/token" {
					return &ourafake.Fault{Status: tc.status, Header: tc.header, Body: tc.body}
				}
				return nil
			}
			for _, call := range []func() error{
				func() error {
					_, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "secret-refresh-value"})
					return err
				},
				func() error {
					_, err := f.connector().Exchange(context.Background(), "secret-code-value", "http://localhost/cb")
					return err
				},
			} {
				err := call()
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "secret-refresh-value")
				assert.NotContains(t, err.Error(), "secret-code-value")
				assert.NotContains(t, err.Error(), "test-client-secret")
				_, limited := connector.AsRateLimit(err)
				switch tc.want {
				case reauth:
					assert.ErrorIs(t, err, connector.ErrNeedsReauth)
				case client:
					assert.ErrorIs(t, err, ErrClientRejected)
					assert.NotErrorIs(t, err, connector.ErrNeedsReauth, "a wrong client secret must not disconnect every user")
				case rateLimited:
					assert.True(t, limited)
					assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
				case transient:
					assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
					assert.NotErrorIs(t, err, ErrClientRejected)
					assert.False(t, limited)
				}
			}
		})
	}
}

// The token answer's expires_in may come as a number or a string; an absurd
// one neither overflows nor fails the grant.
func TestTokenEndpoint_ExpiresIn(t *testing.T) {
	cases := map[string]time.Duration{
		`86400`:     24 * time.Hour,
		`"86400"`:   24 * time.Hour,
		`86400.0`:   24 * time.Hour,
		`null`:      0,
		`-5`:        0,
		`"soon"`:    0,
		`1e30`:      maxExpiresIn * time.Second,
		`922337203`: maxExpiresIn * time.Second,
	}
	for raw, want := range cases {
		f := newFakeOura(t)
		f.TokenBody = `{"access_token":"a","refresh_token":"r","expires_in":` + raw + `}`
		tok, err := f.connector().Refresh(context.Background(), connector.Tokens{Refresh: "x"})
		require.NoError(t, err, raw)
		if want == 0 {
			assert.True(t, tok.Expiry.IsZero(), raw)
			continue
		}
		assert.Equal(t, fakeNow.Add(want), tok.Expiry, raw)
	}
}

// A token request cancelled by the caller reports the cancellation, not a
// provider error.
func TestTokenEndpoint_Cancelled(t *testing.T) {
	f := newFakeOura(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.connector().Refresh(ctx, connector.Tokens{Refresh: "r"})
	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
}

// A token endpoint that cannot be reached is a transient failure.
func TestTokenEndpoint_Unreachable(t *testing.T) {
	c := New(Options{ClientID: "id", ClientSecret: "secret", APIBase: "http://127.0.0.1:1"})
	_, err := c.Refresh(context.Background(), connector.Tokens{Refresh: "r"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
	assert.NotErrorIs(t, err, ErrClientRejected)
}
