// SPDX-License-Identifier: AGPL-3.0-only

package automation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

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
}

// Retention deletes data older than Days. It exists for hosted instances: a
// self-hosted install leaves OHS_RETENTION_DAYS at 0 and keeps everything, and
// Start then does not schedule it. Run refuses to act with Days <= 0 as well.
//
// Per end user it deletes, in this order:
//
//  1. the raw files of every provider day before the cutoff day, one
//     DeletePrefix per raw/<tenant>/<user>/<provider>/<day>/ (DeletePrefix
//     accepts nothing broader than a user);
//  2. the raw_objects and etl_jobs rows of those keys;
//  3. the ohs_data rows whose own time is before the cutoff.
//
// Files go first: if the run dies half way, a row without its file is
// cleaned on the next run, whereas a file without its row would only ever be
// a warning from the orphan scanner. Every statement is scoped by tenant_id
// and user_id.
type Retention struct {
	Pool    *pgxpool.Pool
	Store   rawstore.Store
	Days    int
	Metrics *Metrics
	Now     func() time.Time
}

// RetentionResult is the outcome of one run.
type RetentionResult struct {
	Rows     int64 // ohs_data rows deleted
	Prefixes int   // raw day prefixes deleted
	Users    int   // users visited
}

// Run prunes every user. A failure on one user is logged and the others are
// still tried; the first error is returned at the end.
func (r *Retention) Run(ctx context.Context) (RetentionResult, error) {
	var res RetentionResult
	if r.Days <= 0 {
		return res, nil
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	cutoff := now.UTC().Add(-time.Duration(r.Days) * 24 * time.Hour)

	type owner struct{ tenant, user uuid.UUID }
	rows, err := r.Pool.Query(ctx, `SELECT tenant_id, id FROM ohs_core.end_users ORDER BY tenant_id, id`)
	if err != nil {
		return res, fmt.Errorf("list users: %w", err)
	}
	var owners []owner
	for rows.Next() {
		var o owner
		if err := rows.Scan(&o.tenant, &o.user); err != nil {
			rows.Close()
			return res, fmt.Errorf("scan user: %w", err)
		}
		owners = append(owners, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("list users: %w", err)
	}

	var firstErr error
	for _, o := range owners {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		n, p, err := r.pruneUser(ctx, o.tenant, o.user, cutoff)
		res.Rows += n
		res.Prefixes += p
		res.Users++
		if err != nil {
			slog.ErrorContext(ctx, "retention for user failed", "tenant_id", o.tenant, "user_id", o.user, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if m := r.Metrics; m != nil {
		m.RetentionRows.Add(res.Rows)
		m.RetentionObjects.Add(int64(res.Prefixes))
	}
	slog.InfoContext(ctx, "retention complete", "days", r.Days, "users", res.Users,
		"rows_deleted", res.Rows, "raw_prefixes_deleted", res.Prefixes)
	return res, firstErr
}

func (r *Retention) pruneUser(ctx context.Context, tenant, user uuid.UUID, cutoff time.Time) (rowsDeleted int64, prefixes int, err error) {
	userPrefix := fmt.Sprintf("raw/%s/%s/", tenant, user)
	cutoffDay := cutoff.Format("2006-01-02")
	// Walk page by page and keep only the day prefixes to delete (a few per
	// provider and day), never the keys.
	seen := map[string]bool{}
	var old []string
	err = r.Store.Walk(ctx, userPrefix, func(keys []string) error {
		for _, k := range keys {
			// raw/<tenant>/<user>/<provider>/<day>/<file>
			parts := strings.Split(k, "/")
			if len(parts) != 6 || parts[4] >= cutoffDay {
				continue
			}
			prefix := strings.Join(parts[:5], "/") + "/"
			if !seen[prefix] {
				seen[prefix] = true
				old = append(old, prefix)
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("walk %s: %w", userPrefix, err)
	}
	for _, prefix := range old {
		if err := r.Store.DeletePrefix(ctx, prefix); err != nil {
			return rowsDeleted, prefixes, fmt.Errorf("delete %s: %w", prefix, err)
		}
		prefixes++
	}

	// Rows for every key under a deleted prefix, including files that were
	// already gone, so a half-done earlier run is finished.
	if _, err := r.Pool.Exec(ctx, `
		DELETE FROM ohs_sync.etl_jobs
		 WHERE tenant_id = $1 AND user_id = $2
		   AND starts_with(key, $3) AND split_part(key, '/', 5) < $4`,
		tenant, user, userPrefix, cutoffDay); err != nil {
		return rowsDeleted, prefixes, fmt.Errorf("delete etl_jobs: %w", err)
	}
	if _, err := r.Pool.Exec(ctx, `
		DELETE FROM ohs_sync.raw_objects
		 WHERE tenant_id = $1 AND user_id = $2
		   AND starts_with(key, $3) AND split_part(key, '/', 5) < $4`,
		tenant, user, userPrefix, cutoffDay); err != nil {
		return rowsDeleted, prefixes, fmt.Errorf("delete raw_objects: %w", err)
	}

	var errs []error
	cutoffDate := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.UTC)
	for _, t := range retentionTables {
		bound := cutoff
		if t.date {
			bound = cutoffDate // a date column: whole days before the cutoff day
		}
		n, err := pruneTable(ctx, r.Pool, t.table, t.col, tenant, user, bound)
		if err != nil {
			errs = append(errs, fmt.Errorf("prune %s: %w", t.table, err))
			continue
		}
		rowsDeleted += n
	}
	return rowsDeleted, prefixes, errors.Join(errs...)
}

// pruneTable deletes one user's rows of table older than bound. Old rows sit in
// compressed chunks, so the delete may decompress as many batches as it needs
// rather than stop at the per-transaction default.
func pruneTable(ctx context.Context, pool *pgxpool.Pool, table, col string, tenant, user uuid.UUID, bound time.Time) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL timescaledb.max_tuples_decompressed_per_dml_transaction = 0`); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx,
		fmt.Sprintf(`DELETE FROM ohs_data.%s WHERE tenant_id = $1 AND user_id = $2 AND %s < $3`, table, col),
		tenant, user, bound)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}
