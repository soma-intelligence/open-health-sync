// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// pageOAuth is an OAuth connector whose consent page is on auth.<slug>.example.test.
type pageOAuth struct{ slug string }

func (p pageOAuth) Slug() string                                   { return p.slug }
func (pageOAuth) Enabled(connector.Config) bool                    { return true }
func (pageOAuth) Map(connector.RawRecord) (model.MapResult, error) { return model.MapResult{}, nil }
func (p pageOAuth) AuthorizeURL(state, redirect string) (string, error) {
	v := url.Values{"state": {state}, "redirect_uri": {redirect}}
	return "https://auth." + p.slug + ".example.test/authorize?" + v.Encode(), nil
}
func (pageOAuth) Exchange(context.Context, string, string) (connector.Tokens, error) {
	return connector.Tokens{}, nil
}
func (pageOAuth) Refresh(context.Context, connector.Tokens) (connector.Tokens, error) {
	return connector.Tokens{}, nil
}
func (pageOAuth) Fetch(context.Context, connector.Connection, time.Time, time.Time, connector.RawSink) error {
	return nil
}

// pageDeps resolves withings and oura (OAuth) and a device connector.
func pageDeps() Deps {
	byID := map[string]connector.Connector{
		"withings": pageOAuth{"withings"},
		"oura":     pageOAuth{"oura"},
		"device":   probePush{"device"},
	}
	return Deps{
		PublicURL: "https://ohs.example.test",
		Slugs:     []string{"device", "oura", "withings"},
		Lookup:    func(s string) (connector.Connector, bool) { c, ok := byID[s]; return c, ok },
	}
}

func intp(n int) *int { return &n }

func TestNewConnectSessionDefaults(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 0, 0, 500, time.UTC)
	s, code, problem := pageDeps().newConnectSession(uuid.Nil, ConnectSessionRequest{}, now)
	require.Zero(t, code)
	require.Empty(t, problem)
	assert.Equal(t, []string{"oura", "withings"}, s.providers, "every enabled OAuth provider, devices left out")
	assert.Equal(t, time.Date(2026, 3, 1, 10, 30, 0, 0, time.UTC), s.expiresAt)
	assert.Empty(t, s.redirect)
	assert.Empty(t, s.appName)
}

func TestNewConnectSessionKeepsValidFields(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	s, code, problem := pageDeps().newConnectSession(uuid.Nil, ConnectSessionRequest{
		RedirectURI: "https://app.example.test/done",
		Providers:   []string{"withings", "oura", "withings"},
		AppName:     "  Sleep Diary  ",
		LogoURL:     "https://cdn.example.test/logo.png",
		TTLSeconds:  intp(60),
	}, now)
	require.Zero(t, code)
	require.Empty(t, problem)
	assert.Equal(t, []string{"withings", "oura"}, s.providers, "order kept, duplicates dropped")
	assert.Equal(t, "Sleep Diary", s.appName)
	assert.Equal(t, "https://app.example.test/done", s.redirect)
	assert.Equal(t, "https://cdn.example.test/logo.png", s.logoURL)
	assert.Equal(t, now.Add(time.Minute), s.expiresAt)
}

func TestNewConnectSessionRefuses(t *testing.T) {
	long := "https://cdn.example.test/" + strings.Repeat("a", 2048)
	many := make([]string, maxConnectProviders+1)
	for i := range many {
		many[i] = "withings"
	}
	for name, tc := range map[string]struct {
		req  ConnectSessionRequest
		want string
	}{
		"http redirect":        {ConnectSessionRequest{RedirectURI: "http://app.example.test/done"}, "redirect_uri"},
		"relative redirect":    {ConnectSessionRequest{RedirectURI: "/done"}, "redirect_uri"},
		"unknown provider":     {ConnectSessionRequest{Providers: []string{"nope"}}, `"nope"`},
		"non-OAuth provider":   {ConnectSessionRequest{Providers: []string{"device"}}, `"device"`},
		"too many providers":   {ConnectSessionRequest{Providers: many}, "at most 64"},
		"app name too long":    {ConnectSessionRequest{AppName: strings.Repeat("é", 81)}, "app_name"},
		"app name control":     {ConnectSessionRequest{AppName: "a\nb"}, "control"},
		"http logo":            {ConnectSessionRequest{LogoURL: "http://cdn.example.test/logo.png"}, "logo_url"},
		"data logo":            {ConnectSessionRequest{LogoURL: "data:image/png;base64,AAAA"}, "logo_url"},
		"logo with user":       {ConnectSessionRequest{LogoURL: "https://u:p@cdn.example.test/l.png"}, "logo_url"},
		"logo too long":        {ConnectSessionRequest{LogoURL: long}, "logo_url"},
		"ttl too short":        {ConnectSessionRequest{TTLSeconds: intp(59)}, "ttl_seconds"},
		"ttl too long":         {ConnectSessionRequest{TTLSeconds: intp(3601)}, "ttl_seconds"},
		"ttl zero is explicit": {ConnectSessionRequest{TTLSeconds: intp(0)}, "ttl_seconds"},
	} {
		t.Run(name, func(t *testing.T) {
			_, code, problem := pageDeps().newConnectSession(uuid.Nil, tc.req, time.Now())
			assert.Equal(t, http.StatusBadRequest, code)
			assert.Contains(t, problem, tc.want)
		})
	}
	_, code, problem := pageDeps().newConnectSession(uuid.Nil, ConnectSessionRequest{AppName: strings.Repeat("é", 80)}, time.Now())
	assert.Zero(t, code)
	assert.Empty(t, problem, "80 characters, more than 80 bytes, is accepted")
}

func TestNewConnectSessionWithNoOAuthProvider(t *testing.T) {
	d := pageDeps()
	d.Slugs = []string{"device"}
	_, code, problem := d.newConnectSession(uuid.Nil, ConnectSessionRequest{}, time.Now())
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, problem, "no OAuth provider")
}

// A provider turned off for the tenant is left out of the default list and
// refused by name (403); another tenant still gets it.
func TestNewConnectSessionProviderOff(t *testing.T) {
	off, other := uuid.New(), uuid.New()
	d := pageDeps()
	d.Allowed = func(tenant uuid.UUID, slug string) bool { return tenant != off || slug != "oura" }
	s, code, _ := d.newConnectSession(off, ConnectSessionRequest{}, time.Now())
	require.Zero(t, code)
	assert.Equal(t, []string{"withings"}, s.providers)
	_, code, problem := d.newConnectSession(off, ConnectSessionRequest{Providers: []string{"withings", "oura"}}, time.Now())
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, problem, `"oura"`)
	assert.Contains(t, problem, ProviderOffDetail)
	s, code, _ = d.newConnectSession(other, ConnectSessionRequest{Providers: []string{"oura"}}, time.Now())
	require.Zero(t, code)
	assert.Equal(t, []string{"oura"}, s.providers)

	d.Allowed = func(uuid.UUID, string) bool { return false }
	_, code, problem = d.newConnectSession(off, ConnectSessionRequest{}, time.Now())
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, problem, "no OAuth provider")
}

func TestConnectURL(t *testing.T) {
	d := Deps{PublicURL: "https://ohs.example.test/"}
	assert.Equal(t, "https://ohs.example.test/connect?session=abc_-1", d.ConnectURL("abc_-1"))
}

// pageMux mounts the page routes over handlers without a database: only
// answers given before any query can be tested.
func pageMux() *http.ServeMux {
	h := &Handlers{d: pageDeps()}
	mux := http.NewServeMux()
	mux.Handle("GET /connect", h.ConnectPage())
	mux.Handle("GET /connect/{provider}", h.ConnectPage())
	mux.Handle("POST /connect/{provider}", h.ConnectStart())
	return mux
}

func assertPageHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "default-src 'none'")
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
}

func TestConnectPageMalformedTokenIs404Page(t *testing.T) {
	for _, target := range []string{"/connect", "/connect?session=short", "/connect/withings?session=" + strings.Repeat("a", 44)} {
		rec := httptest.NewRecorder()
		pageMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		assert.Equal(t, http.StatusNotFound, rec.Code, target)
		assertPageHeaders(t, rec)
		assert.Contains(t, rec.Body.String(), "This link is not valid")
		assert.NotContains(t, rec.Body.String(), "<form")
	}
}

func TestConnectStartMalformedTokenIs404Page(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/connect/withings", strings.NewReader("session=nope"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	pageMux().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assertPageHeaders(t, rec)
	assert.Empty(t, rec.Header().Get("Location"))
}

func TestConnectStartRefusesLargeForm(t *testing.T) {
	body := "session=" + strings.Repeat("a", maxConnectForm)
	req := httptest.NewRequest(http.MethodPost, "/connect/withings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	pageMux().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assertPageHeaders(t, rec)
}

func TestConnectPageTemplateEscapes(t *testing.T) {
	var b strings.Builder
	require.NoError(t, connectPage.Execute(&b, connectView{
		AppName: `<script>alert(1)</script>`, LogoURL: `https://cdn.example.test/l.png?a="b"`,
		Session: `tok"en`, Expires: "in 30 minutes", Connected: 1,
		Providers: []providerView{
			{Slug: "withings", Name: "Withings", Initial: "W", Detail: providerDetails["withings"], Status: "connected",
				StatusText: "Connected", Button: "Reconnect", Action: "connect/withings"},
			{Slug: "oura", Name: "Oura", Status: "not_connected", StatusText: "Not connected", Button: "Connect", Action: "connect/oura"},
		},
	}))
	out := b.String()
	assert.NotContains(t, out, "<script>")
	assert.Contains(t, out, "&lt;script&gt;alert(1)&lt;/script&gt; would like to read your health data")
	assert.Contains(t, out, `src="https://cdn.example.test/l.png?a=%22b%22"`)
	assert.Contains(t, out, `value="tok&#34;en"`)
	assert.Contains(t, out, `action="connect/withings"`)
	assert.Contains(t, out, `action="connect/oura"`)
	assert.Contains(t, out, "Not connected")
	assert.Equal(t, 2, strings.Count(out, "<form"))
	assert.Contains(t, out, "1 of 2 connected")
	assert.Contains(t, out, "Weight, body composition, blood pressure and sleep")
	assert.Contains(t, out, "&lt;script&gt;alert(1)&lt;/script&gt; never sees your password")
}

func TestProviderDetailsAreForNamedProviders(t *testing.T) {
	for slug := range providerDetails {
		_, ok := providerNames[slug]
		assert.True(t, ok, "%s has a detail line but no display name", slug)
	}
}

func TestConnectPageTemplateWithoutAppName(t *testing.T) {
	var b strings.Builder
	require.NoError(t, connectPage.Execute(&b, connectView{Session: "t", Single: true, Providers: []providerView{
		{Name: "Oura", Status: "needs_reauth", StatusText: "Needs reconnecting", Button: "Reconnect", Action: "oura"},
	}}))
	out := b.String()
	assert.Contains(t, out, "Connect your health data")
	assert.NotContains(t, out, "<img")
	assert.Contains(t, out, "Needs reconnecting")
}

func TestPageCSPAllowsConsentOrigins(t *testing.T) {
	h := &Handlers{d: pageDeps()}
	csp := h.pageCSP([]string{"withings", "oura", "withings"})
	assert.Contains(t, csp, "form-action 'self' https://auth.withings.example.test https://auth.oura.example.test;")
	assert.Contains(t, csp, "img-src https:")
	assert.NotContains(t, csp, "script-src")
}

func TestBackCookie(t *testing.T) {
	exp := time.Date(2026, 3, 1, 10, 30, 0, 0, time.UTC)
	h := &Handlers{d: Deps{PublicURL: "https://ohs.example.test/base/"}}
	c := h.backCookie("tok", exp)
	assert.Equal(t, "/base/oauth/", c.Path)
	assert.True(t, c.Secure)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
	assert.Equal(t, exp, c.Expires)

	h = &Handlers{d: Deps{PublicURL: "http://localhost:8080"}}
	c = h.backCookie("tok", exp)
	assert.Equal(t, "/oauth/", c.Path)
	assert.False(t, c.Secure)
}

func TestExpiresIn(t *testing.T) {
	assert.Equal(t, "in 30 minutes", expiresIn(29*time.Minute+time.Second))
	assert.Equal(t, "in about a minute", expiresIn(20*time.Second))
	assert.Equal(t, 30, minutesLeft(29*time.Minute+time.Second))
	assert.Equal(t, 1, minutesLeft(-time.Second))
}

func TestProviderName(t *testing.T) {
	assert.Equal(t, "intervals.icu", providerName("intervals_icu"))
	assert.Equal(t, "WHOOP", providerName("whoop"))
	assert.Equal(t, "fakefit", providerName("fakefit"))
}

func TestResultPageBackLink(t *testing.T) {
	rec := httptest.NewRecorder()
	renderResultBack(rec, http.StatusOK, "withings", true, "https://ohs.example.test/connect?session=abc")
	assert.Contains(t, rec.Body.String(), "Withings is connected")
	assert.Contains(t, rec.Body.String(), `href="https://ohs.example.test/connect?session=abc"`)

	rec = httptest.NewRecorder()
	renderResult(rec, http.StatusOK, "withings", true)
	assert.NotContains(t, rec.Body.String(), "href=")
	assert.Contains(t, rec.Body.String(), "You can close this window.")
}
