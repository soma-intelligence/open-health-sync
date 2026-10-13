// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// httpMetrics counts requests by matched route pattern and status code, and
// tracks the ingest limiter and the rate limiter. Route patterns and route
// groups are fixed sets, so the label cardinality is bounded.
type httpMetrics struct {
	mu       sync.Mutex
	requests map[routeCode]int64

	ingestInFlight atomic.Int64
	ingestRejected atomic.Int64
	auditFailures  atomic.Int64

	// rateLimited counts the requests refused with 429, by route group.
	rateLimited map[string]*atomic.Int64
}

type routeCode struct {
	route string
	code  int
}

func newHTTPMetrics() *httpMetrics {
	return &httpMetrics{
		requests: map[routeCode]int64{},
		rateLimited: map[string]*atomic.Int64{
			groupInbound:       new(atomic.Int64),
			groupOAuthCallback: new(atomic.Int64),
		},
	}
}

func (m *httpMetrics) count(route string, code int) {
	if route == "" {
		route = "unmatched"
	}
	m.mu.Lock()
	m.requests[routeCode{route, code}]++
	m.mu.Unlock()
}

// statusWriter records the status code. Unwrap keeps http.ResponseController
// working for the handlers behind it.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// renderMetrics writes the Prometheus text exposition format (version 0.0.4).
// etl holds the automation snapshot; a name ending in _total is a counter,
// anything else a gauge.
func renderMetrics(w io.Writer, m *httpMetrics, etl map[string]int64) {
	m.mu.Lock()
	keys := make([]routeCode, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	counts := make(map[routeCode]int64, len(keys))
	for k, v := range m.requests {
		counts[k] = v
	}
	m.mu.Unlock()
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return keys[i].code < keys[j].code
	})

	fmt.Fprint(w, "# HELP ohs_http_requests_total HTTP requests by route pattern and status code.\n")
	fmt.Fprint(w, "# TYPE ohs_http_requests_total counter\n")
	for _, k := range keys {
		fmt.Fprintf(w, "ohs_http_requests_total{route=\"%s\",code=\"%d\"} %d\n", escapeLabel(k.route), k.code, counts[k])
	}
	fmt.Fprint(w, "# HELP ohs_ingest_in_flight Device uploads being processed now.\n")
	fmt.Fprint(w, "# TYPE ohs_ingest_in_flight gauge\n")
	fmt.Fprintf(w, "ohs_ingest_in_flight %d\n", m.ingestInFlight.Load())
	fmt.Fprint(w, "# HELP ohs_ingest_rejected_total Device uploads refused with 503 because the limit was reached.\n")
	fmt.Fprint(w, "# TYPE ohs_ingest_rejected_total counter\n")
	fmt.Fprintf(w, "ohs_ingest_rejected_total %d\n", m.ingestRejected.Load())
	fmt.Fprint(w, "# HELP ohs_audit_write_failures_total Audit events that could not be written.\n")
	fmt.Fprint(w, "# TYPE ohs_audit_write_failures_total counter\n")
	fmt.Fprintf(w, "ohs_audit_write_failures_total %d\n", m.auditFailures.Load())
	fmt.Fprint(w, "# HELP ohs_rate_limited_total Provider-facing requests refused with 429 by the rate limiter.\n")
	fmt.Fprint(w, "# TYPE ohs_rate_limited_total counter\n")
	for _, g := range []string{groupInbound, groupOAuthCallback} {
		fmt.Fprintf(w, "ohs_rate_limited_total{route_group=\"%s\"} %d\n", g, m.rateLimited[g].Load())
	}

	names := make([]string, 0, len(etl))
	for n := range etl {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		typ := "gauge"
		if strings.HasSuffix(n, "_total") {
			typ = "counter"
		}
		fmt.Fprintf(w, "# TYPE ohs_etl_%s %s\nohs_etl_%s %d\n", n, typ, n, etl[n])
	}
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(s string) string { return labelEscaper.Replace(s) }
