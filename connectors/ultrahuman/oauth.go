// SPDX-License-Identifier: AGPL-3.0-only

package ultrahuman

import (
	"bytes"
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

// Scopes requested at consent, as the Partnership API documents them:
// ring_data (Ring AIR metrics), cgm_data (M1 CGM metrics) and profile (the
// user id and time zone of user_info).
var Scopes = []string{"ring_data", "cgm_data", "profile"}

// Paths under the API base.
const (
	tokenPath    = "/api/partners/oauth/token"
	revokePath   = "/api/partners/oauth/revoke"
	metricsPath  = "/api/partners/v1/user_data/metrics"
	userInfoPath = "/api/partners/v1/user_data/user_info"
)

// errNotConfigured reports a call that needs the client keys before they were
// set.
var errNotConfigured = errors.New("ultrahuman: client id and secret are not configured")

// ErrClientRejected reports a token request refused because of the
// application's own credentials (invalid_client or unauthorized_client). It
// is the operator's to fix, so it never wraps connector.ErrNeedsReauth.
var ErrClientRejected = errors.New("ultrahuman: the token endpoint refused the client credentials (check OHS_ULTRAHUMAN_CLIENT_ID and OHS_ULTRAHUMAN_CLIENT_SECRET)")

// AuthorizeURL returns the Ultrahuman consent page URL.
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
	return c.authBase + "/authorise?" + q.Encode(), nil
}

// Exchange trades an authorization code for tokens, then asks user_info for
// the Ultrahuman user id. A failed lookup leaves ProviderUserID empty rather
// than failing the connect: a pull does not need it. A code Ultrahuman
// refuses (invalid_grant) gives connector.ErrNeedsReauth.
func (c *Connector) Exchange(ctx context.Context, code, redirect string) (connector.Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("ultrahuman: exchange: %w", err)
	}
	id, err := c.userID(ctx, t.Access)
	if err != nil {
		if ctx.Err() != nil {
			return connector.Tokens{}, ctx.Err()
		}
		slog.Warn("ultrahuman: user_info lookup failed, connecting without the Ultrahuman user id", "provider", Slug, "error", err)
	}
	t.ProviderUserID = id
	return t, nil
}

// Refresh trades a refresh token for new tokens. Ultrahuman asks clients to
// use the newly issued refresh token next time; when the answer holds none,
// the old one is kept. A refused grant gives connector.ErrNeedsReauth.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Refresh == "" {
		return connector.Tokens{}, fmt.Errorf("ultrahuman: refresh: no refresh token: %w", connector.ErrNeedsReauth)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	nt, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("ultrahuman: refresh: %w", err)
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

// Revoke revokes the grant at Ultrahuman (POST /api/partners/oauth/revoke),
// the refresh token first, then the access token. Ultrahuman answers 200
// for a token revoked or already invalid.
func (c *Connector) Revoke(ctx context.Context, t connector.Tokens) error {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return errNotConfigured
	}
	var tokens []string
	for _, tok := range []string{t.Refresh, t.Access} {
		if tok != "" {
			tokens = append(tokens, tok)
		}
	}
	if len(tokens) == 0 {
		return errors.New("ultrahuman: revoke: no token")
	}
	for _, tok := range tokens {
		form := url.Values{}
		form.Set("token", tok)
		form.Set("client_id", id)
		form.Set("client_secret", secret)
		resp, _, err := c.postForm(ctx, revokePath, form)
		if err != nil {
			return fmt.Errorf("ultrahuman: revoke: %w", err)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			return connector.RateLimited(resp.Header, c.now())
		}
		if resp.StatusCode >= 300 {
			return fmt.Errorf("ultrahuman: revoke: HTTP %d", resp.StatusCode)
		}
	}
	return nil
}

// tokenBody is the answer of the token endpoint.
type tokenBody struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	CreatedAt    int64  `json:"created_at"`
	Error        string `json:"error"`
}

// requestToken posts to the token endpoint with the client credentials in the
// form, as Ultrahuman documents it.
func (c *Connector) requestToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	id, secret := c.credentials()
	if id == "" || secret == "" {
		return connector.Tokens{}, errNotConfigured
	}
	form.Set("client_id", id)
	form.Set("client_secret", secret)
	resp, body, err := c.postForm(ctx, tokenPath, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("token request: %w", err)
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
	t := connector.Tokens{Access: tb.AccessToken, Refresh: tb.RefreshToken, Scopes: strings.Fields(tb.Scope)}
	if tb.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(tb.ExpiresIn) * time.Second).UTC()
	}
	return t, nil
}

// userID asks user_info for the Ultrahuman user id. The answer's field names
// are not published; the lookup takes "id" or "user_id", at the top level or
// under "data".
func (c *Connector) userID(ctx context.Context, token string) (string, error) {
	body, status, err := c.get(ctx, token, userInfoPath, nil)
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", fmt.Errorf("user_info: HTTP %d without a body", status)
	}
	var doc map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return "", fmt.Errorf("user_info: %w", err)
	}
	if inner, ok := doc["data"]; ok {
		var d map[string]json.RawMessage
		if json.Unmarshal(inner, &d) == nil {
			if id := pickID(d); id != "" {
				return id, nil
			}
		}
	}
	if id := pickID(doc); id != "" {
		return id, nil
	}
	return "", errors.New("user_info: no user id in the answer")
}

// pickID returns the first of "id", "user_id" that holds a string or number.
func pickID(m map[string]json.RawMessage) string {
	for _, k := range []string{"id", "user_id"} {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
		var n json.Number
		if json.Unmarshal(raw, &n) == nil && n.String() != "" {
			return n.String()
		}
	}
	return ""
}

// clip shortens s to n bytes, for provider text put into an error.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
