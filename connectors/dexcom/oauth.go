// SPDX-License-Identifier: AGPL-3.0-only

package dexcom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Scopes requested at consent. offline_access is the only scope Dexcom
// accepts; it gives the refresh token.
var Scopes = []string{"offline_access"}

// errNotConfigured reports a call that needs the client keys before they were
// set.
var errNotConfigured = errors.New("dexcom: client id and secret are not configured")

// AuthorizeURL returns the Dexcom login page URL of the configured region.
func (c *Connector) AuthorizeURL(state, redirect string) (string, error) {
	id, _ := c.credentials()
	if id == "" {
		return "", errNotConfigured
	}
	q := url.Values{}
	q.Set("client_id", id)
	q.Set("redirect_uri", redirect)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(Scopes, " "))
	q.Set("state", state)
	return c.base() + "/v3/oauth2/login?" + q.Encode(), nil
}

// Exchange trades an authorization code (valid one minute) for tokens.
func (c *Connector) Exchange(ctx context.Context, code, redirect string) (connector.Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("dexcom: exchange: %w", err)
	}
	return t, nil
}

// Refresh trades the refresh token for new tokens. Dexcom rotates refresh
// tokens: the old one stops working, so the returned Tokens must replace the
// stored ones. A refused refresh token gives connector.ErrNeedsReauth.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Refresh == "" {
		return connector.Tokens{}, fmt.Errorf("dexcom: refresh: no refresh token: %w", connector.ErrNeedsReauth)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	nt, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("dexcom: refresh: %w", err)
	}
	nt.ProviderUserID = t.ProviderUserID
	if nt.Refresh == "" {
		// Not expected (Dexcom always rotates), but never lose the grant.
		nt.Refresh = t.Refresh
	}
	return nt, nil
}

// tokenBody is the answer of the token endpoint.
type tokenBody struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
}

// requestToken posts form, with the client credentials in the body as Dexcom
// documents, to the token endpoint.
func (c *Connector) requestToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return connector.Tokens{}, errNotConfigured
	}
	form.Set("client_id", id)
	form.Set("client_secret", secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/v3/oauth2/token", strings.NewReader(form.Encode()))
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
	jsonErr := json.Unmarshal(body, &tb)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return connector.Tokens{}, connector.RateLimited(resp.Header, c.now())
	case tb.Error == "invalid_grant", resp.StatusCode == http.StatusUnauthorized:
		return connector.Tokens{}, connector.ErrNeedsReauth
	case resp.StatusCode >= 400:
		return connector.Tokens{}, fmt.Errorf("token endpoint HTTP %d", resp.StatusCode)
	case jsonErr != nil:
		return connector.Tokens{}, fmt.Errorf("token endpoint: decode answer: %w", jsonErr)
	case tb.AccessToken == "":
		return connector.Tokens{}, errors.New("token endpoint: answer holds no access token")
	}
	t := connector.Tokens{
		Access:  tb.AccessToken,
		Refresh: tb.RefreshToken,
		Scopes:  append([]string(nil), Scopes...),
	}
	if tb.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(tb.ExpiresIn) * time.Second).UTC()
	}
	return t, nil
}
