// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// webhookBox seals the secrets of webhookEnv; a delivery worker opens them
// with it.
func webhookBox(t *testing.T) *crypto.Box {
	box, err := crypto.NewBox(bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	return box
}

func webhookEnv(t *testing.T, allowPrivate bool) *env {
	box := webhookBox(t)
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
	// An attempt of that delivery, as the worker records it.
	e.exec(`INSERT INTO ohs_core.webhook_attempts (tenant_id, delivery_id, endpoint_id, attempt, started_at,
			duration_ms, status_code, error)
		SELECT tenant_id, id, endpoint_id, 1, now(), 12, 503, 'receiver answered HTTP 503'
		FROM ohs_core.webhook_deliveries WHERE tenant_id = $1 AND user_id = $2`, e.tenantA, u)

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
	var attempts int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.webhook_attempts WHERE tenant_id = $1`,
		e.tenantA).Scan(&attempts))
	assert.Zero(t, attempts, "attempts go with the user's deliveries")
}

type attemptJSON struct {
	Attempt             int       `json:"attempt"`
	StartedAt           time.Time `json:"started_at"`
	DurationMS          int       `json:"duration_ms"`
	StatusCode          *int      `json:"status_code"`
	Error               *string   `json:"error"`
	ResponseBodyExcerpt *string   `json:"response_body_excerpt"`
}

type deliveryDetailJSON struct {
	deliveryJSON
	ReplayOf *int64          `json:"replay_of"`
	Payload  json.RawMessage `json:"payload"`
	History  []attemptJSON   `json:"attempt_history"`
}

// The sandbox: a receiver that fails then accepts, the attempt history, a
// replay with the same webhook-id, and a sample of a chosen event type.
func TestWebhookSandbox(t *testing.T) {
	e := webhookEnv(t, true)
	ctx := context.Background()
	var mu sync.Mutex
	codes := []int{http.StatusInternalServerError}
	var ids []string
	var bodies [][]byte
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		ids = append(ids, r.Header.Get(webhook.HeaderID))
		bodies = append(bodies, body)
		code := http.StatusOK
		if len(codes) > 0 {
			code, codes = codes[0], codes[1:]
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, `{"received":`+strconv.FormatBool(code == http.StatusOK)+`}`)
	}))
	t.Cleanup(rcv.Close)
	worker := webhook.NewWorker(e.pool, webhookBox(t), webhook.WorkerConfig{
		AllowPrivate: true, Delays: make([]time.Duration, webhook.MaxAttempts-1),
	})
	// drain runs passes until nothing is due and returns what the receiver
	// got so far.
	drain := func() ([]string, [][]byte) {
		for {
			n, err := worker.Pass(ctx)
			require.NoError(t, err)
			if n == 0 {
				break
			}
		}
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(ids), slices.Clone(bodies)
	}

	r := e.do("POST", "/v1/webhooks", e.keyA, map[string]any{"url": rcv.URL + "/hooks"})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var wh webhookJSON
	r.json(t, &wh)
	path := "/v1/webhooks/" + wh.ID.String()

	// A sample data.written, failed once, then accepted.
	assert.Equal(t, http.StatusBadRequest, e.do("POST", path+"/test", e.keyA, map[string]any{"event_type": "data.everything"}).code)
	tr := e.do("POST", path+"/test", e.keyA, map[string]any{"event_type": "data.written"})
	require.Equal(t, http.StatusAccepted, tr.code, string(tr.body))
	var d deliveryJSON
	tr.json(t, &d)
	assert.Equal(t, "data.written", d.EventType)
	got, sent := drain()
	require.Len(t, got, 2)

	dpath := path + "/deliveries/" + jsonNumber(d.ID)
	g := e.do("GET", dpath, e.keyA, nil)
	require.Equal(t, http.StatusOK, g.code, string(g.body))
	var detail deliveryDetailJSON
	g.json(t, &detail)
	assert.Equal(t, "done", detail.Status)
	assert.Nil(t, detail.ReplayOf)
	assert.Equal(t, string(sent[0]), string(detail.Payload))
	var payload struct {
		Type string         `json:"type"`
		Test bool           `json:"test"`
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(detail.Payload, &payload))
	assert.Equal(t, "data.written", payload.Type)
	assert.True(t, payload.Test)
	assert.Equal(t, uuid.Nil.String(), payload.Data["user_id"])
	require.Len(t, detail.History, 2)
	require.NotNil(t, detail.History[0].StatusCode)
	assert.Equal(t, 500, *detail.History[0].StatusCode)
	require.NotNil(t, detail.History[0].Error)
	require.NotNil(t, detail.History[0].ResponseBodyExcerpt)
	assert.Equal(t, `{"received":false}`, *detail.History[0].ResponseBodyExcerpt)
	require.NotNil(t, detail.History[1].StatusCode)
	assert.Equal(t, 200, *detail.History[1].StatusCode)
	assert.Nil(t, detail.History[1].Error)
	assert.Equal(t, []int{1, 2}, []int{detail.History[0].Attempt, detail.History[1].Attempt})

	// Replay: a new delivery of the same event.
	rp := e.do("POST", dpath+"/replay", e.keyA, nil)
	require.Equal(t, http.StatusAccepted, rp.code, string(rp.body))
	var replay deliveryDetailJSON
	rp.json(t, &replay)
	assert.NotEqual(t, d.ID, replay.ID)
	assert.Equal(t, d.EventID, replay.EventID)
	assert.Equal(t, "pending", replay.Status)
	require.NotNil(t, replay.ReplayOf)
	assert.Equal(t, d.ID, *replay.ReplayOf)
	assert.Equal(t, path+"/deliveries/"+jsonNumber(replay.ID), rp.header.Get("Location"))
	rpath := path + "/deliveries/" + jsonNumber(replay.ID)
	assert.Equal(t, http.StatusConflict, e.do("POST", rpath+"/replay", e.keyA, nil).code, "pending")
	got, sent = drain()
	require.Len(t, got, 3)
	assert.Equal(t, got[0], got[2], "same webhook-id")
	assert.Equal(t, sent[0], sent[2], "same payload")
	var replayed deliveryDetailJSON
	e.do("GET", rpath, e.keyA, nil).json(t, &replayed)
	assert.Equal(t, "done", replayed.Status)
	assert.Len(t, replayed.History, 1)

	// The cap on queued replays.
	for range webhook.MaxQueuedReplays {
		require.Equal(t, http.StatusAccepted, e.do("POST", dpath+"/replay", e.keyA, nil).code)
	}
	assert.Equal(t, http.StatusTooManyRequests, e.do("POST", dpath+"/replay", e.keyA, nil).code)

	// Another tenant sees nothing; unknown ids are 404.
	assert.Equal(t, http.StatusNotFound, e.do("GET", dpath, e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("POST", dpath+"/replay", e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", path+"/deliveries/999999", e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("POST", path+"/deliveries/999999/replay", e.keyA, nil).code)

	// The generic test event still works without a body.
	tr = e.do("POST", path+"/test", e.keyA, nil)
	require.Equal(t, http.StatusAccepted, tr.code, string(tr.body))
	tr.json(t, &d)
	assert.Equal(t, "webhook.test", d.EventType)
}
