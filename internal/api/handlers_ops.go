// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	apispec "github.com/soma-intelligence/open-health-sync/api"
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
	if s.d.Metrics {
		mux.HandleFunc("GET /metrics", s.metricsHandler)
	}
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
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	renderMetrics(w, s.metrics, etl)
}
