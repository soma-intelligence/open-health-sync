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

// ETL tiers.
const (
	TierStandard = "standard"
	TierHighFreq = "high_freq"
)

// ETLJob is one raw object waiting to be turned into canonical records.
type ETLJob struct {
	ID       int64
	TenantID uuid.UUID
	UserID   uuid.UUID
	Provider string
	Key      string
	Tier     string
	Attempts int
	// ClaimedAt is set by Claim and fences Complete and Fail.
	ClaimedAt time.Time
}

// EnqueueETL adds a job inside the caller's transaction, so it commits with
// the raw_objects row it belongs to. An empty Tier means standard.
func EnqueueETL(ctx context.Context, tx pgx.Tx, j ETLJob) error {
	if j.Tier == "" {
		j.Tier = TierStandard
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO ohs_sync.etl_jobs (tenant_id, user_id, provider, key, tier)
		VALUES ($1, $2, $3, $4, $5)`,
		j.TenantID, j.UserID, j.Provider, j.Key, j.Tier)
	if err != nil {
		return fmt.Errorf("enqueue etl job: %w", err)
	}
	return nil
}

// DefaultETLPerUser is how many ETL jobs of one user and tier run at once.
const DefaultETLPerUser = 1

// ETL claims and settles etl_jobs.
type ETL struct {
	pool *pgxpool.Pool
	// PerUser caps the jobs of one user and tier running at once, across
	// every instance. Zero means DefaultETLPerUser; a negative value means
	// no cap. The writers of one user take turns on a lock anyway, so more
	// than one job per user holds worker slots without writing faster, and
	// one user's large import would keep every other user waiting.
	PerUser int
	// Hooks observe the claims and the settles. Set them before the first
	// Claim.
	Hooks ETLHooks
}

// ETLHooks tell the process what its ETL workers do, without the workers
// knowing: the queue is where every worker loop comes round and where every
// job starts and ends. A nil hook is skipped.
type ETLHooks struct {
	// Poll runs at the start of every Claim of tier: the heartbeat of that
	// tier's worker loop.
	Poll func(tier string)
	// Claimed runs for each job a Claim returns.
	Claimed func(j ETLJob)
	// Settled runs once Complete or Fail has answered for a job, with how it
	// ended: completed, failed, claim_lost (ErrNotRunning) or settle_error.
	Settled func(j ETLJob, outcome string)
}

// settled reports the end of j: outcome is what the worker meant (completed
// or failed), err what the settle statement answered.
func (q *ETL) settled(j ETLJob, outcome string, err error) {
	if q.Hooks.Settled == nil {
		return
	}
	switch {
	case errors.Is(err, ErrNotRunning):
		outcome = "claim_lost"
	case err != nil:
		outcome = "settle_error"
	}
	q.Hooks.Settled(j, outcome)
}

// NewETL returns the ETL queue over pool.
func NewETL(pool *pgxpool.Pool) *ETL { return &ETL{pool: pool} }

func (q *ETL) perUser() int {
	switch {
	case q.PerUser == 0:
		return DefaultETLPerUser
	case q.PerUser < 0:
		return 0
	}
	return q.PerUser
}

// etlClaimLock serializes the claims of one tier ($1) across instances, so
// the per-user counts a claim reads are not raced by another claim.
const etlClaimLock = `SELECT pg_advisory_xact_lock(hashtextextended('ohs_sync.etl_jobs:claim:' || $1, 0))`

// etlClaimWindow is how many due jobs a claim of n looks at. Jobs of users
// at their cap are skipped before the window, so a backlog of theirs never
// fills it; a user below the cap can fill it alone, and then the claim
// returns fewer than n jobs and the next one moves past that user.
func etlClaimWindow(n int) int { return max(16*n, 256) }

var reclaimETLSQL = reclaimSQL("etl_jobs", MaxAttempts, "tier = $2 AND ", "")

// claimETLSQL picks up to $1 due jobs of tier $3, oldest first, at most $2
// running per user (0 means no cap) counting the jobs already running, out
// of a window of $4 due jobs, and marks them running.
const claimETLSQL = `
	WITH busy AS MATERIALIZED (
		SELECT tenant_id, user_id, count(*) AS n
		FROM ohs_sync.etl_jobs
		WHERE status = 'running' AND tier = $3
		GROUP BY tenant_id, user_id
	), due AS (
		SELECT j.id, j.tenant_id, j.user_id, j.not_before
		FROM ohs_sync.etl_jobs j
		WHERE j.tier = $3 AND j.status = 'pending' AND j.not_before <= now()
		  AND (j.tenant_id, j.user_id) NOT IN (SELECT b.tenant_id, b.user_id FROM busy b WHERE $2 > 0 AND b.n >= $2)
		ORDER BY j.not_before, j.id
		LIMIT $4
	), ranked AS (
		SELECT d.id, d.not_before, coalesce(b.n, 0) + row_number() OVER (
			PARTITION BY d.tenant_id, d.user_id ORDER BY d.not_before, d.id) AS slot
		FROM due d LEFT JOIN busy b USING (tenant_id, user_id)
	), picked AS (
		SELECT t.id
		FROM (
			SELECT r.id FROM ranked r
			WHERE $2 = 0 OR r.slot <= $2
			ORDER BY r.not_before, r.id
			LIMIT $1
		) c CROSS JOIN LATERAL (
			SELECT j.id FROM ohs_sync.etl_jobs j
			WHERE j.id = c.id AND j.status = 'pending'
			FOR UPDATE SKIP LOCKED
		) t
	), upd AS (
		UPDATE ohs_sync.etl_jobs t SET status = 'running', claimed_at = now()
		FROM picked p WHERE t.id = p.id AND t.status = 'pending'
		RETURNING t.*
	)
	SELECT id, tenant_id, user_id, provider, key, tier, attempts, claimed_at FROM upd`

// Claim takes up to n jobs of one tier, oldest first, at most PerUser of
// one user at a time: a user's jobs run in queue order (not_before, then
// id), and the jobs of a user at the cap are passed over, so one user's
// backlog does not keep the others waiting. Pending jobs past their
// not_before are eligible. A running job past the visibility timeout is put
// back first, with an attempt burned, and is claimed again like any due job.
// Concurrent claimers get disjoint sets; claims of one tier take turns (an
// advisory lock), so the cap holds across instances. A claim can return
// fewer than n jobs while more are due.
func (q *ETL) Claim(ctx context.Context, tier string, n int) ([]ETLJob, error) {
	if q.Hooks.Poll != nil {
		q.Hooks.Poll(tier)
	}
	if n <= 0 {
		return nil, nil
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim etl jobs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, etlClaimLock, tier); err != nil {
		return nil, fmt.Errorf("claim etl jobs: lock: %w", err)
	}
	if _, err := tx.Exec(ctx, reclaimETLSQL, VisibilityTimeout.Seconds(), tier); err != nil {
		return nil, fmt.Errorf("claim etl jobs: reclaim: %w", err)
	}
	rows, err := tx.Query(ctx, claimETLSQL, n, q.perUser(), tier, etlClaimWindow(n))
	if err != nil {
		return nil, fmt.Errorf("claim etl jobs: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ETLJob, error) {
		var j ETLJob
		err := r.Scan(&j.ID, &j.TenantID, &j.UserID, &j.Provider, &j.Key, &j.Tier, &j.Attempts, &j.ClaimedAt)
		return j, err
	})
	if err != nil {
		return nil, fmt.Errorf("claim etl jobs: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim etl jobs: commit: %w", err)
	}
	if q.Hooks.Claimed != nil {
		for _, j := range out {
			q.Hooks.Claimed(j)
		}
	}
	return out, nil
}

// Complete marks the job done. It returns ErrNotRunning if the claim is gone.
func (q *ETL) Complete(ctx context.Context, j ETLJob) error {
	err := complete(ctx, "etl_jobs", q.pool, j.ID, j.ClaimedAt)
	q.settled(j, "completed", err)
	return err
}

// Fail records the error and retries the job after 30s*2^attempts, or parks
// it once it has failed five times. It returns ErrNotRunning if the claim is gone.
func (q *ETL) Fail(ctx context.Context, j ETLJob, cause error) error {
	err := fail(ctx, "etl_jobs", q.pool, j.ID, j.ClaimedAt, cause, standardBackoff)
	q.settled(j, "failed", err)
	return err
}

// ErrNotParked is returned by RetryETL for a job that is not parked: it is
// still pending or running, or it is done.
var ErrNotParked = errors.New("queue: job is not parked")

// ErrNoJob is returned by RetryETL for an id of no job of the tenant.
var ErrNoJob = errors.New("queue: no such job")

// ETLJobInfo describes one etl_jobs row for an operator.
type ETLJobInfo struct {
	ID        int64
	UserID    uuid.UUID
	Provider  string
	Tier      string
	Status    string
	Attempts  int
	LastError *string
	CreatedAt time.Time
	// NotBefore is when a pending job may run next.
	NotBefore time.Time
}

const etlInfoCols = `id, user_id, provider, tier, status, attempts, last_error, created_at, not_before`

func scanETLInfo(row pgx.Row) (ETLJobInfo, error) {
	var j ETLJobInfo
	err := row.Scan(&j.ID, &j.UserID, &j.Provider, &j.Tier, &j.Status, &j.Attempts, &j.LastError, &j.CreatedAt, &j.NotBefore)
	j.CreatedAt, j.NotBefore = j.CreatedAt.UTC(), j.NotBefore.UTC()
	return j, err
}

// ListETL lists a tenant's jobs of one status (pending, running or parked),
// newest first, optionally of one provider. before (an id) continues a page.
// It returns limit+1 rows at most, so the caller knows whether more follow.
func ListETL(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, status, provider string, before int64, limit int) ([]ETLJobInfo, error) {
	rows, err := pool.Query(ctx, `SELECT `+etlInfoCols+` FROM ohs_sync.etl_jobs
		WHERE tenant_id = $1 AND status = $2 AND ($3 = '' OR provider = $3) AND ($4 = 0 OR id < $4)
		ORDER BY id DESC LIMIT $5`, tenantID, status, provider, before, limit+1)
	if err != nil {
		return nil, fmt.Errorf("list etl jobs: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ETLJobInfo, error) { return scanETLInfo(r) })
	if err != nil {
		return nil, fmt.Errorf("list etl jobs: %w", err)
	}
	return out, nil
}

// RetryETL queues one parked job of the tenant again with fresh attempts.
// The last error stays until the next attempt replaces it.
func RetryETL(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, id int64) (ETLJobInfo, error) {
	j, err := scanETLInfo(pool.QueryRow(ctx, `UPDATE ohs_sync.etl_jobs
		SET status = 'pending', attempts = 0, not_before = now(), claimed_at = NULL
		WHERE tenant_id = $1 AND id = $2 AND status = 'parked'
		RETURNING `+etlInfoCols, tenantID, id))
	if !errors.Is(err, pgx.ErrNoRows) {
		if err != nil {
			return ETLJobInfo{}, fmt.Errorf("retry etl job: %w", err)
		}
		return j, nil
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ohs_sync.etl_jobs WHERE tenant_id = $1 AND id = $2)`,
		tenantID, id).Scan(&exists); err != nil {
		return ETLJobInfo{}, fmt.Errorf("retry etl job: %w", err)
	}
	if !exists {
		return ETLJobInfo{}, ErrNoJob
	}
	return ETLJobInfo{}, ErrNotParked
}

// RetryParkedETL queues every parked job of the tenant again, or of one
// provider when provider is set, and reports how many.
func RetryParkedETL(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, provider string) (int64, error) {
	tag, err := pool.Exec(ctx, `UPDATE ohs_sync.etl_jobs
		SET status = 'pending', attempts = 0, not_before = now(), claimed_at = NULL
		WHERE tenant_id = $1 AND status = 'parked' AND ($2 = '' OR provider = $2)`, tenantID, provider)
	if err != nil {
		return 0, fmt.Errorf("retry parked etl jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}
