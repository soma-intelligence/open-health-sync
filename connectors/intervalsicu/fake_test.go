// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/intervalsicu/icufake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

var fakeNow = time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)

const testWebhookSecret = "test-webhook-secret"

// newTestConnector returns a connector pointed at a new fake, with test keys,
// a fixed clock and a sleeper that records instead of sleeping.
func newTestConnector(t *testing.T, chunkDays int) (*Connector, *icufake.Server, *[]time.Duration) {
	t.Helper()
	f := icufake.New(t)
	var mu sync.Mutex
	slept := &[]time.Duration{}
	c := New(Options{
		ClientID: "test-client-id", ClientSecret: "test-client-secret",
		WebhookSecret: testWebhookSecret,
		OAuthBase:     f.URL(), APIBase: f.APIURL(),
		HTTPClient: f.Client(),
		ChunkDays:  chunkDays,
		Now:        func() time.Time { return fakeNow },
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()
			*slept = append(*slept, d)
			return nil
		},
	})
	return c, f, slept
}

func testConn() connector.Connection {
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: icufake.AccessToken, ProviderUserID: "i900001"}}
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

// ids returns the "id" of every emitted line of dataType, in emit order.
func (s *recordingSink) ids(t *testing.T, dataType string) []string {
	t.Helper()
	var out []string
	for _, c := range s.calls {
		if c.DataType != dataType {
			continue
		}
		for _, l := range c.Lines {
			var head struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(l, &head); err != nil {
				t.Fatalf("line is not JSON: %s", l)
			}
			out = append(out, head.ID)
		}
	}
	return out
}

// record turns one fixture object into the RawRecord the ETL would hand Map.
func record(t *testing.T, dataType string, raw []byte) connector.RawRecord {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return connector.RawRecord{UserID: "user-1", Provider: Slug, DataType: dataType, Fields: fields}
}

// fixture returns the object of id in a fixture list.
func fixture(t *testing.T, file, id string) []byte {
	t.Helper()
	for _, raw := range icufake.List(file) {
		var head struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &head) == nil && head.ID == id {
			return raw
		}
	}
	t.Fatalf("no %s in %s", id, file)
	return nil
}
