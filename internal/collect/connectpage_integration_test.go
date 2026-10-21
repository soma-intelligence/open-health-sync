// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
)

// newPageEnv is newConnEnv with the hosted connection page routes mounted,
// over handlers that list the fake provider as the enabled OAuth provider.
func newPageEnv(t *testing.T) *connEnv {
	t.Helper()
	e := newConnEnv(t)
	d := e.deps
	d.Slugs = []string{fakeSlug}
	h := collect.NewHandlers(d)
	tk := auth.Middleware(e.store.LookupKey, auth.TenantKey)
	e.mux.Handle("POST /v1/users/{id}/connect-sessions", tk(h.CreateConnectSession()))
	e.mux.Handle("GET /connect", h.ConnectPage())
	e.mux.Handle("GET /connect/{provider}", h.ConnectPage())
	e.mux.Handle("POST /connect/{provider}", h.ConnectStart())
	return e
}

// createSession creates a connect session and returns its token.
func (e *connEnv) createSession(t *testing.T, key string, user uuid.UUID, body string) (string, collect.ConnectSessionResponse) {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/v1/users/"+user.String()+"/connect-sessions", key, []byte(body))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp collect.ConnectSessionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	u, err := url.Parse(resp.URL)
	require.NoError(t, err)
	assert.Equal(t, "https://ohs.example.test/connect", u.Scheme+"://"+u.Host+u.Path)
	token := u.Query().Get("session")
	require.Len(t, token, 43)
	return token, resp
}

func (e *connEnv) postConnect(t *testing.T, provider, token string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"session": {token}}
	req := httptest.NewRequest(http.MethodPost, "/connect/"+provider, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// consentState checks a 303 to the fake provider's consent page and returns
// its state.
func (e *connEnv) consentState(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(loc.String(), e.fake.srv.URL+"/authorize?"), loc.String())
	assert.Equal(t, "https://ohs.example.test/oauth/"+fakeSlug+"/callback", loc.Query().Get("redirect_uri"))
	state := loc.Query().Get("state")
	require.NotEmpty(t, state)
	return state
}

func TestConnectPageFlowToRedirect(t *testing.T) {
	e := newPageEnv(t)
	before := time.Now()
	token, resp := e.createSession(t, e.keyA, e.userA,
		`{"redirect_uri":"https://app.example.test/done","app_name":"Sleep Diary","logo_url":"https://cdn.example.test/logo.png"}`)
	assert.WithinDuration(t, before.Add(30*time.Minute), resp.ExpiresAt, 5*time.Second)
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_core.connect_sessions WHERE token_hash = $1`, token),
		"the token itself is never stored")
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_core.connect_sessions WHERE tenant_id = $1 AND user_id = $2`,
		e.tenantA, e.userA))

	rec := e.do(t, http.MethodGet, "/connect?session="+token, "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "form-action 'self' "+e.fake.srv.URL+";")
	body := rec.Body.String()
	assert.Contains(t, body, "Sleep Diary would like to read your health data")
	assert.Contains(t, body, `src="https://cdn.example.test/logo.png"`)
	assert.Contains(t, body, `action="connect/`+fakeSlug+`"`)
	assert.Contains(t, body, "Not connected")

	state := e.consentState(t, e.postConnect(t, fakeSlug, token))
	e.fake.grant("page-code", "pu-page")
	rec = e.callback(t, state, "page-code")
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "app.example.test", loc.Host)
	assert.Equal(t, "connected", loc.Query().Get("status"))
	assert.Equal(t, fakeSlug, loc.Query().Get("provider"))
	assert.Equal(t, "active", e.connStatus(t, e.userA))

	// The session still serves the page, now showing the connection.
	rec = e.do(t, http.MethodGet, "/connect/"+fakeSlug+"?session="+token, "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `class="pill connected">Connected<`)
	assert.Contains(t, rec.Body.String(), "Reconnect")
	assert.Equal(t, 1, strings.Count(rec.Body.String(), "<form"))
	again := e.consentState(t, e.postConnect(t, fakeSlug, token))
	assert.NotEqual(t, state, again, "each start mints a fresh state")
}

func TestConnectPageWithoutRedirectLinksBack(t *testing.T) {
	e := newPageEnv(t)
	token, _ := e.createSession(t, e.keyA, e.userA, ``)
	rec := e.postConnect(t, fakeSlug, token)
	state := e.consentState(t, rec)
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "ohs_connect" {
			cookie = c
		}
	}
	require.NotNil(t, cookie)
	assert.Equal(t, "/oauth/", cookie.Path)
	assert.True(t, cookie.HttpOnly)
	assert.True(t, cookie.Secure)

	e.fake.grant("page-code-2", "pu-page-2")
	q := url.Values{"state": {state}, "code": {"page-code-2"}}
	req := httptest.NewRequest(http.MethodGet, "/oauth/"+fakeSlug+"/callback?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	out := httptest.NewRecorder()
	e.mux.ServeHTTP(out, req)
	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	assert.Contains(t, out.Body.String(), fakeSlug+" is connected")
	assert.Contains(t, out.Body.String(), `href="https://ohs.example.test/connect?session=`+token+`"`)
}

func TestConnectSessionExpired(t *testing.T) {
	e := newPageEnv(t)
	now := time.Now()
	e.setNow(now)
	token, _ := e.createSession(t, e.keyA, e.userA, `{"ttl_seconds":60}`)
	e.setNow(now.Add(2 * time.Minute))

	rec := e.do(t, http.MethodGet, "/connect?session="+token, "", nil)
	assert.Equal(t, http.StatusGone, rec.Code)
	assert.Contains(t, rec.Body.String(), "This link has expired")
	assert.NotContains(t, rec.Body.String(), fakeSlug)

	rec = e.postConnect(t, fakeSlug, token)
	assert.Equal(t, http.StatusGone, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"))
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.oauth_states WHERE user_id = $1`, e.userA))
}

func TestConnectSessionRefusals(t *testing.T) {
	e := newPageEnv(t)
	path := "/v1/users/" + e.userA.String() + "/connect-sessions"

	rec := e.do(t, http.MethodPost, path, e.keyB, []byte(`{}`))
	assert.Equal(t, http.StatusNotFound, rec.Code, "another tenant's user")

	rec = e.do(t, http.MethodPost, path, e.keyA, []byte(`{"providers":["nope"]}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = e.do(t, http.MethodPost, path, e.keyA, []byte(`{"redirect_uri":"http://app.example.test/"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = e.do(t, http.MethodPost, path, e.keyA, []byte(`{"colour":"red"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = e.do(t, http.MethodPost, path, "", []byte(`{}`))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// A well-formed token that was never issued.
	unknown := strings.Repeat("A", 43)
	rec = e.do(t, http.MethodGet, "/connect?session="+unknown, "", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, http.StatusNotFound, e.postConnect(t, fakeSlug, unknown).Code)

	// A provider the session does not offer.
	token, _ := e.createSession(t, e.keyA, e.userA, `{"providers":["`+fakeSlug+`"]}`)
	rec = e.do(t, http.MethodGet, "/connect/other?session="+token, "", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, http.StatusNotFound, e.postConnect(t, "other", token).Code)
}
