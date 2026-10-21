// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestGetRetriesTransportErrors(t *testing.T) {
	httpc, calls := flakyClient(2, `[]`)
	var waits []time.Duration
	c := New(Options{
		APIBase: "http://icu.invalid", HTTPClient: httpc, RetryBase: time.Second,
		Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil },
	})
	body, err := c.get(context.Background(), "tok", "/athlete/0/activities", nil)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(body))
	assert.Equal(t, 3, *calls)
	require.Len(t, waits, 2)
	assert.GreaterOrEqual(t, waits[0], time.Second)
	assert.LessOrEqual(t, waits[0], 1500*time.Millisecond)
	assert.GreaterOrEqual(t, waits[1], 2*time.Second)
	assert.LessOrEqual(t, waits[1], 3*time.Second)
}

func TestGetTransportFailures(t *testing.T) {
	t.Run("gives up after max attempts", func(t *testing.T) {
		httpc, calls := flakyClient(maxAttempts, "")
		c := New(Options{APIBase: "http://icu.invalid", HTTPClient: httpc,
			Sleep: func(context.Context, time.Duration) error { return nil }})
		_, err := c.get(context.Background(), "tok", "/x", nil)
		assert.ErrorContains(t, err, "intervals_icu http:")
		assert.ErrorContains(t, err, "connection reset")
		assert.Equal(t, maxAttempts, *calls)
	})
	t.Run("sleep error ends the ladder", func(t *testing.T) {
		httpc, calls := flakyClient(maxAttempts, "")
		stop := errors.New("shutting down")
		c := New(Options{APIBase: "http://icu.invalid", HTTPClient: httpc,
			Sleep: func(context.Context, time.Duration) error { return stop }})
		_, err := c.get(context.Background(), "tok", "/x", nil)
		assert.ErrorIs(t, err, stop)
		assert.Equal(t, 1, *calls)
	})
	t.Run("cancelled context is not retried", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		httpc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			cancel()
			return nil, errors.New("dial failed")
		})}
		c := New(Options{APIBase: "http://icu.invalid", HTTPClient: httpc,
			Sleep: func(context.Context, time.Duration) error { t.Fatal("no retry after cancel"); return nil }})
		_, err := c.get(ctx, "tok", "/x", nil)
		assert.ErrorIs(t, err, context.Canceled)
	})
	t.Run("unreadable body", func(t *testing.T) {
		httpc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(failingReader{})}, nil
		})}
		c := New(Options{APIBase: "http://icu.invalid", HTTPClient: httpc})
		_, err := c.get(context.Background(), "tok", "/x", nil)
		assert.ErrorContains(t, err, "intervals_icu: read response:")
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})
}

// failingReader fails every read.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestReadBodyLimit(t *testing.T) {
	b, err := readBody(strings.NewReader("ab"), 2)
	require.NoError(t, err)
	assert.Equal(t, "ab", string(b))
	_, err = readBody(strings.NewReader("abc"), 2)
	assert.EqualError(t, err, "intervals_icu: response exceeds 2 bytes")
}

func TestJitterAndPause(t *testing.T) {
	assert.Zero(t, jitter(0))
	assert.Zero(t, jitter(-time.Second))
	for range 100 {
		d := jitter(5 * time.Second)
		assert.GreaterOrEqual(t, d, 5*time.Second)
		assert.LessOrEqual(t, d, 7500*time.Millisecond)
	}
	assert.NoError(t, pause(context.Background(), 0))
	assert.NoError(t, pause(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, pause(ctx, 0), context.Canceled)
	assert.ErrorIs(t, pause(ctx, time.Hour), context.Canceled)
}

func TestFlexIDUnmarshal(t *testing.T) {
	cases := []struct {
		in      string
		want    flexID
		wantErr bool
	}{
		{`"i12345"`, "i12345", false},
		{`"  i7 "`, "i7", false},
		{`900001`, "900001", false},
		{` 42 `, "42", false},
		{`null`, "", false},
		{`"unterminated`, "", true},
		{`true`, "", true},
		{`{"id":1}`, "", true},
	}
	for _, tc := range cases {
		var f flexID = "previous"
		err := f.UnmarshalJSON([]byte(tc.in))
		if tc.wantErr {
			assert.Error(t, err, tc.in)
			continue
		}
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, f, tc.in)
	}

	// Inside a document, as the token answer carries it.
	var tb struct {
		Athlete struct {
			ID flexID `json:"id"`
		} `json:"athlete"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{"athlete":{"id":123}}`), &tb))
	assert.Equal(t, flexID("123"), tb.Athlete.ID)
}

func TestActivityStartLocalEdges(t *testing.T) {
	// Fractional seconds in the local start are read; the zone gives UTC.
	res, err := mapLine(t, DataTypeActivity, `{"id":"i6","type":"Run","start_date_local":"2026-01-10T07:30:00.250","timezone":"Europe/Paris","elapsed_time":1800}`)
	require.NoError(t, err)
	w := res.Workouts[0]
	assert.Equal(t, time.Date(2026, 1, 10, 6, 30, 0, 0, time.UTC), w.StartDate, "CET is UTC+1; sub-second precision is dropped")
	assert.Equal(t, w.StartDate.Add(30*time.Minute), w.EndDate)

	for _, line := range []string{
		// An unknown zone leaves no way to place the local start.
		`{"id":"i7","type":"Run","start_date_local":"2026-01-10T07:30:00","timezone":"Mars/Olympus"}`,
		`{"id":"i8","type":"Run","start_date_local":"10/01/2026 07:30","timezone":"Europe/Paris"}`,
	} {
		_, err := mapLine(t, DataTypeActivity, line)
		assert.Error(t, err, line)
	}
}

func TestZone(t *testing.T) {
	assert.Nil(t, zone(""))
	assert.Nil(t, zone("   "))
	assert.Nil(t, zone("Not/AZone"))
	loc := zone(" Asia/Tokyo ")
	require.NotNil(t, loc)
	assert.Equal(t, "Asia/Tokyo", loc.String())
}
