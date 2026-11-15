//go:build integration

// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/consolesession"
)

// consoleURL is the public URL of the console tests: plain http on this
// machine, where a session cookie needs no Secure attribute.
const consoleURL = "http://localhost:8080"

func withConsole(d *api.Deps) { d.Console, d.PublicURL = true, consoleURL }

// keyIDOf is the id of the API key whose plaintext is key.
func (e *env) keyIDOf(key string) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`SELECT id FROM ohs_core.api_keys WHERE key_hash = $1`, auth.Hash(key)).Scan(&id))
	return id
}

// session signs in through POST /console/session and returns the session
// cookie token and the CSRF token.
func (e *env) session(key string) (cookie, csrf string) {
	e.t.Helper()
	r := e.do("POST", "/console/session", "", map[string]any{"key": key})
	require.Equal(e.t, http.StatusCreated, r.code, string(r.body))
	var body struct {
		CSRF string `json:"csrf_token"`
	}
	r.json(e.t, &body)
	return cookieOf(e.t, r), body.CSRF
}

// cookieOf is the value of the ohs_console cookie a response sets.
func cookieOf(t *testing.T, r resp) string {
	t.Helper()
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == auth.SessionCookie {
			return c.Value
		}
	}
	require.FailNow(t, "no ohs_console cookie")
	return ""
}

// sessionAs opens a session straight in the store, for a read only one on a
// server that is not read only.
func (e *env) sessionAs(key string, readOnly bool) (cookie, csrf string) {
	e.t.Helper()
	var tenantID uuid.UUID
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`SELECT tenant_id FROM ohs_core.api_keys WHERE key_hash = $1`, auth.Hash(key)).Scan(&tenantID))
	cookie, csrf, _, err := consolesession.New(e.pool).Create(context.Background(), tenantID, e.keyIDOf(key), readOnly)
	require.NoError(e.t, err)
	return cookie, csrf
}

func ck(cookie string) string { return auth.SessionCookie + "=" + cookie }

func TestConsoleSessionAuthenticatesTenantRoutes(t *testing.T) {
	e := newEnv(t, withConsole)
	e.user(e.tenantA, "a-1")
	e.user(e.tenantB, "b-1")
	cookie, _ := e.session(e.keyA)
	r := e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie))
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Contains(t, string(r.body), "a-1")
	assert.NotContains(t, string(r.body), "b-1")
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/users", "", nil, "Cookie", ck("nope")).code)
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/users", "", nil).code)
}

func TestConsoleSessionBearerWins(t *testing.T) {
	e := newEnv(t, withConsole)
	cookie, _ := e.session(e.keyA)
	r := e.do("GET", "/v1/users", "ohs_sk_wrong", nil, "Cookie", ck(cookie))
	assert.Equal(t, http.StatusUnauthorized, r.code, "a bad bearer is not rescued by a cookie")
}

func TestConsoleSessionCSRF(t *testing.T) {
	e := newEnv(t, withConsole)
	cookie, csrf := e.session(e.keyA)
	body := map[string]any{"external_ref": "p-1"}
	detail := func(r resp) string {
		var p struct {
			Detail string `json:"detail"`
		}
		r.json(t, &p)
		return p.Detail
	}
	r := e.do("POST", "/v1/users", "", body, "Cookie", ck(cookie))
	require.Equal(t, http.StatusForbidden, r.code, string(r.body))
	assert.Equal(t, auth.DetailCSRF, detail(r))
	r = e.do("POST", "/v1/users", "", body, "Cookie", ck(cookie), auth.CSRFHeader, "wrong")
	require.Equal(t, http.StatusForbidden, r.code, string(r.body))
	assert.Equal(t, auth.DetailCSRF, detail(r))
	r = e.do("POST", "/v1/users", "", body, "Cookie", ck(cookie), auth.CSRFHeader, cookie)
	assert.Equal(t, http.StatusForbidden, r.code, "the session token is not the CSRF token")
	r = e.do("POST", "/v1/users", "", body, "Cookie", ck(cookie), auth.CSRFHeader, csrf)
	assert.Equal(t, http.StatusCreated, r.code, string(r.body))
	assert.Equal(t, http.StatusOK, e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie)).code, "GET needs none")
}

func TestConsoleSessionForeignOrigin(t *testing.T) {
	e := newEnv(t, withConsole)
	cookie, _ := e.session(e.keyA)
	r := e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie), "Origin", "https://evil.example")
	require.Equal(t, http.StatusForbidden, r.code, string(r.body))
	assert.Contains(t, string(r.body), auth.DetailOrigin)
	r = e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie), "Origin", consoleURL)
	assert.Equal(t, http.StatusOK, r.code, string(r.body))
	r = e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie), "Origin", "null")
	assert.Equal(t, http.StatusForbidden, r.code)
}

func TestConsoleSessionReadOnly(t *testing.T) {
	e := newEnv(t, withConsole, func(d *api.Deps) { d.ConsoleReadOnly = true })
	cookie, csrf := e.session(e.keyA)
	r := e.do("POST", "/v1/users", "", map[string]any{"external_ref": "p-1"}, "Cookie", ck(cookie), auth.CSRFHeader, csrf)
	require.Equal(t, http.StatusForbidden, r.code, string(r.body))
	assert.Contains(t, string(r.body), auth.DetailReadOnly)
	assert.Equal(t, http.StatusOK, e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie)).code)
	r = e.do("POST", "/v1/users", e.keyA, map[string]any{"external_ref": "p-2"})
	assert.Equal(t, http.StatusCreated, r.code, "the same tenant's bearer key keeps full rights")
}

// A read only session still signs out and switches tenant: neither writes data.
func TestConsoleReadOnlySessionKeepsExemptRoutes(t *testing.T) {
	e := newEnv(t, withConsole, func(d *api.Deps) { d.ConsoleReadOnly = true })
	cookie, csrf := e.session(e.keyA)
	r := e.do("PUT", "/console/session/tenant", "", map[string]any{"tenant_id": e.tenantB}, "Cookie", ck(cookie), auth.CSRFHeader, csrf)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r = e.do("DELETE", "/console/session", "", nil, "Cookie", ck(cookie), auth.CSRFHeader, csrf)
	assert.Equal(t, http.StatusNoContent, r.code, string(r.body))
}

func TestConsoleSessionIdleAndAbsoluteExpiry(t *testing.T) {
	e := newEnv(t, withConsole)
	cookie, _ := e.session(e.keyA)
	require.Equal(t, http.StatusOK, e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie)).code)
	e.exec(`UPDATE ohs_core.console_sessions SET last_seen_at = now() - $1::interval`, "12 hours 1 minute")
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie)).code)

	cookie2, _ := e.session(e.keyA)
	require.Equal(t, http.StatusOK, e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie2)).code)
	e.exec(`UPDATE ohs_core.console_sessions SET created_at = now() - $1::interval, last_seen_at = now()`, "7 days 1 minute")
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie2)).code)
}

func TestConsoleSessionEndsWithKeyRevocation(t *testing.T) {
	e := newEnv(t, withConsole)
	r := e.do("POST", "/v1/keys", e.keyA, nil)
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var created struct {
		ID  uuid.UUID `json:"id"`
		Key string    `json:"key"`
	}
	r.json(t, &created)
	cookie, _ := e.session(created.Key)
	require.Equal(t, http.StatusOK, e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie)).code)
	r = e.do("DELETE", "/v1/keys/"+created.ID.String(), e.keyA, nil)
	require.Equal(t, http.StatusNoContent, r.code, string(r.body))
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie)).code)
}

func TestConsoleSessionAuditFields(t *testing.T) {
	e := newEnv(t, withConsole)
	cookie, _ := e.session(e.keyA)
	require.Equal(t, http.StatusOK, e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie)).code)
	require.Equal(t, http.StatusOK, e.do("GET", "/v1/users", e.keyA, nil).code)

	r := e.do("GET", "/v1/audit?limit=100", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var page auditPage
	r.json(t, &page)
	var users []audit.Entry
	for _, en := range page.Data {
		if en.Route == "GET /v1/users" {
			users = append(users, en)
		}
	}
	require.Len(t, users, 2, "%+v", page.Data)
	bearer, cookieEv := users[0], users[1] // newest first
	assert.Nil(t, bearer.ActorTenantID)
	assert.Equal(t, audit.APIKey, cookieEv.Actor.Type)
	assert.Equal(t, e.keyIDOf(e.keyA).String(), cookieEv.Actor.ID)
	require.NotNil(t, cookieEv.ActorTenantID)
	assert.Equal(t, e.tenantA, *cookieEv.ActorTenantID)
	assert.NotContains(t, string(r.body), cookie)
}

func TestConsoleOffIgnoresCookie(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.sessionAs(e.keyA, false)
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie)).code)
}

// An admin session of A acting as B: an admin route is recorded on A, the
// settings are updated by A, and B's own audit log shows none of it.
func TestConsoleSessionAdminRouteRecordsOnSessionTenant(t *testing.T) {
	e, _ := settingsEnv(t, withConsole)
	cookie, csrf, sess, err := consolesession.New(e.pool).Create(context.Background(), e.tenantA, e.keyIDOf(e.keyA), false)
	require.NoError(t, err)
	_, err = consolesession.New(e.pool).SetActingTenant(context.Background(), sess.ID, e.tenantB)
	require.NoError(t, err)

	r := e.do("GET", "/v1/admin/settings", "", nil, "Cookie", ck(cookie))
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var sb settingsBody
	r.json(t, &sb)
	r = e.do("PATCH", "/v1/admin/settings", "", `{"OHS_WEBHOOK_PAUSE_AFTER": "9"}`, "Cookie", ck(cookie),
		auth.CSRFHeader, csrf, "If-Match", etag(sb.Revision), "Content-Type", "application/json")
	require.Equal(t, http.StatusOK, r.code, string(r.body))

	var by uuid.UUID
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT updated_by FROM ohs_core.instance_settings WHERE name = 'OHS_WEBHOOK_PAUSE_AFTER'`).Scan(&by))
	assert.Equal(t, e.tenantA, by, "updated_by is the session tenant, not the tenant viewed")

	var onA, onB int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT count(*) FILTER (WHERE tenant_id = $1 AND actor_tenant_id = $1), count(*) FILTER (WHERE tenant_id = $2)
		 FROM ohs_core.audit_events WHERE route LIKE '% /v1/admin/settings'`, e.tenantA, e.tenantB).Scan(&onA, &onB))
	assert.Equal(t, 2, onA)
	assert.Zero(t, onB)

	r = e.do("GET", "/v1/audit?limit=100", e.keyB, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.NotContains(t, string(r.body), "/v1/admin/settings")
}

// do with a request the caller built, for a Host or a TLS state do cannot set.
func (e *env) serve(req *http.Request) resp {
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return resp{code: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
}

func (e *env) sessionCount() int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_core.console_sessions`).Scan(&n))
	return n
}

type consoleTenantBody struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Admin bool      `json:"admin"`
}

type consoleSessionBody struct {
	CSRF         string            `json:"csrf_token"`
	Tenant       consoleTenantBody `json:"tenant"`
	ActingTenant consoleTenantBody `json:"acting_tenant"`
	KeyPrefix    string            `json:"key_prefix"`
	KeyScopes    []string          `json:"key_scopes"`
	ReadOnly     bool              `json:"read_only"`
}

func (e *env) auditRows(action string) (n int, tenant, actor uuid.UUID, detail map[string]any) {
	e.t.Helper()
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ohs_core.audit_events WHERE action = $1`, action).Scan(&n))
	if n > 0 {
		var at *uuid.UUID
		require.NoError(e.t, e.pool.QueryRow(context.Background(),
			`SELECT tenant_id, actor_tenant_id, detail FROM ohs_core.audit_events WHERE action = $1 ORDER BY id DESC LIMIT 1`, action).
			Scan(&tenant, &at, &detail))
		if at != nil {
			actor = *at
		}
	}
	return
}

func TestConsoleSignIn(t *testing.T) {
	e := newEnv(t, withConsole)
	r := e.do("POST", "/console/session", "", map[string]any{"key": e.keyA})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	cookies := (&http.Response{Header: r.header}).Cookies()
	require.Len(t, cookies, 1)
	c := cookies[0]
	assert.Equal(t, auth.SessionCookie, c.Name)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, c.SameSite)
	assert.Equal(t, "/", c.Path)
	assert.False(t, c.Secure)
	assert.Zero(t, c.MaxAge)
	var body consoleSessionBody
	r.json(t, &body)
	assert.Len(t, body.CSRF, 43)
	assert.Equal(t, consoleTenantBody{ID: e.tenantA, Name: "Acme", Admin: true}, body.Tenant)
	assert.Equal(t, body.Tenant, body.ActingTenant)
	assert.Equal(t, e.keyA[:12], body.KeyPrefix)
	assert.False(t, body.ReadOnly)
	assert.NotContains(t, string(r.body), e.keyA)

	n, ten, actor, detail := e.auditRows(audit.SessionCreate)
	assert.Equal(t, 1, n)
	assert.Equal(t, e.tenantA, ten)
	assert.Equal(t, e.tenantA, actor)
	assert.Contains(t, detail["key_prefix"], e.keyA[:12])
	assert.NotContains(t, detail, "key")
}

func TestConsoleSignInSecureCookie(t *testing.T) {
	e := newEnv(t, withConsole, func(d *api.Deps) { d.PublicURL = "https://ohs.example.com" })
	r := e.do("POST", "/console/session", "", map[string]any{"key": e.keyA}, "Origin", "https://ohs.example.com")
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	cookies := (&http.Response{Header: r.header}).Cookies()
	require.Len(t, cookies, 1)
	assert.True(t, cookies[0].Secure)
}

func TestConsoleSignInReadOnlyServer(t *testing.T) {
	e := newEnv(t, withConsole, func(d *api.Deps) { d.ConsoleReadOnly = true })
	r := e.do("POST", "/console/session", "", map[string]any{"key": e.keyA})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var body consoleSessionBody
	r.json(t, &body)
	assert.True(t, body.ReadOnly)
	r = e.do("POST", "/v1/users", "", map[string]any{"external_ref": "x"}, "Cookie", ck(cookieOf(t, r)), auth.CSRFHeader, body.CSRF)
	assert.Equal(t, http.StatusForbidden, r.code)
	assert.Contains(t, string(r.body), auth.DetailReadOnly)
}

func TestConsoleSignInFailuresAreThrottled(t *testing.T) {
	e := newEnv(t, withConsole, func(d *api.Deps) { d.RateLimitAuthFailures = api.RateLimit{Rate: 1.0 / 60, Burst: 2} })
	bad := "ohs_sk_" + strings.Repeat("x", 43)
	var codes []int
	var last resp
	for range 3 {
		last = e.do("POST", "/console/session", "", map[string]any{"key": bad})
		codes = append(codes, last.code)
	}
	assert.Equal(t, []int{401, 401, 429}, codes)
	assert.NotEmpty(t, last.header.Get("Retry-After"))
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ohs_core.audit_events WHERE action = 'auth.failed' AND detail->>'key_prefix' = $1`, bad[:12]+"...").Scan(&n))
	assert.Equal(t, 1, n)
	r := e.do("POST", "/console/session", "", map[string]any{"key": e.keyA})
	assert.Equal(t, http.StatusCreated, r.code, "a good key is never throttled")
}

func TestConsoleSignInRefusals(t *testing.T) {
	e := newEnv(t, withConsole)
	ut := e.do("POST", "/v1/users", e.keyA, map[string]any{"external_ref": "u"})
	require.Equal(t, http.StatusCreated, ut.code)
	var u struct {
		ID uuid.UUID `json:"id"`
	}
	ut.json(t, &u)
	tok := e.do("POST", "/v1/users/"+u.ID.String()+"/tokens", e.keyA, map[string]any{"scopes": []string{"read"}})
	require.Equal(t, http.StatusCreated, tok.code, string(tok.body))
	var tk struct {
		Token string `json:"token"`
	}
	tok.json(t, &tk)
	require.NotEmpty(t, tk.Token)

	r := e.do("POST", "/console/session", "", map[string]any{"key": tk.Token})
	assert.Equal(t, http.StatusForbidden, r.code)
	assert.Contains(t, string(r.body), "not a user token")

	r = e.do("POST", "/console/session", "", `{"key": `)
	assert.Equal(t, http.StatusBadRequest, r.code)

	r = e.do("POST", "/console/session", "", map[string]any{"key": e.keyA}, "Origin", "https://evil.example")
	assert.Equal(t, http.StatusForbidden, r.code)
	assert.Contains(t, string(r.body), auth.DetailOrigin)

	scoped := e.newScopedKey("/v1/keys", e.keyA, "users:read")
	r = e.do("POST", "/console/session", "", map[string]any{"key": scoped.Key})
	assert.Equal(t, http.StatusForbidden, r.code)
	assert.Contains(t, string(r.body), auth.ScopeTenantRead)

	assert.Zero(t, e.sessionCount())
}

func TestConsoleSignInRefusedOverPlainHTTP(t *testing.T) {
	e := newEnv(t, withConsole, func(d *api.Deps) { d.PublicURL = "" })
	req := httptest.NewRequest("POST", "/console/session", strings.NewReader(`{"key": "`+e.keyA+`"}`))
	req.Host = "192.168.1.10:8080"
	r := e.serve(req)
	require.Equal(t, http.StatusForbidden, r.code, string(r.body))
	assert.Contains(t, string(r.body), "the console signs in over https, or on localhost")
	assert.Zero(t, e.sessionCount())
}

func TestConsoleSessionGetRotatesCSRF(t *testing.T) {
	e := newEnv(t, withConsole)
	cookie, old := e.session(e.keyA)
	r := e.do("GET", "/console/session", "", nil, "Cookie", ck(cookie))
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var body consoleSessionBody
	r.json(t, &body)
	require.Len(t, body.CSRF, 43)
	require.NotEqual(t, old, body.CSRF)
	assert.Equal(t, e.keyA[:12], body.KeyPrefix)

	post := func(token string) int {
		return e.do("POST", "/v1/users", "", map[string]any{"external_ref": "r-" + token[:6]}, "Cookie", ck(cookie), auth.CSRFHeader, token).code
	}
	r = e.do("POST", "/v1/users", "", map[string]any{"external_ref": "old"}, "Cookie", ck(cookie), auth.CSRFHeader, old)
	assert.Equal(t, http.StatusForbidden, r.code)
	assert.Contains(t, string(r.body), auth.DetailCSRF)
	assert.Equal(t, http.StatusCreated, post(body.CSRF))
	n, _, actor, _ := e.auditRows(audit.SessionRead)
	assert.Equal(t, 1, n)
	assert.Equal(t, e.tenantA, actor)
}

func TestConsoleSignOut(t *testing.T) {
	e := newEnv(t, withConsole)
	cookie, csrf := e.session(e.keyA)
	r := e.do("DELETE", "/console/session", "", nil, "Cookie", ck(cookie))
	assert.Equal(t, http.StatusForbidden, r.code)
	r = e.do("DELETE", "/console/session", "", nil, "Cookie", ck(cookie), auth.CSRFHeader, csrf)
	require.Equal(t, http.StatusNoContent, r.code, string(r.body))
	cookies := (&http.Response{Header: r.header}).Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, auth.SessionCookie, cookies[0].Name)
	assert.Empty(t, cookies[0].Value)
	assert.Equal(t, -1, cookies[0].MaxAge, "Max-Age=0")
	assert.Contains(t, r.header.Get("Set-Cookie"), "Max-Age=0")
	assert.True(t, cookies[0].HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/console/session", "", nil, "Cookie", ck(cookie)).code)
	assert.Zero(t, e.sessionCount())
	n, _, _, _ := e.auditRows(audit.SessionDelete)
	assert.Equal(t, 1, n)

	ro, rcsrf := e.sessionAs(e.keyA, true)
	r = e.do("DELETE", "/console/session", "", nil, "Cookie", ck(ro), auth.CSRFHeader, rcsrf)
	assert.Equal(t, http.StatusNoContent, r.code, "a read only session signs out too")
}

func TestConsoleTenantSwitch(t *testing.T) {
	e := newEnv(t, withConsole)
	e.user(e.tenantA, "a-1")
	e.user(e.tenantB, "b-1")
	cookie, csrf := e.session(e.keyA)
	put := func(body any) resp {
		return e.do("PUT", "/console/session/tenant", "", body, "Cookie", ck(cookie), auth.CSRFHeader, csrf)
	}
	r := put(map[string]any{"tenant_id": e.tenantB})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var body consoleSessionBody
	r.json(t, &body)
	assert.Equal(t, e.tenantB, body.ActingTenant.ID)
	assert.Equal(t, e.tenantA, body.Tenant.ID)
	assert.Empty(t, body.CSRF)
	assert.NotContains(t, string(r.body), "csrf_token")

	r = e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie))
	require.Equal(t, http.StatusOK, r.code)
	assert.Contains(t, string(r.body), "b-1")
	assert.NotContains(t, string(r.body), "a-1")
	r = e.do("GET", "/v1/tenant", "", nil, "Cookie", ck(cookie))
	require.Equal(t, http.StatusOK, r.code)
	assert.Contains(t, string(r.body), e.tenantB.String())

	var onB, actorOnB int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE actor_tenant_id = $2) FROM ohs_core.audit_events
		 WHERE tenant_id = $1 AND route = 'GET /v1/tenant'`, e.tenantB, e.tenantA).Scan(&onB, &actorOnB))
	assert.Equal(t, 1, onB)
	assert.Equal(t, 1, actorOnB)
	n, ten, actor, detail := e.auditRows(audit.SessionTenant)
	assert.Equal(t, 1, n)
	assert.Equal(t, e.tenantA, ten)
	assert.Equal(t, e.tenantA, actor)
	assert.Equal(t, e.tenantA.String(), detail["from_tenant_id"])
	assert.Equal(t, e.tenantB.String(), detail["to_tenant_id"])

	r = put(map[string]any{"tenant_id": e.tenantA})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &body)
	assert.Equal(t, e.tenantA, body.ActingTenant.ID)

	assert.Equal(t, http.StatusNotFound, put(map[string]any{"tenant_id": uuid.New()}).code)
	assert.Equal(t, http.StatusBadRequest, put(`{"tenant_id": "not-a-uuid"}`).code)

	ro, rcsrf := e.sessionAs(e.keyA, true)
	r = e.do("PUT", "/console/session/tenant", "", map[string]any{"tenant_id": e.tenantB}, "Cookie", ck(ro), auth.CSRFHeader, rcsrf)
	assert.Equal(t, http.StatusOK, r.code, "a read only admin session may switch")
}

func TestConsoleTenantSwitchNeedsAdmin(t *testing.T) {
	e := newEnv(t, withConsole)
	cookie, csrf := e.session(e.keyB)
	r := e.do("PUT", "/console/session/tenant", "", map[string]any{"tenant_id": e.tenantA}, "Cookie", ck(cookie), auth.CSRFHeader, csrf)
	require.Equal(t, http.StatusForbidden, r.code, string(r.body))
	assert.Contains(t, string(r.body), "instance admin tenant")
	var refused int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ohs_core.audit_events WHERE action = $1 AND status = 403 AND tenant_id = $2 AND actor_tenant_id = $2`,
		audit.SessionTenant, e.tenantB).Scan(&refused))
	assert.Equal(t, 1, refused, "a refused switch is in the audit log")
	r = e.do("GET", "/console/session", "", nil, "Cookie", ck(cookie))
	var body consoleSessionBody
	r.json(t, &body)
	assert.Equal(t, e.tenantB, body.ActingTenant.ID)
}

func TestConsoleSessionRoutesNeedASession(t *testing.T) {
	e := newEnv(t, withConsole)
	for _, c := range []struct{ method, path string }{
		{"GET", "/console/session"}, {"DELETE", "/console/session"}, {"PUT", "/console/session/tenant"},
	} {
		r := e.do(c.method, c.path, e.keyA, map[string]any{"tenant_id": e.tenantB})
		assert.Equal(t, http.StatusUnauthorized, r.code, c.method)
		assert.Contains(t, string(r.body), "this route needs a console session")
	}
}
