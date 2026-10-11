// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

var (
	fetchFrom = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	fetchTo   = fakeNow
)

func day(d int) time.Time { return time.Date(2026, 3, d, 0, 0, 0, 0, time.UTC) }

func TestFetch_EmitsEachDataTypeByDay(t *testing.T) {
	f := newFakePolar(t)
	c := f.connector()
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))

	type key struct {
		dt  string
		day time.Time
	}
	got := map[key]int{}
	for _, call := range sink.calls {
		k := key{call.DataType, call.Day}
		require.Zero(t, got[k], "one Emit per data type and day: %v", k)
		got[k] = len(call.Lines)
	}
	assert.Equal(t, map[key]int{
		{DataTypeSleep, day(4)}:           1, // the February night is outside the window
		{DataTypeNightlyRecharge, day(4)}: 1,
		{DataTypeExercise, day(5)}:        1, // the February exercise is outside the window
		{DataTypeActivity, day(4)}:        1,
		{DataTypeActivity, day(5)}:        1,
		{DataTypeContinuousHR, day(4)}:    1,
		{DataTypePhysicalInfo, day(6)}:    1,
	}, got)

	for _, r := range f.Requests() {
		assert.Equal(t, "Bearer fake-access-token-1", r.Auth, r.Path)
	}
	acts := f.Calls(http.MethodGet, "/v3/users/activities")
	require.Len(t, acts, 1)
	assert.Equal(t, "2026-03-01", acts[0].Query.Get("from"))
	assert.Equal(t, "2026-03-09", acts[0].Query.Get("to"))
	hr := f.Calls(http.MethodGet, "/v3/users/continuous-heart-rate")
	require.Len(t, hr, 1)
	assert.Equal(t, "2026-03-01", hr[0].Query.Get("from"))
	assert.Equal(t, "2026-03-09", hr[0].Query.Get("to"))
	assert.Empty(t, f.Calls("", "/v3/users/"+testPolarUser), "Fetch does not register or probe")
}

func TestFetch_LinesAreCompactAndEnriched(t *testing.T) {
	f := newFakePolar(t)
	c := f.connector()
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))

	for _, call := range sink.calls {
		for _, l := range call.Lines {
			assert.NotContains(t, string(l), "\n", call.DataType)
			var m map[string]any
			require.NoError(t, json.Unmarshal(l, &m), call.DataType)
		}
	}
	var hr map[string]any
	require.NoError(t, json.Unmarshal(sink.linesOf(DataTypeContinuousHR)[0], &hr))
	assert.EqualValues(t, 60, hr["utc_offset_minutes"], "offset learned from the sleep of the same date")
	var pi map[string]any
	require.NoError(t, json.Unmarshal(sink.linesOf(DataTypePhysicalInfo)[0], &pi))
	assert.Equal(t, "2026-03-10T00:00:00Z", pi["fetched_at"])
}

func TestFetch_MapsEndToEnd(t *testing.T) {
	f := newFakePolar(t)
	c := f.connector()
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))

	w := mapEmitted(t, c, sink, DataTypeExercise)
	require.Len(t, w.Workouts, 1)
	assert.Equal(t, "EXC0001", w.Workouts[0].ExternalID)

	s := mapEmitted(t, c, sink, DataTypeSleep)
	require.Len(t, s.SleepSessions, 1)
	assert.Len(t, s.SleepPhases, 7)

	hr := mapEmitted(t, c, sink, DataTypeContinuousHR)
	require.Len(t, hr.QuantitySamples, 2)
	assert.Equal(t, time.Date(2026, 3, 4, 7, 0, 0, 0, time.UTC), hr.QuantitySamples[0].StartDate)
	assert.Equal(t, "Etc/GMT-1", hr.QuantitySamples[0].Metadata["timezone"])

	pi := mapEmitted(t, c, sink, DataTypePhysicalInfo)
	assert.Len(t, pi.QuantitySamples, 4)
}

func TestFetch_Unauthorized(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		f := newFakePolar(t)
		f.DataHTTP = status
		err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
		require.ErrorIs(t, err, connector.ErrNeedsReauth, "status %d", status)
	}
	err := New(Options{}).Fetch(context.Background(), connector.Connection{}, fetchFrom, fetchTo, &recordingSink{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestFetch_ServerErrorIsNotReauth(t *testing.T) {
	f := newFakePolar(t)
	f.DataHTTP = http.StatusInternalServerError
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
	_, limited := connector.AsRateLimit(err)
	assert.False(t, limited)
}

func TestFetch_RateLimited(t *testing.T) {
	cases := []struct {
		name                     string
		retryAfter, usage, limit string
		reset                    string
		want                     time.Duration
	}{
		{name: "retry-after", retryAfter: "120", want: 120 * time.Second},
		{name: "short term used up", usage: "520, 900", limit: "500, 5000", reset: "60, 3600", want: 60 * time.Second},
		{name: "long term used up", usage: "520, 5100", limit: "500, 5000", reset: "60, 3600", want: time.Hour},
		{name: "reset only", reset: "45, 3600", want: 45 * time.Second},
		{name: "no header", want: connector.DefaultRetryAfter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePolar(t)
			f.RateLimit = func(_, p string) bool { return p == "/v3/users/sleep" }
			f.RetryAfter, f.RateLimitUsage, f.RateLimitLimit, f.RateLimitResetHdr = tc.retryAfter, tc.usage, tc.limit, tc.reset
			sink := &recordingSink{}
			err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink)
			rl, ok := connector.AsRateLimit(err)
			require.True(t, ok, "%v", err)
			assert.Equal(t, tc.want, rl.RetryAfter)
			assert.Empty(t, sink.calls)
		})
	}
}

func TestFetch_NoData(t *testing.T) {
	f := newFakePolar(t)
	f.Empty = true
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))
	assert.Empty(t, sink.calls)
}

func TestFetch_ActivityWindowClampedAndChunked(t *testing.T) {
	f := newFakePolar(t)
	c := f.connector()
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, c.FetchTypes(context.Background(), testConn(), from, fetchTo, []string{DataTypeActivity}, &recordingSink{}))
	calls := f.Calls(http.MethodGet, "/v3/users/activities")
	require.NotEmpty(t, calls)
	assert.Equal(t, "2025-03-10", calls[0].Query.Get("from"), "no further back than 365 days")
	assert.Equal(t, "2026-03-09", calls[len(calls)-1].Query.Get("to"))
	for _, r := range calls {
		a, _ := time.Parse("2006-01-02", r.Query.Get("from"))
		b, _ := time.Parse("2006-01-02", r.Query.Get("to"))
		assert.LessOrEqual(t, int(b.Sub(a).Hours()/24)+1, rangeChunkDays)
	}
}

func TestFetch_UnknownType(t *testing.T) {
	f := newFakePolar(t)
	err := f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{"nope"}, &recordingSink{})
	require.Error(t, err)
}

type failingSink struct{}

func (failingSink) Emit(context.Context, string, time.Time, [][]byte) error {
	return errors.New("disk full")
}

func TestFetch_SinkError(t *testing.T) {
	f := newFakePolar(t)
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, failingSink{})
	require.ErrorContains(t, err, "disk full")
}

func TestFetch_ExerciseOutsideWindowByStartButUploadedInIt(t *testing.T) {
	f := newFakePolar(t)
	c := f.connector()
	sink := &recordingSink{}
	// The February exercise was uploaded at 2026-02-10T19:00Z.
	from := time.Date(2026, 2, 10, 18, 30, 0, 0, time.UTC)
	to := time.Date(2026, 2, 10, 20, 0, 0, 0, time.UTC)
	require.NoError(t, c.FetchTypes(context.Background(), testConn(), from, to, []string{DataTypeExercise}, sink))
	lines := sink.linesOf(DataTypeExercise)
	require.Len(t, lines, 1)
	var e Exercise
	require.NoError(t, json.Unmarshal(lines[0], &e))
	assert.Equal(t, "EXC0002", e.ID)
}

func TestTransportRetry(t *testing.T) {
	f := newFakePolar(t)
	c := f.connector()
	c.apiBase = "http://127.0.0.1:1" // nothing listens
	_, _, err := c.get(context.Background(), "tok", "/v3/exercises", nil)
	require.Error(t, err)
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Len(t, f.slept, maxAttempts-1)
}

func TestMapIsDeterministic(t *testing.T) {
	f := newFakePolar(t)
	c := f.connector()
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))
	for _, dt := range AllDataTypes {
		a := mapEmitted(t, c, sink, dt)
		b := mapEmitted(t, c, sink, dt)
		assert.Equal(t, hashes(a), hashes(b), dt)
	}
}

func hashes(r model.MapResult) []string {
	var out []string
	for _, q := range r.QuantitySamples {
		out = append(out, q.ContentHash)
	}
	for _, s := range r.SleepSessions {
		out = append(out, s.ContentHash)
	}
	for _, w := range r.Workouts {
		out = append(out, w.ContentHash)
	}
	return out
}
