// SPDX-License-Identifier: AGPL-3.0-only

package smartfhir

import (
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

// SMART App Launch requires PKCE (S256). The core stores nothing for the
// connector between AuthorizeURL and the exchange, so the verifier is derived
// from the OAuth state, as for Garmin: base64url(HMAC-SHA256(key,
// "ohs-smart-fhir-pkce:" + state)). See verifierKey for the key.

// pkcePrefix separates the verifier derivation from any other use of the key.
const pkcePrefix = "ohs-smart-fhir-pkce:"

var (
	// errNotConfigured reports a call that needs the keys before they were set.
	errNotConfigured = errors.New("smart_fhir: client id and FHIR base URL are not configured")
	// ErrPKCEState is returned by Exchange: the verifier comes from the state,
	// which only ExchangeState receives.
	ErrPKCEState = errors.New("smart_fhir: the token exchange needs the OAuth state to derive the PKCE code verifier; call ExchangeState")
)

// CodeVerifier derives the PKCE code verifier of an OAuth state under key.
func CodeVerifier(key, state string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(pkcePrefix + state))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CodeChallenge is the S256 challenge of a verifier (RFC 7636 section 4.2).
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL returns the health system's sign-in page for a standalone
// patient launch: aud names the FHIR server, the challenge is derived from
// state.
func (c *Connector) AuthorizeURL(state, redirect string) (string, error) {
	id := c.key(KeyClientID)
	if id == "" || c.base() == "" {
		return "", errNotConfigured
	}
	if state == "" {
		return "", errors.New("smart_fhir: authorize: PKCE needs a non-empty state")
	}
	eps, err := c.endpointsOf(context.Background())
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", id)
	if redirect != "" {
		q.Set("redirect_uri", redirect)
	}
	q.Set("scope", strings.Join(c.scopes(), " "))
	q.Set("state", state)
	q.Set("aud", c.base())
	q.Set("code_challenge", CodeChallenge(CodeVerifier(c.verifierKey(), state)))
	q.Set("code_challenge_method", "S256")
	sep := "?"
	if strings.Contains(eps.authorize, "?") {
		sep = "&"
	}
	return eps.authorize + sep + q.Encode(), nil
}

// Exchange always fails: the verifier comes from the state, which only
// ExchangeState receives.
func (c *Connector) Exchange(context.Context, string, string) (connector.Tokens, error) {
	return connector.Tokens{}, ErrPKCEState
}

// ExchangeState trades an authorization code for tokens. The token answer
// names the patient in context ("patient"), kept as the provider user id:
// every pull reads that patient's record.
func (c *Connector) ExchangeState(ctx context.Context, code, state, redirect string) (connector.Tokens, error) {
	if code == "" || state == "" {
		return connector.Tokens{}, errors.New("smart_fhir: exchange: code and state are required")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("code_verifier", CodeVerifier(c.verifierKey(), state))
	if redirect != "" {
		form.Set("redirect_uri", redirect)
	}
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("smart_fhir: exchange: %w", err)
	}
	if t.ProviderUserID == "" {
		return connector.Tokens{}, errors.New("smart_fhir: exchange: the token answer names no patient (was launch/patient granted?)")
	}
	return t, nil
}

// Refresh trades the refresh token for new tokens. A server that issues no
// refresh token (Epic, for a public client) leaves the connection to expire
// with its access token: the user connects again.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Refresh == "" {
		return connector.Tokens{}, fmt.Errorf("smart_fhir: refresh: no refresh token: %w", connector.ErrNeedsReauth)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	nt, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("smart_fhir: refresh: %w", err)
	}
	if nt.ProviderUserID == "" {
		nt.ProviderUserID = t.ProviderUserID
	}
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh
	}
	if len(nt.Scopes) == 0 {
		nt.Scopes = t.Scopes
	}
	return nt, nil
}

// tokenBody is the answer of the token endpoint (SMART App Launch 2.x,
// section "Response").
type tokenBody struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	Patient      string `json:"patient"`
	Error        string `json:"error"`
}

// requestToken posts form to the token endpoint. A confidential client
// authenticates with HTTP Basic (client_secret_basic, which every SMART
// server supports); a public client sends its client_id in the body.
func (c *Connector) requestToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	id, secret := c.key(KeyClientID), c.key(KeyClientSecret)
	if id == "" {
		return connector.Tokens{}, errNotConfigured
	}
	eps, err := c.endpointsOf(ctx)
	if err != nil {
		return connector.Tokens{}, err
	}
	if secret == "" {
		form.Set("client_id", id)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, eps.token, strings.NewReader(form.Encode()))
	if err != nil {
		return connector.Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if secret != "" {
		req.SetBasicAuth(url.QueryEscape(id), url.QueryEscape(secret))
	}
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
	jsonErr := json.Unmarshal(body, &tb)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return connector.Tokens{}, connector.RateLimited(resp.Header, c.now())
	case tb.Error == "invalid_grant", resp.StatusCode == http.StatusUnauthorized:
		return connector.Tokens{}, connector.ErrNeedsReauth
	case resp.StatusCode >= 400:
		return connector.Tokens{}, fmt.Errorf("token endpoint HTTP %d (%s)", resp.StatusCode, tb.Error)
	case jsonErr != nil:
		return connector.Tokens{}, fmt.Errorf("token endpoint: decode answer: %w", jsonErr)
	case tb.AccessToken == "":
		return connector.Tokens{}, errors.New("token endpoint: answer holds no access token")
	}
	t := connector.Tokens{
		Access:         tb.AccessToken,
		Refresh:        tb.RefreshToken,
		Scopes:         strings.Fields(tb.Scope),
		ProviderUserID: tb.Patient,
	}
	if tb.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(tb.ExpiresIn) * time.Second).UTC()
	}
	return t, nil
}
