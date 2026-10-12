// SPDX-License-Identifier: AGPL-3.0-only

package strava

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

var inboundNow = time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)

func inboundConnector(subscriptionID string) *Connector {
	return New(Options{
		ClientID: "test-client-id", ClientSecret: "test-client-secret",
		VerifyToken: "test-verify-token", SubscriptionID: subscriptionID,
		Now: func() time.Time { return inboundNow },
	})
}

func post(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/inbound/strava", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

const (
	createEvent = `{"aspect_type":"create","event_time":1772539200,"object_id":90000000001,"object_type":"activity","owner_id":1000001,"subscription_id":4242,"updates":{}}`
	updateEvent = `{"aspect_type":"update","event_time":1772539200,"object_id":90000000001,"object_type":"activity","owner_id":1000001,"subscription_id":4242,"updates":{"title":"Invented","type":"Ride"}}`
	deleteEvent = `{"aspect_type":"delete","event_time":1772539200,"object_id":90000000001,"object_type":"activity","owner_id":1000001,"subscription_id":4242,"updates":{}}`
	deauthEvent = `{"aspect_type":"update","event_time":1772539200,"object_id":1000001,"object_type":"athlete","owner_id":1000001,"subscription_id":4242,"updates":{"authorized":"false"}}`
)

func TestHandleInbound_ActivityCreateAndUpdatePull(t *testing.T) {
	for _, body := range []string{createEvent, updateEvent} {
		events, err := inboundConnector("").HandleInbound(context.Background(), post(body))
		require.NoError(t, err)
		require.Len(t, events, 1)
		e := events[0]
		assert.Equal(t, "1000001", e.ProviderUserID)
		assert.Equal(t, DataTypeActivity, e.DataType)
		assert.True(t, e.From.Equal(inboundNow.Add(-InboundLookback)))
		assert.True(t, e.To.Equal(inboundNow))
	}
}

// Without a subscription id to check, an unsigned delete is not applied.
func TestHandleInbound_DeleteIgnoredWithoutSubscriptionID(t *testing.T) {
	events, err := inboundConnector("").HandleInbound(context.Background(), post(deleteEvent))
	require.NoError(t, err)
	assert.Empty(t, events)
}

// With it, the event is handed back verbatim, and Map turns it into the
// deletion of the activity's workout.
func TestHandleInbound_DeleteIsStoredAndMapped(t *testing.T) {
	c := inboundConnector("4242")
	events, err := c.HandleInbound(context.Background(), post(deleteEvent))
	require.NoError(t, err)
	require.Len(t, events, 1)
	ev := events[0]
	assert.Equal(t, "1000001", ev.ProviderUserID)
	assert.Equal(t, DataTypeEvent, ev.DataType)
	assert.Equal(t, [][]byte{[]byte(deleteEvent)}, ev.Lines)

	var fields map[string]any
	require.NoError(t, json.Unmarshal(ev.Lines[0], &fields))
	res, err := c.Map(connector.RawRecord{UserID: "u-1", Provider: Slug, DataType: DataTypeEvent, Fields: fields})
	require.NoError(t, err)
	assert.Equal(t, []model.Deletion{{Provider: Slug, ExternalID: "90000000001"}}, res.Deletions)

	// The id matches the workout the activity was stored as.
	act, err := c.Map(connector.RawRecord{UserID: "u-1", Provider: Slug, DataType: DataTypeActivity, Fields: map[string]any{
		"id": float64(90000000001), "sport_type": "Run", "start_date": "2026-03-01T08:00:00Z", "elapsed_time": float64(1800),
	}})
	require.NoError(t, err)
	require.Len(t, act.Workouts, 1)
	assert.Equal(t, res.Deletions[0].ExternalID, act.Workouts[0].ExternalID)

	// A delete for another subscription is still refused.
	_, err = inboundConnector("9999").HandleInbound(context.Background(), post(deleteEvent))
	assert.ErrorIs(t, err, ErrWrongSubscription)
}

// A stored event line other than an activity delete maps to nothing.
func TestMapEvent_OtherEventsGiveNothing(t *testing.T) {
	c := inboundConnector("4242")
	for _, body := range []string{createEvent, updateEvent, deauthEvent} {
		var fields map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &fields))
		res, err := c.Map(connector.RawRecord{UserID: "u-1", Provider: Slug, DataType: DataTypeEvent, Fields: fields})
		require.NoError(t, err)
		assert.True(t, res.IsEmpty(), body)
	}
}

// A deauthorization queues a short pull: Strava refuses it, and the refused
// refresh marks the connection needs_reauth.
func TestHandleInbound_DeauthorizationPulls(t *testing.T) {
	events, err := inboundConnector("").HandleInbound(context.Background(), post(deauthEvent))
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "1000001", events[0].ProviderUserID)
	assert.Equal(t, time.Hour, events[0].To.Sub(events[0].From))

	boolForm := strings.Replace(deauthEvent, `"false"`, `false`, 1)
	events, err = inboundConnector("").HandleInbound(context.Background(), post(boolForm))
	require.NoError(t, err)
	assert.Len(t, events, 1)
}

func TestHandleInbound_UnknownIsEmpty(t *testing.T) {
	for _, body := range []string{
		`{"aspect_type":"update","object_type":"athlete","owner_id":1,"updates":{"authorized":"true"}}`,
		`{"aspect_type":"update","object_type":"athlete","owner_id":1,"updates":{"weight":"70"}}`,
		`{"aspect_type":"create","object_type":"route","owner_id":1}`,
		`{"aspect_type":"archive","object_type":"activity","owner_id":1}`,
	} {
		events, err := inboundConnector("").HandleInbound(context.Background(), post(body))
		require.NoError(t, err, body)
		assert.Empty(t, events, body)
	}
}

func TestHandleInbound_MalformedIsAnError(t *testing.T) {
	for _, body := range []string{
		``,
		`not json`,
		`[1,2]`,
		`{"aspect_type":"create","object_type":"activity"}`,
		`{"aspect_type":"create","object_type":"activity","owner_id":"abc"}`,
		`{"object_type":"activity","owner_id":1}`,
		createEvent + `{}`,
	} {
		_, err := inboundConnector("").HandleInbound(context.Background(), post(body))
		assert.Error(t, err, body)
	}
}

func TestHandleInbound_SubscriptionChecked(t *testing.T) {
	_, err := inboundConnector("4242").HandleInbound(context.Background(), post(createEvent))
	require.NoError(t, err)
	_, err = inboundConnector("9999").HandleInbound(context.Background(), post(createEvent))
	assert.ErrorIs(t, err, ErrWrongSubscription)
}

func TestHandleInbound_OffWithoutVerifyToken(t *testing.T) {
	c := New(Options{ClientID: "a", ClientSecret: "b"})
	_, err := c.HandleInbound(context.Background(), post(createEvent))
	assert.ErrorIs(t, err, ErrWebhookNotConfigured)
}

func TestHandleInbound_GetAndHeadGiveNothing(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		events, err := inboundConnector("").HandleInbound(context.Background(), httptest.NewRequest(m, "/inbound/strava", nil))
		require.NoError(t, err)
		assert.Empty(t, events)
	}
}

func challenge(query string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/inbound/strava?"+query, nil)
}

func TestAnswerChallenge(t *testing.T) {
	c := inboundConnector("")
	ctype, body, err := c.AnswerChallenge(challenge("hub.mode=subscribe&hub.verify_token=test-verify-token&hub.challenge=15f7d1a91c1f40f8a748fd134752feb3"))
	require.NoError(t, err)
	assert.Equal(t, "application/json", ctype)
	var got map[string]string
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, map[string]string{"hub.challenge": "15f7d1a91c1f40f8a748fd134752feb3"}, got)

	// A challenge is escaped as JSON, never echoed raw.
	_, body, err = c.AnswerChallenge(challenge(`hub.verify_token=test-verify-token&hub.challenge=%22%7D%3Cx%3E`))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, `"}<x>`, got["hub.challenge"])

	_, body, err = c.AnswerChallenge(challenge(""))
	require.NoError(t, err, "a plain probe")
	assert.Nil(t, body)
}

func TestAnswerChallenge_Refused(t *testing.T) {
	c := inboundConnector("")
	_, _, err := c.AnswerChallenge(challenge("hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=x"))
	assert.ErrorIs(t, err, ErrBadVerifyToken)
	_, _, err = c.AnswerChallenge(challenge("hub.mode=subscribe&hub.challenge=x"))
	assert.ErrorIs(t, err, ErrBadVerifyToken)
	_, _, err = c.AnswerChallenge(challenge("hub.mode=unsubscribe&hub.verify_token=test-verify-token&hub.challenge=x"))
	assert.Error(t, err)

	off := New(Options{ClientID: "a", ClientSecret: "b"})
	_, _, err = off.AnswerChallenge(challenge("hub.verify_token=&hub.challenge=x"))
	assert.ErrorIs(t, err, ErrBadVerifyToken, "no verify token configured")
}

// The connector plugs into the probe route through the core interface.
func TestAnswerChallenge_IsTheCoreInterface(t *testing.T) {
	var c connector.Connector = inboundConnector("")
	_, ok := c.(connector.ChallengeResponder)
	assert.True(t, ok)
}
