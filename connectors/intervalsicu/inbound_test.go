// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func post(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/inbound/intervals_icu", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestInboundActivity(t *testing.T) {
	c, _, _ := newTestConnector(t, 0)
	evs, err := c.HandleInbound(context.Background(), post(`{"secret":"test-webhook-secret","events":[
		{"athlete_id":"i900001","type":"ACTIVITY_UPLOADED","timestamp":"2026-03-03T10:00:00Z",
		 "activity":{"id":"i1001","start_date":"2026-03-01T07:00:00Z","start_date_local":"2026-03-01T08:00:00"}}]}`))
	require.NoError(t, err)
	start := time.Date(2026, 3, 1, 7, 0, 0, 0, time.UTC)
	assert.Equal(t, []connector.InboundEvent{{
		ProviderUserID: "i900001", DataType: DataTypeActivity,
		From: start.Add(-24 * time.Hour), To: start.Add(24 * time.Hour),
	}}, evs)
}

func TestInboundActivityWithoutStart(t *testing.T) {
	c, _, _ := newTestConnector(t, 0)
	evs, err := c.HandleInbound(context.Background(), post(`{"secret":"test-webhook-secret","events":[
		{"athlete_id":"i900001","type":"ACTIVITY_ANALYZED"}]}`))
	require.NoError(t, err)
	require.Len(t, evs, 1)
	assert.Equal(t, fakeNow.Add(-7*24*time.Hour), evs[0].From)
	assert.Equal(t, fakeNow, evs[0].To)
}

func TestInboundWellnessAndMerge(t *testing.T) {
	c, _, _ := newTestConnector(t, 0)
	evs, err := c.HandleInbound(context.Background(), post(`{"secret":"test-webhook-secret","events":[
		{"athlete_id":"i900001","type":"WELLNESS_UPDATED","wellness":{"id":"2026-03-02"}},
		{"athlete_id":"i900001","type":"WELLNESS_UPDATED","wellness":{"id":"2026-02-27"}},
		{"athlete_id":"i900002","type":"WELLNESS_UPDATED"},
		{"athlete_id":"i900001","type":"CALENDAR_UPDATED"},
		{"athlete_id":"i900001","type":"SPORT_SETTINGS_UPDATED"}]}`))
	require.NoError(t, err)
	require.Len(t, evs, 2)
	assert.Equal(t, connector.InboundEvent{
		ProviderUserID: "i900001", DataType: DataTypeWellness,
		From: day("2026-02-26"), To: day("2026-03-04"),
	}, evs[0], "two events of one athlete merge into one pull")
	assert.Equal(t, connector.InboundEvent{
		ProviderUserID: "i900002", DataType: DataTypeWellness,
		From: day("2026-02-28"), To: day("2026-03-04"),
	}, evs[1])
}

func TestInboundNumericAthleteID(t *testing.T) {
	c, _, _ := newTestConnector(t, 0)
	evs, err := c.HandleInbound(context.Background(), post(`{"secret":"test-webhook-secret","events":[{"athlete_id":2049151,"type":"ACTIVITY_UPLOADED"}]}`))
	require.NoError(t, err)
	require.Len(t, evs, 1)
	assert.Equal(t, "2049151", evs[0].ProviderUserID)
}

func TestInboundSecret(t *testing.T) {
	c, _, _ := newTestConnector(t, 0)
	for _, body := range []string{
		`{"events":[{"athlete_id":"i1","type":"ACTIVITY_UPLOADED"}]}`,
		`{"secret":"","events":[]}`,
		`{"secret":"wrong","events":[{"athlete_id":"i1","type":"ACTIVITY_UPLOADED"}]}`,
		`{"secret":"test-webhook-secret-longer","events":[]}`,
	} {
		_, err := c.HandleInbound(context.Background(), post(body))
		assert.ErrorIs(t, err, ErrBadSecret, body)
	}
}

func TestInboundOffWithoutSecretKey(t *testing.T) {
	c := New(Options{ClientID: "id", ClientSecret: "s"})
	_, err := c.HandleInbound(context.Background(), post(`{"secret":"","events":[]}`))
	assert.ErrorIs(t, err, ErrWebhookNotConfigured)
}

func TestInboundMalformed(t *testing.T) {
	c, _, _ := newTestConnector(t, 0)
	for _, body := range []string{
		`not json`,
		`{"secret":"test-webhook-secret","events":[]} {}`,
		`{"secret":"test-webhook-secret","events":[{"type":"ACTIVITY_UPLOADED"}]}`,
	} {
		_, err := c.HandleInbound(context.Background(), post(body))
		assert.Error(t, err, body)
		assert.NotErrorIs(t, err, ErrBadSecret)
	}
}

func TestInboundProbe(t *testing.T) {
	c, _, _ := newTestConnector(t, 0)
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		evs, err := c.HandleInbound(context.Background(), httptest.NewRequest(m, "/inbound/intervals_icu", nil))
		assert.NoError(t, err)
		assert.Empty(t, evs)
	}
}
