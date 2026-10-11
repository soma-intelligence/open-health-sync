// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

var inboundNow = time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)

const (
	testSecret = "test-client-secret"
	testVerify = "test-verification-token"
)

func inboundConnector(verify string) *Connector {
	return New(Options{
		ClientID: "test-client-id", ClientSecret: testSecret, VerificationToken: verify,
		Now: func() time.Time { return inboundNow },
	})
}

func notice(eventType, dataType string) string {
	return `{"event_type":"` + eventType + `","data_type":"` + dataType +
		`","object_id":"sleep-0001","event_time":"2026-03-03T11:59:00+00:00","user_id":"oura-user-1"}`
}

// webhook builds a notification POST signed with secret at sentAt.
func webhook(secret string, sentAt time.Time, body string) *http.Request {
	ts := strconv.FormatInt(sentAt.Unix(), 10)
	r := httptest.NewRequest(http.MethodPost, "/inbound/oura", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(HeaderTimestamp, ts)
	r.Header.Set(HeaderSignature, Sign(secret, ts, []byte(body)))
	return r
}

func challengeRequest(token, challenge string) *http.Request {
	q := url.Values{}
	if token != "" {
		q.Set("verification_token", token)
	}
	if challenge != "" {
		q.Set("challenge", challenge)
	}
	return httptest.NewRequest(http.MethodGet, "/inbound/oura?"+q.Encode(), nil)
}

func TestAnswerChallenge(t *testing.T) {
	c := inboundConnector(testVerify)
	ctype, body, err := c.AnswerChallenge(challengeRequest(testVerify, "chal-123"))
	require.NoError(t, err)
	assert.Equal(t, "application/json", ctype)
	assert.JSONEq(t, `{"challenge":"chal-123"}`, string(body))

	// A plain probe gets an empty 200.
	_, body, err = c.AnswerChallenge(challengeRequest("", ""))
	require.NoError(t, err)
	assert.Nil(t, body)

	_, _, err = c.AnswerChallenge(challengeRequest("wrong", "chal-123"))
	assert.ErrorIs(t, err, ErrBadVerificationToken)
	_, _, err = c.AnswerChallenge(challengeRequest("", "chal-123"))
	assert.ErrorIs(t, err, ErrBadVerificationToken)

	// Webhooks off: no challenge is answered.
	_, _, err = inboundConnector("").AnswerChallenge(challengeRequest(testVerify, "chal-123"))
	assert.ErrorIs(t, err, ErrWebhooksOff)
}

func TestHandleInbound_CreateAndUpdatePull(t *testing.T) {
	c := inboundConnector(testVerify)
	for _, ev := range []string{EventCreate, EventUpdate} {
		for _, dt := range []string{DataTypeSleep, DataTypeDailyActivity, DataTypeDailyStress} {
			events, err := c.HandleInbound(context.Background(), webhook(testSecret, inboundNow.Add(-time.Minute), notice(ev, dt)))
			require.NoError(t, err)
			require.Len(t, events, 1)
			e := events[0]
			assert.Equal(t, "oura-user-1", e.ProviderUserID)
			assert.Equal(t, dt, e.DataType)
			assert.Nil(t, e.Lines)
			assert.True(t, e.From.Equal(inboundNow.Add(-InboundLookback)))
			assert.True(t, e.To.Equal(inboundNow))
		}
	}
}

// A deleted sleep, workout or session comes back as the verbatim body to
// store, which Map turns into the deletion of object_id.
func TestHandleInbound_DeleteIsStoredAndMapped(t *testing.T) {
	c := inboundConnector(testVerify)
	for _, dt := range []string{DataTypeSleep, DataTypeWorkout, DataTypeSession} {
		body := notice(EventDelete, dt)
		events, err := c.HandleInbound(context.Background(), webhook(testSecret, inboundNow, body))
		require.NoError(t, err, dt)
		require.Len(t, events, 1, dt)
		ev := events[0]
		assert.Equal(t, "oura-user-1", ev.ProviderUserID)
		assert.Equal(t, DataTypeWebhook, ev.DataType)
		assert.Equal(t, [][]byte{[]byte(body)}, ev.Lines, "stored verbatim")

		res := mustMap(t, DataTypeWebhook, string(ev.Lines[0]))
		assert.Equal(t, []model.Deletion{{Provider: Slug, ExternalID: "sleep-0001"}}, res.Deletions, dt)
	}
}

// A daily document is keyed by its day and replaced by its next copy: its
// delete is acknowledged and not applied, as is an unknown event type.
func TestHandleInbound_OtherDeletesAndUnknownGiveNothing(t *testing.T) {
	c := inboundConnector(testVerify)
	for _, body := range []string{
		notice(EventDelete, DataTypeDailyActivity), notice(EventDelete, "tag"), notice("archive", DataTypeSleep),
	} {
		events, err := c.HandleInbound(context.Background(), webhook(testSecret, inboundNow, body))
		require.NoError(t, err, body)
		assert.Empty(t, events, body)
	}
}

func TestHandleInbound_Refusals(t *testing.T) {
	c := inboundConnector(testVerify)
	body := notice(EventUpdate, DataTypeSleep)
	ctx := context.Background()

	_, err := inboundConnector("").HandleInbound(ctx, webhook(testSecret, inboundNow, body))
	assert.ErrorIs(t, err, ErrWebhooksOff, "webhooks are off without the verification token")

	_, err = c.HandleInbound(ctx, webhook("another-secret", inboundNow, body))
	assert.ErrorIs(t, err, ErrBadSignature)

	r := webhook(testSecret, inboundNow, body)
	r.Header.Del(HeaderSignature)
	_, err = c.HandleInbound(ctx, r)
	assert.ErrorIs(t, err, ErrBadSignature)

	// A signature over other bytes.
	r = webhook(testSecret, inboundNow, body)
	r.Body = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(notice(EventDelete, DataTypeSleep))).Body
	_, err = c.HandleInbound(ctx, r)
	assert.ErrorIs(t, err, ErrBadSignature)

	_, err = c.HandleInbound(ctx, webhook(testSecret, inboundNow.Add(-2*time.Hour), body))
	assert.ErrorIs(t, err, ErrBadSignature, "a stale delivery")
	_, err = c.HandleInbound(ctx, webhook(testSecret, inboundNow.Add(10*time.Minute), body))
	assert.ErrorIs(t, err, ErrBadSignature, "a delivery from the future")

	for _, bad := range []string{`not json`, `{"event_type":"update"}`, notice(EventUpdate, DataTypeSleep) + `{}`} {
		_, err = c.HandleInbound(ctx, webhook(testSecret, inboundNow, bad))
		assert.Error(t, err, bad)
		assert.NotErrorIs(t, err, ErrBadSignature, bad)
	}
	_, err = c.HandleInbound(ctx, webhook(testSecret, inboundNow,
		`{"event_type":"delete","data_type":"sleep","user_id":"oura-user-1"}`))
	assert.Error(t, err, "a delete without object_id")
}

// The signature is compared without regard to the case of its hex digits,
// and a timestamp in milliseconds or RFC 3339 is read as one.
func TestVerifySignature_Formats(t *testing.T) {
	body := []byte(notice(EventUpdate, DataTypeSleep))
	for _, ts := range []string{
		strconv.FormatInt(inboundNow.Unix(), 10),
		strconv.FormatInt(inboundNow.UnixMilli(), 10),
		inboundNow.Format(time.RFC3339),
		"opaque-value",
	} {
		sig := Sign(testSecret, ts, body)
		assert.NoError(t, verifySignature(testSecret, ts, sig, body, inboundNow), ts)
		assert.NoError(t, verifySignature(testSecret, ts, strings.ToLower(sig), body, inboundNow), ts)
	}
	old := strconv.FormatInt(inboundNow.Add(-2*time.Hour).UnixMilli(), 10)
	assert.ErrorIs(t, verifySignature(testSecret, old, Sign(testSecret, old, body), body, inboundNow), ErrBadSignature)
}

func TestHandleInbound_HeadAndGetGiveNothing(t *testing.T) {
	for _, m := range []string{http.MethodHead, http.MethodGet} {
		events, err := inboundConnector(testVerify).HandleInbound(context.Background(), httptest.NewRequest(m, "/inbound/oura", nil))
		require.NoError(t, err)
		assert.Empty(t, events)
	}
}

// A stored notification other than a delete of a stored type maps to nothing.
func TestMapWebhook_OtherNotificationsGiveNothing(t *testing.T) {
	for _, body := range []string{notice(EventUpdate, DataTypeSleep), notice(EventDelete, DataTypeDailySleep)} {
		var fields map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &fields))
		res, err := New(Options{}).Map(connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: DataTypeWebhook, Fields: fields})
		require.NoError(t, err)
		assert.True(t, res.IsEmpty(), body)
	}
}

// The verification token reaches the connector through Configure.
func TestConfigure_VerificationToken(t *testing.T) {
	c := New(Options{Now: func() time.Time { return inboundNow }})
	require.NoError(t, c.Configure(connector.Config{
		KeyClientID: "id", KeyClientSecret: testSecret, KeyVerificationToken: testVerify,
	}))
	_, body, err := c.AnswerChallenge(challengeRequest(testVerify, "x"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"challenge":"x"}`, string(body))
	assert.Equal(t, []string{KeyClientID, KeyClientSecret}, c.RequiredKeys(), "the token stays optional")
}
