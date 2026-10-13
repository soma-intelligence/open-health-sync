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
	priorityInbound = 0
	priorityRoutine = 1
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
	if j.Inbound {
		prio = priorityInbound
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
			"id, tenant_id, user_id, provider, from_time, to_time, coalesce(data_type, ''), priority = 0, trigger, attempts, not_before, claimed_at",
			"priority, not_before, id"),
		n, VisibilityTimeout.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim pull jobs: %w", err)
	}
	defer rows.Close()
	var out []PullJob
	for rows.Next() {
		var j PullJob
		if err := rows.Scan(&j.ID, &j.TenantID, &j.UserID, &j.Provider, &j.From, &j.To, &j.DataType, &j.Inbound, &j.Trigger, &j.Attempts, &j.NotBefore, &j.ClaimedAt); err != nil {
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
