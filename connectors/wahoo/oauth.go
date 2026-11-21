// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

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

// Scopes requested at consent, space separated: the profile (Wahoo answers
// 403 to everything without user_read), workouts, and offline_data, which
// webhooks need.
var Scopes = []string{"user_read", "workouts_read", "offline_data"}

// errNotConfigured reports a call that needs the client keys before they were
// set.
var errNotConfigured = errors.New("wahoo: client id and secret are not configured")

// AuthorizeURL returns the Wahoo consent page URL.
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
	return c.base + "/oauth/authorize?" + q.Encode(), nil
}

// Exchange trades an authorization code for tokens, then reads GET /v1/user:
// the token answer does not name the user, and webhooks name them by that id.
func (c *Connector) Exchange(ctx context.Context, code, redirect string) (connector.Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("wahoo: exchange: %w", err)
	}
	body, err := c.get(ctx, t.Access, userPath, nil)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("wahoo: exchange: read user: %w", err)
	}
	var u User
	if err := json.Unmarshal(body, &u); err != nil {
		return connector.Tokens{}, fmt.Errorf("wahoo: exchange: decode user: %w", err)
	}
	if u.ID.String() == "" {
		return connector.Tokens{}, errors.New("wahoo: exchange: the user answer holds no id")
	}
	t.ProviderUserID = u.ID.String()
	return t, nil
}

// Refresh trades a refresh token for a new pair. Wahoo revokes the old pair
// once the new access token is used, and caps a user at 10 unrevoked tokens
// per application, so the core refreshes only near expiry. A refresh token
// Wahoo rejects gives connector.ErrNeedsReauth.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Refresh == "" {
		return connector.Tokens{}, fmt.Errorf("wahoo: refresh: no refresh token: %w", connector.ErrNeedsReauth)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	nt, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("wahoo: refresh: %w", err)
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

// Revoke ends the grant (DELETE /v1/permissions with the user's token). A
// token Wahoo no longer knows (401) counts as revoked. Disconnecting calls it
// best effort.
func (c *Connector) Revoke(ctx context.Context, t connector.Tokens) error {
	if t.Access == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/v1/permissions", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.Access)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("wahoo: revoke request: %w", err)
	}
	_, _ = readBody(resp.Body, 1<<20)
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return rateLimited(resp.Header, c.now())
	case resp.StatusCode >= 400:
		return fmt.Errorf("wahoo: revoke HTTP %d", resp.StatusCode)
	}
	return nil
}

// requestToken posts to the token endpoint with the client credentials, form
// encoded in the body.
func (c *Connector) requestToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return connector.Tokens{}, errNotConfigured
	}
	form.Set("client_id", id)
	form.Set("client_secret", secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/oauth/token", strings.NewReader(form.Encode()))
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
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return connector.Tokens{}, connector.ErrNeedsReauth
	case resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "invalid_grant"):
		return connector.Tokens{}, connector.ErrNeedsReauth
	case resp.StatusCode == http.StatusTooManyRequests:
		return connector.Tokens{}, rateLimited(resp.Header, c.now())
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
	t := connector.Tokens{Access: tr.AccessToken, Refresh: tr.RefreshToken}
	if tr.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC()
	}
	t.Scopes = strings.Fields(tr.Scope)
	return t, nil
}
