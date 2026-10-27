// SPDX-License-Identifier: AGPL-3.0-only

package strava

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestRateLimited(t *testing.T) {
	now := time.Date(2026, 3, 3, 10, 7, 30, 0, time.UTC)
	cases := map[string]struct {
		h    http.Header
		want time.Duration
	}{
		"retry-after wins":                          {http.Header{"Retry-After": {"90"}, "X-Ratelimit-Usage": {"5,2000"}, "X-Ratelimit-Limit": {"200,2000"}}, 90 * time.Second},
		"retry-after date":                          {http.Header{"Retry-After": {"Tue, 03 Mar 2026 10:09:30 GMT"}}, 2 * time.Minute},
		"bad retry-after falls to the quarter hour": {http.Header{"Retry-After": {"soon"}}, 7*time.Minute + 30*time.Second},
		"short window spent":                        {http.Header{"X-Ratelimit-Limit": {"200,2000"}, "X-Ratelimit-Usage": {"200,450"}}, 7*time.Minute + 30*time.Second},
		"daily spent":                               {http.Header{"X-Ratelimit-Limit": {"200,2000"}, "X-Ratelimit-Usage": {"12,2000"}}, 13*time.Hour + 52*time.Minute + 30*time.Second},
		"daily read spent":                          {http.Header{"X-Readratelimit-Limit": {"100,1000"}, "X-Readratelimit-Usage": {"20,1001"}}, 13*time.Hour + 52*time.Minute + 30*time.Second},
		"no headers":                                {http.Header{}, 7*time.Minute + 30*time.Second},
		"garbled usage":                             {http.Header{"X-Ratelimit-Limit": {"200"}, "X-Ratelimit-Usage": {"a,b"}}, 7*time.Minute + 30*time.Second},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, rateLimited(tc.h, now).RetryAfter)
		})
	}
}

func TestGet_RetriesTransportError(t *testing.T) {
	f := newFakeStrava(t)
	c := f.connector(0)
	calls := 0
	c.httpc = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return nil, assert.AnError
		}
		return f.Client().Transport.RoundTrip(r)
	})}
	_, err := c.get(context.Background(), "tok", activitiesPath, nil)
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
	assert.Len(t, f.slept, 2)
	assert.GreaterOrEqual(t, f.slept[1], 2*f.slept[0]*2/3, "the ladder doubles")
}

func TestGet_TransportErrorSurfacesAfterTheBudget(t *testing.T) {
	f := newFakeStrava(t)
	c := f.connector(0)
	c.httpc = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return nil, assert.AnError })}
	_, err := c.get(context.Background(), "tok", activitiesPath, nil)
	require.Error(t, err)
	assert.Len(t, f.slept, maxAttempts-1)
}

func TestFetch_PageThatIsNotAListIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":"not a list"}`))
	}))
	defer srv.Close()
	c := New(Options{ClientID: "a", ClientSecret: "b", APIBase: srv.URL, HTTPClient: srv.Client(), PageDelay: -1})
	err := c.Fetch(context.Background(), conn(), march1, march3, &recordingSink{})
	assert.ErrorContains(t, err, "decode page 1")
}

// A null entry of a page used to be stored as the raw line "null", which is
// not an activity and failed Map on every replay.
func TestFetch_NullEntryIsSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[null, {"id":1,"start_date":"2026-03-01T07:00:00Z"}]`))
	}))
	defer srv.Close()
	c := New(Options{ClientID: "a", ClientSecret: "b", APIBase: srv.URL, HTTPClient: srv.Client(), PageDelay: -1})
	sink := &recordingSink{}
	require.NoError(t, c.Fetch(context.Background(), conn(), march1, march3, sink))
	assert.Equal(t, []string{`{"id":1,"start_date":"2026-03-01T07:00:00Z"}`}, linesOf(sink))
}

func linesOf(s *recordingSink) []string {
	var out []string
	for _, l := range s.lines() {
		out = append(out, string(l))
	}
	return out
}

func TestReadBody_RefusesPastTheCap(t *testing.T) {
	_, err := readBody(strings.NewReader("12345"), 4)
	assert.Error(t, err)
	b, err := readBody(strings.NewReader("1234"), 4)
	require.NoError(t, err)
	assert.Equal(t, "1234", string(b))
}

func TestRateLimitIsTyped(t *testing.T) {
	_, ok := connector.AsRateLimit(rateLimited(http.Header{}, time.Now()))
	assert.True(t, ok)
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
