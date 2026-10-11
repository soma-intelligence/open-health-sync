// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

var inboundNow = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)

const testSecret = "test-client-secret"

func inboundConnector() *Connector {
	return New(Options{ClientID: "test-client-id", ClientSecret: testSecret, Now: func() time.Time { return inboundNow }})
}

// webhookAt builds a WHOOP webhook POST signed with secret at sentAt.
func webhookAt(secret string, sentAt time.Time, body string) *http.Request {
	ts := strconv.FormatInt(sentAt.UnixMilli(), 10)
	r := httptest.NewRequest(http.MethodPost, "/inbound/whoop", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(HeaderTimestamp, ts)
	r.Header.Set(HeaderSignature, Sign(secret, ts, []byte(body)))
	return r
}

func event(eventType string) string {
	return `{"user_id":10001,"id":"aaaaaaaa-0000-4000-8000-000000000001","type":"` + eventType + `","trace_id":"trace-1"}`
}

func TestHandleInbound_Valid(t *testing.T) {
	c := inboundConnector()
	events, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow.Add(-30*time.Second), event("sleep.updated")))
	require.NoError(t, err)
	require.Len(t, events, 1)
	e := events[0]
	assert.Equal(t, "10001", e.ProviderUserID)
	assert.Equal(t, DataTypeSleep, e.DataType)
	assert.True(t, e.From.Equal(inboundNow.Add(-InboundLookback)))
	assert.True(t, e.To.Equal(inboundNow), "to = now")
}

func TestHandleInbound_UpdateTypes(t *testing.T) {
	c := inboundConnector()
	for typ, dt := range map[string]string{
		EventRecoveryUpdated: DataTypeRecovery,
		EventSleepUpdated:    DataTypeSleep,
		EventWorkoutUpdated:  DataTypeWorkout,
	} {
		events, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow, event(typ)))
		require.NoError(t, err, typ)
		require.Len(t, events, 1, typ)
		assert.Equal(t, dt, events[0].DataType, typ)
	}
}

// A deleted sleep or workout comes back as the verbatim body to store, which
// Map turns into the deletion of that id.
func TestHandleInbound_DeletionIsStoredAndMapped(t *testing.T) {
	c := inboundConnector()
	for _, typ := range []string{EventSleepDeleted, EventWorkoutDeleted} {
		body := event(typ)
		events, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow, body))
		require.NoError(t, err, typ)
		require.Len(t, events, 1, typ)
		ev := events[0]
		assert.Equal(t, "10001", ev.ProviderUserID)
		assert.Equal(t, DataTypeWebhook, ev.DataType)
		assert.Equal(t, [][]byte{[]byte(body)}, ev.Lines, "stored verbatim")

		var fields map[string]any
		require.NoError(t, json.Unmarshal(ev.Lines[0], &fields))
		res, err := c.Map(connector.RawRecord{UserID: "u-1", Provider: Slug, DataType: DataTypeWebhook, Fields: fields})
		require.NoError(t, err, typ)
		assert.Equal(t, []model.Deletion{{Provider: Slug, ExternalID: "aaaaaaaa-0000-4000-8000-000000000001"}}, res.Deletions, typ)
		assert.Equal(t, 1, res.RecordCount(), typ)
	}
}

// recovery.deleted names the sleep, while recoveries are stored by cycle: it
// is acknowledged and not applied, as is a type the connector does not know.
func TestHandleInbound_RecoveryDeletionAndUnknownGiveNoEvent(t *testing.T) {
	c := inboundConnector()
	for _, typ := range []string{EventRecoveryDeleted, "cycle.updated", "body.updated"} {
		events, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow, event(typ)))
		require.NoError(t, err, typ)
		assert.Empty(t, events, typ)
	}
}

func TestHandleInbound_DeletionWithoutIDIsRefused(t *testing.T) {
	c := inboundConnector()
	_, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow,
		`{"user_id":10001,"type":"sleep.deleted","trace_id":"trace-1"}`))
	assert.Error(t, err)
}

// A stored webhook line that is not an applied deletion maps to nothing.
func TestMapWebhook_OtherTypesGiveNothing(t *testing.T) {
	c := inboundConnector()
	for _, typ := range []string{EventSleepUpdated, EventRecoveryDeleted} {
		var fields map[string]any
		require.NoError(t, json.Unmarshal([]byte(event(typ)), &fields))
		res, err := c.Map(connector.RawRecord{UserID: "u-1", Provider: Slug, DataType: DataTypeWebhook, Fields: fields})
		require.NoError(t, err)
		assert.True(t, res.IsEmpty(), typ)
	}
}

func TestEventToDataType(t *testing.T) {
	assert.Equal(t, DataTypeRecovery, EventToDataType("recovery.updated"))
	assert.Equal(t, DataTypeSleep, EventToDataType("sleep.updated"))
	assert.Equal(t, DataTypeWorkout, EventToDataType("workout.updated"))
	assert.Equal(t, "", EventToDataType("workout.deleted"))
	assert.Equal(t, "", EventToDataType("nope"))
}

func TestHandleInbound_BadSignature(t *testing.T) {
	c := inboundConnector()
	body := event("sleep.updated")

	t.Run("missing headers", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/inbound/whoop", strings.NewReader(body))
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature)
	})
	t.Run("missing signature", func(t *testing.T) {
		r := webhookAt(testSecret, inboundNow, body)
		r.Header.Del(HeaderSignature)
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature)
	})
	t.Run("wrong", func(t *testing.T) {
		r := webhookAt(testSecret, inboundNow, body)
		r.Header.Set(HeaderSignature, "c2lnbmF0dXJl")
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature)
	})
	t.Run("another secret", func(t *testing.T) {
		_, err := c.HandleInbound(context.Background(), webhookAt("another-secret", inboundNow, body))
		assert.ErrorIs(t, err, ErrBadSignature)
	})
	t.Run("body changed after signing", func(t *testing.T) {
		r := webhookAt(testSecret, inboundNow, body)
		r.Body = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Replace(body, "10001", "10002", 1))).Body
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature)
	})
	t.Run("timestamp changed after signing", func(t *testing.T) {
		r := webhookAt(testSecret, inboundNow, body)
		r.Header.Set(HeaderTimestamp, strconv.FormatInt(inboundNow.Add(time.Second).UnixMilli(), 10))
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature)
	})
	t.Run("timestamp not a number", func(t *testing.T) {
		r := webhookAt(testSecret, inboundNow, body)
		r.Header.Set(HeaderTimestamp, "soon")
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature)
	})
}

// A correctly signed delivery outside the tolerance is a replay.
func TestHandleInbound_Stale(t *testing.T) {
	c := inboundConnector()
	body := event("workout.updated")
	for _, sent := range []time.Time{
		inboundNow.Add(-SignatureTolerance - time.Second),
		inboundNow.Add(-time.Hour),
		inboundNow.Add(SignatureTolerance + time.Second),
	} {
		_, err := c.HandleInbound(context.Background(), webhookAt(testSecret, sent, body))
		assert.ErrorIs(t, err, ErrBadSignature, "sent %v", sent)
	}
	events, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow.Add(-SignatureTolerance+time.Second), body))
	require.NoError(t, err, "inside the tolerance")
	assert.Len(t, events, 1)
}

// A signed body that is not the documented JSON is an error, not a
// signature failure.
func TestHandleInbound_Malformed(t *testing.T) {
	c := inboundConnector()
	for _, body := range []string{`not json`, `{"type":"sleep.updated"}`, `{"user_id":10001}`, `[]`} {
		_, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow, body))
		require.Error(t, err, body)
		assert.NotErrorIs(t, err, ErrBadSignature, body)
	}
}

func TestHandleInbound_TooLarge(t *testing.T) {
	c := inboundConnector()
	body := `{"user_id":10001,"type":"sleep.updated","pad":"` + strings.Repeat("x", maxInboundBody) + `"}`
	_, err := c.HandleInbound(context.Background(), webhookAt(testSecret, inboundNow, body))
	assert.Error(t, err)
}

func TestHandleInbound_ReachabilityCheck(t *testing.T) {
	c := inboundConnector()
	for _, m := range []string{http.MethodHead, http.MethodGet} {
		events, err := c.HandleInbound(context.Background(), httptest.NewRequest(m, "/inbound/whoop", nil))
		require.NoError(t, err, m)
		assert.Empty(t, events, m)
	}
}

func TestHandleInbound_NotConfigured(t *testing.T) {
	_, err := New(Options{}).HandleInbound(context.Background(), webhookAt(testSecret, time.Now(), event("sleep.updated")))
	assert.ErrorIs(t, err, errNotConfigured)
}

// The documented algorithm: base64 of HMAC-SHA256(timestamp + body), keyed by
// the client secret. Computed independently for one fixed input.
func TestSign_KnownValue(t *testing.T) {
	got := Sign("secret", "1700000000000", []byte(`{"a":1}`))
	assert.Equal(t, "0pM2GdttJSewPFhxsH1nVKvgeUeQD/mx7t0F5acVYpI=", got)
}
