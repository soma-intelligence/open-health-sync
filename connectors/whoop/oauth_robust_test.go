// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/whoop/whoopfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// strictFake is a fake that keeps WHOOP's OAuth state: codes are single use,
// a refresh rotates the refresh token and retires the access tokens.
func strictFake(t *testing.T) *fakeWhoop {
	t.Helper()
	f := newFakeWhoop(t)
	f.Strict = true
	f.ClientID, f.ClientSecret = "test-client-id", "test-client-secret"
	return f
}

// consent walks the consent page like a browser and returns the code.
func consent(t *testing.T, f *fakeWhoop, c *Connector) string {
	t.Helper()
	authURL, err := c.AuthorizeURL("state-12345678", "http://localhost/cb")
	require.NoError(t, err)
	noFollow := f.Client()
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := noFollow.Get(authURL)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusFound, res.StatusCode)
	back, err := url.Parse(res.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "state-12345678", back.Query().Get("state"))
	return back.Query().Get("code")
}

// The whole token life: consent, exchange, refresh (rotation), and the old
// refresh token refused afterwards, as WHOOP documents.
func TestOAuth_RefreshTokenRotation(t *testing.T) {
	f := strictFake(t)
	c := f.connector()
	ctx := context.Background()

	code := consent(t, f, c)
	t1, err := c.Exchange(ctx, code, "http://localhost/cb")
	require.NoError(t, err)
	require.NotEmpty(t, t1.Refresh)
	assert.Equal(t, whoopfake.UserID, t1.ProviderUserID)

	_, err = c.Exchange(ctx, code, "http://localhost/cb")
	assert.ErrorIs(t, err, connector.ErrNeedsReauth, "a code is good once (invalid_grant)")

	t2, err := c.Refresh(ctx, t1)
	require.NoError(t, err)
	assert.NotEqual(t, t1.Refresh, t2.Refresh, "rotated")
	assert.NotEqual(t, t1.Access, t2.Access)
	assert.Equal(t, t1.ProviderUserID, t2.ProviderUserID)

	// The previous access token no longer works, the new one does.
	sink := &recordingSink{}
	old := connector.Connection{Tokens: t1}
	err = c.FetchTypes(ctx, old, fetchFrom, fetchTo, []string{DataTypeWorkout}, sink)
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
	require.NoError(t, c.FetchTypes(ctx, connector.Connection{Tokens: t2}, fetchFrom, fetchTo, []string{DataTypeWorkout}, sink))
	assert.Len(t, sink.linesOf(DataTypeWorkout), 2)

	// The spent refresh token is refused: the grant would be gone if the
	// caller had not stored the rotated pair.
	_, err = c.Refresh(ctx, t1)
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
	t3, err := c.Refresh(ctx, t2)
	require.NoError(t, err)
	assert.NotEqual(t, t2.Refresh, t3.Refresh)
}

// invalid_grant sends the connection to reauthorization; invalid_client and
// server errors do not: they are the operator's or WHOOP's problem.
func TestOAuth_ErrorKinds(t *testing.T) {
	ctx := context.Background()

	f := strictFake(t)
	_, err := f.connector().Refresh(ctx, connector.Tokens{Refresh: "never-issued"})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth, "invalid_grant")

	f = strictFake(t)
	f.ClientSecret = "the-other-secret"
	_, refresh := f.IssueTokens()
	_, err = f.connector().Refresh(ctx, connector.Tokens{Refresh: refresh})
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth, "invalid_client")
	assert.Contains(t, err.Error(), "invalid_client")
	assert.NotContains(t, err.Error(), "test-client-secret", "the secret never reaches an error")

	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		f = strictFake(t)
		f.TokenStatus = status
		_, err = f.connector().Refresh(ctx, connector.Tokens{Refresh: "r"})
		require.Error(t, err, status)
		assert.NotErrorIs(t, err, connector.ErrNeedsReauth, status)
	}
}

// Without the offline scope WHOOP issues no refresh token: the connection
// would die within the hour, so the exchange is refused and the grant
// revoked.
func TestExchange_NoRefreshTokenIsRefused(t *testing.T) {
	f := strictFake(t)
	f.OmitRefreshToken = true
	c := f.connector()
	_, err := c.Exchange(context.Background(), consent(t, f, c), "http://localhost/cb")
	require.Error(t, err)
	assert.ErrorIs(t, err, errNoRefreshToken)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
	assert.Contains(t, err.Error(), "offline")
	assert.Equal(t, 1, f.Revocations(), "the access token issued is revoked")
	assert.Empty(t, f.Calls("/v2/user/profile/basic"))
}

// The access token expires during a pull (401 on the second page): Fetch
// stops with ErrNeedsReauth, the caller refreshes once and pulls again, and
// the second pull gets everything.
func TestFetch_TokenExpiresMidFetch(t *testing.T) {
	f := strictFake(t)
	c := f.connector()
	ctx := context.Background()
	access, refresh := f.IssueTokens()
	conn := connector.Connection{Tokens: connector.Tokens{Access: access, Refresh: refresh}}

	f.Fail = func(path string, n int) *whoopfake.Fault {
		if path == "/v2/cycle" && n == 2 {
			f.ExpireAccessTokens()
			return &whoopfake.Fault{Status: http.StatusUnauthorized}
		}
		return nil
	}
	sink := &recordingSink{}
	err := c.Fetch(ctx, conn, fetchFrom, fetchTo, sink)
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
	assert.NotContains(t, err.Error(), access)
	assert.Len(t, sink.emitsOf(DataTypeCycle), 1, "the first page was emitted before the 401")
	assert.Empty(t, f.Calls("/v2/recovery"), "the pull stops at the 401")

	fresh, err := c.Refresh(ctx, conn.Tokens)
	require.NoError(t, err)
	conn.Tokens = fresh
	require.NoError(t, c.Fetch(ctx, conn, fetchFrom, fetchTo, sink))
	assert.Len(t, sink.linesOf(DataTypeCycle), 6, "page one twice (raw objects are idempotent), then the rest")
	assert.Len(t, sink.linesOf(DataTypeWorkout), 2)
}

func TestRevoke(t *testing.T) {
	ctx := context.Background()

	t.Run("live token", func(t *testing.T) {
		f := strictFake(t)
		access, refresh := f.IssueTokens()
		require.NoError(t, f.connector().Revoke(ctx, connector.Tokens{Access: access, Refresh: refresh}))
		assert.Equal(t, 1, f.Revocations())
		calls := f.Calls("/v2/user/access")
		require.Len(t, calls, 1)
		assert.Equal(t, http.MethodDelete, calls[0].Method)
		assert.Equal(t, "Bearer "+access, calls[0].Auth)
		assert.Empty(t, f.Calls("/oauth2/token"))
	})
	t.Run("expired access token is refreshed first", func(t *testing.T) {
		f := strictFake(t)
		access, refresh := f.IssueTokens()
		f.ExpireAccessTokens()
		require.NoError(t, f.connector().Revoke(ctx, connector.Tokens{Access: access, Refresh: refresh}))
		assert.Equal(t, 1, f.Revocations())
		assert.Len(t, f.Calls("/oauth2/token"), 1)
	})
	t.Run("grant already gone", func(t *testing.T) {
		f := strictFake(t)
		require.NoError(t, f.connector().Revoke(ctx, connector.Tokens{Access: "dead", Refresh: "dead"}))
		assert.Zero(t, f.Revocations())
	})
	t.Run("already revoked (404)", func(t *testing.T) {
		f := newFakeWhoop(t)
		f.Fail = func(string, int) *whoopfake.Fault { return &whoopfake.Fault{Status: http.StatusNotFound} }
		require.NoError(t, f.connector().Revoke(ctx, connector.Tokens{Access: "a"}))
	})
	t.Run("server error", func(t *testing.T) {
		f := newFakeWhoop(t)
		f.Fail = func(string, int) *whoopfake.Fault { return &whoopfake.Fault{Status: http.StatusServiceUnavailable} }
		err := f.connector().Revoke(ctx, connector.Tokens{Access: "a", Refresh: "r"})
		require.Error(t, err)
		var se *statusError
		assert.True(t, errors.As(err, &se), "%v", err)
		assert.Len(t, f.Calls("/v2/user/access"), maxAttempts, "retried")
	})
	t.Run("no token at all", func(t *testing.T) {
		assert.Error(t, New(Options{ClientID: "a", ClientSecret: "b"}).Revoke(ctx, connector.Tokens{}))
	})
}
