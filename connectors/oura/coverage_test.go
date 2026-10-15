// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestMapRecord_UnknownTypesCountedAndBounded(t *testing.T) {
	m := newMapper()
	for i := 0; i < maxUnknownTypes+6; i++ {
		res, err := m.mapRecord(fmt.Sprintf("future_type_%d", i), map[string]any{"id": "x"}, testUserID)
		require.NoError(t, err)
		assert.Nil(t, res, "an unknown type maps to nothing")
	}
	res, err := m.mapRecord("future_type_0", nil, testUserID)
	require.NoError(t, err)
	assert.Nil(t, res)

	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Len(t, m.unknownTypes, maxUnknownTypes, "the counter stops growing")
	assert.Equal(t, 2, m.unknownTypes["future_type_0"], "a type already seen keeps counting")
	assert.NotContains(t, m.unknownTypes, fmt.Sprintf("future_type_%d", maxUnknownTypes))
}

func TestMapRecord_FieldsThatCannotBeEncoded(t *testing.T) {
	_, err := New(Options{}).Map(connector.RawRecord{UserID: testUserID, DataType: DataTypeHeartRate,
		Fields: map[string]any{"bpm": math.Inf(1)}})
	require.ErrorContains(t, err, "oura: heartrate: re-encode line")
}

func TestMapRecord_WrongShapeNamesTheType(t *testing.T) {
	_, err := New(Options{}).Map(connector.RawRecord{UserID: testUserID, DataType: DataTypeDailySpO2,
		Fields: map[string]any{"day": 20260301}})
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "oura: "+DataTypeDailySpO2+": "), err.Error())
}

func TestClip(t *testing.T) {
	assert.Equal(t, "abc", clip("abc", 3))
	assert.Equal(t, "ab...", clip("abc", 2))
	assert.Equal(t, "...", clip("abc", 0))
}

func TestRevoke_Answers(t *testing.T) {
	var calls atomic.Int32
	var status atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "45")
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	c := New(Options{APIBase: srv.URL, HTTPClient: srv.Client(), Now: func() time.Time { return fakeNow }})

	status.Store(http.StatusTooManyRequests)
	err := c.Revoke(context.Background(), connector.Tokens{Access: "tok-a"})
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 45*time.Second, rl.RetryAfter)

	status.Store(http.StatusBadRequest)
	require.EqualError(t, c.Revoke(context.Background(), connector.Tokens{Access: "tok-a"}), "oura: revoke HTTP 400")

	status.Store(http.StatusNoContent)
	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{Access: "tok-a"}))
	assert.Equal(t, int32(3), calls.Load())
}

func TestRevoke_TransportErrorHidesTheToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	c := New(Options{APIBase: base})
	err := c.Revoke(context.Background(), connector.Tokens{Access: "secret-access-token"})
	require.EqualError(t, err, "oura: revoke request failed")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, c.Revoke(ctx, connector.Tokens{Access: "secret-access-token"}), context.Canceled)
}

// cutShort is a body that fails after its first bytes.
type cutShort struct{ sent bool }

func (b *cutShort) Read(p []byte) (int, error) {
	if b.sent {
		return 0, fmt.Errorf("connection reset")
	}
	b.sent = true
	return copy(p, `{"data":`), nil
}

func (b *cutShort) Close() error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGet_BodyCutShortIsRetried(t *testing.T) {
	var calls atomic.Int32
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: &cutShort{}, Request: r}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
			Body: http.NoBody, Request: r}, nil
	})
	var waits []time.Duration
	c := New(Options{APIBase: "http://oura.invalid", HTTPClient: &http.Client{Transport: rt}, RetryBase: 10 * time.Millisecond,
		Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }})
	var out map[string]any
	err := c.get(context.Background(), "tok", "/v2/usercollection/sleep", nil, &out)
	require.ErrorContains(t, err, "decode answer", "the second, empty answer is not JSON")
	assert.Equal(t, int32(2), calls.Load())
	require.Len(t, waits, 1)
	assert.GreaterOrEqual(t, waits[0], 10*time.Millisecond)
	assert.Less(t, waits[0], 16*time.Millisecond)
}

func TestGet_BodyAlwaysCutShortGivesUp(t *testing.T) {
	var calls atomic.Int32
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: &cutShort{}, Request: r}, nil
	})
	c := New(Options{APIBase: "http://oura.invalid", HTTPClient: &http.Client{Transport: rt},
		Sleep: func(context.Context, time.Duration) error { return nil }})
	err := c.get(context.Background(), "tok", "/v2/usercollection/sleep", nil, &struct{}{})
	require.ErrorContains(t, err, "oura: read response")
	assert.Equal(t, int32(maxAttempts), calls.Load())
}

func TestGet_StatusAnswers(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		status int
		header string
		check  func(t *testing.T, err error)
	}{
		{"not found outside the collection", "/v2/usercollection_other", http.StatusNotFound, "",
			func(t *testing.T, err error) { require.EqualError(t, err, "oura: /v2/usercollection_other: HTTP 404") }},
		{"not found in the collection", "/v2/usercollection/sleep", http.StatusNotFound, "",
			func(t *testing.T, err error) { require.ErrorIs(t, err, errNotFound) }},
		{"server error with Retry-After", "/v2/usercollection/sleep", http.StatusBadGateway, "20",
			func(t *testing.T, err error) {
				rl, ok := connector.AsRateLimit(err)
				require.True(t, ok, "got %v", err)
				assert.Equal(t, 20*time.Second, rl.RetryAfter)
			}},
		{"redirect status", "/v2/usercollection/sleep", http.StatusNotModified, "",
			func(t *testing.T, err error) { require.ErrorContains(t, err, "unexpected HTTP 304") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			c := New(Options{APIBase: srv.URL, HTTPClient: srv.Client(), Now: func() time.Time { return fakeNow },
				Sleep: func(context.Context, time.Duration) error { return nil }})
			tc.check(t, c.get(context.Background(), "tok", tc.path, nil, &struct{}{}))
		})
	}
}

func TestGet_SleepCancelledEndsRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	c := New(Options{APIBase: srv.URL, HTTPClient: srv.Client(),
		Sleep: func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }})
	require.ErrorIs(t, c.get(ctx, "tok", "/v2/usercollection/sleep", nil, &struct{}{}), context.Canceled)
	assert.Equal(t, int32(1), calls.Load())
}

func TestJitterAndPause(t *testing.T) {
	assert.Zero(t, jitter(0))
	for range 20 {
		d := jitter(time.Second)
		assert.GreaterOrEqual(t, d, time.Second)
		assert.LessOrEqual(t, d, 1500*time.Millisecond)
	}
	require.NoError(t, pause(context.Background(), 0))
	require.NoError(t, pause(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, pause(ctx, time.Hour), context.Canceled)
}
