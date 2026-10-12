// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"bytes"
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

// Scopes requested at consent. AccessLink has a single scope.
var Scopes = []string{"accesslink.read_all"}

// errNotConfigured reports a call that needs the client keys before they were
// set.
var errNotConfigured = errors.New("polar: client id and secret are not configured")

// AuthorizeURL returns the Polar Flow consent page URL.
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
	return c.authBase + "/oauth2/authorization?" + q.Encode(), nil
}

// Exchange trades an authorization code for a token, then registers the
// user with this client (POST /v3/users), which AccessLink requires before
// any data endpoint answers. A user already registered (409) is fine. A
// registration refused because the user did not accept Polar's mandatory
// consents fails the exchange: the user has to connect again.
func (c *Connector) Exchange(ctx context.Context, code, redirect string) (connector.Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	if redirect != "" {
		form.Set("redirect_uri", redirect)
	}
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("polar: exchange: %w", err)
	}
	if err := c.register(ctx, t); err != nil {
		return connector.Tokens{}, fmt.Errorf("polar: exchange: %w", err)
	}
	return t, nil
}

// Refresh keeps the grant alive. Polar issues no refresh token and its
// access tokens last for years, so there is nothing to trade: Refresh checks
// the token against AccessLink (GET /v3/users/{user-id}) and returns the
// same tokens when it still works. A token Polar rejects (401 or 403), a
// user no longer registered, or a token past its expiry gives
// connector.ErrNeedsReauth.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Access == "" || t.ProviderUserID == "" {
		return connector.Tokens{}, fmt.Errorf("polar: refresh: no access token or user id: %w", connector.ErrNeedsReauth)
	}
	if !t.Expiry.IsZero() && !c.now().Before(t.Expiry) {
		return connector.Tokens{}, fmt.Errorf("polar: refresh: token expired, Polar issues no refresh token: %w", connector.ErrNeedsReauth)
	}
	body, status, err := c.get(ctx, t.Access, "/v3/users/"+url.PathEscape(t.ProviderUserID), nil)
	if isUnauthorized(err) {
		return connector.Tokens{}, fmt.Errorf("polar: refresh: %w: %w", err, connector.ErrNeedsReauth)
	}
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("polar: refresh: %w", err)
	}
	if status == http.StatusNoContent || status == http.StatusNotFound || len(body) == 0 {
		return connector.Tokens{}, fmt.Errorf("polar: refresh: user not registered: %w", connector.ErrNeedsReauth)
	}
	return t, nil
}

// Revoke de-registers the user from this client (DELETE /v3/users/{user-id}),
// which also revokes the access token at Polar. A user already gone (404) or
// a token already refused (401) counts as done.
func (c *Connector) Revoke(ctx context.Context, t connector.Tokens) error {
	if t.Access == "" || t.ProviderUserID == "" {
		return errors.New("polar: revoke: no access token or user id")
	}
	status, err := c.send(ctx, http.MethodDelete, t.Access, "/v3/users/"+url.PathEscape(t.ProviderUserID), nil)
	if err != nil {
		return fmt.Errorf("polar: revoke: %w", err)
	}
	switch {
	case status < 300, status == http.StatusNotFound, status == http.StatusUnauthorized:
		return nil
	default:
		return fmt.Errorf("polar: revoke: HTTP %d", status)
	}
}

// AfterConnect registers the user of conn with this client. Exchange already
// does it; AfterConnect is idempotent (409 is success) and is there for a
// core post-connect hook.
func (c *Connector) AfterConnect(ctx context.Context, conn connector.Connection) error {
	return c.register(ctx, conn.Tokens)
}

// MemberID is the partner identifier this connector registers a Polar user
// under: stable for one Polar user, so a reconnect lands on the same
// registration.
func MemberID(providerUserID string) string { return "ohs-" + providerUserID }

// register sends POST /v3/users. 200 and 201 are a new registration, 409 an
// existing one; 403 means the user has not accepted Polar's mandatory
// consents.
func (c *Connector) register(ctx context.Context, t connector.Tokens) error {
	if t.Access == "" || t.ProviderUserID == "" {
		return errors.New("register user: no access token or user id")
	}
	payload, err := json.Marshal(map[string]string{"member-id": MemberID(t.ProviderUserID)})
	if err != nil {
		return err
	}
	status, err := c.send(ctx, http.MethodPost, t.Access, "/v3/users", payload)
	if err != nil {
		return fmt.Errorf("register user: %w", err)
	}
	switch status {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent, http.StatusConflict:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("register user: %w: %w", errUnauthorized, connector.ErrNeedsReauth)
	case http.StatusForbidden:
		return fmt.Errorf("register user: mandatory Polar consents not accepted: %w", connector.ErrNeedsReauth)
	default:
		return fmt.Errorf("register user: HTTP %d", status)
	}
}

// tokenBody is the answer of the token endpoint.
type tokenBody struct {
	AccessToken string      `json:"access_token"`
	TokenType   string      `json:"token_type"`
	ExpiresIn   int64       `json:"expires_in"`
	XUserID     json.Number `json:"x_user_id"`
	Error       string      `json:"error"`
}

// requestToken posts to the token endpoint with HTTP Basic client
// authentication.
func (c *Connector) requestToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return connector.Tokens{}, errNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenBase+"/v2/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return connector.Tokens{}, err
	}
	req.SetBasicAuth(id, secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json;charset=UTF-8")
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
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	jsonErr := dec.Decode(&tb)
	if jsonErr == nil && strings.Contains(tb.Error, "invalid_grant") {
		return connector.Tokens{}, connector.ErrNeedsReauth
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return connector.Tokens{}, rateLimited(resp.Header, c.now())
	}
	if resp.StatusCode == http.StatusUnauthorized || (resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "invalid_grant")) {
		return connector.Tokens{}, connector.ErrNeedsReauth
	}
	if resp.StatusCode >= 400 {
		return connector.Tokens{}, fmt.Errorf("token endpoint HTTP %d", resp.StatusCode)
	}
	if jsonErr != nil {
		return connector.Tokens{}, fmt.Errorf("token endpoint: decode answer: %w", jsonErr)
	}
	if tb.AccessToken == "" {
		return connector.Tokens{}, errors.New("token endpoint: answer holds no access token")
	}
	if tb.XUserID.String() == "" {
		return connector.Tokens{}, errors.New("token endpoint: answer holds no x_user_id")
	}
	t := connector.Tokens{
		Access:         tb.AccessToken,
		ProviderUserID: tb.XUserID.String(),
		Scopes:         append([]string(nil), Scopes...),
	}
	if tb.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(tb.ExpiresIn) * time.Second).UTC()
	}
	return t, nil
}
