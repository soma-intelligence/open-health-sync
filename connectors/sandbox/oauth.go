// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// AuthorizeURL returns the sandbox consent page: GET /inbound/sandbox on this
// instance, whose public base it reads from the callback URL the core hands
// it (<public>/oauth/sandbox/callback).
func (c *Connector) AuthorizeURL(state, redirect string) (string, error) {
	base, ok := strings.CutSuffix(redirect, callbackPathSuffix)
	if !ok || !validRedirect(redirect) {
		return "", fmt.Errorf("sandbox: redirect %q is not this instance's sandbox callback", redirect)
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", grantedScope)
	q.Set("state", state)
	return base + "/inbound/" + Slug + "?" + q.Encode(), nil
}

// AnswerChallenge serves the consent page on GET /inbound/sandbox when the
// request is an authorization request (response_type=code); any other GET is
// a plain reachability probe and gets an empty 200. The redirect_uri must
// point back at the host the page is served from, so the page cannot send a
// code elsewhere.
func (c *Connector) AnswerChallenge(r *http.Request) (string, []byte, error) {
	q := r.URL.Query()
	if q.Get("response_type") == "" {
		return "", nil, nil
	}
	u, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || !strings.EqualFold(u.Host, r.Host) {
		return "", nil, errors.New("sandbox: redirect_uri is not on this instance")
	}
	srv, _, _ := c.state()
	page, err := srv.consentPage(q)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox: %w", err)
	}
	return "text/html; charset=utf-8", page, nil
}

// Exchange trades a code from the consent page for tokens.
func (c *Connector) Exchange(ctx context.Context, code, redirect string) (connector.Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	t, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("sandbox: exchange: %w", err)
	}
	return t, nil
}

// Refresh trades the refresh token for a new pair. A refresh token the
// sandbox refuses (expired after 30 days, or signed with another secret)
// gives connector.ErrNeedsReauth.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	if t.Refresh == "" {
		return connector.Tokens{}, fmt.Errorf("sandbox: refresh: no refresh token: %w", connector.ErrNeedsReauth)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	nt, err := c.requestToken(ctx, form)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("sandbox: refresh: %w", err)
	}
	return nt, nil
}

// requestToken posts to the token endpoint.
func (c *Connector) requestToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	srv, base, httpc := c.state()
	form.Set("client_id", srv.clientID)
	form.Set("client_secret", srv.clientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return connector.Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpc.Do(req)
	if err != nil {
		return connector.Tokens{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return connector.Tokens{}, err
	}
	switch {
	case resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "invalid_grant"):
		return connector.Tokens{}, connector.ErrNeedsReauth
	case resp.StatusCode >= 400:
		return connector.Tokens{}, fmt.Errorf("token endpoint HTTP %d", resp.StatusCode)
	}
	var a tokenAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return connector.Tokens{}, fmt.Errorf("token endpoint: decode answer: %w", err)
	}
	if a.AccessToken == "" || a.User.ID == "" {
		return connector.Tokens{}, errors.New("token endpoint: answer holds no access token or user")
	}
	return connector.Tokens{
		Access:         a.AccessToken,
		Refresh:        a.RefreshToken,
		Expiry:         c.now().Add(time.Duration(a.ExpiresIn) * time.Second).UTC(),
		Scopes:         strings.Fields(a.Scope),
		ProviderUserID: a.User.ID,
	}, nil
}
