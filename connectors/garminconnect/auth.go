// SPDX-License-Identifier: AGPL-3.0-only

package garminconnect

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

// Sign-in.
//
// Garmin's Connect app signs in over SSO: POST the email and password to the
// mobile login endpoint, which answers a CAS service ticket, then exchange
// the ticket for an OAuth2 bearer at the DI token endpoint. The protocol
// parameters (the SSO client id, the service URL) are the values Garmin's
// endpoints require, not a disguise; see python-garminconnect. Garmin issues
// an OAuth2 refresh token, but it is short lived and account dependent, so
// this connector keeps the account credentials the way the Eight Sleep
// connector does: the email and password go in Tokens.Refresh, which the
// core seals with OHS_ENCRYPTION_KEY, and Refresh signs in again with them.

// Protocol constants required by Garmin's SSO and token endpoints.
const (
	// ssoClientID selects the Connect mobile SSO client configuration.
	ssoClientID = "GCM_IOS_DARK"
	// ssoServiceURL is the service a ticket is issued for; the ticket
	// exchange must name the same value.
	ssoServiceURL = "https://mobile.integration.garmin.com/gcm/ios"
	// diGrantType is the service-ticket grant of the DI token endpoint.
	diGrantType = "https://connectapi.garmin.com/di-oauth2-service/oauth/grant/service_ticket"
)

// diClientIDs are the DI OAuth2 client ids tried in order at the token
// exchange; Garmin rotates which one a service ticket is accepted for.
var diClientIDs = []string{
	"GARMIN_CONNECT_MOBILE_ANDROID_DI_2025Q2",
	"GARMIN_CONNECT_MOBILE_ANDROID_DI_2024Q4",
	"GARMIN_CONNECT_MOBILE_ANDROID_DI",
	"GARMIN_CONNECT_MOBILE_IOS_DI",
}

// credentialPrefix marks the credential blob stored as the refresh token, so
// a value of any other shape is refused rather than sent to Garmin.
const credentialPrefix = "ohs-garmin-connect-v1:"

// ErrNoConsentPage is returned by AuthorizeURL and Exchange: Garmin Connect
// has no consent page for third parties. The core sends the user to its own
// sign-in page and calls SignIn instead.
var ErrNoConsentPage = errors.New("garmin_connect: no OAuth consent page; users sign in with their Garmin Connect email and password (SignIn)")

// ErrBlocked reports that Garmin refused the request as a non-browser client
// (an anti-bot challenge), rather than refusing the credentials. It is not a
// reason to re-ask for the password.
var ErrBlocked = errors.New("garmin_connect: Garmin refused the request (anti-bot challenge); this unofficial sign-in may be blocked")

// credentials is what a connection keeps to sign in again.
type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (k credentials) encode() (string, error) {
	b, err := json.Marshal(k)
	if err != nil {
		return "", err
	}
	return credentialPrefix + string(b), nil
}

func decodeCredentials(s string) (credentials, bool) {
	var k credentials
	rest, ok := strings.CutPrefix(s, credentialPrefix)
	if !ok || json.Unmarshal([]byte(rest), &k) != nil || k.Email == "" || k.Password == "" {
		return credentials{}, false
	}
	return k, true
}

// loginAnswer is the SSO mobile login answer.
type loginAnswer struct {
	ServiceTicketID string `json:"serviceTicketId"`
	ResponseStatus  struct {
		Type string `json:"type"`
	} `json:"responseStatus"`
}

// tokenAnswer is the DI token endpoint answer.
type tokenAnswer struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresIn    json.Number `json:"expires_in"`
}

// AuthorizeURL fails: Garmin Connect has no consent page (see ErrNoConsentPage).
func (c *Connector) AuthorizeURL(string, string) (string, error) { return "", ErrNoConsentPage }

// Exchange fails: there is no authorization code to exchange.
func (c *Connector) Exchange(context.Context, string, string) (connector.Tokens, error) {
	return connector.Tokens{}, ErrNoConsentPage
}

// SignIn trades the account email and password for a bearer token. Refused
// credentials give connector.ErrBadCredentials; an anti-bot block gives
// ErrBlocked; a rate limit a *connector.RateLimitError.
func (c *Connector) SignIn(ctx context.Context, email, password string) (connector.Tokens, error) {
	return c.signIn(ctx, credentials{Email: email, Password: password})
}

// Refresh signs in again with the credentials the connection keeps. A
// password Garmin no longer accepts (changed, or the account closed) gives an
// error wrapping connector.ErrNeedsReauth.
func (c *Connector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	k, ok := decodeCredentials(t.Refresh)
	if !ok {
		return connector.Tokens{}, fmt.Errorf("garmin_connect: the connection keeps no credentials: %w", connector.ErrNeedsReauth)
	}
	fresh, err := c.signIn(ctx, k)
	if errors.Is(err, connector.ErrBadCredentials) {
		return connector.Tokens{}, fmt.Errorf("garmin_connect: %w: %w", err, connector.ErrNeedsReauth)
	}
	if fresh.ProviderUserID == "" {
		fresh.ProviderUserID = t.ProviderUserID
	}
	return fresh, err
}

func (c *Connector) signIn(ctx context.Context, k credentials) (connector.Tokens, error) {
	ticket, err := c.ssoLogin(ctx, k)
	if err != nil {
		return connector.Tokens{}, err
	}
	a, err := c.exchangeTicket(ctx, ticket)
	if err != nil {
		return connector.Tokens{}, err
	}
	refresh, err := k.encode()
	if err != nil {
		return connector.Tokens{}, err
	}
	t := connector.Tokens{
		Access:         a.AccessToken,
		Refresh:        refresh,
		ProviderUserID: jwtSubject(a.AccessToken),
	}
	if secs, err := a.ExpiresIn.Float64(); err == nil && secs > 0 {
		t.Expiry = c.now().Add(time.Duration(secs) * time.Second)
	}
	return t, nil
}

// ssoLogin posts the credentials to the mobile login endpoint and returns the
// service ticket of a SUCCESSFUL answer.
func (c *Connector) ssoLogin(ctx context.Context, k credentials) (string, error) {
	q := url.Values{}
	q.Set("clientId", ssoClientID)
	q.Set("locale", "en-US")
	q.Set("service", ssoServiceURL)
	endpoint := c.ssoBase + "/mobile/api/login?" + q.Encode()

	body, err := json.Marshal(map[string]any{
		"username":     k.Email,
		"password":     k.Password,
		"rememberMe":   true,
		"captchaToken": "",
	})
	if err != nil {
		return "", err
	}
	resp, raw, err := c.do(ctx, http.MethodPost, endpoint, "", "application/json", body)
	if err != nil {
		return "", fmt.Errorf("garmin_connect: sign in: %w", err)
	}
	switch s := resp.StatusCode; {
	case s == http.StatusUnauthorized, s == http.StatusBadRequest:
		// The body is not logged: it may echo the email.
		return "", fmt.Errorf("garmin_connect: sign in: HTTP %d: %w", s, connector.ErrBadCredentials)
	case s == http.StatusForbidden:
		return "", fmt.Errorf("garmin_connect: sign in: HTTP 403: %w", ErrBlocked)
	case s == http.StatusTooManyRequests:
		return "", connector.RateLimited(resp.Header, c.now())
	case s >= 300:
		return "", fmt.Errorf("garmin_connect: sign in: HTTP %d", s)
	}
	var a loginAnswer
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("garmin_connect: sign in: decode answer: %w", err)
	}
	switch a.ResponseStatus.Type {
	case "SUCCESSFUL":
		if a.ServiceTicketID == "" {
			return "", errors.New("garmin_connect: sign in: SUCCESSFUL without a service ticket")
		}
		return a.ServiceTicketID, nil
	case "INVALID_USERNAME_PASSWORD":
		return "", fmt.Errorf("garmin_connect: sign in: %w", connector.ErrBadCredentials)
	case "MFA_REQUIRED":
		return "", errors.New("garmin_connect: sign in: this account needs two-factor authentication, which this connector does not support")
	case "CAPTCHA_REQUIRED":
		return "", fmt.Errorf("garmin_connect: sign in: a captcha was required: %w", ErrBlocked)
	default:
		return "", fmt.Errorf("garmin_connect: sign in: unexpected status %q", a.ResponseStatus.Type)
	}
}

// exchangeTicket trades the service ticket for a bearer, trying each DI
// client id in turn.
func (c *Connector) exchangeTicket(ctx context.Context, ticket string) (tokenAnswer, error) {
	var lastStatus int
	for _, id := range diClientIDs {
		form := url.Values{}
		form.Set("client_id", id)
		form.Set("service_ticket", ticket)
		form.Set("grant_type", diGrantType)
		form.Set("service_url", ssoServiceURL)
		resp, raw, err := c.do(ctx, http.MethodPost, c.tokenURL, "Basic "+basicAuth(id), "application/x-www-form-urlencoded", []byte(form.Encode()))
		if err != nil {
			return tokenAnswer{}, fmt.Errorf("garmin_connect: token exchange: %w", err)
		}
		status := resp.StatusCode
		switch {
		case status == http.StatusTooManyRequests:
			return tokenAnswer{}, connector.RateLimited(resp.Header, c.now())
		case status == http.StatusForbidden:
			return tokenAnswer{}, fmt.Errorf("garmin_connect: token exchange: HTTP 403: %w", ErrBlocked)
		case status >= 300:
			lastStatus = status
			continue
		}
		var a tokenAnswer
		if err := json.Unmarshal(raw, &a); err != nil || a.AccessToken == "" {
			lastStatus = status
			continue
		}
		return a, nil
	}
	return tokenAnswer{}, fmt.Errorf("garmin_connect: token exchange failed for every client id (last HTTP %d): %w", lastStatus, connector.ErrBadCredentials)
}

// basicAuth is the HTTP Basic value of "<id>:" (the DI endpoint takes the
// client id as the username and an empty password).
func basicAuth(id string) string {
	return base64.StdEncoding.EncodeToString([]byte(id + ":"))
}

// jwtSubject reads the "sub" claim of a JWT access token, the stable id of
// the Garmin account, or "" when the token is not a readable JWT. It verifies
// nothing: the token is Garmin's and is used only to label the connection.
func jwtSubject(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
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
