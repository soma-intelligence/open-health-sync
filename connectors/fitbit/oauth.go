// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

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

// identityPath is users.getIdentity for the token's user.
const identityPath = "/v4/users/me/identity"

// errNotConfigured reports a call that needs the client keys before they were
// set.
var errNotConfigured = errors.New("fitbit: client id and secret are not configured")

// ErrClientRejected reports a token request Google refused because of the
// OAuth client itself (invalid_client, unauthorized_client): the operator
// must fix OHS_FITBIT_CLIENT_ID or OHS_FITBIT_CLIENT_SECRET. It never wraps
// connector.ErrNeedsReauth.
var ErrClientRejected = errors.New("fitbit: Google refused the OAuth client (check OHS_FITBIT_CLIENT_ID and OHS_FITBIT_CLIENT_SECRET)")

// ErrNoScopeGranted reports a consent that granted none of the read scopes:
// the connection could pull nothing.
var ErrNoScopeGranted = errors.New("fitbit: the user granted none of the Google Health read scopes")

// tokenBody is Google's token endpoint answer.
type tokenBody struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
}

// identity is the users.getIdentity answer.
type identity struct {
	Name         string `json:"name"`
	HealthUserID string `json:"healthUserId"`
	LegacyUserID string `json:"legacyUserId"`
}

// AuthorizeURL returns the Google consent page URL: offline access for a
// refresh token, prompt=consent so a reconnect gets a new refresh token, and
// incremental authorization.
func (c *Connector) AuthorizeURL(state, redirect string) (string, error) {
	id, _ := c.credentials()
	if id == "" {
		return "", errNotConfigured
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", id)
	q.Set("redirect_uri", redirect)
	q.Set("scope", strings.Join(Scopes, " "))
	q.Set("state", state)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("include_granted_scopes", "true")
	return c.authURL + "?" + q.Encode(), nil
}

// Exchange trades an authorization code for tokens, then reads the user's
// Google Health user id (users.getIdentity), which webhooks name users by.
//
// The scopes the user granted come from the token answer; a data type whose
// scope was refused is skipped by Fetch. A consent granting no read scope at
// all gives ErrNoScopeGranted. An account not linked to Google Health gives
// ErrAccountNotLinked, after revoking the grant just issued. Any other
// identity failure connects without the id (pulls work; webhooks for that
// user are ignored until a token refresh reads it).
func (c *Connector) Exchange(ctx context.Context, code, redirect string) (connector.Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("fitbit: exchange: %w", err)
	}
	if len(t.Scopes) > 0 && len(grantedTypes(t.Scopes)) == 0 {
		c.revokeQuietly(ctx, t)
		return connector.Tokens{}, ErrNoScopeGranted
	}
	id, err := c.identity(ctx, t.Access)
	switch {
	case err == nil:
		t.ProviderUserID = id
	case errors.Is(err, ErrAccountNotLinked):
		c.revokeQuietly(ctx, t)
		return connector.Tokens{}, ErrAccountNotLinked
	case ctx.Err() != nil:
		return connector.Tokens{}, ctx.Err()
	default:
		slog.Warn("fitbit: identity lookup failed, connecting without the Google Health user id", "provider", Slug, "error", err)
	}
	return t, nil
}

// identity returns the healthUserId of the token's user.
func (c *Connector) identity(ctx context.Context, token string) (string, error) {
	var id identity
	if err := c.get(ctx, token, identityPath, nil, &id); err != nil {
		return "", err
	}
	if id.HealthUserID == "" {
		return "", errors.New("fitbit: identity answer holds no healthUserId")
	}
	return id.HealthUserID, nil
}

// Refresh trades the refresh token for a new access token. Google keeps the
// refresh token (an answer without one keeps the stored one). invalid_grant
// (a revoked grant, or a Testing-mode refresh token past its 7 days) gives
// connector.ErrNeedsReauth. A connection still missing its Google Health user
// id gets it read here, best effort.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Refresh == "" {
		return connector.Tokens{}, fmt.Errorf("fitbit: refresh: no refresh token: %w", connector.ErrNeedsReauth)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	nt, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("fitbit: refresh: %w", err)
	}
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh
	}
	if len(nt.Scopes) == 0 {
		nt.Scopes = t.Scopes
	}
	nt.ProviderUserID = t.ProviderUserID
	if nt.ProviderUserID == "" {
		if id, err := c.identity(ctx, nt.Access); err == nil {
			nt.ProviderUserID = id
		}
	}
	return nt, nil
}

// requestToken posts to the token endpoint with the client credentials in the
// form (client_secret_post, which Google accepts).
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
	switch tb.Error {
	case "invalid_grant":
		return connector.Tokens{}, connector.ErrNeedsReauth
	case "invalid_client", "unauthorized_client":
		return connector.Tokens{}, ErrClientRejected
	}
	switch code := resp.StatusCode; {
	case code == http.StatusUnauthorized:
		return connector.Tokens{}, connector.ErrNeedsReauth
	case code == http.StatusTooManyRequests || code == http.StatusGatewayTimeout:
		return connector.Tokens{}, connector.RateLimited(resp.Header, c.now())
	case code >= 400:
		if tb.Error != "" {
			return connector.Tokens{}, fmt.Errorf("token endpoint HTTP %d: %s", code, clip(tb.Error, 64))
		}
		return connector.Tokens{}, fmt.Errorf("token endpoint HTTP %d", code)
	case tb.Error != "":
		return connector.Tokens{}, fmt.Errorf("token endpoint: %s", clip(tb.Error, 64))
	}
	if jsonErr != nil {
		return connector.Tokens{}, errors.New("token endpoint: the answer is not JSON")
	}
	if tb.AccessToken == "" {
		return connector.Tokens{}, errors.New("token endpoint: answer holds no access token")
	}
	t := connector.Tokens{Access: tb.AccessToken, Refresh: tb.RefreshToken, Scopes: strings.Fields(tb.Scope)}
	if tb.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(tb.ExpiresIn) * time.Second).UTC()
	}
	return t, nil
}

// Revoke revokes the grant at Google (POST oauth2.googleapis.com/revoke),
// with the refresh token when there is one, which ends the whole grant. A
// token Google no longer knows (HTTP 400 invalid_token) counts as revoked.
func (c *Connector) Revoke(ctx context.Context, t connector.Tokens) error {
	tok := t.Refresh
	if tok == "" {
		tok = t.Access
	}
	if tok == "" {
		return errors.New("fitbit: revoke: no token")
	}
	form := url.Values{}
	form.Set("token", tok)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("fitbit: revoke: %w", err)
	}
	body, _ := readBody(resp.Body, 64<<10)
	resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	var tb tokenBody
	if resp.StatusCode == http.StatusBadRequest && json.Unmarshal(body, &tb) == nil && tb.Error == "invalid_token" {
		return nil
	}
	return fmt.Errorf("fitbit: revoke: HTTP %d", resp.StatusCode)
}

// revokeQuietly revokes a grant the connect is about to refuse, so it does
// not stay valid at Google; a failure is only logged.
func (c *Connector) revokeQuietly(ctx context.Context, t connector.Tokens) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := c.Revoke(rctx, t); err != nil {
		slog.Warn("fitbit: revoking a refused grant failed", "provider", Slug, "error", err)
	}
}

// grantedTypes returns the data types whose scope is in scopes. An empty
// scopes list (unknown, as for a connection stored without them) grants
// every type.
func grantedTypes(scopes []string) []string {
	if len(scopes) == 0 {
		return AllDataTypes
	}
	granted := map[string]bool{}
	for _, s := range scopes {
		granted[s] = true
	}
	var out []string
	for _, d := range dataTypes {
		if granted[d.Scope] {
			out = append(out, d.ID)
		}
	}
	return out
}
