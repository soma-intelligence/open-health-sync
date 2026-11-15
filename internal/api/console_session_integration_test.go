//go:build integration

// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/consolesession"
)

func withConsole(d *api.Deps) { d.Console = true }

// keyIDOf is the id of the API key whose plaintext is key.
func (e *env) keyIDOf(key string) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`SELECT id FROM ohs_core.api_keys WHERE key_hash = $1`, auth.Hash(key)).Scan(&id))
	return id
}

// session opens a console session for the tenant of key and returns the
// session cookie token and the CSRF token. Task 5 swaps the store call for
// POST /console/session.
func (e *env) session(key string) (cookie, csrf string) {
	e.t.Helper()
	return e.sessionAs(key, false)
}

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
	// httptest requests are addressed to example.com.
	r = e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie), "Origin", "http://example.com")
	assert.Equal(t, http.StatusOK, r.code, string(r.body))
	r = e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie), "Origin", "null")
	assert.Equal(t, http.StatusForbidden, r.code)
}

func TestConsoleSessionReadOnly(t *testing.T) {
	e := newEnv(t, withConsole, func(d *api.Deps) { d.ConsoleReadOnly = true })
	cookie, csrf := e.sessionAs(e.keyA, true)
	r := e.do("POST", "/v1/users", "", map[string]any{"external_ref": "p-1"}, "Cookie", ck(cookie), auth.CSRFHeader, csrf)
	require.Equal(t, http.StatusForbidden, r.code, string(r.body))
	assert.Contains(t, string(r.body), auth.DetailReadOnly)
	assert.Equal(t, http.StatusOK, e.do("GET", "/v1/users", "", nil, "Cookie", ck(cookie)).code)
	r = e.do("POST", "/v1/users", e.keyA, map[string]any{"external_ref": "p-2"})
	assert.Equal(t, http.StatusCreated, r.code, "the same tenant's bearer key keeps full rights")
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
	cookie, _ := e.session(e.keyA)
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
