// SPDX-License-Identifier: AGPL-3.0-only

package eightsleep

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/eightsleep/eightsleepfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestFetch(t *testing.T) {
	f := newFake(t)
	f.AccessToken = "fake-access-token-1"
	sink := &recordingSink{}
	from := fakeNow.Add(-30 * 24 * time.Hour)
	require.NoError(t, newConnector(f).Fetch(context.Background(), testConn(), from, fakeNow, sink))

	require.Len(t, sink.calls, 2)
	assert.Equal(t, DataTypeTrends, sink.calls[0].DataType)
	assert.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), sink.calls[0].Day)
	assert.Equal(t, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), sink.calls[1].Day)
	require.Len(t, sink.calls[0].Lines, 1)
	assert.NotContains(t, string(sink.calls[0].Lines[0]), "\n")

	// 30 days in chunks of 14: three requests, contiguous, ending on the
	// last day before now.
	reqs := f.Requests()
	require.Len(t, reqs, 3)
	assert.Equal(t, "/v1/users/"+eightsleepfake.UserID+"/trends", reqs[0].Path)
	assert.Equal(t, "Bearer fake-access-token-1", reqs[0].Auth)
	q := reqs[0].Query
	assert.Equal(t, "UTC", q.Get("tz"))
	assert.Equal(t, "2026-02-01", q.Get("from"))
	assert.Equal(t, "2026-02-14", q.Get("to"))
	assert.Equal(t, "true", q.Get("include-all-sessions"))
	assert.Equal(t, "2026-02-15", reqs[1].Query.Get("from"))
	assert.Equal(t, "2026-03-01", reqs[2].Query.Get("from"))
	assert.Equal(t, "2026-03-02", reqs[2].Query.Get("to"))
}

func TestFetchClampsToNow(t *testing.T) {
	f := newFake(t)
	sink := &recordingSink{}
	require.NoError(t, newConnector(f).Fetch(context.Background(), testConn(), fakeNow.Add(time.Hour), fakeNow.Add(48*time.Hour), sink))
	assert.Empty(t, f.Requests())
	assert.Empty(t, sink.calls)
}

func TestFetchRejectedToken(t *testing.T) {
	f := newFake(t)
	f.AccessToken = "another-token"
	err := newConnector(f).Fetch(context.Background(), testConn(), fakeNow.Add(-24*time.Hour), fakeNow, &recordingSink{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestFetchWithoutUser(t *testing.T) {
	conn := testConn()
	conn.Tokens.ProviderUserID = ""
	err := newConnector(newFake(t)).Fetch(context.Background(), conn, fakeNow.Add(-24*time.Hour), fakeNow, &recordingSink{})
	require.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestFetchRateLimited(t *testing.T) {
	f := newFake(t)
	f.DataHTTP, f.RetryAfter = http.StatusTooManyRequests, "120"
	err := newConnector(f).Fetch(context.Background(), testConn(), fakeNow.Add(-24*time.Hour), fakeNow, &recordingSink{})
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok)
	assert.Equal(t, 2*time.Minute, rl.RetryAfter)
}

func TestFetchServerError(t *testing.T) {
	f := newFake(t)
	f.DataHTTP = http.StatusInternalServerError
	err := newConnector(f).Fetch(context.Background(), testConn(), fakeNow.Add(-24*time.Hour), fakeNow, &recordingSink{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestFetchCapsWindow(t *testing.T) {
	f := newFake(t)
	require.NoError(t, newConnector(f).Fetch(context.Background(), testConn(), fakeNow.AddDate(-1, 0, 0), fakeNow, &recordingSink{}))
	reqs := f.Requests()
	require.NotEmpty(t, reqs)
	assert.Equal(t, fakeNow.Add(-maxPull).Format(time.DateOnly), reqs[0].Query.Get("from"))
}
