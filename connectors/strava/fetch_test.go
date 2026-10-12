// SPDX-License-Identifier: AGPL-3.0-only

package strava

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func conn() connector.Connection {
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: "fake-access-token", ProviderUserID: "1000001"}}
}

var (
	march1 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	march3 = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
)

func TestFetch_EmitsOneObjectPerDay(t *testing.T) {
	f := newFakeStrava(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector(0).Fetch(context.Background(), conn(), march1, march3, sink))

	require.Len(t, sink.calls, 2)
	assert.Equal(t, DataTypeActivity, sink.calls[0].DataType)
	assert.True(t, sink.calls[0].Day.Equal(march1))
	assert.True(t, sink.calls[1].Day.Equal(march1.AddDate(0, 0, 1)))
	assert.Len(t, sink.calls[0].Lines, 2)
	assert.Len(t, sink.calls[1].Lines, 2)
	for _, l := range sink.lines() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(l, &m), "each line is one activity object")
		assert.NotContains(t, string(l), "\n")
	}

	calls := f.Calls("/api/v3/athlete/activities")
	require.Len(t, calls, 1, "four activities fit one page of 200")
	q := calls[0].Query
	assert.Equal(t, strconv.FormatInt(march1.Unix()-1, 10), q.Get("after"))
	assert.Equal(t, strconv.FormatInt(march3.Unix(), 10), q.Get("before"))
	assert.Equal(t, "1", q.Get("page"))
	assert.Equal(t, "200", q.Get("per_page"))
	assert.Equal(t, "Bearer fake-access-token", calls[0].Auth)
}

func TestFetch_Paginates(t *testing.T) {
	f := newFakeStrava(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector(3).Fetch(context.Background(), conn(), march1, march3, sink))
	assert.Len(t, sink.lines(), 4)
	calls := f.Calls("/api/v3/athlete/activities")
	require.Len(t, calls, 2, "a full page of 3, then a short page of 1")
	assert.Equal(t, "2", calls[1].Query.Get("page"))
	assert.Equal(t, "3", calls[1].Query.Get("per_page"))

	// Exactly a full last page needs one more, empty, request.
	f = newFakeStrava(t)
	sink = &recordingSink{}
	require.NoError(t, f.connector(2).Fetch(context.Background(), conn(), march1, march3, sink))
	assert.Len(t, sink.lines(), 4)
	assert.Len(t, f.Calls("/api/v3/athlete/activities"), 3)
}

func TestFetch_WindowIsHalfOpen(t *testing.T) {
	f := newFakeStrava(t)
	sink := &recordingSink{}
	// The first activity starts exactly at 07:30; the window ends at the
	// second one's start (18:00), which is excluded.
	from := time.Date(2026, 3, 1, 7, 30, 0, 0, time.UTC)
	to := time.Date(2026, 3, 1, 18, 0, 0, 0, time.UTC)
	require.NoError(t, f.connector(0).Fetch(context.Background(), conn(), from, to, sink))
	lines := sink.lines()
	require.Len(t, lines, 1)
	assert.Contains(t, string(lines[0]), `"id":90000000001`)
}

func TestFetch_EmptyWindowEmitsNothing(t *testing.T) {
	f := newFakeStrava(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector(0).Fetch(context.Background(), conn(), march3, march1, sink))
	assert.Empty(t, sink.calls)
	assert.Empty(t, f.Requests())
}

func TestFetch_RejectedAccessTokenNeedsReauth(t *testing.T) {
	f := newFakeStrava(t)
	f.DataHTTP = http.StatusUnauthorized
	err := f.connector(0).Fetch(context.Background(), conn(), march1, march3, &recordingSink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
	assert.NotContains(t, err.Error(), "fake-access-token")

	err = f.connector(0).Fetch(context.Background(), connector.Connection{}, march1, march3, &recordingSink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth, "no access token")
}

func TestFetch_OtherStatusIsAnError(t *testing.T) {
	f := newFakeStrava(t)
	f.DataHTTP = http.StatusInternalServerError
	err := f.connector(0).Fetch(context.Background(), conn(), march1, march3, &recordingSink{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
	_, limited := connector.AsRateLimit(err)
	assert.False(t, limited)
}

func TestFetch_RateLimitPropagates(t *testing.T) {
	f := newFakeStrava(t)
	f.RateLimit = func(string) bool { return true }
	f.RateLimitHeaders = http.Header{"X-Ratelimit-Limit": {"200,2000"}, "X-Ratelimit-Usage": {"201,900"}}
	sink := &recordingSink{}
	err := f.connector(0).Fetch(context.Background(), conn(), march1, march3, sink)
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok)
	assert.Equal(t, 15*time.Minute, rl.RetryAfter, "fakeNow is on a quarter hour: wait for the next one")
	assert.Empty(t, sink.calls)
}

func TestFetchThenMap(t *testing.T) {
	f := newFakeStrava(t)
	sink := &recordingSink{}
	c := f.connector(0)
	require.NoError(t, c.Fetch(context.Background(), conn(), march1, march3, sink))
	var workouts int
	for _, l := range sink.lines() {
		var fields map[string]any
		require.NoError(t, json.Unmarshal(l, &fields))
		res, err := c.Map(connector.RawRecord{UserID: "u1", Provider: Slug, DataType: DataTypeActivity, Fields: fields})
		require.NoError(t, err)
		workouts += len(res.Workouts)
	}
	assert.Equal(t, 4, workouts)
}

func TestEmitByDay_SinkErrorStops(t *testing.T) {
	err := emitByDay(context.Background(), failingSink{}, DataTypeActivity, []dayLine{{day: march1, line: []byte(`{}`)}})
	assert.ErrorContains(t, err, "2026-03-01")
}

type failingSink struct{}

func (failingSink) Emit(context.Context, string, time.Time, [][]byte) error {
	return errors.New("disk full")
}
