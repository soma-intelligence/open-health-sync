// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/intervalsicu/icufake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

var (
	winFrom = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	winTo   = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
)

func day(s string) time.Time {
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestFetch(t *testing.T) {
	c, f, _ := newTestConnector(t, 0)
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), winFrom, winTo, sink))

	// i1004 started before the window; the Strava stub i1003 has no UTC
	// start and is kept (raw first) under its local day.
	assert.Equal(t, []string{"i1001", "i1002", "i1003"}, sink.ids(t, DataTypeActivity))
	// Wellness of the UTC days the window touches: 1 and 2 March.
	assert.Equal(t, []string{"2026-03-01", "2026-03-02"}, sink.ids(t, DataTypeWellness))

	var days []string
	for _, call := range sink.calls {
		days = append(days, call.DataType+" "+call.Day.Format(dateLayout))
		for _, l := range call.Lines {
			assert.NotContains(t, string(l), "\n", "one compact line per record")
		}
	}
	assert.Equal(t, []string{
		"activity 2026-03-01", "activity 2026-03-02",
		"wellness 2026-03-01", "wellness 2026-03-02",
	}, days)

	acts := f.Calls("/api/v1/athlete/0/activities")
	require.Len(t, acts, 1)
	assert.Equal(t, "Bearer "+icufake.AccessToken, acts[0].Auth)
	assert.Equal(t, "2026-02-28", acts[0].Query.Get("oldest"), "one day of margin for the local date")
	assert.Equal(t, "2026-03-04", acts[0].Query.Get("newest"))
	well := f.Calls("/api/v1/athlete/0/wellness")
	require.Len(t, well, 1)
	assert.Equal(t, "2026-03-01", well[0].Query.Get("oldest"))
	assert.Equal(t, "2026-03-02", well[0].Query.Get("newest"))
}

func TestFetchChunksAndDeduplicates(t *testing.T) {
	c, f, _ := newTestConnector(t, 1)
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), winFrom, winTo, sink))
	assert.Len(t, f.Calls("/api/v1/athlete/0/activities"), 2)
	assert.Len(t, f.Calls("/api/v1/athlete/0/wellness"), 2)
	// Overlapping chunks list some activities twice; each is emitted once.
	assert.Equal(t, []string{"i1001", "i1002", "i1003"}, sink.ids(t, DataTypeActivity))
	assert.Equal(t, []string{"2026-03-01", "2026-03-02"}, sink.ids(t, DataTypeWellness))
}

func TestFetchEmptyWindow(t *testing.T) {
	c, f, _ := newTestConnector(t, 0)
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), winTo, winFrom, sink))
	assert.Empty(t, sink.calls)
	assert.Empty(t, f.Requests())
}

func TestFetchUnauthorized(t *testing.T) {
	c, _, _ := newTestConnector(t, 0)
	conn := testConn()
	conn.Tokens.Access = "revoked"
	err := c.Fetch(context.Background(), conn, winFrom, winTo, &recordingSink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)

	conn.Tokens.Access = ""
	err = c.Fetch(context.Background(), conn, winFrom, winTo, &recordingSink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestFetchForbiddenIsNotReauth(t *testing.T) {
	c, f, _ := newTestConnector(t, 0)
	f.DataHTTP = http.StatusForbidden
	err := c.Fetch(context.Background(), testConn(), winFrom, winTo, &recordingSink{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
	assert.ErrorContains(t, err, "HTTP 403")
}

func TestFetchRateLimited(t *testing.T) {
	c, f, _ := newTestConnector(t, 0)
	f.RateLimit = func(p string) bool { return p == "/api/v1/athlete/0/wellness" }
	sink := &recordingSink{}
	err := c.Fetch(context.Background(), testConn(), winFrom, winTo, sink)
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, 120*time.Second, rl.RetryAfter)
}

func TestFetchRetriesServerErrors(t *testing.T) {
	c, f, slept := newTestConnector(t, 0)
	f.DataFailures = 2
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), winFrom, winTo, sink))
	assert.Len(t, *slept, 2)
	assert.Equal(t, []string{"i1001", "i1002", "i1003"}, sink.ids(t, DataTypeActivity))

	c, f, _ = newTestConnector(t, 0)
	f.DataFailures = 10
	err := c.Fetch(context.Background(), testConn(), winFrom, winTo, &recordingSink{})
	assert.ErrorContains(t, err, "HTTP 503")
}

func TestFetchNotified(t *testing.T) {
	c, f, _ := newTestConnector(t, 0)
	sink := &recordingSink{}
	require.NoError(t, c.FetchNotified(context.Background(), testConn(), winFrom, winTo, DataTypeWellness, sink))
	assert.Empty(t, f.Calls("/api/v1/athlete/0/activities"))
	assert.Equal(t, []string{"2026-03-01", "2026-03-02"}, sink.ids(t, DataTypeWellness))

	sink = &recordingSink{}
	require.NoError(t, c.FetchNotified(context.Background(), testConn(), winFrom, winTo, DataTypeActivity, sink))
	assert.Len(t, f.Calls("/api/v1/athlete/0/wellness"), 1)
	assert.Empty(t, sink.ids(t, DataTypeWellness))
	assert.Len(t, sink.ids(t, DataTypeActivity), 3)

	sink = &recordingSink{}
	require.NoError(t, c.FetchNotified(context.Background(), testConn(), winFrom, winTo, "other", sink))
	assert.Len(t, sink.ids(t, DataTypeWellness), 2)
}

func TestFetchBadAnswer(t *testing.T) {
	c, f, _ := newTestConnector(t, 0)
	f.Activities = []json.RawMessage{json.RawMessage(`{"id":"i9","start_date_local":"2026-03-01T08:00:00","start_date":5}`)}
	err := c.Fetch(context.Background(), testConn(), winFrom, winTo, &recordingSink{})
	assert.Error(t, err)
}

// A null entry of a list used to be stored as the raw line "null", which is
// not a record and failed Map on every replay.
func TestFetchNullEntryIsSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wellness") {
			_, _ = w.Write([]byte(`[null, {"id":"2026-03-01","weight":70}]`))
			return
		}
		_, _ = w.Write([]byte(`[null, {"id":"i1","start_date":"2026-03-01T07:00:00Z"}]`))
	}))
	defer srv.Close()
	c := New(Options{ClientID: "a", ClientSecret: "b", APIBase: srv.URL, HTTPClient: srv.Client(), Now: func() time.Time { return fakeNow }})
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), winFrom, winTo, sink))
	for _, call := range sink.calls {
		for _, l := range call.Lines {
			assert.NotEqual(t, "null", string(l), call.DataType)
		}
	}
	assert.Equal(t, []string{"i1"}, sink.ids(t, DataTypeActivity))
	assert.Equal(t, []string{"2026-03-01"}, sink.ids(t, DataTypeWellness))
}

// TestFetchThenMap runs the fetched lines through Map, as the ETL would.
func TestFetchThenMap(t *testing.T) {
	c, _, _ := newTestConnector(t, 0)
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), testConn(), winFrom, winTo, sink))
	workouts, samples := 0, 0
	for _, call := range sink.calls {
		for _, l := range call.Lines {
			res, err := c.Map(record(t, call.DataType, l))
			require.NoError(t, err, "%s", l)
			workouts += len(res.Workouts)
			samples += len(res.QuantitySamples)
		}
	}
	assert.Equal(t, 2, workouts, "the Strava stub gives nothing")
	assert.Equal(t, 12+1, samples)
	assert.Equal(t, day("2026-03-01"), utcDay(winFrom))
}
