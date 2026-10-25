// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"strconv"
	"sync"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// ingestRetryAfter is the Retry-After (seconds) of a refused upload.
const ingestRetryAfter = "5"

func (s *server) mountIngest(mux *http.ServeMux) {
	n := s.d.IngestLimit
	if n <= 0 {
		n = DefaultIngestLimit
	}
	perTenant := s.d.IngestTenantLimit
	if perTenant <= 0 {
		perTenant = DefaultIngestTenantLimit(n)
	}
	// One limiter for every device route: the limit bounds the memory of the
	// process, whichever store the uploads come from.
	limit := newIngestLimiter(n, perTenant, s.metrics)
	for path, dev := range map[string]connector.DeviceConnector{
		"POST /v1/ingest/apple-health":   s.d.Device,
		"POST /v1/ingest/health-connect": s.d.HealthConnect,
		"POST /v1/ingest/xiaomi":         s.d.Xiaomi,
		"POST /v1/ingest/activity-files": s.d.ActivityFiles,
		"POST /v1/ingest/bluetooth":      s.d.Bluetooth,
	} {
		if dev == nil {
			continue
		}
		upload := collect.NewUploadHandler(collect.UploadDeps{Pool: s.d.Pool, Raw: s.d.Raw, Device: dev,
			Allowed: s.providerAllowed, MaxDecompressedBytes: s.d.IngestMaxDecompressedBytes})
		// Authenticate first, so an anonymous caller gets 401 and never a
		// slot, and the slot is counted against the caller's tenant.
		mux.Handle(path, auth.Middleware(s.lookup, auth.UserUpload)(limit.wrap(upload)))
	}
}

// DefaultIngestTenantLimit is the share of n upload slots one tenant is
// sure to get when IngestTenantLimit is unset: half, rounded up.
func DefaultIngestTenantLimit(n int) int {
	return max(1, (n+1)/2)
}

// ingestReserve is how many of n slots a tenant past its share leaves free
// for the other tenants: one per 8 slots, at least one.
func ingestReserve(n int) int {
	return max(1, n/8)
}

// ingestLimiter lets at most n uploads through at once across every handler
// it wraps. One upload can hold 64 MiB of body plus its decompression
// buffers, so the limit bounds memory. It is work conserving: a tenant takes
// any free slot up to its share (perTenant), and past it only while more
// than the reserve stays free, so a lone tenant uses every slot but the
// reserve, and one tenant (a gzip bomb, a large import) never refuses the
// others' uploads. A request over a limit is refused at once with 503 and
// Retry-After; it is never queued.
type ingestLimiter struct {
	n, perTenant, reserve int
	m                     *httpMetrics

	mu    sync.Mutex
	inUse int
	held  map[uuid.UUID]int
}

func newIngestLimiter(n, perTenant int, m *httpMetrics) *ingestLimiter {
	return &ingestLimiter{n: n, perTenant: perTenant, reserve: ingestReserve(n), m: m, held: map[uuid.UUID]int{}}
}

// limitInFlight lets at most n requests through at once, with no tenant
// share.
func limitInFlight(n int, m *httpMetrics, next http.Handler) http.Handler {
	return newIngestLimiter(n, n, m).wrap(next)
}

// take reserves a slot for tenant (shared false: no tenant accounting). It
// returns the refusal's detail, empty when the slot is taken.
func (l *ingestLimiter) take(tenant uuid.UUID, shared bool) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.inUse >= l.n:
		return "too many uploads in progress, retry later"
	case shared && l.held[tenant] >= l.perTenant && l.inUse >= l.n-l.reserve:
		return "this tenant already has " + strconv.Itoa(l.held[tenant]) +
			" uploads in progress and the last slots are kept for other tenants, retry later"
	}
	l.inUse++
	if shared {
		l.held[tenant]++
	}
	return ""
}

func (l *ingestLimiter) release(tenant uuid.UUID, shared bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inUse--
	if shared {
		if l.held[tenant]--; l.held[tenant] <= 0 {
			delete(l.held, tenant)
		}
	}
}

func (l *ingestLimiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, shared := auth.From(r.Context())
		shared = shared && l.perTenant < l.n
		if detail := l.take(p.TenantID, shared); detail != "" {
			l.m.ingestRejected.Add(1)
			w.Header().Set("Retry-After", ingestRetryAfter)
			problem(w, http.StatusServiceUnavailable, detail)
			return
		}
		l.m.ingestInFlight.Add(1)
		defer func() {
			l.m.ingestInFlight.Add(-1)
			l.release(p.TenantID, shared)
		}()
		next.ServeHTTP(w, r)
	})
}
