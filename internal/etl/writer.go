// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// The writer handles database writes with deduplication, batching, and COPY
// for high-volume data. Every row carries the tenant and user of the job, and
// every conflict target is the table's dedup unique: (tenant_id, user_id,
// content_hash, <time column>) on hypertables, (tenant_id, user_id,
// content_hash) on plain tables. quantity_samples, which is compressed, has no
// such unique and dedups by time window instead (see insertMissing). Record families live in writer_samples.go
// (flat tables) and writer_parents.go (parents with child rows).

// Unqualified table names, the keys of Counts.
const (
	tQuantity    = "quantity_samples"
	tCategory    = "category_samples"
	tSleep       = "sleep_sessions"
	tPhases      = "sleep_phases"
	tWorkouts    = "workouts"
	tRoutes      = "route_points"
	tNutrition   = "nutrition_logs"
	tECG         = "ecg_recordings"
	tECGSamples  = "ecg_samples"
	tHeartbeat   = "heartbeat_samples"
	tDaily       = "daily_summaries"
	tObservation = "observations"
)

// qualified returns the schema-qualified name of a canonical table.
func qualified(table string) string { return "ohs_data." + table }

// TxLike is the part of pgx.Tx the writer uses.
type TxLike interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	CopyFrom(ctx context.Context, tableName pgx.Identifier, columns []string, rowSrc pgx.CopyFromSource) (int64, error)
}

// TableCount is the written and skipped (duplicate or unattachable) count of
// one table.
type TableCount = struct{ Written, Skipped int }

// Counts holds per-table write counts, keyed by unqualified table name
// ("quantity_samples").
type Counts map[string]TableCount

// add records write results for a table, accumulating across batches.
func (c Counts) add(table string, written, skipped int) {
	tc := c[table]
	tc.Written += written
	tc.Skipped += skipped
	c[table] = tc
}

// TotalWritten returns the sum of written counts across all tables.
func (c Counts) TotalWritten() int {
	n := 0
	for _, tc := range c {
		n += tc.Written
	}
	return n
}

// TotalSkipped returns the sum of skipped counts across all tables.
func (c Counts) TotalSkipped() int {
	n := 0
	for _, tc := range c {
		n += tc.Skipped
	}
	return n
}

// scope is who owns every row of one write: the job's tenant, user and
// provider. Rows take tenant_id and user_id from here, never from the record,
// so a connector cannot write into another user's data.
type scope struct {
	tenant   uuid.UUID
	user     uuid.UUID
	provider string
}

// providerOr returns p, or the scope's provider when p is empty.
func (s scope) providerOr(p string) string {
	if p != "" {
		return p
	}
	return s.provider
}

// WriterConfig controls writer behavior.
type WriterConfig struct {
	BatchSize     int // Multi-row INSERT batch size (default: 100)
	CopyThreshold int // Row count above which COPY is used (default: 500)
}

// Writer handles database writes for all canonical record types.
type Writer struct {
	cfg WriterConfig
}

// NewWriter creates a Writer with the given config, applying defaults.
func NewWriter(cfg WriterConfig) *Writer {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.CopyThreshold <= 0 {
		cfg.CopyThreshold = 500
	}
	return &Writer{cfg: cfg}
}

// Accumulate folds one mapped record into a running per-type accumulator. The
// ETL maps one raw line at a time and folds the result in here, so the raw JSON
// map is released immediately instead of the whole file staying live through
// the write phase. dst must not be nil; src may be.
func Accumulate(dst, src *model.MapResult) {
	if src == nil {
		return
	}
	dst.QuantitySamples = append(dst.QuantitySamples, src.QuantitySamples...)
	dst.CategorySamples = append(dst.CategorySamples, src.CategorySamples...)
	dst.SleepSessions = append(dst.SleepSessions, src.SleepSessions...)
	dst.SleepPhases = append(dst.SleepPhases, src.SleepPhases...)
	dst.Workouts = append(dst.Workouts, src.Workouts...)
	dst.RoutePoints = append(dst.RoutePoints, src.RoutePoints...)
	dst.NutritionLogs = append(dst.NutritionLogs, src.NutritionLogs...)
	dst.ECGRecordings = append(dst.ECGRecordings, src.ECGRecordings...)
	dst.ECGSamples = append(dst.ECGSamples, src.ECGSamples...)
	dst.HeartbeatSamples = append(dst.HeartbeatSamples, src.HeartbeatSamples...)
	dst.DailySummaries = append(dst.DailySummaries, src.DailySummaries...)
	dst.Observations = append(dst.Observations, src.Observations...)
	dst.Deletions = append(dst.Deletions, src.Deletions...)
	dst.HighFrequency = dst.HighFrequency || src.HighFrequency
	// Every MapResult in one accumulation comes from the same raw object, so the
	// first non-empty key describes the whole batch.
	if dst.SourceKey == "" {
		dst.SourceKey = src.SourceKey
	}
}

// WriteAccumulated writes one flattened batch of canonical records for sc in
// tx. The caller owns the transaction boundary. The batch's deletions are
// applied first, then every record of a tombstoned provider record is dropped
// (see writer_deletions.go): acc is filtered in place.
func (w *Writer) WriteAccumulated(ctx context.Context, tx TxLike, sc scope, acc *model.MapResult) (Counts, error) {
	c := Counts{}
	if acc == nil {
		return c, nil
	}
	if len(acc.Deletions) > 0 {
		if err := applyDeletions(ctx, tx, sc, acc.Deletions, c); err != nil {
			return nil, fmt.Errorf("apply deletions: %w", err)
		}
	}
	if err := dropTombstoned(ctx, tx, sc, acc, c); err != nil {
		return nil, err
	}
	type step struct {
		name string
		run  func() error
	}
	flat := func(table string, n int, fn func() (int, int, error)) step {
		return step{table, func() error {
			if n == 0 {
				return nil
			}
			written, skipped, err := fn()
			if err != nil {
				return err
			}
			c.add(table, written, skipped)
			return nil
		}}
	}
	steps := []step{
		flat(tQuantity, len(acc.QuantitySamples), func() (int, int, error) {
			return w.writeQuantitySamples(ctx, tx, sc, acc.QuantitySamples)
		}),
		flat(tCategory, len(acc.CategorySamples), func() (int, int, error) {
			return w.writeCategorySamples(ctx, tx, sc, acc.CategorySamples)
		}),
		flat(tNutrition, len(acc.NutritionLogs), func() (int, int, error) {
			return w.writeNutritionLogs(ctx, tx, sc, acc.NutritionLogs)
		}),
		{"sleep+phases", func() error {
			if len(acc.SleepSessions) == 0 && len(acc.SleepPhases) == 0 {
				return nil
			}
			return w.writeSleepWithPhases(ctx, tx, sc, acc.SleepSessions, acc.SleepPhases, c)
		}},
		{"workouts+routes", func() error {
			if len(acc.Workouts) == 0 && len(acc.RoutePoints) == 0 {
				return nil
			}
			return w.writeWorkoutsWithRoutes(ctx, tx, sc, acc.Workouts, acc.RoutePoints, c)
		}},
		{"ecg+samples", func() error {
			if len(acc.ECGRecordings) == 0 && len(acc.ECGSamples) == 0 {
				return nil
			}
			return w.writeECGWithSamples(ctx, tx, sc, acc.ECGRecordings, acc.ECGSamples, acc.SourceKey, c)
		}},
		flat(tDaily, len(acc.DailySummaries), func() (int, int, error) {
			return w.writeDailySummaries(ctx, tx, sc, acc.DailySummaries)
		}),
		flat(tHeartbeat, len(acc.HeartbeatSamples), func() (int, int, error) {
			return w.writeHeartbeatSamples(ctx, tx, sc, acc.HeartbeatSamples)
		}),
		flat(tObservation, len(acc.Observations), func() (int, int, error) {
			return w.writeObservations(ctx, tx, sc, acc.Observations)
		}),
	}
	for _, s := range steps {
		if err := s.run(); err != nil {
			return nil, fmt.Errorf("write %s: %w", s.name, err)
		}
	}
	return c, nil
}

// --- Shared helpers ---

// insertBatches runs a multi-row INSERT per batch of rows. head is the
// statement up to and including VALUES; tail is the conflict clause.
func (w *Writer) insertBatches(ctx context.Context, tx TxLike, table, head, tail string, colCount int, rows [][]any) (written, skipped int, err error) {
	for i := 0; i < len(rows); i += w.cfg.BatchSize {
		end := min(i+w.cfg.BatchSize, len(rows))
		batch := rows[i:end]
		args := make([]any, 0, len(batch)*colCount)
		phs := make([]string, 0, len(batch))
		for j, r := range batch {
			phs = append(phs, placeholders(j*colCount+1, colCount))
			args = append(args, r...)
		}
		tag, err := tx.Exec(ctx, head+strings.Join(phs, ", ")+tail, args...)
		if err != nil {
			return written, skipped, fmt.Errorf("insert %s: %w", table, err)
		}
		n := int(tag.RowsAffected())
		written += n
		skipped += len(batch) - n
	}
	return written, skipped, nil
}

// insertHead builds "INSERT INTO ohs_data.<table> (cols) VALUES ".
func insertHead(table string, columns []string) string {
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES ", qualified(table), quoteCols(columns))
}

// quoteCols renders a column list with every name quoted ("timestamp" is a
// keyword).
func quoteCols(columns []string) string {
	q := make([]string, len(columns))
	for i, c := range columns {
		q[i] = pgx.Identifier{c}.Sanitize()
	}
	return strings.Join(q, ", ")
}

// doNothing is the first-write-wins conflict clause on a dedup unique.
func doNothing(conflictCols string) string {
	return " ON CONFLICT (" + conflictCols + ") DO NOTHING"
}

// insertParentWithFallback inserts a parent record with RETURNING id. On
// ON CONFLICT DO NOTHING (no row back) it falls back to selectSQL with
// selectArgs. Returns the resolved ID and whether the row was newly inserted.
func insertParentWithFallback(ctx context.Context, tx TxLike, insertSQL string, insertArgs []any, selectSQL string, selectArgs ...any) (int64, bool, error) {
	var id int64
	err := tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	if err := tx.QueryRow(ctx, selectSQL, selectArgs...).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("fallback select by content_hash: %w", err)
	}
	return id, false, nil
}

// stage copies rows into a temporary table shaped like the write columns of
// table and returns its quoted name and the number of rows copied.
//
// The staging table is a projection of the write columns (CREATE TABLE AS ...
// WITH NO DATA): no indexes, defaults or constraints, so the COPY is a pure
// heap append and the index work happens once, on the target. It is created IF
// NOT EXISTS and truncated, because ON COMMIT DROP only fires at commit and a
// second COPY into the same target inside one transaction (two workouts with
// GPS routes in one upload) would otherwise fail. Duplicate rows inside one
// batch must be filtered by the caller (copyDedupe): staging has no unique.
func stage(ctx context.Context, tx TxLike, table string, columns []string, rows [][]any) (temp string, copied int64, err error) {
	temp = pgx.Identifier{"_tmp_" + table}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`CREATE TEMPORARY TABLE IF NOT EXISTS %s ON COMMIT DROP AS SELECT %s FROM %s WITH NO DATA`,
		temp, quoteCols(columns), pgx.Identifier{"ohs_data", table}.Sanitize())); err != nil {
		return "", 0, fmt.Errorf("create staging for %s: %w", table, err)
	}
	if _, err := tx.Exec(ctx, "TRUNCATE TABLE "+temp); err != nil {
		return "", 0, fmt.Errorf("truncate staging for %s: %w", table, err)
	}
	copied, err = tx.CopyFrom(ctx, pgx.Identifier{"_tmp_" + table}, columns, pgx.CopyFromRows(rows))
	if err != nil {
		return "", 0, fmt.Errorf("copy to staging for %s: %w", table, err)
	}
	return temp, copied, nil
}

// writeHighFrequencyCopy stages rows with COPY into a temporary table and moves
// them into the target with INSERT ... SELECT ... ON CONFLICT DO NOTHING.
func (w *Writer) writeHighFrequencyCopy(ctx context.Context, tx TxLike, table string, columns []string, rows [][]any, conflictCols string) (written, skipped int, err error) {
	temp, copied, err := stage(ctx, tx, table, columns, rows)
	if err != nil {
		return 0, 0, err
	}
	cols := quoteCols(columns)
	insert := fmt.Sprintf(`INSERT INTO %s (%s) SELECT %s FROM %s`, qualified(table), cols, cols, temp)
	if conflictCols != "" {
		insert += doNothing(conflictCols)
	}
	tag, err := tx.Exec(ctx, insert)
	if err != nil {
		return 0, 0, fmt.Errorf("insert from staging into %s: %w", table, err)
	}
	written = int(tag.RowsAffected())
	return written, int(copied) - written, nil
}

// --- Window dedup (tables without a dedup unique) ---
//
// A compressed TimescaleDB chunk checks a unique index by decompressing whole
// batches, which makes a late upload into an old period tens of times slower
// and, past timescaledb.max_tuples_decompressed_per_dml_transaction, fail. A
// table that is compressed therefore has no dedup unique: the writer reads the
// stored (time, content_hash) pairs of the batch's time window for this user
// (one segment of each compressed chunk) and inserts only the missing rows,
// under a per-user lock so two jobs of one user cannot both insert a row.

// lockUserTable serializes the writers of one user's rows in table until the
// transaction ends.
func lockUserTable(ctx context.Context, tx TxLike, table string, sc scope) error {
	key := qualified(table) + ":" + sc.tenant.String() + ":" + sc.user.String()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return fmt.Errorf("lock %s for user: %w", table, err)
	}
	return nil
}

// window is the time range [lo, hi] a batch covers.
type window struct{ lo, hi time.Time }

// extend widens the window to include at.
func (wd *window) extend(at time.Time) {
	if wd.lo.IsZero() || at.Before(wd.lo) {
		wd.lo = at
	}
	if wd.hi.IsZero() || at.After(wd.hi) {
		wd.hi = at
	}
}

// insertMissing inserts the staged rows of temp whose (timeCol, content_hash)
// is not stored yet for the scope's user, and returns how many it inserted.
func insertMissing(ctx context.Context, tx TxLike, sc scope, table, timeCol, temp string, columns []string, wd window) (int, error) {
	cols := quoteCols(columns)
	at := pgx.Identifier{timeCol}.Sanitize()
	tag, err := tx.Exec(ctx, fmt.Sprintf(`WITH stored AS MATERIALIZED (
			SELECT %[1]s AS at, content_hash FROM %[2]s
			WHERE tenant_id = $1 AND user_id = $2 AND %[1]s BETWEEN $3 AND $4)
		INSERT INTO %[2]s (%[3]s)
		SELECT %[3]s FROM %[4]s s
		WHERE NOT EXISTS (SELECT 1 FROM stored WHERE stored.at = s.%[1]s AND stored.content_hash = s.content_hash)`,
		at, qualified(table), cols, temp), sc.tenant, sc.user, wd.lo, wd.hi)
	if err != nil {
		return 0, fmt.Errorf("insert missing into %s: %w", table, err)
	}
	return int(tag.RowsAffected()), nil
}

// updateStored applies latest wins (see latestWins) from the staged rows of
// temp to the stored rows with the same (timeCol, content_hash), and returns
// how many stored rows changed.
func updateStored(ctx context.Context, tx TxLike, sc scope, table, timeCol, temp string, figures []string, wd window) (int, error) {
	at := pgx.Identifier{timeCol}.Sanitize()
	set := make([]string, 0, len(figures)+1)
	stored := make([]string, 0, len(figures))
	merged := make([]string, 0, len(figures))
	for _, f := range figures {
		m := fmt.Sprintf("COALESCE(s.%s, t.%s)", f, f)
		set = append(set, f+" = "+m)
		stored = append(stored, "t."+f)
		merged = append(merged, m)
	}
	set = append(set, "metadata = s.metadata")
	tag, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %[1]s t SET %[2]s FROM %[3]s s
		WHERE t.tenant_id = $1 AND t.user_id = $2 AND t.%[4]s BETWEEN $3 AND $4
		  AND t.%[4]s = s.%[4]s AND t.content_hash = s.content_hash
		  AND (%[5]s) IS DISTINCT FROM (%[6]s)`,
		qualified(table), strings.Join(set, ", "), temp, at, strings.Join(stored, ", "), strings.Join(merged, ", ")),
		sc.tenant, sc.user, wd.lo, wd.hi)
	if err != nil {
		return 0, fmt.Errorf("update stored %s: %w", table, err)
	}
	return int(tag.RowsAffected()), nil
}

// copyDedupeKey is a staged row's identity within one job: the target's dedup
// unique minus tenant and user, which are constant for a job.
type copyDedupeKey struct {
	hash  string
	stamp int64
}

// copyDedupe tracks which (content_hash, time) pairs are already staged in one
// COPY batch. Two identical rows in one file must not both reach the target and
// must count as one written plus one skipped, as the INSERT path answers.
type copyDedupe map[copyDedupeKey]struct{}

// staged reports whether this pair was already staged, and records it if not.
func (d copyDedupe) staged(hash string, stamp time.Time) bool {
	k := copyDedupeKey{hash: hash, stamp: stamp.UnixNano()}
	if _, ok := d[k]; ok {
		return true
	}
	d[k] = struct{}{}
	return false
}

// --- Latest wins (Replaceable rows) ---

// replaceableSplit separates the rows the writer may only insert from the ones
// it must upsert, in their original order. The upsert group keeps one copy of
// each identity, the LAST: Postgres refuses an ON CONFLICT DO UPDATE that
// touches one row twice in a statement, and the last copy should win anyway.
// superseded counts the copies dropped, so written plus skipped still equals
// what the file carried.
func replaceableSplit[T any](rows []T, replaceable func(T) bool, key func(T) copyDedupeKey) (insertOnly, upsert []T, superseded int) {
	last := make(map[copyDedupeKey]int)
	for i, r := range rows {
		if replaceable(r) {
			last[key(r)] = i
		}
	}
	for i, r := range rows {
		switch {
		case !replaceable(r):
			insertOnly = append(insertOnly, r)
		case last[key(r)] == i:
			upsert = append(upsert, r)
		default:
			superseded++
		}
	}
	return insertOnly, upsert, superseded
}

// latestWins builds the conflict action for a Replaceable row of table.
//
// Every figure the new copy carries replaces the stored one. A figure it omits
// keeps the stored value, because a vendor that stops reporting a field has not
// measured it as NULL. A copy that changes nothing writes nothing, and metadata
// follows the figures it describes.
func latestWins(table, conflictCols string, figures []string) string {
	// The existing row is referenced by the unqualified table name.
	alias := table
	set := make([]string, 0, len(figures)+1)
	stored := make([]string, 0, len(figures))
	merged := make([]string, 0, len(figures))
	for _, f := range figures {
		m := fmt.Sprintf("COALESCE(EXCLUDED.%s, %s.%s)", f, alias, f)
		set = append(set, f+" = "+m)
		stored = append(stored, alias+"."+f)
		merged = append(merged, m)
	}
	set = append(set, "metadata = EXCLUDED.metadata")
	return fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET %s WHERE (%s) IS DISTINCT FROM (%s)",
		conflictCols, strings.Join(set, ", "), strings.Join(stored, ", "), strings.Join(merged, ", "))
}

// placeholders generates a SQL placeholder group like "($1,$2,$3)" starting at
// startIdx, written into one buffer rather than a Sprintf per column.
func placeholders(startIdx, count int) string {
	if count <= 0 {
		return "()"
	}
	buf := make([]byte, 0, count*7+2)
	buf = append(buf, '(')
	for i := range count {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, '$')
		buf = strconv.AppendInt(buf, int64(startIdx+i), 10)
	}
	buf = append(buf, ')')
	return string(buf)
}

// normalizeMetadata ensures metadata is never nil (JSONB null vs {}).
func normalizeMetadata(md map[string]any) map[string]any {
	if md == nil {
		return map[string]any{}
	}
	return md
}
