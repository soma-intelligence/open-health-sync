// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: "fake-access-token-1", ProviderUserID: "10001"}}
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
		all.Workouts = append(all.Workouts, res.Workouts...)
		all.DailySummaries = append(all.DailySummaries, res.DailySummaries...)
	}
	return all
}

// One raw object per data type and page; every line one NDJSON object.
func TestFetch_EmitsOnePerDataTypeAndPage(t *testing.T) {
	f := newFakeWhoop(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))

	got := map[string][]int{}
	for _, call := range sink.calls {
		got[call.DataType] = append(got[call.DataType], len(call.Lines))
		for _, line := range call.Lines {
			assert.NotContains(t, string(line), "\n", "a line must be one NDJSON line")
			assert.True(t, json.Valid(line))
			assert.Equal(t, byte('{'), line[0])
		}
	}
	assert.Equal(t, map[string][]int{
		DataTypeCycle:    {2, 2},
		DataTypeRecovery: {2, 2},
		DataTypeSleep:    {2, 1},
		DataTypeWorkout:  {2},
		DataTypeBody:     {1},
	}, got, "the fake serves two records a page; cycle 92999 ends inside the window, so WHOOP returns it")

	// Each page is filed under the UTC day of its first record (WHOOP sorts
	// newest first).
	assert.Equal(t, day(2), sink.emitsOf(DataTypeCycle)[0].Day)
	assert.Equal(t, day(1), sink.emitsOf(DataTypeCycle)[1].Day)
	assert.Equal(t, day(3), sink.emitsOf(DataTypeBody)[0].Day, "the body snapshot is filed under today")

	for _, call := range f.Requests() {
		assert.Equal(t, http.MethodGet, call.Method)
		assert.Equal(t, "Bearer fake-access-token-1", call.Auth)
	}
}

func TestFetch_RequestShapes(t *testing.T) {
	f := newFakeWhoop(t)
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{}))

	cycles := f.Calls("/v2/cycle")
	require.Len(t, cycles, 2, "two pages: next_token followed")
	q := cycles[0].Query
	assert.Equal(t, "25", q.Get("limit"))
	assert.Equal(t, "2026-03-01T00:00:00Z", q.Get("start"))
	assert.Equal(t, "2026-03-03T00:00:00Z", q.Get("end"))
	assert.Empty(t, q.Get("nextToken"))
	assert.Equal(t, "2", cycles[1].Query.Get("nextToken"))

	for _, p := range []string{"/v2/recovery", "/v2/activity/sleep"} {
		assert.Len(t, f.Calls(p), 2, p)
	}
	assert.Len(t, f.Calls("/v2/activity/workout"), 1)
	assert.Len(t, f.Calls("/v2/user/measurement/body"), 1)
	for _, r := range f.Requests() {
		assert.False(t, strings.HasPrefix(r.Path, "/v2/cycle/"), "a cycle the pull already has is not asked for: %s", r.Path)
	}
}

// Lines are the provider objects as sent: only the documented additions.
func TestFetch_LinesAreProviderObjects(t *testing.T) {
	f := newFakeWhoop(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))

	var cy map[string]any
	require.NoError(t, json.Unmarshal(sink.linesOf(DataTypeCycle)[0], &cy))
	assert.EqualValues(t, 93003, cy["id"])
	assert.Equal(t, "+01:00", cy["timezone_offset"])
	_, added := cy["cycle"]
	assert.False(t, added, "cycle lines are verbatim")

	recs := sink.linesOf(DataTypeRecovery)
	require.Len(t, recs, 4)
	for _, line := range recs {
		var r Recovery
		require.NoError(t, json.Unmarshal(line, &r))
		require.NotNil(t, r.Cycle, "cycle %s added", r.CycleID)
		assert.Equal(t, "+01:00", r.Cycle.TimezoneOffset)
	}

	var b Body
	require.NoError(t, json.Unmarshal(sink.linesOf(DataTypeBody)[0], &b))
	assert.Equal(t, "2026-03-03", b.SnapshotDate)
}

// A recovery whose cycle WHOOP no longer has is kept without it.
func TestFetch_RecoveryCycleGone(t *testing.T) {
	f := newFakeWhoop(t)
	f.Records = map[string]string{"/v2/recovery": `{"records":[{"cycle_id":1,"created_at":"2026-03-01T07:00:00.000Z","score_state":"SCORED","score":{"recovery_score":50}}]}`}
	sink := &recordingSink{}
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeRecovery}, sink))
	lines := sink.linesOf(DataTypeRecovery)
	require.Len(t, lines, 1)
	assert.NotContains(t, string(lines[0]), `"cycle"`)
}

// Fetch then Map: what comes out of the stored lines.
func TestFetchThenMap(t *testing.T) {
	f := newFakeWhoop(t)
	c := f.connector()
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))

	cyc := mapEmitted(t, c, sink, DataTypeCycle)
	assert.Equal(t, 4, countType(cyc.QuantitySamples, model.QTStrain))
	assert.Equal(t, 4, countType(cyc.QuantitySamples, model.QTHeartRate))
	assert.Len(t, cyc.DailySummaries, 4)

	rec := mapEmitted(t, c, sink, DataTypeRecovery)
	assert.Equal(t, 3, countType(rec.QuantitySamples, model.QTHRVRMSSD), "the pending recovery gives nothing")
	assert.Zero(t, countType(rec.QuantitySamples, model.QTHRVSDNN))
	assert.Len(t, rec.DailySummaries, 3)

	sl := mapEmitted(t, c, sink, DataTypeSleep)
	assert.Len(t, sl.SleepSessions, 3, "the pending sleep is stored, its scored copy replaces it")

	wk := mapEmitted(t, c, sink, DataTypeWorkout)
	require.Len(t, wk.Workouts, 2)
	assert.Equal(t, 2, countType(wk.QuantitySamples, model.QTWorkoutStrain))

	body := mapEmitted(t, c, sink, DataTypeBody)
	assert.Len(t, body.QuantitySamples, 2)

	// The cycle and the recovery of one day fill one summary row.
	days := map[time.Time]string{}
	for _, d := range append(cyc.DailySummaries, rec.DailySummaries...) {
		if h, seen := days[d.SummaryDate]; seen {
			assert.Equal(t, h, d.ContentHash, "one identity per day")
		}
		days[d.SummaryDate] = d.ContentHash
	}
}

// Body measurements are only read when the window reaches today.
func TestFetch_BodyOnlyForRecentWindows(t *testing.T) {
	f := newFakeWhoop(t)
	sink := &recordingSink{}
	old := fetchTo.AddDate(0, 0, -7)
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), old.AddDate(0, 0, -7), old, sink))
	assert.Empty(t, f.Calls("/v2/user/measurement/body"))
	assert.Empty(t, sink.linesOf(DataTypeBody))
}

func TestFetch_EmptyWindow(t *testing.T) {
	f := newFakeWhoop(t)
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchTo, fetchFrom, []string{DataTypeCycle}, &recordingSink{}))
	assert.Empty(t, f.Calls("/v2/cycle"))
}

func TestFetch_UnknownDataType(t *testing.T) {
	f := newFakeWhoop(t)
	err := f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{"heart_rate"}, &recordingSink{})
	assert.Error(t, err)
}

// A rejected access token sends the connection to reauthorization.
func TestFetch_UnauthorizedNeedsReauth(t *testing.T) {
	f := newFakeWhoop(t)
	f.DataHTTP = http.StatusUnauthorized
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
	assert.NotContains(t, err.Error(), "fake-access-token-1")
}

func TestFetch_NoAccessTokenNeedsReauth(t *testing.T) {
	err := New(Options{}).Fetch(context.Background(), connector.Connection{}, fetchFrom, fetchTo, &recordingSink{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}

// A 429 stops the pull at once with the typed rate limit error. WHOOP
// documents X-RateLimit-Reset; Retry-After wins when both are sent.
func TestFetch_RateLimited(t *testing.T) {
	cases := []struct {
		name, retryAfter, reset string
		want                    time.Duration
	}{
		{"Retry-After", "120", "", 120 * time.Second},
		{"X-RateLimit-Reset", "", "42", 42 * time.Second},
		{"both", "30", "42", 30 * time.Second},
		{"neither", "", "", connector.DefaultRetryAfter},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeWhoop(t)
			f.RateLimit = func(p string) bool { return p == "/v2/recovery" }
			f.RetryAfter, f.RateLimitReset = c.retryAfter, c.reset
			sink := &recordingSink{}
			err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink)
			rl, ok := connector.AsRateLimit(err)
			require.True(t, ok, "got %v", err)
			assert.Equal(t, c.want, rl.RetryAfter)
			assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
			assert.Len(t, f.Calls("/v2/recovery"), 1, "no retry on a rate limit")
			assert.Empty(t, f.Calls("/v2/activity/sleep"), "the pull stops")
			assert.Empty(t, f.slept, "nothing sleeps on a rate limit")
		})
	}
}

// A server error fails the pull without asking for reauthorization.
func TestFetch_ServerError(t *testing.T) {
	f := newFakeWhoop(t)
	f.DataHTTP = http.StatusInternalServerError
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
	_, limited := connector.AsRateLimit(err)
	assert.False(t, limited)
}

// failingTransport fails every request at the transport level.
type failingTransport struct{ calls int }

func (ft *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	ft.calls++
	return nil, errors.New("connection refused")
}

// Transport errors are retried on a doubling ladder, five attempts in all,
// and the error does not carry the request URL (it holds the page token).
func TestGet_TransportRetries(t *testing.T) {
	ft := &failingTransport{}
	var waits []time.Duration
	c := New(Options{
		APIBase: "http://whoop.invalid", HTTPClient: &http.Client{Transport: ft},
		RetryBase: 10 * time.Millisecond, PageDelay: -1,
		Sleep: func(_ context.Context, d time.Duration) error {
			waits = append(waits, d)
			return nil
		},
	})
	var out map[string]any
	err := c.get(context.Background(), "tok", pathCycle, map[string][]string{"nextToken": {"secret-page"}}, &out)
	require.Error(t, err)
	assert.Equal(t, maxAttempts, ft.calls)
	require.Len(t, waits, maxAttempts-1)
	for i := 1; i < len(waits); i++ {
		assert.Greater(t, waits[i], waits[i-1], "the ladder climbs")
	}
	assert.NotContains(t, err.Error(), "secret-page")
	assert.False(t, strings.Contains(err.Error(), "tok"))
}

func TestJitter_SpreadsUpwards(t *testing.T) {
	const d = time.Second
	for i := 0; i < 100; i++ {
		got := jitter(d)
		assert.GreaterOrEqual(t, got, d)
		assert.Less(t, got, d+d/2+time.Nanosecond)
	}
	assert.Equal(t, time.Duration(0), jitter(0))
}

func countType(samples []model.QuantitySample, qt string) int {
	n := 0
	for _, s := range samples {
		if s.QuantityType == qt {
			n++
		}
	}
	return n
}
