// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/garmin/garminfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestCodeVerifier_IsDeterministicUnreservedAndKeyed(t *testing.T) {
	v := CodeVerifier("secret", "state-1")
	assert.Equal(t, v, CodeVerifier("secret", "state-1"))
	assert.Len(t, v, 43)
	assert.Regexp(t, regexp.MustCompile(`^[A-Za-z0-9\-._~]{43,128}$`), v)
	assert.NotEqual(t, v, CodeVerifier("secret", "state-2"), "another state, another verifier")
	assert.NotEqual(t, v, CodeVerifier("other-secret", "state-1"), "another secret, another verifier")
}

func TestCodeChallenge_RFC7636Vector(t *testing.T) {
	// RFC 7636, appendix B.
	assert.Equal(t, "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		CodeChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"))
}

func TestAuthorizeURL_CarriesS256ChallengeOfStateVerifier(t *testing.T) {
	f := newFakeGarmin(t)
	c := f.connector()
	raw, err := c.AuthorizeURL("state-abc", "https://ohs.example/oauth/garmin/callback")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, "/oauth2Confirm", u.Path)
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, testClientID, q.Get("client_id"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, CodeChallenge(CodeVerifier(testClientSecret, "state-abc")), q.Get("code_challenge"))
	assert.Equal(t, "https://ohs.example/oauth/garmin/callback", q.Get("redirect_uri"))
	assert.Equal(t, "state-abc", q.Get("state"))
	assert.Empty(t, q.Get("code_verifier"), "the verifier never leaves the server")
	assert.NotContains(t, raw, testClientSecret)

	_, err = c.AuthorizeURL("", "https://ohs.example/cb")
	assert.Error(t, err, "PKCE needs a state")
	_, err = New(Options{}).AuthorizeURL("s", "r")
	assert.ErrorIs(t, err, errNotConfigured)
}

// TestPKCE_RoundTrip: the challenge sent at authorize matches the verifier
// sent at the exchange, which the fake checks like Garmin would.
func TestPKCE_RoundTrip(t *testing.T) {
	f := newFakeGarmin(t)
	c := f.connector()
	raw, err := c.AuthorizeURL("state-xyz", "https://ohs.example/cb")
	require.NoError(t, err)
	u, _ := url.Parse(raw)
	f.Challenge = u.Query().Get("code_challenge")
	f.Code = "auth-code-1"

	tok, err := c.ExchangeState(context.Background(), "auth-code-1", "state-xyz", "https://ohs.example/cb")
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-1", tok.Access)
	assert.Equal(t, "fake-refresh-token-1", tok.Refresh)
	assert.Equal(t, garminfake.UserID, tok.ProviderUserID)
	assert.Equal(t, fakeNow.Add(86400e9), tok.Expiry)
	assert.Contains(t, tok.Scopes, "CONNECT_READ")

	calls := f.Calls(http.MethodPost, garminfake.TokenPath)
	require.Len(t, calls, 1)
	form := calls[0].Form
	assert.Equal(t, "authorization_code", form.Get("grant_type"))
	assert.Equal(t, testClientID, form.Get("client_id"))
	assert.Equal(t, testClientSecret, form.Get("client_secret"))
	assert.Equal(t, "https://ohs.example/cb", form.Get("redirect_uri"))
	uid := f.Calls(http.MethodGet, garminfake.UserIDPath)
	require.Len(t, uid, 1)
	assert.Equal(t, "Bearer fake-access-token-1", uid[0].Auth)

	// The wrong state derives another verifier: Garmin refuses the code.
	_, err = c.ExchangeState(context.Background(), "auth-code-1", "state-other", "https://ohs.example/cb")
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestExchange_WithoutStateExplainsPKCE(t *testing.T) {
	_, err := New(Options{ClientID: "a", ClientSecret: "b"}).Exchange(context.Background(), "code", "r")
	assert.ErrorIs(t, err, ErrPKCEState)
}

func TestExchangeState_Errors(t *testing.T) {
	f := newFakeGarmin(t)
	_, err := f.connector().ExchangeState(context.Background(), "", "s", "r")
	assert.Error(t, err)

	f.TokenStatus, f.TokenBody = http.StatusTooManyRequests, ""
	_, err = f.connector().ExchangeState(context.Background(), "c", "s", "r")
	_, limited := connector.AsRateLimit(err)
	assert.True(t, limited)

	f.TokenStatus, f.TokenBody = http.StatusOK, `{"token_type":"bearer"}`
	_, err = f.connector().ExchangeState(context.Background(), "c", "s", "r")
	assert.ErrorContains(t, err, "no access token")

	f.TokenStatus = 0
	f.AccessToken = "another-token" // the user id call is refused
	_, err = f.connector().ExchangeState(context.Background(), "c", "s", "r")
	assert.Error(t, err)
}

func TestRefresh_RotatesRefreshTokenAndKeepsUser(t *testing.T) {
	f := newFakeGarmin(t)
	c := f.connector()
	in := testConn().Tokens
	out, err := c.Refresh(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "fake-access-token-2", out.Access)
	assert.Equal(t, "fake-refresh-token-2", out.Refresh, "Garmin rotates the refresh token")
	assert.Equal(t, garminfake.UserID, out.ProviderUserID)
	calls := f.Calls(http.MethodPost, garminfake.TokenPath)
	require.Len(t, calls, 1)
	assert.Equal(t, "refresh_token", calls[0].Form.Get("grant_type"))
	assert.Equal(t, "fake-refresh-token-1", calls[0].Form.Get("refresh_token"))

	_, err = c.Refresh(context.Background(), connector.Tokens{Access: "a"})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
	_, err = c.Refresh(context.Background(), connector.Tokens{Refresh: "revoked-1"})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)

	f.TokenStatus = http.StatusUnauthorized
	_, err = c.Refresh(context.Background(), in)
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestRefresh_KeepsOldRefreshTokenWhenNoneReturned(t *testing.T) {
	f := newFakeGarmin(t)
	f.TokenStatus, f.TokenBody = http.StatusOK, `{"access_token":"new","expires_in":60}`
	out, err := f.connector().Refresh(context.Background(), testConn().Tokens)
	require.NoError(t, err)
	assert.Equal(t, "fake-refresh-token-1", out.Refresh)
}

func TestRevoke_DeletesRegistration(t *testing.T) {
	f := newFakeGarmin(t)
	require.NoError(t, f.connector().Revoke(context.Background(), testConn().Tokens))
	calls := f.Calls(http.MethodDelete, garminfake.RegistrationPath)
	require.Len(t, calls, 1)
	assert.Equal(t, "Bearer fake-access-token-1", calls[0].Auth)

	f.AccessToken = "other" // 401: already gone counts as done
	require.NoError(t, f.connector().Revoke(context.Background(), testConn().Tokens))
	assert.Error(t, f.connector().Revoke(context.Background(), connector.Tokens{}))
}

func TestConfigure(t *testing.T) {
	c := New(Options{})
	assert.Error(t, c.Configure(connector.Config{KeyClientID: "a"}))
	require.NoError(t, c.Configure(connector.Config{KeyClientID: "a", KeyClientSecret: "b", KeyInboundToken: "t"}))
	id, secret := c.credentials()
	assert.Equal(t, "a", id)
	assert.Equal(t, "b", secret)
	assert.Equal(t, "t", c.inboundToken())
	assert.True(t, c.Enabled(connector.Config{KeyClientID: "a", KeyClientSecret: "b"}))
	assert.False(t, c.Enabled(connector.Config{KeyClientID: "a"}))
	assert.Equal(t, []string{KeyClientID, KeyClientSecret}, c.RequiredKeys())
	assert.Equal(t, "garmin", c.Slug())
}

func TestRegistered(t *testing.T) {
	c, ok := connector.Get(Slug)
	require.True(t, ok)
	_, isState := c.(connector.StateExchanger)
	_, isNotified := c.(connector.NotifiedFetcher)
	_, isRevoker := c.(connector.Revoker)
	_, isPush := connector.AsPush(c)
	assert.True(t, isState && isNotified && isRevoker && isPush)
}
