// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

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

// Scopes requested at consent, comma separated as intervals.icu expects:
// read access to activities and to wellness records.
var Scopes = []string{"ACTIVITY:READ", "WELLNESS:READ"}

// errNotConfigured reports a call that needs the client keys before they were
// set.
var errNotConfigured = errors.New("intervals_icu: client id and secret are not configured")

// AuthorizeURL returns the intervals.icu consent page URL.
func (c *Connector) AuthorizeURL(state, redirect string) (string, error) {
	id, _ := c.credentials()
	if id == "" {
		return "", errNotConfigured
	}
	q := url.Values{}
	q.Set("client_id", id)
	q.Set("redirect_uri", redirect)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(Scopes, ","))
	q.Set("state", state)
	return c.oauthBase + "/oauth/authorize?" + q.Encode(), nil
}

// Exchange trades an authorization code for an access token. intervals.icu
// asks for the exchange within two minutes of consent. Its answer names the
// athlete, whose id becomes the provider user id webhook events carry.
func (c *Connector) Exchange(ctx context.Context, code, _ string) (connector.Tokens, error) {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return connector.Tokens{}, errNotConfigured
	}
	form := url.Values{}
	form.Set("client_id", id)
	form.Set("client_secret", secret)
	form.Set("code", code)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.oauthBase+"/api/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return connector.Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("intervals_icu: exchange: token request: %w", err)
	}
	body, err := readBody(resp.Body, 1<<20)
	resp.Body.Close()
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("intervals_icu: exchange: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return connector.Tokens{}, fmt.Errorf("intervals_icu: exchange: %w", connector.ErrNeedsReauth)
	case resp.StatusCode == http.StatusTooManyRequests:
		return connector.Tokens{}, connector.RateLimited(resp.Header, c.now())
	case resp.StatusCode >= 400:
		return connector.Tokens{}, fmt.Errorf("intervals_icu: exchange: token endpoint HTTP %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return connector.Tokens{}, fmt.Errorf("intervals_icu: exchange: decode answer: %w", err)
	}
	if tr.AccessToken == "" {
		return connector.Tokens{}, errors.New("intervals_icu: exchange: answer holds no access token")
	}
	if tr.Athlete == nil || tr.Athlete.ID == "" {
		return connector.Tokens{}, errors.New("intervals_icu: exchange: answer holds no athlete id")
	}
	t := connector.Tokens{
		Access:         tr.AccessToken,
		Refresh:        tr.RefreshToken,
		ProviderUserID: string(tr.Athlete.ID),
	}
	if tr.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC()
	}
	for _, s := range strings.FieldsFunc(tr.Scope, func(r rune) bool { return r == ',' || r == ' ' }) {
		t.Scopes = append(t.Scopes, s)
	}
	return t, nil
}

// Refresh cannot renew anything: intervals.icu issues no refresh token, and a
// token has no stated expiry, so the core only calls Refresh after the API
// refused the access token. That means the athlete revoked the app (or
// connected again elsewhere, which replaces the token): the user must connect
// again, and Refresh says so with connector.ErrNeedsReauth.
func (c *Connector) Refresh(_ context.Context, _ connector.Tokens) (connector.Tokens, error) {
	return connector.Tokens{}, fmt.Errorf("intervals_icu: refresh: no refresh token exists: %w", connector.ErrNeedsReauth)
}

// Revoke ends the grant at intervals.icu (DELETE /disconnect-app with the
// athlete's token), which also stops webhook delivery for the athlete. A
// token intervals.icu no longer knows (401) counts as revoked. Disconnecting
// calls it best effort.
func (c *Connector) Revoke(ctx context.Context, t connector.Tokens) error {
	if t.Access == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.apiBase+"/disconnect-app", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.Access)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("intervals_icu: revoke request: %w", err)
	}
	_, _ = readBody(resp.Body, 1<<20)
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return connector.RateLimited(resp.Header, c.now())
	case resp.StatusCode >= 400:
		return fmt.Errorf("intervals_icu: revoke HTTP %d", resp.StatusCode)
	}
	return nil
}
