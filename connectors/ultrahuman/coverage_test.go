// SPDX-License-Identifier: AGPL-3.0-only

package ultrahuman

import (
	"context"
	"encoding/json"
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

// bodyServer answers every request with status and body.
func bodyServer(t *testing.T, status int, body string) *Connector {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(Options{
		ClientID: testClientID, ClientSecret: testClientSecret,
		AuthBase: srv.URL, APIBase: srv.URL, HTTPClient: srv.Client(),
		Now:   func() time.Time { return fakeNow },
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
}

func TestDo_RetriesTransportErrors(t *testing.T) {
	httpc, calls := flakyClient(3, `{"status":"ok"}`)
	var waits []time.Duration
	c := New(Options{
		APIBase: "http://uh.invalid", HTTPClient: httpc, RetryBase: time.Second,
		Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil },
	})
	body, status, err := c.get(context.Background(), "tok", "/api/partners/v1/user_data/metrics", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.JSONEq(t, `{"status":"ok"}`, string(body))
	require.Equal(t, 4, *calls)
	require.Len(t, waits, 3)
	for i, base := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		require.GreaterOrEqual(t, waits[i], base)
		require.LessOrEqual(t, waits[i], base+base/2)
	}
}

func TestDo_TransportFailures(t *testing.T) {
	t.Run("gives up after max attempts", func(t *testing.T) {
		httpc, calls := flakyClient(maxAttempts, "")
		c := New(Options{APIBase: "http://uh.invalid", HTTPClient: httpc,
			Sleep: func(context.Context, time.Duration) error { return nil }})
		_, _, err := c.get(context.Background(), "tok", "/x", nil)
		require.ErrorContains(t, err, "ultrahuman http:")
		require.Equal(t, maxAttempts, *calls)
	})
	t.Run("sleep error ends the ladder", func(t *testing.T) {
		httpc, calls := flakyClient(maxAttempts, "")
		stop := errors.New("shutting down")
		c := New(Options{APIBase: "http://uh.invalid", HTTPClient: httpc,
			Sleep: func(context.Context, time.Duration) error { return stop }})
		_, _, err := c.get(context.Background(), "tok", "/x", nil)
		require.ErrorIs(t, err, stop)
		require.Equal(t, 1, *calls)
	})
	t.Run("cancelled context is not retried", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		httpc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			cancel()
			return nil, errors.New("dial failed")
		})}
		c := New(Options{APIBase: "http://uh.invalid", HTTPClient: httpc,
			Sleep: func(context.Context, time.Duration) error { t.Fatal("no retry after cancel"); return nil }})
		_, _, err := c.get(ctx, "tok", "/x", nil)
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("bad base URL fails the build", func(t *testing.T) {
		c := New(Options{APIBase: "http://uh.invalid\x7f", HTTPClient: http.DefaultClient})
		_, _, err := c.get(context.Background(), "tok", "/x", nil)
		require.Error(t, err)
	})
	t.Run("oversized body", func(t *testing.T) {
		httpc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{},
				Body: io.NopCloser(io.LimitReader(zeroReader{}, maxResponseBytes+1))}, nil
		})}
		c := New(Options{APIBase: "http://uh.invalid", HTTPClient: httpc})
		_, _, err := c.get(context.Background(), "tok", "/x", nil)
		require.ErrorContains(t, err, "ultrahuman: read response: ultrahuman: response exceeds")
	})
}

// zeroReader yields zero bytes forever.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestJitterAndPause(t *testing.T) {
	require.Zero(t, jitter(0))
	require.Zero(t, jitter(-time.Second))
	for range 100 {
		d := jitter(2 * time.Second)
		require.GreaterOrEqual(t, d, 2*time.Second)
		require.LessOrEqual(t, d, 3*time.Second)
	}
	require.NoError(t, pause(context.Background(), 0))
	require.NoError(t, pause(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, pause(ctx, 0), context.Canceled)
	require.ErrorIs(t, pause(ctx, time.Hour), context.Canceled)
}

func TestUserID(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    string
		wantErr string
	}{
		{"top level string id", 200, `{"id":"uh-7"}`, "uh-7", ""},
		{"top level user_id number", 200, `{"user_id":12345678901234567890}`, "12345678901234567890", ""},
		{"nested under data", 200, `{"data":{"user_id":"uh-9"},"status":200}`, "uh-9", ""},
		{"data without id falls back to top level", 200, `{"data":{"name":"x"},"id":"uh-3"}`, "uh-3", ""},
		{"data not an object", 200, `{"data":[1],"id":5}`, "5", ""},
		{"empty id is skipped", 200, `{"id":"","user_id":"uh-4"}`, "uh-4", ""},
		{"no id", 200, `{"data":{"email":"a@b.c"}}`, "", "no user id"},
		{"id of another type", 200, `{"id":{"v":1}}`, "", "no user id"},
		{"not json", 200, `<html>`, "", "user_info:"},
		{"no data", http.StatusNoContent, ``, "", "HTTP 204 without a body"},
		{"forbidden", http.StatusForbidden, ``, "", errForbidden.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := bodyServer(t, tc.status, tc.body)
			id, err := c.userID(context.Background(), "tok")
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Empty(t, id)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, id)
		})
	}
}

func TestPickID(t *testing.T) {
	raw := func(s string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(s), &m))
		return m
	}
	require.Equal(t, "a", pickID(raw(`{"id":"a","user_id":"b"}`)), "id wins over user_id")
	require.Equal(t, "42", pickID(raw(`{"id":42}`)))
	require.Equal(t, "b", pickID(raw(`{"id":null,"user_id":"b"}`)))
	require.Empty(t, pickID(raw(`{"id":true}`)))
	require.Empty(t, pickID(nil))
}

func TestClip(t *testing.T) {
	require.Equal(t, "abc", clip("abc", 3))
	require.Equal(t, "ab", clip("abc", 2))
	require.Empty(t, clip("", 5))
}

func TestRequestToken_ProviderErrorIsClipped(t *testing.T) {
	long := strings.Repeat("x", 100)
	c := bodyServer(t, 200, `{"error":"`+long+`"}`)
	_, err := c.Refresh(context.Background(), connector.Tokens{Refresh: "r1"})
	require.EqualError(t, err, "ultrahuman: refresh: token endpoint: "+strings.Repeat("x", 64))

	c = bodyServer(t, 400, `{"error":"invalid_scope"}`)
	_, err = c.Refresh(context.Background(), connector.Tokens{Refresh: "r1"})
	require.EqualError(t, err, "ultrahuman: refresh: token endpoint HTTP 400: invalid_scope")
}

func TestTemperatureDeltaBounds(t *testing.T) {
	cases := []struct {
		v    float64
		unit string
		want float64
		ok   bool
	}{
		{-0.3, "°C", -0.3, true},
		{10, "celsius", 10, true},
		{-10.5, "°C", 0, false},
		{18, "fahrenheit", 10, true},
		{-27, "degF", -15, false},
	}
	for _, tc := range cases {
		v, unit, ok := temperatureDelta(tc.v, tc.unit)
		require.Equal(t, tc.ok, ok, "%v %s", tc.v, tc.unit)
		if tc.ok {
			require.InDelta(t, tc.want, v, 1e-9)
			require.Equal(t, "celsius", unit)
		}
	}
}

func TestOffsetZone(t *testing.T) {
	for minutes, want := range map[int]string{
		0: "Etc/UTC", 60: "Etc/GMT-1", -480: "Etc/GMT+8", 840: "Etc/GMT-14", -720: "Etc/GMT+12",
		345: "", 900: "", -780: "",
	} {
		require.Equal(t, want, offsetZone(minutes), "offset %d", minutes)
	}
}
