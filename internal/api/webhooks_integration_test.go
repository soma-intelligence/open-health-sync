// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/deletion"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

type webhookJSON struct {
	ID             uuid.UUID  `json:"id"`
	URL            string     `json:"url"`
	Description    string     `json:"description"`
	EventTypes     []string   `json:"event_types"`
	Status         string     `json:"status"`
	DisabledReason *string    `json:"disabled_reason"`
	Secret         string     `json:"secret"`
	CreatedAt      time.Time  `json:"created_at"`
	LastSuccessAt  *time.Time `json:"last_success_at"`
}

type deliveryJSON struct {
	ID        int64     `json:"id"`
	EventID   uuid.UUID `json:"event_id"`
	EventType string    `json:"event_type"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
}

func webhookEnv(t *testing.T, allowPrivate bool) *env {
	box, err := crypto.NewBox(bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	return newEnv(t, func(d *api.Deps) {
		d.Webhooks = webhook.NewStore(d.Pool, box)
		d.WebhookAllowPrivate = allowPrivate
	})
}

func TestWebhookEndpointsCRUD(t *testing.T) {
	e := webhookEnv(t, false)

	r := e.do("POST", "/v1/webhooks", e.keyA, map[string]any{
		"url": "https://app.example.com/hooks", "description": "prod", "event_types": []string{"data.written", "data.written"},
	})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	var created webhookJSON
	r.json(t, &created)
	assert.True(t, strings.HasPrefix(created.Secret, webhook.SecretPrefix))
	assert.Equal(t, []string{"data.written"}, created.EventTypes, "deduplicated")
	assert.Equal(t, "active", created.Status)
	path := "/v1/webhooks/" + created.ID.String()
	assert.Equal(t, path, r.header.Get("Location"))

	// The secret is never shown again.
	g := e.do("GET", path, e.keyA, nil)
	require.Equal(t, http.StatusOK, g.code)
	assert.NotContains(t, string(g.body), "secret")
	l := e.do("GET", "/v1/webhooks", e.keyA, nil)
	require.Equal(t, http.StatusOK, l.code)
	var list page[webhookJSON]
	l.json(t, &list)
	require.Len(t, list.Data, 1)
	assert.Nil(t, list.NextCursor)
	assert.NotContains(t, string(l.body), created.Secret)

	// Another tenant sees nothing of it.
	assert.Equal(t, http.StatusNotFound, e.do("GET", path, e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("PATCH", path, e.keyB, map[string]any{"description": "x"}).code)
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", path, e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("POST", path+"/test", e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("POST", path+"/rotate-secret", e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", path+"/deliveries", e.keyB, nil).code)
	var lb page[webhookJSON]
	e.do("GET", "/v1/webhooks", e.keyB, nil).json(t, &lb)
	assert.Empty(t, lb.Data)

	// Patch: an empty event_types list means every type.
	p := e.do("PATCH", path, e.keyA, map[string]any{"event_types": []string{}, "description": "all"})
	require.Equal(t, http.StatusOK, p.code, string(p.body))
	var patched webhookJSON
	p.json(t, &patched)
	assert.Empty(t, patched.EventTypes)
	assert.Equal(t, "all", patched.Description)
	assert.Equal(t, created.URL, patched.URL)

	p = e.do("PATCH", path, e.keyA, map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, p.code)
	p.json(t, &patched)
	assert.Equal(t, "disabled", patched.Status)
	require.NotNil(t, patched.DisabledReason)
	p = e.do("PATCH", path, e.keyA, map[string]any{"enabled": true})
	p.json(t, &patched)
	assert.Equal(t, "active", patched.Status)
	assert.Nil(t, patched.DisabledReason)

	// Rotate.
	rs := e.do("POST", path+"/rotate-secret", e.keyA, nil)
	require.Equal(t, http.StatusOK, rs.code)
	var sec struct{ Secret string }
	rs.json(t, &sec)
	assert.NotEqual(t, created.Secret, sec.Secret)
	assert.True(t, strings.HasPrefix(sec.Secret, webhook.SecretPrefix))

	// Test event, listed and not retryable while pending.
	tr := e.do("POST", path+"/test", e.keyA, nil)
	require.Equal(t, http.StatusAccepted, tr.code, string(tr.body))
	var d deliveryJSON
	tr.json(t, &d)
	assert.Equal(t, "webhook.test", d.EventType)
	assert.Equal(t, "pending", d.Status)
	dl := e.do("GET", path+"/deliveries?status=pending&limit=10", e.keyA, nil)
	require.Equal(t, http.StatusOK, dl.code, string(dl.body))
	var dp page[deliveryJSON]
	dl.json(t, &dp)
	require.Len(t, dp.Data, 1)
	assert.Equal(t, d.ID, dp.Data[0].ID)
	retry := path + "/deliveries/" + jsonNumber(d.ID) + "/retry"
	assert.Equal(t, http.StatusConflict, e.do("POST", retry, e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("POST", path+"/deliveries/999999/retry", e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("POST", path+"/deliveries/x/retry", e.keyA, nil).code)
	assert.Equal(t, http.StatusBadRequest, e.do("GET", path+"/deliveries?status=lost", e.keyA, nil).code)
	assert.Equal(t, http.StatusBadRequest, e.do("GET", path+"/deliveries?limit=0", e.keyA, nil).code)
	assert.Equal(t, http.StatusBadRequest, e.do("GET", path+"/deliveries?cursor=abc", e.keyA, nil).code)

	// Delete.
	assert.Equal(t, http.StatusNoContent, e.do("DELETE", path, e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", path, e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/webhooks/not-a-uuid", e.keyA, nil).code)
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/webhooks", "", nil).code)
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestWebhookRequestValidation(t *testing.T) {
	e := webhookEnv(t, false)
	bad := []map[string]any{
		{},
		{"url": "http://app.example.com/hooks"},
		{"url": "https://127.0.0.1/hooks"},
		{"url": "https://localhost/hooks"},
		{"url": "https://169.254.169.254/latest/meta-data"},
		{"url": "https://app.example.com/hooks", "event_types": []string{"data.everything"}},
		{"url": "https://app.example.com/hooks", "event_types": []string{"webhook.test"}},
		{"url": "https://app.example.com/hooks", "description": strings.Repeat("d", 256)},
		{"url": "https://app.example.com/hooks", "secret": "mine"},
	}
	for _, body := range bad {
		r := e.do("POST", "/v1/webhooks", e.keyA, body)
		assert.Equal(t, http.StatusBadRequest, r.code, "%v: %s", body, r.body)
	}
	r := e.do("POST", "/v1/webhooks", e.keyA, map[string]any{"url": "https://app.example.com/hooks"})
	require.Equal(t, http.StatusCreated, r.code)
	var w webhookJSON
	r.json(t, &w)
	assert.Equal(t, http.StatusBadRequest,
		e.do("PATCH", "/v1/webhooks/"+w.ID.String(), e.keyA, map[string]any{"url": "https://10.0.0.1/x"}).code)

	// With OHS_WEBHOOK_ALLOW_PRIVATE a local receiver is accepted.
	p := webhookEnv(t, true)
	r = p.do("POST", "/v1/webhooks", p.keyA, map[string]any{"url": "http://localhost:3000/hooks"})
	assert.Equal(t, http.StatusCreated, r.code, string(r.body))
}

// A user's deletion drops the webhooks not yet sent about them and queues
// user.deleted with their external_ref.
func TestUserDeletionAnnouncedByWebhook(t *testing.T) {
	e := webhookEnv(t, false)
	ctx := context.Background()
	r := e.do("POST", "/v1/webhooks", e.keyA, map[string]any{"url": "https://app.example.com/hooks"})
	require.Equal(t, http.StatusCreated, r.code)
	u := e.user(e.tenantA, "ref-9")
	_, err := webhook.Enqueue(ctx, e.pool, webhook.Event{Tenant: e.tenantA, User: &u, Type: webhook.DataWritten})
	require.NoError(t, err)

	require.Equal(t, http.StatusAccepted, e.do("DELETE", "/v1/users/"+u.String(), e.keyA, nil).code)
	raw, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)
	n, err := deletion.NewWorker(e.pool, raw, nil).Pass(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	rows, err := e.pool.Query(ctx, `SELECT event_type, payload FROM ohs_core.webhook_deliveries WHERE tenant_id = $1`, e.tenantA)
	require.NoError(t, err)
	defer rows.Close()
	var types []string
	for rows.Next() {
		var typ string
		var payload map[string]any
		require.NoError(t, rows.Scan(&typ, &payload))
		types = append(types, typ)
		data := payload["data"].(map[string]any)
		assert.Equal(t, u.String(), data["user_id"])
		assert.Equal(t, "ref-9", data["external_ref"])
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"user.deleted"}, types)
}
