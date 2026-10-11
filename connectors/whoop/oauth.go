// SPDX-License-Identifier: AGPL-3.0-only

package whoop

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

// Scopes requested at consent. offline is what makes WHOOP hand out a
// refresh token; read:profile gives the user id webhooks name.
var Scopes = []string{
	"read:recovery", "read:cycles", "read:sleep", "read:workout",
	"read:profile", "read:body_measurement", "offline",
}

// errNotConfigured reports a call that needs the client keys before they were
// set.
var errNotConfigured = errors.New("whoop: client id and secret are not configured")

// AuthorizeURL returns the WHOOP consent page URL.
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
	return c.authBase + "/oauth2/auth?" + q.Encode(), nil
}

// Exchange trades an authorization code for tokens, then reads the WHOOP user
// id (webhooks name the user by it). A failed profile read does not fail the
// connection: the pulls work without it, and Refresh tries again.
func (c *Connector) Exchange(ctx context.Context, code, redirect string) (connector.Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("whoop: exchange: %w", err)
	}
	c.fillUserID(ctx, &t)
	return t, nil
}

// Refresh trades a refresh token for new tokens. WHOOP rotates refresh tokens
// and invalidates the previous access token: the caller must store the
// returned pair. A refresh token WHOOP rejects for good (invalid_grant, or a
// 401 that is not about the client) gives connector.ErrNeedsReauth.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Refresh == "" {
		return connector.Tokens{}, fmt.Errorf("whoop: refresh: no refresh token: %w", connector.ErrNeedsReauth)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	form.Set("scope", "offline")
	nt, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("whoop: refresh: %w", err)
	}
	nt.ProviderUserID = t.ProviderUserID
	if len(nt.Scopes) == 0 {
		nt.Scopes = t.Scopes
	}
	if nt.ProviderUserID == "" {
		c.fillUserID(ctx, &nt)
	}
	return nt, nil
}

// fillUserID reads the WHOOP user id into t, best effort.
func (c *Connector) fillUserID(ctx context.Context, t *connector.Tokens) {
	var p profile
	if err := c.get(ctx, t.Access, pathProfile, nil, &p); err != nil {
		slog.Warn("whoop: could not read the WHOOP user id, webhooks will not match this connection yet",
			"provider", Slug, "error", err)
		return
	}
	t.ProviderUserID = string(p.UserID)
}

// requestToken posts to the token endpoint with the client credentials in
// the form and decodes the answer.
func (c *Connector) requestToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return connector.Tokens{}, errNotConfigured
	}
	form.Set("client_id", id)
	form.Set("client_secret", secret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authBase+"/oauth2/token", strings.NewReader(form.Encode()))
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

	var ta tokenAnswer
	jsonErr := json.Unmarshal(body, &ta)
	switch {
	// An OAuth error, whatever the HTTP status: {"error":"invalid_grant"}.
	case jsonErr == nil && ta.Error == "invalid_grant":
		return connector.Tokens{}, connector.ErrNeedsReauth
	case resp.StatusCode == http.StatusTooManyRequests:
		return connector.Tokens{}, c.rateLimited(resp.Header)
	// A wrong client id or secret is the operator's problem, not the user's:
	// it must not send every connection to needs_reauth.
	case jsonErr == nil && ta.Error == "invalid_client":
		return connector.Tokens{}, errors.New("token endpoint: client id or secret rejected (invalid_client)")
	case resp.StatusCode == http.StatusUnauthorized:
		return connector.Tokens{}, connector.ErrNeedsReauth
	case resp.StatusCode >= 400:
		if jsonErr == nil && ta.Error != "" {
			return connector.Tokens{}, fmt.Errorf("token endpoint HTTP %d: %s", resp.StatusCode, ta.Error)
		}
		return connector.Tokens{}, fmt.Errorf("token endpoint HTTP %d", resp.StatusCode)
	case jsonErr != nil:
		return connector.Tokens{}, fmt.Errorf("token endpoint: decode answer: %w", jsonErr)
	case ta.AccessToken == "":
		return connector.Tokens{}, errors.New("token endpoint: answer holds no access token")
	}

	t := connector.Tokens{Access: ta.AccessToken, Refresh: ta.RefreshToken}
	if ta.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(ta.ExpiresIn) * time.Second).UTC()
	}
	t.Scopes = strings.Fields(ta.Scope)
	return t, nil
}
