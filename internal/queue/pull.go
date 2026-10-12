// SPDX-License-Identifier: AGPL-3.0-only

package queue

import (
	"context"
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
	Inbound   bool
	Attempts  int
	NotBefore time.Time
	// ClaimedAt is set by Claim and fences Complete, Fail and Defer.
	ClaimedAt time.Time
}

// Pull job priorities (pull_jobs.priority), claimed lowest first.
const (
	priorityInbound = 0
	priorityRoutine = 1
)

// EnqueuePull adds a job inside the caller's transaction. A pending or running
// job for the same user, provider, window and data type makes this a no-op.
// A zero NotBefore means now.
func EnqueuePull(ctx context.Context, tx pgx.Tx, j PullJob) error {
	var nb *time.Time
	if !j.NotBefore.IsZero() {
		nb = &j.NotBefore
	}
	var dt *string
	if j.DataType != "" {
		dt = &j.DataType
	}
	prio := priorityRoutine
	if j.Inbound {
		prio = priorityInbound
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO ohs_sync.pull_jobs (tenant_id, user_id, provider, from_time, to_time, not_before, data_type, priority)
		VALUES ($1, $2, $3, $4, $5, COALESCE($6, now()), $7, $8)
		ON CONFLICT (tenant_id, user_id, provider, from_time, to_time, (coalesce(data_type, '')))
			WHERE status IN ('pending', 'running') DO NOTHING`,
		j.TenantID, j.UserID, j.Provider, j.From, j.To, nb, dt, prio)
	if err != nil {
		return fmt.Errorf("enqueue pull job: %w", err)
	}
	return nil
}

// Pull claims and settles pull_jobs.
type Pull struct{ pool *pgxpool.Pool }

// NewPull returns the pull queue over pool.
func NewPull(pool *pgxpool.Pool) *Pull { return &Pull{pool: pool} }

// Claim takes up to n due jobs, notification pulls first and then oldest
// first, with the same rules as ETL.Claim.
func (q *Pull) Claim(ctx context.Context, n int) ([]PullJob, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := q.pool.Query(ctx,
		claimSQLOrdered("pull_jobs", "",
			"id, tenant_id, user_id, provider, from_time, to_time, coalesce(data_type, ''), priority = 0, attempts, not_before, claimed_at",
			"priority, not_before, id"),
		n, VisibilityTimeout.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim pull jobs: %w", err)
	}
	defer rows.Close()
	var out []PullJob
	for rows.Next() {
		var j PullJob
		if err := rows.Scan(&j.ID, &j.TenantID, &j.UserID, &j.Provider, &j.From, &j.To, &j.DataType, &j.Inbound, &j.Attempts, &j.NotBefore, &j.ClaimedAt); err != nil {
			return nil, fmt.Errorf("scan pull job: %w", err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim pull jobs: %w", err)
	}
	return out, nil
}

// Complete marks the job done. It returns ErrNotRunning if the claim is gone.
func (q *Pull) Complete(ctx context.Context, j PullJob) error {
	return complete(ctx, "pull_jobs", q.pool, j.ID, j.ClaimedAt)
}

// Fail records the error and retries after 30s*2^attempts, or parks the job
// after five failures. It returns ErrNotRunning if the claim is gone.
func (q *Pull) Fail(ctx context.Context, j PullJob, cause error) error {
	return fail(ctx, "pull_jobs", q.pool, j.ID, j.ClaimedAt, cause)
}

// Defer puts a running job back to pending until the given time (provider
// rate limit). No attempt is burned. It returns ErrNotRunning if the claim is gone.
func (q *Pull) Defer(ctx context.Context, j PullJob, until time.Time) error {
	tag, err := q.pool.Exec(ctx, `
		UPDATE ohs_sync.pull_jobs SET status = 'pending', not_before = $3, claimed_at = NULL
		WHERE id = $1 AND status = 'running' AND claimed_at = $2`, j.ID, j.ClaimedAt, until)
	if err != nil {
		return fmt.Errorf("defer pull job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotRunning
	}
	return nil
}
