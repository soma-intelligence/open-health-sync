// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestQuotaLimited(t *testing.T) {
	now := fakeNow
	cases := []struct {
		name string
		h    http.Header
		body string
		want time.Duration
	}{
		{"retry-after wins", http.Header{"Retry-After": {"90"}}, `replenished in 01:00:00`, 90 * time.Second},
		{"unusable retry-after falls to the message", http.Header{"Retry-After": {"soon"}}, `Quota will be replenished in 00:10:05.`, 10*time.Minute + 5*time.Second},
		{"days", http.Header{}, `replenished in 0.02:00:00`, 2 * time.Hour},
		{"capped", http.Header{}, `replenished in 2.00:00:01`, connector.MaxRetryAfter},
		{"zero is a second", http.Header{}, `replenished in 00:00:00`, time.Second},
		{"no wait named", http.Header{}, `Out of call volume quota.`, connector.DefaultRetryAfter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, quotaLimited(tc.h, []byte(tc.body), now).RetryAfter)
		})
	}
}

func TestClip(t *testing.T) {
	assert.Equal(t, "abc", clip("abc", 3))
	assert.Equal(t, "a...", clip("abc", 1))
	_, err := workoutItems([]byte(`{"error":"` + strings.Repeat("x", 300) + `","payload":[]}`))
	require.Error(t, err)
	assert.Less(t, len(err.Error()), 260, "a long provider error is clipped")
	assert.True(t, strings.HasSuffix(err.Error(), "..."))
}

// listServer serves workout pages by offset and records the offsets asked.
type listServer struct {
	mu      sync.Mutex
	offsets []string
}

func (s *listServer) start(t *testing.T, perPage int, page func(offset string) string) *Connector {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		off := r.URL.Query().Get("offset")
		s.mu.Lock()
		s.offsets = append(s.offsets, off)
		s.mu.Unlock()
		_, _ = w.Write([]byte(page(off)))
	}))
	t.Cleanup(srv.Close)
	return New(Options{OAuthBase: srv.URL, APIBase: srv.URL, HTTPClient: srv.Client(),
		ClientID: "test-client-id", ClientSecret: "test-client-secret", SubscriptionKey: "test-key",
		PageDelay: -1, PerPage: perPage, Now: func() time.Time { return fakeNow },
		Sleep: func(context.Context, time.Duration) error { return nil }})
}

func (s *listServer) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.offsets...)
}

func TestFetchWorkouts_UnreadableStartAndNoID(t *testing.T) {
	s := &listServer{}
	c := s.start(t, 2, func(off string) string {
		if off == "0" {
			return `{"error":null,"payload":[{"workoutKey":"k1","startTime":1772611200000},{"startTime":-5}]}`
		}
		return `{"error":null,"payload":[{"startTime":0}]}`
	})
	out, err := c.fetchWorkouts(context.Background(), "tok", mar1, fakeNow)
	require.NoError(t, err)
	assert.Equal(t, []string{"0", "2"}, s.asked())
	require.Len(t, out, 3, "lines without an id are all kept")
	assert.Equal(t, time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), out[0].day)
	assert.Equal(t, utcDay(mar1), out[1].day, "a negative start is filed under the window's first day")
	assert.Equal(t, utcDay(mar1), out[2].day, "so is a zero start")
}

func TestFetchWorkouts_BadPages(t *testing.T) {
	for name, body := range map[string]string{
		"item not an object": `[[1]]`,
		"not a list":         `"payload"`,
	} {
		s := &listServer{}
		c := s.start(t, 10, func(string) string { return body })
		_, err := c.fetchWorkouts(context.Background(), "tok", mar1, fakeNow)
		assert.ErrorContains(t, err, "page 1", name)
	}

	// Cancelled once page 1 is in, while it waits before page 2.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	c := New(Options{APIBase: "http://suunto.invalid", SubscriptionKey: "k", PageDelay: time.Hour, PerPage: 1,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			cancel()
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Request: r,
				Body: io.NopCloser(strings.NewReader(`[{"workoutKey":"a"}]`))}, nil
		})}})
	_, err := c.fetchWorkouts(ctx, "tok", mar1, fakeNow)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRevoke_Answers(t *testing.T) {
	var mu sync.Mutex
	status := http.StatusOK
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+r.Header.Get("Authorization"))
		code := status
		mu.Unlock()
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(code)
	}))
	defer srv.Close()
	c := New(Options{ClientID: "test-client-id", ClientSecret: "s", OAuthBase: srv.URL, HTTPClient: srv.Client(),
		Now: func() time.Time { return fakeNow }})
	set := func(code int) {
		mu.Lock()
		defer mu.Unlock()
		status = code
	}

	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{Access: "acc"}))
	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{}), "nothing to send")
	mu.Lock()
	assert.Equal(t, []string{"GET /oauth/deauthorize?client_id=test-client-id Bearer acc"}, seen)
	mu.Unlock()

	set(http.StatusUnauthorized)
	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{Access: "acc"}), "already gone")
	set(http.StatusTooManyRequests)
	rl, ok := connector.AsRateLimit(c.Revoke(context.Background(), connector.Tokens{Access: "acc"}))
	require.True(t, ok)
	assert.Equal(t, 20*time.Second, rl.RetryAfter)
	set(http.StatusInternalServerError)
	require.EqualError(t, c.Revoke(context.Background(), connector.Tokens{Access: "acc"}), "suunto: deauthorize HTTP 500")

	require.ErrorIs(t, New(Options{}).Revoke(context.Background(), connector.Tokens{Access: "acc"}), errNotConfigured)
	srv.Close()
	require.ErrorContains(t, c.Revoke(context.Background(), connector.Tokens{Access: "acc"}), "deauthorize request")
}

func TestRequestToken_Answers(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(t *testing.T, tok connector.Tokens, err error)
	}{
		{"invalid grant", http.StatusBadRequest, `{"error":"invalid_grant"}`,
			func(t *testing.T, _ connector.Tokens, err error) { require.ErrorIs(t, err, connector.ErrNeedsReauth) }},
		{"other 400", http.StatusBadRequest, `{"error":"invalid_request"}`,
			func(t *testing.T, _ connector.Tokens, err error) {
				require.EqualError(t, err, "token endpoint HTTP 400")
				assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
			}},
		{"rate limited", http.StatusTooManyRequests, ``,
			func(t *testing.T, _ connector.Tokens, err error) {
				_, ok := connector.AsRateLimit(err)
				assert.True(t, ok)
			}},
		{"not json", http.StatusOK, `<html>`,
			func(t *testing.T, _ connector.Tokens, err error) { require.ErrorContains(t, err, "decode answer") }},
		{"no access token", http.StatusOK, `{"refresh_token":"r"}`,
			func(t *testing.T, _ connector.Tokens, err error) { require.ErrorContains(t, err, "no access token") }},
		{"opaque token", http.StatusOK, `{"access_token":"opaque","refresh_token":"r","expires_in":"7200","scope":"workout, sleep","user":" someone "}`,
			func(t *testing.T, tok connector.Tokens, err error) {
				require.NoError(t, err)
				assert.Equal(t, "opaque", tok.Access)
				assert.Equal(t, fakeNow.Add(2*time.Hour), tok.Expiry)
				assert.Equal(t, []string{"workout", "sleep"}, tok.Scopes)
				assert.Equal(t, "someone", tok.ProviderUserID, "not a JWT: the user field")
			}},
		{"no scope", http.StatusOK, `{"access_token":"opaque","expires_in":0}`,
			func(t *testing.T, tok connector.Tokens, err error) {
				require.NoError(t, err)
				assert.Nil(t, tok.Scopes)
				assert.True(t, tok.Expiry.IsZero())
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New(Options{ClientID: "id", ClientSecret: "secret", OAuthBase: srv.URL, HTTPClient: srv.Client(),
				Now: func() time.Time { return fakeNow }})
			tok, err := c.requestToken(context.Background(), url.Values{"grant_type": {"refresh_token"}})
			tc.check(t, tok, err)
		})
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	_, err := New(Options{ClientID: "id", ClientSecret: "secret", OAuthBase: base}).requestToken(context.Background(), url.Values{})
	require.ErrorContains(t, err, "token request")
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestJitterPauseReadBody(t *testing.T) {
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
	require.ErrorIs(t, pause(ctx, 0), context.Canceled)
	require.ErrorIs(t, pause(ctx, time.Hour), context.Canceled)

	_, err := readBody(failingReader{}, 10)
	require.EqualError(t, err, "connection reset")
	_, err = readBody(strings.NewReader("abc"), 2)
	require.EqualError(t, err, "suunto: response exceeds 2 bytes")
}

// A start or an id of the wrong JSON type keeps the line, like any
// unreadable start: one odd workout never drops the page.
func TestFetchWorkouts_WrongTypedFieldsKeepThePage(t *testing.T) {
	s := &listServer{}
	c := s.start(t, 10, func(string) string {
		return `{"error":null,"payload":[{"workoutKey":"k1","startTime":"soon"},` +
			`{"workoutId":"w-2","startTime":1772611200000},{"workoutKey":"k3","startTime":null},` +
			`{"workoutKey":"k1","startTime":1772611200000}]}`
	})
	out, err := c.fetchWorkouts(context.Background(), "tok", mar1, fakeNow)
	require.NoError(t, err)
	require.Len(t, out, 3, "the repeated k1 is still deduplicated")
	assert.Equal(t, utcDay(mar1), out[0].day, "an unreadable start is filed under the window's first day")
	assert.Equal(t, time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), out[1].day)
	assert.Equal(t, utcDay(mar1), out[2].day)
}
