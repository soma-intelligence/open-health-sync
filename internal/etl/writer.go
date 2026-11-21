// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/soma-intelligence/open-health-sync/internal/memlimit"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// The writer handles database writes with deduplication, batching, and COPY
// for high-volume data. Every row carries the tenant and user of the job, and
// every conflict target is the table's dedup unique: (tenant_id, user_id,
// content_hash, <time column>) on hypertables, (tenant_id, user_id,
// content_hash) on plain tables. quantity_samples, which is compressed, has no
// such unique and dedups by time window instead (see insertMissing). Record
// families live in writer_samples.go (flat tables) and writer_parents.go (parents with child rows).

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
// so a connector cannot write into another user's data. uv is the user's keys
// when a vault is configured: every row is then written sealed (see
// vaultrows.go); nil writes clear rows. hours, set by WriteAccumulated when
// the job holds keys, collects the hours of the samples the write touches
// (hourly.go); nil collects nothing.
type scope struct {
	tenant   uuid.UUID
	user     uuid.UUID
	provider string
	// captured is when the raw object being written was captured (its
	// raw_objects.captured_at, the start of the pull's fetch, or else its
	// created_at). It orders the copies of a Replaceable row: see
	// latestWins. Zero means unknown, and an unknown copy is never held back.
	captured time.Time
	uv       *vault.UserVault
	hours    hourSet
	// zone is the user's time zone (end_users.timezone), nil when the user
	// has none: the zone of the local day of a record whose metadata names
	// no zone of its own (scope.location).
	zone *time.Location
}

// capturedAt is the captured_at value of every row of one write: the raw
// object's capture time, or NULL when it is unknown.
func (s scope) capturedAt() any { return nullTime(s.captured) }

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
	// ChunkRecords is how many bulk records (quantity and category samples,
	// heartbeats, observations) of one raw object the ETL holds at a time:
	// an object with more is written chunk by chunk inside its transaction
	// (stream.go). Default DefaultChunkRecords, or LowChunkRecords under the
	// low-memory profile (memlimit.Low).
	ChunkRecords int
}

// DefaultChunkRecords and LowChunkRecords are the default sizes of a write
// chunk. A chunk of quantity samples costs about 4 KiB of heap per record
// while it is written (the records, their keyed copies, the staged rows, the
// dedup lookups and the garbage of mapping them), so a job on a large object
// peaks near 40 MiB with the default and near 12 MiB with the low profile,
// where the eight jobs of a process (OHS_ETL_CONCURRENCY, two tiers) share a
// soft limit of a few hundred MiB. Smaller chunks cost no time: the size of a
// chunk does not change what the database does per row.
const (
	DefaultChunkRecords = 10_000
	LowChunkRecords     = 2_500
)

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
	if cfg.ChunkRecords <= 0 {
		cfg.ChunkRecords = DefaultChunkRecords
		if memlimit.Low() {
			cfg.ChunkRecords = LowChunkRecords
		}
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
	dst.AliasExternalIDs = dst.AliasExternalIDs || src.AliasExternalIDs
	// Every MapResult in one accumulation comes from the same raw object, so the
	// first non-empty key describes the whole batch.
	if dst.SourceKey == "" {
		dst.SourceKey = src.SourceKey
	}
}

// WriteAccumulated writes one flattened batch of canonical records for sc in
// tx. The caller owns the transaction boundary. The batch's deletions are
// applied first, then every record of a tombstoned provider record is dropped
// (see writer_deletions.go): acc is filtered in place. An aliased batch
// (acc.AliasExternalIDs) applies its deletions through the record aliases and
// writes the aliases of its records last (see writer_aliases.go). With keys,
// the hourly buckets of every hour the batch touched are recomputed last
// (hourly.go).
//
// It is begin and finish with nothing in between. An object too large to hold
// goes through the same two, with its bulk records written chunk by chunk
// between them (batchWrite.writeChunk, stream.go).
func (w *Writer) WriteAccumulated(ctx context.Context, tx TxLike, sc scope, acc *model.MapResult) (Counts, error) {
	if acc == nil {
		return Counts{}, nil
	}
	bw, err := w.begin(ctx, tx, sc, acc, false)
	if err != nil {
		return nil, err
	}
	return bw.finish(ctx, tx, acc)
}

// batchWrite is the write of one raw object in progress, inside its
// transaction: begin takes the locks and applies the deletions, writeChunk
// writes a slice of the object's bulk records, finish writes the rest and
// recomputes what the whole write touched.
type batchWrite struct {
	w  *Writer
	sc scope
	c  Counts
	// aliased is MapResult.AliasExternalIDs of the object.
	aliased bool
	// staleDays are the local days of the additive samples the deletions
	// removed.
	staleDays map[dayKey]*time.Location
	// days are the days the chunks written so far touched (touchedDays),
	// with the first zone seen for each.
	days map[dayKey]*time.Location
}

// begin starts the write of one object for sc in tx: it takes the alias lock
// of an aliased object and the merge locks, and applies the object's
// deletions. acc holds the object's deletions and every record that is not
// written through writeChunk; bulkQuantity says the chunks will carry quantity
// samples, which acc then does not show.
func (w *Writer) begin(ctx context.Context, tx TxLike, sc scope, acc *model.MapResult, bulkQuantity bool) (*batchWrite, error) {
	bw := &batchWrite{w: w, sc: sc, c: Counts{}, aliased: acc.AliasExternalIDs}
	if sc.uv != nil {
		bw.sc.hours = hourSet{}
	}
	if acc.AliasExternalIDs {
		if err := lockAliases(ctx, tx, sc); err != nil {
			return nil, err
		}
	}
	if err := lockMerged(ctx, tx, sc, acc, bulkQuantity); err != nil {
		return nil, err
	}
	if len(acc.Deletions) > 0 {
		// A deletion may reach rows in compressed chunks: one of HealthKit's
		// can span months of a user's segment. Lift the decompression cap for
		// this transaction, as the other deletion paths do, or the DELETE fails
		// past the default limit and takes the whole upload with it.
		if _, err := tx.Exec(ctx, `SET LOCAL timescaledb.max_tuples_decompressed_per_dml_transaction = 0`); err != nil {
			return nil, fmt.Errorf("apply deletions: %w", err)
		}
		apply := applyDeletions
		if acc.AliasExternalIDs {
			apply = applyAliasDeletions
		}
		days, err := apply(ctx, tx, bw.sc, acc.Deletions, bw.c)
		if err != nil {
			return nil, fmt.Errorf("apply deletions: %w", err)
		}
		bw.staleDays = days
	}
	return bw, nil
}

// writeRecords writes the records of acc: it drops those of a tombstoned
// provider record (acc is filtered in place), converts the clear rows they can
// meet, writes every family in a fixed order, then the aliases of an aliased
// object, and collects the hours of the quantity samples.
func (bw *batchWrite) writeRecords(ctx context.Context, tx TxLike, acc *model.MapResult) error {
	w, sc, c := bw.w, bw.sc, bw.c
	if err := dropTombstoned(ctx, tx, sc, acc, c); err != nil {
		return err
	}
	// The clear rows the batch can meet are converted first (legacy.go);
	// from here on every hash is the stored one: keyed when the job holds
	// keys. touchedDays reads only types, times and zones.
	if err := convertBatchLegacy(ctx, tx, sc, acc); err != nil {
		return err
	}
	acc = sc.keyed(acc)
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
			return fmt.Errorf("write %s: %w", s.name, err)
		}
	}
	if bw.aliased {
		if err := writeAliases(ctx, tx, sc, acc); err != nil {
			return err
		}
	}
	// The hours of the batch's samples, written or met again; those of the
	// samples its deletions and supersedes removed are in sc.hours already.
	for _, s := range acc.QuantitySamples {
		sc.hours.add(s.QuantityType, s.StartDate)
	}
	return nil
}

// writeChunk writes one slice of the object's bulk records (chunk is filtered
// in place) and remembers the days it touched.
func (bw *batchWrite) writeChunk(ctx context.Context, tx TxLike, chunk *model.MapResult) error {
	if err := bw.writeRecords(ctx, tx, chunk); err != nil {
		return err
	}
	bw.days = mergeDays(bw.days, touchedDays(bw.sc, chunk))
	return nil
}

// touched returns the days the whole write touched: those of its chunks, then
// those of acc, the records finish wrote (filtered).
func (bw *batchWrite) touched(acc *model.MapResult) map[dayKey]*time.Location {
	if len(bw.days) == 0 {
		return touchedDays(bw.sc, acc)
	}
	return mergeDays(bw.days, touchedDays(bw.sc, acc))
}

// mergeDays adds to dst the days of src it does not hold yet, so the first
// zone seen for a day stays, and returns dst (src when dst is nil).
func mergeDays(dst, src map[dayKey]*time.Location) map[dayKey]*time.Location {
	if dst == nil {
		return src
	}
	for k, loc := range src {
		if _, ok := dst[k]; !ok {
			dst[k] = loc
		}
	}
	return dst
}

// finish writes acc, the records of the object that did not go through
// writeChunk, then recomputes the hourly buckets of every hour the whole
// write touched and the derived daily rows of the days its deletions left
// stale. It returns the counts of the whole write.
func (bw *batchWrite) finish(ctx context.Context, tx TxLike, acc *model.MapResult) (Counts, error) {
	sc, c := bw.sc, bw.c
	if err := bw.writeRecords(ctx, tx, acc); err != nil {
		return nil, err
	}
	if err := recomputeHourSet(ctx, tx, sc, sc.hours); err != nil {
		return nil, err
	}
	// The days whose additive samples a deletion removed are summed again.
	// The days the batch's own samples touch are left to the caller, which
	// derives them after this write. Most batches delete nothing, so the
	// batch's days are only collected when there is something to subtract.
	staleDays := bw.staleDays
	if len(staleDays) > 0 {
		for k := range bw.touched(acc) {
			delete(staleDays, k)
		}
	}
	if len(staleDays) > 0 {
		n, k, err := deriveDailySummaries(ctx, tx, sc, staleDays)
		if err != nil {
			return nil, fmt.Errorf("derive daily summaries after deletions: %w", err)
		}
		if n+k > 0 {
			c.add(tDaily, n, k)
		}
	}
	return c, nil
}

// lockMerged takes, with keys, the user lock of every table whose stored rows
// this batch reads to merge in Go (latest wins, figure blanking): without it
// two jobs of one user could both read the stored row and the second write
// would drop the first one's figures. The locks are taken up front, in the
// order of the write steps, so two jobs never wait for each other in a cycle.
// The quantity lock is also taken for a batch with deletions only: removing a
// sample recomputes its hourly bucket (RecomputeHours), which takes that lock,
// and taking it there, after the daily lock of a figure clearing, would break
// the order. Without keys the merge is one SQL statement and needs no lock.
// bulkQuantity says the batch has quantity samples acc does not hold (the
// chunks of a large object).
func lockMerged(ctx context.Context, tx TxLike, sc scope, acc *model.MapResult, bulkQuantity bool) error {
	if sc.uv == nil {
		return nil
	}
	replaceable := func(n int, f func(int) bool) bool {
		for i := range n {
			if f(i) {
				return true
			}
		}
		return false
	}
	clears := replaceable(len(acc.Deletions), func(i int) bool { return len(acc.Deletions[i].ClearSummaryFields) > 0 })
	for _, l := range []struct {
		table string
		need  bool
	}{
		{tQuantity, bulkQuantity || len(acc.QuantitySamples) > 0 || len(acc.Deletions) > 0},
		{tNutrition, replaceable(len(acc.NutritionLogs), func(i int) bool { return acc.NutritionLogs[i].Replaceable })},
		{tSleep, replaceable(len(acc.SleepSessions), func(i int) bool { return acc.SleepSessions[i].Replaceable })},
		{tWorkouts, replaceable(len(acc.Workouts), func(i int) bool { return acc.Workouts[i].Replaceable })},
		{tDaily, clears || replaceable(len(acc.DailySummaries), func(i int) bool { return acc.DailySummaries[i].Replaceable })},
	} {
		if l.need {
			if err := lockUserTable(ctx, tx, l.table, sc); err != nil {
				return err
			}
		}
	}
	return nil
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
// rows is read once, so a caller can generate them as the COPY consumes them
// (lazyRows) instead of holding a second copy of the batch.
func stage(ctx context.Context, tx TxLike, table string, columns []string, rows pgx.CopyFromSource) (temp string, copied int64, err error) {
	return stageAs(ctx, tx, table, "_tmp_"+table, columns, rows)
}

// stageAs is stage into the staging table name, for a second column set of
// one table inside one transaction.
func stageAs(ctx context.Context, tx TxLike, table, name string, columns []string, rows pgx.CopyFromSource) (temp string, copied int64, err error) {
	temp = pgx.Identifier{name}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`CREATE TEMPORARY TABLE IF NOT EXISTS %s ON COMMIT DROP AS SELECT %s FROM %s WITH NO DATA`,
		temp, quoteCols(columns), pgx.Identifier{"ohs_data", table}.Sanitize())); err != nil {
		return "", 0, fmt.Errorf("create staging for %s: %w", table, err)
	}
	if _, err := tx.Exec(ctx, "TRUNCATE TABLE "+temp); err != nil {
		return "", 0, fmt.Errorf("truncate staging for %s: %w", table, err)
	}
	copied, err = tx.CopyFrom(ctx, pgx.Identifier{name}, columns, rows)
	if err != nil {
		return "", 0, fmt.Errorf("copy to staging for %s: %w", table, err)
	}
	return temp, copied, nil
}

// writeHighFrequencyCopy stages rows with COPY into a temporary table and moves
// them into the target with INSERT ... SELECT ... ON CONFLICT DO NOTHING.
func (w *Writer) writeHighFrequencyCopy(ctx context.Context, tx TxLike, table string, columns []string, rows pgx.CopyFromSource, conflictCols string) (written, skipped int, err error) {
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

// kindWindows is the time range a batch covers for each kind of row it
// carries (the quantity type of a sample), plus the range of the whole batch.
// A part that holds five years of weights and one day of steps then reads the
// stored weights of five years and the stored steps of one day, not every
// stored row of every type over five years.
type kindWindows struct {
	all    window
	byKind map[string]*window
}

// extend widens the window of kind, and the whole batch's, to include at.
func (kw *kindWindows) extend(kind string, at time.Time) {
	kw.all.extend(at)
	if kw.byKind == nil {
		kw.byKind = map[string]*window{}
	}
	wd, ok := kw.byKind[kind]
	if !ok {
		wd = &window{}
		kw.byKind[kind] = wd
	}
	wd.extend(at)
}

// arrays returns the kinds and their bounds as parallel arrays, sorted by
// kind so the statement arguments are stable.
func (kw *kindWindows) arrays() (kinds []string, lo, hi []time.Time) {
	kinds = make([]string, 0, len(kw.byKind))
	for k := range kw.byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	lo, hi = make([]time.Time, len(kinds)), make([]time.Time, len(kinds))
	for i, k := range kinds {
		lo[i], hi[i] = kw.byKind[k].lo, kw.byKind[k].hi
	}
	return kinds, lo, hi
}

// insertMissing inserts the staged rows of temp whose (timeCol, content_hash)
// is not stored yet for the scope's user, and returns how many it inserted.
//
// The stored set is read per kind (kindCol, the table's type column) over that
// kind's own window, which the (tenant_id, user_id, <kind>, <time>) index
// serves. The whole batch's window is repeated as a plain bound so chunks
// outside it are excluded when the statement is planned.
func insertMissing(ctx context.Context, tx TxLike, sc scope, table, timeCol, kindCol, temp string, columns []string, kw kindWindows) (int, error) {
	cols := quoteCols(columns)
	at := pgx.Identifier{timeCol}.Sanitize()
	kind := pgx.Identifier{kindCol}.Sanitize()
	kinds, lo, hi := kw.arrays()
	tag, err := tx.Exec(ctx, fmt.Sprintf(`WITH stored AS MATERIALIZED (
			SELECT t.%[1]s AS at, t.content_hash
			FROM unnest($3::text[], $4::timestamptz[], $5::timestamptz[]) AS k(kind, lo, hi)
			JOIN %[2]s t ON t.%[5]s = k.kind AND t.%[1]s BETWEEN k.lo AND k.hi
			WHERE t.tenant_id = $1 AND t.user_id = $2 AND t.%[1]s BETWEEN $6 AND $7)
		INSERT INTO %[2]s (%[3]s)
		SELECT %[3]s FROM %[4]s s
		WHERE NOT EXISTS (SELECT 1 FROM stored WHERE stored.at = s.%[1]s AND stored.content_hash = s.content_hash)`,
		at, qualified(table), cols, temp, kind), sc.tenant, sc.user, kinds, lo, hi, kw.all.lo, kw.all.hi)
	if err != nil {
		return 0, fmt.Errorf("insert missing into %s: %w", table, err)
	}
	return int(tag.RowsAffected()), nil
}

// updateStored applies latest wins (see latestWins) from the staged rows of
// temp to the stored rows with the same (kindCol, timeCol, content_hash), and
// returns how many stored rows changed. The kind is part of the join so each
// staged row finds its stored copy through the type index. extra are the
// promoted columns that follow metadata.
func updateStored(ctx context.Context, tx TxLike, sc scope, table, timeCol, kindCol, temp string, figures, extra []string, wd window) (int, error) {
	at := pgx.Identifier{timeCol}.Sanitize()
	kind := pgx.Identifier{kindCol}.Sanitize()
	set := make([]string, 0, len(figures)+2+len(extra))
	stored := make([]string, 0, len(figures))
	merged := make([]string, 0, len(figures))
	for _, f := range figures {
		m := fmt.Sprintf("COALESCE(s.%s, t.%s)", f, f)
		set = append(set, f+" = "+m)
		stored = append(stored, "t."+f)
		merged = append(merged, m)
	}
	set = append(set, "metadata = s.metadata", "captured_at = s.captured_at")
	for _, x := range extra {
		set = append(set, x+" = s."+x)
	}
	tag, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %[1]s t SET %[2]s FROM %[3]s s
		WHERE t.tenant_id = $1 AND t.user_id = $2 AND t.%[4]s BETWEEN $3 AND $4
		  AND t.%[7]s = s.%[7]s AND t.%[4]s = s.%[4]s AND t.content_hash = s.content_hash
		  AND (%[5]s) IS DISTINCT FROM (%[6]s)
		  AND %[8]s`,
		qualified(table), strings.Join(set, ", "), temp, at, strings.Join(stored, ", "), strings.Join(merged, ", "),
		kind, notStale("t.captured_at", "s.captured_at")),
		sc.tenant, sc.user, wd.lo, wd.hi)
	if err != nil {
		return 0, fmt.Errorf("update stored %s: %w", table, err)
	}
	return int(tag.RowsAffected()), nil
}

// advanceStored moves the capture time of the stored rows forward to that of
// the staged rows of temp with the same (kindCol, timeCol, content_hash),
// where the staged copy is newer (see newerCapture). It runs after
// updateStored, so the rows it reaches are the ones the new copy left as they
// were. They are not counted as written.
func advanceStored(ctx context.Context, tx TxLike, sc scope, table, timeCol, kindCol, temp string, wd window) error {
	at := pgx.Identifier{timeCol}.Sanitize()
	kind := pgx.Identifier{kindCol}.Sanitize()
	_, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %[1]s t SET captured_at = s.captured_at FROM %[2]s s
		WHERE t.tenant_id = $1 AND t.user_id = $2 AND t.%[3]s BETWEEN $3 AND $4
		  AND t.%[4]s = s.%[4]s AND t.%[3]s = s.%[3]s AND t.content_hash = s.content_hash
		  AND %[5]s`,
		qualified(table), temp, at, kind, newerCapture("t.captured_at", "s.captured_at")),
		sc.tenant, sc.user, wd.lo, wd.hi)
	if err != nil {
		return fmt.Errorf("advance capture time of %s: %w", table, err)
	}
	return nil
}

// updateStoredSealed is updateStored for rows staged sealed, each with the id
// of the stored copy it was merged with: the merge already ran in Go
// (mergeUpserts), which also left out the copies older than the stored row,
// so the staged row replaces that copy when its content MAC or a clear figure
// differs. The capture guard is repeated here (shape.notStale): a stale copy
// never overwrites a newer one, whatever staged it.
func updateStoredSealed(ctx context.Context, tx TxLike, sc scope, s *shape, timeCol, kindCol, temp string, figures []string, wd window) (int, error) {
	at := pgx.Identifier{timeCol}.Sanitize()
	kind := pgx.Identifier{kindCol}.Sanitize()
	tag, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %[1]s t SET %[2]s FROM %[3]s s
		WHERE t.tenant_id = $1 AND t.user_id = $2 AND t.%[4]s BETWEEN $3 AND $4
		  AND t.%[6]s = s.%[6]s AND t.%[4]s = s.%[4]s AND t.content_hash = s.content_hash AND t.id = s.id
		  AND %[5]s%[7]s`,
		qualified(s.t.Name), strings.Join(s.sealedSet("s", figures), ", "), temp, at, s.sealedDistinct("t", "s", figures),
		kind, s.notStale("t", "s")),
		sc.tenant, sc.user, wd.lo, wd.hi)
	if err != nil {
		return 0, fmt.Errorf("update stored %s: %w", s.t.Name, err)
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
	if len(last) == 0 {
		// Nothing to upsert: the batch is returned as it is, not copied.
		return rows, nil, 0
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
// follows the figures it describes. The one thing such a copy can still move is
// the stored capture time, when it is the newer of the two: that is a second
// statement (advanceCapture, advanceStored), so it is never counted as a write.
//
// "Latest" is the capture time of the raw object a copy came from
// (captured_at), not the order the jobs happen to run in: a retried, replayed
// or requeued job for an older object does not overwrite what a newer object
// stored (see notStale). extra are the promoted columns read from metadata,
// which follow it.
func latestWins(table, conflictCols string, figures, extra []string) string {
	// The existing row is referenced by the unqualified table name.
	alias := table
	set := make([]string, 0, len(figures)+2)
	stored := make([]string, 0, len(figures))
	merged := make([]string, 0, len(figures))
	for _, f := range figures {
		m := fmt.Sprintf("COALESCE(EXCLUDED.%s, %s.%s)", f, alias, f)
		set = append(set, f+" = "+m)
		stored = append(stored, alias+"."+f)
		merged = append(merged, m)
	}
	set = append(set, "metadata = EXCLUDED.metadata", "captured_at = EXCLUDED.captured_at")
	for _, x := range extra {
		set = append(set, x+" = EXCLUDED."+x)
	}
	return fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET %s WHERE (%s) IS DISTINCT FROM (%s) AND %s",
		conflictCols, strings.Join(set, ", "), strings.Join(stored, ", "), strings.Join(merged, ", "),
		notStale(alias+".captured_at", "EXCLUDED.captured_at"))
}

// notStale is the condition under which a new copy may replace a stored row:
// it was captured at or after the stored one. Equal times replace, so
// processing one object again after a mapper fix still corrects its rows. A
// row stored before captured_at existed, and a copy whose capture time is
// unknown, carry no order and replace as before.
func notStale(stored, incoming string) string {
	return fmt.Sprintf("(%[1]s IS NULL OR %[2]s IS NULL OR %[2]s >= %[1]s)", stored, incoming)
}

// newerCapture is the condition under which a copy that changed nothing still
// moves the stored capture time forward: it was captured after the stored row,
// or the stored row has no capture time yet. Without it the row keeps the time
// of the object that last changed its figures, and an object captured between
// that one and the newest could overwrite what the newest confirmed. An equal
// time is not newer: processing one object again writes no row.
func newerCapture(stored, incoming string) string {
	return fmt.Sprintf("(%[2]s > %[1]s OR (%[1]s IS NULL AND %[2]s IS NOT NULL))", stored, incoming)
}

// advanceCapture is the conflict action that only moves the capture time of
// the stored row forward (see newerCapture). It follows a latestWins insert
// of the same rows, which left the rows it reaches as they were.
func advanceCapture(table, conflictCols string) string {
	return fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET captured_at = EXCLUDED.captured_at WHERE %s",
		conflictCols, newerCapture(table+".captured_at", "EXCLUDED.captured_at"))
}

// upsertBatches inserts Replaceable rows with latest wins (replace, the
// table's latestWins clause), then moves the capture time of the rows the
// insert left as they were forward (advance, its advanceCapture clause). The
// second pass only runs when a row was skipped and the capture time is known.
func (w *Writer) upsertBatches(ctx context.Context, tx TxLike, sc scope, table, head, replace, advance string, colCount int, rows [][]any) (written, skipped int, err error) {
	written, skipped, err = w.insertBatches(ctx, tx, table, head, replace, colCount, rows)
	if err != nil || skipped == 0 || sc.captured.IsZero() {
		return written, skipped, err
	}
	if _, _, err := w.insertBatches(ctx, tx, table, head, advance, colCount, rows); err != nil {
		return written, skipped, fmt.Errorf("advance capture time: %w", err)
	}
	return written, skipped, nil
}

// advanceParent moves the capture time of one stored Replaceable parent row
// forward (see newerCapture). update is the table's statement: tenant, user,
// content hash, start, capture time.
func advanceParent(ctx context.Context, tx TxLike, sc scope, update, hash string, start time.Time) error {
	if sc.captured.IsZero() {
		return nil
	}
	_, err := tx.Exec(ctx, update, sc.tenant, sc.user, hash, start, sc.captured)
	return err
}

// advanceCaptured moves the capture time of stored rows of table forward to
// the scope's, by id: the rows a sealed upsert merged in Go and left as they
// were (mergeUpserts). at is each row's timeCol (of SQL type timeType), which
// with kinds (the rows' kindCol, when the table has one) lets the statement
// reach each row through the user's time index instead of scanning for ids.
func advanceCaptured(ctx context.Context, tx TxLike, sc scope, table, timeCol, timeType, kindCol string, adv advanceSet) error {
	if len(adv.ids) == 0 || sc.captured.IsZero() {
		return nil
	}
	at := pgx.Identifier{timeCol}.Sanitize()
	args := []any{sc.tenant, sc.user, sc.captured, adv.ids, adv.times, adv.wd.lo, adv.wd.hi}
	from, join := fmt.Sprintf("unnest($4::bigint[], $5::%s[]) AS a(id, at)", timeType), ""
	if kindCol != "" {
		args = append(args, adv.kinds)
		from = fmt.Sprintf("unnest($4::bigint[], $5::%s[], $8::text[]) AS a(id, at, kind)", timeType)
		join = " AND t." + pgx.Identifier{kindCol}.Sanitize() + " = a.kind"
	}
	_, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %[1]s t SET captured_at = $3 FROM %[2]s
		WHERE t.tenant_id = $1 AND t.user_id = $2 AND t.%[3]s BETWEEN $6 AND $7
		  AND t.%[3]s = a.at AND t.id = a.id%[4]s AND %[5]s`,
		qualified(table), from, at, join, newerCapture("t.captured_at", "$3::timestamptz")), args...)
	if err != nil {
		return fmt.Errorf("advance capture time of %s: %w", table, err)
	}
	return nil
}

// advanceSet names the stored rows whose capture time a sealed upsert moves
// forward: their ids, their time column and, for a table with a kind column,
// their kind.
type advanceSet struct {
	ids   []int64
	times []time.Time
	kinds []string
	wd    window
}

func (a *advanceSet) add(id int64, at time.Time, kind string) {
	a.ids, a.times, a.kinds = append(a.ids, id), append(a.times, at), append(a.kinds, kind)
	a.wd.extend(at)
}

// lazyRows is a COPY source that builds the row of each record as the COPY
// reads it, so the batch is never held a second time as [][]any. row returns
// the finished row (sealed with keys); skip, when set, drops a record (an
// in-batch duplicate) and is called once per record, in order.
func lazyRows[T any](recs []T, row func(T) ([]any, error), skip func(T) bool) pgx.CopyFromSource {
	i := 0
	return pgx.CopyFromFunc(func() ([]any, error) {
		for i < len(recs) {
			r := recs[i]
			i++
			if skip != nil && skip(r) {
				continue
			}
			return row(r)
		}
		return nil, nil
	})
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
