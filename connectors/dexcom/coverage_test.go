// SPDX-License-Identifier: AGPL-3.0-only

package dexcom

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// roundTripFunc is an http.RoundTripper made of one function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// flaky fails the first n requests with a transport error, then answers 200
// with body.
func flaky(n int, body string) (*http.Client, *int) {
	var mu sync.Mutex
	calls := 0
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= n {
			return nil, errors.New("connection reset")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, &calls
}

func TestRequiredKeysAndEnabled(t *testing.T) {
	c := New(Options{})
	require.Equal(t, []string{KeyClientID, KeyClientSecret}, c.RequiredKeys())
	require.True(t, c.Enabled(connector.Config{KeyClientID: "id", KeyClientSecret: "secret"}))
	require.False(t, c.Enabled(connector.Config{KeyClientID: "id"}))
	require.False(t, c.Enabled(connector.Config{}))
}

func TestGetRetriesTransportErrors(t *testing.T) {
	httpc, calls := flaky(2, `{"ok":true}`)
	var waits []time.Duration
	c := New(Options{
		APIBase: "http://dexcom.invalid", HTTPClient: httpc, RetryBase: time.Second,
		Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil },
	})
	body, err := c.get(context.Background(), "acc-1", "/v3/users/self/egvs", nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(body))
	require.Equal(t, 3, *calls)
	require.Len(t, waits, 2)
	// The ladder doubles: 1 s then 2 s, each jittered up to +50%.
	require.GreaterOrEqual(t, waits[0], time.Second)
	require.Less(t, waits[0], 1500*time.Millisecond+time.Nanosecond)
	require.GreaterOrEqual(t, waits[1], 2*time.Second)
	require.Less(t, waits[1], 3*time.Second+time.Nanosecond)
}

func TestGetGivesUpAfterMaxAttempts(t *testing.T) {
	httpc, calls := flaky(maxAttempts, "")
	c := New(Options{
		APIBase: "http://dexcom.invalid", HTTPClient: httpc, RetryBase: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	_, err := c.get(context.Background(), "acc-1", "/v3/users/self/egvs", nil)
	require.ErrorContains(t, err, "dexcom http:")
	require.ErrorContains(t, err, "connection reset")
	require.Equal(t, maxAttempts, *calls)
}

func TestGetStopsOnSleepError(t *testing.T) {
	httpc, calls := flaky(maxAttempts, "")
	stop := errors.New("shutting down")
	c := New(Options{
		APIBase: "http://dexcom.invalid", HTTPClient: httpc,
		Sleep: func(context.Context, time.Duration) error { return stop },
	})
	_, err := c.get(context.Background(), "acc-1", "/v3/users/self/egvs", nil)
	require.ErrorIs(t, err, stop)
	require.Equal(t, 1, *calls)
}

func TestGetCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	httpc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		cancel()
		return nil, errors.New("dial failed")
	})}
	c := New(Options{
		APIBase: "http://dexcom.invalid", HTTPClient: httpc,
		Sleep: func(context.Context, time.Duration) error { t.Fatal("no retry after cancel"); return nil },
	})
	_, err := c.get(ctx, "acc-1", "/v3/users/self/egvs", nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestGetStatusCodes(t *testing.T) {
	retryAt := fakeNow.Add(90 * time.Second)
	tests := []struct {
		name   string
		code   int
		header map[string]string
		check  func(t *testing.T, err error)
	}{
		{"forbidden", http.StatusForbidden, nil, func(t *testing.T, err error) {
			require.ErrorIs(t, err, errUnauthorized)
		}},
		{"rate limited with date", http.StatusTooManyRequests, map[string]string{"Retry-After": retryAt.Format(http.TimeFormat)}, func(t *testing.T, err error) {
			rl, ok := connector.AsRateLimit(err)
			require.True(t, ok)
			require.Equal(t, 90*time.Second, rl.RetryAfter)
		}},
		{"rate limited without header", http.StatusTooManyRequests, nil, func(t *testing.T, err error) {
			rl, ok := connector.AsRateLimit(err)
			require.True(t, ok)
			require.Equal(t, connector.DefaultRetryAfter, rl.RetryAfter)
		}},
		{"server error", http.StatusBadGateway, nil, func(t *testing.T, err error) {
			require.EqualError(t, err, "dexcom: GET /v3/users/self/egvs: HTTP 502")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hits int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				require.Equal(t, "Bearer acc-1", r.Header.Get("Authorization"))
				require.Equal(t, "2026-03-01T00:00:00", r.URL.Query().Get("startDate"))
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()
			c := New(Options{
				APIBase: srv.URL, HTTPClient: srv.Client(),
				Now:   func() time.Time { return fakeNow },
				Sleep: func(context.Context, time.Duration) error { t.Fatal("HTTP errors are not retried"); return nil },
			})
			_, err := c.get(context.Background(), "acc-1", "/v3/users/self/egvs", map[string][]string{"startDate": {"2026-03-01T00:00:00"}})
			tc.check(t, err)
			require.Equal(t, 1, hits)
		})
	}
}

func TestReadBodyLimit(t *testing.T) {
	b, err := readBody(strings.NewReader("12345"), 5)
	require.NoError(t, err)
	require.Equal(t, "12345", string(b))

	_, err = readBody(strings.NewReader("123456"), 5)
	require.EqualError(t, err, "dexcom: response exceeds 5 bytes")

	_, err = readBody(io.MultiReader(strings.NewReader("12"), errReader{}), 5)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

// errReader fails every read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestJitter(t *testing.T) {
	require.Zero(t, jitter(0))
	require.Zero(t, jitter(-time.Second))
	for range 100 {
		d := jitter(10 * time.Second)
		require.GreaterOrEqual(t, d, 10*time.Second)
		require.LessOrEqual(t, d, 15*time.Second)
	}
}

func TestPause(t *testing.T) {
	require.NoError(t, pause(context.Background(), 0))
	require.NoError(t, pause(context.Background(), time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, pause(ctx, 0), context.Canceled)
	require.ErrorIs(t, pause(ctx, time.Hour), context.Canceled)
}

func TestOffsetZone(t *testing.T) {
	tests := []struct {
		minutes int
		want    string
	}{
		{0, "Etc/UTC"},
		{120, "Etc/GMT-2"},
		{-300, "Etc/GMT+5"},
		{14 * 60, "Etc/GMT-14"},
		{-12 * 60, "Etc/GMT+12"},
		{330, ""},
		{15 * 60, ""},
		{-13 * 60, ""},
	}
	for _, tc := range tests {
		require.Equal(t, tc.want, offsetZone(tc.minutes), "offset %d", tc.minutes)
	}
}

func TestMapEGVSparseRecord(t *testing.T) {
	c := New(Options{})

	// A record with only the required fields: unit "unknown" falls back to
	// mg/dL, no trend or app metadata, no transmitter.
	sparse := map[string]any{
		"recordId": "rec-sparse", "systemTime": "2026-02-01T08:00:00Z", "displayTime": "2026-02-01T09:00:00+01:00",
		"value": 104, "unit": "unknown", "transmitterGeneration": "g7",
	}
	res, err := c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeEGV, Fields: sparse})
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 1)
	s := res.QuantitySamples[0]
	require.Equal(t, 104.0, s.Value)
	require.Equal(t, "mg/dL", s.Unit)
	require.Equal(t, "blood_glucose", s.QuantityType)
	require.Empty(t, s.DeviceID)
	require.Equal(t, time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC), s.StartDate)
	require.Equal(t, s.StartDate, s.EndDate)
	require.Equal(t, 60, s.Metadata["utc_offset_minutes"])
	require.Equal(t, "Etc/GMT-1", s.Metadata["timezone"])
	require.NotContains(t, s.Metadata, "trend")
	require.NotContains(t, s.Metadata, "trend_rate")
	require.NotContains(t, s.Metadata, "display_app")
	require.NotContains(t, s.Metadata, "out_of_range")

	// A half hour offset is kept in minutes but has no Etc zone.
	india := map[string]any{
		"recordId": "rec-ist", "systemTime": "2026-02-01T08:00:00Z", "displayTime": "2026-02-01T13:30:00+05:30",
		"value": 110, "unit": "mg/dL",
	}
	res, err = c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeEGV, Fields: india})
	require.NoError(t, err)
	require.Equal(t, 330, res.QuantitySamples[0].Metadata["utc_offset_minutes"])
	require.NotContains(t, res.QuantitySamples[0].Metadata, "timezone")

	noID := map[string]any{"systemTime": "2026-02-01T08:00:00Z", "value": 100}
	_, err = c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeEGV, Fields: noID})
	require.ErrorContains(t, err, "without recordId")

	wrongType := map[string]any{"recordId": "rec-x", "systemTime": "2026-02-01T08:00:00Z", "value": "high"}
	_, err = c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeEGV, Fields: wrongType})
	require.ErrorContains(t, err, "dexcom: egvs:")
}
