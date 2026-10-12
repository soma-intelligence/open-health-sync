// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/garmin/garminfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const (
	testClientID     = "test-client-id"
	testClientSecret = "test-client-secret"
	testUserID       = "00000000-0000-0000-0000-000000000001"
)

var fakeNow = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)

type fakeGarmin struct {
	*garminfake.Server
	mu    sync.Mutex
	slept []time.Duration
}

func newFakeGarmin(t *testing.T) *fakeGarmin {
	t.Helper()
	s := garminfake.New(t)
	s.ClientID, s.ClientSecret = testClientID, testClientSecret
	return &fakeGarmin{Server: s}
}

// connector returns a Garmin connector pointed at the fake, with test keys,
// no pacing, a fixed clock and a recording sleeper.
func (f *fakeGarmin) connector(opts ...func(*Options)) *Connector {
	o := Options{
		ClientID: testClientID, ClientSecret: testClientSecret,
		AuthURL:    f.URL() + "/oauth2Confirm",
		TokenURL:   f.URL() + garminfake.TokenPath,
		APIBase:    f.URL(),
		HTTPClient: f.Client(),
		ChunkDelay: -1,
		Now:        func() time.Time { return fakeNow },
		Sleep: func(_ context.Context, d time.Duration) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.slept = append(f.slept, d)
			return nil
		},
	}
	for _, fn := range opts {
		fn(&o)
	}
	return New(o)
}

func testConn() connector.Connection {
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: "fake-access-token-1", Refresh: "fake-refresh-token-1", ProviderUserID: garminfake.UserID}}
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

func (s *recordingSink) lines(dataType string) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]byte
	for _, c := range s.calls {
		if c.DataType == dataType {
			out = append(out, c.Lines...)
		}
	}
	return out
}

// mapLine maps one raw line as the ETL would: parsed into fields, then Map.
func mapLine(t *testing.T, dataType string, line []byte) model.MapResult {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal(line, &fields))
	res, err := New(Options{}).Map(connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: dataType, Fields: fields})
	require.NoError(t, err)
	return res
}

// mapFixture maps the first item of testdata/<dataType>.json.
func mapFixture(t *testing.T, dataType string) model.MapResult {
	t.Helper()
	return mapLine(t, dataType, garminfake.Summaries(dataType)[0])
}
