// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/suunto/suuntofake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

var (
	mar1 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	mar2 = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	mar3 = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
)

func conn() connector.Connection {
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: "user-token", ProviderUserID: suuntofake.Username}}
}

func workoutKeys(t *testing.T, lines [][]byte) []string {
	t.Helper()
	var out []string
	for _, l := range lines {
		var w struct {
			WorkoutKey string `json:"workoutKey"`
		}
		require.NoError(t, json.Unmarshal(l, &w))
		out = append(out, w.WorkoutKey)
	}
	return out
}

func TestFetchWorkoutsPagesByTime(t *testing.T) {
	f := newFakeSuunto(t)
	c := f.connector(1)
	sink := &recordingSink{}
	require.NoError(t, c.FetchTypes(context.Background(), conn(), mar1, fakeNow, []string{DataTypeWorkout}, sink))

	calls := f.Calls("/v3/workouts")
	require.Len(t, calls, 4, "three one-item pages, then an empty one")
	for i, r := range calls {
		assert.Equal(t, "Bearer user-token", r.Auth)
		assert.Equal(t, suuntofake.SubscriptionKey, r.SubscriptionKey)
		assert.Equal(t, strconv.FormatInt(mar1.UnixMilli(), 10), r.Query.Get("since"))
		assert.Equal(t, strconv.FormatInt(fakeNow.UnixMilli(), 10), r.Query.Get("until"))
		assert.Equal(t, "1", r.Query.Get("limit"))
		assert.Equal(t, strconv.Itoa(i), r.Query.Get("offset"))
	}

	require.Len(t, sink.calls, 2, "one emit per UTC day of the workout start")
	assert.Equal(t, mar2, sink.calls[0].Day)
	assert.Equal(t, []string{"aaaaaaaaaaaaaaaaaaaa0001", "aaaaaaaaaaaaaaaaaaaa0003"}, workoutKeys(t, sink.calls[0].Lines))
	assert.Equal(t, mar3, sink.calls[1].Day)
	assert.Equal(t, []string{"aaaaaaaaaaaaaaaaaaaa0002"}, workoutKeys(t, sink.calls[1].Lines))
	for _, l := range sink.lines(DataTypeWorkout) {
		assert.NotContains(t, string(l), "\n", "one compact line per workout")
	}
}

func TestFetchWorkoutsFullPageSize(t *testing.T) {
	f := newFakeSuunto(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector(0).FetchTypes(context.Background(), conn(), mar1, fakeNow, []string{DataTypeWorkout}, sink))
	require.Len(t, f.Calls("/v3/workouts"), 1, "a short page is the last")
	assert.Equal(t, "100", f.Calls("/v3/workouts")[0].Query.Get("limit"))
	assert.Len(t, sink.lines(DataTypeWorkout), 3)
}

func TestFetchWorkoutsDedupAcrossPages(t *testing.T) {
	f := newFakeSuunto(t)
	w := suuntofake.Workouts()
	f.Workouts = []json.RawMessage{w[0], w[0], w[1]}
	sink := &recordingSink{}
	require.NoError(t, f.connector(1).FetchTypes(context.Background(), conn(), mar1, fakeNow, []string{DataTypeWorkout}, sink))
	assert.ElementsMatch(t, []string{"aaaaaaaaaaaaaaaaaaaa0001", "aaaaaaaaaaaaaaaaaaaa0002"}, workoutKeys(t, sink.lines(DataTypeWorkout)))
}

func TestWorkoutItemsShapes(t *testing.T) {
	items, err := workoutItems([]byte(`[{"workoutKey":"a"}]`))
	require.NoError(t, err)
	assert.Len(t, items, 1)
	items, err = workoutItems([]byte(`{"error":null,"payload":[{"workoutKey":"a"},{"workoutKey":"b"}]}`))
	require.NoError(t, err)
	assert.Len(t, items, 2)
	_, err = workoutItems([]byte(`{"error":{"code":"x"},"payload":[]}`))
	assert.Error(t, err)
	_, err = workoutItems([]byte(`<html>`))
	assert.Error(t, err)
}

func TestFetchAllTypes(t *testing.T) {
	f := newFakeSuunto(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector(0).Fetch(context.Background(), conn(), mar1, fakeNow, sink))

	assert.Len(t, sink.lines(DataTypeWorkout), 3)
	assert.Len(t, sink.lines(DataTypeSleep), 2, "an array answer: one line per element")
	assert.Len(t, sink.lines(DataTypeRecovery), 1)
	assert.Len(t, sink.lines(DataTypeDailyActivity), 1, "an object answer: one line")
	for _, dt := range rawOnlyTypes {
		for _, c := range sink.calls {
			if c.DataType == dt {
				assert.Equal(t, mar1, c.Day, "filed under the window start")
			}
		}
	}

	sleep := f.Calls("/247samples/sleep")
	require.Len(t, sleep, 1)
	assert.Equal(t, strconv.FormatInt(mar1.UnixMilli(), 10), sleep[0].Query.Get("from"))
	assert.Equal(t, strconv.FormatInt(fakeNow.UnixMilli(), 10), sleep[0].Query.Get("to"))
	assert.Equal(t, suuntofake.SubscriptionKey, sleep[0].SubscriptionKey)
	daily := f.Calls("/247/daily-activity-statistics")
	require.Len(t, daily, 1)
	assert.Equal(t, "2026-03-01T00:00:00", daily[0].Query.Get("startdate"))
	assert.Equal(t, "2026-03-03T23:59:59", daily[0].Query.Get("enddate"))
	assert.Len(t, f.Calls("/247samples/recovery"), 1)
}

func Test247WindowsOf28Days(t *testing.T) {
	f := newFakeSuunto(t)
	from := fakeNow.Add(-60 * 24 * time.Hour)
	sink := &recordingSink{}
	require.NoError(t, f.connector(0).FetchTypes(context.Background(), conn(), from, fakeNow, []string{DataTypeRecovery}, sink))
	calls := f.Calls("/247samples/recovery")
	require.Len(t, calls, 3, "28 + 28 + 4 days")
	wantFrom := from
	for i, r := range calls {
		gotFrom, _ := strconv.ParseInt(r.Query.Get("from"), 10, 64)
		gotTo, _ := strconv.ParseInt(r.Query.Get("to"), 10, 64)
		assert.Equal(t, wantFrom.UnixMilli(), gotFrom, "window %d", i)
		assert.LessOrEqual(t, gotTo-gotFrom, maxSpan247.Milliseconds())
		wantFrom = time.UnixMilli(gotTo).UTC()
	}
	assert.Equal(t, fakeNow, wantFrom, "the windows end at to")
	assert.Len(t, sink.calls, 3, "one emit per window start day")
}

func TestRawLines(t *testing.T) {
	l, err := rawLines([]byte(`[{"a":1}, {"b":2}]`))
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte(`{"a":1}`), []byte(`{"b":2}`)}, l)
	l, err = rawLines([]byte("{\n \"a\": 1\n}"))
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte(`{"a":1}`)}, l)
	l, err = rawLines([]byte(`[1, 2]`))
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte(`{"response":[1,2]}`)}, l)
	l, err = rawLines([]byte(`  `))
	require.NoError(t, err)
	assert.Empty(t, l)
	l, err = rawLines([]byte(`null`))
	require.NoError(t, err)
	assert.Empty(t, l)
	_, err = rawLines([]byte(`{oops`))
	assert.Error(t, err)
}

func TestFetchNotifiedPullsOnlyThatType(t *testing.T) {
	f := newFakeSuunto(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector(0).FetchNotified(context.Background(), conn(), mar1, fakeNow, DataTypeWorkout, sink))
	assert.Len(t, f.Calls("/v3/workouts"), 1)
	assert.Empty(t, f.Calls("/247samples/sleep"))
	assert.Empty(t, f.Calls("/247/daily-activity-statistics"))

	g := newFakeSuunto(t)
	require.NoError(t, g.connector(0).FetchNotified(context.Background(), conn(), mar1, fakeNow, DataTypeSleep, &recordingSink{}))
	assert.Empty(t, g.Calls("/v3/workouts"))
	assert.Len(t, g.Calls("/247samples/sleep"), 1)

	h := newFakeSuunto(t)
	require.NoError(t, h.connector(0).FetchNotified(context.Background(), conn(), mar1, fakeNow, "unknown", &recordingSink{}))
	assert.Len(t, h.Calls("/v3/workouts"), 1, "an unknown type pulls everything")
	assert.Len(t, h.Calls("/247samples/recovery"), 1)
}

func TestFetchUnauthorizedNeedsReauth(t *testing.T) {
	f := newFakeSuunto(t)
	f.DataHTTP = http.StatusUnauthorized
	err := f.connector(0).Fetch(context.Background(), conn(), mar1, fakeNow, &recordingSink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)

	err = f.connector(0).Fetch(context.Background(), connector.Connection{}, mar1, fakeNow, &recordingSink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth, "no access token")
}

// A refused subscription key is the operator's problem, not the user's: the
// connection must not be marked needs_reauth.
func TestFetchBadSubscriptionKeyIsNotReauth(t *testing.T) {
	f := newFakeSuunto(t)
	c := New(Options{
		ClientID: suuntofake.ClientID, ClientSecret: suuntofake.ClientSecret, SubscriptionKey: "wrong-key",
		OAuthBase: f.URL(), APIBase: f.URL(), HTTPClient: f.Client(), PageDelay: -1,
		Now: func() time.Time { return fakeNow },
	})
	err := c.Fetch(context.Background(), conn(), mar1, fakeNow, &recordingSink{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSubscriptionKey)
	assert.False(t, errors.Is(err, connector.ErrNeedsReauth))
	assert.Equal(t, "wrong-key", f.Calls("/v3/workouts")[0].SubscriptionKey)
}

func TestFetchRateLimited(t *testing.T) {
	f := newFakeSuunto(t)
	f.RateLimit = func(p string) bool { return p == "/v3/workouts" }
	f.RateLimitHeaders = http.Header{"Retry-After": {"30"}}
	err := f.connector(0).Fetch(context.Background(), conn(), mar1, fakeNow, &recordingSink{})
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, 30*time.Second, rl.RetryAfter)
}

func TestFetchQuotaExceeded(t *testing.T) {
	f := newFakeSuunto(t)
	f.Quota = func(p string) bool { return p == "/247samples/sleep" }
	sink := &recordingSink{}
	err := f.connector(0).Fetch(context.Background(), conn(), mar1, fakeNow, sink)
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, 2*time.Hour+15*time.Minute+30*time.Second, rl.RetryAfter)
	assert.Len(t, sink.lines(DataTypeWorkout), 3, "workouts came first")

	g := newFakeSuunto(t)
	g.Quota = func(string) bool { return true }
	g.QuotaBody = `{"statusCode":403,"message":"Out of call volume quota. Quota will be replenished in 3.04:00:00."}`
	rl, ok = connector.AsRateLimit(g.connector(0).Fetch(context.Background(), conn(), mar1, fakeNow, &recordingSink{}))
	require.True(t, ok)
	assert.Equal(t, connector.MaxRetryAfter, rl.RetryAfter, "3 days 4 hours, capped")

	h := newFakeSuunto(t)
	h.Quota = func(string) bool { return true }
	h.QuotaBody = `{"message":"Out of call volume quota."}`
	rl, ok = connector.AsRateLimit(h.connector(0).Fetch(context.Background(), conn(), mar1, fakeNow, &recordingSink{}))
	require.True(t, ok)
	assert.Equal(t, connector.DefaultRetryAfter, rl.RetryAfter)
}

func TestFetchSkipsUnavailable247(t *testing.T) {
	f := newFakeSuunto(t)
	f.Status247 = map[string]int{"/247samples/sleep": http.StatusNotFound}
	sink := &recordingSink{}
	require.NoError(t, f.connector(0).Fetch(context.Background(), conn(), mar1, fakeNow, sink))
	assert.Empty(t, sink.lines(DataTypeSleep))
	assert.Len(t, sink.lines(DataTypeWorkout), 3)
	assert.Len(t, sink.lines(DataTypeRecovery), 1)

	g := newFakeSuunto(t)
	g.Workouts = nil
	g.Status247 = map[string]int{"/v3/workouts": http.StatusNotFound}
	assert.Error(t, g.connector(0).Fetch(context.Background(), conn(), mar1, fakeNow, &recordingSink{}), "workouts are never skipped")
}

func TestFetchEmptyWindow(t *testing.T) {
	f := newFakeSuunto(t)
	require.NoError(t, f.connector(0).Fetch(context.Background(), conn(), fakeNow, fakeNow, &recordingSink{}))
	assert.Empty(t, f.Requests())
}

func TestFetchRetriesTransportErrors(t *testing.T) {
	f := newFakeSuunto(t)
	c := f.connector(0)
	c.apiBase = "http://127.0.0.1:1" // nothing listens
	err := c.FetchTypes(context.Background(), conn(), mar1, fakeNow, []string{DataTypeWorkout}, &recordingSink{})
	require.Error(t, err)
	assert.Len(t, f.slept, maxAttempts-1)
}
