// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/whoop/whoopfake"
)

// fakeWhoop wraps the httptest WHOOP of whoopfake.
type fakeWhoop struct {
	*whoopfake.Server
	// slept records the waits of transport retries (no real sleep).
	mu    sync.Mutex
	slept []time.Duration
}

func newFakeWhoop(t *testing.T) *fakeWhoop {
	t.Helper()
	return &fakeWhoop{Server: whoopfake.New(t)}
}

var fakeNow = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)

// connector returns a WHOOP connector pointed at the fake, with test keys, no
// pacing, a fixed clock and a recording sleeper.
func (f *fakeWhoop) connector() *Connector {
	return New(Options{
		ClientID: "test-client-id", ClientSecret: "test-client-secret",
		AuthBase: f.URL(), APIBase: f.URL(),
		HTTPClient: f.Client(),
		PageDelay:  -1,
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

func (s *recordingSink) linesOf(dataType string) [][]byte {
	var out [][]byte
	for _, c := range s.calls {
		if c.DataType == dataType {
			out = append(out, c.Lines...)
		}
	}
	return out
}

func (s *recordingSink) emitsOf(dataType string) []emitCall {
	var out []emitCall
	for _, c := range s.calls {
		if c.DataType == dataType {
			out = append(out, c)
		}
	}
	return out
}
