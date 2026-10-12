// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testWebhookSecret = "test-webhook-secret"

func inboundConnector(secret string) *Connector {
	return New(Options{ClientID: testClientID, ClientSecret: testClientSecret, WebhookSecret: secret, Now: func() time.Time { return fakeNow }})
}

func webhook(body, signature string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/inbound/polar", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if signature != "" {
		r.Header.Set(HeaderSignature, signature)
	}
	return r
}

const exerciseEvent = `{"event":"EXERCISE","user_id":10101010,"entity_id":"EXC0001","timestamp":"2026-03-09T20:00:00Z","url":"https://www.polaraccesslink.com/v3/exercises/EXC0001"}`

func TestHandleInbound_Signed(t *testing.T) {
	c := inboundConnector(testWebhookSecret)
	evs, err := c.HandleInbound(context.Background(), webhook(exerciseEvent, Signature(testWebhookSecret, []byte(exerciseEvent))))
	require.NoError(t, err)
	require.Len(t, evs, 1)
	assert.Equal(t, testPolarUser, evs[0].ProviderUserID)
	assert.Equal(t, DataTypeExercise, evs[0].DataType)
	assert.Equal(t, time.Date(2026, 3, 9, 18, 0, 0, 0, time.UTC), evs[0].From)
	assert.Equal(t, fakeNow, evs[0].To)

	upper := strings.ToUpper(Signature(testWebhookSecret, []byte(exerciseEvent)))
	_, err = c.HandleInbound(context.Background(), webhook(exerciseEvent, upper))
	require.NoError(t, err, "hex case does not matter")
}

func TestHandleInbound_BadSignature(t *testing.T) {
	c := inboundConnector(testWebhookSecret)
	for name, sig := range map[string]string{
		"missing":    "",
		"wrong key":  Signature("other-secret", []byte(exerciseEvent)),
		"not hex":    "zz-not-hex",
		"other body": Signature(testWebhookSecret, []byte(exerciseEvent+" ")),
	} {
		_, err := c.HandleInbound(context.Background(), webhook(exerciseEvent, sig))
		assert.ErrorIs(t, err, ErrBadSignature, name)
	}
}

func TestHandleInbound_NoSecretAcceptsUnsigned(t *testing.T) {
	c := inboundConnector("")
	evs, err := c.HandleInbound(context.Background(), webhook(exerciseEvent, ""))
	require.NoError(t, err)
	require.Len(t, evs, 1)
}

func TestHandleInbound_Ping(t *testing.T) {
	c := inboundConnector(testWebhookSecret)
	evs, err := c.HandleInbound(context.Background(), webhook(`{"event":"PING","timestamp":"2026-03-09T20:00:00Z"}`, ""))
	require.NoError(t, err, "PING is answered before the operator holds the key")
	assert.Empty(t, evs)

	r := webhook(`{"timestamp":"2026-03-09T20:00:00Z"}`, "")
	r.Header.Set(HeaderEvent, "PING")
	evs, err = c.HandleInbound(context.Background(), r)
	require.NoError(t, err)
	assert.Empty(t, evs)
}

func TestHandleInbound_EventTypes(t *testing.T) {
	c := inboundConnector("")
	for event, want := range map[string]string{
		"EXERCISE":                     DataTypeExercise,
		"SLEEP":                        DataTypeSleep,
		"CONTINUOUS_HEART_RATE":        DataTypeContinuousHR,
		"ACTIVITY_SUMMARY":             DataTypeActivity,
		"PHYSICAL_INFORMATION":         DataTypePhysicalInfo,
		"SLEEP_WISE_ALERTNESS":         "",
		"SLEEP_WISE_CIRCADIAN_BEDTIME": "",
		"SOMETHING_NEW":                "",
	} {
		body := `{"event":"` + event + `","user_id":10101010,"timestamp":"2026-03-09T20:00:00Z"}`
		evs, err := c.HandleInbound(context.Background(), webhook(body, ""))
		require.NoError(t, err, event)
		if want == "" {
			assert.Empty(t, evs, event)
			continue
		}
		require.Len(t, evs, 1, event)
		assert.Equal(t, want, evs[0].DataType, event)
	}
}

func TestHandleInbound_Malformed(t *testing.T) {
	c := inboundConnector("")
	for name, body := range map[string]string{
		"not json":   `event=EXERCISE`,
		"no user":    `{"event":"EXERCISE","timestamp":"2026-03-09T20:00:00Z"}`,
		"no type":    `{"user_id":10101010}`,
		"empty body": ``,
	} {
		_, err := c.HandleInbound(context.Background(), webhook(body, ""))
		assert.Error(t, err, name)
	}
}

func TestHandleInbound_HeadAndGet(t *testing.T) {
	c := inboundConnector(testWebhookSecret)
	for _, m := range []string{http.MethodHead, http.MethodGet} {
		evs, err := c.HandleInbound(context.Background(), httptest.NewRequest(m, "/inbound/polar", nil))
		require.NoError(t, err)
		assert.Empty(t, evs)
	}
}

func TestInboundWindow(t *testing.T) {
	now := fakeNow
	floor := now.Add(-InboundLookback)
	cases := []struct {
		name     string
		ev       webhookEvent
		wantFrom time.Time
	}{
		{"recent timestamp", webhookEvent{Timestamp: "2026-03-09T20:00:00Z"}, time.Date(2026, 3, 9, 18, 0, 0, 0, time.UTC)},
		{"date earlier than timestamp", webhookEvent{Timestamp: "2026-03-09T20:00:00Z", Date: "2026-03-08"}, time.Date(2026, 3, 7, 22, 0, 0, 0, time.UTC)},
		{"ancient timestamp", webhookEvent{Timestamp: "1970-01-01T00:00:00Z"}, floor},
		{"no time", webhookEvent{}, floor},
		{"future", webhookEvent{Timestamp: "2027-01-01T00:00:00Z"}, now.Add(-InboundOverlap)},
	}
	for _, tc := range cases {
		from, to := inboundWindow(tc.ev, now)
		assert.Equal(t, tc.wantFrom, from, tc.name)
		assert.Equal(t, now, to, tc.name)
	}
}
