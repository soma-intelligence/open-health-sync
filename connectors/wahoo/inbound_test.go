// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/wahoo/wahoofake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func post(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/inbound/wahoo", strings.NewReader(body))
}

func TestHandleInbound_StoresTheSummaryWithoutTheToken(t *testing.T) {
	c := New(Options{ClientID: "id", ClientSecret: "s", WebhookToken: "fake-webhook-token"})
	evs, err := c.HandleInbound(context.Background(), post(string(wahoofake.File("webhook.json"))))
	require.NoError(t, err)
	require.Len(t, evs, 1)
	ev := evs[0]
	assert.Equal(t, "60462", ev.ProviderUserID)
	assert.Equal(t, DataTypeWebhook, ev.DataType)
	require.Len(t, ev.Lines, 1)
	assert.NotContains(t, string(ev.Lines[0]), "fake-webhook-token", "the token is never stored")
	assert.NotContains(t, string(ev.Lines[0]), "\n")

	// The stored line maps to the workout.
	var f map[string]any
	require.NoError(t, json.Unmarshal(ev.Lines[0], &f))
	res, err := c.Map(connector.RawRecord{UserID: "u1", DataType: ev.DataType, Fields: f})
	require.NoError(t, err)
	require.Len(t, res.Workouts, 1)
	assert.Equal(t, "56519", res.Workouts[0].ExternalID)
}

func TestHandleInbound_Refusals(t *testing.T) {
	body := string(wahoofake.File("webhook.json"))
	off := New(Options{ClientID: "id", ClientSecret: "s"})
	_, err := off.HandleInbound(context.Background(), post(body))
	assert.ErrorIs(t, err, ErrWebhookNotConfigured)

	c := New(Options{ClientID: "id", ClientSecret: "s", WebhookToken: "other-token"})
	_, err = c.HandleInbound(context.Background(), post(body))
	assert.ErrorIs(t, err, ErrBadWebhookToken)

	c = New(Options{ClientID: "id", ClientSecret: "s", WebhookToken: "fake-webhook-token"})
	for name, b := range map[string]string{
		"no token":      `{"event_type":"workout_summary","user":{"id":1}}`,
		"number token":  `{"event_type":"workout_summary","webhook_token":123}`,
		"not json":      `{"event_type":`,
		"trailing":      body + `{}`,
		"no user":       `{"event_type":"workout_summary","webhook_token":"fake-webhook-token","workout_summary":{"workout":{"id":1}}}`,
		"no workout id": `{"event_type":"workout_summary","webhook_token":"fake-webhook-token","user":{"id":1},"workout_summary":{"id":2}}`,
	} {
		_, err := c.HandleInbound(context.Background(), post(b))
		assert.Error(t, err, name)
	}

	evs, err := c.HandleInbound(context.Background(), post(`{"event_type":"plan_updated","webhook_token":"fake-webhook-token"}`))
	require.NoError(t, err)
	assert.Empty(t, evs, "another event type is acknowledged and ignored")

	evs, err = c.HandleInbound(context.Background(), httptest.NewRequest(http.MethodHead, "/inbound/wahoo", nil))
	require.NoError(t, err)
	assert.Empty(t, evs)
}
