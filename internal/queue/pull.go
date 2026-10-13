// SPDX-License-Identifier: AGPL-3.0-only

package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PullJob is one provider pull for one user over [From, To).
type PullJob struct {
	ID       int64
	TenantID uuid.UUID
	UserID   uuid.UUID
	Provider string
	From, To time.Time
	// DataType, when set, restricts the pull to that provider data type
	// (a notification named it); empty pulls every type.
	DataType string
	// Inbound marks a pull a provider notification asked for: it is
	// claimed before routine (backfill and backstop) pulls.
	Inbound bool
	// Trigger is what started the job (TriggerScheduled when empty).
	Trigger   string
	Attempts  int
	NotBefore time.Time
	// ClaimedAt is set by Claim and fences Complete, Fail and Defer.
	ClaimedAt time.Time
	// Stored is the number of raw objects the attempt stored, recorded by
	// Complete and Fail.
	Stored int
}

// Triggers (pull_jobs.trigger): what started a pull job.
const (
	TriggerBackfill     = "backfill"     // the first pull of a new connection
	TriggerScheduled    = "scheduled"    // the regular backstop pull
	TriggerNotification = "notification" // a provider notification
	TriggerManual       = "manual"       // POST /v1/users/{id}/connections/{provider}/sync
)

// Outcomes (pull_jobs.outcome): how the last attempt of a job ended.
const (
	OutcomeCompleted    = "completed"
	OutcomeFailed       = "failed"
	OutcomeRateLimited  = "rate_limited"
	OutcomeNeedsReauth  = "needs_reauth"
	OutcomeMissingScope = "missing_scope"
	OutcomeInactive     = "inactive"
)

// Pull job priorities (pull_jobs.priority), claimed lowest first.
const (
	priorityInbound  = 0 // a notification or a manual sync
	priorityRoutine  = 1 // the regular backstop pull
	priorityBackfill = 2 // the first 30 days of a new connection
)

func (j PullJob) trigger() string {
	switch {
	case j.Trigger != "":
		return j.Trigger
	case j.Inbound:
		return TriggerNotification
	}
	return TriggerScheduled
}

// EnqueuePull adds a job inside the caller's transaction. A pending or running
// job for the same user, provider, window and data type makes this a no-op.
// A zero NotBefore means now.
func EnqueuePull(ctx context.Context, tx pgx.Tx, j PullJob) error {
	_, err := tx.Exec(ctx, insertPullSQL, j.args()...)
	if err != nil {
		return fmt.Errorf("enqueue pull job: %w", err)
	}
	return nil
}

const insertPullSQL = `
	INSERT INTO ohs_sync.pull_jobs (tenant_id, user_id, provider, from_time, to_time, not_before, data_type, priority, trigger)
	VALUES ($1, $2, $3, $4, $5, COALESCE($6, now()), $7, $8, $9)
	ON CONFLICT (tenant_id, user_id, provider, from_time, to_time, (coalesce(data_type, '')))
		WHERE status IN ('pending', 'running') DO NOTHING`

// args are insertPullSQL's arguments.
func (j PullJob) args() []any {
	var nb *time.Time
	if !j.NotBefore.IsZero() {
		nb = &j.NotBefore
	}
	var dt *string
	if j.DataType != "" {
		dt = &j.DataType
	}
	prio := priorityRoutine
	switch {
	case j.Inbound:
		prio = priorityInbound
	case j.trigger() == TriggerBackfill:
		prio = priorityBackfill
	}
	return []any{j.TenantID, j.UserID, j.Provider, j.From, j.To, nb, dt, prio, j.trigger()}
}

// EnqueuePullID is EnqueuePull returning the job's id: the new job's
// (created), or the pending or running job that already covers the same
// window (not created).
func EnqueuePullID(ctx context.Context, tx pgx.Tx, j PullJob) (id int64, created bool, err error) {
	// The job a conflict met can finish before it is read back: then the
	// insert goes through on the second try.
	for range 2 {
		err = tx.QueryRow(ctx, insertPullSQL+` RETURNING id`, j.args()...).Scan(&id)
		if err == nil {
			return id, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, false, fmt.Errorf("enqueue pull job: %w", err)
		}
		err = tx.QueryRow(ctx, `
			SELECT id FROM ohs_sync.pull_jobs
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND from_time = $4 AND to_time = $5
			  AND coalesce(data_type, '') = $6 AND status IN ('pending', 'running')`,
			j.TenantID, j.UserID, j.Provider, j.From, j.To, j.DataType).Scan(&id)
		if err == nil {
			return id, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, false, fmt.Errorf("find pull job: %w", err)
		}
	}
	return 0, false, fmt.Errorf("enqueue pull job: %w", err)
}

// Pull claims and settles pull_jobs.
type Pull struct {
	pool *pgxpool.Pool
	// Limits caps the pulls running at once per provider, across every
	// instance. Zero values mean no cap.
	Limits PullLimits
}

// PullLimits is the pull budget of each provider.
type PullLimits struct {
	// PerProvider caps the running pulls of one provider.
	PerProvider int
	// BackfillPerProvider caps the running backfill pulls of one provider,
	// so new connections leave room for everyone else's regular pulls.
	BackfillPerProvider int
}

// NewPull returns the pull queue over pool, without limits.
func NewPull(pool *pgxpool.Pool) *Pull { return &Pull{pool: pool} }

// pullClaimLock serializes pull claims across instances ("ohs pclm"), so the
// per-provider counts a claim reads are not raced by another claim.
const pullClaimLock int64 = 0x6f68732070636c6d

// maxClaimCandidates bounds the jobs one claim ranks.
const maxClaimCandidates = 10000

// claimPullSQL picks up to $1 due jobs (pending and due, or running past the
// visibility timeout $2) in priority order, skipping the providers paused
// for a rate limit and the jobs past a provider's caps ($3 running pulls,
// $4 running backfills; 0 means no cap), and claims them like claimSQL.
var claimPullSQL = fmt.Sprintf(`
	WITH busy AS (
		SELECT provider, count(*) AS n, count(*) FILTER (WHERE trigger = 'backfill') AS nb
		FROM ohs_sync.pull_jobs
		WHERE status = 'running' AND claimed_at >= now() - make_interval(secs => $2::double precision)
		GROUP BY provider
	), due AS (
		SELECT id, provider, trigger, priority, not_before
		FROM ohs_sync.pull_jobs j
		WHERE ((status = 'pending' AND not_before <= now())
			OR (status = 'running' AND claimed_at < now() - make_interval(secs => $2::double precision)))
		  AND NOT EXISTS (SELECT 1 FROM ohs_core.provider_limits l
			WHERE l.provider = j.provider AND l.paused_until > now())
		ORDER BY priority, not_before, id
		LIMIT %[2]d
	), ranked AS (
		SELECT d.id, d.priority, d.not_before,
			coalesce(b.n, 0) + row_number() OVER (PARTITION BY d.provider ORDER BY d.priority, d.not_before, d.id) AS slot,
			CASE WHEN d.trigger = 'backfill' THEN coalesce(b.nb, 0) + row_number() OVER (
				PARTITION BY d.provider, d.trigger = 'backfill' ORDER BY d.priority, d.not_before, d.id) END AS bslot
		FROM due d LEFT JOIN busy b USING (provider)
	), picked AS (
		SELECT t.id, (t.status = 'running' AND t.attempts + 1 >= %[1]d) AS exhausted
		FROM ranked r JOIN ohs_sync.pull_jobs t ON t.id = r.id
		WHERE ($3 = 0 OR r.slot <= $3) AND ($4 = 0 OR r.bslot IS NULL OR r.bslot <= $4)
		ORDER BY r.priority, r.not_before, r.id
		LIMIT $1
		FOR UPDATE OF t SKIP LOCKED
	), upd AS (
		UPDATE ohs_sync.pull_jobs t SET
			attempts   = t.attempts + (t.status = 'running')::int,
			status     = CASE WHEN p.exhausted THEN 'parked' ELSE 'running' END,
			claimed_at = CASE WHEN p.exhausted THEN NULL ELSE now() END,
			last_error = CASE WHEN p.exhausted THEN 'visibility timeout expired' ELSE t.last_error END,
			finished_at = CASE WHEN p.exhausted THEN now() ELSE t.finished_at END,
			outcome    = CASE WHEN p.exhausted THEN 'failed' ELSE t.outcome END
		FROM picked p WHERE t.id = p.id
		  AND (t.status = 'pending' OR (t.status = 'running' AND t.claimed_at < now() - make_interval(secs => $2::double precision)))
		RETURNING t.*
	)
	SELECT id, tenant_id, user_id, provider, from_time, to_time, coalesce(data_type, ''), priority = 0, trigger,
		attempts, not_before, claimed_at
	FROM upd WHERE status = 'running'`, MaxAttempts, maxClaimCandidates)

// Claim takes up to n due jobs, notification and manual pulls first, then
// regular pulls, then backfills, oldest first within each, with the same
// rules as ETL.Claim. A provider paused for a rate limit (PauseProvider) is
// skipped, and so are jobs past the provider's Limits. Claims of every
// instance take turns (an advisory lock), so the caps hold across instances.
func (q *Pull) Claim(ctx context.Context, n int) ([]PullJob, error) {
	if n <= 0 {
		return nil, nil
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim pull jobs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, pullClaimLock); err != nil {
		return nil, fmt.Errorf("claim pull jobs: lock: %w", err)
	}
	rows, err := tx.Query(ctx, claimPullSQL, n, VisibilityTimeout.Seconds(), q.Limits.PerProvider, q.Limits.BackfillPerProvider)
	if err != nil {
		return nil, fmt.Errorf("claim pull jobs: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (PullJob, error) {
		var j PullJob
		err := r.Scan(&j.ID, &j.TenantID, &j.UserID, &j.Provider, &j.From, &j.To, &j.DataType, &j.Inbound,
			&j.Trigger, &j.Attempts, &j.NotBefore, &j.ClaimedAt)
		return j, err
	})
	if err != nil {
		return nil, fmt.Errorf("claim pull jobs: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim pull jobs: commit: %w", err)
	}
	return out, nil
}

// Provider pause bounds: a rate limit pauses every pull of its provider for
// its Retry-After, at least ProviderPauseBase doubled for each limit met in a
// row, and at most ProviderPauseMax (one user's daily quota must not stop
// everyone's pulls for a day; that user's job waits on its own).
const (
	ProviderPauseBase = 30 * time.Second
	ProviderPauseMax  = 15 * time.Minute
)

// PauseProvider pauses the claims of provider's pulls after a rate limit that
// asked to wait retryAfter, and returns until when.
func (q *Pull) PauseProvider(ctx context.Context, provider string, retryAfter time.Duration) (time.Time, error) {
	var until time.Time
	err := q.pool.QueryRow(ctx, `
		INSERT INTO ohs_core.provider_limits AS l (provider, paused_until, consecutive)
		VALUES ($1, now() + make_interval(secs => least(greatest($2::double precision, $3::double precision), $4::double precision)), 1)
		ON CONFLICT (provider) DO UPDATE SET
			consecutive  = l.consecutive + 1,
			paused_until = greatest(l.paused_until, now() + make_interval(secs => least(
				greatest($2::double precision, $3::double precision * power(2, l.consecutive)), $4::double precision))),
			updated_at   = now()
		RETURNING paused_until`,
		provider, retryAfter.Seconds(), ProviderPauseBase.Seconds(), ProviderPauseMax.Seconds()).Scan(&until)
	if err != nil {
		return time.Time{}, fmt.Errorf("pause provider: %w", err)
	}
	return until, nil
}

// ProviderOK resets provider's backoff after a successful pull.
func (q *Pull) ProviderOK(ctx context.Context, provider string) error {
	if _, err := q.pool.Exec(ctx, `UPDATE ohs_core.provider_limits SET consecutive = 0, updated_at = now()
		WHERE provider = $1 AND consecutive > 0`, provider); err != nil {
		return fmt.Errorf("reset provider backoff: %w", err)
	}
	return nil
}

// PausedProviders returns the providers paused now, with when they resume.
func (q *Pull) PausedProviders(ctx context.Context) (map[string]time.Time, error) {
	rows, err := q.pool.Query(ctx, `SELECT provider, paused_until FROM ohs_core.provider_limits WHERE paused_until > now()`)
	if err != nil {
		return nil, fmt.Errorf("paused providers: %w", err)
	}
	out := map[string]time.Time{}
	for rows.Next() {
		var p string
		var t time.Time
		if err := rows.Scan(&p, &t); err != nil {
			rows.Close()
			return nil, err
		}
		out[p] = t
	}
	return out, rows.Err()
}

// Complete marks the job done. It returns ErrNotRunning if the claim is gone.
func (q *Pull) Complete(ctx context.Context, j PullJob) error {
	tag, err := q.pool.Exec(ctx, `
		UPDATE ohs_sync.pull_jobs SET status = 'done', claimed_at = NULL, outcome = 'completed',
			objects_stored = $3, started_at = $2, finished_at = now()
		WHERE id = $1 AND status = 'running' AND claimed_at = $2`, j.ID, j.ClaimedAt, j.Stored)
	if err != nil {
		return fmt.Errorf("complete pull job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotRunning
	}
	return nil
}

// Fail records the error and retries after 30s*2^attempts, or parks the job
// after five failures. It returns ErrNotRunning if the claim is gone.
func (q *Pull) Fail(ctx context.Context, j PullJob, cause error) error {
	if err := fail(ctx, "pull_jobs", q.pool, j.ID, j.ClaimedAt, cause); err != nil {
		return err
	}
	// The claim is gone now; the row is identified by id alone, and nothing
	// else writes these columns.
	if _, err := q.pool.Exec(ctx, `
		UPDATE ohs_sync.pull_jobs SET outcome = 'failed', objects_stored = $2, started_at = $3,
			finished_at = CASE WHEN status = 'parked' THEN now() END
		WHERE id = $1`, j.ID, j.Stored, j.ClaimedAt); err != nil {
		return fmt.Errorf("record pull failure: %w", err)
	}
	return nil
}

// Defer puts a running job back to pending until the given time (provider
// rate limit). No attempt is burned. It returns ErrNotRunning if the claim is gone.
func (q *Pull) Defer(ctx context.Context, j PullJob, until time.Time) error {
	tag, err := q.pool.Exec(ctx, `
		UPDATE ohs_sync.pull_jobs SET status = 'pending', not_before = $3, claimed_at = NULL,
			outcome = 'rate_limited', started_at = $2
		WHERE id = $1 AND status = 'running' AND claimed_at = $2`, j.ID, j.ClaimedAt, until)
	if err != nil {
		return fmt.Errorf("defer pull job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotRunning
	}
	return nil
}
