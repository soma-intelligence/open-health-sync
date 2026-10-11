// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/polar/polarfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const (
	testClientID     = "test-client-id"
	testClientSecret = "test-client-secret"
	testUserID       = "00000000-0000-0000-0000-000000000001"
	testPolarUser    = "10101010"
)

var fakeNow = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

// fakePolar wraps the httptest AccessLink of polarfake.
type fakePolar struct {
	*polarfake.Server
	mu    sync.Mutex
	slept []time.Duration
}

func newFakePolar(t *testing.T) *fakePolar {
	t.Helper()
	s := polarfake.New(t)
	s.ClientID, s.ClientSecret = testClientID, testClientSecret
	return &fakePolar{Server: s}
}

// connector returns a Polar connector pointed at the fake, with test keys,
// no pacing, a fixed clock and a recording sleeper.
func (f *fakePolar) connector() *Connector {
	return New(Options{
		ClientID: testClientID, ClientSecret: testClientSecret,
		AuthBase: f.URL(), TokenBase: f.URL(), APIBase: f.URL(),
		HTTPClient: f.Client(),
		ChunkDelay: -1,
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
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: "fake-access-token-1", ProviderUserID: testPolarUser}}
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

// mapEmitted runs Map over every emitted line of dataType, as the ETL would
// after reading the raw object back.
func mapEmitted(t *testing.T, c *Connector, sink *recordingSink, dataType string) model.MapResult {
	t.Helper()
	var all model.MapResult
	for _, line := range sink.linesOf(dataType) {
		var fields map[string]any
		require.NoError(t, json.Unmarshal(line, &fields))
		res, err := c.Map(connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: dataType, Fields: fields})
		require.NoError(t, err)
		all.QuantitySamples = append(all.QuantitySamples, res.QuantitySamples...)
		all.SleepSessions = append(all.SleepSessions, res.SleepSessions...)
		all.SleepPhases = append(all.SleepPhases, res.SleepPhases...)
		all.Workouts = append(all.Workouts, res.Workouts...)
	}
	return all
}

// mapLine maps one JSON line.
func mapLine(t *testing.T, dataType, line string) (model.MapResult, error) {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &fields))
	return New(Options{}).Map(connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: dataType, Fields: fields})
}
