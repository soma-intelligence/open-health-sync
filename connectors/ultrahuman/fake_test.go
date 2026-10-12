// SPDX-License-Identifier: AGPL-3.0-only

package ultrahuman

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/ultrahuman/ultrahumanfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const (
	testClientID     = "test-client-id"
	testClientSecret = "test-client-secret"
	testUserID       = "00000000-0000-0000-0000-000000000001"
)

var fakeNow = time.Date(2026, 3, 10, 6, 0, 0, 0, time.UTC)

type fakeUH struct {
	*ultrahumanfake.Server
	mu    sync.Mutex
	slept []time.Duration
}

func newFake(t *testing.T) *fakeUH {
	t.Helper()
	s := ultrahumanfake.New(t)
	s.ClientID, s.ClientSecret = testClientID, testClientSecret
	return &fakeUH{Server: s}
}

// connector returns a connector pointed at the fake, with test keys, a fixed
// clock and a recording sleeper.
func (f *fakeUH) connector() *Connector {
	return New(Options{
		ClientID: testClientID, ClientSecret: testClientSecret,
		AuthBase: f.URL(), APIBase: f.URL(),
		HTTPClient: f.Client(),
		Now:        func() time.Time { return fakeNow },
		Sleep: func(_ context.Context, d time.Duration) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.slept = append(f.slept, d)
			return nil
		},
	})
}

func testConn() connector.Connection {
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: "fake-access-token-1"}}
}

type emitCall struct {
	DataType string
	Day      time.Time
	Lines    [][]byte
}

// recordingSink keeps every Emit call.
type recordingSink struct {
	mu    sync.Mutex
	calls []emitCall
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

// mapEmitted runs Map over every emitted line, as the ETL would.
func mapEmitted(t *testing.T, c *Connector, sink *recordingSink) model.MapResult {
	t.Helper()
	var all model.MapResult
	for _, call := range sink.calls {
		for _, line := range call.Lines {
			var fields map[string]any
			require.NoError(t, json.Unmarshal(line, &fields))
			res, err := c.Map(connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: call.DataType, Fields: fields})
			require.NoError(t, err)
			all.QuantitySamples = append(all.QuantitySamples, res.QuantitySamples...)
			all.SleepSessions = append(all.SleepSessions, res.SleepSessions...)
			all.SleepPhases = append(all.SleepPhases, res.SleepPhases...)
		}
	}
	return all
}

// fixtureLine is the raw line Fetch stores for a testdata day.
func fixtureLine(t *testing.T, date, file string) map[string]any {
	t.Helper()
	line, err := dayLine(date, ultrahumanfake.File(file))
	require.NoError(t, err)
	require.NotNil(t, line)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(line, &fields))
	return fields
}

func mapFields(t *testing.T, fields map[string]any) (model.MapResult, error) {
	t.Helper()
	return New(Options{}).Map(connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: DataTypeMetrics, Fields: fields})
}
