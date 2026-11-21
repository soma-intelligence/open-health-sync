// SPDX-License-Identifier: AGPL-3.0-only

package automation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ReconcileWindow bounds the unprocessed-object probe. It answers "is the
// pipeline losing objects right now", not a question about all of history: the
// orphan scanner and the drainer own the old backlog, and a probe over every
// object ever ingested would slow down for ever.
const ReconcileWindow = 7 * 24 * time.Hour

// StaleAfter is how long a raw object may wait for a finished job before the
// reconciler counts it as unprocessed. It is long enough for a normal backlog
// to drain.
const StaleAfter = time.Hour

// ReconcileFullEvery is how often the unprocessed-object probe reads the whole
// window again. Between two full passes it only reads the objects created
// since the oldest one it last found unprocessed (see Reconciler).
const ReconcileFullEvery = time.Hour

// scanOverlap is how far behind its mark an incremental scan starts again. A
// raw_objects row takes created_at from the start of its transaction, so a row
// committed late appears behind rows already seen; the overlap covers a
// transaction this long, and the periodic full pass anything slower.
const scanOverlap = 10 * time.Minute

// Reconciler asserts, from the tracking tables, that nothing the pipeline took
// in is silently stuck, and publishes the answers as metrics. It never changes
// data.
type Reconciler struct {
	Pool    *pgxpool.Pool
	Metrics *Metrics
	Now     func() time.Time
	// Window and Stale override ReconcileWindow and StaleAfter when positive.
	Window, Stale time.Duration
	// FullEvery overrides ReconcileFullEvery when positive.
	FullEvery time.Duration

	// The unprocessed-object probe is incremental. mark is a time before which
	// every object of the window had a finished job at the last look, so a pass
	// only reads from there: a few minutes of objects when the pipeline is
	// healthy, and from the oldest unprocessed object when it is not. The mark
	// lives in memory only. A new process starts with a full pass, and a full
	// pass runs again every FullEvery, which also catches an object that went
	// back to unprocessed behind the mark (its job replayed).
	mu       sync.Mutex
	mark     time.Time
	lastFull time.Time
}

// Report is the outcome of one pass. Healthy is all zeros.
type Report struct {
	// PendingFailedRecords are records known dropped and not yet recovered.
	PendingFailedRecords int64
	// ParkedJobs are etl_jobs that failed every attempt and wait for an operator.
	ParkedJobs int64
	// UnprocessedObjects are raw objects older than StaleAfter, inside the
	// window, with no finished job.
	UnprocessedObjects int64
}

// Run counts the three signals, updates the metrics and logs what is wrong.
func (r *Reconciler) Run(ctx context.Context) (Report, error) {
	var rep Report
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	window, stale := r.Window, r.Stale
	if window <= 0 {
		window = ReconcileWindow
	}
	if stale <= 0 {
		stale = StaleAfter
	}

	fullEvery := r.FullEvery
	if fullEvery <= 0 {
		fullEvery = ReconcileFullEvery
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	since, before := now.Add(-window), now.Add(-stale)
	full := r.mark.IsZero() || now.Sub(r.lastFull) >= fullEvery
	if !full && r.mark.After(since) {
		since = r.mark
	}

	var oldest *time.Time
	err := r.Pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM ohs_data.failed_records WHERE status = 'pending'),
		  (SELECT count(*) FROM ohs_sync.etl_jobs WHERE status = 'parked'),
		  u.n, u.oldest
		FROM (SELECT count(*) AS n, min(o.created_at) AS oldest FROM ohs_sync.raw_objects o
		       WHERE o.created_at >= $1 AND o.created_at < $2
		         AND NOT EXISTS (SELECT 1 FROM ohs_sync.etl_jobs j WHERE j.key = o.key AND j.status = 'done')) u`,
		since, before).
		Scan(&rep.PendingFailedRecords, &rep.ParkedJobs, &rep.UnprocessedObjects, &oldest)
	if err != nil {
		return rep, fmt.Errorf("reconcile: %w", err)
	}
	// Everything before the oldest unprocessed object is finished; with none,
	// everything before the stale bound is.
	mark := before
	if oldest != nil && oldest.Before(mark) {
		mark = *oldest
	}
	r.mark = mark.Add(-scanOverlap)
	if full {
		r.lastFull = now
	}

	if m := r.Metrics; m != nil {
		m.PendingFailedRecords.Store(rep.PendingFailedRecords)
		m.ParkedJobs.Store(rep.ParkedJobs)
		m.UnprocessedObjects.Store(rep.UnprocessedObjects)
		m.LastReconcileUnix.Store(now.Unix())
	}
	if rep.PendingFailedRecords > 0 {
		slog.WarnContext(ctx, "unrecovered failed records", "pending", rep.PendingFailedRecords)
	}
	if rep.ParkedJobs > 0 {
		slog.WarnContext(ctx, "parked etl jobs", "parked", rep.ParkedJobs)
	}
	if rep.UnprocessedObjects > 0 {
		slog.WarnContext(ctx, "raw objects without a finished job",
			"count", rep.UnprocessedObjects, "window", window, "stale_after", stale)
	}
	slog.InfoContext(ctx, "reconciliation complete",
		"pending_failed_records", rep.PendingFailedRecords,
		"parked_jobs", rep.ParkedJobs,
		"unprocessed_objects", rep.UnprocessedObjects)
	return rep, nil
}
