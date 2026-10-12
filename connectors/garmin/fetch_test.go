// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/garmin/garminfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestFetch_ChunksByDayAndEmitsEveryType(t *testing.T) {
	f := newFakeGarmin(t)
	f.AccessToken = "fake-access-token-1"
	sink := &recordingSink{}
	from := fakeNow.Add(-72 * time.Hour)
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), from, fakeNow, sink))

	for _, dt := range AllDataTypes {
		calls := f.Calls(http.MethodGet, "/wellness-api/rest/"+dt)
		require.Len(t, calls, 3, "%s: 72 h in windows of 24 h", dt)
		prevEnd := from.Unix()
		for _, c := range calls {
			s, _ := strconv.ParseInt(c.Query.Get("uploadStartTimeInSeconds"), 10, 64)
			e, _ := strconv.ParseInt(c.Query.Get("uploadEndTimeInSeconds"), 10, 64)
			assert.Equal(t, prevEnd, s, "%s: windows are contiguous", dt)
			assert.LessOrEqual(t, e-s, int64(86400))
			assert.Equal(t, "Bearer fake-access-token-1", c.Auth)
			prevEnd = e
		}
		assert.Equal(t, fakeNow.Unix(), prevEnd)
		assert.Len(t, sink.lines(dt), len(garminfake.Summaries(dt)), dt)
	}
	// Lines are filed under the day they describe.
	for _, c := range sink.calls {
		if c.DataType == DataTypeSleeps {
			assert.Equal(t, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), c.Day)
		}
		if c.DataType == DataTypeDailies {
			assert.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), c.Day)
		}
	}
	// Each line is one compact object holding the summary as sent.
	line := sink.lines(DataTypeActivities)[0]
	assert.NotContains(t, string(line), "\n")
	var obj map[string]any
	require.NoError(t, json.Unmarshal(line, &obj))
	assert.Equal(t, "RUNNING", obj["activityType"])
}

func TestFetch_SplitsAnUnevenWindowAndClampsToNow(t *testing.T) {
	f := newFakeGarmin(t)
	from := fakeNow.Add(-30 * time.Hour)
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), from, fakeNow.Add(48*time.Hour), []string{DataTypeDailies}, &recordingSink{}))
	calls := f.Calls(http.MethodGet, "/wellness-api/rest/dailies")
	require.Len(t, calls, 2)
	assert.Equal(t, strconv.FormatInt(fakeNow.Unix(), 10), calls[1].Query.Get("uploadEndTimeInSeconds"), "never past now")
	s, _ := strconv.ParseInt(calls[1].Query.Get("uploadStartTimeInSeconds"), 10, 64)
	assert.Equal(t, int64(6*3600), fakeNow.Unix()-s)

	// An empty or reversed window asks nothing.
	f2 := newFakeGarmin(t)
	require.NoError(t, f2.connector().Fetch(context.Background(), testConn(), fakeNow, fakeNow, &recordingSink{}))
	assert.Empty(t, f2.Requests())
}

func TestFetch_TypeNotGrantedIsSkipped(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusForbidden} {
		f := newFakeGarmin(t)
		f.NotGranted = map[string]int{DataTypeBloodPressures: code, DataTypeSleeps: code}
		sink := &recordingSink{}
		require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fakeNow.Add(-72*time.Hour), fakeNow, sink))
		assert.Empty(t, sink.lines(DataTypeBloodPressures))
		assert.Empty(t, sink.lines(DataTypeSleeps))
		assert.NotEmpty(t, sink.lines(DataTypeDailies))
		assert.Len(t, f.Calls(http.MethodGet, "/wellness-api/rest/sleeps"), 1, "the first refusal skips the type's other windows")
	}
}

func TestFetch_UnauthorizedNeedsReauth(t *testing.T) {
	f := newFakeGarmin(t)
	f.AccessToken = "another-token"
	err := f.connector().Fetch(context.Background(), testConn(), fakeNow.Add(-time.Hour), fakeNow, &recordingSink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)

	err = f.connector().Fetch(context.Background(), connector.Connection{}, fakeNow.Add(-time.Hour), fakeNow, &recordingSink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestFetch_RateLimited(t *testing.T) {
	f := newFakeGarmin(t)
	f.RetryAfter = "120"
	f.RateLimit = func(p string) bool { return strings.HasSuffix(p, "/epochs") }
	sink := &recordingSink{}
	err := f.connector().Fetch(context.Background(), testConn(), fakeNow.Add(-72*time.Hour), fakeNow, sink)
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 120*time.Second, rl.RetryAfter)
	assert.NotEmpty(t, sink.lines(DataTypeDailies), "types before the limit were emitted")
}

func TestFetch_ServerErrorAndBadAnswer(t *testing.T) {
	f := newFakeGarmin(t)
	f.DataHTTP = http.StatusInternalServerError
	err := f.connector().Fetch(context.Background(), testConn(), fakeNow.Add(-time.Hour), fakeNow, &recordingSink{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)

	f.DataHTTP = http.StatusNotFound
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fakeNow.Add(-time.Hour), fakeNow, &recordingSink{}), "404 is no data")
}

func TestFetchNotified_PullsOnlyTheNamedType(t *testing.T) {
	f := newFakeGarmin(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector().FetchNotified(context.Background(), testConn(), fakeNow.Add(-72*time.Hour), fakeNow, DataTypeSleeps, sink))
	for _, r := range f.Requests() {
		assert.Equal(t, "/wellness-api/rest/sleeps", r.Path)
	}
	assert.Len(t, sink.lines(DataTypeSleeps), 1)

	// A notification key alias pulls its data type.
	f2 := newFakeGarmin(t)
	require.NoError(t, f2.connector().FetchNotified(context.Background(), testConn(), fakeNow.Add(-time.Hour), fakeNow, "allDayRespiration", &recordingSink{}))
	require.Len(t, f2.Requests(), 1)
	assert.Equal(t, "/wellness-api/rest/respiration", f2.Requests()[0].Path)

	// Unknown: everything.
	f3 := newFakeGarmin(t)
	require.NoError(t, f3.connector().FetchNotified(context.Background(), testConn(), fakeNow.Add(-time.Hour), fakeNow, "nope", &recordingSink{}))
	assert.Len(t, f3.Requests(), len(AllDataTypes))
}

func TestFetch_RetriesTransportErrors(t *testing.T) {
	f := newFakeGarmin(t)
	c := f.connector(func(o *Options) { o.APIBase = "http://127.0.0.1:1" })
	err := c.FetchTypes(context.Background(), testConn(), fakeNow.Add(-time.Hour), fakeNow, []string{DataTypeDailies}, &recordingSink{})
	require.Error(t, err)
	assert.Len(t, f.slept, maxAttempts-1)
}

func TestCleanLine_DropsAccessToken(t *testing.T) {
	line, day, err := cleanLine([]byte(`{"userId":"u","userAccessToken":"secret","summaryId":"s","calendarDate":"2026-03-02"}`))
	require.NoError(t, err)
	assert.NotContains(t, string(line), "secret")
	assert.NotContains(t, string(line), "userAccessToken")
	assert.Equal(t, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), day)

	_, _, err = cleanLine([]byte(`[1]`))
	assert.Error(t, err)
}
