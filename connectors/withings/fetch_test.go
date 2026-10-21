// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: "fake-access-token-1", ProviderUserID: "424242"}}
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
		all.ECGRecordings = append(all.ECGRecordings, res.ECGRecordings...)
		all.ECGSamples = append(all.ECGSamples, res.ECGSamples...)
	}
	return all
}

func TestFetch_EmitsOneDataTypePerDay(t *testing.T) {
	f := newFakeWithings(t)
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
		_, dup := got[k]
		assert.False(t, dup, "one Emit per data type and day: %v", k)
		got[k] = len(call.Lines)
		for _, line := range call.Lines {
			assert.NotContains(t, string(line), "\n", "a line must be one NDJSON line")
			assert.True(t, json.Valid(line))
			assert.Equal(t, byte('{'), line[0])
		}
	}
	assert.Equal(t, map[key]int{
		{DataTypeMeasure, day(1)}:  1,
		{DataTypeMeasure, day(2)}:  2,
		{DataTypeActivity, day(1)}: 1,
		{DataTypeActivity, day(2)}: 1,
		{DataTypeSleep, day(1)}:    1,
		{DataTypeWorkout, day(2)}:  1,
		{DataTypeECG, day(2)}:      1,
		{DataTypeIntraday, day(1)}: 1,
		{DataTypeIntraday, day(2)}: 1,
	}, got)

	for _, call := range f.Requests() {
		if call.Path != "/v2/oauth2" {
			assert.Equal(t, "Bearer fake-access-token-1", call.Auth)
		}
	}
}

// Lines are the provider objects as sent: only the documented additions.
func TestFetch_LinesAreProviderObjects(t *testing.T) {
	f := newFakeWithings(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))

	m := sink.linesOf(DataTypeMeasure)
	require.Len(t, m, 3)
	var g map[string]any
	require.NoError(t, json.Unmarshal(m[0], &g))
	assert.EqualValues(t, 1001, g["grpid"])
	assert.Equal(t, "Body Cardio", g["model"], "provider fields kept")
	assert.Equal(t, "Europe/Paris", g["timezone"], "body timezone added to the group")

	a := sink.linesOf(DataTypeActivity)
	var act map[string]any
	require.NoError(t, json.Unmarshal(a[0], &act))
	assert.Equal(t, true, act["is_tracker"], "activity lines are verbatim")

	s := sink.linesOf(DataTypeSleep)
	require.Len(t, s, 1)
	var sl SleepSeries
	require.NoError(t, json.Unmarshal(s[0], &sl))
	assert.Len(t, sl.SleepPhases, 4, "the detailed segments of the night are attached")

	e := sink.linesOf(DataTypeECG)
	require.Len(t, e, 1)
	var hr HeartRecord
	require.NoError(t, json.Unmarshal(e[0], &hr))
	require.NotNil(t, hr.Signal)
	assert.Len(t, hr.Signal.Signal, 6)
	assert.Contains(t, string(e[0]), `"bloodpressure"`)

	in := sink.linesOf(DataTypeIntraday)
	require.Len(t, in, 2)
	var p IntradayPoint
	require.NoError(t, json.Unmarshal(in[0], &p))
	assert.EqualValues(t, 1772359200, p.Timestamp, "the series key becomes timestamp")
}

// Fetch then Map: what comes out of the stored lines.
func TestFetchThenMap(t *testing.T) {
	f := newFakeWithings(t)
	c := f.connector()
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))

	meas := mapEmitted(t, c, sink, DataTypeMeasure)
	m := byType(meas.QuantitySamples)
	require.Len(t, meas.QuantitySamples, 5, "weight, fat ratio, BP pair, heart rate; the objective is dropped")
	assert.InDelta(t, 70.0, m["body_mass"].Value, 1e-9)
	assert.InDelta(t, 120.0, m["blood_pressure_systolic"].Value, 1e-9)
	for _, s := range meas.QuantitySamples {
		assert.Equal(t, "Europe/Paris", s.Metadata["timezone"], "R17")
	}

	act := mapEmitted(t, c, sink, DataTypeActivity)
	require.Len(t, act.QuantitySamples, 10, "steps, distance, energy, moderate and vigorous minutes, two days")
	for _, s := range act.QuantitySamples {
		assert.Equal(t, "Europe/Paris", s.Metadata["timezone"])
	}

	sl := mapEmitted(t, c, sink, DataTypeSleep)
	require.Len(t, sl.SleepSessions, 1)
	assert.Len(t, sl.SleepPhases, 4)
	assert.Equal(t, "Europe/Paris", sl.SleepSessions[0].Metadata["timezone"])

	wk := mapEmitted(t, c, sink, DataTypeWorkout)
	require.Len(t, wk.Workouts, 1)
	assert.Equal(t, "running", wk.Workouts[0].ActivityType)
	assert.Equal(t, 2700, *wk.Workouts[0].DurationS)

	ecg := mapEmitted(t, c, sink, DataTypeECG)
	require.Len(t, ecg.ECGRecordings, 1)
	assert.Len(t, ecg.ECGSamples, 6)
	assert.Equal(t, "ecg_afib_classification", ecg.QuantitySamples[0].QuantityType)

	intra := mapEmitted(t, c, sink, DataTypeIntraday)
	im := map[string]int{}
	for _, s := range intra.QuantitySamples {
		im[s.QuantityType]++
	}
	assert.Equal(t, 2, im[model.QTStepCountIntraday])
	assert.Equal(t, 1, im[model.QTHeartRate], "the 0 bpm interval gives no heart rate")
}

func TestFetch_RequestShapes(t *testing.T) {
	f := newFakeWithings(t)
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{}))

	meas := f.calls("/measure", "getmeas")
	require.Len(t, meas, 2, "two pages: more/offset followed")
	assert.Equal(t, "1772323200", meas[0].Form.Get("startdate"))
	assert.Equal(t, "1772496000", meas[0].Form.Get("enddate"))
	assert.Empty(t, meas[0].Form.Get("meastype"), "every meastype in one call")
	assert.Equal(t, "1", meas[1].Form.Get("offset"))

	act := f.calls("/v2/measure", "getactivity")
	require.Len(t, act, 1)
	assert.Equal(t, "2026-03-01", act[0].Form.Get("startdateymd"))
	assert.Equal(t, "2026-03-02", act[0].Form.Get("enddateymd"), "the exclusive end at midnight does not pull the next day")

	intra := f.calls("/v2/measure", "getintradayactivity")
	require.Len(t, intra, 2, "intraday is limited to 24 h per call")

	assert.Len(t, f.calls("/v2/sleep", "getsummary"), 1)
	det := f.calls("/v2/sleep", "get")
	require.Len(t, det, 1, "one detail call per night")
	assert.Equal(t, "1772402400", det[0].Form.Get("startdate"), "the night's own start")
	assert.Equal(t, "1772431200", det[0].Form.Get("enddate"), "the night's own end")
	assert.Len(t, f.calls("/v2/heart", "list"), 1)
	sig := f.calls("/v2/heart", "get")
	require.Len(t, sig, 1)
	assert.Equal(t, "4001", sig[0].Form.Get("signalid"))
}

func TestFetchTypes_OnlyTheAskedType(t *testing.T) {
	f := newFakeWithings(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeSleep}, sink))
	for _, call := range sink.calls {
		assert.Equal(t, DataTypeSleep, call.DataType)
	}
	assert.Empty(t, f.calls("/measure", ""))

	err := f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{"bogus"}, sink)
	assert.Error(t, err)
}

// A failed waveform download keeps the classification.
func TestFetch_SignalFailureKeepsClassification(t *testing.T) {
	f := newFakeWithings(t)
	f.SignalFails = true
	sink := &recordingSink{}
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeECG}, sink))
	lines := sink.linesOf(DataTypeECG)
	require.Len(t, lines, 1)
	assert.NotContains(t, string(lines[0]), `"signal"`)
}

func TestFetch_RejectedAccessTokenNeedsReauth(t *testing.T) {
	t.Run("inner status 401", func(t *testing.T) {
		f := newFakeWithings(t)
		f.DataStatus = 401
		err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
		require.ErrorIs(t, err, connector.ErrNeedsReauth)
	})
	t.Run("HTTP 401", func(t *testing.T) {
		f := newFakeWithings(t)
		f.DataHTTP = http.StatusUnauthorized
		err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
		require.ErrorIs(t, err, connector.ErrNeedsReauth)
	})
	t.Run("no access token", func(t *testing.T) {
		err := New(Options{}).Fetch(context.Background(), connector.Connection{}, fetchFrom, fetchTo, &recordingSink{})
		require.ErrorIs(t, err, connector.ErrNeedsReauth)
	})
}

func TestFetch_OtherStatusIsAnError(t *testing.T) {
	f := newFakeWithings(t)
	f.DataStatus = 503
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestFetch_EmptyWindowEmitsNothing(t *testing.T) {
	f := newFakeWithings(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchTo, fetchFrom, sink))
	assert.Empty(t, sink.calls)
	assert.Empty(t, f.Requests())
}

// Sleep action=get answers at most 24 h after startdate: every night of a
// multi-night window must still get its phases.
func TestFetchSleep_EveryNightGetsItsPhases(t *testing.T) {
	f := newFakeWithings(t)
	c := f.connector()
	sink := &recordingSink{}
	to := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	require.NoError(t, c.FetchTypes(context.Background(), testConn(), fetchFrom, to, []string{DataTypeSleep}, sink))
	lines := sink.linesOf(DataTypeSleep)
	require.Len(t, lines, 2)
	want := map[string]int{"2001": 4, "2002": 3}
	for _, l := range lines {
		var s SleepSeries
		require.NoError(t, json.Unmarshal(l, &s))
		assert.Len(t, s.SleepPhases, want[string(s.ID)], "night %s", s.ID)
	}
	for _, call := range f.calls("/v2/sleep", "get") {
		start, _ := strconv.ParseInt(call.Form.Get("startdate"), 10, 64)
		end, _ := strconv.ParseInt(call.Form.Get("enddate"), 10, 64)
		assert.LessOrEqual(t, end-start, int64(86400), "never more than 24 h per call")
	}
}

// A corrupt summary whose end lies a year after its start: the detail calls
// cover its first 48 h only, not one call per day of the bogus span.
func TestFetchSleep_BogusSummarySpanIsClamped(t *testing.T) {
	f := newFakeWithings(t)
	start := time.Date(2026, 3, 1, 22, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	f.SleepSummaryBody = fmt.Sprintf(`{"status":0,"body":{"series":[{"id":9001,"startdate":%d,"enddate":%d,"date":"2026-03-01","state":0,"data":{}}],"more":false,"offset":0}}`,
		start.Unix(), end.Unix())
	sink := &recordingSink{}
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeSleep}, sink))
	require.Len(t, sink.linesOf(DataTypeSleep), 1, "the summary is still stored")
	calls := f.calls("/v2/sleep", "get")
	require.NotEmpty(t, calls)
	assert.LessOrEqual(t, len(calls), 2, "48 h in 24 h steps")
	for _, call := range calls {
		e, _ := strconv.ParseInt(call.Form.Get("enddate"), 10, 64)
		assert.LessOrEqual(t, e, start.Add(48*time.Hour).Unix())
	}
}

// Without any summary, segments are asked for day by day and grouped into
// synthetic nights.
func TestFetchSleep_SyntheticNightsWithoutSummary(t *testing.T) {
	f := newFakeWithings(t)
	f.NoSleepSummary = true
	sink := &recordingSink{}
	to := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, to, []string{DataTypeSleep}, sink))
	lines := sink.linesOf(DataTypeSleep)
	require.Len(t, lines, 2, "both nights found")
	for _, call := range f.calls("/v2/sleep", "get") {
		start, _ := strconv.ParseInt(call.Form.Get("startdate"), 10, 64)
		end, _ := strconv.ParseInt(call.Form.Get("enddate"), 10, 64)
		assert.LessOrEqual(t, end-start, int64(86400))
	}
}

// A rate limit on a sub-request (waveform, sleep detail) stops the pull with
// the typed error instead of being logged and skipped.
func TestFetch_RateLimitPropagates(t *testing.T) {
	cases := []struct {
		name, dataType, path, action string
		inner                        bool
	}{
		{"ECG waveform 429", DataTypeECG, "/v2/heart", "get", false},
		{"ECG waveform 601", DataTypeECG, "/v2/heart", "get", true},
		{"sleep detail 429", DataTypeSleep, "/v2/sleep", "get", false},
		{"measure 601", DataTypeMeasure, "/measure", "getmeas", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeWithings(t)
			f.RateLimit = func(p, a string) bool { return p == c.path && a == c.action }
			f.RateLimitInner = c.inner
			f.RetryAfter = "120"
			sink := &recordingSink{}
			err := f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{c.dataType}, sink)
			rl, ok := connector.AsRateLimit(err)
			require.True(t, ok, "got %v", err)
			assert.Equal(t, 120*time.Second, rl.RetryAfter)
			assert.Empty(t, sink.calls, "nothing emitted for the interrupted type")
			assert.Len(t, f.calls(c.path, c.action), 1, "no retry inside the connector")
		})
	}
}

func TestYMDChunks(t *testing.T) {
	d := func(m, day int) time.Time { return time.Date(2026, time.Month(m), day, 0, 0, 0, 0, time.UTC) }
	assert.Equal(t, []ymdSpan{{"2026-03-01", "2026-03-02"}}, ymdChunks(d(3, 1), d(3, 3), 30))
	assert.Equal(t, []ymdSpan{{"2026-03-01", "2026-03-03"}}, ymdChunks(d(3, 1), d(3, 3).Add(time.Hour), 30))
	got := ymdChunks(d(1, 1), d(3, 2), 30) // 60 days
	assert.Equal(t, []ymdSpan{{"2026-01-01", "2026-01-30"}, {"2026-01-31", "2026-03-01"}}, got, "no day in two windows")
	assert.Empty(t, ymdChunks(d(3, 3), d(3, 1), 30))
	assert.Empty(t, ymdChunks(d(3, 1), d(3, 1), 30))
}

// An empty page that claims more must stop the loop, on every paginated
// endpoint (heart list included).
func TestPager_EmptyPageWithMoreStops(t *testing.T) {
	c := New(Options{PageDelay: -1})
	calls := 0
	err := c.pager(context.Background(), "heart list", "a", "b", func(int) (int, bool, int, error) {
		calls++
		return 0, true, 5, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

// A night with phase segments but no summary still becomes a session.
func TestSynthesizeNights(t *testing.T) {
	nights := synthesizeNights([]SleepPhaseSegment{
		{Startdate: 1000, Enddate: 2000, State: 1},
		{Startdate: 2000, Enddate: 2600, State: 2},
		{Startdate: 20000, Enddate: 21000, State: 3}, // gap > 2 h: a new night
	})
	require.Len(t, nights, 2)
	assert.EqualValues(t, 1000, nights[0].Startdate)
	assert.EqualValues(t, 2600, nights[0].Enddate)
	assert.EqualValues(t, 1000, nights[0].Data["lightsleepduration"])
	assert.EqualValues(t, 600, nights[0].Data["deepsleepduration"])
	assert.Len(t, nights[0].SleepPhases, 2)
	assert.EqualValues(t, 1000, nights[1].Data["remsleepduration"])

	before := DroppedSleepStates()
	n2 := synthesizeNights([]SleepPhaseSegment{{Startdate: 1000, Enddate: 2000, State: 7}, {Startdate: 2000, Enddate: 2600, State: 2}})
	require.Len(t, n2, 1)
	assert.Len(t, n2[0].SleepPhases, 1, "the unknown state is dropped")
	assert.EqualValues(t, 2000, n2[0].Startdate)
	assert.Equal(t, int64(1), DroppedSleepStates()-before)

	line, err := json.Marshal(nights[0])
	require.NoError(t, err)
	res := mustMap(t, DataTypeSleep, string(line))
	require.Len(t, res.SleepSessions, 1)
	assert.Equal(t, 1600, *res.SleepSessions[0].TotalSleepSeconds)
	assert.Len(t, res.SleepPhases, 2)
}

func TestWithField(t *testing.T) {
	out, err := withField([]byte(`{"a":1}`), "tz", "Europe/Paris")
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1,"tz":"Europe/Paris"}`, string(out))
	assert.True(t, bytes.HasPrefix(out, []byte(`{"tz"`)))

	out, err = withField([]byte(`{}`), "tz", "X")
	require.NoError(t, err)
	assert.Equal(t, `{"tz":"X"}`, string(out))

	out, err = withField([]byte(`{"tz":"kept"}`), "tz", "X")
	require.NoError(t, err)
	assert.Equal(t, `{"tz":"kept"}`, string(out), "an existing field is never overwritten")

	_, err = withField([]byte(`[1]`), "tz", "X")
	assert.Error(t, err)
}

func TestGetChunksBetween(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	chunk := 30 * 24 * time.Hour

	spans := getChunksBetween(start, start.Add(24*time.Hour), chunk)
	require.Len(t, spans, 1)
	assert.True(t, spans[0].end.Equal(start.Add(24*time.Hour)))

	end := start.Add(75 * 24 * time.Hour)
	spans = getChunksBetween(start, end, chunk)
	require.Len(t, spans, 3)
	assert.True(t, spans[0].start.Equal(start))
	for i := 1; i < len(spans); i++ {
		assert.True(t, spans[i].start.Equal(spans[i-1].end), "spans are contiguous")
	}
	assert.True(t, spans[2].end.Equal(end))

	assert.Empty(t, getChunksBetween(end, start, chunk))
	assert.Empty(t, getChunksBetween(start, start, chunk))
}
