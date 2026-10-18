// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// OAuth 2.0 with PKCE (S256), from Garmin's public OAuth 2.0 PKCE
// specification. Garmin has no scopes to request: the API sections are set
// when the app is created and the user ticks the permissions at consent.
//
// PKCE needs the code verifier at the token exchange, but AuthorizeURL and
// the exchange are separate requests and the core stores nothing for the
// connector. So the verifier is derived from the OAuth state, which the core
// keeps and hands back to ExchangeState: base64url(HMAC-SHA256(client
// secret, "ohs-garmin-pkce:" + state)), 43 characters. The state is random
// and single use, and the client secret never leaves the server, so the
// verifier is as unguessable as a random one.

// pkcePrefix separates the verifier derivation from any other use of the
// client secret as an HMAC key.
const pkcePrefix = "ohs-garmin-pkce:"

// Health API paths of the user endpoints.
const (
	pathUserID       = "/wellness-api/rest/user/id"
	pathRegistration = "/wellness-api/rest/user/registration"
)

var (
	// errNotConfigured reports a call that needs the client keys before they
	// were set.
	errNotConfigured = errors.New("garmin: client id and secret are not configured")
	// ErrPKCEState is returned by Exchange: Garmin requires PKCE, whose
	// verifier is derived from the OAuth state, which Exchange does not get.
	// The core calls ExchangeState instead.
	ErrPKCEState = errors.New("garmin: the token exchange needs the OAuth state to derive the PKCE code verifier; call ExchangeState")
)

// CodeVerifier derives the PKCE code verifier of an OAuth state under the
// client secret: base64url without padding of HMAC-SHA256(secret,
// "ohs-garmin-pkce:" + state), 43 characters from the unreserved set.
func CodeVerifier(secret, state string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(pkcePrefix + state))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CodeChallenge is the S256 challenge of a verifier: base64url without
// padding of SHA-256(verifier) (RFC 7636 section 4.2).
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL returns the Garmin Connect consent page URL, with the S256
// challenge of the verifier derived from state.
func (c *Connector) AuthorizeURL(state, redirect string) (string, error) {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return "", errNotConfigured
	}
	if state == "" {
		return "", errors.New("garmin: authorize: PKCE needs a non-empty state")
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", id)
	q.Set("code_challenge", CodeChallenge(CodeVerifier(secret, state)))
	q.Set("code_challenge_method", "S256")
	if redirect != "" {
		q.Set("redirect_uri", redirect)
	}
	q.Set("state", state)
	return c.authURL + "?" + q.Encode(), nil
}

// Exchange always fails: Garmin requires PKCE and the verifier comes from
// the state, which only ExchangeState receives.
func (c *Connector) Exchange(context.Context, string, string) (connector.Tokens, error) {
	return connector.Tokens{}, ErrPKCEState
}

// ExchangeState trades an authorization code for tokens, sending the code
// verifier derived from state, then reads the Garmin user id
// (GET /wellness-api/rest/user/id), which notifications are keyed by.
func (c *Connector) ExchangeState(ctx context.Context, code, state, redirect string) (connector.Tokens, error) {
	_, secret := c.credentials()
	if secret == "" {
		return connector.Tokens{}, errNotConfigured
	}
	if code == "" || state == "" {
		return connector.Tokens{}, errors.New("garmin: exchange: code and state are required")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("code_verifier", CodeVerifier(secret, state))
	if redirect != "" {
		form.Set("redirect_uri", redirect)
	}
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("garmin: exchange: %w", err)
	}
	uid, err := c.userID(ctx, t.Access)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("garmin: exchange: %w", err)
	}
	t.ProviderUserID = uid
	return t, nil
}

// Refresh trades the refresh token for new tokens. Garmin rotates the
// refresh token: the answer carries a new one, which replaces the old. The
// Garmin user id is carried over.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Refresh == "" {
		return connector.Tokens{}, fmt.Errorf("garmin: refresh: no refresh token: %w", connector.ErrNeedsReauth)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	nt, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("garmin: refresh: %w", err)
	}
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh
	}
	if len(nt.Scopes) == 0 {
		nt.Scopes = t.Scopes
	}
	nt.ProviderUserID = t.ProviderUserID
	return nt, nil
}

// Revoke deletes the user's registration with this app
// (DELETE /wellness-api/rest/user/registration), which ends Garmin's
// notifications for the user and invalidates the grant. A user already gone
// (404) or a token already refused (401) counts as done.
func (c *Connector) Revoke(ctx context.Context, t connector.Tokens) error {
	if t.Access == "" {
		return errors.New("garmin: revoke: no access token")
	}
	resp, _, err := c.do(ctx, http.MethodDelete, t.Access, c.apiBase+pathRegistration)
	if err != nil {
		return fmt.Errorf("garmin: revoke: %w", err)
	}
	switch s := resp.StatusCode; {
	case s < 300, s == http.StatusNotFound, s == http.StatusUnauthorized:
		return nil
	case s == http.StatusTooManyRequests:
		return fmt.Errorf("garmin: revoke: %w", connector.RateLimited(resp.Header, c.now()))
	default:
		return fmt.Errorf("garmin: revoke: HTTP %d", s)
	}
}

// userID reads the Garmin user id of a token.
func (c *Connector) userID(ctx context.Context, token string) (string, error) {
	body, err := c.get(ctx, token, pathUserID, nil)
	if err != nil {
		return "", fmt.Errorf("user id: %w", err)
	}
	var u struct {
		UserID string `json:"userId"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return "", fmt.Errorf("user id: decode answer: %w", err)
	}
	if u.UserID == "" {
		return "", errors.New("user id: answer holds no userId")
	}
	return u.UserID, nil
}

// tokenBody is the answer of the token endpoint.
type tokenBody struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

// requestToken posts form to the token endpoint, with the client id and
// secret in the form as Garmin's specification shows.
func (c *Connector) requestToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return connector.Tokens{}, errNotConfigured
	}
	form.Set("client_id", id)
	form.Set("client_secret", secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return connector.Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("token request: %w", err)
	}
	body, err := readBody(resp.Body, 1<<20)
	resp.Body.Close()
	if err != nil {
		return connector.Tokens{}, err
	}
	var tb tokenBody
	jsonErr := json.NewDecoder(bytes.NewReader(body)).Decode(&tb)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return connector.Tokens{}, connector.RateLimited(resp.Header, c.now())
	case jsonErr == nil && strings.Contains(tb.Error, "invalid_grant"),
		resp.StatusCode == http.StatusUnauthorized,
		resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "invalid_grant"):
		return connector.Tokens{}, connector.ErrNeedsReauth
	case resp.StatusCode >= 400:
		return connector.Tokens{}, fmt.Errorf("token endpoint HTTP %d", resp.StatusCode)
	case jsonErr != nil:
		return connector.Tokens{}, fmt.Errorf("token endpoint: decode answer: %w", jsonErr)
	case tb.AccessToken == "":
		return connector.Tokens{}, errors.New("token endpoint: answer holds no access token")
	}
	t := connector.Tokens{Access: tb.AccessToken, Refresh: tb.RefreshToken}
	if tb.Scope != "" {
		t.Scopes = strings.Fields(tb.Scope)
	}
	if tb.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(tb.ExpiresIn) * time.Second).UTC()
	}
	return t, nil
}
