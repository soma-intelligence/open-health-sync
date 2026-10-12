// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/suunto/suuntofake"
)

// fakeSuunto wraps the httptest Suunto of suuntofake.
type fakeSuunto struct {
	*suuntofake.Server
	// slept records the waits of transport retries (no real sleep).
	mu    sync.Mutex
	slept []time.Duration
}

func newFakeSuunto(t *testing.T) *fakeSuunto {
	t.Helper()
	return &fakeSuunto{Server: suuntofake.New(t)}
}

var fakeNow = time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)

const testNotificationSecret = "test-notification-secret"

// connector returns a Suunto connector pointed at the fake, with the fake's
// keys, no pacing, a fixed clock and a recording sleeper.
func (f *fakeSuunto) connector(perPage int) *Connector {
	return New(Options{
		ClientID: suuntofake.ClientID, ClientSecret: suuntofake.ClientSecret,
		SubscriptionKey:    suuntofake.SubscriptionKey,
		NotificationSecret: testNotificationSecret,
		OAuthBase:          f.URL(), APIBase: f.URL(),
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

// lines returns every line emitted for dataType.
func (s *recordingSink) lines(dataType string) [][]byte {
	var out [][]byte
	for _, c := range s.calls {
		if c.DataType == dataType {
			out = append(out, c.Lines...)
		}
	}
	return out
}
