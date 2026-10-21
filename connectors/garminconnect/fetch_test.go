// SPDX-License-Identifier: AGPL-3.0-only

package garminconnect

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/garminconnect/garminconnectfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// recordingSink keeps every Emit call.
type recordingSink struct {
	mu    sync.Mutex
	calls []emitCall
}

type emitCall struct {
	DataType string
	Day      time.Time
	Lines    [][]byte
}

func (s *recordingSink) Emit(_ context.Context, dataType string, day time.Time, lines [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([][]byte, len(lines))
	for i, l := range lines {
		cp[i] = append([]byte(nil), l...)
	}
	s.calls = append(s.calls, emitCall{DataType: dataType, Day: day, Lines: cp})
	return nil
}

func (s *recordingSink) byType(dt string) []emitCall {
	var out []emitCall
	for _, c := range s.calls {
		if c.DataType == dt {
			out = append(out, c)
		}
	}
	return out
}

func TestFetch(t *testing.T) {
	f := garminconnectfake.New(t)
	c := newConnector(f)
	conn := connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: f.Bearer(), ProviderUserID: garminconnectfake.Subject}}

	var sink recordingSink
	from := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	require.NoError(t, c.Fetch(context.Background(), conn, from, to, &sink))

	// Dailies for both days in the window.
	assert.Len(t, sink.byType(DataTypeDailies), 2)
	// Sleep and HRV only on the fixture day.
	require.Len(t, sink.byType(DataTypeSleeps), 1)
	require.Len(t, sink.byType(DataTypeHRV), 1)
	assert.Equal(t, garminconnectfake.SampleDate, sink.byType(DataTypeSleeps)[0].Day.Format(time.DateOnly))
	// One activity, filed under its start day.
	require.Len(t, sink.byType(DataTypeActivities), 1)
	assert.Len(t, sink.byType(DataTypeActivities)[0].Lines, 1)
}

func TestFetchNeedsReauthOn401(t *testing.T) {
	f := garminconnectfake.New(t)
	f.APIStatus = http.StatusUnauthorized
	c := newConnector(f)
	conn := connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: f.Bearer()}}
	err := c.Fetch(context.Background(), conn, fakeNow.Add(-48*time.Hour), fakeNow, &recordingSink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestFetchBlockedOn403(t *testing.T) {
	f := garminconnectfake.New(t)
	f.APIStatus = http.StatusForbidden
	c := newConnector(f)
	conn := connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: f.Bearer()}}
	err := c.Fetch(context.Background(), conn, fakeNow.Add(-48*time.Hour), fakeNow, &recordingSink{})
	assert.ErrorIs(t, err, ErrBlocked)
}

func TestFetchNoToken(t *testing.T) {
	c := newConnector(garminconnectfake.New(t))
	err := c.Fetch(context.Background(), connector.Connection{Provider: Slug}, fakeNow.Add(-24*time.Hour), fakeNow, &recordingSink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
}
