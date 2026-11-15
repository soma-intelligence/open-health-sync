// SPDX-License-Identifier: AGPL-3.0-only

// Package app wires the roles of one ohs process (api, collect, etl) from the
// configuration and runs them until the context ends.
package app

import (
	"cmp"
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	// Every connector registers itself.
	_ "github.com/soma-intelligence/open-health-sync/connectors/all"

	"github.com/soma-intelligence/open-health-sync/connectors/activityfiles"
	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/connectors/bluetooth"
	"github.com/soma-intelligence/open-health-sync/connectors/healthconnect"
	"github.com/soma-intelligence/open-health-sync/connectors/labs"
	"github.com/soma-intelligence/open-health-sync/connectors/selfreport"
	"github.com/soma-intelligence/open-health-sync/connectors/xiaomi"
	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/consent"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/deletion"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/etl/automation"
	"github.com/soma-intelligence/open-health-sync/internal/memlimit"
	"github.com/soma-intelligence/open-health-sync/internal/ops"
	"github.com/soma-intelligence/open-health-sync/internal/providerset"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/tenantkeys"
	"github.com/soma-intelligence/open-health-sync/internal/tenantproviders"
	"github.com/soma-intelligence/open-health-sync/internal/tracing"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// Process tuning.
const (
	// ETLConcurrency is how many jobs each ETL tier runs at once, unless
	// OHS_ETL_CONCURRENCY says otherwise.
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
	tracingFlush = 5 * time.Second
	// auditFlush bounds the write of the audit events still queued once the
	// HTTP server has stopped.
	auditFlush        = 10 * time.Second
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
func Run(ctx context.Context, cfg config.Config, start settings.Start) error {
	return runWith(ctx, cfg, os.Stderr, options{grace: ShutdownGrace, margin: shutdownMargin, memLimit: true,
		start: start, lookup: os.LookupEnv, environ: os.Environ()})
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
	// memLimit sets the runtime's soft memory limit from the container's
	// when GOMEMLIMIT is unset (the process, not a test).
	memLimit bool
	// noMinStall holds every loop to three of its own intervals, without
	// the one minute floor (ops.MinStall); jobLimit, when set, is how long a
	// claimed ETL job excuses its loop, in place of jobBusyLimit.
	noMinStall bool
	jobLimit   time.Duration
	// start is what Boot learned about the stored settings; lookup and
	// environ are the environment the configuration was read from. Nil
	// lookup and environ mean os.LookupEnv and os.Environ().
	start   settings.Start
	lookup  func(string) (string, bool)
	environ []string
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
	if opts.memLimit {
		switch n, src := memlimit.Apply(os.LookupEnv, memlimit.DefaultCgroupRoot); src {
		case memlimit.Cgroup:
			logger.Info("memory limit set from the container's", "gomemlimit_bytes", n,
				"fraction", memlimit.Fraction)
		case memlimit.Env:
			logger.Info("memory limit set by GOMEMLIMIT", "gomemlimit_bytes", n)
		}
	}

	// Seals with OHS_ENCRYPTION_KEY, still opens what the previous keys sealed.
	box, err := crypto.NewKeyring(cfg.EncryptionKey, cfg.PreviousEncryptionKeys...)
	if err != nil {
		return fmt.Errorf("OHS_ENCRYPTION_KEY, OHS_PREVIOUS_ENCRYPTION_KEYS: %w", err)
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
	// The vault: a process that runs api or etl opens keys. One that runs
	// collect alone seals to public keys only and never calls the key
	// provider, so it does not even open it.
	canOpen := cfg.Roles["api"] || cfg.Roles["etl"]
	var keyProvider vault.KeyProvider
	if canOpen {
		if keyProvider, err = vault.OpenProvider(ctx, cfg.Vault); err != nil {
			return fmt.Errorf("OHS_VAULT_KEY_PROVIDER: %w", err)
		}
	}
	keyVault := vault.New(pool, keyProvider, vault.Options{
		CanOpen: canOpen, CacheUsers: cfg.Vault.CacheUsers, CacheTTL: cfg.Vault.CacheTTL,
	})
	store := tenant.New(pool)
	if canOpen {
		// Users are created with their keys.
		store = store.WithVault(keyVault)
	}
	// The enabled connectors: the environment's keys, then what the console
	// saved. Every role reads the set through Lookup, so a provider turned on
	// or off applies without a restart.
	providers, err := providerset.New(ctx, providerset.Options{Env: cfg.Providers, Store: providerset.NewPG(pool, box)})
	if err != nil {
		return err
	}
	// The providers an instance admin turned off for one tenant, read the
	// same way: every role asks Allowed, which does no I/O.
	tenantProviders := tenantproviders.New(ctx, tenantproviders.NewPG(pool))
	// The provider keys tenants brought for themselves, read the same way.
	// Removing them sets the connections made with them needs_reauth.
	tenantKeys := tenantkeys.New(ctx, tenantkeys.Options{Store: tenantkeys.NewPG(pool, box, collect.RetireTenantKeys)})
	collectDeps := collect.Deps{
		Pool: pool, Raw: raw, Box: box, Tenants: store,
		Lookup:     providers.Lookup,
		Allowed:    tenantProviders.Allowed,
		TenantKeys: tenantKeys,
		PublicURL:  cfg.PublicURL,
		// Zero leaves each provider its own backfill window.
		BackfillWindow: backfillWindow(cfg),
		// Raw objects are sealed to public keys: collect alone can.
		Vault: keyVault,
	}

	if cfg.BootstrapTenant != "" {
		if err := bootstrap(ctx, store, cfg.BootstrapTenant); err != nil {
			return err
		}
	}
	if canOpen {
		// A root key that does not open refuses the start, rather than
		// failing every read and write later.
		if err := keyVault.Ready(ctx); err != nil {
			return fmt.Errorf("vault: the key provider does not open the tenant keys: %w", err)
		}
		if _, err := ensureVaultKeys(ctx, pool, keyVault); err != nil {
			return fmt.Errorf("vault: giving keys to the tenants and users without: %w", err)
		}
		if ctx.Err() != nil {
			// Stopped during the pass: nothing started, nothing failed.
			return nil
		}
	}

	// Events are written for the event stream unless it is off
	// (OHS_STREAM_MAX=0), by every role that produces them.
	webhook.SetStreamRecording(cfg.StreamMax >= 0)

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

	// Reload what other processes saved in the console.
	goRole("providers", func() error { return providers.Run(ctx, providerset.ReloadEvery) })
	goRole("tenant providers", func() error { return tenantProviders.Run(ctx, tenantproviders.ReloadEvery) })
	goRole("tenant keys", func() error { return tenantKeys.Run(ctx, tenantkeys.ReloadEvery) })
	if canOpen {
		// Drops from the cache the users another process shredded.
		goRole("vault", func() error { return keyVault.Listen(ctx) })
	}

	etlMetrics := &automation.Metrics{}
	// The heartbeats of the loops below and the worker metrics, served on
	// the ops listener (OHS_OPS_LISTEN).
	loops := ops.NewRegistry()
	if opts.noMinStall {
		loops.WithMinStall(0)
	}
	workerMetrics := ops.NewMetrics(loops)
	if cfg.OpsListen != "" {
		d := api.OpsDeps{Pool: pool, Raw: raw, Vault: keyVault, Stalled: stalledLoops(loops)}
		if cfg.Metrics {
			writers := []func(io.Writer){}
			if cfg.Roles["etl"] {
				writers = append(writers, func(w io.Writer) { ops.WriteSnapshot(w, "ohs_etl_", etlMetrics.Snapshot()) })
			}
			if canOpen {
				writers = append(writers, keyVault.Metrics().Write)
			}
			d.Metrics = opsMetrics(workerMetrics, writers...)
			goRole("queue backlog", func() error {
				return sampleQueues(ctx, pool, cfg.Roles, workerMetrics, queueSampleEvery)
			})
		}
		if err := serveOps(ctx, cfg, d, opts.grace, logger, goRole); err != nil {
			cancel()
			wg.Wait()
			return err
		}
	}
	if cfg.Roles["etl"] {
		q := queue.NewETL(pool)
		// Zero (a Config not built by Load) means the queue's default.
		q.PerUser = cfg.ETLJobsPerUser
		q.Hooks = etlHooks(loops, workerMetrics, cmp.Or(opts.jobLimit, jobBusyLimit))
		for _, tier := range []string{queue.TierStandard, queue.TierHighFreq} {
			w := etl.NewWorkerWith(pool, raw, q, tier, etlConcurrency(cfg), etl.WithShutdownGrace(opts.grace),
				etl.WithConsentRequired(cfg.ConsentRequired), etl.WithVault(keyVault),
				etl.WithRowsWritten(workerMetrics.ETLRows))
			goRole("etl "+tier, func() error { return w.Run(ctx) })
		}
		// The user's keys are shredded first, then provider grants are
		// revoked best effort before the user's rows go. A deletion cut
		// short by shutdown stays pending for the next start.
		dw := deletion.NewWorker(pool, raw, collectDeps).WithVault(keyVault).WithShutdownGrace(opts.grace).
			WithHeartbeat(loops.Loop(loopDeletion, deletionPollEvery))
		goRole("deletion", func() error { return dw.Run(ctx) })
		// Records of a category whose consent was revoked are purged here.
		cp := consent.NewPurger(pool).WithVault(keyVault)
		purgeLoop := loops.Loop(loopConsentPurge, consent.DefaultPurgeEvery)
		goRole("consent purge", func() error { return runConsentPurge(ctx, cp, purgeLoop) })
		// Outgoing webhooks: deliveries are written with the change they
		// announce, posted from here.
		wcfg := webhookWorkerConfig(cfg)
		wcfg.Loop, wcfg.Observe = loops.Loop(loopWebhooks, webhookPollEvery), workerMetrics.WebhookDone
		ww := webhook.NewWorker(pool, box, wcfg)
		goRole("webhooks", func() error { return ww.Run(ctx) })
		goRole("automation", func() error {
			return automation.Start(ctx, automation.Deps{
				Pool: pool, Store: raw, RetentionDays: cfg.RetentionDays, Metrics: etlMetrics,
				AuditRetentionDays: cfg.AuditRetentionDays,
				// Zero means the default, negative keeps the rows for ever.
				TrackingRetentionDays: cfg.TrackingRetentionDays,
				OnTick:                loops.Task,
			})
		})
	}

	if cfg.Roles["collect"] {
		scfg := schedulerConfig(cfg, opts.grace)
		scfg.Hooks = pullHooks(loops, workerMetrics)
		sch := collect.NewScheduler(collectDeps, scfg)
		goRole("collect", func() error { return sch.Run(ctx) })
	}

	if cfg.Roles["api"] {
		// The audit events of the high volume actions are written in batches.
		// Closed once every role has stopped, the HTTP server included, and
		// before the pool (deferred earlier) closes: that writes what is
		// still queued.
		auditWriter := audit.NewWriter(pool, audit.WriterOptions{OnError: func(err error, lost int) {
			slog.Warn("audit: batch not written", "error", err, "lost_events", lost)
		}})
		defer func() {
			actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), auditFlush)
			defer acancel()
			if err := auditWriter.Close(actx); err != nil {
				slog.Warn("audit: flush at shutdown", "error", err)
			}
		}()
		deps := api.Deps{
			Pool:      pool,
			Store:     store,
			Raw:       raw,
			Vault:     keyVault,
			Providers: providers,
			Metrics:   cfg.Metrics,
			Console:   cfg.Console,
			// read-only hides the console's write actions.
			ConsoleReadOnly: cfg.ConsoleReadOnly,
			Collect:         collect.NewHandlers(collectDeps),
			// Providers turned off per tenant, and their admin routes.
			TenantProviders: tenantProviders,
			// Provider keys of one tenant, and their admin routes.
			TenantKeys: tenantKeys,
			PublicURL:  cfg.PublicURL,
			Webhooks:   webhook.NewStore(pool, box, webhookStoreOptions(cfg)...),
			// WebhookAllowPrivate relaxes the endpoint URL checks.
			WebhookAllowPrivate: cfg.WebhookAllowPrivate,
			// Zero (a Config not built by Load) means the api default.
			IngestLimit:                cfg.IngestConcurrency,
			IngestTenantLimit:          cfg.IngestTenantConcurrency,
			IngestMaxDecompressedBytes: int64(cfg.IngestMaxDecompressedMiB) << 20,
			Version:                    Version,
			// Non-empty mounts the first-run setup routes (/v1/setup).
			SetupToken: cfg.SetupToken,
			// Zero (a Config not built by Load) means no rate limit.
			RateLimitInbound:         api.RateLimit(cfg.RateLimitInbound),
			RateLimitInboundProvider: api.RateLimit(cfg.RateLimitInboundProvider),
			RateLimitOAuth:           api.RateLimit(cfg.RateLimitOAuth),
			TrustedProxies:           cfg.TrustedProxies,
			Audit:                    auditWriter,
		}
		apiTuning(cfg, &deps)
		if cfg.Roles["etl"] {
			deps.ETLMetrics = etlMetrics
		}
		if c, ok := providers.Lookup(applehealth.Slug); ok {
			deps.Device, _ = connector.AsDevice(c)
		}
		if c, ok := providers.Lookup(healthconnect.Slug); ok {
			deps.HealthConnect, _ = connector.AsDevice(c)
		}
		if c, ok := providers.Lookup(xiaomi.Slug); ok {
			deps.Xiaomi, _ = connector.AsDevice(c)
		}
		if c, ok := providers.Lookup(activityfiles.Slug); ok {
			deps.ActivityFiles, _ = connector.AsDevice(c)
		}
		if c, ok := providers.Lookup(bluetooth.Slug); ok {
			deps.Bluetooth, _ = connector.AsDevice(c)
		}
		if c, ok := providers.Lookup(selfreport.Slug); ok {
			deps.SelfReport, _ = connector.AsDevice(c)
		}
		if c, ok := providers.Lookup(labs.Slug); ok {
			deps.Labs, _ = connector.AsDevice(c)
		}
		if cfg.Metrics && cfg.MetricsListen != "" {
			deps.MetricsMux = http.NewServeMux()
		}
		deps.MetricsToken = cfg.MetricsToken
		if cfg.Metrics && cfg.MetricsListen == "" && cfg.MetricsToken == "" {
			slog.Info("metrics: /metrics refuses scrapes until OHS_METRICS_TOKEN or OHS_METRICS_LISTEN is set")
		}
		if warn := untrustedProxyWarning(cfg.Listen, cfg.TrustedProxies); warn != "" {
			slog.Warn("api: " + warn)
		}
		if cfg.StreamMax >= 0 {
			// The event stream: one LISTEN connection, the streams of this
			// process, ended when the server shuts down.
			deps.Stream = api.NewStreamHub(pool, api.StreamOptions{Max: cfg.StreamMax, MaxPerTenant: cfg.StreamMaxPerTenant})
			goRole("event stream", func() error { return deps.Stream.Run(ctx) })
		}
		srv := newHTTPServer(cfg.Listen, api.NewRouter(deps), logger)
		if deps.Stream != nil {
			srv.RegisterOnShutdown(deps.Stream.Close)
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
		ln = limitConnections(ln, cfg.MaxConnections)
		slog.Info("api listening", "addr", ln.Addr().String(), "max_connections", cfg.MaxConnections)
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

// userKeysBatch is how many users one transaction of the startup pass gives
// keys to.
const userKeysBatch = 500

// ensureVaultKeys gives a key to every tenant that has none, each in a
// committed transaction of its own, then keys to every active user that has
// none (stored before the vault, or by a tool without one), one transaction
// per batch, and returns how many users. A tenant key committed first is
// unwrapped once and then served from the cache to every batch. A user being
// deleted gets none. A ctx that ends during the pass is a shutdown, not a
// failure: the pass stops and returns no error.
func ensureVaultKeys(ctx context.Context, pool *pgxpool.Pool, v *vault.Vault) (int, error) {
	if err := ensureTenantKeys(ctx, pool, v); err != nil {
		if ctx.Err() != nil {
			return 0, nil
		}
		return 0, err
	}
	total := 0
	var after struct{ tenant, user uuid.UUID }
	for {
		users, err := ensureUserKeysBatch(ctx, pool, v, after.tenant, after.user)
		if ctx.Err() != nil {
			return total, nil
		}
		if err != nil {
			return total, err
		}
		// Until a batch finds none: the cursor moves past every user seen,
		// so one left out (it turned deleting) is never read again.
		if len(users) == 0 {
			return total, nil
		}
		total += len(users)
		after.tenant, after.user = users[len(users)-1][0], users[len(users)-1][1]
		slog.Info("vault: keys given to users that had none", "users", len(users), "done", total)
	}
}

// ensureTenantKeys creates the missing tenant keys, one transaction each.
func ensureTenantKeys(ctx context.Context, pool *pgxpool.Pool, v *vault.Vault) error {
	rows, err := pool.Query(ctx, `
		SELECT id FROM ohs_core.tenants t
		WHERE NOT EXISTS (SELECT 1 FROM ohs_core.vault_keys k WHERE k.tenant_id = t.id AND k.user_id IS NULL)
		ORDER BY id`)
	if err != nil {
		return err
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, t := range tenants {
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return v.EnsureTenant(ctx, tx, t) }); err != nil {
			return err
		}
	}
	return nil
}

// ensureUserKeysBatch gives keys to the next batch of users after the cursor
// (tenant, user) and returns them, in order. It takes them FOR SHARE: a
// deletion starting meanwhile waits for the batch, and a user it already
// turned deleting is left out.
func ensureUserKeysBatch(ctx context.Context, pool *pgxpool.Pool, v *vault.Vault, tenant, user uuid.UUID) ([][2]uuid.UUID, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	rows, err := tx.Query(ctx, `
		SELECT tenant_id, id FROM ohs_core.end_users u
		WHERE status = 'active' AND (u.tenant_id, u.id) > ($2, $3)
		  AND NOT EXISTS (SELECT 1 FROM ohs_core.vault_keys k WHERE k.tenant_id = u.tenant_id AND k.user_id = u.id)
		ORDER BY tenant_id, id
		LIMIT $1
		FOR SHARE`, userKeysBatch, tenant, user)
	if err != nil {
		return nil, err
	}
	users, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]uuid.UUID, error) {
		var x [2]uuid.UUID
		return x, r.Scan(&x[0], &x[1])
	})
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		if err := v.EnsureUser(ctx, tx, u[0], u[1]); err != nil {
			return nil, err
		}
	}
	return users, tx.Commit(ctx)
}

// serveMetrics serves /metrics on an address of its own (OHS_METRICS_LISTEN),
// stopped with the process.
func serveMetrics(ctx context.Context, addr string, mux *http.ServeMux, grace time.Duration,
	logger *slog.Logger, goRole func(string, func() error)) error {
	srv := newHTTPServer(addr, mux, logger)
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

// bootstrap creates the OHS_BOOTSTRAP_TENANT tenant on first start and logs
// its key once, and makes it an instance admin at every start. This is the
// only place an API key is ever logged.
func bootstrap(ctx context.Context, store *tenant.Store, name string) error {
	t, key, err := store.EnsureTenant(ctx, name)
	if err != nil {
		return fmt.Errorf("bootstrap tenant: %w", err)
	}
	// The operator named it: it manages the instance from the console.
	if !t.Admin {
		if err := store.SetAdmin(ctx, t.ID, true); err != nil {
			return fmt.Errorf("bootstrap tenant: %w", err)
		}
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
