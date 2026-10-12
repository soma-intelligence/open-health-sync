// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Clock skew both ways: within the tolerance either side is accepted, past
// it either side is refused.
func TestHandleInbound_ClockSkewBothWays(t *testing.T) {
	c := inboundConnector()
	body := event(EventSleepUpdated)
	for _, d := range []time.Duration{-SignatureTolerance + time.Second, -time.Minute, 0, time.Minute, SignatureTolerance - time.Second} {
		_, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow.Add(d), body))
		assert.NoError(t, err, d)
	}
	for _, d := range []time.Duration{-SignatureTolerance - time.Second, SignatureTolerance + time.Second, -48 * time.Hour, 48 * time.Hour} {
		_, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow.Add(d), body))
		assert.ErrorIs(t, err, ErrBadSignature, d)
	}
	for _, ts := range []string{"-1", "99999999999999999999", "1e12", " 1772496000000"} {
		r := webhookAt(testSecret, inboundNow, body)
		r.Header.Set(HeaderTimestamp, ts)
		r.Header.Set(HeaderSignature, Sign(testSecret, ts, []byte(body)))
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature, ts)
	}
}

// A delivery replayed within the tolerance gives the same events again: the
// core stores a deletion once (same bytes) and applies it once, and an update
// queues one more short pull. Refusing it could lose a retry WHOOP sent
// because our first answer did not reach it.
func TestHandleInbound_ReplayWithinToleranceIsIdempotent(t *testing.T) {
	c := inboundConnector()
	sent := inboundNow.Add(-time.Minute)
	for _, typ := range []string{EventSleepDeleted, EventRecoveryUpdated} {
		first, err := c.HandleInbound(context.Background(), webhookAt(testSecret, sent, event(typ)))
		require.NoError(t, err)
		again, err := c.HandleInbound(context.Background(), webhookAt(testSecret, sent, event(typ)))
		require.NoError(t, err)
		assert.Equal(t, first, again, typ)
	}
}

// A pretty printed deletion is stored as one line (NDJSON), still JSON, and
// maps to the same deletion. The signature covers the bytes WHOOP sent.
func TestHandleInbound_DeletionBodyIsOneLine(t *testing.T) {
	c := inboundConnector()
	body := "{\n  \"user_id\": 10001,\n  \"id\": \"bbbbbbbb-0000-4000-8000-000000000001\",\n  \"type\": \"workout.deleted\",\n  \"trace_id\": \"t\"\n}\n"
	events, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow, body))
	require.NoError(t, err)
	require.Len(t, events, 1)
	line := events[0].Lines[0]
	assert.False(t, bytes.ContainsAny(line, "\n\r"))
	var fields map[string]any
	require.NoError(t, json.Unmarshal(line, &fields))
	res, err := c.Map(connector.RawRecord{UserID: "u", Provider: Slug, DataType: DataTypeWebhook, Fields: fields})
	require.NoError(t, err)
	require.Len(t, res.Deletions, 1)
	assert.Equal(t, "bbbbbbbb-0000-4000-8000-000000000001", res.Deletions[0].ExternalID)
}

// Bodies the documented shape does not allow are errors (the route answers
// 400 and WHOOP retries), never a panic, and never a signature failure.
func TestHandleInbound_MalformedBodies(t *testing.T) {
	c := inboundConnector()
	for _, body := range []string{
		``, `null`, `"x"`, `{`, `{"user_id":{},"type":"sleep.updated"}`, `{"user_id":10001,"type":7}`,
		`{"user_id":10001,"id":[1],"type":"sleep.deleted"}`, `{"user_id":10001,"type":"workout.deleted","id":""}`,
	} {
		_, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow, body))
		require.Error(t, err, body)
		assert.NotErrorIs(t, err, ErrBadSignature, body)
	}
}

// The size cap: a body at the cap is read, one byte over is refused.
func TestHandleInbound_SizeCap(t *testing.T) {
	c := inboundConnector()
	head := `{"user_id":10001,"type":"sleep.updated","pad":"`
	at := head + strings.Repeat("x", maxInboundBody-len(head)-2) + `"}`
	require.Len(t, at, maxInboundBody)
	_, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow, at))
	assert.NoError(t, err)
	over := head + strings.Repeat("x", maxInboundBody-len(head)-1) + `"}`
	_, err = c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow, over))
	assert.Error(t, err)
}

// Every deletion type, by its id as a v2 webhook sends it.
func TestHandleInbound_EachDeletionType(t *testing.T) {
	c := inboundConnector()
	for typ, want := range map[string]string{
		EventSleepDeleted:    "aaaaaaaa-0000-4000-8000-000000000001",
		EventWorkoutDeleted:  "aaaaaaaa-0000-4000-8000-000000000001",
		EventRecoveryDeleted: "aaaaaaaa-0000-4000-8000-000000000001_recovery",
	} {
		events, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow, event(typ)))
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Empty(t, events[0].From, "a deletion pulls nothing")
		var fields map[string]any
		require.NoError(t, json.Unmarshal(events[0].Lines[0], &fields))
		res, err := c.Map(connector.RawRecord{UserID: "u", Provider: Slug, DataType: DataTypeWebhook, Fields: fields})
		require.NoError(t, err)
		require.Len(t, res.Deletions, 1, typ)
		assert.Equal(t, want, res.Deletions[0].ExternalID, typ)
		assert.Equal(t, Slug, res.Deletions[0].Provider)
		assert.Empty(t, res.QuantitySamples)
	}
}
