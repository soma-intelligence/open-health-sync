// SPDX-License-Identifier: AGPL-3.0-only

package eightsleep

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/eightsleep/eightsleepfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const (
	testClientID     = "test-client-id"
	testClientSecret = "test-client-secret"
	testUserID       = "00000000-0000-0000-0000-000000000001"
)

var fakeNow = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)

func newFake(t *testing.T) *eightsleepfake.Server {
	t.Helper()
	s := eightsleepfake.New(t)
	s.ClientID, s.ClientSecret = testClientID, testClientSecret
	return s
}

// newConnector returns a connector pointed at f, with test keys, no pacing,
// a fixed clock and no real waits.
func newConnector(f *eightsleepfake.Server, opts ...func(*Options)) *Connector {
	o := Options{
		ClientID: testClientID, ClientSecret: testClientSecret,
		AuthURL:    f.URL() + eightsleepfake.TokenPath,
		APIBase:    f.URL() + eightsleepfake.APIPrefix,
		HTTPClient: f.Client(),
		ChunkDelay: -1,
		Now:        func() time.Time { return fakeNow },
		Sleep:      func(context.Context, time.Duration) error { return nil },
	}
	for _, fn := range opts {
		fn(&o)
	}
	return New(o)
}

func testConn() connector.Connection {
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{Access: "fake-access-token-1", ProviderUserID: eightsleepfake.UserID}}
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

// mapLine maps one raw line as the ETL would: parsed into fields, then Map.
func mapLine(t *testing.T, line []byte) model.MapResult {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal(line, &fields))
	res, err := New(Options{}).Map(connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: DataTypeTrends, Fields: fields})
	require.NoError(t, err)
	return res
}
