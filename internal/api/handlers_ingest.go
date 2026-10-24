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

// DefaultIngestTenantLimit is the share of n upload slots one tenant may
// hold when IngestTenantLimit is unset: half, rounded up, so one tenant
// cannot hold them all.
func DefaultIngestTenantLimit(n int) int {
	return max(1, (n+1)/2)
}

// ingestLimiter lets at most cap(slots) uploads through at once across every
// handler it wraps, and at most perTenant of them for one tenant. One upload
// can hold 64 MiB of body plus its decompression buffers, so the limit
// bounds memory; the tenant share keeps one tenant (a gzip bomb, a large
// import) from refusing every other tenant's uploads. A request over either
// limit is refused at once with 503 and Retry-After; it is never queued.
type ingestLimiter struct {
	slots     chan struct{}
	perTenant int
	m         *httpMetrics

	mu   sync.Mutex
	held map[uuid.UUID]int
}

func newIngestLimiter(n, perTenant int, m *httpMetrics) *ingestLimiter {
	return &ingestLimiter{slots: make(chan struct{}, n), perTenant: perTenant, m: m, held: map[uuid.UUID]int{}}
}

// limitInFlight lets at most n requests through at once, with no tenant
// share.
func limitInFlight(n int, m *httpMetrics, next http.Handler) http.Handler {
	return newIngestLimiter(n, n, m).wrap(next)
}

// takeTenant reserves one of the tenant's share, false when it is used up.
func (l *ingestLimiter) takeTenant(tenant uuid.UUID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[tenant] >= l.perTenant {
		return false
	}
	l.held[tenant]++
	return true
}

func (l *ingestLimiter) releaseTenant(tenant uuid.UUID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[tenant]--; l.held[tenant] <= 0 {
		delete(l.held, tenant)
	}
}

func (l *ingestLimiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, shared := auth.From(r.Context())
		shared = shared && l.perTenant < cap(l.slots)
		if shared && !l.takeTenant(p.TenantID) {
			l.refuse(w, "this tenant already has "+strconv.Itoa(l.perTenant)+" uploads in progress, retry later")
			return
		}
		select {
		case l.slots <- struct{}{}:
		default:
			if shared {
				l.releaseTenant(p.TenantID)
			}
			l.refuse(w, "too many uploads in progress, retry later")
			return
		}
		l.m.ingestInFlight.Add(1)
		defer func() {
			l.m.ingestInFlight.Add(-1)
			<-l.slots
			if shared {
				l.releaseTenant(p.TenantID)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (l *ingestLimiter) refuse(w http.ResponseWriter, detail string) {
	l.m.ingestRejected.Add(1)
	w.Header().Set("Retry-After", ingestRetryAfter)
	problem(w, http.StatusServiceUnavailable, detail)
}
