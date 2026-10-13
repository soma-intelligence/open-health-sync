// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// fakeClock is a settable clock for the limiter.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(perAddr, global RateLimit) (*rateLimiter, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	l := newRateLimiter(perAddr, global)
	l.now = clk.now
	return l, clk
}

func TestBucketBurstThenRefill(t *testing.T) {
	l, clk := newTestLimiter(RateLimit{Rate: 2, Burst: 4}, RateLimit{})
	for i := range 4 {
		ok, _, _ := l.allow("192.0.2.1", "")
		require.True(t, ok, "burst request %d", i)
	}
	ok, wait, scope := l.allow("192.0.2.1", "")
	require.False(t, ok, "over the burst")
	assert.Equal(t, scopeAddress, scope)
	assert.Equal(t, 500*time.Millisecond, wait, "one token every 1/rate seconds")

	ok, _, _ = l.allow("192.0.2.2", "")
	assert.True(t, ok, "another address has its own bucket")

	clk.advance(500 * time.Millisecond)
	ok, _, _ = l.allow("192.0.2.1", "")
	assert.True(t, ok, "one token refilled")
	ok, _, _ = l.allow("192.0.2.1", "")
	assert.False(t, ok)

	clk.advance(time.Hour)
	for i := range 4 {
		ok, _, _ := l.allow("192.0.2.1", "")
		require.True(t, ok, "refilled to the burst, request %d", i)
	}
	ok, _, _ = l.allow("192.0.2.1", "")
	assert.False(t, ok, "never above the burst")
}

func TestBucketGlobalPerKey(t *testing.T) {
	l, _ := newTestLimiter(RateLimit{Rate: 100, Burst: 100}, RateLimit{Rate: 1, Burst: 3})
	for i := range 3 {
		ok, _, _ := l.allow(fmt.Sprintf("192.0.2.%d", i+1), "withings")
		require.True(t, ok)
	}
	ok, wait, scope := l.allow("192.0.2.9", "withings")
	require.False(t, ok, "spread over addresses, the key's bucket still runs dry")
	assert.Equal(t, scopeProvider, scope)
	assert.Equal(t, time.Second, wait)

	ok, _, _ = l.allow("192.0.2.9", "oura")
	assert.True(t, ok, "another key has its own bucket")

	// A refusal by the global bucket takes nothing from the address's.
	l2, _ := newTestLimiter(RateLimit{Rate: 0.001, Burst: 1}, RateLimit{Rate: 0.001, Burst: 1})
	ok, _, _ = l2.allow("192.0.2.1", "a")
	require.True(t, ok)
	ok, _, _ = l2.allow("192.0.2.2", "a")
	require.False(t, ok)
	ok, _, _ = l2.allow("192.0.2.2", "b")
	assert.True(t, ok, "the refused request left 192.0.2.2 its token")
}

func TestRetryAfterSeconds(t *testing.T) {
	assert.Equal(t, "1", retryAfter(0))
	assert.Equal(t, "1", retryAfter(10*time.Millisecond))
	assert.Equal(t, "1", retryAfter(time.Second))
	assert.Equal(t, "2", retryAfter(1001*time.Millisecond))
	assert.Equal(t, "50", retryAfter(50*time.Second))
}

func TestBucketEvictionAndCap(t *testing.T) {
	l, clk := newTestLimiter(RateLimit{Rate: 1, Burst: 2}, RateLimit{})
	l.maxAddrs = 3
	for _, a := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		ok, _, _ := l.allow(a, "")
		require.True(t, ok)
	}
	assert.Equal(t, 3, l.tracked())

	// Beyond the cap, new addresses share one bucket.
	ok, _, _ := l.allow("198.51.100.1", "")
	require.True(t, ok)
	ok, _, _ = l.allow("198.51.100.2", "")
	require.True(t, ok)
	ok, _, scope := l.allow("198.51.100.3", "")
	assert.False(t, ok, "the overflow bucket is shared")
	assert.Equal(t, scopeOverflow, scope)
	assert.Equal(t, 3, l.tracked(), "the cap holds")

	// A tracked address keeps its own bucket.
	ok, _, _ = l.allow("192.0.2.1", "")
	assert.True(t, ok)

	// Idle and full buckets go at the next sweep; 192.0.2.1 was used later.
	clk.advance(idleEvict - time.Minute)
	ok, _, _ = l.allow("192.0.2.1", "")
	require.True(t, ok)
	clk.advance(time.Minute + time.Second)
	ok, _, _ = l.allow("192.0.2.4", "")
	require.True(t, ok)
	assert.Equal(t, 2, l.tracked(), "192.0.2.2 and 192.0.2.3 evicted, 192.0.2.4 tracked")

	// A bucket not yet full is kept even when idle.
	slow, clk2 := newTestLimiter(RateLimit{Rate: 0.0001, Burst: 2}, RateLimit{})
	ok, _, _ = slow.allow("192.0.2.1", "")
	require.True(t, ok)
	clk2.advance(idleEvict + sweepEvery)
	slow.allow("192.0.2.2", "")
	assert.Equal(t, 2, slow.tracked())
}

func TestClientAddress(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	cases := []struct {
		name, remote, xff string
		trusted           []netip.Prefix
		want              string
	}{
		{"direct", "192.0.2.10:5555", "", nil, "192.0.2.10"},
		{"untrusted peer forging the header", "192.0.2.10:5555", "203.0.113.1", nil, "192.0.2.10"},
		{"untrusted peer, proxies configured", "192.0.2.10:5555", "203.0.113.1", trusted, "192.0.2.10"},
		{"trusted proxy", "10.0.0.2:80", "203.0.113.1", trusted, "203.0.113.1"},
		{"forged entry left of the real client", "10.0.0.2:80", "198.51.100.66, 203.0.113.1", trusted, "203.0.113.1"},
		{"chain of trusted proxies", "10.0.0.2:80", "203.0.113.1, 10.1.1.1,10.2.2.2", trusted, "203.0.113.1"},
		{"only proxies", "10.0.0.2:80", "10.1.1.1", trusted, "10.1.1.1"},
		{"trusted proxy without the header", "10.0.0.2:80", "", trusted, "10.0.0.2"},
		{"unparsable hop stops the walk", "10.0.0.2:80", "203.0.113.1, junk", trusted, "10.0.0.2"},
		{"hop with a port", "10.0.0.2:80", "203.0.113.1:4711", trusted, "203.0.113.1"},
		{"IPv6 by /64", "[2001:db8:1:2:aaaa::1]:443", "", nil, "2001:db8:1:2::/64"},
		{"IPv6 behind an IPv6 proxy", "[fd00::5]:443", "[2001:db8:1:2::9]:1", trusted, "2001:db8:1:2::/64"},
		{"IPv4-mapped peer", "[::ffff:192.0.2.10]:5555", "", nil, "192.0.2.10"},
		{"not an address", "pipe", "", nil, "pipe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/inbound/x", nil)
			r.RemoteAddr = c.remote
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			assert.Equal(t, c.want, clientAddress(r, c.trusted))
		})
	}
	// Several header lines read as one list.
	r := httptest.NewRequest("POST", "/inbound/x", nil)
	r.RemoteAddr = "10.0.0.2:80"
	r.Header.Add("X-Forwarded-For", "198.51.100.66")
	r.Header.Add("X-Forwarded-For", "203.0.113.1, 10.1.1.1")
	assert.Equal(t, "203.0.113.1", clientAddress(r, trusted))
}

// noPush is a connector that is not push: its inbound routes answer 404
// before any work, enough to see the limiter in front of them.
type noPush struct{}

func (noPush) Slug() string                  { return "noop" }
func (noPush) Enabled(connector.Config) bool { return true }
func (noPush) Map(connector.RawRecord) (model.MapResult, error) {
	return model.MapResult{}, nil
}

func limitedRouter(t *testing.T, d Deps) http.Handler {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	box, err := crypto.NewBox([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	store := tenant.New(pool)
	d.Pool, d.Store = pool, store
	d.Collect = collect.NewHandlers(collect.Deps{
		Pool: pool, Box: box, Tenants: store,
		Lookup: func(slug string) (connector.Connector, bool) { return noPush{}, slug == "noop" },
	})
	return NewRouter(d)
}

func TestRouterRateLimits(t *testing.T) {
	h := limitedRouter(t, Deps{
		Metrics:                  true,
		MetricsToken:             "test-metrics-token-0123",
		RateLimitInbound:         RateLimit{Rate: 0.001, Burst: 2},
		RateLimitInboundProvider: RateLimit{Rate: 1000, Burst: 1000},
		RateLimitOAuth:           RateLimit{Rate: 0.001, Burst: 1},
	})
	do := func(method, target, remote string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(method, target, nil)
		r.RemoteAddr = remote
		h.ServeHTTP(rec, r)
		return rec
	}

	// The probe and the notification route share the inbound bucket.
	assert.Equal(t, http.StatusNotFound, do("GET", "/inbound/noop", "192.0.2.1:1").Code)
	assert.Equal(t, http.StatusNotFound, do("POST", "/inbound/noop", "192.0.2.1:1").Code)
	rec := do("POST", "/inbound/noop", "192.0.2.1:1")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "1000", rec.Header().Get("Retry-After"))
	assert.Equal(t, http.StatusNotFound, do("POST", "/inbound/noop", "192.0.2.2:1").Code, "another address")

	// The callback has its own bucket: 192.0.2.1 still has its token there.
	assert.Equal(t, http.StatusBadRequest, do("GET", "/oauth/noop/callback?code=c&state=", "192.0.2.1:1").Code)
	rec = do("GET", "/oauth/noop/callback?code=c&state=s", "192.0.2.1:1")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotContains(t, rec.Body.String(), "code=c", "the query string is never echoed")

	// Authenticated routes are not limited.
	for _, p := range []string{"/v1/users", "/v1/users/abc/connections"} {
		for range 10 {
			assert.Equal(t, http.StatusUnauthorized, do("GET", p, "192.0.2.1:1").Code, p)
		}
	}

	mrec := httptest.NewRecorder()
	mr := httptest.NewRequest("GET", "/metrics", nil)
	mr.Header.Set("Authorization", "Bearer test-metrics-token-0123")
	h.ServeHTTP(mrec, mr)
	require.Equal(t, http.StatusOK, mrec.Code)
	out := mrec.Body.String()
	assert.Contains(t, out, "# TYPE ohs_rate_limited_total counter\n")
	assert.Contains(t, out, `ohs_rate_limited_total{route_group="inbound"} 1`)
	assert.Contains(t, out, `ohs_rate_limited_total{route_group="oauth_callback"} 1`)
	assert.Contains(t, out, `ohs_http_requests_total{route="POST /inbound/{provider}",code="429"} 1`)
}

func TestRouterRateLimitsOff(t *testing.T) {
	h := limitedRouter(t, Deps{})
	for range 50 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/inbound/noop", strings.NewReader("{}")))
		require.Equal(t, http.StatusNotFound, rec.Code, "zero limits disable the limiter")
	}
}
