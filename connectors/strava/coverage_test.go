// SPDX-License-Identifier: AGPL-3.0-only

package strava

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func TestMapEvent_ActivityDeleteBecomesDeletion(t *testing.T) {
	res, err := mapEvent([]byte(`{"object_type":"activity","object_id":1234567,"aspect_type":"delete","owner_id":77}`), "u-1")
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, []model.Deletion{{Provider: Slug, ExternalID: "1234567"}}, res.Deletions)

	for name, line := range map[string]string{
		"zero id":     `{"object_type":"activity","object_id":0,"aspect_type":"delete"}`,
		"negative id": `{"object_type":"activity","object_id":-5,"aspect_type":"delete"}`,
		"no id":       `{"object_type":"activity","aspect_type":"delete"}`,
		"fraction":    `{"object_type":"activity","object_id":1.5,"aspect_type":"delete"}`,
	} {
		_, err := mapEvent([]byte(line), "u-1")
		require.EqualError(t, err, "strava: event: object_id is not an activity id", name)
	}
	_, err = mapEvent([]byte(`{"object_type":7}`), "u-1")
	require.Error(t, err, "a line of the wrong shape")

	res, err = mapEvent([]byte(`{"object_type":"athlete","object_id":1,"aspect_type":"delete"}`), "u-1")
	require.NoError(t, err)
	assert.Nil(t, res, "only an activity delete maps")
}

func TestDeauthorized(t *testing.T) {
	for in, want := range map[string]bool{
		`{"authorized":"false"}`: true,
		`{"authorized":false}`:   true,
		`{"authorized":"true"}`:  false,
		`{"authorized":true}`:    false,
		`{"authorized":0}`:       false,
		`{"title":"Morning"}`:    false,
		`[1]`:                    false,
		``:                       false,
	} {
		assert.Equal(t, want, deauthorized(json.RawMessage(in)), in)
	}
}

// pageServer serves activity pages and records the page asked for.
type pageServer struct {
	mu    sync.Mutex
	pages []string
}

func (s *pageServer) start(t *testing.T, perPage int, page func(n string) string) *Connector {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := r.URL.Query().Get("page")
		s.mu.Lock()
		s.pages = append(s.pages, n)
		s.mu.Unlock()
		_, _ = w.Write([]byte(page(n)))
	}))
	t.Cleanup(srv.Close)
	return New(Options{APIBase: srv.URL, OAuthBase: srv.URL, HTTPClient: srv.Client(),
		ClientID: "test-client-id", ClientSecret: "test-client-secret",
		PageDelay: -1, PerPage: perPage, Now: func() time.Time { return fakeNow },
		Sleep: func(context.Context, time.Duration) error { return nil }})
}

func (s *pageServer) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.pages...)
}

func TestFetchActivities_FiltersAndKeepsUnreadableStarts(t *testing.T) {
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	s := &pageServer{}
	c := s.start(t, 2, func(n string) string {
		switch n {
		case "1":
			return `[{"id":1,"start_date":"2026-03-02T07:00:00Z"},{"id":2,"start_date":"2026-02-28T23:59:59Z"}]`
		case "2":
			return `[{"id":3,"start_date":"2026-03-03T00:00:00Z"},{"id":4,"start_date":"yesterday"}]`
		}
		return `[]`
	})
	out, err := c.fetchActivities(context.Background(), "tok", from, to)
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2", "3"}, s.asked(), "a full page asks for the next one")
	require.Len(t, out, 2)
	assert.JSONEq(t, `{"id":1,"start_date":"2026-03-02T07:00:00Z"}`, string(out[0].line))
	assert.Equal(t, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), out[0].day)
	assert.JSONEq(t, `{"id":4,"start_date":"yesterday"}`, string(out[1].line), "kept for Map to report")
	assert.Equal(t, from, out[1].day, "filed under the first day of the window")
}

func TestFetchActivities_BadPages(t *testing.T) {
	for name, body := range map[string]string{
		"item not an object": `[[1,2]]`,
		"not a list":         `{"message":"oops"}`,
	} {
		s := &pageServer{}
		c := s.start(t, 30, func(string) string { return body })
		_, err := c.fetchActivities(context.Background(), "tok", fetchFromForTest, fetchFromForTest.Add(time.Hour))
		assert.ErrorContains(t, err, "page 1", name)
	}

	// Cancelled once page 1 is in, while it waits before page 2.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	c := New(Options{APIBase: "http://strava.invalid", PageDelay: time.Hour, PerPage: 1,
		HTTPClient: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			calls++
			cancel()
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Request: r,
				Body: io.NopCloser(strings.NewReader(`[{"id":1,"start_date":"2026-03-01T07:00:00Z"}]`))}, nil
		})}})
	_, err := c.fetchActivities(ctx, "tok", fetchFromForTest, fetchFromForTest.Add(24*time.Hour))
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls)

	out, err := c.fetchActivities(context.Background(), "tok", fetchFromForTest, fetchFromForTest)
	require.NoError(t, err)
	assert.Empty(t, out, "an empty window asks nothing")
}

var fetchFromForTest = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

func TestRevoke_Answers(t *testing.T) {
	var mu sync.Mutex
	var forms []string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id, _, _ := r.BasicAuth()
		mu.Lock()
		forms = append(forms, id+" "+r.PostForm.Get("token_type_hint")+" "+r.PostForm.Get("token"))
		code := status
		mu.Unlock()
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(code)
	}))
	defer srv.Close()
	c := New(Options{ClientID: "test-client-id", ClientSecret: "test-client-secret", OAuthBase: srv.URL,
		HTTPClient: srv.Client(), Now: func() time.Time { return fakeNow }})

	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{Access: "acc"}))
	require.NoError(t, c.Revoke(context.Background(), connector.Tokens{}), "nothing to revoke")
	mu.Lock()
	assert.Equal(t, []string{"test-client-id access_token acc"}, forms)
	status = http.StatusTooManyRequests
	mu.Unlock()
	rl, ok := connector.AsRateLimit(c.Revoke(context.Background(), connector.Tokens{Refresh: "ref"}))
	require.True(t, ok)
	assert.Equal(t, 30*time.Second, rl.RetryAfter)

	mu.Lock()
	status = http.StatusBadGateway
	mu.Unlock()
	require.EqualError(t, c.Revoke(context.Background(), connector.Tokens{Refresh: "ref"}), "strava: revoke HTTP 502")

	require.ErrorIs(t, New(Options{}).Revoke(context.Background(), connector.Tokens{Access: "acc"}), errNotConfigured)

	srv.Close()
	err := c.Revoke(context.Background(), connector.Tokens{Access: "acc"})
	require.ErrorContains(t, err, "strava: revoke request")
}

func TestRequestToken_Answers(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(t *testing.T, err error)
	}{
		{"server error", http.StatusInternalServerError, `{}`,
			func(t *testing.T, err error) { require.EqualError(t, err, "token endpoint HTTP 500") }},
		{"bad client is not reauth", http.StatusBadRequest, `{"errors":[{"resource":"Application","field":"client_id","code":"invalid"}]}`,
			func(t *testing.T, err error) {
				require.EqualError(t, err, "token endpoint HTTP 400")
				assert.NotErrorIs(t, err, connector.ErrNeedsReauth)
			}},
		{"invalid grant in plain text", http.StatusBadRequest, `invalid_grant`,
			func(t *testing.T, err error) { require.ErrorIs(t, err, connector.ErrNeedsReauth) }},
		{"unauthorized", http.StatusUnauthorized, ``,
			func(t *testing.T, err error) { require.ErrorIs(t, err, connector.ErrNeedsReauth) }},
		{"not json", http.StatusOK, `<html>`,
			func(t *testing.T, err error) { require.ErrorContains(t, err, "token endpoint: decode answer") }},
		{"no access token", http.StatusOK, `{"refresh_token":"r"}`,
			func(t *testing.T, err error) {
				require.EqualError(t, err, "token endpoint: answer holds no access token")
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New(Options{ClientID: "id", ClientSecret: "secret", OAuthBase: srv.URL, HTTPClient: srv.Client()})
			_, err := c.requestToken(context.Background(), url.Values{"grant_type": {"refresh_token"}})
			tc.check(t, err)
		})
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	c := New(Options{ClientID: "id", ClientSecret: "secret", OAuthBase: base})
	_, err := c.requestToken(context.Background(), url.Values{})
	require.ErrorContains(t, err, "token request")
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestJitterPauseReadBody(t *testing.T) {
	assert.Zero(t, jitter(0))
	assert.Zero(t, jitter(-time.Second))
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
	b, err := readBody(strings.NewReader("[]"), 2)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(b))
}
