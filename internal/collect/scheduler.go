// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/tracing"
)

// Pull windows. The intervals, lookback, catch-up and backfill below are the
// defaults: each provider can declare its own (connector.SyncPolicy).
const (
	// PullEvery is how often a connection is pulled while its provider's
	// notifications arrive: the pull is only a backstop for a lost one.
	PullEvery = 6 * time.Hour
	// PullEveryWithoutPush is how often a connection is pulled when its
	// provider sends no notifications, or none arrived within PushSilence
	// (not configured, subscription lost, delivery failing).
	PullEveryWithoutPush = time.Hour
	// PushSilence is how long a connection may go without a notification
	// before its notifications count as not arriving.
	PushSilence = 24 * time.Hour
	// BackfillWindow is how far back a new connection is pulled.
	BackfillWindow = 30 * 24 * time.Hour
	// BackfillChunk is the span of one backfill pull job.
	BackfillChunk = 7 * 24 * time.Hour
	// pullOverlap re-reads the end of the previous window for late data.
	pullOverlap = time.Hour
	// maxCatchUp bounds how far back a regular pull reaches after an outage.
	maxCatchUp = 7 * 24 * time.Hour
	// defaultScheduleBatch bounds the connections one scheduling pass looks at.
	defaultScheduleBatch = 1000
)

// Default pull budget per provider (see SchedulerConfig).
const (
	DefaultPullsPerProvider     = 4
	DefaultBackfillsPerProvider = 2
)

// schedulerLock is the advisory lock key that makes one instance schedule at
// a time ("ohs pull" as bytes).
const schedulerLock int64 = 0x6f68732070756c6c

// SchedulerConfig tunes the pull loop. Zero values take the defaults.
type SchedulerConfig struct {
	// Concurrency is the number of pulls in flight per instance (default 4).
	Concurrency int
	// Tick is how often due connections are looked for (default 1 minute).
	Tick time.Duration
	// JobTimeout bounds one pull (default queue.VisibilityTimeout minus one minute).
	JobTimeout time.Duration
	// IdleMin and IdleMax bound the pause after an empty claim (default 1 to 2 s).
	IdleMin, IdleMax time.Duration
	// Batch bounds the backfills and the regular pulls one pass enqueues
	// (default 1000 each); the rest wait for the next tick.
	Batch int
	// PerProvider caps the pulls of one provider running at once across
	// every instance (default DefaultPullsPerProvider); BackfillPerProvider
	// caps its backfill pulls (default DefaultBackfillsPerProvider). A
	// negative value means no cap.
	PerProvider, BackfillPerProvider int
	// ShutdownGrace, when set, is how long pulls in flight may go on once
	// Run's context ends; then their context is cancelled (an unsettled
	// claim is reclaimed later). Zero leaves them bounded by JobTimeout only.
	ShutdownGrace time.Duration
}

// Scheduler is the collect role: it enqueues pulls (a backfill per new
// connection, then one pull per connection every PullEvery) and runs them,
// and subscribes pending push subscriptions at their providers.
type Scheduler struct {
	d   Deps
	q   *queue.Pull
	cfg SchedulerConfig
}

// NewScheduler returns the pull scheduler. Deps needs Pool, Raw and Box.
func NewScheduler(d Deps, cfg SchedulerConfig) *Scheduler {
	d.check("NewScheduler")
	if d.Raw == nil {
		panic("collect: NewScheduler needs Raw")
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	if cfg.Tick <= 0 {
		cfg.Tick = time.Minute
	}
	if cfg.JobTimeout <= 0 {
		cfg.JobTimeout = queue.VisibilityTimeout - time.Minute
	}
	if cfg.Batch <= 0 {
		cfg.Batch = defaultScheduleBatch
	}
	if cfg.IdleMin <= 0 {
		cfg.IdleMin = time.Second
	}
	if cfg.IdleMax < cfg.IdleMin {
		cfg.IdleMax = 2 * cfg.IdleMin
	}
	if cfg.PerProvider == 0 {
		cfg.PerProvider = DefaultPullsPerProvider
	}
	if cfg.BackfillPerProvider == 0 {
		cfg.BackfillPerProvider = DefaultBackfillsPerProvider
	}
	q := queue.NewPull(d.Pool)
	q.Limits = queue.PullLimits{PerProvider: max(cfg.PerProvider, 0), BackfillPerProvider: max(cfg.BackfillPerProvider, 0)}
	return &Scheduler{d: d, q: q, cfg: cfg}
}

// Run schedules and pulls until ctx is cancelled, then waits for the pulls
// in flight and returns nil. Pulls run under a context that outlives ctx
// (bounded by JobTimeout, and by ShutdownGrace once ctx ends), so shutdown
// does not leave half claimed jobs.
func (s *Scheduler) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.scheduleLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		s.subscribeLoop(ctx)
	}()
	s.workLoop(ctx)
	wg.Wait()
	return nil
}

func (s *Scheduler) scheduleLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.Tick)
	defer t.Stop()
	for {
		if _, err := s.Schedule(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "collect: schedule pulls", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// graceCtx returns a context for work in flight: it outlives ctx by the
// shutdown grace (when set), then is cancelled. Call cancel when done.
func (s *Scheduler) graceCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	jobCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	if s.cfg.ShutdownGrace <= 0 {
		return jobCtx, cancelJobs
	}
	stop := context.AfterFunc(ctx, func() {
		t := time.AfterFunc(s.cfg.ShutdownGrace, cancelJobs)
		context.AfterFunc(jobCtx, func() { t.Stop() })
	})
	return jobCtx, func() { stop(); cancelJobs() }
}

// subscribeLoop renews silent subscriptions and subscribes pending push
// subscriptions on every tick, apart from pull scheduling so a slow provider
// cannot hold pulls back. Each pass is bounded by subscribePassBudget.
func (s *Scheduler) subscribeLoop(ctx context.Context) {
	jobCtx, cancel := s.graceCtx(ctx)
	defer cancel()
	t := time.NewTicker(s.cfg.Tick)
	defer t.Stop()
	for {
		if n, err := s.RenewSilent(ctx, defaultScheduleBatch); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "collect: renew silent subscriptions", "error", err)
		} else if n > 0 {
			slog.InfoContext(ctx, "collect: renewing subscriptions that went silent", "count", n)
		}
		if _, err := s.subscribePass(ctx, jobCtx, defaultSubscribeBatch); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "collect: subscribe pending", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Scheduler) workLoop(ctx context.Context) {
	jobCtx, cancelJobs := s.graceCtx(ctx)
	defer cancelJobs()
	slots := make(chan struct{}, s.cfg.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case slots <- struct{}{}:
		}
		n := 1
	more:
		for n < s.cfg.Concurrency {
			select {
			case slots <- struct{}{}:
				n++
			default:
				break more
			}
		}
		jobs, err := s.q.Claim(ctx, n)
		if err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "collect: claim pull jobs", "error", err)
		}
		for range n - len(jobs) {
			<-slots
		}
		for _, job := range jobs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				s.handle(jobCtx, job)
			}()
		}
		// Fewer jobs than asked (even zero) does not mean the queue is
		// empty: a parked row can take a claim slot. Poll again later.
		if len(jobs) == 0 && !s.idle(ctx) {
			return
		}
	}
}

func (s *Scheduler) idle(ctx context.Context) bool {
	d := s.cfg.IdleMin
	if span := s.cfg.IdleMax - s.cfg.IdleMin; span > 0 {
		d += time.Duration(rand.Int64N(int64(span)))
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// PullOnce claims up to n due jobs and runs them, waiting for all of them. It
// returns how many were claimed. Run is the production loop; PullOnce serves
// tools and tests.
func (s *Scheduler) PullOnce(ctx context.Context, n int) (int, error) {
	jobs, err := s.q.Claim(ctx, n)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handle(ctx, job)
		}()
	}
	wg.Wait()
	return len(jobs), nil
}

// backfillWindows splits the policy's Backfill ending at now into
// BackfillChunk jobs, oldest first.
func backfillWindows(now time.Time, p connector.SyncPolicy) [][2]time.Time {
	end := now.UTC().Truncate(time.Second)
	var out [][2]time.Time
	for from := end.Add(-p.Backfill); from.Before(end); from = from.Add(p.BackfillChunk) {
		to := from.Add(p.BackfillChunk)
		if to.After(end) {
			to = end
		}
		out = append(out, [2]time.Time{from, to})
	}
	return out
}

// enqueueBackfill adds the backfill jobs of a new connection in tx, over its
// provider's backfill policy.
func enqueueBackfill(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, provider string, p connector.SyncPolicy, now time.Time) error {
	for _, w := range backfillWindows(now, p) {
		if err := queue.EnqueuePull(ctx, tx, queue.PullJob{
			TenantID: tenantID, UserID: userID, Provider: provider, From: w[0], To: w[1],
			Trigger: queue.TriggerBackfill,
		}); err != nil {
			return err
		}
	}
	return nil
}

// pullEvery is the regular pull interval of a connection under its
// provider's policy: PullEvery while its notifications arrive,
// PullEveryWithoutPush otherwise.
func pullEvery(p connector.SyncPolicy, pushHealthy bool) time.Duration {
	if pushHealthy {
		return p.PullEvery
	}
	return p.PullEveryWithoutPush
}

// pushHealthy reports whether a connection's notifications count as
// arriving: its provider sends them and one came within PushSilence.
func pushHealthy(push bool, lastEvent *time.Time, now time.Time) bool {
	return push && lastEvent != nil && now.Sub(*lastEvent) < PushSilence
}

// regularWindow is the pull window of the slot ending at slotEnd under
// policy p: the last interval (every) plus the provider's lookback, reaching
// back to the end of the last complete pull (syncedThrough) after an outage or
// a failed window, at most MaxCatchUp. The first slot of each UTC day reaches
// back Reconcile further when the policy sets it.
func regularWindow(slotEnd time.Time, every time.Duration, p connector.SyncPolicy, syncedThrough *time.Time) (time.Time, time.Time) {
	from := slotEnd.Add(-every - p.Lookback)
	if syncedThrough != nil {
		if back := syncedThrough.Add(-p.Lookback); back.Before(from) {
			from = back
		}
	}
	if floor := slotEnd.Add(-p.MaxCatchUp); from.Before(floor) {
		from = floor
	}
	if rf := reconcileFrom(slotEnd, p); rf.Before(from) {
		from = rf
	}
	return from, slotEnd
}

// reconcileFrom is where the slot ending at slotEnd must start for the
// policy's daily reconciliation: Reconcile back when slotEnd is a UTC
// midnight and the policy reconciles, else slotEnd (no constraint).
func reconcileFrom(slotEnd time.Time, p connector.SyncPolicy) time.Time {
	if p.Reconcile > 0 && slotEnd.UTC().Truncate(24*time.Hour).Equal(slotEnd) {
		return slotEnd.Add(-p.Reconcile)
	}
	return slotEnd
}

// Schedule runs one scheduling pass and returns the number of connections it
// enqueued work for. Regular pulls are aligned on slots of the connection's
// interval: PullEvery slots (00:00, 06:00, 12:00, 18:00 UTC) while its
// provider's notifications arrive, hourly slots otherwise (see pullEvery).
// Each active connection gets one job whose window ends at the latest slot
// boundary, once. A connection whose provider is not available on this
// instance, or is turned off for its tenant, is skipped. Only one instance
// schedules at a time (advisory lock); the others return 0.
func (s *Scheduler) Schedule(ctx context.Context) (int, error) {
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, schedulerLock).Scan(&locked); err != nil {
		return 0, fmt.Errorf("scheduler lock: %w", err)
	}
	if !locked {
		return 0, nil
	}
	now := s.d.now()
	n := 0

	// Providers are filtered in SQL, before the LIMIT: connections of a
	// provider this instance cannot pull must not fill the batch and starve
	// the others.
	slugs, err := s.slugs(ctx, tx)
	if err != nil {
		return 0, err
	}
	if len(slugs) > 0 {
		if n, err = s.scheduleConnections(ctx, tx, now, slugs); err != nil {
			return 0, err
		}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM ohs_sync.oauth_states WHERE expires_at < $1`, now.Add(-time.Hour)); err != nil {
		return 0, fmt.Errorf("purge oauth states: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ohs_core.connect_sessions WHERE expires_at < $1`, now.Add(-time.Hour)); err != nil {
		return 0, fmt.Errorf("purge connect sessions: %w", err)
	}
	if err := pruneRuns(ctx, tx, now, s.cfg.Batch); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return n, nil
}

// slugs are the providers this instance pulls: Deps.Slugs when set, else
// the providers of active connections; either way only those the instance's
// keys or a tenant's run as an OAuth connector.
func (s *Scheduler) slugs(ctx context.Context, tx pgx.Tx) ([]string, error) {
	candidates := s.d.Slugs
	if candidates == nil {
		rows, err := tx.Query(ctx, `SELECT DISTINCT provider FROM ohs_sync.connections WHERE status = 'active'`)
		if err != nil {
			return nil, fmt.Errorf("list providers: %w", err)
		}
		candidates, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, fmt.Errorf("list providers: %w", err)
		}
	}
	out := make([]string, 0, len(candidates))
	for _, slug := range candidates {
		if _, ok := s.d.capableOAuth(slug); ok {
			out = append(out, slug)
		}
	}
	return out, nil
}

// pushSlugs keeps the providers of slugs that send notifications.
func (s *Scheduler) pushSlugs(slugs []string) []string {
	out := []string{}
	for _, slug := range slugs {
		if c, ok := s.d.capable(slug); ok {
			if _, push := connector.AsPush(c); push {
				out = append(out, slug)
			}
		}
	}
	return out
}

// providerOn is the condition, on a connection c, that its provider is not
// turned off for its tenant. It reads the table rather than Deps.Allowed so
// it filters before the LIMIT: connections that cannot be pulled must not
// fill the batch and starve the others.
const providerOn = `NOT EXISTS (SELECT 1 FROM ohs_core.tenant_providers tp
		WHERE tp.tenant_id = c.tenant_id AND tp.provider = c.provider AND NOT tp.enabled)`

// scheduleConnections enqueues the backfills and the regular pulls due now.
// Only active connections of active users are considered, and only those
// whose provider is on for their tenant. The user rows are held FOR SHARE
// until tx ends, so a deletion cannot start between the check and the
// inserts (and then miss the jobs written here).
func (s *Scheduler) scheduleConnections(ctx context.Context, tx pgx.Tx, now time.Time, slugs []string) (int, error) {
	type key struct {
		tenant, user uuid.UUID
		provider     string
		lastSync     *time.Time
	}
	collectKeys := func(sql string, args ...any) ([]key, error) {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []key
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.tenant, &k.user, &k.provider, &k.lastSync); err != nil {
				return nil, err
			}
			out = append(out, k)
		}
		return out, rows.Err()
	}
	n := 0

	// Connections stored without a backfill (the callback enqueues its own;
	// this covers rows written by other paths), oldest first.
	fresh, err := collectKeys(`
		SELECT c.tenant_id, c.user_id, c.provider, c.last_sync_at FROM ohs_sync.connections c
		JOIN ohs_core.end_users u ON u.tenant_id = c.tenant_id AND u.id = c.user_id AND u.status = 'active'
		WHERE c.status = 'active' AND c.initial_backfill_at IS NULL AND c.provider = ANY($1)
		  AND `+providerOn+`
		ORDER BY c.created_at, c.tenant_id, c.user_id, c.provider
		LIMIT $2
		FOR SHARE OF u`, slugs, s.cfg.Batch)
	if err != nil {
		return 0, fmt.Errorf("find connections to backfill: %w", err)
	}
	for _, k := range fresh {
		if err := enqueueBackfill(ctx, tx, k.tenant, k.user, k.provider, s.d.syncPolicy(k.provider), now); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE ohs_sync.connections SET initial_backfill_at = $4, updated_at = now()
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
			k.tenant, k.user, k.provider, now); err != nil {
			return 0, fmt.Errorf("mark backfill: %w", err)
		}
		n++
	}

	// Regular pulls, the least recently synced first. Each provider has its
	// own policy (connector.SyncPolicy), passed as one row per provider: its
	// slot ends (the long interval's while its notifications arrive, the
	// short one's otherwise), spans (interval plus lookback), lookback,
	// catch-up bound and reconciliation start for each slot. $4 = silence
	// cutoff.
	pp := s.policyRows(slugs, now)
	rows, err := tx.Query(ctx, `
		SELECT c.tenant_id, c.user_id, c.provider, c.synced_through, s.slot_end, s.healthy
		FROM ohs_sync.connections c
		JOIN ohs_core.end_users u ON u.tenant_id = c.tenant_id AND u.id = c.user_id AND u.status = 'active'
		JOIN unnest($1::text[], $4::bool[], $5::timestamptz[], $6::timestamptz[], $7::float8[], $8::float8[],
		            $9::float8[], $10::float8[], $11::timestamptz[], $12::timestamptz[])
		     AS p(provider, push, long_slot, short_slot, long_span, short_span, lookback, catchup, long_recon, short_recon)
		  ON p.provider = c.provider
		CROSS JOIN LATERAL (
			SELECT h.healthy,
			       CASE WHEN h.healthy THEN p.long_slot ELSE p.short_slot END AS slot_end,
			       CASE WHEN h.healthy THEN p.long_span ELSE p.short_span END AS span,
			       CASE WHEN h.healthy THEN p.long_recon ELSE p.short_recon END AS recon_from
			FROM (SELECT coalesce(p.push AND c.last_event_at > $3, false) AS healthy) h
		) s
		WHERE c.status = 'active' AND `+providerOn+`
		  AND c.initial_backfill_at IS NOT NULL AND c.initial_backfill_at < s.slot_end
		  AND NOT EXISTS (
			SELECT 1 FROM ohs_sync.pull_jobs j
			WHERE j.tenant_id = c.tenant_id AND j.user_id = c.user_id AND j.provider = c.provider
			  -- a pull of one data type does not stand in for the backstop
			  AND j.data_type IS NULL
			  AND (j.to_time = s.slot_end
			       -- any other job counts only if it covers the whole window
			       -- regularWindow would pull (catch-up and reconciliation
			       -- included), e.g. a slot job an inbound burst widened
			       OR (j.to_time > s.slot_end AND j.from_time <= least(s.recon_from, greatest(
					s.slot_end - make_interval(secs => p.catchup),
					least(s.slot_end - make_interval(secs => s.span),
					      coalesce(c.synced_through, s.slot_end) - make_interval(secs => p.lookback)))))))
		ORDER BY c.last_sync_at NULLS FIRST, c.tenant_id, c.user_id, c.provider
		LIMIT $2
		FOR SHARE OF u`, pp.provider, s.cfg.Batch, now.Add(-PushSilence), pp.push, pp.longSlot, pp.shortSlot,
		pp.longSpan, pp.shortSpan, pp.lookback, pp.catchup, pp.longRecon, pp.shortRecon)
	if err != nil {
		return 0, fmt.Errorf("find due connections: %w", err)
	}
	type dueConn struct {
		tenant, user  uuid.UUID
		provider      string
		syncedThrough *time.Time
		slotEnd       time.Time
		healthy       bool
	}
	due, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (dueConn, error) {
		var k dueConn
		err := r.Scan(&k.tenant, &k.user, &k.provider, &k.syncedThrough, &k.slotEnd, &k.healthy)
		return k, err
	})
	if err != nil {
		return 0, fmt.Errorf("find due connections: %w", err)
	}
	for _, k := range due {
		p := s.d.syncPolicy(k.provider)
		from, to := regularWindow(k.slotEnd, pullEvery(p, k.healthy), p, k.syncedThrough)
		if err := queue.EnqueuePull(ctx, tx, queue.PullJob{
			TenantID: k.tenant, UserID: k.user, Provider: k.provider, From: from, To: to,
		}); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// policyRows are the per-provider columns of the regular pull query, one
// entry per slug, in the same order.
type policyRows struct {
	provider              []string
	push                  []bool
	longSlot, shortSlot   []time.Time
	longSpan, shortSpan   []float64
	lookback, catchup     []float64
	longRecon, shortRecon []time.Time
}

func (s *Scheduler) policyRows(slugs []string, now time.Time) policyRows {
	push := map[string]bool{}
	for _, slug := range s.pushSlugs(slugs) {
		push[slug] = true
	}
	var r policyRows
	for _, slug := range slugs {
		p := s.d.syncPolicy(slug)
		long, short := now.UTC().Truncate(p.PullEvery), now.UTC().Truncate(p.PullEveryWithoutPush)
		r.provider = append(r.provider, slug)
		r.push = append(r.push, push[slug])
		r.longSlot = append(r.longSlot, long)
		r.shortSlot = append(r.shortSlot, short)
		r.longSpan = append(r.longSpan, (p.PullEvery + p.Lookback).Seconds())
		r.shortSpan = append(r.shortSpan, (p.PullEveryWithoutPush + p.Lookback).Seconds())
		r.lookback = append(r.lookback, p.Lookback.Seconds())
		r.catchup = append(r.catchup, p.MaxCatchUp.Seconds())
		r.longRecon = append(r.longRecon, reconcileFrom(long, p))
		r.shortRecon = append(r.shortRecon, reconcileFrom(short, p))
	}
	return r
}

// handle runs one pull job and settles it. A lost claim (ErrNotRunning)
// means another worker owns the job: the result is dropped (R11).
// With tracing on, the job is a span carrying the ids and counters its log
// lines carry, and its outcome.
func (s *Scheduler) handle(ctx context.Context, job queue.PullJob) {
	log := slog.With("job_id", job.ID, "tenant_id", job.TenantID, "user_id", job.UserID,
		"provider", job.Provider, "attempts", job.Attempts)
	kind := "scheduled"
	if job.Inbound {
		kind = "notification"
	}
	ctx, span := tracing.Tracer().Start(ctx, "pull job", trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.Int64(tracing.AttrJobID, job.ID),
			attribute.String(tracing.AttrJobKind, kind),
			attribute.String(tracing.AttrProvider, job.Provider),
			attribute.String(tracing.AttrTenantID, job.TenantID.String()),
			attribute.String(tracing.AttrUserID, job.UserID.String()),
			attribute.Int(tracing.AttrAttempts, job.Attempts)))
	outcome := ""
	defer func() {
		if r := recover(); r != nil {
			log.ErrorContext(ctx, "collect: pull job panicked", "panic", r)
			outcome = s.settle(ctx, log, job, errors.New("pull job panicked"))
		}
		endJobSpan(span, outcome)
	}()
	jobCtx, cancel := context.WithTimeout(ctx, s.cfg.JobTimeout)
	defer cancel()
	start := time.Now()
	stored, err := s.pull(jobCtx, job)
	job.Stored = stored
	s.logSync(ctx, log, job, stored, time.Since(start), err)
	outcome = s.settle(ctx, log, job, err)
}

// endJobSpan records a pull job's outcome and ends its span. A failed pull
// is an error span; the error text stays in the logs and the sync log.
func endJobSpan(span trace.Span, outcome string) {
	span.SetAttributes(attribute.String(tracing.AttrOutcome, outcome))
	if outcome == outcomeFailed || outcome == outcomeSettleError {
		span.SetStatus(codes.Error, "")
	}
	span.End()
}

// Pull job outcomes, as the ohs.job.outcome span attribute records them.
const (
	outcomeCompleted   = "completed"
	outcomeDeferred    = "deferred"
	outcomeNeedsReauth = "needs_reauth"
	outcomeInactive    = "inactive"
	outcomeFailed      = "failed"
	outcomeClaimLost   = "claim_lost"
	outcomeSettleError = "settle_error"
)

// pull fetches the job's window. It returns the number of raw objects stored.
func (s *Scheduler) pull(ctx context.Context, job queue.PullJob) (int, error) {
	if _, ok := s.d.capableOAuth(job.Provider); !ok {
		return 0, fmt.Errorf("provider %q is not available on this instance", job.Provider)
	}
	// Queued before the provider was turned off for the tenant: nothing is
	// fetched, and the job is cancelled rather than failed (settle).
	if !s.d.allowed(job.TenantID, job.Provider) {
		return 0, ErrProviderOff
	}
	conn, oc, err := s.d.connection(ctx, job.TenantID, job.UserID, job.Provider)
	if err != nil {
		return 0, err
	}
	sink := newPullSink(s.d, job)
	fetch := func(conn connector.Connection) error {
		if nf, ok := oc.(connector.NotifiedFetcher); ok && job.DataType != "" {
			return nf.FetchNotified(ctx, conn, job.From, job.To, job.DataType, sink)
		}
		return oc.Fetch(ctx, conn, job.From, job.To, sink)
	}
	err = fetch(conn)
	if errors.Is(err, connector.ErrNeedsReauth) {
		// The access token may just have expired early or been revoked
		// alone (Withings answers 401): refresh once and retry once. Only a
		// refused refresh, or a second refusal, means the grant is gone.
		conn, err = s.d.refresh(ctx, job.TenantID, job.UserID, job.Provider, conn.Tokens.Access)
		if err != nil {
			// refresh marks needs_reauth itself when the provider refuses.
			return sink.stored, err
		}
		err = fetch(conn)
		if errors.Is(err, connector.ErrNeedsReauth) {
			// Refused again: the grant is gone, unless a reconnect replaced
			// the token in between.
			err = s.d.markNeedsReauthIf(ctx, job.TenantID, job.UserID, job.Provider, conn.Tokens.Access, reauthReason(err))
		}
	}
	return sink.stored, err
}

// errTokenReplaced: the provider refused a token the connection no longer
// holds (a reconnect landed meanwhile). The job is retried with the new one.
var errTokenReplaced = errors.New("collect: provider refused a token that has since been replaced")

// markNeedsReauthIf flags the connection needs_reauth (broken for reason)
// under its row lock, only while it still holds the refused access token. It
// returns connector.ErrMissingScope for that reason, else ErrNeedsReauth.
func (d Deps) markNeedsReauthIf(ctx context.Context, tenantID, userID uuid.UUID, provider, rejected, reason string) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	c, err := scanConn(tx.QueryRow(ctx, `SELECT `+connCols+`
		FROM ohs_sync.connections WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
		FOR UPDATE`, tenantID, userID, provider))
	if errors.Is(err, pgx.ErrNoRows) {
		return errInactive
	}
	if err != nil {
		return fmt.Errorf("lock connection: %w", err)
	}
	if c.status != StatusActive {
		return errInactive
	}
	conn, err := d.open(c)
	if err != nil {
		return err
	}
	if conn.Tokens.Access != rejected {
		return errTokenReplaced
	}
	if err := markNeedsReauth(ctx, tx, tenantID, userID, provider, reason, d.now()); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if reason == BrokenMissingScope {
		return connector.ErrMissingScope
	}
	return connector.ErrNeedsReauth
}

// settle records the outcome on the job, and on the connection's freshness
// and health, and returns it (an outcome* value).
func (s *Scheduler) settle(ctx context.Context, log *slog.Logger, job queue.PullJob, cause error) string {
	var err error
	var outcome string
	switch rl, limited := connector.AsRateLimit(cause); {
	case cause == nil:
		outcome = outcomeCompleted
		err = s.q.Complete(ctx, job)
		if err == nil {
			if uerr := s.recordSuccess(ctx, job); uerr != nil {
				log.ErrorContext(ctx, "collect: record last sync", "error", uerr)
			}
			if uerr := s.q.ProviderOK(ctx, job.Provider); uerr != nil {
				log.ErrorContext(ctx, "collect: reset provider backoff", "error", uerr)
			}
		}
	case limited:
		outcome = outcomeDeferred
		until := s.d.now().Add(rl.RetryAfter)
		log.InfoContext(ctx, "collect: provider rate limit, pull deferred", "retry_after", rl.RetryAfter)
		err = s.q.Defer(ctx, job, until)
		// The limit is the provider's for the whole application, usually:
		// every pull of it waits, instead of each drawing its own refusal.
		if paused, perr := s.q.PauseProvider(ctx, job.Provider, rl.RetryAfter); perr != nil {
			log.ErrorContext(ctx, "collect: pause provider", "error", perr)
		} else {
			log.InfoContext(ctx, "collect: provider paused after a rate limit", "until", paused)
		}
		if err == nil && rl.RetryAfter >= RateLimitBrokenAfter {
			if uerr := s.recordRateLimit(ctx, job, until); uerr != nil {
				log.ErrorContext(ctx, "collect: record rate limit", "error", uerr)
			}
		}
	case errors.Is(cause, connector.ErrNeedsReauth):
		outcome = outcomeNeedsReauth
		log.WarnContext(ctx, "collect: provider rejected the grant, connection needs reauthorization")
		parked := queue.OutcomeNeedsReauth
		if errors.Is(cause, connector.ErrMissingScope) {
			parked = queue.OutcomeMissingScope
		}
		err = parkJob(ctx, s.d.Pool, job, reasonNeedsReauth, parked)
	case errors.Is(cause, errInactive):
		outcome = outcomeInactive
		err = parkJob(ctx, s.d.Pool, job, reasonInactive, queue.OutcomeInactive)
	case errors.Is(cause, ErrProviderOff):
		// Cancelled like the job of a stopped connection: not a failure,
		// never retried. The regular pulls catch up once it is back on.
		outcome = outcomeInactive
		log.InfoContext(ctx, "collect: provider turned off for the tenant, pull cancelled")
		err = parkJob(ctx, s.d.Pool, job, reasonProviderOff, queue.OutcomeInactive)
	default:
		outcome = outcomeFailed
		msg := safeErr(cause)
		log.WarnContext(ctx, "collect: pull failed", "error", msg)
		err = s.q.Fail(ctx, job, errors.New(msg))
		if err == nil {
			if uerr := s.recordFailure(ctx, job, msg); uerr != nil {
				log.ErrorContext(ctx, "collect: record pull failure", "error", uerr)
			}
		}
	}
	switch {
	case errors.Is(err, queue.ErrNotRunning):
		log.InfoContext(ctx, "collect: pull job claim lost, result dropped")
		return outcomeClaimLost
	case err != nil:
		log.ErrorContext(ctx, "collect: settle pull job", "error", err)
		return outcomeSettleError
	}
	return outcome
}

// recordSuccess moves the connection's freshness forward and restores it if
// it was broken. synced_through only moves forward, never past now (a
// notification may name a window ending later), and only for a pull of
// every type: one type does not make the rest current.
func (s *Scheduler) recordSuccess(ctx context.Context, job queue.PullJob) error {
	now := s.d.now()
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var status string
	err = tx.QueryRow(ctx, `
		UPDATE ohs_sync.connections SET last_sync_at = $4, last_attempt_at = $4,
			synced_through = CASE WHEN $6 THEN synced_through
				ELSE greatest(synced_through, least($5::timestamptz, $4::timestamptz)) END,
			last_error = NULL, consecutive_failures = 0, updated_at = now()
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
		RETURNING status`,
		job.TenantID, job.UserID, job.Provider, now, job.To, job.DataType != "").Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// A disconnect or a needs_reauth that landed meanwhile stands.
	if status == StatusActive {
		if err := markRestored(ctx, tx, job.TenantID, job.UserID, job.Provider, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// recordFailure counts a failed pull on the connection; the FailingAfter-th
// failure in a row breaks it.
func (s *Scheduler) recordFailure(ctx context.Context, job queue.PullJob, msg string) error {
	now := s.d.now()
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var failures int
	var status string
	err = tx.QueryRow(ctx, `
		UPDATE ohs_sync.connections SET last_attempt_at = $4, last_error = $5,
			consecutive_failures = consecutive_failures + 1, updated_at = now()
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
		RETURNING consecutive_failures, status`,
		job.TenantID, job.UserID, job.Provider, now, msg).Scan(&failures, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if failures >= FailingAfter && status == StatusActive {
		if err := markBroken(ctx, tx, job.TenantID, job.UserID, job.Provider, BrokenFailing, now,
			map[string]any{"last_error": msg, "consecutive_failures": failures}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// recordRateLimit notes a long provider rate limit on the connection, which
// breaks it until the next successful pull.
func (s *Scheduler) recordRateLimit(ctx context.Context, job queue.PullJob, until time.Time) error {
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var status string
	err = tx.QueryRow(ctx, `
		UPDATE ohs_sync.connections SET rate_limited_until = greatest(rate_limited_until, $4), updated_at = now()
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
		RETURNING status`, job.TenantID, job.UserID, job.Provider, until).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status == StatusActive {
		if err := markBroken(ctx, tx, job.TenantID, job.UserID, job.Provider, BrokenRateLimited, s.d.now(),
			map[string]any{"retry_at": until.UTC()}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// parkJob stops a claimed job for good (its connection is no longer pulled).
// It is fenced on the claim like the queue's own settles.
func parkJob(ctx context.Context, q execer, job queue.PullJob, reason, outcome string) error {
	tag, err := q.Exec(ctx, `
		UPDATE ohs_sync.pull_jobs SET status = 'parked', claimed_at = NULL, last_error = $3,
			outcome = $4, objects_stored = $5, started_at = $2, finished_at = now()
		WHERE id = $1 AND status = 'running' AND claimed_at = $2`, job.ID, job.ClaimedAt, reason, outcome, job.Stored)
	if err != nil {
		return fmt.Errorf("park pull job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return queue.ErrNotRunning
	}
	return nil
}

// logSync writes the pull's sync_log row. Rate limits, stopped connections
// and providers turned off for the tenant are not errors of the pull and are
// not logged there.
func (s *Scheduler) logSync(ctx context.Context, log *slog.Logger, job queue.PullJob, stored int, took time.Duration, cause error) {
	status, msg := "completed", (*string)(nil)
	if cause != nil {
		if _, limited := connector.AsRateLimit(cause); limited || errors.Is(cause, errInactive) ||
			errors.Is(cause, ErrProviderOff) {
			return
		}
		status = "error"
		m := safeErr(cause)
		msg = &m
	}
	if err := s.insertSyncLog(ctx, job, status, stored, took, msg); err != nil && !errors.Is(err, errInactive) {
		log.ErrorContext(ctx, "collect: write sync log", "error", err)
	}
}

// insertSyncLog writes the row under a share lock on an active user, so no
// row lands after a deletion swept the user's tables. errInactive means the
// user is gone or being deleted and nothing was written.
func (s *Scheduler) insertSyncLog(ctx context.Context, job queue.PullJob, status string, stored int, took time.Duration, msg *string) error {
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockActiveUser(ctx, tx, job.TenantID, job.UserID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ohs_sync.sync_log (tenant_id, user_id, provider, stage, status, records_written,
			duration_ms, error_message, day_start, day_end)
		VALUES ($1, $2, $3, 'pull', $4, $5, $6, $7, $8, $9)`,
		job.TenantID, job.UserID, job.Provider, status, stored, took.Milliseconds(), msg, job.From, job.To); err != nil {
		return fmt.Errorf("insert sync log: %w", err)
	}
	return tx.Commit(ctx)
}
