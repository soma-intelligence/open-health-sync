// SPDX-License-Identifier: AGPL-3.0-only

package ultrahuman

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func TestFetch_OneCallPerDay(t *testing.T) {
	f := newFake(t)
	c := f.connector()
	sink := &recordingSink{}
	from := time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 10, 6, 0, 0, 0, time.UTC)
	require.NoError(t, c.Fetch(context.Background(), testConn(), from, to, sink))

	calls := f.Calls("/api/partners/v1/user_data/metrics")
	var asked []string
	for _, r := range calls {
		asked = append(asked, r.Query.Get("date"))
		require.Equal(t, "Bearer fake-access-token-1", r.Auth)
	}
	require.Equal(t, []string{"2026-03-06", "2026-03-07", "2026-03-08", "2026-03-09", "2026-03-10"}, asked,
		"March 6 runs until 12:00 UTC on March 7 in UTC-12")

	// Only the two days with data are stored, one line each.
	require.Len(t, sink.calls, 2)
	for i, want := range []string{"2026-03-08", "2026-03-09"} {
		call := sink.calls[i]
		require.Equal(t, DataTypeMetrics, call.DataType)
		require.Equal(t, want, call.Day.Format("2006-01-02"))
		require.Len(t, call.Lines, 1)
		require.NotContains(t, string(call.Lines[0]), "\n")
		var rd rawDay
		require.NoError(t, json.Unmarshal(call.Lines[0], &rd))
		require.Equal(t, want, rd.Date)
	}
	f.mu.Lock()
	require.Len(t, f.slept, 4, "days are paced")
	f.mu.Unlock()

	res := mapEmitted(t, c, sink)
	require.Len(t, res.SleepSessions, 2)
	require.NotEmpty(t, res.QuantitySamples)
}

func TestFetch_MidnightEndExcludesThatDate(t *testing.T) {
	got := dates(time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC))
	require.Len(t, got, 2)
	require.Equal(t, "2026-03-09", got[1].Format("2006-01-02"))
	require.Len(t, dates(time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)), 1)
	require.Empty(t, dates(time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)))
	long := dates(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC))
	require.Len(t, long, maxDays)
	require.Equal(t, "2026-03-08", long[len(long)-1].Format("2006-01-02"))
}

func TestFetch_Unauthorized(t *testing.T) {
	for _, st := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		f := newFake(t)
		f.MetricsStatus = func(date string) int {
			if date == "2026-03-09" {
				return st
			}
			return 0
		}
		sink := &recordingSink{}
		err := f.connector().Fetch(context.Background(), testConn(), time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC), sink)
		require.ErrorIs(t, err, connector.ErrNeedsReauth, "status %d", st)
		require.Len(t, sink.calls, 1, "the day before the refusal is kept")
	}

	f := newFake(t)
	f.AccessToken = "other"
	err := f.connector().Fetch(context.Background(), testConn(), fakeNow, fakeNow, &recordingSink{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)

	err = f.connector().Fetch(context.Background(), connector.Connection{}, fakeNow, fakeNow, &recordingSink{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestFetch_RateLimited(t *testing.T) {
	f := newFake(t)
	f.RetryAfter = "120"
	f.MetricsStatus = func(date string) int {
		if date == "2026-03-09" {
			return http.StatusTooManyRequests
		}
		return 0
	}
	err := f.connector().Fetch(context.Background(), testConn(), time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), &recordingSink{})
	var rl *connector.RateLimitError
	require.True(t, errors.As(err, &rl))
	require.Equal(t, 120*time.Second, rl.RetryAfter)
	require.NotErrorIs(t, err, connector.ErrNeedsReauth)
	require.Len(t, f.Calls("/api/partners/v1/user_data/metrics"), 3, "March 7 and 8, then stops at the rate limit")
}

func TestFetch_NoDataAndServerErrors(t *testing.T) {
	f := newFake(t)
	f.MetricsStatus = func(date string) int {
		if date == "2026-03-08" {
			return http.StatusNotFound
		}
		return 0
	}
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC), sink))
	require.Len(t, sink.calls, 1)

	f2 := newFake(t)
	f2.MetricsStatus = func(string) int { return http.StatusInternalServerError }
	err := f2.connector().Fetch(context.Background(), testConn(), fakeNow, fakeNow, &recordingSink{})
	require.ErrorContains(t, err, "HTTP 500")
	require.NotErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestFetch_CancelledContext(t *testing.T) {
	f := newFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := f.connector().Fetch(ctx, testConn(), fakeNow, fakeNow, &recordingSink{})
	require.Error(t, err)
}

func TestDayLine(t *testing.T) {
	line, err := dayLine("2026-03-09", []byte(`{"data":{"metric_data":[]}}`))
	require.NoError(t, err)
	require.Nil(t, line)
	line, err = dayLine("2026-03-09", nil)
	require.NoError(t, err)
	require.Nil(t, line)
	_, err = dayLine("2026-03-09", []byte(`[1,2]`))
	require.Error(t, err)
	_, err = dayLine("2026-03-09", []byte(`not json`))
	require.Error(t, err)
}

// Fetch then Map end to end: the mapped records are deterministic across two
// runs, so the ETL re-mapping a day never duplicates.
func TestFetch_MapDeterministic(t *testing.T) {
	run := func() model.MapResult {
		f := newFake(t)
		c := f.connector()
		sink := &recordingSink{}
		require.NoError(t, c.Fetch(context.Background(), testConn(), time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC), sink))
		return mapEmitted(t, c, sink)
	}
	a, b := run(), run()
	require.Equal(t, len(a.QuantitySamples), len(b.QuantitySamples))
	for i := range a.QuantitySamples {
		require.Equal(t, a.QuantitySamples[i].ContentHash, b.QuantitySamples[i].ContentHash)
	}
	require.Equal(t, a.SleepSessions[0].ContentHash, b.SleepSessions[0].ContentHash)
}

// Ultrahuman dates are the user's local days. A window asks every date whose
// local day can overlap it, in any zone from UTC-12 to UTC+14: a pull at
// 02:00 to 04:00 UTC on March 10 still asks March 9, which in California
// (UTC-7) runs until 07:00 UTC on March 10.
func TestFetch_AsksEveryLocalDayOfTheWindow(t *testing.T) {
	day := func(ds []time.Time) []string {
		var out []string
		for _, d := range ds {
			out = append(out, d.Format("2006-01-02"))
		}
		return out
	}
	require.Equal(t, []string{"2026-03-09", "2026-03-10"},
		day(localDates(time.Date(2026, 3, 10, 2, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 4, 0, 0, 0, time.UTC))))
	require.Equal(t, []string{"2026-03-10", "2026-03-11"},
		day(localDates(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC))),
		"UTC+14 is already on March 11 at 10:00 UTC")

	f := newFake(t)
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(),
		time.Date(2026, 3, 10, 2, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 4, 0, 0, 0, time.UTC), &recordingSink{}))
	var asked []string
	for _, r := range f.Calls("/api/partners/v1/user_data/metrics") {
		asked = append(asked, r.Query.Get("date"))
	}
	require.Equal(t, []string{"2026-03-09", "2026-03-10"}, asked)
}
