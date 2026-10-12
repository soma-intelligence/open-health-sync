// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var inboundNow = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)

func inboundConnector() *Connector {
	return New(Options{ClientID: "test-client-id", ClientSecret: "test-client-secret", Now: func() time.Time { return inboundNow }})
}

// notification builds a Withings notification POST to the signed callback
// URL of userID.
func notification(t *testing.T, c *Connector, userID string, form url.Values) *http.Request {
	t.Helper()
	cb, err := c.SignedCallbackURL("http://localhost:8080/inbound/withings", userID)
	require.NoError(t, err)
	u, err := url.Parse(cb)
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, u.RequestURI(), strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func notifyForm(userID, appli string, start, end int64) url.Values {
	f := url.Values{}
	f.Set("userid", userID)
	f.Set("appli", appli)
	f.Set("startdate", fmt.Sprint(start))
	f.Set("enddate", fmt.Sprint(end))
	return f
}

func TestHandleInbound_Valid(t *testing.T) {
	c := inboundConnector()
	start := inboundNow.Add(-3 * time.Hour)
	r := notification(t, c, "424242", notifyForm("424242", "1", start.Unix(), inboundNow.Add(-time.Hour).Unix()))
	events, err := c.HandleInbound(context.Background(), r)
	require.NoError(t, err)
	require.Len(t, events, 1)
	e := events[0]
	assert.Equal(t, "424242", e.ProviderUserID)
	assert.Equal(t, DataTypeMeasure, e.DataType)
	assert.True(t, e.From.Equal(start.Add(-InboundOverlap)), "from = startdate minus the overlap")
	assert.True(t, e.To.Equal(inboundNow), "to = now")
}

func TestHandleInbound_AllAppliTypes(t *testing.T) {
	c := inboundConnector()
	for appli, dt := range map[int]string{
		AppliMeasurement: DataTypeMeasure,
		AppliActivity:    DataTypeActivity,
		AppliSleep:       DataTypeSleep,
		AppliWorkout:     DataTypeWorkout,
	} {
		r := notification(t, c, "99", notifyForm("99", fmt.Sprint(appli), inboundNow.Unix()-60, inboundNow.Unix()))
		events, err := c.HandleInbound(context.Background(), r)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, dt, events[0].DataType, "appli %d", appli)
	}
}

func TestAppliToDataType(t *testing.T) {
	assert.Equal(t, DataTypeMeasure, AppliToDataType(1))
	assert.Equal(t, DataTypeActivity, AppliToDataType(16))
	assert.Equal(t, DataTypeSleep, AppliToDataType(44))
	assert.Equal(t, DataTypeWorkout, AppliToDataType(46))
	assert.Equal(t, "", AppliToDataType(999))
}

func TestHandleInbound_BadSignature(t *testing.T) {
	c := inboundConnector()
	form := notifyForm("424242", "1", inboundNow.Unix()-60, inboundNow.Unix())

	t.Run("missing", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/inbound/withings", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature)
	})
	t.Run("wrong", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/inbound/withings?sig=deadbeef", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature)
	})
	t.Run("signed for another user", func(t *testing.T) {
		r := notification(t, c, "111", form) // URL of user 111, body claims 424242
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature)
	})
	t.Run("signed with another secret", func(t *testing.T) {
		other := New(Options{ClientID: "x", ClientSecret: "another-secret"})
		r := notification(t, other, "424242", form)
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature)
	})
	t.Run("no userid", func(t *testing.T) {
		f := url.Values{}
		f.Set("appli", "1")
		r := notification(t, c, "424242", f)
		_, err := c.HandleInbound(context.Background(), r)
		assert.ErrorIs(t, err, ErrBadSignature)
	})
}

func TestHandleInbound_MalformedAppli(t *testing.T) {
	c := inboundConnector()
	r := notification(t, c, "424242", notifyForm("424242", "abc", 1, 2))
	_, err := c.HandleInbound(context.Background(), r)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrBadSignature)
}

// A category this connector does not pull: no event, no error, so the route
// still answers 200 and Withings stops retrying.
func TestHandleInbound_UnknownAppliIgnored(t *testing.T) {
	c := inboundConnector()
	r := notification(t, c, "424242", notifyForm("424242", "999", 1, 2))
	events, err := c.HandleInbound(context.Background(), r)
	require.NoError(t, err)
	assert.Empty(t, events)
}

// Withings checks the callback URL is reachable when a subscription is made.
func TestHandleInbound_ReachabilityCheck(t *testing.T) {
	c := inboundConnector()
	for _, m := range []string{http.MethodHead, http.MethodGet} {
		events, err := c.HandleInbound(context.Background(), httptest.NewRequest(m, "/inbound/withings", nil))
		require.NoError(t, err, m)
		assert.Empty(t, events, m)
	}
}

// The body cannot ask for a full history pull: startdate=0 is clamped to the
// lookback.
func TestHandleInbound_IgnoresCallerFetchWindow(t *testing.T) {
	c := inboundConnector()
	r := notification(t, c, "424242", notifyForm("424242", "1", 0, 9999999999))
	events, err := c.HandleInbound(context.Background(), r)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.True(t, events[0].From.Equal(inboundNow.Add(-InboundLookback)))
	assert.True(t, events[0].To.Equal(inboundNow), "the end is now, never the body's")
}

func TestInboundWindow(t *testing.T) {
	now := inboundNow
	cases := []struct {
		name      string
		startdate string
		wantFrom  time.Time
	}{
		{"missing", "", now.Add(-InboundLookback)},
		{"garbage", "soon", now.Add(-InboundLookback)},
		{"recent", fmt.Sprint(now.Add(-time.Hour).Unix()), now.Add(-time.Hour - InboundOverlap)},
		{"older than the lookback", fmt.Sprint(now.Add(-30 * 24 * time.Hour).Unix()), now.Add(-InboundLookback)},
		{"in the future", fmt.Sprint(now.Add(48 * time.Hour).Unix()), now.Add(-InboundOverlap)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from, to := inboundWindow(c.startdate, now)
			assert.True(t, from.Equal(c.wantFrom), "from = %v, want %v", from, c.wantFrom)
			assert.True(t, to.Equal(now))
		})
	}
}

func TestSignedCallbackURL(t *testing.T) {
	c := inboundConnector()
	u, err := c.SignedCallbackURL("https://ohs.example.org/inbound/withings", "424242")
	require.NoError(t, err)
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	assert.Equal(t, "ohs.example.org", parsed.Host)
	assert.Equal(t, "/inbound/withings", parsed.Path)
	assert.Len(t, parsed.Query().Get("sig"), 64, "hex SHA-256")

	again, _ := c.SignedCallbackURL("https://ohs.example.org/inbound/withings", "424242")
	assert.Equal(t, u, again, "deterministic: Withings matches subscriptions on the URL")
	other, _ := c.SignedCallbackURL("https://ohs.example.org/inbound/withings", "111")
	assert.NotEqual(t, u, other)

	_, err = c.SignedCallbackURL("https://ohs.example.org/inbound/withings", "")
	assert.Error(t, err)
}

func TestFetchNotified_PullsOnlyWhatTheNotificationConcerns(t *testing.T) {
	to := time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC)
	from := to.Add(-2 * time.Hour)
	all := newFakeWithings(t)
	require.NoError(t, all.connector().Fetch(context.Background(), testConn(), from, to, &recordingSink{}))
	for dt, want := range map[string][]string{
		DataTypeMeasure:  {DataTypeMeasure, DataTypeECG},
		DataTypeActivity: {DataTypeActivity, DataTypeIntraday},
		DataTypeSleep:    {DataTypeSleep},
		DataTypeWorkout:  {DataTypeWorkout},
	} {
		f := newFakeWithings(t)
		sink := &recordingSink{}
		require.NoError(t, f.connector().FetchNotified(context.Background(), testConn(), from, to, dt, sink))
		got := map[string]bool{}
		for _, c := range sink.calls {
			got[c.DataType] = true
		}
		for d := range got {
			assert.Contains(t, want, d, dt)
		}
		assert.LessOrEqual(t, len(f.Requests()), len(all.Requests())/2, "%s: %d calls against %d for every type", dt, len(f.Requests()), len(all.Requests()))
	}
	f := newFakeWithings(t)
	require.NoError(t, f.connector().FetchNotified(context.Background(), testConn(), from, to, "unknown", &recordingSink{}))
	assert.Equal(t, len(all.Requests()), len(f.Requests()), "an unknown type pulls everything")
}
