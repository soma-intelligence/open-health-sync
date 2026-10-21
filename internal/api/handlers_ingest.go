// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"

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
	// One limiter for every device route: the limit bounds the memory of the
	// process, whichever store the uploads come from.
	limit := newInFlightLimiter(n, s.metrics)
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
			Allowed: s.providerAllowed})
		// Authenticate first, so an anonymous caller gets 401 and never a slot.
		mux.Handle(path, auth.Middleware(s.lookup, auth.UserUpload)(limit(upload)))
	}
}

// newInFlightLimiter returns a wrapper that lets at most n requests through
// at once across every handler it wraps.
func newInFlightLimiter(n int, m *httpMetrics) func(http.Handler) http.Handler {
	slots := make(chan struct{}, n)
	return func(next http.Handler) http.Handler { return limitSlots(slots, m, next) }
}

// limitInFlight lets at most n requests through at once. One upload can hold
// 64 MiB of body plus its decompression buffers, so the limit bounds memory.
// A request over the limit is refused at once with 503 and Retry-After; it is
// never queued.
func limitInFlight(n int, m *httpMetrics, next http.Handler) http.Handler {
	return limitSlots(make(chan struct{}, n), m, next)
}

// limitSlots is limitInFlight over a shared set of slots.
func limitSlots(slots chan struct{}, m *httpMetrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
		default:
			m.ingestRejected.Add(1)
			w.Header().Set("Retry-After", ingestRetryAfter)
			problem(w, http.StatusServiceUnavailable, "too many uploads in progress, retry later")
			return
		}
		m.ingestInFlight.Add(1)
		defer func() {
			m.ingestInFlight.Add(-1)
			<-slots
		}()
		next.ServeHTTP(w, r)
	})
}
