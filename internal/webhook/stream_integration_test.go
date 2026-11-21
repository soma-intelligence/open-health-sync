// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package webhook_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

func (e *env) streamTypes() []string {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT event_type FROM ohs_core.stream_events WHERE tenant_id = $1 ORDER BY seq`, e.tenant)
	require.NoError(e.t, err)
	types, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(e.t, err)
	return types
}

// Every event is written once for the stream, with the payload a delivery
// carries, and announced on the channel at commit only.
func TestEnqueueRecordsTheStream(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	listener, err := pgx.ConnectConfig(ctx, e.pool.Config().ConnConfig.Copy())
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close(context.Background()) })
	_, err = listener.Exec(ctx, "LISTEN "+webhook.StreamChannel)
	require.NoError(t, err)

	// No endpoint: the stream still gets the event.
	tx, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	u := e.user
	n, err := webhook.Enqueue(ctx, tx, webhook.Event{Tenant: e.tenant, User: &u, Type: webhook.DataWritten,
		Data: map[string]any{"provider": "oura", "first_day": "2026-10-11"}})
	require.NoError(t, err)
	assert.Zero(t, n, "no endpoint, no delivery")
	wctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	_, err = listener.WaitForNotification(wctx)
	cancel()
	require.Error(t, err, "nothing is announced before the commit")
	require.NoError(t, tx.Commit(ctx))

	wctx, cancel = context.WithTimeout(ctx, 5*time.Second)
	note, err := listener.WaitForNotification(wctx)
	cancel()
	require.NoError(t, err)
	tenant, seq, ok := webhook.ParseStreamNotification(note.Payload)
	require.True(t, ok, note.Payload)
	assert.Equal(t, e.tenant, tenant)
	assert.Equal(t, uuid.MustParse(e.tenant.String()).String()+":", note.Payload[:37], "ids only")

	var raw []byte
	var typ string
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT event_type, payload FROM ohs_core.stream_events WHERE seq = $1`, seq).
		Scan(&typ, &raw))
	assert.Equal(t, webhook.DataWritten, typ)
	var p struct {
		ID   uuid.UUID      `json:"id"`
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &p))
	assert.Equal(t, webhook.DataWritten, p.Type)
	assert.Equal(t, map[string]any{"provider": "oura", "first_day": "2026-10-11", "user_id": e.user.String(), "external_ref": "u-1"}, p.Data)

	// A rolled back event leaves nothing.
	tx, err = e.pool.Begin(ctx)
	require.NoError(t, err)
	_, err = webhook.Enqueue(ctx, tx, webhook.Event{Tenant: e.tenant, User: &u, Type: webhook.TagCreated})
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))
	assert.Equal(t, []string{webhook.DataWritten}, e.streamTypes())
}

// A user's erasure leaves only user.deleted about them.
func TestStreamUserDeletedDropsTheUsersEvents(t *testing.T) {
	e := newEnv(t)
	e.enqueue(webhook.DataWritten)
	e.enqueue(webhook.ConnectionBroken)
	_, err := webhook.Enqueue(context.Background(), e.pool, webhook.Event{Tenant: e.tenant, Type: webhook.DataWritten})
	require.NoError(t, err, "an event about no user")
	e.enqueue(webhook.UserDeleted)
	assert.Equal(t, []string{webhook.DataWritten, webhook.UserDeleted}, e.streamTypes())
}

func TestStreamRecordingOffAndPrune(t *testing.T) {
	e := newEnv(t)
	webhook.SetStreamRecording(false)
	e.enqueue(webhook.DataWritten)
	webhook.SetStreamRecording(true)
	assert.Empty(t, e.streamTypes(), "OHS_STREAM_MAX=0 writes nothing")

	e.enqueue(webhook.DataWritten)
	e.enqueue(webhook.DataDeleted)
	ctx := context.Background()
	_, err := e.pool.Exec(ctx, `UPDATE ohs_core.stream_events SET created_at = now() - interval '25 hours'
		WHERE event_type = $1`, webhook.DataWritten)
	require.NoError(t, err)
	require.NoError(t, e.worker().Prune(ctx))
	assert.Equal(t, []string{webhook.DataDeleted}, e.streamTypes())
}
