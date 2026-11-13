// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/wahoo/wahoofake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// workoutsOn returns n workouts starting at noon UTC on n consecutive days
// from first, ids 1000+i, built from the list fixture.
func workoutsOn(first time.Time, n int) []json.RawMessage {
	base := wahoofake.Workouts()[0]
	out := make([]json.RawMessage, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, wahoofake.Edit(base, map[string]any{
			"id":     1000 + i,
			"starts": first.AddDate(0, 0, i).Add(12 * time.Hour).Format("2006-01-02T15:04:05.000Z"),
		}))
	}
	return out
}

func TestFetch_UserAndWorkoutsWithSummaries(t *testing.T) {
	f := wahoofake.New(t)
	march1 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	f.Workouts = workoutsOn(march1, 2)
	f.Summaries = map[string]json.RawMessage{"1000": wahoofake.File("workout_summary.json")}
	sink := &recordingSink{}
	require.NoError(t, testConnector(f, 0).Fetch(context.Background(), conn(), march1, march1.AddDate(0, 0, 2), sink))

	users := sink.of(DataTypeUser)
	require.Len(t, users, 1)
	var u map[string]any
	require.NoError(t, json.Unmarshal(users[0], &u))
	assert.Equal(t, map[string]any{"id": 60462.0, "height": "2.0", "weight": "80.0", "updated_at": "2018-10-24T20:46:40.000Z"}, u,
		"name, email and birth date are not stored")

	lines := sink.of(DataTypeWorkout)
	require.Len(t, lines, 2)
	var days []time.Time
	for _, c := range sink.calls {
		if c.DataType == DataTypeWorkout {
			days = append(days, c.Day)
		}
	}
	assert.Equal(t, []time.Time{march1, march1.AddDate(0, 0, 1)}, days, "one object per UTC day, in day order")
	var w struct {
		ID      int             `json:"id"`
		Summary json.RawMessage `json:"workout_summary"`
	}
	require.NoError(t, json.Unmarshal(lines[0], &w))
	assert.Equal(t, 1000, w.ID)
	assert.Contains(t, string(w.Summary), `"distance_accum":"24909.71"`, "the summary is fetched and embedded")
	require.NoError(t, json.Unmarshal(lines[1], &w))
	assert.Equal(t, "null", string(w.Summary), "no summary yet (404): the workout is kept without one")

	assert.Len(t, f.Calls("/v1/workouts"), 1)
	assert.Len(t, f.Calls("/v1/workouts/1000/workout_summary"), 1)
	assert.Len(t, f.Calls("/v1/workouts/1001/workout_summary"), 1)
	assert.Equal(t, "Bearer fake-access-token", f.Calls("/v1/workouts")[0].Auth)
}

func TestFetch_StopsAtTheFirstPageReachingPastFrom(t *testing.T) {
	f := wahoofake.New(t)
	feb1 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	f.Workouts = workoutsOn(feb1, 30) // Feb 1 to Mar 2, newest first
	sums := map[string]json.RawMessage{}
	for i := 0; i < 30; i++ {
		sums[fmt.Sprint(1000+i)] = wahoofake.File("workout_summary.json")
	}
	f.Summaries = sums
	sink := &recordingSink{}
	from := time.Date(2026, 2, 24, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, testConnector(f, 3).Fetch(context.Background(), conn(), from, to, sink))
	assert.Len(t, sink.of(DataTypeWorkout), 6, "Feb 24 to Mar 1; Mar 2 is past to")
	// Page 1: Mar 2, Mar 1, Feb 28; page 2: Feb 27 to 25; page 3 reaches Feb 23.
	assert.Len(t, f.Calls("/v1/workouts"), 3)
	assert.Equal(t, "3", f.Calls("/v1/workouts")[2].Query.Get("page"))
	assert.Equal(t, "3", f.Calls("/v1/workouts")[2].Query.Get("per_page"))
}

func TestFetch_Refusals(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusUnauthorized: connector.ErrNeedsReauth,
		http.StatusForbidden:    connector.ErrMissingScope,
	} {
		f := wahoofake.New(t)
		f.DataHTTP = status
		err := testConnector(f, 0).Fetch(context.Background(), conn(), fakeNow.AddDate(0, 0, -1), fakeNow, &recordingSink{})
		assert.ErrorIs(t, err, want, status)
	}
	err := New(Options{}).Fetch(context.Background(), connector.Connection{}, fakeNow, fakeNow, &recordingSink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth, "no access token")
}

func TestFetch_RateLimitAndServerErrors(t *testing.T) {
	f := wahoofake.New(t)
	f.RateLimit = func(p string) bool { return p == "/v1/workouts" }
	f.RateLimitHeaders = http.Header{"X-Ratelimit-Reset": {"300"}}
	err := testConnector(f, 0).Fetch(context.Background(), conn(), fakeNow.AddDate(0, 0, -1), fakeNow, &recordingSink{})
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok, err)
	assert.Equal(t, 5*time.Minute, rl.RetryAfter)

	f = wahoofake.New(t)
	f.DataHTTP = http.StatusServiceUnavailable
	err = testConnector(f, 0).Fetch(context.Background(), conn(), fakeNow.AddDate(0, 0, -1), fakeNow, &recordingSink{})
	require.Error(t, err)
	assert.Len(t, f.Calls("/v1/user"), maxAttempts, "a 503 is retried, then reported")
}
