// SPDX-License-Identifier: AGPL-3.0-only

package strava

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

// Scopes requested at consent: read (required by Strava for any app) and
// activity:read_all (every activity, including "Only You" ones and privacy
// zone data). activity:read would leave private activities out.
var Scopes = []string{"read", "activity:read_all"}

// errNotConfigured reports a call that needs the client keys before they were
// set.
var errNotConfigured = errors.New("strava: client id and secret are not configured")

// AuthorizeURL returns the Strava consent page URL. approval_prompt=auto skips
// the prompt for an athlete who already authorized the same scopes.
func (c *Connector) AuthorizeURL(state, redirect string) (string, error) {
	id, _ := c.credentials()
	if id == "" {
		return "", errNotConfigured
	}
	q := url.Values{}
	q.Set("client_id", id)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirect)
	q.Set("approval_prompt", "auto")
	q.Set("scope", strings.Join(Scopes, ","))
	q.Set("state", state)
	return c.oauthBase + "/oauth/authorize?" + q.Encode(), nil
}

// Exchange trades an authorization code for tokens. Strava's answer carries
// the athlete, whose id becomes the provider user id push events name.
func (c *Connector) Exchange(ctx context.Context, code, _ string) (connector.Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("strava: exchange: %w", err)
	}
	if t.ProviderUserID == "" {
		return connector.Tokens{}, errors.New("strava: exchange: answer holds no athlete id")
	}
	return t, nil
}

// Refresh trades a refresh token for new tokens. Strava may rotate the
// refresh token and invalidates the old one at once: the caller must store
// the returned pair. A refresh token Strava rejects (HTTP 401, or 400 naming
// the refresh token invalid, as after a deauthorization) gives
// connector.ErrNeedsReauth.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Refresh == "" {
		return connector.Tokens{}, fmt.Errorf("strava: refresh: no refresh token: %w", connector.ErrNeedsReauth)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	nt, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("strava: refresh: %w", err)
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

// Revoke ends the grant at Strava (POST /oauth/revoke, client credentials in
// HTTP Basic). Revoking one token of the pair revokes the other; Strava
// answers 200 even for a token it no longer knows. Disconnecting calls it
// best effort.
func (c *Connector) Revoke(ctx context.Context, t connector.Tokens) error {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return errNotConfigured
	}
	form := url.Values{}
	switch {
	case t.Refresh != "":
		form.Set("token", t.Refresh)
		form.Set("token_type_hint", "refresh_token")
	case t.Access != "":
		form.Set("token", t.Access)
		form.Set("token_type_hint", "access_token")
	default:
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.oauthBase+"/oauth/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(id, secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("strava: revoke request: %w", err)
	}
	_, _ = readBody(resp.Body, 1<<20)
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return rateLimited(resp.Header, c.now())
	case resp.StatusCode >= 400:
		return fmt.Errorf("strava: revoke HTTP %d", resp.StatusCode)
	}
	return nil
}

// requestToken posts to the token endpoint with the client credentials.
func (c *Connector) requestToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return connector.Tokens{}, errNotConfigured
	}
	form.Set("client_id", id)
	form.Set("client_secret", secret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.oauthBase+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return connector.Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	case resp.StatusCode == http.StatusBadRequest && grantRejected(body):
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
	t := connector.Tokens{
		Access:  tr.AccessToken,
		Refresh: tr.RefreshToken,
	}
	if tr.Athlete != nil {
		t.ProviderUserID = string(tr.Athlete.ID)
	}
	switch {
	case tr.ExpiresAt > 0:
		t.Expiry = time.Unix(tr.ExpiresAt, 0).UTC()
	case tr.ExpiresIn > 0:
		t.Expiry = c.now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC()
	}
	for _, s := range strings.FieldsFunc(tr.Scope, func(r rune) bool { return r == ',' || r == ' ' }) {
		t.Scopes = append(t.Scopes, s)
	}
	return t, nil
}

// grantRejected reports a 400 answer that names the refresh token or the
// authorization code invalid: the grant is gone, not the request malformed.
// An invalid client id or secret is an operator error and stays a plain one.
func grantRejected(body []byte) bool {
	var e apiError
	if json.Unmarshal(body, &e) != nil {
		return strings.Contains(string(body), "invalid_grant")
	}
	for _, f := range e.Errors {
		switch f.Field {
		case "refresh_token", "code":
			return true
		}
		switch f.Resource {
		case "RefreshToken", "AuthorizationCode":
			return true
		}
	}
	return false
}
