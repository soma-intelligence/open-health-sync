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
	priorityBackfill = 2 // the history of a new connection
)

// TriggerName is what started the job, one of the Trigger constants, for
// the metrics: Trigger, or what Inbound says when Trigger is empty.
func (j PullJob) TriggerName() string { return j.trigger() }

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

// notBefore is the job's not_before, nil (now) when unset.
func (j PullJob) notBefore() *time.Time {
	if j.NotBefore.IsZero() {
		return nil
	}
	return &j.NotBefore
}

// dataType is the job's data_type, nil when it pulls every type.
func (j PullJob) dataType() *string {
	if j.DataType == "" {
		return nil
	}
	return &j.DataType
}

func (j PullJob) priority() int {
	switch {
	case j.Inbound:
		return priorityInbound
	case j.trigger() == TriggerBackfill:
		return priorityBackfill
	}
	return priorityRoutine
}

// args are insertPullSQL's arguments.
func (j PullJob) args() []any {
	return []any{j.TenantID, j.UserID, j.Provider, j.From, j.To, j.notBefore(), j.dataType(), j.priority(), j.trigger()}
}

// expediteSet makes a pending job that waits out a failure due now. The
// attempt it then makes is given back (attempts - 1), so it is not counted
// against the job's retries: hurrying a job during a provider outage must
// not park it sooner than its schedule would. If the attempt fails, the job
// waits again as long as it was waiting. Only a failure's backoff is cut
// short: a job deferred for the provider's rate limit waits as asked.
const (
	expediteSet   = `not_before = now(), attempts = greatest(attempts - 1, 0)`
	expediteWhere = `status = 'pending' AND not_before > now() AND outcome = 'failed'`
)

// ExpeditePull brings pending job id forward to now when it is waiting out
// a failure (see expediteSet), and reports whether it did. Someone asked for
// this data now (a manual sync, a provider notification): a backoff of up to
// TransientBackoffMax must not swallow that.
func ExpeditePull(ctx context.Context, tx pgx.Tx, id int64) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE ohs_sync.pull_jobs SET `+expediteSet+` WHERE id = $1 AND `+expediteWhere, id)
	if err != nil {
		return false, fmt.Errorf("expedite pull job: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ExpediteCoveringPulls is ExpeditePull for the pending jobs of j's
// connection that cover j's window and data type (a job of every type covers
// any type). It returns how many it brought forward.
func ExpediteCoveringPulls(ctx context.Context, tx pgx.Tx, j PullJob) (int64, error) {
	tag, err := tx.Exec(ctx, `UPDATE ohs_sync.pull_jobs SET `+expediteSet+`
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
		  AND from_time <= $4 AND to_time >= $5 AND (data_type IS NULL OR data_type = $6)
		  AND `+expediteWhere, j.TenantID, j.UserID, j.Provider, j.From, j.To, j.DataType)
	if err != nil {
		return 0, fmt.Errorf("expedite pull jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// EnqueuePullID is EnqueuePull returning the job's id: the new job's
// (created), or the pending or running job that already covers the same
// window (not created). When j is a manual sync or a notification and the
// job it met is waiting out a failure, that job is brought forward to now
// (ExpeditePull).
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
			if j.priority() == priorityInbound {
				if _, err := ExpeditePull(ctx, tx, id); err != nil {
					return 0, false, err
				}
			}
			return id, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, false, fmt.Errorf("find pull job: %w", err)
		}
	}
	return 0, false, fmt.Errorf("enqueue pull job: %w", err)
}

// enqueuePullsBatch bounds the rows of one EnqueuePulls statement.
const enqueuePullsBatch = 5000

// EnqueuePulls adds jobs inside the caller's transaction in one statement
// (one per enqueuePullsBatch jobs), with the rules of EnqueuePull for each,
// and returns how many were added. Jobs are inserted in the order given, so
// their ids follow it.
func EnqueuePulls(ctx context.Context, tx pgx.Tx, jobs []PullJob) (int64, error) {
	var added int64
	for len(jobs) > 0 {
		batch := jobs[:min(len(jobs), enqueuePullsBatch)]
		jobs = jobs[len(batch):]
		n, err := enqueuePulls(ctx, tx, batch)
		if err != nil {
			return 0, err
		}
		added += n
	}
	return added, nil
}

func enqueuePulls(ctx context.Context, tx pgx.Tx, jobs []PullJob) (int64, error) {
	n := len(jobs)
	tenants, users := make([]uuid.UUID, n), make([]uuid.UUID, n)
	providers, triggers := make([]string, n), make([]string, n)
	froms, tos := make([]time.Time, n), make([]time.Time, n)
	notBefores, dataTypes, priorities := make([]*time.Time, n), make([]*string, n), make([]int32, n)
	for i, j := range jobs {
		tenants[i], users[i], providers[i], froms[i], tos[i] = j.TenantID, j.UserID, j.Provider, j.From, j.To
		notBefores[i], dataTypes[i], priorities[i], triggers[i] = j.notBefore(), j.dataType(), int32(j.priority()), j.trigger()
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO ohs_sync.pull_jobs (tenant_id, user_id, provider, from_time, to_time, not_before, data_type, priority, trigger)
		SELECT u.tenant_id, u.user_id, u.provider, u.from_time, u.to_time, COALESCE(u.not_before, now()), u.data_type, u.priority, u.trigger
		FROM unnest($1::uuid[], $2::uuid[], $3::text[], $4::timestamptz[], $5::timestamptz[], $6::timestamptz[],
		            $7::text[], $8::int[], $9::text[]) WITH ORDINALITY
		     AS u(tenant_id, user_id, provider, from_time, to_time, not_before, data_type, priority, trigger, ord)
		ORDER BY u.ord
		ON CONFLICT (tenant_id, user_id, provider, from_time, to_time, (coalesce(data_type, '')))
			WHERE status IN ('pending', 'running') DO NOTHING`,
		tenants, users, providers, froms, tos, notBefores, dataTypes, priorities, triggers)
	if err != nil {
		return 0, fmt.Errorf("enqueue pull jobs: %w", err)
	}
	return tag.RowsAffected(), nil
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

// reclaimPullSQL puts back the pulls whose claim expired ($1 = visibility
// timeout seconds); one out of attempts is parked as failed.
//
// The ceiling is the transient one (TransientMaxAttempts), for every pull.
// A claim expires when the instance that held it died or was redeployed,
// which says nothing against the job, and the row does not record which
// schedule its failures were on. With the short ceiling a pull four
// failures into a provider outage was parked by the next deploy, and the
// days of retries it had left were lost. A pull that fails for good is still
// parked by Fail at its fifth failure.
var reclaimPullSQL = reclaimSQL("pull_jobs", TransientMaxAttempts, "", `,
			finished_at = CASE WHEN exhausted THEN now() ELSE t.finished_at END,
			outcome    = CASE WHEN exhausted THEN 'failed' ELSE t.outcome END`)

// claimPullSQL picks up to $1 due jobs in priority order and marks them
// running. Providers paused for a rate limit are skipped, and so are the
// jobs past a provider's caps ($2 running pulls, $3 running backfills; 0
// means no cap).
//
// The caps are applied provider by provider, before anything is ranked: for
// each provider with pending jobs (prov, a loose scan of the claim index)
// and each priority, at most the provider's free slots are read, through
// pull_jobs_claim (provider, priority, not_before, id). A provider with a
// backlog far past its cap therefore costs a few rows, and cannot hide the
// jobs of the others. Backfills are the lowest priority, so the rows read
// before them never count against the backfill cap.
const claimPullSQL = `
	WITH RECURSIVE prov AS (
		(SELECT provider FROM ohs_sync.pull_jobs WHERE status = 'pending' ORDER BY provider LIMIT 1)
		UNION ALL
		SELECT (SELECT j.provider FROM ohs_sync.pull_jobs j
			WHERE j.status = 'pending' AND j.provider > p.provider ORDER BY j.provider LIMIT 1)
		FROM prov p WHERE p.provider IS NOT NULL
	), busy AS MATERIALIZED (
		SELECT provider, count(*) AS n, count(*) FILTER (WHERE trigger = 'backfill') AS nb
		FROM ohs_sync.pull_jobs
		WHERE status = 'running'
		GROUP BY provider
	), room AS (
		SELECT p.provider, coalesce(b.n, 0) AS n, coalesce(b.nb, 0) AS nb,
			CASE WHEN $2 = 0 THEN $1 ELSE least($1, $2 - coalesce(b.n, 0)) END AS free
		FROM prov p LEFT JOIN busy b USING (provider)
		WHERE p.provider IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM ohs_core.provider_limits l
			WHERE l.provider = p.provider AND l.paused_until > now())
	), due AS (
		SELECT d.id, d.provider, d.trigger, d.priority, d.not_before, r.n, r.nb
		FROM room r CROSS JOIN generate_series(0, 2) AS prio
		CROSS JOIN LATERAL (
			SELECT j.id, j.provider, j.trigger, j.priority, j.not_before
			FROM ohs_sync.pull_jobs j
			WHERE j.status = 'pending' AND j.provider = r.provider AND j.priority = prio
			  AND j.not_before <= now()
			ORDER BY j.not_before, j.id
			LIMIT greatest(r.free, 0)
		) d
	), ranked AS (
		SELECT d.id, d.priority, d.not_before,
			d.n + row_number() OVER (PARTITION BY d.provider ORDER BY d.priority, d.not_before, d.id) AS slot,
			CASE WHEN d.trigger = 'backfill' THEN d.nb + row_number() OVER (
				PARTITION BY d.provider, d.trigger = 'backfill' ORDER BY d.priority, d.not_before, d.id) END AS bslot
		FROM due d
	), picked AS (
		SELECT t.id
		FROM (
			SELECT r.id FROM ranked r
			WHERE ($2 = 0 OR r.slot <= $2) AND ($3 = 0 OR r.bslot IS NULL OR r.bslot <= $3)
			ORDER BY r.priority, r.not_before, r.id
			LIMIT $1
		) c CROSS JOIN LATERAL (
			SELECT j.id FROM ohs_sync.pull_jobs j
			WHERE j.id = c.id AND j.status = 'pending'
			FOR UPDATE SKIP LOCKED
		) t
	), upd AS (
		UPDATE ohs_sync.pull_jobs t SET status = 'running', claimed_at = now()
		FROM picked p WHERE t.id = p.id AND t.status = 'pending'
		RETURNING t.*
	)
	SELECT id, tenant_id, user_id, provider, from_time, to_time, coalesce(data_type, ''), priority = 0, trigger,
		attempts, not_before, claimed_at
	FROM upd`

// Claim takes up to n due jobs, notification and manual pulls first, then
// regular pulls, then backfills, oldest first within each. Pending jobs past
// their not_before are eligible; a running job past the visibility timeout
// is put back first, with an attempt burned, and is claimed again like any
// due job. A provider paused for a rate limit (PauseProvider) is skipped,
// and so are jobs past the provider's Limits. Claims of every instance take
// turns (an advisory lock), so the caps hold across instances.
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
	if _, err := tx.Exec(ctx, reclaimPullSQL, VisibilityTimeout.Seconds()); err != nil {
		return nil, fmt.Errorf("claim pull jobs: reclaim: %w", err)
	}
	rows, err := tx.Query(ctx, claimPullSQL, n, q.Limits.PerProvider, q.Limits.BackfillPerProvider)
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
	tag, err := settleExec(ctx, q.pool, `
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
	return q.fail(ctx, j, cause, standardBackoff)
}

// Transient failures (the provider is down or unreachable) are retried on a
// much longer schedule than Fail's: a provider outage of a day must not
// leave a hole in a user's history. The delays are 1 min, 2 min, 4 min and
// so on up to TransientBackoffMax, and the job is parked at its
// TransientMaxAttempts-th failure, about three days after the first.
const (
	TransientBackoffBase = time.Minute
	TransientBackoffMax  = 6 * time.Hour
	TransientMaxAttempts = 20
)

var transientBackoff = backoff{base: TransientBackoffBase, max: TransientBackoffMax, attempts: TransientMaxAttempts}

// FailTransient is Fail for an error that is the provider's and should
// pass: a 5xx answer, a network error, a timeout. Attempts are counted on
// the job whichever way it failed, so a job that then fails for good (Fail)
// after five or more transient failures is parked at once. A claim that
// expires counts against the transient ceiling too (reclaimPullSQL).
func (q *Pull) FailTransient(ctx context.Context, j PullJob, cause error) error {
	return q.fail(ctx, j, cause, transientBackoff)
}

func (q *Pull) fail(ctx context.Context, j PullJob, cause error, b backoff) error {
	if err := fail(ctx, "pull_jobs", q.pool, j.ID, j.ClaimedAt, cause, b); err != nil {
		return err
	}
	// The claim is gone now; the row is identified by id alone, and nothing
	// else writes these columns.
	if _, err := settleExec(ctx, q.pool, `
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
	tag, err := settleExec(ctx, q.pool, `
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

// ErrPullQueued is returned by RetryPull when a pending or running job
// already covers the parked job's window: nothing is left to retry.
var ErrPullQueued = errors.New("queue: a job for this window is already queued")

// retryPullSet queues a parked pull job again from scratch.
const retryPullSet = `status = 'pending', attempts = 0, not_before = now(), claimed_at = NULL,
	outcome = NULL, finished_at = NULL`

// noLiveTwin is the condition that no pending or running job covers the
// window of parked job t (the idempotency index allows one).
const noLiveTwin = `NOT EXISTS (SELECT 1 FROM ohs_sync.pull_jobs o
	WHERE o.tenant_id = t.tenant_id AND o.user_id = t.user_id AND o.provider = t.provider
	  AND o.from_time = t.from_time AND o.to_time = t.to_time
	  AND coalesce(o.data_type, '') = coalesce(t.data_type, '') AND o.status IN ('pending', 'running'))`

// RetryPull queues one parked pull job of the tenant again with fresh
// attempts, whatever parked it. The last error stays until the next attempt
// replaces it. It returns ErrNoJob for an id of no job of the tenant,
// ErrNotParked for a job that is pending, running or done, and ErrPullQueued
// when another job already covers the same window. A job whose connection is
// no longer active is parked again by the worker that claims it.
func RetryPull(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, id int64) error {
	tag, err := pool.Exec(ctx, `UPDATE ohs_sync.pull_jobs t SET `+retryPullSet+`
		WHERE t.tenant_id = $1 AND t.id = $2 AND t.status = 'parked' AND `+noLiveTwin, tenantID, id)
	if err != nil {
		return fmt.Errorf("retry pull job: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var status string
	err = pool.QueryRow(ctx, `SELECT status FROM ohs_sync.pull_jobs WHERE tenant_id = $1 AND id = $2`, tenantID, id).Scan(&status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNoJob
	case err != nil:
		return fmt.Errorf("retry pull job: %w", err)
	case status == "parked":
		return ErrPullQueued
	}
	return ErrNotParked
}

// RetryParkedPulls queues again the parked pull jobs of one connection that
// gave up on an error: attempts spent, or the grant refused. Jobs cancelled
// because the connection stopped (OutcomeInactive) stay parked, and so does
// a job whose window a pending or running job already covers. Of several
// parked jobs for one window only the latest is queued. It reports how many
// jobs were queued.
func RetryParkedPulls(ctx context.Context, pool *pgxpool.Pool, tenantID, userID uuid.UUID, provider string) (int64, error) {
	tag, err := pool.Exec(ctx, `UPDATE ohs_sync.pull_jobs t SET `+retryPullSet+`
		WHERE t.id IN (
			SELECT DISTINCT ON (from_time, to_time, coalesce(data_type, '')) id
			FROM ohs_sync.pull_jobs
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status = 'parked'
			  AND coalesce(outcome, '') <> '`+OutcomeInactive+`'
			ORDER BY from_time, to_time, coalesce(data_type, ''), id DESC)
		  AND t.status = 'parked' AND `+noLiveTwin, tenantID, userID, provider)
	if err != nil {
		return 0, fmt.Errorf("retry parked pull jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}
