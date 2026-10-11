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
