// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/oura/ourafake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// --- server errors and rate limits ---

// failFirst answers the first n requests to path with status (and header).
func failFirst(path string, n int, status int, header http.Header) func(*http.Request) *ourafake.Fault {
	var calls atomic.Int64
	return func(r *http.Request) *ourafake.Fault {
		if r.URL.Path != path {
			return nil
		}
		if int(calls.Add(1)) > n {
			return nil
		}
		return &ourafake.Fault{Status: status, Header: header, Body: `{"detail":"upstream"}`}
	}
}

// A server error is retried with a growing wait, and the pull succeeds once
// Oura answers again.
func TestFetch_ServerErrorRetriedWithBackoff(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newFakeOura(t)
			f.Intercept = failFirst("/v2/usercollection/workout", maxServerAttempts-1, status, nil)
			c := New(Options{
				ClientID: "id", ClientSecret: "secret", APIBase: f.URL(), HTTPClient: f.Client(), PageDelay: -1,
				RetryBase: time.Second, Now: func() time.Time { return fakeNow },
				Sleep: func(_ context.Context, d time.Duration) error { f.slept = append(f.slept, d); return nil },
			})
			sink := &recordingSink{}
			require.NoError(t, c.FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, sink))
			assert.Len(t, sink.linesOf(DataTypeWorkout), 1)
			assert.Len(t, f.Calls("/v2/usercollection/workout"), maxServerAttempts)
			require.Len(t, f.slept, maxServerAttempts-1)
			assert.GreaterOrEqual(t, f.slept[0], time.Second)
			assert.GreaterOrEqual(t, f.slept[1], 2*time.Second, "the wait doubles")
		})
	}
}

// A client error is not retried.
func TestFetch_ClientErrorNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity} {
		f := newFakeOura(t)
		f.Status = map[string]int{"workout": status}
		err := f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, &recordingSink{})
		require.Error(t, err)
		assert.Len(t, f.Calls("/v2/usercollection/workout"), 1)
		assert.Empty(t, f.slept)
		assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
	}
}

// A server error that outlasts the retries fails that data type only: the
// others are still pulled and emitted, and Fetch reports the failure so the
// job is retried.
func TestFetch_PartialFailureKeepsTheOtherTypes(t *testing.T) {
	f := newFakeOura(t)
	f.Status = map[string]int{"workout": http.StatusInternalServerError}
	sink := &recordingSink{}
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink)
	require.Error(t, err)
	assert.ErrorIs(t, err, errServer)
	assert.Contains(t, err.Error(), "workout")
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
	_, limited := connector.AsRateLimit(err)
	assert.False(t, limited)
	assert.Len(t, f.Calls("/v2/usercollection/workout"), maxServerAttempts)
	assert.Empty(t, sink.linesOf(DataTypeWorkout))
	for _, dt := range AllDataTypes {
		if dt != DataTypeWorkout {
			assert.NotEmpty(t, sink.linesOf(dt), "%s still pulled", dt)
		}
	}

	// Two broken types: both named.
	f = newFakeOura(t)
	f.Status = map[string]int{"workout": http.StatusBadGateway}
	f.Bodies = map[string]string{"session": `{"data":"nope"}`}
	err = f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch workout")
	assert.Contains(t, err.Error(), "fetch session")
}

// A 503 that keeps naming a Retry-After defers the job by that much.
func TestFetch_ServerErrorWithRetryAfterDefers(t *testing.T) {
	f := newFakeOura(t)
	f.Intercept = failFirst("/v2/usercollection/sleep", 100, http.StatusServiceUnavailable, http.Header{"Retry-After": {"300"}})
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 300*time.Second, rl.RetryAfter)
	assert.Empty(t, f.Calls("/v2/usercollection/workout"), "a rate limit stops the pull")
}

// HTTP 429 on any endpoint the connector calls is a rate limit, its wait from
// Retry-After (seconds or an HTTP date), never retried in place.
func TestRateLimit_EveryEndpoint(t *testing.T) {
	paths := []string{"/oauth/token", "/v2/usercollection/personal_info"}
	for _, dt := range AllDataTypes {
		paths = append(paths, collectionPath+endpoint(dt))
	}
	for _, p := range paths {
		for retryAfter, want := range map[string]time.Duration{
			"42":                            42 * time.Second,
			"":                              connector.DefaultRetryAfter,
			"Tue, 03 Mar 2026 00:10:00 GMT": 10 * time.Minute,
		} {
			t.Run(p+"/"+retryAfter, func(t *testing.T) {
				f := newFakeOura(t)
				f.RateLimit = func(path string) bool { return path == p }
				f.RetryAfter = retryAfter
				c := f.connector()
				var err error
				switch p {
				case "/oauth/token":
					_, err = c.Refresh(context.Background(), connector.Tokens{Refresh: "r"})
				case "/v2/usercollection/personal_info":
					// A rate limited user id lookup does not fail the connect.
					tok, xerr := c.Exchange(context.Background(), "code", "http://localhost/cb")
					require.NoError(t, xerr)
					assert.Empty(t, tok.ProviderUserID)
					return
				default:
					err = c.Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{})
				}
				rl, ok := connector.AsRateLimit(err)
				require.True(t, ok, "got %v", err)
				assert.Equal(t, want, rl.RetryAfter)
				assert.Len(t, f.Calls(p), 1, "never retried in place")
				assert.Empty(t, f.slept)
			})
		}
	}
}

// --- tokens during a pull ---

// The access token expires half way through a pull (HTTP 401 on page 2): the
// pages before stay emitted, Fetch reports ErrNeedsReauth, and after the
// refresh the collect role runs Fetch again with the new token, which
// completes. Nothing is lost.
func TestFetch_TokenExpiresMidFetch(t *testing.T) {
	f := newFakeOura(t)
	f.Rotate = true
	f.PageSize = 1
	c := f.connector()
	ctx := context.Background()
	tok, err := c.Exchange(ctx, "code-1", "http://localhost/cb")
	require.NoError(t, err)
	var sleepCalls atomic.Int64
	f.Intercept = func(r *http.Request) *ourafake.Fault {
		if r.URL.Path == "/v2/usercollection/sleep" && sleepCalls.Add(1) == 2 {
			f.ExpireAccessTokens()
		}
		return nil
	}
	conn := connector.Connection{Provider: Slug, Tokens: tok}
	first := &recordingSink{}
	err = c.Fetch(ctx, conn, fetchFrom, fetchTo, first)
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
	assert.NotContains(t, err.Error(), tok.Access)
	assert.Len(t, first.linesOf(DataTypeSleep), 1, "page 1 was emitted before the 401")
	assert.NotEmpty(t, first.linesOf(DataTypeDailyActivity))
	assert.Empty(t, f.Calls("/v2/usercollection/workout"), "the pull stopped at the 401")

	fresh, err := c.Refresh(ctx, tok)
	require.NoError(t, err)
	require.NotEqual(t, tok.Refresh, fresh.Refresh, "Oura rotates the refresh token")
	require.NotEqual(t, tok.Access, fresh.Access)
	conn.Tokens = fresh
	second := &recordingSink{}
	require.NoError(t, c.Fetch(ctx, conn, fetchFrom, fetchTo, second))
	assert.Len(t, second.linesOf(DataTypeSleep), 3)
	assert.NotEmpty(t, second.linesOf(DataTypeWorkout))
}

// The user revokes the application at Oura: data answers 401 and the refresh
// is invalid_grant, so the connection ends in needs_reauth.
func TestFetch_RevokedConsent(t *testing.T) {
	f := newFakeOura(t)
	f.Rotate = true
	c := f.connector()
	ctx := context.Background()
	tok, err := c.Exchange(ctx, "code-1", "http://localhost/cb")
	require.NoError(t, err)
	require.NoError(t, c.FetchTypes(ctx, connector.Connection{Tokens: tok}, fetchFrom, fetchTo, []string{DataTypeWorkout}, &recordingSink{}))

	f.RevokeConsent()
	err = c.Fetch(ctx, connector.Connection{Tokens: tok}, fetchFrom, fetchTo, &recordingSink{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
	_, err = c.Refresh(ctx, tok)
	require.ErrorIs(t, err, connector.ErrNeedsReauth)

	// Connecting again works.
	tok2, err := c.Exchange(ctx, "code-2", "http://localhost/cb")
	require.NoError(t, err)
	require.NoError(t, c.FetchTypes(ctx, connector.Connection{Tokens: tok2}, fetchFrom, fetchTo, []string{DataTypeWorkout}, &recordingSink{}))
}

// --- cancellation ---

// A cancelled context stops the pull at once, mid page or mid retry wait.
func TestFetch_ContextCancellation(t *testing.T) {
	t.Run("during a request", func(t *testing.T) {
		f := newFakeOura(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.Intercept = func(r *http.Request) *ourafake.Fault {
			if r.URL.Path == "/v2/usercollection/sleep" {
				cancel()
			}
			return nil
		}
		err := f.connector().Fetch(ctx, testConn(), fetchFrom, fetchTo, &recordingSink{})
		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, f.Calls("/v2/usercollection/workout"))
		assert.Empty(t, f.Calls("/v2/usercollection/daily_sleep"))
	})
	t.Run("during a retry wait", func(t *testing.T) {
		f := newFakeOura(t)
		f.Status = map[string]int{"daily_activity": http.StatusServiceUnavailable}
		ctx, cancel := context.WithCancel(context.Background())
		c := New(Options{
			ClientID: "id", ClientSecret: "secret", APIBase: f.URL(), HTTPClient: f.Client(), PageDelay: -1,
			Sleep: func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() },
		})
		err := c.Fetch(ctx, testConn(), fetchFrom, fetchTo, &recordingSink{})
		require.ErrorIs(t, err, context.Canceled)
		assert.Len(t, f.Calls("/v2/usercollection/daily_activity"), 1)
		assert.Empty(t, f.Calls("/v2/usercollection/sleep"))
	})
	t.Run("already cancelled", func(t *testing.T) {
		f := newFakeOura(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := f.connector().Fetch(ctx, testConn(), fetchFrom, fetchTo, &recordingSink{})
		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, f.Requests())
	})
	t.Run("real pause between pages", func(t *testing.T) {
		f := newFakeOura(t)
		f.PageSize = 1
		ctx, cancel := context.WithCancel(context.Background())
		c := New(Options{ClientID: "id", ClientSecret: "secret", APIBase: f.URL(), HTTPClient: f.Client(), PageDelay: time.Hour})
		go func() {
			for len(f.Requests()) == 0 {
				time.Sleep(time.Millisecond)
			}
			cancel()
		}()
		done := make(chan error, 1)
		go func() { done <- c.Fetch(ctx, testConn(), fetchFrom, fetchTo, &recordingSink{}) }()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(10 * time.Second):
			t.Fatal("Fetch did not return after cancellation")
		}
	})
}

// A raw store that refuses a page stops the pull: nothing is skipped
// silently.
func TestFetch_SinkFailureStops(t *testing.T) {
	f := newFakeOura(t)
	boom := errors.New("raw store down")
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, failingSink{boom})
	require.ErrorIs(t, err, boom)
	assert.Empty(t, f.Calls("/v2/usercollection/sleep"), "the first type failed, nothing after it was asked")
}

type failingSink struct{ err error }

func (s failingSink) Emit(context.Context, string, time.Time, [][]byte) error { return s.err }

// --- pagination ---

// pager serves one data type from a script of pages: page i answers
// pages[i] for next_token "" (i = 0) or "p<i>".
func pager(path string, pages ...string) func(*http.Request) *ourafake.Fault {
	return func(r *http.Request) *ourafake.Fault {
		if r.URL.Path != path {
			return nil
		}
		i := 0
		if nt := r.URL.Query().Get("next_token"); nt != "" {
			_, _ = fmt.Sscanf(nt, "p%d", &i)
		}
		if i >= len(pages) {
			return &ourafake.Fault{Status: http.StatusOK, Body: `{"data":[],"next_token":null}`}
		}
		return &ourafake.Fault{Status: http.StatusOK, Body: pages[i]}
	}
}

func workoutDoc(id, day string) string {
	return `{"id":"` + id + `","activity":"walking","calories":100,"day":"` + day + `","distance":1000,` +
		`"end_datetime":"` + day + `T10:30:00+01:00","intensity":"easy","label":null,"source":"autodetected",` +
		`"start_datetime":"` + day + `T10:00:00+01:00"}`
}

func TestFetch_Pagination(t *testing.T) {
	const p = "/v2/usercollection/workout"
	cases := []struct {
		name  string
		pages []string
		calls int
		lines int
	}{
		{"empty first page", []string{`{"data":[],"next_token":null}`}, 1, 0},
		{"next_token absent", []string{`{"data":[` + workoutDoc("w1", "2026-03-01") + `]}`}, 1, 1},
		{"next_token empty string", []string{`{"data":[` + workoutDoc("w1", "2026-03-01") + `],"next_token":""}`}, 1, 1},
		{"empty pages then data", []string{
			`{"data":[],"next_token":"p1"}`, `{"data":[],"next_token":"p2"}`,
			`{"data":[` + workoutDoc("w1", "2026-03-01") + `],"next_token":null}`,
		}, 3, 1},
		{"empty pages forever", []string{
			`{"data":[],"next_token":"p1"}`, `{"data":[],"next_token":"p2"}`, `{"data":[],"next_token":"p3"}`,
			`{"data":[],"next_token":"p4"}`, `{"data":[],"next_token":"p5"}`,
		}, maxEmptyPages, 0},
		{"a loop of two tokens", []string{
			`{"data":[` + workoutDoc("w1", "2026-03-01") + `],"next_token":"p1"}`,
			`{"data":[` + workoutDoc("w2", "2026-03-02") + `],"next_token":"p2"}`,
			`{"data":[` + workoutDoc("w3", "2026-03-02") + `],"next_token":"p1"}`,
		}, 3, 3},
		{"the same document on two pages", []string{
			`{"data":[` + workoutDoc("w1", "2026-03-01") + `],"next_token":"p1"}`,
			`{"data":[` + workoutDoc("w1", "2026-03-01") + `,` + workoutDoc("w2", "2026-03-02") + `],"next_token":null}`,
		}, 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOura(t)
			f.Intercept = pager(p, tc.pages...)
			sink := &recordingSink{}
			require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, sink))
			assert.Len(t, f.Calls(p), tc.calls)
			assert.Len(t, sink.linesOf(DataTypeWorkout), tc.lines)
		})
	}
}

// A runaway pagination is cut at maxPagesPerWindow.
func TestFetch_PaginationBounded(t *testing.T) {
	f := newFakeOura(t)
	var n atomic.Int64
	f.Intercept = func(r *http.Request) *ourafake.Fault {
		if r.URL.Path != "/v2/usercollection/workout" {
			return nil
		}
		i := n.Add(1)
		return &ourafake.Fault{Status: http.StatusOK, Body: fmt.Sprintf(`{"data":[%s],"next_token":"t%d"}`, workoutDoc(fmt.Sprint("w", i), "2026-03-01"), i)}
	}
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, &recordingSink{}))
	assert.Len(t, f.Calls("/v2/usercollection/workout"), maxPagesPerWindow)
}

// A copy of a document whose bytes changed between two pages (Oura re-scored
// it mid pull) is kept: Map gives both the same identity, the later wins.
func TestFetch_ChangedCopyIsKept(t *testing.T) {
	f := newFakeOura(t)
	a := workoutDoc("w1", "2026-03-01")
	b := strings.Replace(a, `"calories":100`, `"calories":120`, 1)
	f.Intercept = pager("/v2/usercollection/workout",
		`{"data":[`+a+`],"next_token":"p1"}`, `{"data":[`+b+`],"next_token":null}`)
	c := f.connector()
	sink := &recordingSink{}
	require.NoError(t, c.FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, sink))
	require.Len(t, sink.linesOf(DataTypeWorkout), 2)
	res := mapEmitted(t, c, sink, DataTypeWorkout)
	require.Len(t, res.Workouts, 2)
	assert.Equal(t, res.Workouts[0].ContentHash, res.Workouts[1].ContentHash)
	assert.True(t, res.Workouts[1].Replaceable)
}

// A page whose documents name no day is filed under the window's first day.
func TestFetch_PageWithoutDay(t *testing.T) {
	f := newFakeOura(t)
	f.Bodies = map[string]string{"workout": `{"data":[{"id":"w"}],"next_token":null}`}
	sink := &recordingSink{}
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, sink))
	require.Len(t, sink.calls, 1)
	assert.Equal(t, time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), sink.calls[0].Day)
}

// --- windows ---

// everyDay returns one workout document per day in [from, to].
func everyDay(from, to time.Time) []map[string]any {
	var docs []map[string]any
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		var m map[string]any
		day := d.Format(time.DateOnly)
		_ = json.Unmarshal([]byte(workoutDoc("w-"+day, day)), &m)
		docs = append(docs, m)
	}
	return docs
}

// Over a long backfill, every day is fetched exactly once, whether Oura's
// end_date is inclusive or exclusive: the windows overlap on their boundary
// day and the copy is skipped.
func TestFetch_LargeWindowMissesNoDay(t *testing.T) {
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, exclusive := range []bool{false, true} {
		t.Run(fmt.Sprintf("exclusive=%v", exclusive), func(t *testing.T) {
			f := newFakeOura(t)
			f.ExclusiveEndDate = exclusive
			f.PageSize = 7
			f.SetDocuments(DataTypeWorkout, everyDay(from.AddDate(0, 0, -5), to.AddDate(0, 0, 5))...)
			sink := &recordingSink{}
			require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), from, to, []string{DataTypeWorkout}, sink))
			got := map[string]int{}
			for _, l := range sink.linesOf(DataTypeWorkout) {
				var w Workout
				require.NoError(t, json.Unmarshal(l, &w))
				got[w.Day]++
			}
			for d := from.AddDate(0, 0, -1); !d.After(to); d = d.AddDate(0, 0, 1) {
				assert.Equal(t, 1, got[d.Format(time.DateOnly)], "day %s", d.Format(time.DateOnly))
			}
			for day, n := range got {
				assert.Equal(t, 1, n, "day %s emitted once", day)
			}
			calls := f.Calls("/v2/usercollection/workout")
			assert.GreaterOrEqual(t, len(calls), 15, "chunked")
			for _, c := range calls {
				s, _ := time.Parse(time.DateOnly, c.Query.Get("start_date"))
				e, _ := time.Parse(time.DateOnly, c.Query.Get("end_date"))
				assert.LessOrEqual(t, e.Sub(s), dateChunkDays*24*time.Hour, "a window spans at most %d days", dateChunkDays)
			}
		})
	}
}

// Heart rate windows are consecutive, at most 7 days, and cover [from, to)
// exactly, whatever the clock time of from and to.
func TestWindows_HeartRateCoverage(t *testing.T) {
	from := time.Date(2026, 1, 1, 13, 17, 0, 0, time.FixedZone("x", 3600))
	to := from.Add(45*24*time.Hour + 5*time.Minute)
	ws := windows(DataTypeHeartRate, from, to)
	require.NotEmpty(t, ws)
	prev := from.UTC()
	for _, w := range ws {
		s, err := time.Parse(time.RFC3339, w.Get("start_datetime"))
		require.NoError(t, err)
		e, err := time.Parse(time.RFC3339, w.Get("end_datetime"))
		require.NoError(t, err)
		assert.True(t, s.Equal(prev), "consecutive")
		assert.LessOrEqual(t, e.Sub(s), heartRateChunk)
		prev = e
	}
	assert.True(t, prev.Equal(to.UTC()))
}

// The date windows reach one day before from (a document's day is a local
// day, up to 14 hours ahead of UTC) and one after to (up to 12 hours behind),
// for any clock time of from and to.
func TestWindows_DateBoundaries(t *testing.T) {
	cases := []struct {
		from, to   time.Time
		start, end string
	}{
		{time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC), "2026-02-28", "2026-03-04"},
		{time.Date(2026, 3, 1, 23, 59, 0, 0, time.UTC), time.Date(2026, 3, 3, 0, 0, 1, 0, time.UTC), "2026-02-28", "2026-03-05"},
		// from and to in another zone are read as instants.
		{time.Date(2026, 3, 1, 1, 0, 0, 0, time.FixedZone("x", 2*3600)), time.Date(2026, 3, 1, 3, 0, 0, 0, time.FixedZone("x", 2*3600)), "2026-02-27", "2026-03-03"},
		// The last day of a month, a year end.
		{time.Date(2025, 12, 31, 18, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC), "2025-12-30", "2026-01-03"},
	}
	for _, tc := range cases {
		ws := windows(DataTypeSleep, tc.from, tc.to)
		require.NotEmpty(t, ws)
		assert.Equal(t, tc.start, ws[0].Get("start_date"), tc.from.String())
		assert.Equal(t, tc.end, ws[len(ws)-1].Get("end_date"), tc.to.String())
	}
}

// A fetch whose window touches a day stores what Oura holds for that local
// day: a night ending on 3 March in UTC+14 (still 2 March in UTC) and one in
// UTC-12 are both fetched for the UTC window of 2 March.
func TestFetch_LocalDaysAtTheWindowEdges(t *testing.T) {
	f := newFakeOura(t)
	night := func(id, day, start, end string) map[string]any {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(fixture(t, DataTypeSleep, 1)), &m))
		m["id"], m["day"], m["bedtime_start"], m["bedtime_end"] = id, day, start, end
		return m
	}
	f.SetDocuments(DataTypeSleep,
		night("kiritimati", "2026-03-03", "2026-03-02T23:00:00+14:00", "2026-03-03T07:00:00+14:00"),
		night("baker", "2026-03-01", "2026-03-01T23:00:00-12:00", "2026-03-02T07:00:00-12:00"),
		night("outside", "2026-02-20", "2026-02-19T23:00:00+01:00", "2026-02-20T07:00:00+01:00"),
	)
	sink := &recordingSink{}
	from := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), from, from.Add(24*time.Hour), []string{DataTypeSleep}, sink))
	var ids []string
	for _, l := range sink.linesOf(DataTypeSleep) {
		var s Sleep
		require.NoError(t, json.Unmarshal(l, &s))
		ids = append(ids, s.ID)
	}
	assert.ElementsMatch(t, []string{"kiritimati", "baker"}, ids)
}
