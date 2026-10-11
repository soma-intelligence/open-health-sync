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

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// Pull windows.
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
	return &Scheduler{d: d, q: queue.NewPull(d.Pool), cfg: cfg}
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

// backfillWindows splits the BackfillWindow ending at now into BackfillChunk
// jobs, oldest first.
func backfillWindows(now time.Time) [][2]time.Time {
	end := now.UTC().Truncate(time.Second)
	var out [][2]time.Time
	for from := end.Add(-BackfillWindow); from.Before(end); from = from.Add(BackfillChunk) {
		to := from.Add(BackfillChunk)
		if to.After(end) {
			to = end
		}
		out = append(out, [2]time.Time{from, to})
	}
	return out
}

// enqueueBackfill adds the backfill jobs of a new connection in tx.
func enqueueBackfill(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, provider string, now time.Time) error {
	for _, w := range backfillWindows(now) {
		if err := queue.EnqueuePull(ctx, tx, queue.PullJob{
			TenantID: tenantID, UserID: userID, Provider: provider, From: w[0], To: w[1],
		}); err != nil {
			return err
		}
	}
	return nil
}

// pullEvery is the regular pull interval of a connection: PullEvery while
// its notifications arrive, PullEveryWithoutPush otherwise.
func pullEvery(pushHealthy bool) time.Duration {
	if pushHealthy {
		return PullEvery
	}
	return PullEveryWithoutPush
}

// pushHealthy reports whether a connection's notifications count as
// arriving: its provider sends them and one came within PushSilence.
func pushHealthy(push bool, lastEvent *time.Time, now time.Time) bool {
	return push && lastEvent != nil && now.Sub(*lastEvent) < PushSilence
}

// regularWindow is the pull window of the slot ending at slotEnd: the last
// interval (every) plus an overlap, reaching back to the last successful sync
// after an outage (at most maxCatchUp).
func regularWindow(slotEnd time.Time, every time.Duration, lastSync *time.Time) (time.Time, time.Time) {
	from := slotEnd.Add(-every - pullOverlap)
	if lastSync != nil {
		if back := lastSync.Add(-pullOverlap); back.Before(from) {
			from = back
		}
	}
	if floor := slotEnd.Add(-maxCatchUp); from.Before(floor) {
		from = floor
	}
	return from, slotEnd
}

// Schedule runs one scheduling pass and returns the number of connections it
// enqueued work for. Regular pulls are aligned on slots of the connection's
// interval: PullEvery slots (00:00, 06:00, 12:00, 18:00 UTC) while its
// provider's notifications arrive, hourly slots otherwise (see pullEvery).
// Each active connection gets one job whose window ends at the latest slot
// boundary, once. A connection whose provider is not available on this
// instance is skipped. Only one instance schedules at a time (advisory lock);
// the others return 0.
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
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return n, nil
}

// slugs are the providers this instance pulls: Deps.Slugs when set, else
// the providers of active connections; either way only those Lookup resolves
// to an OAuth connector.
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
		if _, ok := s.d.oauth(slug); ok {
			out = append(out, slug)
		}
	}
	return out, nil
}

// pushSlugs keeps the providers of slugs that send notifications.
func (s *Scheduler) pushSlugs(slugs []string) []string {
	out := []string{}
	for _, slug := range slugs {
		if c, ok := s.d.lookup(slug); ok {
			if _, push := connector.AsPush(c); push {
				out = append(out, slug)
			}
		}
	}
	return out
}

// scheduleConnections enqueues the backfills and the regular pulls due now.
// Only active connections of active users are considered. The user rows are
// held FOR SHARE until tx ends, so a deletion cannot start between the check
// and the inserts (and then miss the jobs written here).
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
		ORDER BY c.created_at, c.tenant_id, c.user_id, c.provider
		LIMIT $2
		FOR SHARE OF u`, slugs, s.cfg.Batch)
	if err != nil {
		return 0, fmt.Errorf("find connections to backfill: %w", err)
	}
	for _, k := range fresh {
		if err := enqueueBackfill(ctx, tx, k.tenant, k.user, k.provider, now); err != nil {
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

	// Regular pulls, the least recently synced first. Each connection's
	// slot follows its interval: the long one while its notifications
	// arrive ($7 = providers that send them, $8 = silence cutoff), the
	// short one otherwise.
	longSlot, shortSlot := now.UTC().Truncate(PullEvery), now.UTC().Truncate(PullEveryWithoutPush)
	rows, err := tx.Query(ctx, `
		SELECT c.tenant_id, c.user_id, c.provider, c.last_sync_at, s.slot_end, s.healthy
		FROM ohs_sync.connections c
		JOIN ohs_core.end_users u ON u.tenant_id = c.tenant_id AND u.id = c.user_id AND u.status = 'active'
		CROSS JOIN LATERAL (
			SELECT h.healthy,
			       CASE WHEN h.healthy THEN $1::timestamptz ELSE $9::timestamptz END AS slot_end,
			       CASE WHEN h.healthy THEN $4::double precision ELSE $10::double precision END AS span
			FROM (SELECT coalesce(c.provider = ANY($7) AND c.last_event_at > $8, false) AS healthy) h
		) s
		WHERE c.status = 'active' AND c.provider = ANY($3)
		  AND c.initial_backfill_at IS NOT NULL AND c.initial_backfill_at < s.slot_end
		  AND NOT EXISTS (
			SELECT 1 FROM ohs_sync.pull_jobs j
			WHERE j.tenant_id = c.tenant_id AND j.user_id = c.user_id AND j.provider = c.provider
			  AND (j.to_time = s.slot_end
			       -- any other job counts only if it covers the whole window
			       -- regularWindow would pull (catch-up included), e.g. a slot
			       -- job an inbound burst widened
			       OR (j.to_time > s.slot_end AND j.from_time <= greatest(
					s.slot_end - make_interval(secs => $6::double precision),
					least(s.slot_end - make_interval(secs => s.span),
					      coalesce(c.last_sync_at, s.slot_end) - make_interval(secs => $5::double precision))))))
		ORDER BY c.last_sync_at NULLS FIRST, c.tenant_id, c.user_id, c.provider
		LIMIT $2
		FOR SHARE OF u`, longSlot, s.cfg.Batch, slugs, (PullEvery + pullOverlap).Seconds(), pullOverlap.Seconds(),
		maxCatchUp.Seconds(), s.pushSlugs(slugs), now.Add(-PushSilence), shortSlot, (PullEveryWithoutPush + pullOverlap).Seconds())
	if err != nil {
		return 0, fmt.Errorf("find due connections: %w", err)
	}
	type dueConn struct {
		key
		slotEnd time.Time
		healthy bool
	}
	due, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (dueConn, error) {
		var k dueConn
		err := r.Scan(&k.tenant, &k.user, &k.provider, &k.lastSync, &k.slotEnd, &k.healthy)
		return k, err
	})
	if err != nil {
		return 0, fmt.Errorf("find due connections: %w", err)
	}
	for _, k := range due {
		from, to := regularWindow(k.slotEnd, pullEvery(k.healthy), k.lastSync)
		if err := queue.EnqueuePull(ctx, tx, queue.PullJob{
			TenantID: k.tenant, UserID: k.user, Provider: k.provider, From: from, To: to,
		}); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// handle runs one pull job and settles it. A lost claim (ErrNotRunning)
// means another worker owns the job: the result is dropped (R11).
func (s *Scheduler) handle(ctx context.Context, job queue.PullJob) {
	log := slog.With("job_id", job.ID, "tenant_id", job.TenantID, "user_id", job.UserID,
		"provider", job.Provider, "attempts", job.Attempts)
	defer func() {
		if r := recover(); r != nil {
			log.ErrorContext(ctx, "collect: pull job panicked", "panic", r)
			s.settle(ctx, log, job, errors.New("pull job panicked"))
		}
	}()
	jobCtx, cancel := context.WithTimeout(ctx, s.cfg.JobTimeout)
	defer cancel()
	start := time.Now()
	stored, err := s.pull(jobCtx, job)
	s.logSync(ctx, log, job, stored, time.Since(start), err)
	s.settle(ctx, log, job, err)
}

// pull fetches the job's window. It returns the number of raw objects stored.
func (s *Scheduler) pull(ctx context.Context, job queue.PullJob) (int, error) {
	oc, ok := s.d.oauth(job.Provider)
	if !ok {
		return 0, fmt.Errorf("provider %q is not available on this instance", job.Provider)
	}
	conn, err := s.d.connection(ctx, oc, job.TenantID, job.UserID, job.Provider)
	if err != nil {
		return 0, err
	}
	sink := newPullSink(s.d, job)
	err = oc.Fetch(ctx, conn, job.From, job.To, sink)
	if errors.Is(err, connector.ErrNeedsReauth) {
		// The access token may just have expired early or been revoked
		// alone (Withings answers 401): refresh once and retry once. Only a
		// refused refresh, or a second refusal, means the grant is gone.
		conn, err = s.d.refresh(ctx, oc, job.TenantID, job.UserID, job.Provider, conn.Tokens.Access)
		if err != nil {
			// refresh marks needs_reauth itself when the provider refuses.
			return sink.stored, err
		}
		err = oc.Fetch(ctx, conn, job.From, job.To, sink)
		if errors.Is(err, connector.ErrNeedsReauth) {
			// Refused again: the grant is gone, unless a reconnect replaced
			// the token in between.
			err = s.d.markNeedsReauthIf(ctx, job.TenantID, job.UserID, job.Provider, conn.Tokens.Access)
		}
	}
	return sink.stored, err
}

// errTokenReplaced: the provider refused a token the connection no longer
// holds (a reconnect landed meanwhile). The job is retried with the new one.
var errTokenReplaced = errors.New("collect: provider refused a token that has since been replaced")

// markNeedsReauthIf flags the connection needs_reauth under its row lock,
// only while it still holds the refused access token.
func (d Deps) markNeedsReauthIf(ctx context.Context, tenantID, userID uuid.UUID, provider, rejected string) error {
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
	if err := markNeedsReauth(ctx, tx, tenantID, userID, provider); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return connector.ErrNeedsReauth
}

// settle records the outcome on the job.
func (s *Scheduler) settle(ctx context.Context, log *slog.Logger, job queue.PullJob, cause error) {
	var err error
	switch rl, limited := connector.AsRateLimit(cause); {
	case cause == nil:
		err = s.q.Complete(ctx, job)
		if err == nil {
			// synced_through only moves forward, and never past now (a
			// notification may name a window ending later).
			now := s.d.now()
			if _, uerr := s.d.Pool.Exec(ctx, `
				UPDATE ohs_sync.connections SET last_sync_at = $4, last_attempt_at = $4,
					synced_through = greatest(synced_through, least($5::timestamptz, $4::timestamptz)),
					last_error = NULL, consecutive_failures = 0, updated_at = now()
				WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
				job.TenantID, job.UserID, job.Provider, now, job.To); uerr != nil {
				log.ErrorContext(ctx, "collect: record last sync", "error", uerr)
			}
		}
	case limited:
		until := s.d.now().Add(rl.RetryAfter)
		log.InfoContext(ctx, "collect: provider rate limit, pull deferred", "retry_after", rl.RetryAfter)
		err = s.q.Defer(ctx, job, until)
	case errors.Is(cause, connector.ErrNeedsReauth):
		log.WarnContext(ctx, "collect: provider rejected the grant, connection needs reauthorization")
		err = parkJob(ctx, s.d.Pool, job, reasonNeedsReauth)
	case errors.Is(cause, errInactive):
		err = parkJob(ctx, s.d.Pool, job, reasonInactive)
	default:
		msg := safeErr(cause)
		log.WarnContext(ctx, "collect: pull failed", "error", msg)
		err = s.q.Fail(ctx, job, errors.New(msg))
		if err == nil {
			if _, uerr := s.d.Pool.Exec(ctx, `
				UPDATE ohs_sync.connections SET last_attempt_at = $4, last_error = $5,
					consecutive_failures = consecutive_failures + 1, updated_at = now()
				WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
				job.TenantID, job.UserID, job.Provider, s.d.now(), msg); uerr != nil {
				log.ErrorContext(ctx, "collect: record pull failure", "error", uerr)
			}
		}
	}
	switch {
	case errors.Is(err, queue.ErrNotRunning):
		log.InfoContext(ctx, "collect: pull job claim lost, result dropped")
	case err != nil:
		log.ErrorContext(ctx, "collect: settle pull job", "error", err)
	}
}

// parkJob stops a claimed job for good (its connection is no longer pulled).
// It is fenced on the claim like the queue's own settles.
func parkJob(ctx context.Context, q execer, job queue.PullJob, reason string) error {
	tag, err := q.Exec(ctx, `
		UPDATE ohs_sync.pull_jobs SET status = 'parked', claimed_at = NULL, last_error = $3
		WHERE id = $1 AND status = 'running' AND claimed_at = $2`, job.ID, job.ClaimedAt, reason)
	if err != nil {
		return fmt.Errorf("park pull job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return queue.ErrNotRunning
	}
	return nil
}

// logSync writes the pull's sync_log row. Rate limits and stopped
// connections are not errors of the pull and are not logged there.
func (s *Scheduler) logSync(ctx context.Context, log *slog.Logger, job queue.PullJob, stored int, took time.Duration, cause error) {
	status, msg := "completed", (*string)(nil)
	if cause != nil {
		if _, limited := connector.AsRateLimit(cause); limited || errors.Is(cause, errInactive) {
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
