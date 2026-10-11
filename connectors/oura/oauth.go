// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Scopes requested at consent, as Oura's OpenAPI document names them
// (components.securitySchemes.OAuth2). "personal" only serves the Oura user id
// (personal_info); the connector stores nothing else of it. Daily stress and
// VO2 max have no scope of their own: they come with "daily". The other
// scopes Oura offers, "email" and "tag", are not asked for.
var Scopes = []string{"personal", "daily", "heartrate", "workout", "session", "spo2Daily"}

// errNotConfigured reports a call that needs the client keys before they were
// set.
var errNotConfigured = errors.New("oura: client id and secret are not configured")

// ErrClientRejected reports a token request Oura refused because of the
// application's own credentials (OAuth error invalid_client or
// unauthorized_client): OHS_OURA_CLIENT_ID or OHS_OURA_CLIENT_SECRET is wrong,
// or the application was disabled. It is the operator's to fix, so it never
// wraps connector.ErrNeedsReauth: every user would otherwise have to connect
// again after a mistyped secret.
var ErrClientRejected = errors.New("oura: the token endpoint refused the client credentials (check OHS_OURA_CLIENT_ID and OHS_OURA_CLIENT_SECRET)")

// AuthorizeURL returns the Oura consent page URL.
func (c *Connector) AuthorizeURL(state, redirect string) (string, error) {
	id, _ := c.credentials()
	if id == "" {
		return "", errNotConfigured
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", id)
	q.Set("scope", strings.Join(Scopes, " "))
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	return c.accountBase + "/oauth/authorize?" + q.Encode(), nil
}

// Exchange trades an authorization code for tokens, then asks personal_info
// for the Oura user id. A failed user id lookup leaves ProviderUserID empty
// rather than failing the connect: nothing in a pull needs it (only webhooks
// do). A code Oura refuses (expired, already used: invalid_grant) gives
// connector.ErrNeedsReauth: the user must go through consent again.
func (c *Connector) Exchange(ctx context.Context, code, redirect string) (connector.Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("oura: exchange: %w", err)
	}
	var info personalInfo
	if err := c.get(ctx, t.Access, "/v2/usercollection/personal_info", nil, &info); err != nil {
		if ctx.Err() != nil {
			return connector.Tokens{}, ctx.Err()
		}
		slog.Warn("oura: personal_info lookup failed, connecting without the Oura user id", "provider", Slug, "error", err)
	}
	t.ProviderUserID = info.ID
	return t, nil
}

// Refresh trades a refresh token for new tokens. Oura rotates refresh tokens
// (each is single use): the caller must store the returned pair, and a
// refresh token already spent is refused with invalid_grant. A grant Oura
// refuses for good (invalid_grant, or HTTP 401 without an OAuth error naming
// the client) gives connector.ErrNeedsReauth. Refused client credentials give
// ErrClientRejected, a rate limit a *connector.RateLimitError, and anything
// else (a 5xx, an unreadable answer) a plain error the caller retries later
// with the same refresh token.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Refresh == "" {
		return connector.Tokens{}, fmt.Errorf("oura: refresh: no refresh token: %w", connector.ErrNeedsReauth)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	nt, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("oura: refresh: %w", err)
	}
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh
	}
	nt.ProviderUserID = t.ProviderUserID
	if len(nt.Scopes) == 0 {
		nt.Scopes = t.Scopes
	}
	return nt, nil
}

// requestToken posts to the token endpoint with the client credentials in the
// form, as Oura documents it.
func (c *Connector) requestToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return connector.Tokens{}, errNotConfigured
	}
	form.Set("client_id", id)
	form.Set("client_secret", secret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return connector.Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return connector.Tokens{}, ctx.Err()
		}
		return connector.Tokens{}, fmt.Errorf("token request: %w", err)
	}
	body, err := readBody(resp.Body, 1<<20)
	resp.Body.Close()
	if err != nil {
		return connector.Tokens{}, err
	}

	var tb tokenBody
	jsonErr := json.Unmarshal(body, &tb)
	if jsonErr != nil {
		tb = tokenBody{}
	}
	// An OAuth error, whatever the HTTP status: {"error":"invalid_grant"}.
	switch tb.Error {
	case "invalid_grant":
		return connector.Tokens{}, connector.ErrNeedsReauth
	case "invalid_client", "unauthorized_client":
		return connector.Tokens{}, ErrClientRejected
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return connector.Tokens{}, connector.ErrNeedsReauth
	case resp.StatusCode == http.StatusTooManyRequests:
		return connector.Tokens{}, connector.RateLimited(resp.Header, c.now())
	case resp.StatusCode >= 400:
		if tb.Error != "" {
			return connector.Tokens{}, fmt.Errorf("token endpoint HTTP %d: %s", resp.StatusCode, clip(tb.Error, 64))
		}
		return connector.Tokens{}, fmt.Errorf("token endpoint HTTP %d", resp.StatusCode)
	case tb.Error != "":
		return connector.Tokens{}, fmt.Errorf("token endpoint: %s", clip(tb.Error, 64))
	}
	if jsonErr != nil {
		return connector.Tokens{}, errors.New("token endpoint: the answer is not JSON")
	}
	if tb.AccessToken == "" {
		return connector.Tokens{}, errors.New("token endpoint: answer holds no access token")
	}
	t := connector.Tokens{Access: tb.AccessToken, Refresh: tb.RefreshToken}
	if tb.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(tb.ExpiresIn) * time.Second).UTC()
	}
	t.Scopes = strings.Fields(tb.Scope)
	return t, nil
}
