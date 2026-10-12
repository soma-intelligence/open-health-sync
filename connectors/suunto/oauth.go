// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"context"
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

// errNotConfigured reports a call that needs the operator keys before they
// were set.
var errNotConfigured = errors.New("suunto: client id, secret and subscription key are not configured")

// AuthorizeURL returns the Suunto consent page URL. Suunto grants its default
// scope (workout); no scope parameter is sent.
func (c *Connector) AuthorizeURL(state, redirect string) (string, error) {
	id, _ := c.credentials()
	if id == "" {
		return "", errNotConfigured
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", id)
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	return c.oauthBase + "/oauth/authorize?" + q.Encode(), nil
}

// Exchange trades an authorization code for tokens. The provider user id is
// the Suunto username, read from the "user" claim of the access token (a
// JWT); notifications name the user by it.
func (c *Connector) Exchange(ctx context.Context, code, redirect string) (connector.Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	if redirect != "" {
		form.Set("redirect_uri", redirect)
	}
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("suunto: exchange: %w", err)
	}
	if t.ProviderUserID == "" {
		return connector.Tokens{}, errors.New("suunto: exchange: access token names no user")
	}
	return t, nil
}

// Refresh trades a refresh token for new tokens. A refresh token Suunto
// rejects (HTTP 401, or 400 invalid_grant) gives connector.ErrNeedsReauth.
// When the answer holds no new refresh token the old one is kept.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Refresh == "" {
		return connector.Tokens{}, fmt.Errorf("suunto: refresh: no refresh token: %w", connector.ErrNeedsReauth)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	nt, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("suunto: refresh: %w", err)
	}
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh
	}
	if nt.ProviderUserID == "" {
		nt.ProviderUserID = t.ProviderUserID
	}
	if len(nt.Scopes) == 0 {
		nt.Scopes = t.Scopes
	}
	return nt, nil
}

// Revoke disconnects the user from this application at Suunto
// (GET /oauth/deauthorize?client_id=..., the access token as bearer).
// Disconnecting calls it best effort. Without an access token there is
// nothing to send.
func (c *Connector) Revoke(ctx context.Context, t connector.Tokens) error {
	id, _ := c.credentials()
	if id == "" {
		return errNotConfigured
	}
	if t.Access == "" {
		return nil
	}
	q := url.Values{}
	q.Set("client_id", id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.oauthBase+"/oauth/deauthorize?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.Access)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("suunto: deauthorize request: %w", err)
	}
	_, _ = readBody(resp.Body, 1<<20)
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return connector.RateLimited(resp.Header, c.now())
	case resp.StatusCode == http.StatusUnauthorized:
		// The grant is already gone: nothing left to revoke.
		return nil
	case resp.StatusCode >= 400:
		return fmt.Errorf("suunto: deauthorize HTTP %d", resp.StatusCode)
	}
	return nil
}

// requestToken posts to the token endpoint, the client credentials in HTTP
// Basic as Suunto documents.
func (c *Connector) requestToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return connector.Tokens{}, errNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.oauthBase+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return connector.Tokens{}, err
	}
	req.SetBasicAuth(id, secret)
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

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return connector.Tokens{}, connector.ErrNeedsReauth
	case resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "invalid_grant"):
		return connector.Tokens{}, connector.ErrNeedsReauth
	case resp.StatusCode == http.StatusTooManyRequests:
		return connector.Tokens{}, connector.RateLimited(resp.Header, c.now())
	case resp.StatusCode >= 400:
		return connector.Tokens{}, fmt.Errorf("token endpoint HTTP %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return connector.Tokens{}, fmt.Errorf("token endpoint: decode answer: %w", err)
	}
	if tr.AccessToken == "" {
		return connector.Tokens{}, errors.New("token endpoint: answer holds no access token")
	}
	t := connector.Tokens{
		Access:  tr.AccessToken,
		Refresh: tr.RefreshToken,
	}
	if secs, err := tr.ExpiresIn.Int64(); err == nil && secs > 0 {
		t.Expiry = c.now().Add(time.Duration(secs) * time.Second).UTC()
	}
	t.Scopes = strings.FieldsFunc(tr.Scope, func(r rune) bool { return r == ',' || r == ' ' })
	if len(t.Scopes) == 0 {
		t.Scopes = nil
	}
	t.ProviderUserID = jwtUser(tr.AccessToken)
	if t.ProviderUserID == "" {
		t.ProviderUserID = strings.TrimSpace(tr.User)
	}
	return t, nil
}

// jwtUser returns the "user" claim of a JWT, or "" when the token is not a
// JWT or has no such claim.
//
// The signature is not verified, on purpose. The token was just handed to us
// by Suunto's token endpoint over TLS, in answer to a request authenticated
// with our client secret, so its origin is already established; Suunto does
// not publish a key to verify it with; and the claim is only a lookup key for
// incoming notifications, never an authorization decision. A forged token
// could not reach this code without first impersonating Suunto's TLS host.
func jwtUser(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		User any `json:"user"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	switch v := claims.User.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strings.TrimSpace(fmt.Sprintf("%.0f", v))
	}
	return ""
}
