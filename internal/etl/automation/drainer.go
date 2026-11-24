// SPDX-License-Identifier: AGPL-3.0-only

package automation

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/soma-intelligence/open-health-sync/internal/logging"
	"github.com/soma-intelligence/open-health-sync/internal/logutil"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// permanentReasons are the reason codes whose failures are deterministic: the
// same bytes fail the same way on every replay, so they are closed as
// 'permanent' (kept with their payload for inspection) and never retried.
const permanentReasons = "('parse', 'validation')"

// Drainer drives pending failed_records to a terminal state so the
// reconciler's pending count can reach zero.
//
// Per raw object, on every pass:
//
//   - rows with a deterministic reason (parse, validation) become 'permanent';
//   - if the object has recoverable rows (mapping, unit, unknown, job, or no
//     code) it is replayed by queueing a fresh ETL job for its key, once per
//     process lifetime (see Since), and those rows become 'recovered';
//   - if the object's raw_objects row is gone (retention deleted it), nothing
//     can ever replay it and the rows become 'permanent'.
//
// "Recoverable" means a code fix may rescue the record, not that a retry can.
// Replaying the same bytes through the same binary fails identically and writes
// a fresh pending row, so without the once-per-process gate the loop would
// never end. A fix only reaches the process through a restart, so process
// start is the replay boundary: rows created after Since wait for the next
// start.
//
// A replay re-inserts the failed_records of the lines that still fail, and a
// replay of an object with several earlier failures leaves several rows per
// line. The drainer groups by raw key, so duplicates cost nothing: one job per
// object per pass, every duplicate row closed together.
type Drainer struct {
	Pool    *pgxpool.Pool
	Metrics *Metrics
	// Since is the process start. An object whose newest pending failure is
	// not older than it has had its replay under this binary already.
	Since time.Time
}

// DrainResult is the outcome of one pass.
type DrainResult struct {
	Objects         int // objects with pending failures
	Replayed        int // jobs queued (an object with a live job counts as replayed without a new one)
	AwaitingRestart int // recoverable objects held back by Since
	Recovered       int // rows closed as recovered
	Permanent       int // rows closed as permanent
}

type pendingObject struct {
	tenant, user uuid.UUID
	provider     string
	key          string
	recoverable  int64
	permanent    int64
	// newest is the newest recoverable pending row (zero if there is none); it
	// alone decides the replay gate, so a fresh permanent row cannot hold back
	// older recoverable ones. newestAll bounds the rows closed.
	newest    time.Time
	newestAll time.Time
}

// Run does one pass. A failure on one object is logged and the others are
// still tried, the first error is returned at the end.
func (d *Drainer) Run(ctx context.Context) (DrainResult, error) {
	var res DrainResult
	objs, err := d.pending(ctx)
	if err != nil {
		return res, err
	}
	res.Objects = len(objs)
	var firstErr error
	for _, o := range objs {
		if err := d.drainOne(ctx, o, &res); err != nil {
			logutil.ErrCtx(ctx, *logging.Ctx(ctx), err, zerolog.ErrorLevel).Str("key", o.key).Msg("drain_raw_object_failed")
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if res.Objects > 0 {
		logging.Ctx(ctx).Info().Int("objects", res.Objects).Int("replayed", res.Replayed).Int("awaiting_restart", res.AwaitingRestart).Int("recovered", res.Recovered).Int("permanent", res.Permanent).Msg("failed_records_drain_pass_complete")
	}
	return res, firstErr
}

func (d *Drainer) pending(ctx context.Context) ([]pendingObject, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT tenant_id, user_id, provider, raw_key,
		       count(*) FILTER (WHERE coalesce(reason_code, 'unknown') NOT IN `+permanentReasons+`),
		       count(*) FILTER (WHERE coalesce(reason_code, 'unknown') IN `+permanentReasons+`),
		       coalesce(max(created_at) FILTER (WHERE coalesce(reason_code, 'unknown') NOT IN `+permanentReasons+`), 'epoch'::timestamptz),
		       max(created_at)
		FROM ohs_data.failed_records
		WHERE status = 'pending'
		GROUP BY tenant_id, user_id, provider, raw_key
		ORDER BY min(id)`)
	if err != nil {
		return nil, fmt.Errorf("list pending failed records: %w", err)
	}
	defer rows.Close()
	var out []pendingObject
	for rows.Next() {
		var o pendingObject
		if err := rows.Scan(&o.tenant, &o.user, &o.provider, &o.key, &o.recoverable, &o.permanent, &o.newest, &o.newestAll); err != nil {
			return nil, fmt.Errorf("scan pending object: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (d *Drainer) drainOne(ctx context.Context, o pendingObject, res *DrainResult) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Only rows that existed when the group was read are touched: a replay that
	// finishes at once writes fresh rows that must not be closed unseen.
	perm, err := closeRows(ctx, tx, o, "permanent", "IN "+permanentReasons, o.newestAll)
	if err != nil {
		return err
	}
	res.Permanent += perm

	if o.recoverable > 0 {
		var registered bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM ohs_sync.raw_objects WHERE key = $1 AND tenant_id = $2 AND user_id = $3)`,
			o.key, o.tenant, o.user).Scan(&registered); err != nil {
			return fmt.Errorf("check raw object: %w", err)
		}
		switch {
		case !registered:
			// The raw file is gone for good: no replay can ever land these.
			n, err := closeRows(ctx, tx, o, "permanent", "NOT IN "+permanentReasons, o.newest)
			if err != nil {
				return err
			}
			res.Permanent += n
		case !o.newest.Before(d.Since):
			res.AwaitingRestart++
		default:
			// A user being deleted gets no replay: the share lock, held until
			// commit, keeps a deletion from starting before the job is in.
			active, err := lockActiveUser(ctx, tx, o.tenant, o.user)
			if err != nil {
				return err
			}
			if !active {
				break
			}
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "etl_jobs:"+o.key); err != nil {
				return fmt.Errorf("lock key: %w", err)
			}
			var live bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM ohs_sync.etl_jobs WHERE key = $1 AND status IN ('pending', 'running'))`,
				o.key).Scan(&live); err != nil {
				return fmt.Errorf("check live job: %w", err)
			}
			if !live {
				if err := queue.EnqueueETL(ctx, tx, queue.ETLJob{
					TenantID: o.tenant, UserID: o.user, Provider: o.provider, Key: o.key, Tier: tierFor(o.provider),
				}); err != nil {
					return err
				}
				if d.Metrics != nil {
					d.Metrics.DrainerReplays.Add(1)
				}
			}
			n, err := closeRows(ctx, tx, o, "recovered", "NOT IN "+permanentReasons, o.newest)
			if err != nil {
				return err
			}
			res.Recovered += n
			res.Replayed++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// closeRows sets the status of the object's pending rows whose reason matches
// cond ("IN (...)" or "NOT IN (...)") and that are not newer than upTo (the
// newest row of that kind seen when the group was read). Replayed rows count one more attempt.
func closeRows(ctx context.Context, tx pgx.Tx, o pendingObject, status, cond string, upTo time.Time) (int, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE ohs_data.failed_records
		   SET status = $1, recovered_at = now(), attempts = attempts + 1
		 WHERE tenant_id = $2 AND user_id = $3 AND raw_key = $4
		   AND status = 'pending' AND created_at <= $5
		   AND coalesce(reason_code, 'unknown') `+cond,
		status, o.tenant, o.user, o.key, upTo)
	if err != nil {
		return 0, fmt.Errorf("mark %s: %w", status, err)
	}
	return int(tag.RowsAffected()), nil
}
