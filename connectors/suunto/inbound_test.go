// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/suunto/suuntofake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func signedRequest(body, signature string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/inbound/suunto", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if signature != "" {
		r.Header.Set(HeaderSignature, signature)
	}
	return r
}

func signed(body string) *http.Request {
	return signedRequest(body, Signature(testNotificationSecret, []byte(body)))
}

func testConnector() *Connector {
	return New(Options{
		ClientID: "i", ClientSecret: "s", SubscriptionKey: "k", NotificationSecret: testNotificationSecret,
		Now: func() time.Time { return fakeNow },
	})
}

func TestInboundWorkoutCreated(t *testing.T) {
	c := testConnector()
	start := time.Date(2026, 3, 1, 9, 10, 0, 0, time.UTC) // synced two days late
	body := `{"type":"WORKOUT_CREATED","username":"` + suuntofake.Username + `","workout":{"workoutKey":"aaaaaaaaaaaaaaaaaaaa0002","startTime":` +
		formatMs(start) + `}}`
	evs, err := c.HandleInbound(context.Background(), signed(body))
	require.NoError(t, err)
	require.Len(t, evs, 1)
	ev := evs[0]
	assert.Equal(t, suuntofake.Username, ev.ProviderUserID)
	assert.Equal(t, DataTypeWorkout, ev.DataType)
	assert.Equal(t, start.Add(-InboundOverlap), ev.From)
	assert.Equal(t, fakeNow, ev.To)
	assert.Empty(t, ev.Lines)
}

func formatMs(t time.Time) string { return itoa(t.UnixMilli()) }

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestInboundWorkoutWindowBounds(t *testing.T) {
	c := testConnector()
	cases := []struct {
		name     string
		workout  string
		wantFrom time.Time
	}{
		{"no start: last 24 hours", `{"workoutKey":"k"}`, fakeNow.Add(-24 * time.Hour)},
		{"recent start: at least 24 hours", `{"workoutKey":"k","startTime":` + itoa(fakeNow.Add(-time.Hour).UnixMilli()) + `}`, fakeNow.Add(-24 * time.Hour)},
		{"old start: capped at the lookback", `{"workoutKey":"k","startTime":` + itoa(fakeNow.Add(-30*24*time.Hour).UnixMilli()) + `}`, fakeNow.Add(-InboundLookback)},
		{"invalid start", `{"workoutKey":"k","startTime":-5}`, fakeNow.Add(-24 * time.Hour)},
	}
	for _, tc := range cases {
		body := `{"type":"WORKOUT_CREATED","username":"u","workout":` + tc.workout + `}`
		evs, err := c.HandleInbound(context.Background(), signed(body))
		require.NoError(t, err, tc.name)
		require.Len(t, evs, 1, tc.name)
		assert.Equal(t, tc.wantFrom, evs[0].From, tc.name)
		assert.Equal(t, fakeNow, evs[0].To, tc.name)
	}
}

// The API Zone FAQ shape: username and workoutid, as a form or as JSON,
// without a type.
func TestInboundLegacyShapes(t *testing.T) {
	c := testConnector()
	for _, body := range []string{
		`username=jane&workoutid=900001`,
		`{"username":"jane","workoutid":900001}`,
		`{"username":"jane","workoutKey":"aaaaaaaaaaaaaaaaaaaa0001"}`,
	} {
		r := signed(body)
		if !strings.HasPrefix(body, "{") {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		evs, err := c.HandleInbound(context.Background(), r)
		require.NoError(t, err, body)
		require.Len(t, evs, 1, body)
		assert.Equal(t, "jane", evs[0].ProviderUserID)
		assert.Equal(t, DataTypeWorkout, evs[0].DataType)
		assert.Equal(t, fakeNow.Add(-24*time.Hour), evs[0].From)
	}
}

func Test247Notifications(t *testing.T) {
	c := testConnector()
	for typ, dt := range map[string]string{
		EventSleepCreated:    DataTypeSleep,
		EventRecoveryCreated: DataTypeRecovery,
		EventActivityCreated: DataTypeDailyActivity,
	} {
		body := `{"type":"` + typ + `","username":"jane","samples":[{"a":1}]}`
		evs, err := c.HandleInbound(context.Background(), signed(body))
		require.NoError(t, err, typ)
		require.Len(t, evs, 1, typ)
		assert.Equal(t, connector.InboundEvent{ProviderUserID: "jane", DataType: dt, From: fakeNow.Add(-48 * time.Hour), To: fakeNow}, evs[0])
	}
}

func TestInboundIgnoredTypes(t *testing.T) {
	c := testConnector()
	for _, body := range []string{
		`{"type":"ROUTE_CREATED","username":"jane"}`,
		`{"type":"SOMETHING_NEW","username":"jane","workout":{"workoutKey":"k"}}`,
		`{"username":"jane"}`,
	} {
		evs, err := c.HandleInbound(context.Background(), signed(body))
		assert.NoError(t, err, body)
		assert.Empty(t, evs, body)
	}
}

func TestInboundSignature(t *testing.T) {
	c := testConnector()
	body := `{"type":"WORKOUT_CREATED","username":"jane","workout":{"workoutKey":"k"}}`
	mac := hmac.New(sha256.New, []byte(testNotificationSecret))
	mac.Write([]byte(body))
	sum := mac.Sum(nil)

	accepted := []string{
		Signature(testNotificationSecret, []byte(body)),
		strings.ToUpper(Signature(testNotificationSecret, []byte(body))),
		base64.StdEncoding.EncodeToString(sum),
		base64.RawURLEncoding.EncodeToString(sum),
		"sha256=" + Signature(testNotificationSecret, []byte(body)),
	}
	for _, sig := range accepted {
		_, err := c.HandleInbound(context.Background(), signedRequest(body, sig))
		assert.NoError(t, err, sig)
	}

	refused := []string{
		"",
		"zz",
		Signature("another-secret", []byte(body)),
		Signature(testNotificationSecret, []byte(body+" ")),
		Signature(testNotificationSecret, []byte(body))[:32],
	}
	for _, sig := range refused {
		_, err := c.HandleInbound(context.Background(), signedRequest(body, sig))
		assert.ErrorIs(t, err, ErrBadSignature, "%q", sig)
	}
	// A body changed after signing.
	r := signedRequest(strings.Replace(body, "jane", "mallory", 1), Signature(testNotificationSecret, []byte(body)))
	_, err := c.HandleInbound(context.Background(), r)
	assert.ErrorIs(t, err, ErrBadSignature)
}

func TestInboundOffWithoutSecret(t *testing.T) {
	c := New(Options{ClientID: "i", ClientSecret: "s", SubscriptionKey: "k"})
	body := `{"type":"WORKOUT_CREATED","username":"jane"}`
	_, err := c.HandleInbound(context.Background(), signedRequest(body, Signature("", []byte(body))))
	assert.ErrorIs(t, err, ErrNotificationsOff)
}

func TestInboundBadBodies(t *testing.T) {
	c := testConnector()
	for _, body := range []string{
		`{"type":"WORKOUT_CREATED","workout":{"workoutKey":"k"}}`, // no username
		`{"type":"WORKOUT_CREATED","username":"  "}`,
		`{"type":`,
		``,
	} {
		_, err := c.HandleInbound(context.Background(), signed(body))
		assert.Error(t, err, body)
	}
}

func TestInboundProbes(t *testing.T) {
	c := testConnector()
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		evs, err := c.HandleInbound(context.Background(), httptest.NewRequest(m, "/inbound/suunto", nil))
		assert.NoError(t, err)
		assert.Empty(t, evs)
	}
}
