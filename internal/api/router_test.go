// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// offlineRouter is the router over a pool that never connects: enough for
// routes answered before any query.
func offlineRouter(t *testing.T) http.Handler {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return NewRouter(Deps{Pool: pool, Store: tenant.New(pool)})
}

func TestWrongMethodIs405(t *testing.T) {
	h := offlineRouter(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PUT", "/v1/users", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD, POST", rec.Header().Get("Allow"))
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body["detail"], "GET, HEAD, POST")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/users/abc/samples", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/nothing-here", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestOpenAPIServed(t *testing.T) {
	h := offlineRouter(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/openapi.yaml", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/yaml", rec.Header().Get("Content-Type"))
	assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"), "readable by a hosted Swagger UI or Redoc")
	assert.Contains(t, rec.Body.String(), "openapi: 3.1.0")
	assert.Contains(t, rec.Body.String(), "/v1/users/{id}/samples:")
}

func TestConsoleOffByDefault(t *testing.T) {
	h := offlineRouter(t)
	for _, p := range []string{"/console", "/console/", "/console/app.mjs"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		assert.Equal(t, http.StatusNotFound, rec.Code, p)
	}
}

func TestConsoleMountedWithFlag(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	h := NewRouter(Deps{Pool: pool, Store: tenant.New(pool), Console: true})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/console", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	assert.NotEmpty(t, rec.Header().Get("X-Request-Id"), "console requests go through the instrumented router")

	for _, p := range []string{"/console/", "/console/app.mjs", "/console/console.css", "/console/setup", "/console/setup.mjs"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		assert.Equal(t, http.StatusOK, rec.Code, p)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/console/missing.js", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/console", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "the console is GET only")

	// The API itself is unchanged: reads still need a key.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/users", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestConnectPageRoutesMounted(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	box, err := crypto.NewBox(make([]byte, 32))
	require.NoError(t, err)
	store := tenant.New(pool)
	h := NewRouter(Deps{Pool: pool, Store: store, Collect: collect.NewHandlers(collect.Deps{Pool: pool, Box: box, Tenants: store})})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/connect?session=not-a-token", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Contains(t, rec.Body.String(), "This link is not valid")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/connect", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/users/00000000-0000-0000-0000-000000000001/connect-sessions", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestSetupOffByDefault(t *testing.T) {
	h := offlineRouter(t)
	for _, m := range []string{"GET", "POST"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/v1/setup", strings.NewReader(`{"setup_token":"x"}`)))
		assert.Equal(t, http.StatusNotFound, rec.Code, m)
	}
}

// The refusals answered before any query: a bad or oversized body. With the
// database unreachable the setup state is unknown, so a well-formed call
// fails closed (500) without the token being checked. None of them echoes
// the token.
func TestSetupRefusals(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	const token = "the-right-setup-token"
	h := NewRouter(Deps{Pool: pool, Store: tenant.New(pool), SetupToken: token})
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/setup", strings.NewReader(body)))
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
		assert.NotContains(t, rec.Body.String(), token)
		return rec
	}
	for _, body := range []string{`{"setup_token":"the-wrong-setup-token"}`, `{"setup_token":""}`, `{}`, `{"setup_token":"the-right-setup-toke"}`} {
		assert.Equal(t, http.StatusInternalServerError, post(body).Code, body)
	}
	assert.Equal(t, http.StatusBadRequest, post(`not json`).Code)
	assert.Equal(t, http.StatusBadRequest, post(``).Code, "a body is required")
	assert.Equal(t, http.StatusBadRequest, post(`{"setup_token":"`+token+`","extra":1}`).Code)
	assert.Equal(t, http.StatusRequestEntityTooLarge,
		post(`{"setup_token":"`+token+`","tenant_name":"`+strings.Repeat("x", 5000)+`"}`).Code)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PUT", "/v1/setup", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD, POST", rec.Header().Get("Allow"))
}

func TestSetupRateLimit(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	h := NewRouter(Deps{
		Pool: pool, Store: tenant.New(pool), SetupToken: "the-right-setup-token",
		RateLimitOAuth: RateLimit{Rate: 0.001, Burst: 2},
	})
	do := func(method, remote string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/v1/setup", strings.NewReader(`{"setup_token":"a-wrong-setup-token"}`))
		r.RemoteAddr = remote
		h.ServeHTTP(rec, r)
		return rec
	}
	for range 2 {
		assert.NotEqual(t, http.StatusTooManyRequests, do("POST", "192.0.2.1:1").Code)
	}
	rec := do("POST", "192.0.2.1:1")
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "the third try from one address within the burst")
	assert.Equal(t, "1000", rec.Header().Get("Retry-After"))
	assert.NotEqual(t, http.StatusTooManyRequests, do("POST", "192.0.2.2:1").Code, "another address")
	assert.NotEqual(t, http.StatusTooManyRequests, do("GET", "192.0.2.1:1").Code, "the status read is not limited")
}

func TestCheckTenantName(t *testing.T) {
	for name, ok := range map[string]bool{
		"default": true, "Home lab": true, "Équipe santé": true, strings.Repeat("x", 100): true,
		strings.Repeat("x", 101): false, "a\nb": false, "tab\there": false, "\x00": false, "\xff": false,
	} {
		assert.Equal(t, ok, checkTenantName(name) == "", "%q", name)
	}
}

func TestNULAndInvalidUTF8RefusedAtTheEdge(t *testing.T) {
	h := offlineRouter(t)
	for _, target := range []string{
		"/v1/users/abc/samples?type=heart_rate%00",
		"/v1/users/abc/samples?type=%ff%fe",
		"/v1/users/abc/samples?ty%00pe=heart_rate",
		"/v1/users/abc/connections/oura%00/sync",
		"/v1/users/%ff/samples",
		"/v1/audit?action=%00",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		require.Equal(t, http.StatusBadRequest, rec.Code, target)
		status, detail := problemOf(t, rec)
		assert.Equal(t, http.StatusBadRequest, status, target)
		assert.Contains(t, detail, "NUL character or invalid UTF-8", target)
		assert.NotEmpty(t, rec.Header().Get("X-Request-Id"), target)
	}
	// Valid text, escaped or not, reaches the route: 401 without a key.
	for _, target := range []string{
		"/v1/users/abc/samples?type=heart_rate",
		"/v1/users/abc/samples?note=caf%C3%A9",
		"/v1/users/abc/samples?type=a%2Fb",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		assert.Equal(t, http.StatusUnauthorized, rec.Code, target)
	}
}
