// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	apispec "github.com/soma-intelligence/open-health-sync/api"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

func (s *server) mountOps(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.readyz)
	// The OpenAPI description of this build, public like the source. Any
	// origin may read it, so a hosted Swagger UI or Redoc can load it.
	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = w.Write(apispec.OpenAPI)
	})
	if !s.d.Metrics {
		return
	}
	if s.d.MetricsMux != nil {
		// A listener of its own: the operator controls who reaches it by
		// its bind address, so the token is optional there.
		h := http.Handler(http.HandlerFunc(s.metricsHandler))
		if s.d.MetricsToken != "" {
			h = metricsAuth(s.d.MetricsToken, h)
		}
		s.d.MetricsMux.Handle("GET /metrics", h)
		return
	}
	mux.Handle("GET /metrics", metricsAuth(s.d.MetricsToken, http.HandlerFunc(s.metricsHandler)))
}

// metricsAuth lets a scrape through only with the bearer token. With no token
// configured every scrape is refused, so the API listener never serves the
// counters to whoever reaches it.
func metricsAuth(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			problem(w, http.StatusForbidden, "metrics need OHS_METRICS_TOKEN, or OHS_METRICS_LISTEN to serve them on a private address")
			return
		}
		h := r.Header.Get("Authorization")
		ok := len(h) > len("Bearer ") && strings.EqualFold(h[:len("Bearer ")], "Bearer ")
		sum := sha256.Sum256([]byte(strings.TrimSpace(h[min(len(h), len("Bearer ")):])))
		if !ok || subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			problem(w, http.StatusUnauthorized, "metrics need the OHS_METRICS_TOKEN bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.d.Pool.Ping(ctx); err != nil {
		slog.WarnContext(r.Context(), "readyz: database", "error", err)
		problem(w, http.StatusServiceUnavailable, "the database is not reachable")
		return
	}
	if s.d.Raw != nil {
		if err := rawstore.Ping(ctx, s.d.Raw); err != nil {
			slog.WarnContext(r.Context(), "readyz: raw store", "error", err)
			problem(w, http.StatusServiceUnavailable, "the raw store is not reachable")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	var etl map[string]int64
	if s.d.ETLMetrics != nil {
		etl = s.d.ETLMetrics.Snapshot()
	}
	// Read the sync state before writing anything, so a failure leaves the
	// scrape whole without it rather than cut in the middle.
	var sync []collect.ProviderSync
	if s.d.Collect != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		var err error
		sync, err = s.d.Collect.SyncMetrics(ctx)
		cancel()
		if err != nil {
			slog.WarnContext(r.Context(), "metrics: sync state", "error", err)
			sync = nil
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	renderMetrics(w, s.metrics, etl)
	if sync != nil {
		collect.WriteSyncMetrics(w, sync)
	}
}
