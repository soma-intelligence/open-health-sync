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

// ETL claims and settles etl_jobs.
type ETL struct{ pool *pgxpool.Pool }

// NewETL returns the ETL queue over pool.
func NewETL(pool *pgxpool.Pool) *ETL { return &ETL{pool: pool} }

// Claim takes up to n jobs of one tier, oldest first. Pending jobs past their
// not_before and running jobs past the visibility timeout are eligible; a
// reclaimed job burns an attempt. Concurrent claimers get disjoint sets.
func (q *ETL) Claim(ctx context.Context, tier string, n int) ([]ETLJob, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := q.pool.Query(ctx,
		claimSQL("etl_jobs", "tier = $3 AND ",
			"id, tenant_id, user_id, provider, key, tier, attempts, claimed_at"),
		n, VisibilityTimeout.Seconds(), tier)
	if err != nil {
		return nil, fmt.Errorf("claim etl jobs: %w", err)
	}
	defer rows.Close()
	var out []ETLJob
	for rows.Next() {
		var j ETLJob
		if err := rows.Scan(&j.ID, &j.TenantID, &j.UserID, &j.Provider, &j.Key, &j.Tier, &j.Attempts, &j.ClaimedAt); err != nil {
			return nil, fmt.Errorf("scan etl job: %w", err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim etl jobs: %w", err)
	}
	return out, nil
}

// Complete marks the job done. It returns ErrNotRunning if the claim is gone.
func (q *ETL) Complete(ctx context.Context, j ETLJob) error {
	return complete(ctx, "etl_jobs", q.pool, j.ID, j.ClaimedAt)
}

// Fail records the error and retries the job after 30s*2^attempts, or parks
// it once it has failed five times. It returns ErrNotRunning if the claim is gone.
func (q *ETL) Fail(ctx context.Context, j ETLJob, cause error) error {
	return fail(ctx, "etl_jobs", q.pool, j.ID, j.ClaimedAt, cause)
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
