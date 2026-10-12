// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/fitbit/fitbitfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const (
	testClientID      = "test-client-id.apps.googleusercontent.com"
	testClientSecret  = "test-client-secret"
	testWebhookSecret = "Bearer test-webhook-secret"
	testUserID        = "00000000-0000-0000-0000-000000000001"
)

var fakeNow = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

func newFake(t *testing.T) *fitbitfake.Server {
	t.Helper()
	f := fitbitfake.New(t)
	f.ClientID, f.ClientSecret = testClientID, testClientSecret
	f.Scope = ScopeActivity + " " + ScopeMetrics + " " + ScopeSleep
	return f
}

// newConnector returns a connector pointed at the fake, with no pacing, a
// fixed clock and no real sleeping.
func newConnector(f *fitbitfake.Server, mod ...func(*Options)) *Connector {
	o := Options{
		ClientID: testClientID, ClientSecret: testClientSecret, WebhookSecret: testWebhookSecret,
		AuthURL: f.URL() + "/auth", TokenURL: f.URL() + "/token", RevokeURL: f.URL() + "/revoke",
		APIBase: f.URL(), KeysetURL: f.URL() + "/keyset.json",
		HTTPClient: f.Client(), PageDelay: -1,
		Now:   func() time.Time { return fakeNow },
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	for _, m := range mod {
		m(&o)
	}
	return New(o)
}

func testConn(scopes ...string) connector.Connection {
	return connector.Connection{Provider: Slug, Tokens: connector.Tokens{
		Access: "fake-access-token-1", Refresh: "fake-refresh-token", Scopes: scopes,
		ProviderUserID: fitbitfake.HealthUserID,
	}}
}

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

// byType returns the lines emitted for a data type.
func (s *recordingSink) byType(dt string) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]byte
	for _, c := range s.calls {
		if c.DataType == dt {
			out = append(out, c.Lines...)
		}
	}
	return out
}

// mapLine maps one raw line as the ETL would: parsed into fields first.
func mapLine(t *testing.T, dt string, line []byte) model.MapResult {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal(line, &fields))
	res, err := New(Options{}).Map(connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: dt, Fields: fields})
	require.NoError(t, err)
	return res
}

// fixture returns the first data point of a testdata file as a line.
func fixture(t *testing.T, dt string) []byte {
	t.Helper()
	var pts []json.RawMessage
	require.NoError(t, json.Unmarshal(fitbitfake.File(dt+".json"), &pts))
	require.NotEmpty(t, pts)
	return pts[0]
}
