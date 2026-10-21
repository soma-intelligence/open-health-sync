// SPDX-License-Identifier: AGPL-3.0-only

package oura

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
	fetchTo   = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
)

func testConn() connector.Connection {
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: "fake-access-token-1", ProviderUserID: "fake-oura-user-0001"}}
}

func day(d int) time.Time { return time.Date(2026, 3, d, 0, 0, 0, 0, time.UTC) }

// mapEmitted runs Map over every emitted line of dataType, as the ETL would
// after reading the raw object back.
func mapEmitted(t *testing.T, c *Connector, sink *recordingSink, dataType string) model.MapResult {
	t.Helper()
	var all model.MapResult
	for _, line := range sink.linesOf(dataType) {
		var fields map[string]any
		require.NoError(t, json.Unmarshal(line, &fields))
		res, err := c.Map(connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: dataType, Fields: fields})
		require.NoError(t, err)
		all.QuantitySamples = append(all.QuantitySamples, res.QuantitySamples...)
		all.SleepSessions = append(all.SleepSessions, res.SleepSessions...)
		all.SleepPhases = append(all.SleepPhases, res.SleepPhases...)
		all.Workouts = append(all.Workouts, res.Workouts...)
		all.CategorySamples = append(all.CategorySamples, res.CategorySamples...)
		all.Deletions = append(all.Deletions, res.Deletions...)
	}
	return all
}

// One Emit per data type and page; each line one compact JSON object.
func TestFetch_EmitsOneObjectPerDataTypeAndPage(t *testing.T) {
	f := newFakeOura(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))

	type emitted struct {
		dt    string
		day   time.Time
		lines int
	}
	var got []emitted
	for _, call := range sink.calls {
		got = append(got, emitted{call.DataType, call.Day, len(call.Lines)})
		for _, line := range call.Lines {
			assert.NotContains(t, string(line), "\n", "a line must be one NDJSON line")
			assert.True(t, json.Valid(line))
			assert.Equal(t, byte('{'), line[0])
		}
	}
	assert.Equal(t, []emitted{
		{DataTypeDailyActivity, day(1), 2},
		{DataTypeSleep, day(1), 2}, // page 1: the deleted period and the night
		{DataTypeSleep, day(2), 1}, // page 2: the nap
		{DataTypeDailySleep, day(2), 2},
		{DataTypeDailyReadiness, day(1), 2},
		{DataTypeDailySpO2, day(1), 2},
		{DataTypeDailyStress, day(1), 2},
		{DataTypeWorkout, day(2), 1},
		{DataTypeSession, day(1), 2},
		{DataTypeSession, day(2), 1},
		{DataTypeVO2Max, day(2), 2},
		{DataTypeHeartRate, day(1), 2},
		{DataTypeHeartRate, day(1), 2},
		{DataTypeHeartRate, day(2), 1},
	}, got)

	for _, call := range f.Requests() {
		assert.Equal(t, "Bearer fake-access-token-1", call.Auth)
	}
}

// Lines are the provider documents as sent.
func TestFetch_LinesAreProviderDocuments(t *testing.T) {
	f := newFakeOura(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))
	s := sink.linesOf(DataTypeSleep)
	require.Len(t, s, 3)
	var night map[string]any
	require.NoError(t, json.Unmarshal(s[1], &night))
	assert.Equal(t, "sleep-0001", night["id"])
	assert.Equal(t, "v2", night["sleep_algorithm_version"], "provider fields kept")
	assert.Contains(t, night, "readiness")
}

func TestFetch_RequestShapes(t *testing.T) {
	f := newFakeOura(t)
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{}))

	act := f.Calls("/v2/usercollection/daily_activity")
	require.Len(t, act, 1)
	assert.Equal(t, "2026-02-28", act[0].Query.Get("start_date"), "widened by a day: the document day is a local day")
	assert.Equal(t, "2026-03-04", act[0].Query.Get("end_date"), "widened by a day, and one more in case end_date is exclusive")

	sl := f.Calls("/v2/usercollection/sleep")
	require.Len(t, sl, 2, "next_token followed")
	assert.Empty(t, sl[0].Query.Get("next_token"))
	assert.Equal(t, "2", sl[1].Query.Get("next_token"))
	assert.Equal(t, "2026-02-28", sl[1].Query.Get("start_date"), "the window is kept with next_token")

	hr := f.Calls("/v2/usercollection/heartrate")
	require.Len(t, hr, 3)
	assert.Equal(t, "2026-03-01T00:00:00Z", hr[0].Query.Get("start_datetime"))
	assert.Equal(t, "2026-03-03T00:00:00Z", hr[0].Query.Get("end_datetime"))
	assert.Empty(t, hr[0].Query.Get("start_date"))
}

// Fetch then Map: what comes out of the stored lines.
func TestFetchThenMap(t *testing.T) {
	f := newFakeOura(t)
	c := f.connector()
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))

	act := mapEmitted(t, c, sink, DataTypeDailyActivity)
	assert.Len(t, act.QuantitySamples, 10, "steps, active energy, distance, moderate and vigorous minutes for two days")

	sl := mapEmitted(t, c, sink, DataTypeSleep)
	require.Len(t, sl.SleepSessions, 2, "the night and the nap")
	assert.Equal(t, []model.Deletion{{Provider: Slug, ExternalID: "sleep-0003"}}, sl.Deletions, "the deleted period")
	assert.Len(t, sl.SleepPhases, 11+2)
	assert.Len(t, sl.QuantitySamples, 2, "resting heart rate and respiratory rate of the main sleep only")

	rd := mapEmitted(t, c, sink, DataTypeDailyReadiness)
	assert.Len(t, rd.QuantitySamples, 6)

	sp := mapEmitted(t, c, sink, DataTypeDailySpO2)
	assert.Len(t, sp.QuantitySamples, 1, "a day without an average gives nothing")

	wk := mapEmitted(t, c, sink, DataTypeWorkout)
	require.Len(t, wk.Workouts, 1)
	assert.Equal(t, "running", wk.Workouts[0].ActivityType)

	hr := mapEmitted(t, c, sink, DataTypeHeartRate)
	assert.Len(t, hr.QuantitySamples, 5)

	assert.Len(t, mapEmitted(t, c, sink, DataTypeDailySleep).QuantitySamples, 1, "a day without a score gives nothing")
	assert.Len(t, mapEmitted(t, c, sink, DataTypeDailyStress).QuantitySamples, 3)
	assert.Len(t, mapEmitted(t, c, sink, DataTypeSession).CategorySamples, 2, "meditation and breathing; the nap gives nothing")
	assert.Len(t, mapEmitted(t, c, sink, DataTypeVO2Max).QuantitySamples, 1)
}

func TestFetchTypes_OnlyTheAskedType(t *testing.T) {
	f := newFakeOura(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, sink))
	for _, call := range sink.calls {
		assert.Equal(t, DataTypeWorkout, call.DataType)
	}
	assert.Empty(t, f.Calls("/v2/usercollection/sleep"))

	err := f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{"bogus"}, sink)
	assert.Error(t, err)
}

func TestFetch_RejectedAccessTokenNeedsReauth(t *testing.T) {
	t.Run("HTTP 401", func(t *testing.T) {
		f := newFakeOura(t)
		f.DataHTTP = http.StatusUnauthorized
		err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
		require.ErrorIs(t, err, connector.ErrNeedsReauth)
		assert.NotContains(t, err.Error(), "fake-access-token-1")
	})
	t.Run("no access token", func(t *testing.T) {
		err := New(Options{}).Fetch(context.Background(), connector.Connection{}, fetchFrom, fetchTo, &recordingSink{})
		require.ErrorIs(t, err, connector.ErrNeedsReauth)
	})
}

func TestFetch_OtherStatusIsAnError(t *testing.T) {
	f := newFakeOura(t)
	f.DataHTTP = http.StatusInternalServerError
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
}

// A 429 stops the pull with the typed error, its wait from Retry-After.
func TestFetch_RateLimited(t *testing.T) {
	f := newFakeOura(t)
	f.RateLimit = func(p string) bool { return p == "/v2/usercollection/sleep" }
	f.RetryAfter = "120"
	sink := &recordingSink{}
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink)
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 120*time.Second, rl.RetryAfter)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
	assert.Len(t, f.Calls("/v2/usercollection/sleep"), 1, "no retry inside the connector")
	assert.Empty(t, f.Calls("/v2/usercollection/workout"), "the pull stops")
	assert.Empty(t, sink.linesOf(DataTypeSleep))
}

// A rate limit on a later page keeps the pages already emitted.
func TestFetch_RateLimitOnALaterPage(t *testing.T) {
	f := newFakeOura(t)
	f.PageSize = 1
	calls := 0
	f.RateLimit = func(p string) bool {
		if p != "/v2/usercollection/daily_activity" {
			return false
		}
		calls++
		return calls == 2
	}
	sink := &recordingSink{}
	err := f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeDailyActivity}, sink)
	_, ok := connector.AsRateLimit(err)
	require.True(t, ok, "got %v", err)
	assert.Len(t, sink.linesOf(DataTypeDailyActivity), 1)
}

// A data type whose scope was refused (403) is skipped; when every type is
// refused the pull fails.
func TestFetch_Forbidden(t *testing.T) {
	f := newFakeOura(t)
	f.Forbidden = map[string]bool{"heartrate": true}
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))
	assert.Empty(t, sink.linesOf(DataTypeHeartRate))
	assert.NotEmpty(t, sink.linesOf(DataTypeSleep))

	f = newFakeOura(t)
	f.DataHTTP = http.StatusForbidden
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errForbidden))
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestFetch_EmptyWindowEmitsNothing(t *testing.T) {
	f := newFakeOura(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchTo, fetchFrom, sink))
	assert.Empty(t, sink.calls)
	assert.Empty(t, f.Requests())
}

// A page that is not a list of objects fails the pull instead of storing
// junk.
func TestFetch_MalformedPage(t *testing.T) {
	f := newFakeOura(t)
	f.Bodies = map[string]string{"workout": `{"data":[1,2],"next_token":null}`}
	err := f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, &recordingSink{})
	require.Error(t, err)

	f.Bodies = map[string]string{"workout": `not json`}
	err = f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, &recordingSink{})
	require.Error(t, err)
}

// A token that repeats stops the loop, empty pages or not.
func TestFetch_PaginationGuards(t *testing.T) {
	f := newFakeOura(t)
	f.Bodies = map[string]string{"workout": `{"data":[],"next_token":"again"}`}
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, &recordingSink{}))
	assert.Len(t, f.Calls("/v2/usercollection/workout"), 2, "the second answer repeats the token")

	f = newFakeOura(t)
	f.Bodies = map[string]string{"workout": `{"data":[{"id":"w","day":"2026-03-01"}],"next_token":"same"}`}
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, &recordingSink{}))
	assert.Len(t, f.Calls("/v2/usercollection/workout"), 2, "the second answer repeats the token")
}

// Transport errors are retried on the ladder, then given up.
func TestFetch_TransportErrorRetries(t *testing.T) {
	f := newFakeOura(t)
	c := New(Options{
		ClientID: "id", ClientSecret: "secret",
		APIBase: "http://127.0.0.1:1", PageDelay: -1, RetryBase: time.Second,
		Sleep: func(_ context.Context, d time.Duration) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.slept = append(f.slept, d)
			return nil
		},
	})
	err := c.FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, &recordingSink{})
	require.Error(t, err)
	require.Len(t, f.slept, maxAttempts-1)
	assert.GreaterOrEqual(t, f.slept[1], 2*time.Second, "the wait doubles")
}

func TestWindows(t *testing.T) {
	w := windows(DataTypeSleep, fetchFrom, fetchTo)
	require.Len(t, w, 1)
	assert.Equal(t, "2026-02-28", w[0].Get("start_date"))
	assert.Equal(t, "2026-03-04", w[0].Get("end_date"))

	long := windows(DataTypeDailyActivity, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC))
	require.Len(t, long, 3)
	assert.Equal(t, "2025-12-31", long[0].Get("start_date"))
	assert.Equal(t, "2026-01-30", long[0].Get("end_date"))
	assert.Equal(t, "2026-01-30", long[1].Get("start_date"), "windows share their boundary day: none is missed if end_date is exclusive")
	assert.Equal(t, "2026-03-03", long[2].Get("end_date"))

	hr := windows(DataTypeHeartRate, fetchFrom, fetchFrom.Add(10*24*time.Hour))
	require.Len(t, hr, 2, "heartrate in 7 day steps")
	assert.Equal(t, hr[0].Get("end_datetime"), hr[1].Get("start_datetime"))
	assert.Equal(t, "2026-03-11T00:00:00Z", hr[1].Get("end_datetime"))
}
