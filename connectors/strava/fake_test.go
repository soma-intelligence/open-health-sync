// SPDX-License-Identifier: AGPL-3.0-only

package strava

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/strava/stravafake"
)

// fakeStrava wraps the httptest Strava of stravafake.
type fakeStrava struct {
	*stravafake.Server
	// slept records the waits of transport retries (no real sleep).
	mu    sync.Mutex
	slept []time.Duration
}

func newFakeStrava(t *testing.T) *fakeStrava {
	t.Helper()
	return &fakeStrava{Server: stravafake.New(t)}
}

var fakeNow = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)

// connector returns a Strava connector pointed at the fake, with test keys,
// no pacing, a fixed clock and a recording sleeper.
func (f *fakeStrava) connector(perPage int) *Connector {
	return New(Options{
		ClientID: "test-client-id", ClientSecret: "test-client-secret",
		VerifyToken: "test-verify-token",
		OAuthBase:   f.URL(), APIBase: f.APIURL(),
		HTTPClient: f.Client(),
		PageDelay:  -1,
		PerPage:    perPage,
		Now:        func() time.Time { return fakeNow },
		Sleep: func(_ context.Context, d time.Duration) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.slept = append(f.slept, d)
			return nil
		},
	})
}

// recordingSink is a connector.RawSink that keeps every Emit call.
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

func (s *recordingSink) lines() [][]byte {
	var out [][]byte
	for _, c := range s.calls {
		out = append(out, c.Lines...)
	}
	return out
}
