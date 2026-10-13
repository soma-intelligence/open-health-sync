// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package webhook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	box    *crypto.Box
	store  *webhook.Store
	tenant uuid.UUID
	user   uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	box, err := crypto.NewBox(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	e := &env{t: t, pool: pool, box: box, store: webhook.NewStore(pool, box)}
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ('t') RETURNING id`).Scan(&e.tenant))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO ohs_core.end_users (tenant_id, external_ref) VALUES ($1, 'u-1') RETURNING id`, e.tenant).Scan(&e.user))
	return e
}

// worker has no delay between attempts, so retries run on the next pass.
func (e *env) worker() *webhook.Worker {
	return webhook.NewWorker(e.pool, e.box, webhook.WorkerConfig{
		AllowPrivate: true, Delays: make([]time.Duration, webhook.MaxAttempts-1),
	})
}

func (e *env) enqueue(typ string) {
	e.t.Helper()
	u := e.user
	_, err := webhook.Enqueue(context.Background(), e.pool, webhook.Event{
		Tenant: e.tenant, User: &u, Type: typ, Data: map[string]any{"provider": "oura"},
	})
	require.NoError(e.t, err)
}

// receiver records requests and answers with the codes queued in status
// (204 once they run out).
type receiver struct {
	mu     sync.Mutex
	status []int
	reply  string // response body
	got    []*http.Request
	bodies [][]byte
	srv    *httptest.Server
}

func newReceiver(t *testing.T, status ...int) *receiver {
	r := &receiver{status: status}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, req)
		r.bodies = append(r.bodies, body)
		code := http.StatusNoContent
		if len(r.status) > 0 {
			code, r.status = r.status[0], r.status[1:]
		}
		reply := r.reply
		r.mu.Unlock()
		w.WriteHeader(code)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func pass(t *testing.T, w *webhook.Worker) int {
	t.Helper()
	n, err := w.Pass(context.Background())
	require.NoError(t, err)
	return n
}

func TestDeliverySignedAndFiltered(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rcv := newReceiver(t)
	ep, secret, err := e.store.Create(ctx, e.tenant, rcv.srv.URL, "app", []string{webhook.DataWritten})
	require.NoError(t, err)
	assert.Equal(t, []string{webhook.DataWritten}, ep.EventTypes)

	e.enqueue(webhook.DataWritten)
	e.enqueue(webhook.UserDeleted) // not subscribed
	assert.Equal(t, 1, pass(t, e.worker()))
	require.Equal(t, 1, rcv.count())

	req, body := rcv.got[0], rcv.bodies[0]
	require.NoError(t, webhook.Verify(secret, req.Header.Get(webhook.HeaderID), req.Header.Get(webhook.HeaderTimestamp),
		req.Header.Get(webhook.HeaderSignature), body, time.Now()))
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	var p struct {
		ID   string         `json:"id"`
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &p))
	assert.Equal(t, req.Header.Get(webhook.HeaderID), p.ID)
	assert.Equal(t, webhook.DataWritten, p.Type)
	assert.Equal(t, map[string]any{"provider": "oura", "user_id": e.user.String(), "external_ref": "u-1"}, p.Data)

	ds, next, err := e.store.ListDeliveries(ctx, e.tenant, ep.ID, webhook.DeliveryQuery{})
	require.NoError(t, err)
	assert.Empty(t, next)
	require.Len(t, ds, 1)
	assert.Equal(t, "done", ds[0].Status)
	assert.Equal(t, 1, ds[0].Attempts)
	assert.Equal(t, 204, *ds[0].LastStatus)
	got, err := e.store.Get(ctx, e.tenant, ep.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.LastSuccessAt)

	// Rotation: the next delivery verifies with the new secret only.
	secret2, err := e.store.RotateSecret(ctx, e.tenant, ep.ID)
	require.NoError(t, err)
	e.enqueue(webhook.DataWritten)
	pass(t, e.worker())
	req, body = rcv.got[1], rcv.bodies[1]
	ts, sig := req.Header.Get(webhook.HeaderTimestamp), req.Header.Get(webhook.HeaderSignature)
	assert.NoError(t, webhook.Verify(secret2, req.Header.Get(webhook.HeaderID), ts, sig, body, time.Now()))
	assert.ErrorIs(t, webhook.Verify(secret, req.Header.Get(webhook.HeaderID), ts, sig, body, time.Now()), webhook.ErrVerify)
}

func TestRetriesThenParks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rcv := newReceiver(t, 500, 503)
	ep, _, err := e.store.Create(ctx, e.tenant, rcv.srv.URL, "", []string{})
	require.NoError(t, err)
	e.enqueue(webhook.DataWritten)
	w := e.worker()
	pass(t, w)
	pass(t, w)
	got, err := e.store.Get(ctx, e.tenant, ep.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.FailingSince, "failing after two errors")
	pass(t, w)
	assert.Equal(t, 3, rcv.count())
	assert.Equal(t, 0, pass(t, w), "delivered on the third attempt")
	ids := map[string]bool{}
	for _, r := range rcv.got {
		ids[r.Header.Get(webhook.HeaderID)] = true
	}
	assert.Len(t, ids, 1, "every attempt carries the same webhook-id")
	got, err = e.store.Get(ctx, e.tenant, ep.ID)
	require.NoError(t, err)
	assert.Nil(t, got.FailingSince, "a success clears the failure")

	// A receiver that never accepts: the delivery parks after MaxAttempts.
	codes := make([]int, webhook.MaxAttempts)
	for i := range codes {
		codes[i] = 500
	}
	rcv.mu.Lock()
	rcv.status = codes
	rcv.mu.Unlock()
	e.enqueue(webhook.DataWritten)
	for range webhook.MaxAttempts {
		pass(t, w)
	}
	assert.Equal(t, 0, pass(t, w))
	ds, _, err := e.store.ListDeliveries(ctx, e.tenant, ep.ID, webhook.DeliveryQuery{Status: "parked"})
	require.NoError(t, err)
	require.Len(t, ds, 1)
	assert.Equal(t, webhook.MaxAttempts, ds[0].Attempts)
	assert.Equal(t, 500, *ds[0].LastStatus)
	assert.Contains(t, *ds[0].LastError, "HTTP 500")
	got, err = e.store.Get(ctx, e.tenant, ep.ID)
	require.NoError(t, err)
	assert.Equal(t, "active", got.Status, "failing for minutes only: not disabled")

	// Retry from the API: delivered again with the same event id.
	d, err := e.store.Retry(ctx, e.tenant, ep.ID, ds[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", d.Status)
	_, err = e.store.Retry(ctx, e.tenant, ep.ID, ds[0].ID)
	assert.ErrorIs(t, err, webhook.ErrNotRetryable)
	assert.Equal(t, 1, pass(t, w))
	ds, _, err = e.store.ListDeliveries(ctx, e.tenant, ep.ID, webhook.DeliveryQuery{Status: "done"})
	require.NoError(t, err)
	assert.Len(t, ds, 2)
}

func TestLongFailingEndpointIsDisabled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rcv := newReceiver(t, 410)
	ep, _, err := e.store.Create(ctx, e.tenant, rcv.srv.URL, "", []string{})
	require.NoError(t, err)
	_, err = e.pool.Exec(ctx, `UPDATE ohs_core.webhook_endpoints SET failing_since = now() - interval '6 days' WHERE id = $1`, ep.ID)
	require.NoError(t, err)
	e.enqueue(webhook.DataWritten)
	e.enqueue(webhook.DataDeleted)
	_, err = e.pool.Exec(ctx, `UPDATE ohs_core.webhook_deliveries SET attempts = $1 - 1
		WHERE id = (SELECT min(id) FROM ohs_core.webhook_deliveries)`, webhook.MaxAttempts)
	require.NoError(t, err)
	// Make the second delivery due later, so the pass takes the first only.
	_, err = e.pool.Exec(ctx, `UPDATE ohs_core.webhook_deliveries SET not_before = now() + interval '1 hour'
		WHERE id = (SELECT max(id) FROM ohs_core.webhook_deliveries)`)
	require.NoError(t, err)

	assert.Equal(t, 1, pass(t, e.worker()))
	got, err := e.store.Get(ctx, e.tenant, ep.ID)
	require.NoError(t, err)
	assert.Equal(t, "disabled", got.Status)
	require.NotNil(t, got.DisabledReason)
	assert.Contains(t, *got.DisabledReason, "5 days")
	ds, _, err := e.store.ListDeliveries(ctx, e.tenant, ep.ID, webhook.DeliveryQuery{Status: "parked"})
	require.NoError(t, err)
	assert.Len(t, ds, 2, "the pending delivery is parked with the endpoint")

	// Nothing is enqueued for a disabled endpoint; enabling it again does.
	e.enqueue(webhook.DataWritten)
	ds, _, err = e.store.ListDeliveries(ctx, e.tenant, ep.ID, webhook.DeliveryQuery{})
	require.NoError(t, err)
	assert.Len(t, ds, 2)
	on := true
	got, err = e.store.Update(ctx, e.tenant, ep.ID, webhook.Patch{Enabled: &on})
	require.NoError(t, err)
	assert.Equal(t, "active", got.Status)
	assert.Nil(t, got.DisabledReason)
	assert.Nil(t, got.FailingSince)
}

func TestTestEventAndTenantIsolation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rcv := newReceiver(t)
	ep, _, err := e.store.Create(ctx, e.tenant, rcv.srv.URL, "", []string{webhook.UserDeleted})
	require.NoError(t, err)
	d, err := e.store.SendTest(ctx, e.tenant, ep.ID)
	require.NoError(t, err)
	assert.Equal(t, webhook.Test, d.EventType)
	assert.Equal(t, 1, pass(t, e.worker()), "a test goes out whatever the event types")
	var p map[string]any
	require.NoError(t, json.Unmarshal(rcv.bodies[0], &p))
	assert.Equal(t, ep.ID.String(), p["data"].(map[string]any)["endpoint_id"])

	var other uuid.UUID
	require.NoError(t, e.pool.QueryRow(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ('o') RETURNING id`).Scan(&other))
	_, err = e.store.Get(ctx, other, ep.ID)
	assert.ErrorIs(t, err, webhook.ErrNotFound)
	_, err = e.store.SendTest(ctx, other, ep.ID)
	assert.ErrorIs(t, err, webhook.ErrNotFound)
	_, _, err = e.store.ListDeliveries(ctx, other, ep.ID, webhook.DeliveryQuery{})
	assert.ErrorIs(t, err, webhook.ErrNotFound)
	assert.ErrorIs(t, e.store.Delete(ctx, other, ep.ID), webhook.ErrNotFound)
	list, err := e.store.List(ctx, other)
	require.NoError(t, err)
	assert.Empty(t, list)

	require.NoError(t, e.store.Delete(ctx, e.tenant, ep.ID))
	var n int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.webhook_deliveries`).Scan(&n))
	assert.Zero(t, n, "deliveries go with their endpoint")
}

func TestSecretIsSealedAndEndpointsCapped(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, secret, err := e.store.Create(ctx, e.tenant, "https://example.com/a", "", []string{})
	require.NoError(t, err)
	var sealed []byte
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT secret_sealed FROM ohs_core.webhook_endpoints`).Scan(&sealed))
	assert.NotContains(t, string(sealed), secret[len(webhook.SecretPrefix):])
	for i := 1; i < webhook.MaxEndpoints; i++ {
		_, _, err := e.store.Create(ctx, e.tenant, "https://example.com/a", "", []string{})
		require.NoError(t, err)
	}
	_, _, err = e.store.Create(ctx, e.tenant, "https://example.com/a", "", []string{})
	assert.ErrorIs(t, err, webhook.ErrTooMany)
}

func TestDeliveriesPageAndPrune(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ep, _, err := e.store.Create(ctx, e.tenant, "https://example.com/a", "", []string{})
	require.NoError(t, err)
	for range 5 {
		e.enqueue(webhook.DataWritten)
	}
	ds, next, err := e.store.ListDeliveries(ctx, e.tenant, ep.ID, webhook.DeliveryQuery{Limit: 3})
	require.NoError(t, err)
	require.Len(t, ds, 3)
	require.NotEmpty(t, next)
	assert.Greater(t, ds[0].ID, ds[2].ID, "newest first")
	rest, next, err := e.store.ListDeliveries(ctx, e.tenant, ep.ID, webhook.DeliveryQuery{Limit: 3, Cursor: next})
	require.NoError(t, err)
	assert.Len(t, rest, 2)
	assert.Empty(t, next)
	_, _, err = e.store.ListDeliveries(ctx, e.tenant, ep.ID, webhook.DeliveryQuery{Cursor: "x"})
	assert.ErrorIs(t, err, webhook.ErrCursor)

	_, err = e.pool.Exec(ctx, `UPDATE ohs_core.webhook_deliveries SET status = 'done', created_at = now() - interval '31 days'
		WHERE id IN (SELECT id FROM ohs_core.webhook_deliveries ORDER BY id LIMIT 2)`)
	require.NoError(t, err)
	require.NoError(t, e.worker().Prune(ctx))
	ds, _, err = e.store.ListDeliveries(ctx, e.tenant, ep.ID, webhook.DeliveryQuery{})
	require.NoError(t, err)
	assert.Len(t, ds, 3)
}

func TestPrivateDestinationRefusedByDefault(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rcv := newReceiver(t)
	// Stored directly: the API would refuse the URL already.
	ep, _, err := e.store.Create(ctx, e.tenant, rcv.srv.URL, "", []string{})
	require.NoError(t, err)
	e.enqueue(webhook.DataWritten)
	w := webhook.NewWorker(e.pool, e.box, webhook.WorkerConfig{})
	pass(t, w)
	assert.Zero(t, rcv.count())
	ds, _, err := e.store.ListDeliveries(ctx, e.tenant, ep.ID, webhook.DeliveryQuery{})
	require.NoError(t, err)
	require.Len(t, ds, 1)
	assert.Equal(t, "pending", ds[0].Status)
	assert.Contains(t, *ds[0].LastError, "private or reserved")
	assert.Nil(t, ds[0].LastStatus)
}

// drain runs passes until nothing is due.
func drain(t *testing.T, w *webhook.Worker) {
	t.Helper()
	for pass(t, w) > 0 {
		continue
	}
}

func TestAttemptHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rcv := newReceiver(t, 500)
	rcv.reply = "{\"error\":\"db down\"}\r\n\x00"
	ep, _, err := e.store.Create(ctx, e.tenant, rcv.srv.URL, "", []string{})
	require.NoError(t, err)
	e.enqueue(webhook.DataWritten)
	w := e.worker()
	pass(t, w)
	pass(t, w)
	require.Equal(t, 2, rcv.count())

	ds, _, err := e.store.ListDeliveries(ctx, e.tenant, ep.ID, webhook.DeliveryQuery{})
	require.NoError(t, err)
	require.Len(t, ds, 1)
	d, err := e.store.GetDelivery(ctx, e.tenant, ep.ID, ds[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "done", d.Status)
	assert.Equal(t, string(rcv.bodies[0]), string(d.Payload), "the payload is the body sent, byte for byte")
	require.Len(t, d.History, 2)
	first, second := d.History[0], d.History[1]
	assert.Equal(t, 1, first.Attempt)
	require.NotNil(t, first.StatusCode)
	assert.Equal(t, 500, *first.StatusCode)
	require.NotNil(t, first.Error)
	assert.Contains(t, *first.Error, "HTTP 500")
	require.NotNil(t, first.ResponseBodyExcerpt)
	assert.Equal(t, "{\"error\":\"db down\"}\n?", *first.ResponseBodyExcerpt)
	assert.GreaterOrEqual(t, first.DurationMS, 0)
	assert.Equal(t, 2, second.Attempt)
	require.NotNil(t, second.StatusCode)
	assert.Equal(t, 204, *second.StatusCode)
	assert.Nil(t, second.Error)
	assert.Nil(t, second.ResponseBodyExcerpt, "204 has no body")
	assert.False(t, second.StartedAt.Before(first.StartedAt))

	// Another tenant sees nothing; an unknown id is not found.
	var other uuid.UUID
	require.NoError(t, e.pool.QueryRow(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ('o') RETURNING id`).Scan(&other))
	_, err = e.store.GetDelivery(ctx, other, ep.ID, d.ID)
	assert.ErrorIs(t, err, webhook.ErrNotFound)
	_, err = e.store.GetDelivery(ctx, e.tenant, ep.ID, d.ID+1000)
	assert.ErrorIs(t, err, webhook.ErrNotFound)

	// The history counts on across retries and keeps the last KeepAttempts.
	codes := make([]int, 3*webhook.MaxAttempts)
	for i := range codes {
		codes[i] = 503
	}
	rcv.mu.Lock()
	rcv.status, rcv.reply = codes, ""
	rcv.mu.Unlock()
	for range 3 {
		_, err := e.store.Retry(ctx, e.tenant, ep.ID, d.ID)
		require.NoError(t, err)
		for range webhook.MaxAttempts {
			pass(t, w)
		}
	}
	d, err = e.store.GetDelivery(ctx, e.tenant, ep.ID, d.ID)
	require.NoError(t, err)
	assert.Equal(t, "parked", d.Status)
	require.Len(t, d.History, webhook.KeepAttempts)
	last := 2 + 3*webhook.MaxAttempts
	assert.Equal(t, last-webhook.KeepAttempts+1, d.History[0].Attempt)
	assert.Equal(t, last, d.History[webhook.KeepAttempts-1].Attempt)

	// Attempts go with their delivery: deleting the user's deliveries (what
	// the deletion worker does) removes them.
	_, err = e.pool.Exec(ctx, `DELETE FROM ohs_core.webhook_deliveries WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user)
	require.NoError(t, err)
	var n int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.webhook_attempts`).Scan(&n))
	assert.Zero(t, n)
}

func TestReplay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rcv := newReceiver(t)
	ep, secret, err := e.store.Create(ctx, e.tenant, rcv.srv.URL, "", []string{})
	require.NoError(t, err)
	e.enqueue(webhook.DataWritten)
	w := e.worker()
	pass(t, w)
	ds, _, err := e.store.ListDeliveries(ctx, e.tenant, ep.ID, webhook.DeliveryQuery{})
	require.NoError(t, err)
	require.Len(t, ds, 1)
	orig := ds[0]
	require.Equal(t, "done", orig.Status)
	assert.Nil(t, orig.ReplayOf)

	r, err := e.store.Replay(ctx, e.tenant, ep.ID, orig.ID)
	require.NoError(t, err)
	assert.NotEqual(t, orig.ID, r.ID, "a new delivery")
	assert.Equal(t, orig.EventID, r.EventID)
	assert.Equal(t, orig.EventType, r.EventType)
	assert.Equal(t, "pending", r.Status)
	require.NotNil(t, r.ReplayOf)
	assert.Equal(t, orig.ID, *r.ReplayOf)
	_, err = e.store.Replay(ctx, e.tenant, ep.ID, r.ID)
	assert.ErrorIs(t, err, webhook.ErrNotRetryable, "a pending delivery is not replayed")

	assert.Equal(t, 1, pass(t, w))
	require.Equal(t, 2, rcv.count())
	assert.Equal(t, rcv.got[0].Header.Get(webhook.HeaderID), rcv.got[1].Header.Get(webhook.HeaderID))
	assert.Equal(t, rcv.bodies[0], rcv.bodies[1])
	require.NoError(t, webhook.Verify(secret, rcv.got[1].Header.Get(webhook.HeaderID),
		rcv.got[1].Header.Get(webhook.HeaderTimestamp), rcv.got[1].Header.Get(webhook.HeaderSignature), rcv.bodies[1], time.Now()))
	o, err := e.store.GetDelivery(ctx, e.tenant, ep.ID, orig.ID)
	require.NoError(t, err)
	assert.Len(t, o.History, 1, "the original keeps its own history")
	rd, err := e.store.GetDelivery(ctx, e.tenant, ep.ID, r.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", rd.Status)
	assert.Len(t, rd.History, 1)
	assert.Equal(t, string(o.Payload), string(rd.Payload))

	// At most MaxQueuedReplays wait at once per endpoint.
	for range webhook.MaxQueuedReplays {
		_, err := e.store.Replay(ctx, e.tenant, ep.ID, orig.ID)
		require.NoError(t, err)
	}
	_, err = e.store.Replay(ctx, e.tenant, ep.ID, orig.ID)
	assert.ErrorIs(t, err, webhook.ErrTooManyReplays)
	drain(t, w)
	_, err = e.store.Replay(ctx, e.tenant, ep.ID, orig.ID)
	assert.NoError(t, err, "sent replays free the queue")

	var other uuid.UUID
	require.NoError(t, e.pool.QueryRow(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ('o') RETURNING id`).Scan(&other))
	_, err = e.store.Replay(ctx, other, ep.ID, orig.ID)
	assert.ErrorIs(t, err, webhook.ErrNotFound)
	_, err = e.store.Replay(ctx, e.tenant, ep.ID, orig.ID+1000)
	assert.ErrorIs(t, err, webhook.ErrNotFound)

	// A replay names the same user, so a user's erasure removes it too.
	var n int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.webhook_deliveries
		WHERE replay_of IS NOT NULL AND user_id IS DISTINCT FROM $1`, e.user).Scan(&n))
	assert.Zero(t, n)
}

func TestSampleEvent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rcv := newReceiver(t)
	ep, _, err := e.store.Create(ctx, e.tenant, rcv.srv.URL, "", []string{webhook.UserDeleted})
	require.NoError(t, err)
	d, err := e.store.SendSample(ctx, e.tenant, ep.ID, webhook.DataWritten)
	require.NoError(t, err)
	assert.Equal(t, webhook.DataWritten, d.EventType)
	_, err = e.store.SendSample(ctx, e.tenant, ep.ID, "data.everything")
	assert.ErrorIs(t, err, webhook.ErrUnknownType)
	_, err = e.store.SendTest(ctx, e.tenant, ep.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, pass(t, e.worker()), "samples go out whatever the event types")

	byType := map[string]map[string]any{}
	for _, b := range rcv.bodies {
		var p map[string]any
		require.NoError(t, json.Unmarshal(b, &p))
		assert.Equal(t, true, p["test"], "every test-route event is marked")
		byType[p["type"].(string)] = p
	}
	sample := byType[webhook.DataWritten]
	require.NotNil(t, sample)
	data := sample["data"].(map[string]any)
	assert.Equal(t, uuid.Nil.String(), data["user_id"])
	assert.Equal(t, webhook.SampleUserRef, data["external_ref"])
	assert.Equal(t, "oura", data["provider"])
	assert.NotEmpty(t, data["first_day"])
	require.NotNil(t, byType[webhook.Test])
	assert.Equal(t, ep.ID.String(), byType[webhook.Test]["data"].(map[string]any)["endpoint_id"])
}
