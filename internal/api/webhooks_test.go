// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// offlineWebhookRouter mounts the webhook routes over a pool that never
// connects, with every bearer secret taken as a tenant key: enough for the
// refusals answered before any query.
func offlineWebhookRouter(t *testing.T) http.Handler {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	tenantID := uuid.New()
	return NewRouter(Deps{
		Pool: pool, Store: tenant.New(pool), Webhooks: webhook.NewStore(pool, nil),
		Lookup: func(context.Context, string) (auth.Principal, error) {
			return auth.Principal{TenantID: tenantID}, nil
		},
	})
}

func TestWebhookSandboxRequestValidation(t *testing.T) {
	h := offlineWebhookRouter(t)
	wh := "/v1/webhooks/" + uuid.NewString()
	cases := []struct {
		method, path, body string
		want               int
	}{
		{"POST", wh + "/test", `{"event_type":"data.everything"}`, http.StatusBadRequest},
		{"POST", wh + "/test", `{"event_type":42}`, http.StatusBadRequest},
		{"POST", wh + "/test", `{"type":"data.written"}`, http.StatusBadRequest},
		{"POST", wh + "/test", `{"event_type":"data.written"} {}`, http.StatusBadRequest},
		{"GET", wh + "/deliveries/x", "", http.StatusNotFound},
		{"GET", wh + "/deliveries/0", "", http.StatusNotFound},
		{"GET", wh + "/deliveries/-3", "", http.StatusNotFound},
		{"POST", wh + "/deliveries/x/replay", "", http.StatusNotFound},
		{"POST", wh + "/deliveries/0/replay", "", http.StatusNotFound},
		{"GET", "/v1/webhooks/not-a-uuid/deliveries/1", "", http.StatusNotFound},
		{"POST", "/v1/webhooks/not-a-uuid/deliveries/1/replay", "", http.StatusNotFound},
		{"DELETE", wh + "/deliveries/1", "", http.StatusMethodNotAllowed},
		{"GET", wh + "/deliveries/1/replay", "", http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer ohs_sk_test")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, c.want, rec.Code, "%s %s %s: %s", c.method, c.path, c.body, rec.Body)
		assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"), "%s %s", c.method, c.path)
	}
	// The sandbox routes still demand a credential.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", wh+"/deliveries/1", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
