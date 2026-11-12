// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Flat record families: one mapped record is one row, no parent to resolve.

// hyperConflict is the dedup unique of a hypertable partitioned on timeCol.
func hyperConflict(timeCol string) string {
	return "tenant_id, user_id, content_hash, " + timeCol
}

// plainConflict is the dedup unique of a plain table.
const plainConflict = "tenant_id, user_id, content_hash"

// nullTime turns the zero time into NULL for nullable timestamp columns.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// nullString turns the empty string into NULL for nullable text columns.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// sourceSlugOrProvider guarantees source_slug is never written empty. The
// pipeline fills SourceSlug at the attribution choke point; this covers a
// sample that reached the writer another way.
func sourceSlugOrProvider(slug, provider string) string {
	if slug != "" {
		return slug
	}
	return strings.ToLower(provider)
}

// dateOnly keeps the calendar date of t, in t's own location, as midnight UTC.
func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// --- quantity_samples ---

// quantitySampleColumns is the write shape of quantity_samples, shared by the
// INSERT and the COPY path so the two cannot drift.
var quantitySampleColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "source_name", "source_bundle_id", "device_id",
	"product_type", "device_model", "was_user_entered",
	"external_id", "quantity_type", "value", "unit", "is_primary_source",
	"start_date", "end_date", "content_hash", "metadata", "captured_at",
}

// quantitySampleRow is the single source of truth for the values of one
// quantity sample, same order as quantitySampleColumns.
func quantitySampleRow(sc scope, s model.QuantitySample) []any {
	p := sc.providerOr(s.Provider)
	return []any{
		sc.tenant, sc.user, p, sourceSlugOrProvider(s.SourceSlug, p), s.SourceName, s.SourceBundleID,
		s.DeviceID, s.ProductType, s.DeviceModel, s.WasUserEntered,
		s.ExternalID, s.QuantityType, s.Value,
		s.Unit, s.IsPrimarySource, s.StartDate, nullTime(s.EndDate),
		s.ContentHash, normalizeMetadata(s.Metadata), sc.capturedAt(),
	}
}

// quantityFigures are the columns a Replaceable quantity sample replaces.
var quantityFigures = []string{"value", "unit"}

// writeQuantitySamples writes quantity samples. quantity_samples has no dedup
// unique, because it is compressed after a while: duplicates are found in the
// stored rows of the batch's time window, per quantity type (see
// insertMissing). Inside the batch, the first copy of an insert-only row wins
// and the last copy of a Replaceable row wins. Rows are built as the COPY
// reads them (lazyRows), so the batch is not held twice.
func (w *Writer) writeQuantitySamples(ctx context.Context, tx TxLike, sc scope, samples []model.QuantitySample) (written, skipped int, err error) {
	insertOnly, upsert, superseded := replaceableSplit(samples,
		func(s model.QuantitySample) bool { return s.Replaceable },
		func(s model.QuantitySample) copyDedupeKey {
			return copyDedupeKey{hash: s.ContentHash, stamp: s.StartDate.UnixNano()}
		})
	if err := lockUserTable(ctx, tx, tQuantity, sc); err != nil {
		return 0, 0, err
	}
	if len(insertOnly) > 0 {
		seen := make(copyDedupe, len(insertOnly))
		var kw kindWindows
		temp, _, err := stage(ctx, tx, tQuantity, quantitySampleColumns, lazyRows(insertOnly, sc, quantitySampleRow,
			func(s model.QuantitySample) bool {
				if seen.staged(s.ContentHash, s.StartDate) {
					return true
				}
				kw.extend(s.QuantityType, s.StartDate)
				return false
			}))
		if err != nil {
			return 0, 0, err
		}
		n, err := insertMissing(ctx, tx, sc, tQuantity, "start_date", "quantity_type", temp, quantitySampleColumns, kw)
		if err != nil {
			return 0, 0, err
		}
		written, skipped = written+n, skipped+len(insertOnly)-n
	}
	upsert, moved := supersedeMovedSplit(sc, upsert)
	superseded += moved
	upsert, stale, err := dropStaleMovedSamples(ctx, tx, sc, upsert)
	if err != nil {
		return written, skipped, err
	}
	superseded += stale
	if err := supersedeMovedSamples(ctx, tx, sc, upsert); err != nil {
		return written, skipped, err
	}
	if len(upsert) > 0 {
		var kw kindWindows
		for _, s := range upsert {
			kw.extend(s.QuantityType, s.StartDate)
		}
		temp, _, err := stage(ctx, tx, tQuantity, quantitySampleColumns, lazyRows(upsert, sc, quantitySampleRow, nil))
		if err != nil {
			return written, skipped, err
		}
		changed, err := updateStored(ctx, tx, sc, tQuantity, "start_date", "quantity_type", temp, quantityFigures, kw.all)
		if err != nil {
			return written, skipped, err
		}
		added, err := insertMissing(ctx, tx, sc, tQuantity, "start_date", "quantity_type", temp, quantitySampleColumns, kw)
		if err != nil {
			return written, skipped, err
		}
		// The copies that changed nothing still confirm the row at their
		// capture time. Only then is there anything to move.
		if changed+added < len(upsert) && !sc.captured.IsZero() {
			if err := advanceStored(ctx, tx, sc, tQuantity, "start_date", "quantity_type", temp, kw.all); err != nil {
				return written, skipped, err
			}
		}
		written, skipped = written+changed+added, skipped+len(upsert)-changed-added
	}
	return written, skipped + superseded, nil
}

// quantityNewerCopy returns the positions ($3) of the SupersedeMoved samples
// ($4 to $6: provider, external id, quantity type) whose provider record is
// stored from an object captured after this one ($7), under any start.
const quantityNewerCopy = `SELECT n.i
	FROM unnest($3::int[], $4::text[], $5::text[], $6::text[]) AS n(i, provider, external_id, quantity_type)
	WHERE EXISTS (SELECT 1 FROM ohs_data.quantity_samples q
		WHERE q.tenant_id = $1 AND q.user_id = $2
		  AND q.provider = n.provider AND q.external_id IS NOT NULL AND q.external_id COLLATE "C" = n.external_id
		  AND q.quantity_type = n.quantity_type AND q.captured_at > $7)`

// dropStaleMovedSamples drops the SupersedeMoved samples whose provider
// record a newer object already stored, and counts them. Without it a stale
// copy under another start would delete the newer copy and take its place:
// the capture guard of updateStored only sees copies under the same start.
func dropStaleMovedSamples(ctx context.Context, tx TxLike, sc scope, samples []model.QuantitySample) ([]model.QuantitySample, int, error) {
	if sc.captured.IsZero() {
		return samples, 0, nil
	}
	var idx []int32
	var providers, ids, types []string
	for i, s := range samples {
		if supersedes(s) {
			idx = append(idx, int32(i))
			providers = append(providers, sc.providerOr(s.Provider))
			ids = append(ids, s.ExternalID)
			types = append(types, s.QuantityType)
		}
	}
	if len(idx) == 0 {
		return samples, 0, nil
	}
	stale, err := queryPositions(ctx, tx, quantityNewerCopy, sc.tenant, sc.user, idx, providers, ids, types, sc.captured)
	if err != nil {
		return nil, 0, fmt.Errorf("look up newer samples: %w", err)
	}
	return dropPositions(samples, stale), len(stale), nil
}

// queryPositions runs a lookup over a batch passed as parallel arrays and
// returns the positions (the int column the statement selects) it names.
func queryPositions(ctx context.Context, tx TxLike, sql string, args ...any) (map[int]bool, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var i int32
		if err := rows.Scan(&i); err != nil {
			return nil, err
		}
		out[int(i)] = true
	}
	return out, rows.Err()
}

// dropPositions returns in without the elements at the positions in drop.
func dropPositions[T any](in []T, drop map[int]bool) []T {
	if len(drop) == 0 {
		return in
	}
	out := make([]T, 0, len(in)-len(drop))
	for i, r := range in {
		if !drop[i] {
			out = append(out, r)
		}
	}
	return out
}

// quantitySupersede deletes the stored copies of SupersedeMoved samples under
// another start: same provider, external id and quantity type ($3 to $5, one
// element per sample), another start_date ($6). The dedup unique includes
// start_date, so the upsert alone cannot reach them. The external id is
// matched on the index of migration 0014.
const quantitySupersede = `DELETE FROM ohs_data.quantity_samples q
	USING unnest($3::text[], $4::text[], $5::text[], $6::timestamptz[]) AS n(provider, external_id, quantity_type, start_date)
	WHERE q.tenant_id = $1 AND q.user_id = $2
	  AND q.provider = n.provider AND q.external_id IS NOT NULL AND q.external_id COLLATE "C" = n.external_id
	  AND q.quantity_type = n.quantity_type AND q.start_date <> n.start_date`

// supersedes reports a sample whose stored copies under another start the
// writer deletes (QuantitySample.SupersedeMoved).
func supersedes(s model.QuantitySample) bool {
	return s.SupersedeMoved && s.Replaceable && s.ExternalID != ""
}

// supersedeKey is the provider record figure a SupersedeMoved sample is.
type supersedeKey struct{ provider, externalID, quantityType string }

// supersedeMovedSplit keeps, of several SupersedeMoved copies of one figure
// in the batch (a start that moved between two pulls of one object), the last
// one, and counts the others.
func supersedeMovedSplit(sc scope, samples []model.QuantitySample) ([]model.QuantitySample, int) {
	last := map[supersedeKey]int{}
	for i, s := range samples {
		if supersedes(s) {
			last[supersedeKey{sc.providerOr(s.Provider), s.ExternalID, s.QuantityType}] = i
		}
	}
	if len(last) == 0 {
		return samples, 0
	}
	out := samples[:0:0]
	for i, s := range samples {
		if supersedes(s) && last[supersedeKey{sc.providerOr(s.Provider), s.ExternalID, s.QuantityType}] != i {
			continue
		}
		out = append(out, s)
	}
	return out, len(samples) - len(out)
}

// supersedeMovedSamples deletes, before the upsert, the stored copies of the
// SupersedeMoved samples whose start moved. They are not counted: the new
// copy is the record's figure.
func supersedeMovedSamples(ctx context.Context, tx TxLike, sc scope, samples []model.QuantitySample) error {
	var providers, ids, types []string
	var starts []time.Time
	for _, s := range samples {
		if !supersedes(s) {
			continue
		}
		providers = append(providers, sc.providerOr(s.Provider))
		ids = append(ids, s.ExternalID)
		types = append(types, s.QuantityType)
		starts = append(starts, s.StartDate)
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, quantitySupersede, sc.tenant, sc.user, providers, ids, types, starts); err != nil {
		return fmt.Errorf("supersede moved samples: %w", err)
	}
	return nil
}

// rowsOf maps records to rows with fn.
func rowsOf[T any](in []T, sc scope, fn func(scope, T) []any) [][]any {
	out := make([][]any, len(in))
	for i, r := range in {
		out[i] = fn(sc, r)
	}
	return out
}

// copyOrInsert writes rows of a first-write-wins table: COPY above the
// threshold (dropping in-batch duplicates by key, and building each row as the
// COPY reads it), batched INSERTs below.
func copyOrInsert[T any](ctx context.Context, w *Writer, tx TxLike, sc scope, table string, columns []string, conflict string,
	recs []T, row func(scope, T) []any, key func(T) (string, time.Time)) (written, skipped int, err error) {
	if len(recs) > w.cfg.CopyThreshold {
		seen := make(copyDedupe, len(recs))
		dupes := 0
		rows := lazyRows(recs, sc, row, func(r T) bool {
			h, at := key(r)
			if seen.staged(h, at) {
				dupes++
				return true
			}
			return false
		})
		written, skipped, err = w.writeHighFrequencyCopy(ctx, tx, table, columns, rows, conflict)
		if err != nil {
			return 0, 0, err
		}
		return written, skipped + dupes, nil
	}
	return w.insertBatches(ctx, tx, table, insertHead(table, columns), doNothing(conflict), len(columns), rowsOf(recs, sc, row))
}

// --- category_samples ---

var categorySampleColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "source_name", "external_id", "category_type",
	"value_int", "value_label", "start_date", "end_date", "content_hash", "metadata",
}

func categorySampleRow(sc scope, s model.CategorySample) []any {
	p := sc.providerOr(s.Provider)
	return []any{
		sc.tenant, sc.user, p, sourceSlugOrProvider("", p), s.SourceName, s.ExternalID, s.CategoryType,
		s.ValueInt, s.ValueLabel, s.StartDate, nullTime(s.EndDate), s.ContentHash, normalizeMetadata(s.Metadata),
	}
}

// writeCategorySamples writes category samples, switching to COPY above the
// threshold: sleep analysis alone makes this a high-volume table on a backfill.
func (w *Writer) writeCategorySamples(ctx context.Context, tx TxLike, sc scope, samples []model.CategorySample) (written, skipped int, err error) {
	return copyOrInsert(ctx, w, tx, sc, tCategory, categorySampleColumns, hyperConflict("start_date"), samples, categorySampleRow,
		func(s model.CategorySample) (string, time.Time) { return s.ContentHash, s.StartDate })
}

// --- nutrition_logs ---

var nutritionColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "external_id", "start_date", "end_date",
	"meal_type", "energy_kcal", "protein_g", "carbs_g", "fat_g",
	"fiber_g", "sugar_g", "sodium_mg", "nutrients_json", "content_hash", "metadata", "captured_at",
}

func nutritionRow(sc scope, s model.NutritionLog) []any {
	p := sc.providerOr(s.Provider)
	nj := s.NutrientsJSON
	if nj == nil {
		nj = map[string]any{}
	}
	return []any{
		sc.tenant, sc.user, p, sourceSlugOrProvider("", p), s.ExternalID, s.StartDate, s.EndDate,
		s.MealType, s.EnergyKcal, s.ProteinG, s.CarbsG, s.FatG,
		s.FiberG, s.SugarG, s.SodiumMg, nj, s.ContentHash, normalizeMetadata(s.Metadata), sc.capturedAt(),
	}
}

var nutritionLatestWins = latestWins(tNutrition, hyperConflict("start_date"),
	[]string{"energy_kcal", "protein_g", "carbs_g", "fat_g", "fiber_g", "sugar_g", "sodium_mg", "nutrients_json"})

var nutritionAdvance = advanceCapture(tNutrition, hyperConflict("start_date"))

// writeNutritionLogs batch-inserts nutrition logs. A duplicate is skipped,
// except a Replaceable log, whose stored figures the new copy replaces.
func (w *Writer) writeNutritionLogs(ctx context.Context, tx TxLike, sc scope, logs []model.NutritionLog) (written, skipped int, err error) {
	insertOnly, upsert, superseded := replaceableSplit(logs,
		func(l model.NutritionLog) bool { return l.Replaceable },
		func(l model.NutritionLog) copyDedupeKey {
			return copyDedupeKey{hash: l.ContentHash, stamp: l.StartDate.UnixNano()}
		})
	upsert, moved := supersedeMovedLogs(sc, upsert)
	superseded += moved
	upsert, stale, err := dropStaleMovedLogs(ctx, tx, sc, upsert)
	if err != nil {
		return written, skipped, err
	}
	superseded += stale
	if err := deleteMovedLogs(ctx, tx, sc, upsert); err != nil {
		return written, skipped, err
	}
	head := insertHead(tNutrition, nutritionColumns)
	written, skipped, err = w.insertBatches(ctx, tx, tNutrition, head, doNothing(hyperConflict("start_date")),
		len(nutritionColumns), rowsOf(insertOnly, sc, nutritionRow))
	if err != nil {
		return written, skipped, err
	}
	n, k, err := w.upsertBatches(ctx, tx, sc, tNutrition, head, nutritionLatestWins, nutritionAdvance,
		len(nutritionColumns), rowsOf(upsert, sc, nutritionRow))
	if err != nil {
		return written, skipped, err
	}
	return written + n, skipped + k + superseded, nil
}

// nutritionSupersede deletes the stored copies of SupersedeMoved logs under
// another start: same provider and external id ($3, $4, one element per
// log), another start_date ($5). The dedup unique includes start_date, so
// the upsert alone cannot reach them.
const nutritionSupersede = `DELETE FROM ohs_data.nutrition_logs n
	USING unnest($3::text[], $4::text[], $5::timestamptz[]) AS m(provider, external_id, start_date)
	WHERE n.tenant_id = $1 AND n.user_id = $2
	  AND n.provider = m.provider AND n.external_id = m.external_id AND n.start_date <> m.start_date`

// nutritionNewerCopy returns the positions ($3) of the SupersedeMoved logs
// ($4, $5: provider, external id) whose provider record is stored from an
// object captured after this one ($6), under any start.
const nutritionNewerCopy = `SELECT m.i
	FROM unnest($3::int[], $4::text[], $5::text[]) AS m(i, provider, external_id)
	WHERE EXISTS (SELECT 1 FROM ohs_data.nutrition_logs n
		WHERE n.tenant_id = $1 AND n.user_id = $2
		  AND n.provider = m.provider AND n.external_id = m.external_id AND n.captured_at > $6)`

// dropStaleMovedLogs drops the SupersedeMoved logs whose provider record a
// newer object already stored, and counts them (see dropStaleMovedSamples).
func dropStaleMovedLogs(ctx context.Context, tx TxLike, sc scope, logs []model.NutritionLog) ([]model.NutritionLog, int, error) {
	if sc.captured.IsZero() {
		return logs, 0, nil
	}
	var idx []int32
	var providers, ids []string
	for i, l := range logs {
		if supersedesLog(l) {
			idx = append(idx, int32(i))
			providers = append(providers, sc.providerOr(l.Provider))
			ids = append(ids, l.ExternalID)
		}
	}
	if len(idx) == 0 {
		return logs, 0, nil
	}
	stale, err := queryPositions(ctx, tx, nutritionNewerCopy, sc.tenant, sc.user, idx, providers, ids, sc.captured)
	if err != nil {
		return nil, 0, fmt.Errorf("look up newer nutrition logs: %w", err)
	}
	return dropPositions(logs, stale), len(stale), nil
}

func supersedesLog(l model.NutritionLog) bool {
	return l.SupersedeMoved && l.Replaceable && l.ExternalID != ""
}

// supersedeMovedLogs keeps, of several SupersedeMoved copies of one log in
// the batch, the last one, and counts the others.
func supersedeMovedLogs(sc scope, logs []model.NutritionLog) ([]model.NutritionLog, int) {
	type key struct{ provider, externalID string }
	last := map[key]int{}
	for i, l := range logs {
		if supersedesLog(l) {
			last[key{sc.providerOr(l.Provider), l.ExternalID}] = i
		}
	}
	if len(last) == 0 {
		return logs, 0
	}
	out := logs[:0:0]
	for i, l := range logs {
		if supersedesLog(l) && last[key{sc.providerOr(l.Provider), l.ExternalID}] != i {
			continue
		}
		out = append(out, l)
	}
	return out, len(logs) - len(out)
}

// deleteMovedLogs deletes, before the upsert, the stored copies of the
// SupersedeMoved logs whose start moved. They are not counted: the new copy
// is the record.
func deleteMovedLogs(ctx context.Context, tx TxLike, sc scope, logs []model.NutritionLog) error {
	var providers, ids []string
	var starts []time.Time
	for _, l := range logs {
		if !supersedesLog(l) {
			continue
		}
		providers = append(providers, sc.providerOr(l.Provider))
		ids = append(ids, l.ExternalID)
		starts = append(starts, l.StartDate)
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, nutritionSupersede, sc.tenant, sc.user, providers, ids, starts); err != nil {
		return fmt.Errorf("supersede moved nutrition logs: %w", err)
	}
	return nil
}

// --- heartbeat_samples ---

var heartbeatColumns = []string{"tenant_id", "user_id", "provider", "series_id", "timestamp", "rr_ms", "content_hash"}

func heartbeatRow(sc scope, s model.HeartbeatSample) []any {
	return []any{sc.tenant, sc.user, sc.provider, s.SeriesID, s.Timestamp, s.RRMS, model.HeartbeatSampleContentHash(s)}
}

// writeHeartbeatSamples writes heartbeat samples, using COPY above threshold.
func (w *Writer) writeHeartbeatSamples(ctx context.Context, tx TxLike, sc scope, samples []model.HeartbeatSample) (written, skipped int, err error) {
	return copyOrInsert(ctx, w, tx, sc, tHeartbeat, heartbeatColumns, hyperConflict(`"timestamp"`), samples, heartbeatRow,
		func(s model.HeartbeatSample) (string, time.Time) {
			return model.HeartbeatSampleContentHash(s), s.Timestamp
		})
}

// --- observations ---

var observationColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "source_name", "device_id", "ts", "end_ts",
	"canonical_code", "value_num", "value_code", "value_text", "unit_ucum", "value_canonical",
	"is_primary_source", "type_concept", "source_value", "raw_ref", "content_hash", "metadata",
}

func observationHash(o model.Observation) string {
	if o.ContentHash != "" {
		return o.ContentHash
	}
	return model.ObservationContentHash(o)
}

func observationRow(sc scope, o model.Observation) []any {
	p := sc.providerOr(o.Provider)
	return []any{
		sc.tenant, sc.user, p, sourceSlugOrProvider("", p), o.SourceName, o.DeviceID, o.TS, nullTime(o.EndTS),
		o.CanonicalCode, o.ValueNum, o.ValueCode, o.ValueText, o.UnitUCUM, o.ValueCanonical,
		o.IsPrimarySource, o.TypeConcept, o.SourceValue, o.S3Ref, observationHash(o), normalizeMetadata(o.Metadata),
	}
}

// writeObservations writes generic coded observations, the long tail of any
// metric without a typed table, switching to COPY above the threshold.
func (w *Writer) writeObservations(ctx context.Context, tx TxLike, sc scope, obs []model.Observation) (written, skipped int, err error) {
	return copyOrInsert(ctx, w, tx, sc, tObservation, observationColumns, hyperConflict("ts"), obs, observationRow,
		func(o model.Observation) (string, time.Time) { return observationHash(o), o.TS })
}

// --- daily_summaries ---

var dailyColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "summary_date",
	"readiness_score", "activity_score", "sleep_score", "recovery_score", "strain_score",
	"step_count", "active_kcal", "total_kcal", "distance_m", "floors_climbed",
	"resting_hr", "hrv_avg_ms", "content_hash", "metadata", "captured_at",
}

var dailyFigures = []string{"readiness_score", "activity_score", "sleep_score", "recovery_score", "strain_score",
	"step_count", "active_kcal", "total_kcal", "distance_m", "floors_climbed", "resting_hr", "hrv_avg_ms"}

func dailyRow(sc scope, s model.DailySummary) []any {
	p := sc.providerOr(s.Provider)
	return []any{
		sc.tenant, sc.user, p, sourceSlugOrProvider("", p), dateOnly(s.SummaryDate),
		s.ReadinessScore, s.ActivityScore, s.SleepScore, s.RecoveryScore, s.StrainScore,
		s.StepCount, s.ActiveKcal, s.TotalKcal, s.DistanceM, s.FloorsClimbed,
		s.RestingHR, s.HRVAvgMs, s.ContentHash, normalizeMetadata(s.Metadata), sc.capturedAt(),
	}
}

var summaryLatestWins = latestWins(tDaily, hyperConflict("summary_date"), dailyFigures)

var summaryAdvance = advanceCapture(tDaily, hyperConflict("summary_date"))

// writeDailySummaries batch-inserts provider daily summaries. A duplicate is
// skipped, except a Replaceable summary, whose stored figures the new copy
// replaces.
func (w *Writer) writeDailySummaries(ctx context.Context, tx TxLike, sc scope, summaries []model.DailySummary) (written, skipped int, err error) {
	insertOnly, upsert, superseded := replaceableSplit(summaries,
		func(s model.DailySummary) bool { return s.Replaceable },
		func(s model.DailySummary) copyDedupeKey {
			return copyDedupeKey{hash: s.ContentHash, stamp: dateOnly(s.SummaryDate).UnixNano()}
		})
	head := insertHead(tDaily, dailyColumns)
	written, skipped, err = w.insertBatches(ctx, tx, tDaily, head, doNothing(hyperConflict("summary_date")),
		len(dailyColumns), rowsOf(insertOnly, sc, dailyRow))
	if err != nil {
		return written, skipped, err
	}
	n, k, err := w.upsertBatches(ctx, tx, sc, tDaily, head, summaryLatestWins, summaryAdvance,
		len(dailyColumns), rowsOf(upsert, sc, dailyRow))
	if err != nil {
		return written, skipped, err
	}
	return written + n, skipped + k + superseded, nil
}
