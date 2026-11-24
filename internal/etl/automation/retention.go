// SPDX-License-Identifier: AGPL-3.0-only

package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/soma-intelligence/open-health-sync/internal/logging"
	"github.com/soma-intelligence/open-health-sync/internal/logutil"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// retentionTables are the ohs_data tables pruned by age, with the column that
// holds the record's own time. Reference tables (quantity_codes,
// device_aliases) are not listed: they are not per user.
var retentionTables = []struct {
	table, col string
	date       bool // a date column, compared with the cutoff day
}{
	{table: "quantity_samples", col: "start_date"},
	{table: "category_samples", col: "start_date"},
	{table: "sleep_phases", col: "start_date"},
	{table: "sleep_sessions", col: "bedtime_start"},
	{table: "workouts", col: "start_date"},
	{table: "route_points", col: `"timestamp"`},
	{table: "nutrition_logs", col: "start_date"},
	{table: "ecg_samples", col: "ts"},
	{table: "ecg_recordings", col: "start_date"},
	{table: "heartbeat_samples", col: `"timestamp"`},
	{table: "daily_summaries", col: "summary_date", date: true},
	{table: "observations", col: "ts"},
	{table: "workout_tracks", col: "started_at"},
	{table: "failed_records", col: "created_at"},
	// The HealthKit aliases of a row (migration 0043) name it by its start:
	// they go with the rows they hold.
	{table: "record_aliases", col: "start_time"},
	// The sealed hourly buckets (migration 0050) go by their hour. The
	// cutoff is a whole hour, so a bucket goes exactly when every sample it
	// counts does.
	{table: "quantity_hourly_sealed", col: "bucket"},
	// A series blob (migration 0053) goes whole once its last point is
	// before the cutoff: a series is not cut in two, so an ECG, a route or
	// a heartbeat series that straddles the cutoff stays until it is all
	// past it.
	{table: "series_blobs", col: "end_at"},
}

// retentionKept are the ohs_data tables with a user_id that retention leaves
// alone on purpose: tombstones keep a deleted record from coming back, and
// tags are the user's own notes. A new per-user table must be added to
// retentionTables or here (TestRetentionCoversEveryUserTable).
var retentionKept = []string{"deleted_records", "time_tags"}

// Retention deletes data older than Days. It exists for hosted instances: a
// self-hosted install leaves OHS_RETENTION_DAYS at 0 and keeps everything, and
// Start then does not schedule it. Run refuses to act with Days <= 0 as well.
//
// One run works on sets, not user by user:
//
//  1. Raw objects. The raw_objects rows whose key names a day before the
//     cutoff day are read page by page. Each provider day they name is
//     deleted from the store with one DeletePrefix
//     (raw/<tenant>/<user>/<provider>/<day>/, DeletePrefix accepts nothing
//     broader than a user), then the raw_objects and etl_jobs rows of the
//     page are deleted. The store is never listed.
//  2. Records. For each ohs_data table, the chunks that lie wholly before the
//     cutoff are dropped (hypertables only), then the rows before the cutoff
//     that are left are deleted for every user at once, in batches that each
//     commit on their own (pruneTable).
//
// Files go first: if the run dies half way, a row without its file is
// cleaned on the next run, whereas a file without its row is only ever
// reported by the file scanner. A file that never had a raw_objects row is
// not seen by retention at all.
//
// Dropping a chunk removes the rows of every tenant in it. That is safe
// because Days is one setting for the whole instance: no tenant keeps data
// longer than another. If retention ever becomes per tenant, the chunk drop
// has to go and the row deletes have to carry the tenant.
type Retention struct {
	Pool    *pgxpool.Pool
	Store   rawstore.Store
	Days    int
	Metrics *Metrics
	Now     func() time.Time
	// KeepChunks turns the chunk drop off: every row is then deleted by the
	// per-table deletes. Slower, same result.
	KeepChunks bool
}

// RetentionResult is the outcome of one run.
type RetentionResult struct {
	Rows     int64 // ohs_data rows removed, those of dropped chunks included
	Chunks   int   // hypertable chunks dropped whole
	Prefixes int   // raw day prefixes deleted
	Users    int   // users who lost raw objects (records are pruned for every user at once and not counted per user)
}

// retentionRawBatch bounds one page of raw_objects rows.
const retentionRawBatch = 2000

// Run prunes raw objects, then records. A failure on one raw prefix or one
// table is logged and the others are still tried; the errors are joined and
// returned at the end.
func (r *Retention) Run(ctx context.Context) (RetentionResult, error) {
	var res RetentionResult
	if r.Days <= 0 {
		return res, nil
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	// On a whole hour: the hourly buckets and their samples are cut at the
	// same instant.
	cutoff := now.UTC().Add(-time.Duration(r.Days) * 24 * time.Hour).Truncate(time.Hour)

	var errs []error
	if err := r.pruneRaw(ctx, cutoff, &res); err != nil {
		errs = append(errs, err)
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}

	// Which tables are hypertables decides how their rows are deleted in
	// batches, so without the list no record is pruned in this run.
	hyper, err := hypertables(ctx, r.Pool)
	if err != nil {
		logutil.ErrCtx(ctx, *logging.Ctx(ctx), err, zerolog.ErrorLevel).Msg("retention_hypertables_not_listed")
		return res, errors.Join(append(errs, fmt.Errorf("list hypertables: %w", err))...)
	}
	cutoffDate := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.UTC)
	for _, t := range retentionTables {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		bound := cutoff
		if t.date {
			bound = cutoffDate // a date column: whole days before the cutoff day
		}
		if hyper[t.table] && !r.KeepChunks {
			chunks, rows, err := dropChunks(ctx, r.Pool, t.table, t.col, t.date, bound)
			if err != nil {
				// The rows are still deleted below, only more slowly.
				logutil.ErrCtx(ctx, *logging.Ctx(ctx), err, zerolog.WarnLevel).Str("table", t.table).Msg("retention_chunks_not_dropped")
			}
			res.Chunks += chunks
			res.Rows += rows
			logging.Ctx(ctx).Debug().Str("table", t.table).Int("chunks", chunks).Int64("rows", rows).Msg("retention_chunks_dropped")
		}
		// The batches that committed before a failure are counted too.
		n, err := pruneTable(ctx, r.Pool, t.table, t.col, t.date, bound, hyper[t.table])
		res.Rows += n
		if err != nil {
			logutil.ErrCtx(ctx, *logging.Ctx(ctx), err, zerolog.ErrorLevel).Str("table", t.table).Int64("rows_deleted", n).Msg("retention_table_failed")
			errs = append(errs, fmt.Errorf("prune %s: %w", t.table, err))
			continue
		}
		logging.Ctx(ctx).Debug().Str("table", t.table).Int64("rows", n).Msg("retention_rows_deleted")
	}
	if m := r.Metrics; m != nil {
		m.RetentionRows.Add(res.Rows)
		m.RetentionObjects.Add(int64(res.Prefixes))
		m.RetentionChunks.Add(int64(res.Chunks))
	}
	logging.Ctx(ctx).Info().Int("days", r.Days).Int64("rows_deleted", res.Rows).Int("chunks_dropped", res.Chunks).Int("raw_prefixes_deleted", res.Prefixes).Int("raw_users", res.Users).Msg("retention_complete")
	return res, errors.Join(errs...)
}

// rawDayPrefix returns the provider day prefix of a raw key
// (raw/<tenant>/<user>/<provider>/<day>/<file>), or "" for a key of another
// shape.
func rawDayPrefix(key string) string {
	parts := strings.Split(key, "/")
	if len(parts) != 6 || parts[0] != "raw" {
		return ""
	}
	return strings.Join(parts[:5], "/") + "/"
}

// pruneRaw deletes the raw objects whose key day is before the cutoff day:
// the files by provider day prefix, then the raw_objects and etl_jobs rows.
// It walks raw_objects by id. The rows of a prefix the store refused to
// delete are left for the next run.
func (r *Retention) pruneRaw(ctx context.Context, cutoff time.Time, res *RetentionResult) error {
	cutoffDay := cutoff.Format("2006-01-02")
	deleted := map[string]bool{} // prefixes deleted in this run
	failed := map[string]bool{}  // prefixes the store refused, not retried in this run
	users := map[[2]uuid.UUID]bool{}
	var errs []error
	var after int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := r.Pool.Query(ctx, `
			SELECT id, tenant_id, user_id, key FROM ohs_sync.raw_objects
			 WHERE id > $1 AND starts_with(key, 'raw/') AND split_part(key, '/', 5) < $2
			 ORDER BY id LIMIT $3`, after, cutoffDay, retentionRawBatch)
		if err != nil {
			return fmt.Errorf("list old raw objects: %w", err)
		}
		// The page is read in full before the store is touched, so no
		// connection is held across the store calls.
		type oldObject struct {
			id           int64
			tenant, user uuid.UUID
			key          string
		}
		page, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (oldObject, error) {
			var o oldObject
			err := row.Scan(&o.id, &o.tenant, &o.user, &o.key)
			return o, err
		})
		if err != nil {
			return fmt.Errorf("list old raw objects: %w", err)
		}
		var ids []int64
		var keys []string
		for _, o := range page {
			after = o.id
			prefix := rawDayPrefix(o.key)
			if prefix == "" || !strings.HasPrefix(o.key, fmt.Sprintf("raw/%s/%s/", o.tenant, o.user)) {
				continue // not a key this job may delete by prefix
			}
			if !deleted[prefix] && !failed[prefix] {
				if err := r.Store.DeletePrefix(ctx, prefix); err != nil {
					failed[prefix] = true
					logutil.ErrCtx(ctx, *logging.Ctx(ctx), err, zerolog.ErrorLevel).Str("prefix", prefix).Msg("retention_raw_prefix_not_deleted")
					errs = append(errs, fmt.Errorf("delete %s: %w", prefix, err))
				} else {
					deleted[prefix] = true
					res.Prefixes++
				}
			}
			if deleted[prefix] {
				ids, keys = append(ids, o.id), append(keys, o.key)
				users[[2]uuid.UUID{o.tenant, o.user}] = true
			}
		}
		if len(ids) > 0 {
			if _, err := r.Pool.Exec(ctx, `DELETE FROM ohs_sync.etl_jobs WHERE key = ANY($1)`, keys); err != nil {
				return fmt.Errorf("delete etl_jobs: %w", err)
			}
			if _, err := r.Pool.Exec(ctx, `DELETE FROM ohs_sync.raw_objects WHERE id = ANY($1)`, ids); err != nil {
				return fmt.Errorf("delete raw_objects: %w", err)
			}
		}
		if len(page) < retentionRawBatch {
			break
		}
	}
	res.Users = len(users)
	// Jobs of old keys whose raw_objects row is already gone (a run that died
	// between the two deletes).
	if _, err := r.Pool.Exec(ctx, `
		DELETE FROM ohs_sync.etl_jobs j
		 WHERE starts_with(j.key, 'raw/') AND split_part(j.key, '/', 5) < $1
		   AND NOT EXISTS (SELECT 1 FROM ohs_sync.raw_objects o WHERE o.key = j.key)`, cutoffDay); err != nil {
		errs = append(errs, fmt.Errorf("delete etl_jobs without a raw object: %w", err))
	}
	return errors.Join(errs...)
}

// hypertables returns the ohs_data tables that are TimescaleDB hypertables.
func hypertables(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	out := map[string]bool{}
	var present bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('timescaledb_information.hypertables') IS NOT NULL`).Scan(&present); err != nil {
		return nil, err
	}
	if !present {
		return out, nil
	}
	rows, err := pool.Query(ctx, `SELECT hypertable_name::text FROM timescaledb_information.hypertables WHERE hypertable_schema = 'ohs_data'`)
	if err != nil {
		return nil, err
	}
	var name string
	_, err = pgx.ForEachRow(rows, []any{&name}, func() error { out[name] = true; return nil })
	return out, err
}

// dropChunks drops the chunks of a hypertable that lie wholly before bound
// and returns how many chunks and how many rows went with them. A chunk that
// straddles the bound is kept.
//
// The rows are counted before the drop, in the same transaction, so the run
// reports every row it removed whichever way it removed it. The count reads
// only the chunks about to go.
func dropChunks(ctx context.Context, pool *pgxpool.Pool, table, col string, date bool, bound time.Time) (chunks int, rows int64, err error) {
	// A date column's chunks end at midnight UTC. The bound and the chunk end
	// are converted in UTC, never in the session's zone: one day off and the
	// last chunk is counted but not dropped.
	boundAt, endAs := "$2::timestamptz", "$1::timestamptz"
	if date {
		boundAt, endAs = "($2::date::timestamp AT TIME ZONE 'UTC')", "($1::timestamptz AT TIME ZONE 'UTC')::date"
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// The end of the newest chunk that lies wholly before bound: everything
	// before it is in a chunk the drop removes.
	var end *time.Time
	if err := tx.QueryRow(ctx, `SELECT max(range_end) FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'ohs_data' AND hypertable_name = $1 AND range_end <= `+boundAt,
		table, bound).Scan(&end); err != nil {
		return 0, 0, err
	}
	if end == nil {
		return 0, 0, nil
	}
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM ohs_data.%s WHERE %s < %s`, table, col, endAs),
		*end).Scan(&rows); err != nil {
		return 0, 0, err
	}
	if err := tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*) FROM drop_chunks('ohs_data.%s'::regclass, older_than => %s)`, table, endAs),
		*end).Scan(&chunks); err != nil {
		return 0, 0, err
	}
	return chunks, rows, tx.Commit(ctx)
}

// retentionDeleteBatch bounds the rows one DELETE removes from a plain
// table. A variable so a test can lower it.
var retentionDeleteBatch = 50000

// pruneTable deletes the rows of table older than bound, for every user, and
// returns how many it deleted. It works in batches, each committed on its
// own, so the first run on a large instance is not one transaction that
// holds its locks and its snapshot for as long as it takes: a hypertable is
// deleted one chunk at a time, a plain table retentionDeleteBatch rows at a
// time. When a batch fails, the rows of the batches before it stay deleted
// and are returned with the error.
func pruneTable(ctx context.Context, pool *pgxpool.Pool, table, col string, date bool, bound time.Time, hyper bool) (int64, error) {
	if hyper {
		return pruneChunks(ctx, pool, table, col, date, bound)
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		// The inner select picks the batch by row address, which every
		// table has; the bound is repeated on the outer delete for a row
		// that changed in between.
		tag, err := pool.Exec(ctx, fmt.Sprintf(`DELETE FROM ohs_data.%[1]s
			WHERE ctid = ANY (ARRAY(SELECT ctid FROM ohs_data.%[1]s WHERE %[2]s < $1 LIMIT $2)) AND %[2]s < $1`,
			table, col), bound, retentionDeleteBatch)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < int64(retentionDeleteBatch) {
			return total, nil
		}
	}
}

// pruneChunks is pruneTable for a hypertable: one transaction per chunk that
// starts before bound, oldest first. After dropChunks only the chunk that
// straddles the bound still holds such rows; with KeepChunks, or after a
// drop that failed, every old chunk does. Old rows sit in compressed chunks,
// so each delete may decompress as many batches as it needs rather than stop
// at the per-transaction default: that is bounded by the one chunk it reads.
func pruneChunks(ctx context.Context, pool *pgxpool.Pool, table, col string, date bool, bound time.Time) (int64, error) {
	// As in dropChunks: a date column's chunks are cut at midnight UTC, and
	// the bounds are converted in UTC, never in the session's zone.
	as := "$%d::timestamptz"
	if date {
		as = "($%d::timestamptz AT TIME ZONE 'UTC')::date"
	}
	rows, err := pool.Query(ctx, `SELECT range_start, range_end FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'ohs_data' AND hypertable_name = $1 AND range_start < $2
		ORDER BY range_start`, table, bound)
	if err != nil {
		return 0, fmt.Errorf("list chunks: %w", err)
	}
	type span struct{ lo, hi time.Time }
	chunks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (span, error) {
		var c span
		err := row.Scan(&c.lo, &c.hi)
		return c, err
	})
	if err != nil {
		return 0, fmt.Errorf("list chunks: %w", err)
	}
	del := fmt.Sprintf(`DELETE FROM ohs_data.%s WHERE %s >= %s AND %s < %s`,
		table, col, fmt.Sprintf(as, 1), col, fmt.Sprintf(as, 2))
	var total int64
	for _, c := range chunks {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		hi := c.hi
		if bound.Before(hi) {
			hi = bound
		}
		n, err := deleteSpan(ctx, pool, del, c.lo, hi)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// deleteSpan runs one chunk's delete in its own transaction, with the
// decompression cap lifted for it.
func deleteSpan(ctx context.Context, pool *pgxpool.Pool, del string, lo, hi time.Time) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL timescaledb.max_tuples_decompressed_per_dml_transaction = 0`); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, del, lo, hi)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
