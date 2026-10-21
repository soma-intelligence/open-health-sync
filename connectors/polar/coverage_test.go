// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func TestMapSleep_UnknownStagesCounted(t *testing.T) {
	before := DroppedSleepStates()
	res, err := mapLine(t, DataTypeSleep, `{"date":"2026-03-04",
		"sleep_start_time":"2026-03-03T23:30:00+01:00","sleep_end_time":"2026-03-04T03:00:00+01:00",
		"hypnogram":{"23:30":3,"01:00":5,"01:30":9,"02:00":4}}`)
	require.NoError(t, err)
	assert.Equal(t, int64(2), DroppedSleepStates()-before, "codes 5 and 9 are dropped")

	require.Len(t, res.SleepPhases, 2)
	light, deep := res.SleepPhases[0], res.SleepPhases[1]
	assert.Equal(t, model.SleepStageLight, light.Phase)
	assert.Equal(t, time.Date(2026, 3, 3, 22, 30, 0, 0, time.UTC), light.StartDate)
	assert.Equal(t, time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), light.EndDate, "a segment ends where the next mark starts")
	assert.Equal(t, 90*60, light.DurationS)
	assert.Equal(t, model.SleepStageDeep, deep.Phase)
	assert.Equal(t, time.Date(2026, 3, 4, 1, 0, 0, 0, time.UTC), deep.StartDate)
	assert.Equal(t, time.Date(2026, 3, 4, 2, 0, 0, 0, time.UTC), deep.EndDate, "the last segment ends with the night")

	// A night with only known stages drops nothing.
	before = DroppedSleepStates()
	_, err = mapLine(t, DataTypeSleep, `{"date":"2026-03-04",
		"sleep_start_time":"2026-03-03T23:30:00+01:00","sleep_end_time":"2026-03-04T03:00:00+01:00",
		"hypnogram":{"23:30":0,"23:45":1}}`)
	require.NoError(t, err)
	assert.Equal(t, before, DroppedSleepStates())
}

// apiStub answers every request with handle and records the paths asked.
type apiStub struct {
	mu    sync.Mutex
	paths []string
	query []string
}

func (s *apiStub) start(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *Connector {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.query = append(s.query, r.URL.RawQuery)
		s.mu.Unlock()
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(Options{APIBase: srv.URL, HTTPClient: srv.Client(), ChunkDelay: -1,
		Now:   func() time.Time { return fakeNow },
		Sleep: func(context.Context, time.Duration) error { return nil }})
}

func (s *apiStub) seen() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...), append([]string(nil), s.query...)
}

func TestRevoke_Statuses(t *testing.T) {
	tok := connector.Tokens{Access: "fake-access-token-1", ProviderUserID: "user/1"}
	for _, code := range []int{http.StatusNoContent, http.StatusNotFound, http.StatusUnauthorized} {
		s := &apiStub{}
		c := s.start(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })
		require.NoError(t, c.Revoke(context.Background(), tok), "HTTP %d counts as done", code)
		paths, _ := s.seen()
		assert.Equal(t, []string{"DELETE /v3/users/user/1"}, paths)
	}

	s := &apiStub{}
	c := s.start(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	require.EqualError(t, c.Revoke(context.Background(), tok), "polar: revoke: HTTP 500")

	s = &apiStub{}
	c = s.start(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("RateLimit-Usage", "10, 500")
		w.Header().Set("RateLimit-Limit", "100, 500")
		w.Header().Set("RateLimit-Reset", "60, 3600")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	err := c.Revoke(context.Background(), tok)
	rl, ok := connector.AsRateLimit(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, time.Hour, rl.RetryAfter, "the long term window is used up")

	require.Error(t, New(Options{}).Revoke(context.Background(), connector.Tokens{Access: "a"}))
}

func TestSend_TransportErrorsRetriedThenReported(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	var waits []time.Duration
	c := New(Options{APIBase: base, RetryBase: 10 * time.Millisecond,
		Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }})
	status, err := c.send(context.Background(), http.MethodPost, "tok", "/v3/users", []byte(`{}`))
	require.ErrorContains(t, err, "polar http")
	assert.Zero(t, status)
	assert.Len(t, waits, maxAttempts-1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.send(ctx, http.MethodPost, "tok", "/v3/users", nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSend_SleepErrorStopsRetries(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	var calls atomic.Int32
	c := New(Options{APIBase: base,
		Sleep: func(context.Context, time.Duration) error { calls.Add(1); return context.DeadlineExceeded }})
	_, err := c.send(context.Background(), http.MethodDelete, "tok", "/v3/users/1", nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, int32(1), calls.Load())
}

func TestSend_PayloadAndStatus(t *testing.T) {
	var got struct {
		ctype, auth string
	}
	s := &apiStub{}
	c := s.start(t, func(w http.ResponseWriter, r *http.Request) {
		got.ctype, got.auth = r.Header.Get("Content-Type"), r.Header.Get("Authorization")
		w.WriteHeader(http.StatusConflict)
	})
	status, err := c.send(context.Background(), http.MethodPost, "tok", "/v3/users", []byte(`{"member-id":"ohs-1"}`))
	require.NoError(t, err, "the caller reads the status")
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "application/json", got.ctype)
	assert.Equal(t, "Bearer tok", got.auth)
}

func TestPause(t *testing.T) {
	require.NoError(t, pause(context.Background(), 0))
	require.NoError(t, pause(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, pause(ctx, -1), context.Canceled)
	require.ErrorIs(t, pause(ctx, time.Hour), context.Canceled)
}

func TestFetchContinuousHR_ChunksAndOffsets(t *testing.T) {
	s := &apiStub{}
	c := s.start(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("from") != "2026-01-01" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"heart_rates":[
			{"date":"2026-01-02","heart_rate_samples":[{"heart_rate":61,"sample_time":"08:00:00"}]},
			{"date":"2026-01-20","heart_rate_samples":[]}]}`))
	})
	st := &pullState{offsets: map[string]int{"2026-01-03": 60}}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out, err := c.fetchContinuousHR(context.Background(), "tok", from, from.AddDate(0, 0, 40), st)
	require.NoError(t, err)

	_, queries := s.seen()
	assert.Equal(t, []string{"from=2026-01-01&to=2026-01-28", "from=2026-01-29&to=2026-02-09"}, queries, "windows of 28 days")
	require.Len(t, out, 2)
	assert.Equal(t, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), out[0].day)
	assert.JSONEq(t, `{"date":"2026-01-02","heart_rate_samples":[{"heart_rate":61,"sample_time":"08:00:00"}],"utc_offset_minutes":60}`,
		string(out[0].line), "a neighbouring day lends its offset")
	assert.JSONEq(t, `{"date":"2026-01-20","heart_rate_samples":[]}`, string(out[1].line), "no offset known that far away")
}

func TestFetchContinuousHR_BadAnswers(t *testing.T) {
	for name, body := range map[string]string{
		"no list":    `{"other":[]}`,
		"not json":   `{"heart_rates":`,
		"bad date":   `{"heart_rates":[{"date":"02.01.2026"}]}`,
		"date shape": `{"heart_rates":[{"date":2}]}`,
	} {
		s := &apiStub{}
		c := s.start(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
		_, err := c.fetchContinuousHR(context.Background(), "tok", fakeNow.AddDate(0, 0, -1), fakeNow, &pullState{})
		assert.Error(t, err, name)
	}
	s := &apiStub{}
	c := s.start(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	_, err := c.fetchContinuousHR(context.Background(), "tok", fakeNow.AddDate(0, 0, -1), fakeNow, &pullState{})
	require.ErrorIs(t, err, errForbidden)

	c.chunkDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.fetchContinuousHR(ctx, "tok", fakeNow.AddDate(0, 0, -60), fakeNow, &pullState{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestFetchPhysicalInfo(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantDay time.Time
		wantErr bool
		empty   bool
	}{
		{name: "modified wins", status: 200, body: `{"created":"2026-01-01T10:00:00Z","modified":"2026-02-15T23:30:00-02:00","weight":70.5}`,
			wantDay: time.Date(2026, 2, 16, 0, 0, 0, 0, time.UTC)},
		{name: "created", status: 200, body: `{"created":"2026-01-01T10:00:00Z","weight":70.5}`,
			wantDay: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "pull time", status: 200, body: `{"weight":70.5}`, wantDay: fakeNow},
		{name: "no content", status: http.StatusNoContent, empty: true},
		{name: "blank body", status: 200, body: "  \n", empty: true},
		{name: "bad time", status: 200, body: `{"modified":"last week"}`, wantErr: true},
		{name: "not an object", status: 200, body: `[1,2]`, wantErr: true},
		{name: "not json", status: 200, body: `{"weight":`, wantErr: true},
		{name: "wrong type", status: 200, body: `{"weight":"heavy"}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &apiStub{}
			c := s.start(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			out, err := c.fetchPhysicalInfo(context.Background(), "tok")
			paths, _ := s.seen()
			assert.Equal(t, []string{"GET /v3/users/physical-info"}, paths)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.empty {
				assert.Empty(t, out)
				return
			}
			require.Len(t, out, 1)
			assert.Equal(t, tc.wantDay, out[0].day)
			var p PhysicalInfo
			require.NoError(t, json.Unmarshal(out[0].line, &p))
			assert.Equal(t, "2026-03-10T00:00:00Z", p.FetchedAt, "the pull time is added")
			require.NotNil(t, p.Weight)
			assert.InDelta(t, 70.5, *p.Weight, 1e-9)
		})
	}
}
