// SPDX-License-Identifier: AGPL-3.0-only

package eightsleep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Sign-in.
//
// The token endpoint takes an OAuth 2.0 password grant in a JSON body:
// {"client_id", "client_secret", "grant_type": "password", "username",
// "password"}, and answers {"access_token", "expires_in", "userId"}. The
// sign-in does not hand out a refresh token that the community libraries
// rely on, so they keep the password and sign in again before the access
// token expires. This connector does the same: the email and password go in
// Tokens.Refresh, which the core seals with OHS_ENCRYPTION_KEY like any
// refresh token, and Refresh signs in again with them.

// credentialPrefix marks the credential blob stored as the refresh token, so
// a value of any other shape is refused rather than sent to Eight Sleep.
const credentialPrefix = "ohs-eight-sleep-v1:"

var (
	// ErrNoConsentPage is returned by AuthorizeURL and Exchange: Eight Sleep
	// has no consent page for third parties. The core sends the user to its
	// own sign-in page and calls SignIn instead.
	ErrNoConsentPage = errors.New("eight_sleep: no OAuth consent page; users sign in with their email and password (SignIn)")
)

// credentials is what a connection keeps to sign in again.
type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (k credentials) encode() (string, error) {
	b, err := json.Marshal(k)
	if err != nil {
		return "", err
	}
	return credentialPrefix + string(b), nil
}

func decodeCredentials(s string) (credentials, bool) {
	var k credentials
	rest, ok := strings.CutPrefix(s, credentialPrefix)
	if !ok || json.Unmarshal([]byte(rest), &k) != nil || k.Email == "" || k.Password == "" {
		return credentials{}, false
	}
	return k, true
}

// tokenAnswer is the token endpoint answer.
type tokenAnswer struct {
	AccessToken string          `json:"access_token"`
	ExpiresIn   json.Number     `json:"expires_in"`
	UserID      json.RawMessage `json:"userId"`
}

// AuthorizeURL fails: Eight Sleep has no consent page (see ErrNoConsentPage).
func (c *Connector) AuthorizeURL(string, string) (string, error) { return "", ErrNoConsentPage }

// Exchange fails: there is no authorization code to exchange.
func (c *Connector) Exchange(context.Context, string, string) (connector.Tokens, error) {
	return connector.Tokens{}, ErrNoConsentPage
}

// SignIn trades the account email and password for an access token. A
// refused password gives connector.ErrBadCredentials; a rate limit a
// *connector.RateLimitError.
func (c *Connector) SignIn(ctx context.Context, email, password string) (connector.Tokens, error) {
	return c.signIn(ctx, credentials{Email: email, Password: password})
}

// Refresh signs in again with the credentials the connection keeps. A
// password Eight Sleep no longer accepts (changed, or the account closed)
// gives an error wrapping connector.ErrNeedsReauth.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	k, ok := decodeCredentials(t.Refresh)
	if !ok {
		return connector.Tokens{}, fmt.Errorf("eight_sleep: the connection keeps no credentials: %w", connector.ErrNeedsReauth)
	}
	fresh, err := c.signIn(ctx, k)
	if errors.Is(err, connector.ErrBadCredentials) {
		return connector.Tokens{}, fmt.Errorf("eight_sleep: %w: %w", err, connector.ErrNeedsReauth)
	}
	return fresh, err
}

func (c *Connector) signIn(ctx context.Context, k credentials) (connector.Tokens, error) {
	id, secret := c.clientCredentials()
	body, err := json.Marshal(map[string]string{
		"client_id":     id,
		"client_secret": secret,
		"grant_type":    "password",
		"username":      k.Email,
		"password":      k.Password,
	})
	if err != nil {
		return connector.Tokens{}, err
	}
	resp, raw, err := c.do(ctx, http.MethodPost, c.authURL, "", body)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("eight_sleep: sign in: %w", err)
	}
	switch s := resp.StatusCode; {
	case s == http.StatusBadRequest, s == http.StatusUnauthorized, s == http.StatusForbidden:
		// The body is not logged: it may echo the email.
		return connector.Tokens{}, fmt.Errorf("eight_sleep: sign in: HTTP %d: %w", s, connector.ErrBadCredentials)
	case s == http.StatusTooManyRequests:
		return connector.Tokens{}, connector.RateLimited(resp.Header, c.now())
	case s >= 300:
		return connector.Tokens{}, fmt.Errorf("eight_sleep: sign in: HTTP %d", s)
	}
	var a tokenAnswer
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&a); err != nil {
		return connector.Tokens{}, fmt.Errorf("eight_sleep: sign in: decode answer: %w", err)
	}
	userID := jsonID(a.UserID)
	if a.AccessToken == "" || userID == "" {
		return connector.Tokens{}, errors.New("eight_sleep: sign in: answer without access_token or userId")
	}
	refresh, err := k.encode()
	if err != nil {
		return connector.Tokens{}, err
	}
	t := connector.Tokens{Access: a.AccessToken, Refresh: refresh, ProviderUserID: userID}
	if secs, err := a.ExpiresIn.Float64(); err == nil && secs > 0 {
		t.Expiry = c.now().Add(time.Duration(secs) * time.Second)
	}
	return t, nil
}

// jsonID reads an id sent as a string or a number.
func jsonID(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}
