// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

var (
	pullFrom = time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	pullTo   = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
)

func TestFetchEveryType(t *testing.T) {
	f := newFake(t)
	c := newConnector(f)
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), pullFrom, pullTo, sink))

	for _, dt := range AllDataTypes {
		assert.Len(t, f.Calls("/v4/users/me/dataTypes/"+dt+"/dataPoints"), 1, dt)
	}
	assert.Len(t, sink.byType(DataTypeSteps), 3)
	assert.Len(t, sink.byType(DataTypeHeartRate), 2)
	assert.Len(t, sink.byType(DataTypeSleep), 1)
	for _, call := range sink.calls {
		for _, l := range call.Lines {
			assert.NotContains(t, string(l), "\n", "one compact line per point")
		}
	}
	// A page is filed under the day of its first point.
	for _, call := range sink.calls {
		if call.DataType == DataTypeHeight {
			assert.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), call.Day)
		}
		if call.DataType == DataTypeDailyRestingHR {
			assert.Equal(t, time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), call.Day)
		}
	}
	req := f.Calls("/v4/users/me/dataTypes/heart-rate/dataPoints")[0]
	assert.Equal(t, "Bearer fake-access-token-1", req.Auth)
	assert.Equal(t, "10000", req.Query.Get("pageSize"))
	assert.Equal(t, "25", f.Calls("/v4/users/me/dataTypes/sleep/dataPoints")[0].Query.Get("pageSize"))
	assert.Equal(t, "25", f.Calls("/v4/users/me/dataTypes/exercise/dataPoints")[0].Query.Get("pageSize"))
}

func TestFetchOnlyGrantedScopes(t *testing.T) {
	f := newFake(t)
	c := newConnector(f)
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(ScopeSleep, "openid"), pullFrom, pullTo, sink))
	assert.Len(t, f.Calls("/v4/users/me/dataTypes/sleep/dataPoints"), 1)
	assert.Empty(t, f.Calls("/v4/users/me/dataTypes/steps/dataPoints"), "activity scope refused: steps skipped")
	assert.Empty(t, f.Calls("/v4/users/me/dataTypes/heart-rate/dataPoints"))
}

func TestFetchPaging(t *testing.T) {
	f := newFake(t)
	var pts []json.RawMessage
	for i := 0; i < 60; i++ {
		s := pullFrom.Add(time.Duration(i) * time.Hour)
		pts = append(pts, json.RawMessage(fmt.Sprintf(
			`{"name":"users/1/dataTypes/sleep/dataPoints/%d","sleep":{"interval":{"startTime":%q,"startUtcOffset":"0s","endTime":%q,"endUtcOffset":"0s"}}}`,
			i, s.Format(time.RFC3339), s.Add(30*time.Minute).Format(time.RFC3339))))
	}
	f.SetPoints(DataTypeSleep, pts)
	c := newConnector(f)
	sink := &recordingSink{}
	require.NoError(t, c.FetchTypes(context.Background(), testConn(), pullFrom, pullTo, []string{DataTypeSleep}, sink))

	calls := f.Calls("/v4/users/me/dataTypes/sleep/dataPoints")
	require.Len(t, calls, 3, "60 sessions in pages of 25")
	assert.Empty(t, calls[0].Query.Get("pageToken"))
	assert.Equal(t, "page-25", calls[1].Query.Get("pageToken"))
	assert.Equal(t, "page-50", calls[2].Query.Get("pageToken"))
	assert.Equal(t, calls[0].Query.Get("filter"), calls[2].Query.Get("filter"), "the filter is repeated on each page")
	assert.Len(t, sink.calls, 3, "one raw object per page")
	assert.Len(t, sink.byType(DataTypeSleep), 60)
}

func TestFetchSkipsDuplicatePoints(t *testing.T) {
	f := newFake(t)
	p := json.RawMessage(fixture(t, DataTypeWeight))
	f.SetPoints(DataTypeWeight, []json.RawMessage{p, p})
	sink := &recordingSink{}
	require.NoError(t, newConnector(f).FetchTypes(context.Background(), testConn(), pullFrom, pullTo, []string{DataTypeWeight}, sink))
	assert.Len(t, sink.byType(DataTypeWeight), 1)
}

func TestListFilter(t *testing.T) {
	from := time.Date(2026, 3, 9, 6, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 10, 6, 0, 0, 0, time.UTC)
	cases := map[string]string{
		DataTypeSteps:          `steps.interval.start_time >= "2026-03-09T06:00:00Z" AND steps.interval.start_time < "2026-03-10T06:00:00Z"`,
		DataTypeHeartRate:      `heart_rate.sample_time.physical_time >= "2026-03-09T06:00:00Z" AND heart_rate.sample_time.physical_time < "2026-03-10T06:00:00Z"`,
		DataTypeDailyRestingHR: `daily_resting_heart_rate.date >= "2026-03-08" AND daily_resting_heart_rate.date < "2026-03-12"`,
		DataTypeExercise:       `exercise.interval.civil_start_time >= "2026-03-08" AND exercise.interval.civil_start_time < "2026-03-12"`,
		DataTypeSleep:          `sleep.interval.end_time >= "2026-03-09T06:00:00Z" AND sleep.interval.end_time < "2026-03-10T06:00:00Z"`,
	}
	for dt, want := range cases {
		d, ok := lookupDataType(dt)
		require.True(t, ok)
		assert.Equal(t, want, listFilter(d, from, to), dt)
	}
}

func TestFetchForbiddenTypeSkipped(t *testing.T) {
	f := newFake(t)
	f.Forbidden[DataTypeHeartRate] = true
	sink := &recordingSink{}
	require.NoError(t, newConnector(f).Fetch(context.Background(), testConn(), pullFrom, pullTo, sink))
	assert.Empty(t, sink.byType(DataTypeHeartRate))
	assert.NotEmpty(t, sink.byType(DataTypeSteps))

	for _, dt := range AllDataTypes {
		f.Forbidden[dt] = true
	}
	err := newConnector(f).Fetch(context.Background(), testConn(), pullFrom, pullTo, &recordingSink{})
	require.ErrorIs(t, err, errForbidden)
}

func TestFetchRateLimited(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusGatewayTimeout} {
		f := newFake(t)
		f.RetryAfter = "120"
		f.Status = func(dt string) int {
			if dt == DataTypeDistance {
				return code
			}
			return 0
		}
		err := newConnector(f).Fetch(context.Background(), testConn(), pullFrom, pullTo, &recordingSink{})
		rl, ok := connector.AsRateLimit(err)
		require.True(t, ok, "HTTP %d", code)
		assert.Equal(t, 2*time.Minute, rl.RetryAfter)
		assert.Empty(t, f.Calls("/v4/users/me/dataTypes/floors/dataPoints"), "the pull stops at once")
	}
}

func TestFetchUnauthorized(t *testing.T) {
	f := newFake(t)
	f.Status = func(string) int { return http.StatusUnauthorized }
	err := newConnector(f).Fetch(context.Background(), testConn(), pullFrom, pullTo, &recordingSink{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)

	err = newConnector(f).Fetch(context.Background(), connector.Connection{}, pullFrom, pullTo, &recordingSink{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestFetchServerErrorRetriedThenReported(t *testing.T) {
	f := newFake(t)
	f.Status = func(dt string) int {
		if dt == DataTypeSteps {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	sink := &recordingSink{}
	err := newConnector(f).Fetch(context.Background(), testConn(), pullFrom, pullTo, sink)
	require.ErrorIs(t, err, errServer)
	assert.Len(t, f.Calls("/v4/users/me/dataTypes/steps/dataPoints"), maxServerAttempts)
	assert.NotEmpty(t, sink.byType(DataTypeSleep), "the other types are still pulled")
}

func TestFetchNotified(t *testing.T) {
	f := newFake(t)
	c := newConnector(f)
	sink := &recordingSink{}
	require.NoError(t, c.FetchNotified(context.Background(), testConn(), pullFrom, pullTo, "heart_rate", sink))
	var paths []string
	for _, r := range f.Requests() {
		paths = append(paths, r.Path)
	}
	assert.Equal(t, []string{"/v4/users/me/dataTypes/heart-rate/dataPoints"}, paths)

	// Not granted: nothing.
	require.NoError(t, c.FetchNotified(context.Background(), testConn(ScopeSleep), pullFrom, pullTo, DataTypeSteps, sink))
	assert.Len(t, f.Requests(), 1)

	// Unknown: everything granted.
	require.NoError(t, c.FetchNotified(context.Background(), testConn(ScopeSleep), pullFrom, pullTo, "mystery", sink))
	assert.Len(t, f.Requests(), 2)
}

func TestNormalizeDataType(t *testing.T) {
	for in, want := range map[string]string{
		"heart-rate": "heart-rate", "heart_rate": "heart-rate", "heartRate": "heart-rate",
		"HEART_RATE": "heart-rate", "dailyVo2Max": "daily-vo2-max", "steps": "steps",
	} {
		assert.Equal(t, want, normalizeDataType(in), in)
	}
	assert.True(t, strings.HasPrefix(normalizeDataType(" Sleep "), "sleep"))
}
