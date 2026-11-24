// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	apispec "github.com/soma-intelligence/open-health-sync/api"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/logging"
	"github.com/soma-intelligence/open-health-sync/internal/logutil"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// OpsDeps is what the ops listener of a process serves (OHS_OPS_LISTEN).
type OpsDeps struct {
	// Pool, Raw and Vault are what /readyz checks, as the API's does. Raw
	// and Vault may be nil.
	Pool  *pgxpool.Pool
	Raw   rawstore.Store
	Vault *vault.Vault
	// Stalled returns the background loops of the process that are hung,
	// each as one phrase starting with its name. Nil, /healthz always
	// answers 200.
	Stalled func() []string
	// Metrics writes the process's metrics in the Prometheus text format.
	// Nil leaves /metrics out (OHS_METRICS=false).
	Metrics func(w io.Writer)
}

// NewOpsHandler serves /healthz, /readyz and /metrics for a process, the
// workers without the api role first: nothing else, and no credential, so
// the operator decides who reaches it by its bind address. The answers have
// the shapes of the API's. /healthz is the liveness of the process's loops:
// 503 naming the hung ones, where the API's only says the server answers.
// /readyz is the API's check: database, schema version, raw store, vault.
func NewOpsHandler(d OpsDeps) http.Handler {
	if d.Pool == nil {
		panic("api: NewOpsHandler needs Pool")
	}
	s := &server{d: Deps{Pool: d.Pool, Raw: d.Raw, Vault: d.Vault}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if d.Stalled != nil {
			if stalled := d.Stalled(); len(stalled) > 0 {
				detail := "background loops stalled: " + strings.Join(stalled, "; ")
				logging.Ctx(r.Context()).Warn().Str("detail", detail).Msg("healthz_loops_stalled")
				problem(w, http.StatusServiceUnavailable, detail)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.readyz)
	if d.Metrics != nil {
		mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			d.Metrics(w)
		})
	}
	return mux
}

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
		logutil.ErrCtx(r.Context(), *logging.Ctx(r.Context()), err, zerolog.WarnLevel).Msg("readyz_database_failed")
		problem(w, http.StatusServiceUnavailable, "the database is not reachable")
		return
	}
	// /readyz takes no credential: the versions and the dirty mark go to
	// the log, once per change, and the caller gets a reason without them.
	detail, changed := s.schema.check(ctx, time.Now(), db.LatestVersion, s.schemaVersion)
	switch {
	case changed && detail != "":
		logging.Ctx(r.Context()).Warn().Str("detail", detail).Msg("readyz_schema_behind")
	case changed:
		logging.Ctx(r.Context()).Info().Msg("readyz_schema_current")
	}
	if detail != "" {
		problem(w, http.StatusServiceUnavailable, schemaNotReady)
		return
	}
	if s.d.Raw != nil {
		if err := rawstore.Ping(ctx, s.d.Raw); err != nil {
			logutil.ErrCtx(r.Context(), *logging.Ctx(r.Context()), err, zerolog.WarnLevel).Msg("readyz_raw_store_failed")
			problem(w, http.StatusServiceUnavailable, "the raw store is not reachable")
			return
		}
	}
	if s.d.Vault != nil {
		if err := s.d.Vault.Ready(ctx); err != nil {
			logutil.ErrCtx(r.Context(), *logging.Ctx(r.Context()), err, zerolog.WarnLevel).Msg("readyz_vault_failed")
			problem(w, http.StatusServiceUnavailable, "the key provider does not open keys")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// schemaCheckTTL is how long /readyz trusts its last schema version check:
// probes come every few seconds, the version changes with a migration.
const schemaCheckTTL = 10 * time.Second

// schemaNotReady is what /readyz answers when the schema check fails,
// whatever the reason: the route is public, so no version and no word on a
// half done migration.
const schemaNotReady = "database schema is not at the version this binary needs"

// schemaCheck caches whether the database schema is the one this binary was
// built for, and remembers the last answer it reported as new.
type schemaCheck struct {
	mu     sync.Mutex
	at     time.Time
	detail string
	done   bool
	// told is the detail of the last check that returned changed.
	told string
}

// check returns "" when the database is clean and at the binary's schema
// version or a later one, else why the instance is not ready, for the log.
// The answer is kept for schemaCheckTTL, so a probe costs no query most of
// the time. changed is true the first time a detail is returned and when it
// becomes another one or "" again, so the caller logs a state once, not on
// every probe. binary is the version this build migrates to, database reads
// the applied one.
func (c *schemaCheck) check(ctx context.Context, now time.Time, binary func() (uint, error),
	database func(context.Context) (uint, bool, error)) (detail string, changed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() {
		changed = detail != c.told
		c.told = detail
	}()
	if c.done && now.Sub(c.at) < schemaCheckTTL {
		return c.detail, false
	}
	want, err := binary()
	if err != nil {
		return "the schema version of this binary could not be read", false
	}
	got, dirty, err := database(ctx)
	switch {
	case err != nil:
		// Not kept: the next probe asks again.
		return "the database schema version could not be read: " + err.Error(), false
	case dirty:
		c.detail = fmt.Sprintf("the database schema is dirty: a migration to version %d stopped half way", got)
	case got < want:
		// A database ahead of the binary stays ready: during a rolling
		// upgrade the migration lands first and the old replicas must keep
		// serving until the new ones take over.
		c.detail = fmt.Sprintf("the database is at schema version %d, this binary needs version %d", got, want)
	default:
		c.detail = ""
	}
	c.at, c.done = now, true
	return c.detail, false
}

// schemaVersion reads the applied migration version with one plain query,
// as db.ReadSchemaState does: no lock, nothing created. A database that was
// never migrated is version 0.
func (s *server) schemaVersion(ctx context.Context) (version uint, dirty bool, err error) {
	var v int64
	err = s.d.Pool.QueryRow(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&v, &dirty)
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.As(err, &pgErr) && pgErr.Code == "42P01":
		return 0, false, nil
	case err != nil:
		return 0, false, err
	}
	if v > 0 {
		version = uint(v)
	}
	return version, dirty, nil
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
			logutil.ErrCtx(r.Context(), *logging.Ctx(r.Context()), err, zerolog.WarnLevel).Msg("metrics_sync_state_failed")
			sync = nil
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	renderMetrics(w, s.metrics, etl)
	if sync != nil {
		collect.WriteSyncMetrics(w, sync)
	}
	if s.d.Vault != nil {
		s.d.Vault.Metrics().Write(w)
	}
}
