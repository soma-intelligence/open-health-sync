// SPDX-License-Identifier: AGPL-3.0-only

// Package app wires the roles of one ohs process (api, collect, etl) from the
// configuration and runs them until the context ends.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	// Every connector registers itself.
	_ "github.com/soma-intelligence/open-health-sync/connectors/all"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/connectors/healthconnect"
	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/consent"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/deletion"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/etl/automation"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/tracing"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// Process tuning.
const (
	// ETLConcurrency is how many jobs each ETL tier runs at once.
	ETLConcurrency = 4
	// ShutdownGrace bounds shutdown: the HTTP server waits this long for
	// requests in flight and the ETL, deletion and pull workers for their
	// jobs, then cancels them. It stays under the usual 30 s container stop
	// grace period.
	ShutdownGrace = 25 * time.Second
	// shutdownMargin is how much longer Run waits for the roles after
	// ShutdownGrace before it gives up on them (a call ignoring its context).
	shutdownMargin = 3 * time.Second
	// tracingFlush bounds the export of the last spans at exit.
	tracingFlush      = 5 * time.Second
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 2 * time.Minute
)

// Run runs the roles cfg selects until ctx is done (the caller cancels it on
// SIGINT or SIGTERM), then shuts down gracefully: the HTTP server stops
// accepting and the ETL, deletion and pull workers stop claiming;
// requests and jobs in flight get ShutdownGrace to finish, then are cancelled
// (an unsettled job claim is reclaimed later). Run returns at most ShutdownGrace plus a small margin
// after ctx ends, and closes the database pool last. It returns the first
// error that stopped a role early.
func Run(ctx context.Context, cfg config.Config) error {
	return run(ctx, cfg, os.Stderr)
}

// Version is logged at start (set by ohs serve).
var Version = "dev"

// newLogger is the process logger: JSON lines, durations as text ("10m0s")
// rather than nanoseconds, and the request id and trace id of the context on
// each line.
func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(tracing.LogHandler(reqid.Handler(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Value.Kind() == slog.KindDuration {
				a.Value = slog.StringValue(a.Value.Duration().String())
			}
			return a
		},
	}))))
}

// options are the knobs tests turn.
type options struct {
	grace, margin time.Duration
	// wrapRaw, when set, wraps the raw store every role uses.
	wrapRaw func(rawstore.Store) rawstore.Store
}

func run(ctx context.Context, cfg config.Config, logOut io.Writer) error {
	return runWith(ctx, cfg, logOut, options{grace: ShutdownGrace, margin: shutdownMargin})
}

func runWith(ctx context.Context, cfg config.Config, logOut io.Writer, opts options) error {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return fmt.Errorf("OHS_LOG_LEVEL: %q is not one of debug, info, warn, error", cfg.LogLevel)
	}
	logger := newLogger(logOut, level)
	slog.SetDefault(logger)

	providers, err := configureProviders(cfg.Providers)
	if err != nil {
		return err
	}
	box, err := crypto.NewBox(cfg.EncryptionKey)
	if err != nil {
		return fmt.Errorf("OHS_ENCRYPTION_KEY: %w", err)
	}

	// The raw store first: it is local and cheap to check, and an unwritable
	// one would otherwise only show as failed uploads.
	raw, err := rawstore.Open(cfg.RawStore)
	if err != nil {
		hint := ""
		if errors.Is(err, fs.ErrPermission) {
			hint = writeHint(cfg.RawStore)
		}
		return fmt.Errorf("OHS_RAW_STORE: %w%s", err, hint)
	}
	if err := rawstore.CheckWritable(ctx, raw); err != nil {
		return fmt.Errorf("OHS_RAW_STORE: %w%s", err, writeHint(cfg.RawStore))
	}
	if opts.wrapRaw != nil {
		raw = opts.wrapRaw(raw)
	}

	// Tracing before the pool, so its queries are traced from the start.
	shutdownTracing, err := tracing.Setup(ctx, tracing.Options{Enabled: cfg.Tracing, Protocol: cfg.TracingProtocol, Version: Version})
	if err != nil {
		return err
	}
	defer func() {
		// Flush the spans still buffered; a collector that is down must not
		// hold the exit up for long.
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), tracingFlush)
		defer scancel()
		if err := shutdownTracing(sctx); err != nil {
			slog.Warn("tracing: flush at shutdown", "error", err)
		}
	}()
	var dbOpts []db.Option
	if cfg.Tracing {
		dbOpts = append(dbOpts, db.WithTracer(tracing.PGX{}))
		slog.Info("tracing on: exporting OpenTelemetry traces over OTLP")
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL, dbOpts...)
	if err != nil {
		return fmt.Errorf("cannot reach the database of OHS_DATABASE_URL (is Postgres up, are host, port and password right?): %w", err)
	}
	abandoned := false
	defer func() {
		if abandoned {
			// A role still holds a connection: Close would wait for it.
			go pool.Close()
			return
		}
		pool.Close()
	}()
	store := tenant.New(pool)
	collectDeps := collect.Deps{
		Pool: pool, Raw: raw, Box: box, Tenants: store,
		Lookup:    providers.lookup,
		PublicURL: cfg.PublicURL,
		Slugs:     providers.slugs,
	}

	if cfg.BootstrapTenant != "" {
		if err := bootstrap(ctx, store, cfg.BootstrapTenant); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	fail := func(role string, err error) {
		if err == nil {
			return
		}
		errOnce.Do(func() { firstErr = fmt.Errorf("%s: %w", role, err) })
		cancel()
	}
	goRole := func(role string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fail(role, fn())
		}()
	}

	etlMetrics := &automation.Metrics{}
	if cfg.Roles["etl"] {
		q := queue.NewETL(pool)
		for _, tier := range []string{queue.TierStandard, queue.TierHighFreq} {
			w := etl.NewWorkerWith(pool, raw, q, tier, ETLConcurrency, etl.WithShutdownGrace(opts.grace),
				etl.WithConsentRequired(cfg.ConsentRequired))
			goRole("etl "+tier, func() error { return w.Run(ctx) })
		}
		// Provider grants are revoked best effort before the user's rows go.
		// A deletion cut short by shutdown stays pending for the next start.
		dw := deletion.NewWorker(pool, raw, collectDeps).WithShutdownGrace(opts.grace)
		goRole("deletion", func() error { return dw.Run(ctx) })
		// Records of a category whose consent was revoked are purged here.
		cp := consent.NewPurger(pool)
		goRole("consent purge", func() error { return cp.Run(ctx) })
		// Outgoing webhooks: deliveries are written with the change they
		// announce, posted from here.
		ww := webhook.NewWorker(pool, box, webhook.WorkerConfig{AllowPrivate: cfg.WebhookAllowPrivate})
		goRole("webhooks", func() error { return ww.Run(ctx) })
		goRole("automation", func() error {
			return automation.Start(ctx, automation.Deps{
				Pool: pool, Store: raw, RetentionDays: cfg.RetentionDays, Metrics: etlMetrics,
				AuditRetentionDays: cfg.AuditRetentionDays,
			})
		})
	}

	if cfg.Roles["collect"] {
		sch := collect.NewScheduler(collectDeps, collect.SchedulerConfig{
			ShutdownGrace: opts.grace,
			PerProvider:   cfg.PullsPerProvider, BackfillPerProvider: cfg.BackfillsPerProvider,
		})
		goRole("collect", func() error { return sch.Run(ctx) })
	}

	if cfg.Roles["api"] {
		deps := api.Deps{
			Pool:      pool,
			Store:     store,
			Raw:       raw,
			Providers: providers.list,
			Metrics:   cfg.Metrics,
			Console:   cfg.Console,
			Collect:   collect.NewHandlers(collectDeps),
			PublicURL: cfg.PublicURL,
			Webhooks:  webhook.NewStore(pool, box),
			// WebhookAllowPrivate relaxes the endpoint URL checks.
			WebhookAllowPrivate: cfg.WebhookAllowPrivate,
			// Zero (a Config not built by Load) means the api default.
			IngestLimit: cfg.IngestConcurrency,
			Version:     Version,
			// Zero (a Config not built by Load) means no rate limit.
			RateLimitInbound:         api.RateLimit(cfg.RateLimitInbound),
			RateLimitInboundProvider: api.RateLimit(cfg.RateLimitInboundProvider),
			RateLimitOAuth:           api.RateLimit(cfg.RateLimitOAuth),
			TrustedProxies:           cfg.TrustedProxies,
		}
		if cfg.Roles["etl"] {
			deps.ETLMetrics = etlMetrics
		}
		if c, ok := providers.lookup(applehealth.Slug); ok {
			deps.Device, _ = connector.AsDevice(c)
		}
		if c, ok := providers.lookup(healthconnect.Slug); ok {
			deps.HealthConnect, _ = connector.AsDevice(c)
		}
		if cfg.Metrics && cfg.MetricsListen != "" {
			deps.MetricsMux = http.NewServeMux()
		}
		deps.MetricsToken = cfg.MetricsToken
		if cfg.Metrics && cfg.MetricsListen == "" && cfg.MetricsToken == "" {
			slog.Info("metrics: /metrics refuses scrapes until OHS_METRICS_TOKEN or OHS_METRICS_LISTEN is set")
		}
		srv := &http.Server{
			Addr:              cfg.Listen,
			Handler:           api.NewRouter(deps),
			ReadHeaderTimeout: readHeaderTimeout,
			IdleTimeout:       idleTimeout,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		}
		// Started first: its shutdown goroutine stops it if the API listener fails.
		if deps.MetricsMux != nil {
			if err := serveMetrics(ctx, cfg.MetricsListen, deps.MetricsMux, opts.grace, logger, goRole); err != nil {
				cancel()
				wg.Wait()
				return err
			}
		}
		ln, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			cancel()
			wg.Wait()
			return fmt.Errorf("api: %w%s", err, listenHint(err, cfg.Listen))
		}
		slog.Info("api listening", "addr", ln.Addr().String())
		goRole("api", func() error {
			if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		})
		goRole("api shutdown", func() error {
			<-ctx.Done()
			sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), opts.grace)
			defer scancel()
			if err := srv.Shutdown(sctx); err != nil {
				_ = srv.Close()
				return fmt.Errorf("graceful shutdown: %w", err)
			}
			return nil
		})
	}

	slog.Info("ohs started", "version", Version, "roles", roleList(cfg.Roles))
	stopped := make(chan struct{})
	go func() {
		wg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-ctx.Done():
		// Every role bounds its own drain by the grace; the margin covers a
		// call that ignores its context.
		t := time.NewTimer(opts.grace + opts.margin)
		defer t.Stop()
		select {
		case <-stopped:
		case <-t.C:
			abandoned = true
			slog.Error("shutdown grace exceeded, exiting with work still running", "grace", opts.grace)
		}
	}
	slog.Info("ohs stopped")
	errOnce.Do(func() {}) // firstErr is final from here on
	return firstErr
}

// serveMetrics serves /metrics on an address of its own (OHS_METRICS_LISTEN),
// stopped with the process.
func serveMetrics(ctx context.Context, addr string, mux *http.ServeMux, grace time.Duration,
	logger *slog.Logger, goRole func(string, func() error)) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("metrics: %w (OHS_METRICS_LISTEN)", err)
	}
	slog.Info("metrics listening", "addr", ln.Addr().String())
	goRole("metrics", func() error {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	goRole("metrics shutdown", func() error {
		<-ctx.Done()
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
		defer scancel()
		if err := srv.Shutdown(sctx); err != nil {
			_ = srv.Close()
		}
		return nil
	})
	return nil
}

// enabledProviders are the connectors enabled on this instance.
type enabledProviders struct {
	list   []connector.Connector
	slugs  []string
	bySlug map[string]connector.Connector
}

// lookup only answers for enabled connectors.
func (p enabledProviders) lookup(slug string) (connector.Connector, bool) {
	c, ok := p.bySlug[slug]
	return c, ok
}

// configureProviders lists the enabled connectors and hands each one that
// takes keys its OHS_<PROVIDER>_* values. A refused configuration stops
// startup. It logs the enabled slugs, and warns about a provider given some
// of its keys but not all (naming the missing variables, never a value).
func configureProviders(keys map[string]config.ProviderKeys) (enabledProviders, error) {
	for _, c := range connector.All() {
		k, ok := c.(connector.KeyedConnector)
		if !ok {
			continue
		}
		var missing []string
		for _, f := range k.RequiredKeys() {
			if keys[c.Slug()][f] == "" {
				missing = append(missing, "OHS_"+strings.ToUpper(c.Slug())+"_"+f)
			}
		}
		if len(missing) > 0 && len(missing) < len(k.RequiredKeys()) {
			slog.Warn("provider partially configured, left disabled", "provider", c.Slug(),
				"missing", strings.Join(missing, ","))
		}
	}
	p := enabledProviders{list: connector.Enabled(keys), bySlug: map[string]connector.Connector{}}
	for _, c := range p.list {
		slug := c.Slug()
		if cc, ok := c.(connector.Configurable); ok {
			if err := cc.Configure(keys[slug]); err != nil {
				return enabledProviders{}, fmt.Errorf("provider %s: configuration refused: %w", slug, err)
			}
		}
		p.slugs = append(p.slugs, slug)
		p.bySlug[slug] = c
	}
	slog.Info("providers enabled", "providers", strings.Join(p.slugs, ","))
	return p, nil
}

// bootstrap creates the OHS_BOOTSTRAP_TENANT tenant on first start and logs
// its key once. This is the only place an API key is ever logged.
func bootstrap(ctx context.Context, store *tenant.Store, name string) error {
	t, key, err := store.EnsureTenant(ctx, name)
	if err != nil {
		return fmt.Errorf("bootstrap tenant: %w", err)
	}
	if key == "" {
		slog.Info("bootstrap tenant present", "tenant_id", t.ID, "name", t.Name)
		return nil
	}
	slog.Warn("bootstrap tenant created: store this API key now, it is shown only this once",
		"tenant_id", t.ID, "name", t.Name, "api_key", key)
	return nil
}

// listenHint says what to change when the listen address is taken.
func listenHint(err error, addr string) string {
	if errors.Is(err, syscall.EADDRINUSE) {
		return fmt.Sprintf(" (another process holds %s: stop it or set OHS_LISTEN; with docker compose set OHS_HTTP_PORT and OHS_PUBLIC_URL)", addr)
	}
	return ""
}

func roleList(roles map[string]bool) string {
	var out []string
	for _, r := range []string{"api", "collect", "etl"} {
		if roles[r] {
			out = append(out, r)
		}
	}
	return strings.Join(out, ",")
}
