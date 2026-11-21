// SPDX-License-Identifier: AGPL-3.0-only

package automation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

const (
	// orphanBatch bounds one page of raw_objects rows.
	orphanBatch = 500
	// orphanGrace keeps the scanner away from rows being registered right now.
	orphanGrace = time.Minute
	// providerAppleHealth is the connector slug whose uploads are high volume.
	providerAppleHealth = "apple_health"
)

// OrphanScanner finds raw objects that were registered but never queued and
// queues them. A raw_objects row with any etl_jobs row for its key, whatever
// the status, is not an orphan: a parked job is a decision for an operator,
// not something to retry in a loop.
//
// Only rows created in the last OrphanWindow are examined: an older row
// without a job is not a lost upload, and the anti-join stays bounded. The
// opposite check (store files without a row) is FileScanner.
//
// The scan is incremental: after a pass that read its whole range without an
// error, the next one starts where it ended (less scanOverlap), so a run reads
// the rows of the last minutes, not seven days of them. The mark lives in
// memory only: a new process scans the whole window first, so nothing is
// missed across a crash, and the whole window is scanned again every
// FullEvery to catch what an incremental pass cannot see (a row committed
// later than the overlap, a user who became active again).
type OrphanScanner struct {
	Pool    *pgxpool.Pool
	Metrics *Metrics
	Now     func() time.Time
	// Grace overrides orphanGrace when positive.
	Grace time.Duration
	// FullEvery overrides OrphanFullEvery when positive.
	FullEvery time.Duration

	mu       sync.Mutex
	mark     time.Time // rows created before it were examined by an earlier pass
	lastFull time.Time
}

// OrphanResult is the outcome of one scan.
type OrphanResult struct {
	Enqueued int // jobs created
}

// OrphanWindow bounds how far back the scan looks.
const OrphanWindow = 7 * 24 * time.Hour

// OrphanFullEvery is how often the scan reads the whole OrphanWindow again.
const OrphanFullEvery = 6 * time.Hour

// Run queues the orphans found in one scan.
func (s *OrphanScanner) Run(ctx context.Context) (OrphanResult, error) {
	var res OrphanResult
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	grace := s.Grace
	if grace <= 0 {
		grace = orphanGrace
	}

	fullEvery := s.FullEvery
	if fullEvery <= 0 {
		fullEvery = OrphanFullEvery
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	since, before := now.Add(-OrphanWindow), now.Add(-grace)
	full := s.mark.IsZero() || now.Sub(s.lastFull) >= fullEvery
	if !full && s.mark.After(since) {
		since = s.mark
	}

	n, err := s.enqueueOrphans(ctx, since, before)
	res.Enqueued = n
	if s.Metrics != nil {
		s.Metrics.OrphansEnqueued.Add(int64(n))
	}
	if err != nil {
		return res, err
	}
	// Only a pass that finished moves the mark: after a failure the same
	// range is read again.
	s.mark = before.Add(-scanOverlap)
	if full {
		s.lastFull = now
	}
	if n > 0 {
		slog.InfoContext(ctx, "orphan raw objects queued", "jobs", n)
	}

	return res, nil
}

type rawRow struct {
	id       int64
	tenant   uuid.UUID
	user     uuid.UUID
	provider string
	key      string
}

// enqueueOrphans walks raw_objects by id, one page at a time, and queues every
// row created in [since, before) that has no job.
func (s *OrphanScanner) enqueueOrphans(ctx context.Context, since, before time.Time) (int, error) {
	total := 0
	var after int64
	for {
		rows, err := s.Pool.Query(ctx, `
			SELECT o.id, o.tenant_id, o.user_id, o.provider, o.key
			FROM ohs_sync.raw_objects o
			WHERE o.id > $1 AND o.created_at >= $4 AND o.created_at < $2
			  AND NOT EXISTS (SELECT 1 FROM ohs_sync.etl_jobs j WHERE j.key = o.key)
			ORDER BY o.id
			LIMIT $3`, after, before, orphanBatch, since)
		if err != nil {
			return total, fmt.Errorf("find orphan raw objects: %w", err)
		}
		var page []rawRow
		for rows.Next() {
			var r rawRow
			if err := rows.Scan(&r.id, &r.tenant, &r.user, &r.provider, &r.key); err != nil {
				rows.Close()
				return total, fmt.Errorf("scan orphan raw object: %w", err)
			}
			page = append(page, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return total, fmt.Errorf("find orphan raw objects: %w", err)
		}
		if len(page) == 0 {
			return total, nil
		}
		for _, r := range page {
			ok, err := s.enqueueOne(ctx, r)
			if err != nil {
				return total, err
			}
			if ok {
				total++
			}
		}
		after = page[len(page)-1].id
	}
}

// enqueueOne queues r unless a job appeared since the page was read or its
// user is gone or being deleted (checked under a share lock held until the
// insert commits, so a deletion cannot miss the job). The advisory lock on the key serialises two scanners (two instances) so the
// recheck and the insert cannot both pass.
func (s *OrphanScanner) enqueueOne(ctx context.Context, r rawRow) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if active, err := lockActiveUser(ctx, tx, r.tenant, r.user); err != nil || !active {
		return false, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "etl_jobs:"+r.key); err != nil {
		return false, fmt.Errorf("lock key: %w", err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ohs_sync.etl_jobs WHERE key = $1)`, r.key).Scan(&exists); err != nil {
		return false, fmt.Errorf("recheck job: %w", err)
	}
	if exists {
		return false, nil
	}
	if err := queue.EnqueueETL(ctx, tx, queue.ETLJob{
		TenantID: r.tenant, UserID: r.user, Provider: r.provider, Key: r.key, Tier: tierFor(r.provider),
	}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

// tierFor puts the high volume provider on its own tier so it cannot starve
// the others.
func tierFor(provider string) string {
	if provider == providerAppleHealth {
		return queue.TierHighFreq
	}
	return queue.TierStandard
}

// lockActiveUser takes a share lock on the end user row until tx ends and
// reports whether the user is active. A user being deleted, or gone, gets no
// new job: the deletion worker would have nothing left to sweep it with.
func lockActiveUser(ctx context.Context, tx pgx.Tx, tenant, user uuid.UUID) (bool, error) {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR SHARE`,
		tenant, user).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock user: %w", err)
	}
	return status == "active", nil
}
