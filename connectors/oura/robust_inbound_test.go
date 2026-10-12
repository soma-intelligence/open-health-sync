// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"context"
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

// signedRequest builds a notification POST with the given headers.
func signedRequest(body, ts, sig string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/inbound/oura", strings.NewReader(body))
	if ts != "" {
		r.Header.Set(HeaderTimestamp, ts)
	}
	if sig != "" {
		r.Header.Set(HeaderSignature, sig)
	}
	return r
}

// The signature: Oura's upper case hex, but any case is accepted; header
// names are case-insensitive; surrounding spaces are tolerated; a body sent
// re-indented verifies against the compact form Oura signed.
func TestHandleInbound_SignatureForms(t *testing.T) {
	c := inboundConnector(testVerify)
	body := notice(EventUpdate, DataTypeSleep)
	ts := strconv.FormatInt(inboundNow.Unix(), 10)
	sig := Sign(testSecret, ts, []byte(body))
	require.Equal(t, strings.ToUpper(sig), sig, "Oura sends upper case hex")

	for name, s := range map[string]string{
		"upper": sig, "lower": strings.ToLower(sig), "mixed": sig[:10] + strings.ToLower(sig[10:]), "padded": " " + sig + " ",
	} {
		events, err := c.HandleInbound(context.Background(), signedRequest(body, ts, s))
		require.NoError(t, err, name)
		assert.Len(t, events, 1, name)
	}

	// Header names in any case (net/http canonicalizes what arrives).
	r := httptest.NewRequest(http.MethodPost, "/inbound/oura", strings.NewReader(body))
	r.Header.Set("X-OURA-SIGNATURE", sig)
	r.Header.Set("x-oura-timestamp", ts)
	_, err := c.HandleInbound(context.Background(), r)
	require.NoError(t, err, "header names in any case")

	pretty := "{\n  \"event_type\": \"update\",\n  \"data_type\": \"sleep\",\n  \"object_id\": \"sleep-0001\",\n  \"event_time\": \"2026-03-03T11:59:00+00:00\",\n  \"user_id\": \"oura-user-1\"\n}"
	compactSig := Sign(testSecret, ts, []byte(`{"event_type":"update","data_type":"sleep","object_id":"sleep-0001","event_time":"2026-03-03T11:59:00+00:00","user_id":"oura-user-1"}`))
	events, err := c.HandleInbound(context.Background(), signedRequest(pretty, ts, compactSig))
	require.NoError(t, err, "signed over the compact form")
	assert.Len(t, events, 1)
}

func TestHandleInbound_SignatureRefusals(t *testing.T) {
	c := inboundConnector(testVerify)
	body := notice(EventDelete, DataTypeSleep)
	ts := strconv.FormatInt(inboundNow.Unix(), 10)
	sig := Sign(testSecret, ts, []byte(body))
	cases := map[string]*http.Request{
		"missing both":        signedRequest(body, "", ""),
		"missing signature":   signedRequest(body, ts, ""),
		"missing timestamp":   signedRequest(body, "", sig),
		"other secret":        signedRequest(body, ts, Sign("another-secret", ts, []byte(body))),
		"client id as key":    signedRequest(body, ts, Sign("test-client-id", ts, []byte(body))),
		"other timestamp":     signedRequest(body, strconv.FormatInt(inboundNow.Unix()-1, 10), sig),
		"other body":          signedRequest(notice(EventDelete, DataTypeWorkout), ts, sig),
		"truncated signature": signedRequest(body, ts, sig[:len(sig)-2]),
		"not hex":             signedRequest(body, ts, strings.Repeat("Z", len(sig))),
		"body without ts":     signedRequest(body, ts, Sign(testSecret, "", []byte(body))),
		"base64 not hex":      signedRequest(body, ts, "q83vEjRWeJA="),
	}
	for name, r := range cases {
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature, name)
	}
}

// The timestamp must be within an hour behind and 5 minutes ahead of the
// clock; the bounds are inclusive.
func TestHandleInbound_TimestampSkew(t *testing.T) {
	c := inboundConnector(testVerify)
	body := notice(EventUpdate, DataTypeSleep)
	for offset, ok := range map[time.Duration]bool{
		0:                                  true,
		-SignatureMaxAge:                   true,
		-SignatureMaxAge - time.Second:     false,
		SignatureMaxSkew:                   true,
		SignatureMaxSkew + time.Second:     false,
		-30 * 24 * time.Hour:               false,
		365 * 24 * time.Hour:               false,
		-SignatureMaxAge + 59*time.Minute:  true,
		-(SignatureMaxAge - time.Second*1): true,
	} {
		_, err := c.HandleInbound(context.Background(), webhook(testSecret, inboundNow.Add(offset), body))
		if ok {
			assert.NoError(t, err, offset.String())
		} else {
			assert.ErrorIs(t, err, ErrBadSignature, offset.String())
		}
	}
}

// A delivery replayed within the tolerance is accepted again and gives the
// same events: processing it twice is harmless (see HandleInbound).
func TestHandleInbound_ReplayIsIdempotent(t *testing.T) {
	c := inboundConnector(testVerify)
	body := notice(EventDelete, DataTypeWorkout)
	ts := strconv.FormatInt(inboundNow.Add(-10*time.Minute).Unix(), 10)
	sig := Sign(testSecret, ts, []byte(body))
	a, err := c.HandleInbound(context.Background(), signedRequest(body, ts, sig))
	require.NoError(t, err)
	b, err := c.HandleInbound(context.Background(), signedRequest(body, ts, sig))
	require.NoError(t, err)
	assert.Equal(t, a, b)
	ra := mustMap(t, DataTypeWebhook, string(a[0].Lines[0]))
	rb := mustMap(t, DataTypeWebhook, string(b[0].Lines[0]))
	assert.Equal(t, ra, rb)

	// Replayed after the tolerance: refused.
	late := inboundConnector(testVerify)
	late.now = func() time.Time { return inboundNow.Add(SignatureMaxAge) }
	_, err = late.HandleInbound(context.Background(), signedRequest(body, ts, sig))
	assert.ErrorIs(t, err, ErrBadSignature)
}

func TestAnswerChallenge_Cases(t *testing.T) {
	c := inboundConnector(testVerify)
	for name, tc := range map[string]struct {
		token, challenge string
		want             error
	}{
		"wrong token":        {"wrong", "c1", ErrBadVerificationToken},
		"token prefix":       {testVerify[:5], "c1", ErrBadVerificationToken},
		"token with suffix":  {testVerify + "x", "c1", ErrBadVerificationToken},
		"other case":         {strings.ToUpper(testVerify), "c1", ErrBadVerificationToken},
		"missing token":      {"", "c1", ErrBadVerificationToken},
		"probe without both": {"", "", nil},
		"token, no challenge": {
			testVerify, "", nil,
		},
	} {
		_, body, err := c.AnswerChallenge(challengeRequest(tc.token, tc.challenge))
		if tc.want != nil {
			assert.ErrorIs(t, err, tc.want, name)
			assert.Nil(t, body, name)
			continue
		}
		assert.NoError(t, err, name)
		assert.Nil(t, body, name)
	}
	// A challenge with characters that need escaping comes back intact as
	// JSON.
	ch := `a"b\c<d>&` + "é"
	ctype, body, err := c.AnswerChallenge(challengeRequest(testVerify, ch))
	require.NoError(t, err)
	assert.Equal(t, "application/json", ctype)
	assert.JSONEq(t, `{"challenge":"a\"b\\c<d>&é"}`, string(body))
}

// Every data type and event type: what is pulled, what is deleted, what is
// acknowledged and ignored.
func TestHandleInbound_EveryDataType(t *testing.T) {
	c := inboundConnector(testVerify)
	webhookTypes := []string{"tag", "enhanced_tag", "workout", "session", "sleep", "daily_sleep", "daily_readiness",
		"daily_activity", "daily_spo2", "sleep_time", "rest_mode_period", "ring_configuration", "daily_stress",
		"daily_cardiovascular_age", "daily_resilience", "vo2_max", "meal"}
	for _, dt := range webhookTypes {
		for _, ev := range []string{EventCreate, EventUpdate, EventDelete} {
			body := `{"event_type":"` + ev + `","data_type":"` + dt + `","object_id":"obj-1","event_time":"2026-03-03T11:59:00+00:00","user_id":"oura-user-1"}`
			events, err := c.HandleInbound(context.Background(), webhook(testSecret, inboundNow, body))
			require.NoError(t, err, "%s %s", ev, dt)
			switch {
			case ev == EventDelete && deletableTypes[dt]:
				require.Len(t, events, 1, "%s %s", ev, dt)
				assert.Equal(t, DataTypeWebhook, events[0].DataType)
				res := mustMap(t, DataTypeWebhook, string(events[0].Lines[0]))
				assert.Equal(t, []model.Deletion{{Provider: Slug, ExternalID: "obj-1"}}, res.Deletions)
			case ev != EventDelete && pulledTypes[dt]:
				require.Len(t, events, 1, "%s %s", ev, dt)
				assert.Nil(t, events[0].Lines)
				assert.Equal(t, InboundLookback, events[0].To.Sub(events[0].From))
			default:
				assert.Empty(t, events, "%s %s: acknowledged, nothing to do", ev, dt)
			}
		}
	}
}

// Event and data types in another case, or padded, are read alike, at the
// route and in the stored line.
func TestHandleInbound_CaseInsensitiveTypes(t *testing.T) {
	c := inboundConnector(testVerify)
	body := `{"event_type":"DELETE","data_type":" Sleep ","object_id":"sleep-0009","event_time":"2026-03-03T11:59:00+00:00","user_id":"oura-user-1"}`
	events, err := c.HandleInbound(context.Background(), webhook(testSecret, inboundNow, body))
	require.NoError(t, err)
	require.Len(t, events, 1)
	res := mustMap(t, DataTypeWebhook, string(events[0].Lines[0]))
	assert.Equal(t, []model.Deletion{{Provider: Slug, ExternalID: "sleep-0009"}}, res.Deletions)
}

// Bodies that are not one notification are refused (the route answers 401
// and Oura retries), and never as a signature failure.
func TestHandleInbound_Malformed(t *testing.T) {
	c := inboundConnector(testVerify)
	for _, body := range []string{
		``, ` `, `null`, `[]`, `"text"`, `{`, `not json`, `{}`,
		`{"event_type":"update","data_type":"sleep"}`,
		`{"event_type":"update","data_type":"sleep","user_id":""}`,
		`{"event_type":7,"data_type":"sleep","user_id":"u"}`,
		`{"event_type":"update","data_type":"sleep","user_id":123}`,
		`{"event_type":"delete","data_type":"sleep","user_id":"u","object_id":""}`,
		`{"event_type":"update","data_type":"sleep","user_id":"u"} {"event_type":"update"}`,
		`{"event_type":"update","data_type":"sleep","user_id":"u"}]`,
	} {
		_, err := c.HandleInbound(context.Background(), webhook(testSecret, inboundNow, body))
		assert.Error(t, err, body)
		assert.NotErrorIs(t, err, ErrBadSignature, body)
	}
	// Unknown fields are fine.
	events, err := c.HandleInbound(context.Background(), webhook(testSecret, inboundNow,
		`{"event_type":"update","data_type":"sleep","user_id":"u","object_id":"x","event_time":"2026-03-03T11:59:00+00:00","extra":{"a":[1,2]}}`))
	require.NoError(t, err)
	assert.Len(t, events, 1)
}

// An oversized body is refused before anything is parsed; one at the limit
// is read.
func TestHandleInbound_Oversized(t *testing.T) {
	c := inboundConnector(testVerify)
	big := `{"event_type":"update","data_type":"sleep","user_id":"u","pad":"` + strings.Repeat("x", maxInboundBody) + `"}`
	_, err := c.HandleInbound(context.Background(), webhook(testSecret, inboundNow, big))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")

	base := `{"event_type":"update","data_type":"sleep","user_id":"u","pad":""}`
	fit := strings.Replace(base, `"pad":""`, `"pad":"`+strings.Repeat("x", maxInboundBody-len(base))+`"`, 1)
	require.Len(t, fit, maxInboundBody)
	events, err := c.HandleInbound(context.Background(), webhook(testSecret, inboundNow, fit))
	require.NoError(t, err)
	assert.Len(t, events, 1)
}

// No configuration, no webhooks: whatever is sent.
func TestHandleInbound_NotConfigured(t *testing.T) {
	c := New(Options{})
	_, err := c.HandleInbound(context.Background(), webhook(testSecret, inboundNow, notice(EventUpdate, DataTypeSleep)))
	assert.ErrorIs(t, err, ErrWebhooksOff)
	_, _, err = c.AnswerChallenge(challengeRequest("", "c"))
	assert.ErrorIs(t, err, ErrWebhooksOff)

	r := httptest.NewRequest(http.MethodPost, "/inbound/oura", nil)
	r.Body = nil
	_, err = inboundConnector(testVerify).HandleInbound(context.Background(), r)
	assert.Error(t, err)
}

// A deletion stored before the record it removes still maps (the ETL keeps
// the tombstone); a stored line of another kind maps to nothing.
func TestMapWebhook_Lines(t *testing.T) {
	for _, dt := range []string{DataTypeSleep, DataTypeWorkout, DataTypeSession} {
		res := mustMap(t, DataTypeWebhook, `{"event_type":"delete","data_type":"`+dt+`","object_id":"id-`+dt+`","user_id":"u"}`)
		assert.Equal(t, []model.Deletion{{Provider: Slug, ExternalID: "id-" + dt}}, res.Deletions)
	}
	for _, line := range []string{
		`{"event_type":"delete","data_type":"daily_activity","object_id":"x","user_id":"u"}`,
		`{"event_type":"create","data_type":"sleep","object_id":"x","user_id":"u"}`,
		`{}`,
	} {
		assert.True(t, isEmpty(mustMap(t, DataTypeWebhook, line)), line)
	}
	_, err := mapLine(t, DataTypeWebhook, `{"event_type":"delete","data_type":"sleep","user_id":"u"}`)
	assert.Error(t, err)
	_, err = New(Options{}).Map(connector.RawRecord{DataType: DataTypeWebhook, Fields: map[string]any{"event_type": 5}})
	assert.Error(t, err)
}
