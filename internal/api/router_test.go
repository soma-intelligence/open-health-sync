// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	for _, p := range []string{"/console/", "/console/app.mjs", "/console/console.css"} {
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
