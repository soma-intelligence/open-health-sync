// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

import (
	"context"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/wahoo/wahoofake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

var fakeNow = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)

// testConnector returns a connector pointed at f, with test keys, a fixed
// clock and a sleeper that does not sleep.
func testConnector(f *wahoofake.Server, perPage int) *Connector {
	return New(Options{
		ClientID: "test-client-id", ClientSecret: "test-client-secret",
		WebhookToken: "fake-webhook-token",
		Base:         f.URL(), HTTPClient: f.Client(),
		PerPage: perPage,
		Now:     func() time.Time { return fakeNow },
		Sleep:   func(context.Context, time.Duration) error { return nil },
	})
}

func conn() connector.Connection {
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: "fake-access-token", ProviderUserID: "60462"}}
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

func (s *recordingSink) of(dataType string) [][]byte {
	var out [][]byte
	for _, c := range s.calls {
		if c.DataType == dataType {
			out = append(out, c.Lines...)
		}
	}
	return out
}
