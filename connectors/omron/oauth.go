// SPDX-License-Identifier: AGPL-3.0-only

package omron

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

// Scopes requested at consent: the two measurement types this connector
// maps, the OpenID scope that gives the user id (the id_token's sub), and
// offline_access for the refresh token.
var Scopes = []string{"bloodpressure", "weight", "openid", "offline_access"}

// errNotConfigured reports a call that needs the client keys before they were
// set.
var errNotConfigured = errors.New("omron: client id and secret are not configured")

// AuthorizeURL returns the OMRON consent page URL.
func (c *Connector) AuthorizeURL(state, redirect string) (string, error) {
	id, _ := c.credentials()
	if id == "" {
		return "", errNotConfigured
	}
	q := url.Values{}
	q.Set("client_id", id)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(Scopes, " "))
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	return c.endpoints().Authorize + "?" + q.Encode(), nil
}

// Exchange trades an authorization code (single use, valid about 10
// minutes) for tokens. The user id is the sub claim of the id_token.
func (c *Connector) Exchange(ctx context.Context, code, redirect string) (connector.Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("scope", strings.Join(Scopes, " "))
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("omron: exchange: %w", err)
	}
	return t, nil
}

// Refresh trades the refresh token for new tokens. OMRON returns a new
// refresh token each time, which replaces the stored one. A refused refresh
// token gives connector.ErrNeedsReauth.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Refresh == "" {
		return connector.Tokens{}, fmt.Errorf("omron: refresh: no refresh token: %w", connector.ErrNeedsReauth)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	nt, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("omron: refresh: %w", err)
	}
	if nt.ProviderUserID == "" {
		nt.ProviderUserID = t.ProviderUserID
	}
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh
	}
	return nt, nil
}

// tokenBody is the answer of the token endpoint. The documentation shows
// both the OAuth spelling (expires_in) and camel case (expiresIn).
type tokenBody struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresInAlt int64  `json:"expiresIn"`
	Error        string `json:"error"`
}

// requestToken posts form, with the client credentials in the body, to the
// token endpoint.
func (c *Connector) requestToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return connector.Tokens{}, errNotConfigured
	}
	form.Set("client_id", id)
	form.Set("client_secret", secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoints().Token, strings.NewReader(form.Encode()))
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
		Access:         tb.AccessToken,
		Refresh:        tb.RefreshToken,
		Scopes:         append([]string(nil), Scopes...),
		ProviderUserID: subject(tb.IDToken),
	}
	exp := tb.ExpiresIn
	if exp == 0 {
		exp = tb.ExpiresInAlt
	}
	if exp > 0 {
		t.Expiry = c.now().Add(time.Duration(exp) * time.Second).UTC()
	}
	return t, nil
}

// subject reads the sub claim of a JWT without checking its signature: it
// came straight from the token endpoint over TLS, and the id is only used to
// name the OMRON user, never to authorize anything. "" when there is none.
func subject(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Sub
}
