// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
)

func TestLimitInFlight(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 8)
	m := newHTTPMetrics()
	h := limitInFlight(8, m, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))
			codes[i] = rec.Code
		}()
	}
	for range 8 {
		<-entered
	}
	assert.Equal(t, int64(8), m.ingestInFlight.Load())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "5", rec.Header().Get("Retry-After"))
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.Equal(t, int64(1), m.ingestRejected.Load())

	close(release)
	wg.Wait()
	for _, c := range codes {
		assert.Equal(t, http.StatusOK, c)
	}
	assert.Equal(t, int64(0), m.ingestInFlight.Load())

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))
	assert.Equal(t, http.StatusOK, rec.Code, "a slot frees up after each request")
}

// One tenant holds at most its share of the slots: the others still upload
// while it saturates its share.
func TestIngestTenantShare(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 8)
	m := newHTTPMetrics()
	l := newIngestLimiter(4, 2, m)
	h := l.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	as := func(tenant uuid.UUID) *http.Request {
		r := httptest.NewRequest("POST", "/", nil)
		return r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{TenantID: tenant}))
	}
	var wg sync.WaitGroup
	for _, tn := range []uuid.UUID{a, a, b, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), as(tn))
		}()
	}
	for range 4 {
		<-entered
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, as(a))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "tenant a is at its share")
	assert.Equal(t, "5", rec.Header().Get("Retry-After"))
	assert.Contains(t, rec.Body.String(), "this tenant")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, as(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "the instance is full")
	assert.Contains(t, rec.Body.String(), "too many uploads in progress")
	assert.Equal(t, int64(2), m.ingestRejected.Load())

	close(release)
	wg.Wait()
	h.ServeHTTP(httptest.NewRecorder(), as(a))
	assert.Equal(t, int64(0), m.ingestInFlight.Load())
	assert.Empty(t, l.held, "every share is given back")
}

func TestIngestTenantShareDefault(t *testing.T) {
	for n, want := range map[int]int{1: 1, 2: 1, 3: 2, 8: 4, 9: 5} {
		assert.Equal(t, want, DefaultIngestTenantLimit(n), n)
	}
}

func TestRequestHash(t *testing.T) {
	a := requestHash("POST", "/v1/users", []byte(`{"a":1}`))
	assert.Len(t, a, 64)
	assert.Equal(t, a, requestHash("POST", "/v1/users", []byte(`{"a":1}`)))
	assert.NotEqual(t, a, requestHash("POST", "/v1/users", []byte(`{"a":2}`)))
	assert.NotEqual(t, a, requestHash("PUT", "/v1/users", []byte(`{"a":1}`)))
	// The separator keeps path and body apart.
	assert.NotEqual(t, requestHash("POST", "/a", []byte("b")), requestHash("POST", "/", []byte("ab")))
}

func TestRenderMetrics(t *testing.T) {
	m := newHTTPMetrics()
	m.count(`GET /v1/users/{id}`, 200)
	m.count(`GET /v1/users/{id}`, 200)
	m.count(``, 404)
	m.count("GET /x\"\n\\", 500)
	var b strings.Builder
	renderMetrics(&b, m, map[string]int64{"parked_jobs": 2, "run_failures_total": 7})
	out := b.String()
	assert.Contains(t, out, "# TYPE ohs_http_requests_total counter\n")
	assert.Contains(t, out, `ohs_http_requests_total{route="GET /v1/users/{id}",code="200"} 2`)
	assert.Contains(t, out, `ohs_http_requests_total{route="unmatched",code="404"} 1`)
	assert.Contains(t, out, `ohs_http_requests_total{route="GET /x\"\n\\",code="500"} 1`)
	assert.Contains(t, out, "# TYPE ohs_etl_parked_jobs gauge\nohs_etl_parked_jobs 2\n")
	assert.Contains(t, out, "# TYPE ohs_etl_run_failures_total counter\nohs_etl_run_failures_total 7\n")
	// Sorted output is stable.
	var b2 strings.Builder
	renderMetrics(&b2, m, map[string]int64{"parked_jobs": 2, "run_failures_total": 7})
	require.Equal(t, out, b2.String())
}

func TestParseTime(t *testing.T) {
	for _, ok := range []string{"2026-03-01", "2026-03-01T10:00:00Z", "2026-03-01T10:00:00+02:00", "2026-03-01T10:00:00.5Z"} {
		v, err := parseTime(ok)
		require.NoError(t, err, ok)
		assert.Equal(t, "UTC", v.Location().String())
	}
	for _, bad := range []string{"2026-3-1", "yesterday", "1709287200"} {
		_, err := parseTime(bad)
		assert.Error(t, err, bad)
	}
}

func TestProblemRequestIDRewritten(t *testing.T) {
	body := []byte(`{"type":"about:blank","title":"Conflict","status":409,"detail":"x","request_id":"old"}` + "\n")
	got := string(withRequestID(body, "new"))
	assert.Contains(t, got, `"request_id":"new"`)
	assert.Contains(t, got, `"detail":"x"`)
	assert.NotContains(t, got, "old")
	assert.Equal(t, "not json", string(withRequestID([]byte("not json"), "new")))
	assert.Equal(t, `{"a":1}`, string(withRequestID([]byte(`{"a":1}`), "")))
}
