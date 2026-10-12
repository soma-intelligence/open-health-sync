// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"context"
	"crypto/ecdsa"
	"encoding/asn1"
	"encoding/base64"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/fitbit/fitbitfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

const upsertSteps = `{"data":{"version":"1","clientProvidedSubscriptionName":"ohs","healthUserId":"111222333444555666",
"operation":"UPSERT","dataType":"steps","intervals":[{"physicalTimeInterval":{"startTime":"2026-03-08T01:29:00Z","endTime":"2026-03-08T01:34:00Z"},
"civilDateTimeInterval":{"startDateTime":{"date":{"year":2026,"month":3,"day":7},"time":{"hours":17,"minutes":29}},"endDateTime":{"date":{"year":2026,"month":3,"day":7},"time":{"hours":17,"minutes":34}}},
"civilIso8601TimeInterval":{"startTime":"2026-03-07T17:29:00","endTime":"2026-03-07T17:34:00"}}]}}`

// post builds a notification request.
func post(body, auth, sig string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/inbound/fitbit", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", "Google-Health-API-Webhooks")
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	if sig != "" {
		r.Header.Set(HeaderSignature, sig)
	}
	return r
}

// signedFake is a fake serving the keyset of one signer.
func signedFake(t *testing.T) (*fitbitfake.Server, *fitbitfake.Signer) {
	f := newFake(t)
	s := fitbitfake.NewSigner(t, 0x01020304)
	f.Keyset = fitbitfake.Keyset(s)
	return f, s
}

func TestVerificationRequests(t *testing.T) {
	f, _ := signedFake(t)
	c := newConnector(f)
	body := `{"type": "verification"}`

	ev, err := c.HandleInbound(context.Background(), post(body, testWebhookSecret, ""))
	require.NoError(t, err, "with the secret: 200")
	assert.Empty(t, ev)

	_, err = c.HandleInbound(context.Background(), post(body, "", ""))
	require.ErrorIs(t, err, ErrBadAuthorization, "without credentials: 401")
	_, err = c.HandleInbound(context.Background(), post(body, "Bearer wrong", ""))
	require.ErrorIs(t, err, ErrBadAuthorization)
	_, err = c.HandleInbound(context.Background(), post(body, "test-webhook-secret", ""))
	require.ErrorIs(t, err, ErrBadAuthorization, "the header must equal the configured value, scheme included")
}

func TestInboundOffWithoutSecret(t *testing.T) {
	f, _ := signedFake(t)
	c := newConnector(f, func(o *Options) { o.WebhookSecret = "" })
	_, err := c.HandleInbound(context.Background(), post(`{"type":"verification"}`, "", ""))
	require.ErrorIs(t, err, ErrWebhooksOff)

	ev, err := c.HandleInbound(context.Background(), httptest.NewRequest(http.MethodGet, "/inbound/fitbit", nil))
	require.NoError(t, err)
	assert.Empty(t, ev)
}

func TestSignedNotification(t *testing.T) {
	f, s := signedFake(t)
	c := newConnector(f)
	ev, err := c.HandleInbound(context.Background(), post(upsertSteps, testWebhookSecret, s.Sign([]byte(upsertSteps))))
	require.NoError(t, err)
	require.Len(t, ev, 1)
	assert.Equal(t, connector.InboundEvent{
		ProviderUserID: fitbitfake.HealthUserID, DataType: DataTypeSteps,
		From: time.Date(2026, 3, 8, 1, 28, 0, 0, time.UTC), To: time.Date(2026, 3, 8, 1, 35, 0, 0, time.UTC),
	}, ev[0])

	// The keyset is cached: a second delivery does not fetch it again.
	_, err = c.HandleInbound(context.Background(), post(upsertSteps, testWebhookSecret, s.Sign([]byte(upsertSteps))))
	require.NoError(t, err)
	assert.Len(t, f.Calls("/keyset.json"), 1)
}

func TestSignatureRefused(t *testing.T) {
	f, s := signedFake(t)
	c := newConnector(f)
	other := fitbitfake.NewSigner(t, s.KeyID)

	for name, sig := range map[string]string{
		"missing":       "",
		"not base64":    "%%%",
		"other body":    s.Sign([]byte(`{"data":{}}`)),
		"other key":     other.Sign([]byte(upsertSteps)),
		"truncated":     s.Sign([]byte(upsertSteps))[:20],
		"unknown keyid": fitbitfake.NewSigner(t, 77).Sign([]byte(upsertSteps)),
	} {
		_, err := c.HandleInbound(context.Background(), post(upsertSteps, testWebhookSecret, sig))
		require.ErrorIs(t, err, ErrBadSignature, name)
	}
	// A body re-serialized on the way no longer verifies: the raw bytes are signed.
	_, err := c.HandleInbound(context.Background(), post(" "+upsertSteps, testWebhookSecret, s.Sign([]byte(upsertSteps))))
	require.ErrorIs(t, err, ErrBadSignature)
}

func TestSignatureEncodings(t *testing.T) {
	f, s := signedFake(t)
	c := newConnector(f)
	raw, err := base64.StdEncoding.DecodeString(s.Sign([]byte(upsertSteps)))
	require.NoError(t, err)

	// URL-safe alphabet without padding.
	_, err = c.HandleInbound(context.Background(), post(upsertSteps, testWebhookSecret, base64.RawURLEncoding.EncodeToString(raw)))
	require.NoError(t, err)

	// IEEE P1363 (r||s) instead of DER.
	var rs struct{ R, S *big.Int }
	_, err = asn1.Unmarshal(raw[5:], &rs)
	require.NoError(t, err)
	p1363 := append(append([]byte(nil), raw[:5]...), append(rs.R.FillBytes(make([]byte, 32)), rs.S.FillBytes(make([]byte, 32))...)...)
	_, err = c.HandleInbound(context.Background(), post(upsertSteps, testWebhookSecret, base64.StdEncoding.EncodeToString(p1363)))
	require.NoError(t, err)
}

func TestKeyRotationRefetchesKeyset(t *testing.T) {
	f, s := signedFake(t)
	clock := fakeNow
	c := newConnector(f, func(o *Options) { o.Now = func() time.Time { return clock } })
	_, err := c.HandleInbound(context.Background(), post(upsertSteps, testWebhookSecret, s.Sign([]byte(upsertSteps))))
	require.NoError(t, err)

	next := fitbitfake.NewSigner(t, 99)
	f.Keyset = fitbitfake.Keyset(next, s)
	clock = clock.Add(2 * time.Minute)
	_, err = c.HandleInbound(context.Background(), post(upsertSteps, testWebhookSecret, next.Sign([]byte(upsertSteps))))
	require.NoError(t, err, "an unknown key id fetches the keyset again")
	assert.Len(t, f.Calls("/keyset.json"), 2)

	// Within a minute of the last fetch an unknown key id does not refetch.
	_, err = c.HandleInbound(context.Background(), post(upsertSteps, testWebhookSecret, fitbitfake.NewSigner(t, 5).Sign([]byte(upsertSteps))))
	require.ErrorIs(t, err, ErrBadSignature)
	assert.Len(t, f.Calls("/keyset.json"), 2)
}

func TestSignatureModes(t *testing.T) {
	f := newFake(t) // no keyset: 404
	_, err := newConnector(f).HandleInbound(context.Background(), post(upsertSteps, testWebhookSecret, "c2ln"))
	require.ErrorIs(t, err, ErrBadSignature, "required: an unavailable keyset refuses")

	ev, err := newConnector(f, func(o *Options) { o.WebhookSignature = "if_available" }).
		HandleInbound(context.Background(), post(upsertSteps, testWebhookSecret, ""))
	require.NoError(t, err, "if_available: the secret alone admits while the keyset is unavailable")
	assert.Len(t, ev, 1)

	ev, err = newConnector(f, func(o *Options) { o.WebhookSignature = "off" }).
		HandleInbound(context.Background(), post(upsertSteps, testWebhookSecret, "garbage"))
	require.NoError(t, err)
	assert.Len(t, ev, 1)

	// if_available with a keyset: a wrong signature is refused.
	f2, _ := signedFake(t)
	_, err = newConnector(f2, func(o *Options) { o.WebhookSignature = "if_available" }).
		HandleInbound(context.Background(), post(upsertSteps, testWebhookSecret, "c2ln"))
	require.ErrorIs(t, err, ErrBadSignature)
}

func TestBatchNotifications(t *testing.T) {
	f := newFake(t)
	c := newConnector(f, func(o *Options) { o.WebhookSignature = "off" })
	body := `[
	 {"data":{"healthUserId":"u1","operation":"UPSERT","dataType":"heart-rate","intervals":[{"physicalTimeInterval":{"startTime":"2026-03-09T10:00:00Z","endTime":"2026-03-09T10:05:00Z"}}]}},
	 {"data":{"healthUserId":"u1","operation":"UPSERT","dataType":"heart-rate","intervals":[{"physicalTimeInterval":{"startTime":"2026-03-09T08:00:00Z","endTime":"2026-03-09T08:05:00Z"}}]}},
	 {"data":{"healthUserId":"u2","operation":"DELETE","dataType":"sleep","recordId":"9875412","intervals":[{"physicalTimeInterval":{"startTime":"2026-03-08T22:00:00Z","endTime":"2026-03-09T06:00:00Z"}}]}},
	 {"data":{"healthUserId":"u1","operation":"UPSERT","dataType":"daily-resting-heart-rate","intervals":[{"civilDateTimeInterval":{"startDateTime":{"date":{"year":2026,"month":3,"day":9},"time":{}},"endDateTime":{"date":{"year":2026,"month":3,"day":9},"time":{}}}}]}},
	 {"data":{"healthUserId":"u1","operation":"UPSERT","dataType":"total-calories","intervals":[]}},
	 {"data":{"operation":"UPSERT","dataType":"steps"}},
	 {"data":{"healthUserId":"u3","operation":"UPSERT","dataType":"weight"}}
	]`
	ev, err := c.HandleInbound(context.Background(), post(body, testWebhookSecret, ""))
	require.NoError(t, err)
	require.Len(t, ev, 4)
	assert.Equal(t, connector.InboundEvent{ProviderUserID: "u1", DataType: DataTypeHeartRate,
		From: time.Date(2026, 3, 9, 7, 59, 0, 0, time.UTC), To: time.Date(2026, 3, 9, 10, 6, 0, 0, time.UTC)}, ev[0], "merged per user and type")
	assert.Equal(t, "u2", ev[1].ProviderUserID, "a delete queues a pull")
	assert.Equal(t, DataTypeSleep, ev[1].DataType)
	assert.Equal(t, connector.InboundEvent{ProviderUserID: "u1", DataType: DataTypeDailyRestingHR,
		From: time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC)}, ev[2])
	assert.Equal(t, connector.InboundEvent{ProviderUserID: "u3", DataType: DataTypeWeight,
		From: fakeNow.Add(-InboundLookback), To: fakeNow}, ev[3], "no interval: the recent days")
	for _, e := range ev {
		assert.Empty(t, e.Lines)
	}
}

func TestInboundRejectsJunk(t *testing.T) {
	f := newFake(t)
	c := newConnector(f, func(o *Options) { o.WebhookSignature = "off" })
	_, err := c.HandleInbound(context.Background(), post(`not json`, testWebhookSecret, ""))
	require.Error(t, err)
	_, err = c.HandleInbound(context.Background(), post(strings.Repeat(" ", maxInboundBody+1), testWebhookSecret, ""))
	require.Error(t, err)
}

func TestParseKeysetSkipsUnusableKeys(t *testing.T) {
	s := fitbitfake.NewSigner(t, 1)
	good := string(fitbitfake.Keyset(s))
	_, err := parseKeyset([]byte(`{"key":[]}`))
	require.Error(t, err)
	keys, err := parseKeyset([]byte(strings.Replace(good, `"ENABLED"`, `"DISABLED"`, 1)))
	require.Error(t, err)
	assert.Empty(t, keys)
	keys, err = parseKeyset([]byte(good))
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, uint32(1), keys[0].id)
	assert.IsType(t, &ecdsa.PublicKey{}, keys[0].pub)
}
