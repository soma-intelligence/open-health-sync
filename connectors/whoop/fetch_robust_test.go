// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/whoop/whoopfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// workoutPage is one /v2/activity/workout page of the given ids.
func workoutPage(next string, ids ...string) string {
	recs := make([]string, len(ids))
	for i, id := range ids {
		recs[i] = fmt.Sprintf(`{"id":%q,"user_id":10001,"created_at":"2026-03-01T10:00:00.000Z","updated_at":"2026-03-01T10:00:00.000Z",`+
			`"start":"2026-03-01T09:00:00.000Z","end":"2026-03-01T09:30:00.000Z","timezone_offset":"+01:00",`+
			`"sport_name":"running","score_state":"PENDING_SCORE"}`, id)
	}
	out := `{"records":[` + strings.Join(recs, ",") + `]`
	if next != "" {
		out += `,"next_token":"` + next + `"`
	}
	return out + "}"
}

func pullWorkouts(t *testing.T, f *fakeWhoop) (*recordingSink, error) {
	t.Helper()
	sink := &recordingSink{}
	err := f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout}, sink)
	return sink, err
}

// A next_token that comes back again would loop for ever: the walk stops.
func TestFetch_PaginationLoopGuard(t *testing.T) {
	f := newFakeWhoop(t)
	f.Page = func(_ string, q url.Values) (string, bool) {
		switch q.Get("nextToken") {
		case "":
			return workoutPage("A", "w1"), true
		case "A":
			return workoutPage("B", "w2"), true
		default: // B, and anything after, points back to A
			return workoutPage("A", "w3"), true
		}
	}
	sink, err := pullWorkouts(t, f)
	require.NoError(t, err)
	assert.Len(t, f.Calls("/v2/activity/workout"), 3, "A, B, then A again is not followed")
	assert.Len(t, sink.linesOf(DataTypeWorkout), 3)
}

// The page cap stops a provider that never ends.
func TestFetch_PaginationCap(t *testing.T) {
	f := newFakeWhoop(t)
	f.Page = func(_ string, q url.Values) (string, bool) {
		n := len(q.Get("nextToken"))
		return workoutPage(strings.Repeat("x", n+1), fmt.Sprintf("w%d", n)), true
	}
	_, err := pullWorkouts(t, f)
	require.NoError(t, err)
	assert.Len(t, f.Calls("/v2/activity/workout"), maxPagesPerFetch)
}

// An empty page is fine; one that still names a next page stops the walk.
func TestFetch_EmptyPages(t *testing.T) {
	f := newFakeWhoop(t)
	f.Page = func(string, url.Values) (string, bool) { return `{"records":[]}`, true }
	sink, err := pullWorkouts(t, f)
	require.NoError(t, err)
	assert.Empty(t, sink.calls, "nothing to emit")

	f = newFakeWhoop(t)
	f.Page = func(string, url.Values) (string, bool) { return `{"records":[],"next_token":"more"}`, true }
	_, err = pullWorkouts(t, f)
	require.NoError(t, err)
	assert.Len(t, f.Calls("/v2/activity/workout"), 1)

	f = newFakeWhoop(t)
	f.Page = func(string, url.Values) (string, bool) { return `{"records":null,"next_token":null}`, true }
	_, err = pullWorkouts(t, f)
	require.NoError(t, err)
}

// A record repeated on a later page (WHOOP pages by offset, and a record
// added meanwhile shifts them) is emitted once.
func TestFetch_DuplicateAcrossPages(t *testing.T) {
	f := newFakeWhoop(t)
	f.Page = func(_ string, q url.Values) (string, bool) {
		if q.Get("nextToken") == "" {
			return workoutPage("p2", "w1", "w2"), true
		}
		return workoutPage("", "w2", "w3"), true
	}
	sink, err := pullWorkouts(t, f)
	require.NoError(t, err)
	var ids []string
	for _, l := range sink.linesOf(DataTypeWorkout) {
		var w Workout
		require.NoError(t, json.Unmarshal(l, &w))
		ids = append(ids, string(w.ID))
	}
	assert.Equal(t, []string{"w1", "w2", "w3"}, ids)
}

// A record that is not a JSON object is skipped, the others kept.
func TestFetch_NonObjectRecordSkipped(t *testing.T) {
	f := newFakeWhoop(t)
	f.Page = func(string, url.Values) (string, bool) {
		return `{"records":[null,1,"x",` + strings.TrimSuffix(strings.TrimPrefix(workoutPage("", "w1"), `{"records":[`), `]}`) + `]}`, true
	}
	sink, err := pullWorkouts(t, f)
	require.NoError(t, err)
	assert.Len(t, sink.linesOf(DataTypeWorkout), 1)
}

// The connector never asks for more than the documented 25 a page, and the
// fake (like WHOOP) refuses a larger limit.
func TestFetch_LimitNeverOver25(t *testing.T) {
	f := newFakeWhoop(t)
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, &recordingSink{}))
	for _, r := range f.Requests() {
		if l := r.Query.Get("limit"); l != "" {
			assert.Equal(t, "25", l, r.Path)
		}
	}
	assert.Equal(t, 25, maxPageLimit)

	req, err := http.NewRequest(http.MethodGet, f.URL()+"/v2/cycle?limit=26", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer x")
	res, err := f.Client().Do(req)
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusBadRequest, res.StatusCode)
}

// Server errors are retried on the doubling ladder; a recovered server gives
// a complete pull.
func TestFetch_ServerErrorRetriedThenRecovers(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504} {
		f := newFakeWhoop(t)
		f.Fail = func(path string, n int) *whoopfake.Fault {
			if path == "/v2/activity/sleep" && n <= 2 {
				return &whoopfake.Fault{Status: status, Body: "upstream sad"}
			}
			return nil
		}
		sink := &recordingSink{}
		require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink), status)
		assert.Len(t, sink.linesOf(DataTypeSleep), 3, status)
		require.Len(t, f.slept, 2, status)
		assert.GreaterOrEqual(t, f.slept[0], 10*time.Second)
		assert.GreaterOrEqual(t, f.slept[1], 20*time.Second, "the wait doubles")
	}
}

// A client error other than 401, 403, 404 and 429 is not retried.
func TestFetch_ClientErrorNotRetried(t *testing.T) {
	f := newFakeWhoop(t)
	f.Fail = func(path string, _ int) *whoopfake.Fault {
		if path == "/v2/activity/workout" {
			return &whoopfake.Fault{Status: http.StatusBadRequest}
		}
		return nil
	}
	_, err := pullWorkouts(t, f)
	var se *statusError
	require.True(t, errors.As(err, &se), "%v", err)
	assert.Equal(t, http.StatusBadRequest, se.status)
	assert.Len(t, f.Calls("/v2/activity/workout"), 1)
	assert.Empty(t, f.slept)
}

// One data type failing for good does not cost the others: they are pulled,
// and the error (joined) makes the job retry.
func TestFetch_PartialFailure(t *testing.T) {
	f := newFakeWhoop(t)
	f.Fail = func(path string, _ int) *whoopfake.Fault {
		if path == "/v2/recovery" {
			return &whoopfake.Fault{Status: http.StatusServiceUnavailable}
		}
		return nil
	}
	sink := &recordingSink{}
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch recovery")
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
	_, limited := connector.AsRateLimit(err)
	assert.False(t, limited)
	assert.Len(t, f.Calls("/v2/recovery"), maxAttempts)
	assert.Empty(t, sink.linesOf(DataTypeRecovery))
	assert.Len(t, sink.linesOf(DataTypeCycle), 4)
	assert.Len(t, sink.linesOf(DataTypeSleep), 3, "pulled after the failure")
	assert.Len(t, sink.linesOf(DataTypeWorkout), 2)
	assert.Len(t, sink.linesOf(DataTypeBody), 1)

	// A decode failure is also confined to its data type.
	f = newFakeWhoop(t)
	f.Page = func(p string, _ url.Values) (string, bool) { return `{"records":`, p == "/v2/cycle" }
	sink = &recordingSink{}
	err = f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch cycle")
	assert.Len(t, sink.linesOf(DataTypeWorkout), 2)
}

// A 429 in the middle of a pull stops it at once (no retry, no sleep) with
// the typed error; what was emitted stays.
func TestFetch_RateLimitedOnPageTwo(t *testing.T) {
	f := newFakeWhoop(t)
	f.Fail = func(path string, n int) *whoopfake.Fault {
		if path == "/v2/cycle" && n == 2 {
			return &whoopfake.Fault{Status: http.StatusTooManyRequests, Header: map[string]string{
				"X-RateLimit-Limit": "100, 100;window=60, 10000;window=86400", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "17",
			}}
		}
		return nil
	}
	sink := &recordingSink{}
	err := f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink)
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, 17*time.Second, rl.RetryAfter, "X-RateLimit-Reset counts seconds")
	assert.Len(t, sink.emitsOf(DataTypeCycle), 1)
	assert.Empty(t, f.slept)
	assert.Empty(t, f.Calls("/v2/recovery"))
}

// A daily limit answer (reset in hours) defers the job by that much, capped.
func TestFetch_RateLimitedForTheDay(t *testing.T) {
	f := newFakeWhoop(t)
	f.RateLimit = func(string) bool { return true }
	f.RateLimitReset = "43200"
	_, err := pullWorkouts(t, f)
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok)
	assert.Equal(t, 12*time.Hour, rl.RetryAfter)
}

// A pull whose context ends stops quickly and says so.
func TestFetch_ContextCancelled(t *testing.T) {
	f := newFakeWhoop(t)
	ctx, cancel := context.WithCancel(context.Background())
	sink := &cancellingSink{cancel: cancel}
	err := f.connector().Fetch(ctx, testConn(), fetchFrom, fetchTo, sink)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, sink.n, "nothing emitted after the cancellation")
	assert.Len(t, f.Calls("/v2/cycle"), 1)

	// Cancelled while waiting for its pacing slot.
	c := New(Options{ClientID: "a", ClientSecret: "b", APIBase: f.URL(), HTTPClient: f.Client(), PageDelay: time.Hour})
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Fetch(ctx, testConn(), fetchFrom, fetchTo, &recordingSink{}) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Fetch did not return after cancellation")
	}

	// Cancelled during a retry wait.
	f2 := newFakeWhoop(t)
	f2.DataHTTP = http.StatusServiceUnavailable
	c = New(Options{ClientID: "a", ClientSecret: "b", APIBase: f2.URL(), HTTPClient: f2.Client(), PageDelay: -1, RetryBase: time.Hour})
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = c.Fetch(ctx, testConn(), fetchFrom, fetchTo, &recordingSink{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)
}

type cancellingSink struct {
	cancel func()
	n      int
}

func (s *cancellingSink) Emit(context.Context, string, time.Time, [][]byte) error {
	s.n++
	s.cancel()
	return nil
}

// The backfill pulls 7 day windows one after the other. Records spanning a
// boundary come back in both windows (WHOOP returns what intersects the
// window), and map to the same records: nothing is lost, nothing doubles.
func TestFetch_BackfillChunksCoverEverythingOnce(t *testing.T) {
	f := newFakeWhoop(t)
	c := f.connector()
	bounds := []time.Time{
		time.Date(2026, 2, 27, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), // inside cycle 93001 and the night of 03-01
		time.Date(2026, 3, 2, 3, 0, 0, 0, time.UTC),  // inside cycle 93002 and the night of 03-02
		time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC),
	}
	sink := &recordingSink{}
	for i := 0; i+1 < len(bounds); i++ {
		require.NoError(t, c.Fetch(context.Background(), testConn(), bounds[i], bounds[i+1], sink))
	}
	whole := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), bounds[0], bounds[len(bounds)-1], whole))

	for _, dt := range []string{DataTypeCycle, DataTypeRecovery, DataTypeSleep, DataTypeWorkout} {
		chunked := mapEmitted(t, c, sink, dt)
		once := mapEmitted(t, c, whole, dt)
		assert.ElementsMatch(t, unique(hashes(once)), unique(hashes(chunked)), dt)
		assert.GreaterOrEqual(t, len(hashes(chunked)), len(hashes(once)), dt)
	}
}

// A recovery whose cycle lies before the window has it read by id; a cycle
// that cannot be read for a non-fatal reason leaves the recovery without it,
// a rate limit stops the pull.
func TestFetch_RecoveryCycleOutsideWindow(t *testing.T) {
	rec := `{"records":[{"cycle_id":92999,"sleep_id":"aaaaaaaa-0000-4000-8000-000000000000","user_id":10001,` +
		`"created_at":"2026-03-01T06:05:00.000Z","updated_at":"2026-03-01T06:06:00.000Z","score_state":"SCORED",` +
		`"score":{"user_calibrating":false,"recovery_score":70,"resting_heart_rate":50,"hrv_rmssd_milli":60}}]}`
	from := time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)

	f := newFakeWhoop(t)
	f.Records = map[string]string{"/v2/recovery": rec}
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), from, fetchTo, sink))
	assert.Len(t, f.Calls("/v2/cycle/92999"), 1, "read by id: the cycle collection of the window does not hold it")
	var r Recovery
	require.NoError(t, json.Unmarshal(sink.linesOf(DataTypeRecovery)[0], &r))
	require.NotNil(t, r.Cycle)
	assert.Equal(t, "2026-02-28T00:00:00.000Z", r.Cycle.Start)

	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusForbidden} {
		f := newFakeWhoop(t)
		f.Records = map[string]string{"/v2/recovery": rec}
		f.Fail = func(path string, _ int) *whoopfake.Fault {
			if path == "/v2/cycle/92999" {
				return &whoopfake.Fault{Status: status}
			}
			return nil
		}
		sink := &recordingSink{}
		require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), from, fetchTo, []string{DataTypeRecovery}, sink), status)
		lines := sink.linesOf(DataTypeRecovery)
		require.Len(t, lines, 1, status)
		assert.NotContains(t, string(lines[0]), `"cycle"`, status)
	}

	f = newFakeWhoop(t)
	f.Records = map[string]string{"/v2/recovery": rec}
	f.RateLimit = func(p string) bool { return p == "/v2/cycle/92999" }
	err := f.connector().FetchTypes(context.Background(), testConn(), from, fetchTo, []string{DataTypeRecovery}, &recordingSink{})
	_, limited := connector.AsRateLimit(err)
	assert.True(t, limited, "%v", err)
}

// A data type whose scope was not granted is not asked for; a 403 skips the
// data type without failing the pull.
func TestFetch_ScopesNotGranted(t *testing.T) {
	f := newFakeWhoop(t)
	conn := testConn()
	conn.Tokens.Scopes = []string{"read:cycles", "read:sleep", "offline"}
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), conn, fetchFrom, fetchTo, sink))
	assert.Empty(t, f.Calls("/v2/recovery"))
	assert.Empty(t, f.Calls("/v2/activity/workout"))
	assert.Empty(t, f.Calls("/v2/user/measurement/body"))
	assert.Len(t, sink.linesOf(DataTypeSleep), 3)

	// Scopes that name no read scope say nothing: everything is pulled.
	conn.Tokens.Scopes = []string{"offline"}
	f = newFakeWhoop(t)
	require.NoError(t, f.connector().Fetch(context.Background(), conn, fetchFrom, fetchTo, &recordingSink{}))
	assert.NotEmpty(t, f.Calls("/v2/activity/workout"))

	f = newFakeWhoop(t)
	f.Fail = func(path string, _ int) *whoopfake.Fault {
		if path == "/v2/activity/workout" {
			return &whoopfake.Fault{Status: http.StatusForbidden}
		}
		return nil
	}
	sink = &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))
	assert.Empty(t, sink.linesOf(DataTypeWorkout))
	assert.Len(t, sink.linesOf(DataTypeSleep), 3)
}

// The requests of every pull of the process share one schedule, PageDelay
// apart: four pulls at once (the scheduler's default concurrency) stay under
// WHOOP's 100 requests a minute instead of each spending it.
func TestPacing_SharedAcrossConcurrentPulls(t *testing.T) {
	const delay = 30 * time.Millisecond
	f := newFakeWhoop(t)
	c := New(Options{
		ClientID: "a", ClientSecret: "b", APIBase: f.URL(), HTTPClient: f.Client(),
		PageDelay: delay, Now: func() time.Time { return fakeNow },
	})
	// The slots are checked, not the arrival times at the fake server: the
	// pacer spaces sends, and goroutine and network jitter can bring two
	// arrivals closer than the delay without any request leaving early.
	var mu sync.Mutex
	var slots []time.Time
	c.onSlot = func(s time.Time) {
		mu.Lock()
		slots = append(slots, s)
		mu.Unlock()
	}
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, c.FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeWorkout, DataTypeSleep}, &recordingSink{}))
		}()
	}
	wg.Wait()
	require.Len(t, f.Requests(), 12)
	require.Len(t, slots, 12, "one slot per request")
	for i := 1; i < len(slots); i++ {
		assert.GreaterOrEqual(t, slots[i].Sub(slots[i-1]), delay, "slot %d: one shared schedule", i)
	}
	// No request is sent before its slot: the last one arrives at least
	// eleven delays after the pulls started.
	var last time.Time
	for _, r := range f.Requests() {
		if r.At.After(last) {
			last = r.At
		}
	}
	assert.GreaterOrEqual(t, last.Sub(start), 11*delay)
	assert.Equal(t, 600*time.Millisecond, New(Options{}).pageDelay, "100 requests a minute")
}

// X-RateLimit-Remaining 0 makes the next request wait for the window's reset
// (when it is close) instead of drawing a 429.
func TestPacing_WaitsForRateLimitReset(t *testing.T) {
	c := New(Options{PageDelay: time.Millisecond})
	before := time.Now()
	c.observeLimit(http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"3"}})
	assert.WithinDuration(t, before.Add(3*time.Second), c.nextSlot, time.Second)

	for _, h := range []http.Header{
		{"X-Ratelimit-Remaining": {"5"}, "X-Ratelimit-Reset": {"3"}},
		{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"86000"}}, // the daily window: left to the 429
		{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"soon"}},
		{},
	} {
		c := New(Options{PageDelay: time.Millisecond})
		c.observeLimit(h)
		assert.True(t, c.nextSlot.IsZero(), "%v", h)
	}
}

// hashes lists the identity of every record of r: its content hash, and for
// a sample its start too (the dedup key of the hypertables).
func hashes(r model.MapResult) []string {
	var out []string
	for _, s := range r.QuantitySamples {
		out = append(out, s.ContentHash+"@"+s.StartDate.String())
	}
	for _, s := range r.SleepSessions {
		out = append(out, s.ContentHash)
	}
	for _, w := range r.Workouts {
		out = append(out, w.ContentHash)
	}
	for _, d := range r.DailySummaries {
		out = append(out, d.ContentHash+"@"+d.SummaryDate.String())
	}
	return out
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
