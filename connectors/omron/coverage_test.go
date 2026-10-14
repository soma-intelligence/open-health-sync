// SPDX-License-Identifier: AGPL-3.0-only

package omron

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// flakyClient fails the first n requests with a transport error, then
// answers 200 with body.
func flakyClient(n int, body string) (*http.Client, *int) {
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

func testEndpoints(base string) Endpoints {
	return Endpoints{Authorize: base + "/connect/authorize", Token: base + "/connect/token", Measurement: base + "/api/measurement"}
}

func TestRequiredKeysAndEnabled(t *testing.T) {
	c := New(Options{})
	require.Equal(t, []string{KeyClientID, KeyClientSecret}, c.RequiredKeys())
	require.True(t, c.Enabled(connector.Config{KeyClientID: "id", KeyClientSecret: "secret"}))
	require.False(t, c.Enabled(connector.Config{KeyClientSecret: "secret"}))
	require.False(t, c.Enabled(nil))
}

func TestPostRetriesTransportErrors(t *testing.T) {
	httpc, calls := flakyClient(2, `{"status":0}`)
	var waits []time.Duration
	c := New(Options{
		Endpoints: testEndpoints("http://omron.invalid"), HTTPClient: httpc, RetryBase: time.Second,
		Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil },
	})
	body, err := c.post(context.Background(), "acc-1", url.Values{"type": {"bloodpressure"}})
	require.NoError(t, err)
	require.JSONEq(t, `{"status":0}`, string(body))
	require.Equal(t, 3, *calls)
	require.Len(t, waits, 2)
	require.GreaterOrEqual(t, waits[0], time.Second)
	require.LessOrEqual(t, waits[0], 1500*time.Millisecond)
	require.GreaterOrEqual(t, waits[1], 2*time.Second)
	require.LessOrEqual(t, waits[1], 3*time.Second)
}

func TestPostTransportFailures(t *testing.T) {
	t.Run("gives up after max attempts", func(t *testing.T) {
		httpc, calls := flakyClient(maxAttempts, "")
		c := New(Options{Endpoints: testEndpoints("http://omron.invalid"), HTTPClient: httpc,
			Sleep: func(context.Context, time.Duration) error { return nil }})
		_, err := c.post(context.Background(), "acc-1", url.Values{})
		require.ErrorContains(t, err, "omron http:")
		require.ErrorContains(t, err, "connection reset")
		require.Equal(t, maxAttempts, *calls)
	})
	t.Run("sleep error ends the ladder", func(t *testing.T) {
		httpc, calls := flakyClient(maxAttempts, "")
		stop := errors.New("shutting down")
		c := New(Options{Endpoints: testEndpoints("http://omron.invalid"), HTTPClient: httpc,
			Sleep: func(context.Context, time.Duration) error { return stop }})
		_, err := c.post(context.Background(), "acc-1", url.Values{})
		require.ErrorIs(t, err, stop)
		require.Equal(t, 1, *calls)
	})
	t.Run("cancelled context is not retried", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		httpc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			cancel()
			return nil, errors.New("dial failed")
		})}
		c := New(Options{Endpoints: testEndpoints("http://omron.invalid"), HTTPClient: httpc,
			Sleep: func(context.Context, time.Duration) error { t.Fatal("no retry after cancel"); return nil }})
		_, err := c.post(ctx, "acc-1", url.Values{})
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestPostStatusCodes(t *testing.T) {
	tests := []struct {
		name   string
		code   int
		header map[string]string
		check  func(t *testing.T, err error)
	}{
		{"forbidden", http.StatusForbidden, nil, func(t *testing.T, err error) {
			require.ErrorIs(t, err, errUnauthorized)
		}},
		{"rate limited", http.StatusTooManyRequests, map[string]string{"Retry-After": "30"}, func(t *testing.T, err error) {
			rl, ok := connector.AsRateLimit(err)
			require.True(t, ok)
			require.Equal(t, 30*time.Second, rl.RetryAfter)
		}},
		{"server error", http.StatusServiceUnavailable, nil, func(t *testing.T, err error) {
			require.EqualError(t, err, "omron: POST measurement: HTTP 503")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hits int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				require.NoError(t, r.ParseForm())
				require.Equal(t, "weight", r.PostForm.Get("type"))
				require.Equal(t, "Bearer acc-1", r.Header.Get("Authorization"))
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()
			c := New(Options{Endpoints: testEndpoints(srv.URL), HTTPClient: srv.Client(),
				Now:   func() time.Time { return fakeNow },
				Sleep: func(context.Context, time.Duration) error { t.Fatal("HTTP errors are not retried"); return nil }})
			_, err := c.post(context.Background(), "acc-1", url.Values{"type": {"weight"}})
			tc.check(t, err)
			require.Equal(t, 1, hits)
		})
	}
}

func TestFetchRateLimitIsNotReauth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := New(Options{Endpoints: testEndpoints(srv.URL), HTTPClient: srv.Client(),
		Now: func() time.Time { return fakeNow }})
	var s sink
	err := c.Fetch(context.Background(), connector.Connection{Tokens: connector.Tokens{Access: "acc-1"}}, fakeNow.Add(-time.Hour), fakeNow, &s)
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok)
	require.Equal(t, connector.DefaultRetryAfter, rl.RetryAfter)
	require.NotErrorIs(t, err, connector.ErrNeedsReauth)
	require.Empty(t, s.lines)
}

func TestReadBodyLimit(t *testing.T) {
	b, err := readBody(strings.NewReader("abc"), 3)
	require.NoError(t, err)
	require.Equal(t, "abc", string(b))
	_, err = readBody(strings.NewReader("abcd"), 3)
	require.EqualError(t, err, "omron: response exceeds 3 bytes")
}

func TestJitterAndPause(t *testing.T) {
	require.Zero(t, jitter(0))
	require.Zero(t, jitter(-time.Minute))
	for range 100 {
		d := jitter(4 * time.Second)
		require.GreaterOrEqual(t, d, 4*time.Second)
		require.LessOrEqual(t, d, 6*time.Second)
	}

	require.NoError(t, pause(context.Background(), 0))
	require.NoError(t, pause(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, pause(ctx, 0), context.Canceled)
	require.ErrorIs(t, pause(ctx, time.Hour), context.Canceled)
}

func TestPageKey(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{``, ""},
		{`[1,2]`, ""},
		{`{}`, ""},
		{`{"weightPaginationKey":null}`, ""},
		{`{"weightPaginationKey":1644043106941}`, "1644043106941"},
		{`{"weightPaginationKey":"abc-9"}`, "abc-9"},
		{`{"bloodpressurePaginationKey":7}`, ""},
	}
	for _, tc := range tests {
		require.Equal(t, tc.want, pageKey(json.RawMessage(tc.raw), "weightPaginationKey"), tc.raw)
	}
}

func TestMapWeightBodyComposition(t *testing.T) {
	c := New(Options{})
	rec := map[string]any{
		"id": "w-9", "dateTime": "2026-04-01T06:15:00", "dateTimeUtcOffset": "+05:30",
		"weight": 71.4, "weightPref": " KGS ", "bmiValue": 22.1,
		"bodyFatPercentage": "18.5", "visceralFatLevel": 6, "deviceType": "HBF-702T",
	}
	res, err := c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeWeight, Fields: fieldsOf(t, rec)})
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 4)
	want := []struct {
		qt, unit, ext string
		v             float64
	}{
		{"body_mass", "kg", "w-9_weight", 71.4},
		{"body_mass_index", "kg/m2", "w-9_bmi", 22.1},
		{"body_fat_percentage", "percent", "w-9_body_fat", 18.5},
		{"visceral_fat_rating", "index", "w-9_visceral_fat", 6},
	}
	at := time.Date(2026, 4, 1, 6, 15, 0, 0, time.UTC)
	for i, w := range want {
		s := res.QuantitySamples[i]
		require.Equal(t, w.qt, s.QuantityType)
		require.Equal(t, w.unit, s.Unit)
		require.Equal(t, w.ext, s.ExternalID)
		require.Equal(t, w.v, s.Value)
		require.Equal(t, at, s.StartDate)
		require.Equal(t, "HBF-702T", s.DeviceModel)
		require.Equal(t, 330, s.Metadata["utc_offset_minutes"])
		require.NotContains(t, s.Metadata, "timezone", "a half hour offset has no Etc zone")
	}

	// Nothing measured gives no result; an unreadable value counts as unset.
	empty := map[string]any{"id": 3, "dateTime": "2026-04-01T06:15:00", "weight": "n/a", "bmiValue": -1}
	res, err = c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeWeight, Fields: empty})
	require.NoError(t, err)
	require.Empty(t, res.QuantitySamples)
}

func TestMapBloodPressureFlags(t *testing.T) {
	c := New(Options{})
	rec := bpRecord(42, "2026-04-02T21:00:00", 128, 84, nil)
	rec["dateTimeUtcOffset"] = "+00:00"
	rec["irregularHB"] = true
	rec["movementDetect"] = false
	rec["cuffWrapDetect"] = true
	res, err := c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeBloodPressure, Fields: fieldsOf(t, rec)})
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 2, "no pulse sample without a pulse")
	require.Empty(t, res.CategorySamples)
	md := res.QuantitySamples[0].Metadata
	require.Equal(t, true, md["irregular_heartbeat"])
	require.Equal(t, false, md["movement_detected"])
	require.Equal(t, true, md["cuff_wrap_ok"])
	require.Equal(t, "Etc/UTC", md["timezone"])
	require.Equal(t, 0, md["utc_offset_minutes"])

	// A systolic value without its diastolic is no pressure reading; the
	// pulse alone is kept.
	half := bpRecord(43, "2026-04-02T21:05:00", 130, -1, 66)
	res, err = c.Map(connector.RawRecord{UserID: testUser, DataType: DataTypeBloodPressure, Fields: fieldsOf(t, half)})
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 1)
	require.Equal(t, "43_pulse", res.QuantitySamples[0].ExternalID)
	require.Equal(t, "bpm", res.QuantitySamples[0].Unit)
	require.Equal(t, 66.0, res.QuantitySamples[0].Value)
}
