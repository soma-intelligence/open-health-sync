// SPDX-License-Identifier: AGPL-3.0-only

package automation

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/logging"
)

const (
	// DefaultTrackingRetentionDays is how long sync_log rows and finished
	// etl_jobs are kept when the operator sets nothing.
	DefaultTrackingRetentionDays = 90
	// MinTrackingRetentionDays is the shortest age the pruner accepts. The
	// reconciler and the orphan scanner read the jobs of the last seven days
	// (ReconcileWindow, OrphanWindow): a finished job pruned inside that
	// window would make its raw object look unprocessed, and the orphan
	// scanner would queue it again.
	MinTrackingRetentionDays = 14
	// trackingPruneBatch bounds the rows of one delete, so a first run over
	// years of history is many short transactions, not one long one.
	trackingPruneBatch = 10000
)

// TrackingPruner deletes the pipeline's own bookkeeping by age: sync_log rows
// (one or more per pull and per processed object) and finished etl_jobs. Both
// tables only ever grew. Jobs that are pending, running or parked are never
// touched, whatever their age: a parked job waits for an operator.
//
// It is independent of data retention (OHS_RETENTION_DAYS): no health record
// and no raw object is deleted here.
type TrackingPruner struct {
	Pool *pgxpool.Pool
	// Days is the age past which rows go. Values under
	// MinTrackingRetentionDays are raised to it; zero or less does nothing.
	Days    int
	Metrics *Metrics
	Now     func() time.Time
}

// TrackingPruneResult is the outcome of one run.
type TrackingPruneResult struct {
	SyncLog int64 // sync_log rows deleted
	Jobs    int64 // finished etl_jobs deleted
}

// Run deletes the rows older than Days, in batches.
func (p *TrackingPruner) Run(ctx context.Context) (TrackingPruneResult, error) {
	var res TrackingPruneResult
	if p.Days <= 0 {
		return res, nil
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	cutoff := now.UTC().AddDate(0, 0, -max(p.Days, MinTrackingRetentionDays))

	var err error
	if res.SyncLog, err = deleteBatches(ctx, p.Pool, `
		DELETE FROM ohs_sync.sync_log WHERE id IN (
			SELECT id FROM ohs_sync.sync_log WHERE created_at < $1 ORDER BY created_at LIMIT $2)`, cutoff); err != nil {
		return res, fmt.Errorf("prune sync_log: %w", err)
	}
	if res.Jobs, err = deleteBatches(ctx, p.Pool, `
		DELETE FROM ohs_sync.etl_jobs WHERE id IN (
			SELECT id FROM ohs_sync.etl_jobs WHERE status = 'done' AND created_at < $1 LIMIT $2)`, cutoff); err != nil {
		return res, fmt.Errorf("prune etl_jobs: %w", err)
	}
	if m := p.Metrics; m != nil {
		m.TrackingRowsPruned.Add(res.SyncLog + res.Jobs)
	}
	if res.SyncLog+res.Jobs > 0 {
		logging.Ctx(ctx).Info().Int64("sync_log", res.SyncLog).Int64("etl_jobs", res.Jobs).Time("older_than", cutoff).Msg("tracking_rows_pruned")
	}
	return res, nil
}

// deleteBatches runs a delete that takes a cutoff ($1) and a row limit ($2)
// until it finds nothing more, and returns the rows deleted.
func deleteBatches(ctx context.Context, pool *pgxpool.Pool, sql string, cutoff time.Time) (int64, error) {
	var total int64
	for {
		tag, err := pool.Exec(ctx, sql, cutoff, trackingPruneBatch)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < trackingPruneBatch {
			return total, nil
		}
	}
}
