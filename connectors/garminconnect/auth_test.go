// SPDX-License-Identifier: AGPL-3.0-only

package garminconnect

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/garminconnect/garminconnectfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

var fakeNow = time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)

// newConnector returns a connector pointed at f, with no pacing, a fixed
// clock and no real waits.
func newConnector(f *garminconnectfake.Server, opts ...func(*Options)) *Connector {
	o := Options{
		SSOBase:    f.URL(),
		TokenURL:   f.TokenURL(),
		APIBase:    f.URL(),
		HTTPClient: f.Client(),
		ChunkDelay: -1,
		Now:        func() time.Time { return fakeNow },
		Sleep:      func(context.Context, time.Duration) error { return nil },
	}
	for _, fn := range opts {
		fn(&o)
	}
	return New(o)
}

func TestSignIn(t *testing.T) {
	f := garminconnectfake.New(t)
	c := newConnector(f)

	tok, err := c.SignIn(context.Background(), garminconnectfake.Email, garminconnectfake.Password)
	require.NoError(t, err)
	assert.Equal(t, f.Bearer(), tok.Access)
	assert.Equal(t, garminconnectfake.Subject, tok.ProviderUserID)
	assert.True(t, tok.Expiry.After(fakeNow))

	// The stored refresh is the sealed credential blob, not the password in
	// the clear.
	k, ok := decodeCredentials(tok.Refresh)
	require.True(t, ok)
	assert.Equal(t, garminconnectfake.Email, k.Email)
}

func TestSignInBadCredentials(t *testing.T) {
	f := garminconnectfake.New(t)
	c := newConnector(f)
	_, err := c.SignIn(context.Background(), garminconnectfake.Email, "wrong")
	assert.ErrorIs(t, err, connector.ErrBadCredentials)
}

func TestSignInMFAUnsupported(t *testing.T) {
	f := garminconnectfake.New(t)
	f.LoginResponseType = "MFA_REQUIRED"
	c := newConnector(f)
	_, err := c.SignIn(context.Background(), garminconnectfake.Email, garminconnectfake.Password)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "two-factor")
}

func TestSignInBlocked(t *testing.T) {
	f := garminconnectfake.New(t)
	f.LoginStatus = http.StatusForbidden
	c := newConnector(f)
	_, err := c.SignIn(context.Background(), garminconnectfake.Email, garminconnectfake.Password)
	assert.ErrorIs(t, err, ErrBlocked)
}

func TestSignInRateLimited(t *testing.T) {
	f := garminconnectfake.New(t)
	f.LoginStatus = http.StatusTooManyRequests
	f.RetryAfter = "60"
	c := newConnector(f)
	_, err := c.SignIn(context.Background(), garminconnectfake.Email, garminconnectfake.Password)
	_, limited := connector.AsRateLimit(err)
	assert.True(t, limited)
}

func TestRefresh(t *testing.T) {
	f := garminconnectfake.New(t)
	c := newConnector(f)
	tok, err := c.SignIn(context.Background(), garminconnectfake.Email, garminconnectfake.Password)
	require.NoError(t, err)

	fresh, err := c.Refresh(context.Background(), connector.Tokens{Refresh: tok.Refresh, ProviderUserID: "kept"})
	require.NoError(t, err)
	assert.Equal(t, f.Bearer(), fresh.Access)
	assert.Equal(t, garminconnectfake.Subject, fresh.ProviderUserID)
	assert.Equal(t, 2, f.SignIns())
}

func TestRefreshWithoutCredentials(t *testing.T) {
	f := garminconnectfake.New(t)
	c := newConnector(f)
	_, err := c.Refresh(context.Background(), connector.Tokens{Refresh: "garbage"})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestAuthorizeURLHasNoConsentPage(t *testing.T) {
	c := New(Options{})
	_, err := c.AuthorizeURL("state", "https://x/cb")
	assert.ErrorIs(t, err, ErrNoConsentPage)
	_, err = c.Exchange(context.Background(), "code", "https://x/cb")
	assert.ErrorIs(t, err, ErrNoConsentPage)
}
