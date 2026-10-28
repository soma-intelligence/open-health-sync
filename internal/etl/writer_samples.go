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
	"start_date", "end_date", "content_hash", "metadata",
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
		s.ContentHash, normalizeMetadata(s.Metadata),
	}
}

// quantityShape is the write form of quantity_samples (vaultrows.go).
var quantityShape = newShape(tQuantity, quantitySampleColumns, quantityPromoted, deriveQuantity)

// quantityFigures are the columns a Replaceable quantity sample replaces.
var quantityFigures = []string{"value", "unit"}

// quantityFollowMetadata are the promoted columns read from metadata, which a
// clear update replaces with it.
var quantityFollowMetadata = []string{"tz", "derived", "synthesized", "summary_date"}

// writeQuantitySamples writes quantity samples. quantity_samples has no dedup
// unique, because it is compressed after a while: duplicates are found in the
// stored rows of the batch's time window (see insertMissing). Inside the batch,
// the first copy of an insert-only row wins and the last copy of a Replaceable
// row wins.
func (w *Writer) writeQuantitySamples(ctx context.Context, tx TxLike, sc scope, samples []model.QuantitySample) (written, skipped int, err error) {
	insertOnly, upsert, superseded := replaceableSplit(samples,
		func(s model.QuantitySample) bool { return s.Replaceable },
		func(s model.QuantitySample) copyDedupeKey {
			return copyDedupeKey{hash: s.ContentHash, stamp: s.StartDate.UnixNano()}
		})
	if err := lockUserTable(ctx, tx, tQuantity, sc); err != nil {
		return 0, 0, err
	}
	columns := quantityShape.columns(sc)
	if len(insertOnly) > 0 {
		seen := make(copyDedupe, len(insertOnly))
		rows := make([][]any, 0, len(insertOnly))
		var wd window
		for _, s := range insertOnly {
			if seen.staged(s.ContentHash, s.StartDate) {
				continue
			}
			wd.extend(s.StartDate)
			row, err := quantityShape.finish(sc, quantitySampleRow(sc, s))
			if err != nil {
				return 0, 0, err
			}
			rows = append(rows, row)
		}
		temp, _, err := stage(ctx, tx, tQuantity, columns, rows)
		if err != nil {
			return 0, 0, err
		}
		n, err := insertMissing(ctx, tx, sc, tQuantity, "start_date", temp, columns, wd)
		if err != nil {
			return 0, 0, err
		}
		written, skipped = written+n, skipped+len(insertOnly)-n
	}
	upsert, moved := supersedeMovedSplit(sc, upsert)
	superseded += moved
	if err := supersedeMovedSamples(ctx, tx, sc, upsert); err != nil {
		return written, skipped, err
	}
	if len(upsert) > 0 {
		var changed, added int
		var err error
		if sc.uv == nil {
			changed, added, err = upsertQuantityClear(ctx, tx, sc, upsert)
		} else {
			changed, added, err = upsertQuantitySealed(ctx, tx, sc, upsert)
		}
		if err != nil {
			return written, skipped, err
		}
		written, skipped = written+changed+added, skipped+len(upsert)-changed-added
	}
	return written, skipped + superseded, nil
}

// upsertQuantityClear applies latest wins in SQL to clear rows: the stored
// copies are updated from the staged rows, then the missing ones inserted.
func upsertQuantityClear(ctx context.Context, tx TxLike, sc scope, upsert []model.QuantitySample) (changed, added int, err error) {
	columns := quantityShape.columns(sc)
	rows := make([][]any, 0, len(upsert))
	var wd window
	for _, s := range upsert {
		wd.extend(s.StartDate)
		row, err := quantityShape.finish(sc, quantitySampleRow(sc, s))
		if err != nil {
			return 0, 0, err
		}
		rows = append(rows, row)
	}
	temp, _, err := stage(ctx, tx, tQuantity, columns, rows)
	if err != nil {
		return 0, 0, err
	}
	if changed, err = updateStored(ctx, tx, sc, tQuantity, "start_date", temp, quantityFigures, quantityFollowMetadata, wd); err != nil {
		return 0, 0, err
	}
	if added, err = insertMissing(ctx, tx, sc, tQuantity, "start_date", temp, columns, wd); err != nil {
		return changed, 0, err
	}
	return changed, added, nil
}

// upsertQuantitySealed applies latest wins in Go (mergeUpserts), then
// updates the stored copies that changed and inserts the missing ones.
func upsertQuantitySealed(ctx context.Context, tx TxLike, sc scope, upsert []model.QuantitySample) (changed, added int, err error) {
	columns := quantityShape.columns(sc)
	bases := rowsOf(upsert, sc, quantitySampleRow)
	stored, fresh, _, err := mergeUpserts(ctx, tx, sc, quantityShape, "start_date", "timestamptz", bases, quantityFigures)
	if err != nil {
		return 0, 0, err
	}
	var wd window
	for _, s := range upsert {
		wd.extend(s.StartDate)
	}
	if len(stored) > 0 {
		temp, _, err := stage(ctx, tx, tQuantity, columns, stored)
		if err != nil {
			return 0, 0, err
		}
		if changed, err = updateStoredSealed(ctx, tx, sc, quantityShape, "start_date", temp, quantityFigures, wd); err != nil {
			return 0, 0, err
		}
	}
	if len(fresh) > 0 {
		temp, _, err := stage(ctx, tx, tQuantity, columns, fresh)
		if err != nil {
			return changed, 0, err
		}
		if added, err = insertMissing(ctx, tx, sc, tQuantity, "start_date", temp, columns, wd); err != nil {
			return changed, 0, err
		}
	}
	return changed, added, nil
}

// mergeUpserts prepares the sealed rows of an upsert group (base rows of s, a
// content_hash and timeCol column each): a row with a stored copy is merged
// with it (shape.mergeStored) and goes to stored, or is left out and counted
// in unchanged when it changes no figure; a row without one goes to fresh.
// Both come back finished (sealed).
func mergeUpserts(ctx context.Context, tx TxLike, sc scope, s *shape, timeCol, timeType string, bases [][]any, figures []string) (stored, fresh [][]any, unchanged int, err error) {
	hp, tp := s.index["content_hash"], s.index[timeCol]
	hashes := make([]string, len(bases))
	times := make([]time.Time, len(bases))
	var wd window
	for i, b := range bases {
		hashes[i], _ = b[hp].(string)
		times[i], _ = b[tp].(time.Time)
		wd.extend(times[i])
	}
	have, err := storedRows(ctx, tx, sc, s.t.Name, timeCol, timeType, hashes, times, wd.lo, wd.hi)
	if err != nil {
		return nil, nil, 0, err
	}
	for i, b := range bases {
		st, ok := have[keyOf(hashes[i], times[i])]
		if ok {
			merged, changed := s.mergeStored(b, st, figures)
			if !changed {
				unchanged++
				continue
			}
			b = merged
		}
		row, err := s.finish(sc, b)
		if err != nil {
			return nil, nil, 0, err
		}
		if ok {
			stored = append(stored, row)
		} else {
			fresh = append(fresh, row)
		}
	}
	return stored, fresh, unchanged, nil
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
// threshold (after dropping in-batch duplicates by key), batched INSERTs below.
func copyOrInsert[T any](ctx context.Context, w *Writer, tx TxLike, sc scope, s *shape, conflict string,
	recs []T, row func(scope, T) []any, key func(T) (string, time.Time)) (written, skipped int, err error) {
	table, columns := s.t.Name, s.columns(sc)
	if len(recs) > w.cfg.CopyThreshold {
		seen := make(copyDedupe, len(recs))
		rows := make([][]any, 0, len(recs))
		dupes := 0
		for _, r := range recs {
			h, at := key(r)
			if seen.staged(h, at) {
				dupes++
				continue
			}
			f, err := s.finish(sc, row(sc, r))
			if err != nil {
				return 0, 0, err
			}
			rows = append(rows, f)
		}
		written, skipped, err = w.writeHighFrequencyCopy(ctx, tx, table, columns, rows, conflict)
		if err != nil {
			return 0, 0, err
		}
		return written, skipped + dupes, nil
	}
	rows, err := s.finishAll(sc, rowsOf(recs, sc, row))
	if err != nil {
		return 0, 0, err
	}
	return w.insertBatches(ctx, tx, table, insertHead(table, columns), doNothing(conflict), len(columns), rows)
}

// --- category_samples ---

var categorySampleColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "source_name", "external_id", "category_type",
	"value_int", "value_label", "start_date", "end_date", "content_hash", "metadata",
}

var categoryShape = newShape(tCategory, categorySampleColumns, tzPromoted, deriveTZ)

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
	return copyOrInsert(ctx, w, tx, sc, categoryShape, hyperConflict("start_date"), samples, categorySampleRow,
		func(s model.CategorySample) (string, time.Time) { return s.ContentHash, s.StartDate })
}

// --- nutrition_logs ---

var nutritionColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "external_id", "start_date", "end_date",
	"meal_type", "energy_kcal", "protein_g", "carbs_g", "fat_g",
	"fiber_g", "sugar_g", "sodium_mg", "nutrients_json", "content_hash", "metadata",
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
		s.FiberG, s.SugarG, s.SodiumMg, nj, s.ContentHash, normalizeMetadata(s.Metadata),
	}
}

var nutritionShape = newShape(tNutrition, nutritionColumns, nil, nil)

// nutritionFigures are the columns a Replaceable log's new copy replaces.
var nutritionFigures = []string{"energy_kcal", "protein_g", "carbs_g", "fat_g", "fiber_g", "sugar_g", "sodium_mg", "nutrients_json"}

var (
	nutritionLatestWins       = latestWins(tNutrition, hyperConflict("start_date"), nutritionFigures, nil)
	nutritionLatestWinsSealed = nutritionShape.latestWinsSealed(hyperConflict("start_date"), nutritionFigures)
)

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
	if err := deleteMovedLogs(ctx, tx, sc, upsert); err != nil {
		return written, skipped, err
	}
	n, k, err := w.writeGroups(ctx, tx, sc, nutritionShape, "start_date", "timestamptz", hyperConflict("start_date"),
		nutritionFigures, nutritionLatestWins, nutritionLatestWinsSealed,
		rowsOf(insertOnly, sc, nutritionRow), rowsOf(upsert, sc, nutritionRow))
	return written + n, skipped + k + superseded, err
}

// nutritionSupersede deletes the stored copies of SupersedeMoved logs under
// another start: same provider and external id ($3, $4, one element per
// log), another start_date ($5). The dedup unique includes start_date, so
// the upsert alone cannot reach them.
const nutritionSupersede = `DELETE FROM ohs_data.nutrition_logs n
	USING unnest($3::text[], $4::text[], $5::timestamptz[]) AS m(provider, external_id, start_date)
	WHERE n.tenant_id = $1 AND n.user_id = $2
	  AND n.provider = m.provider AND n.external_id = m.external_id AND n.start_date <> m.start_date`

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

var heartbeatShape = newShape(tHeartbeat, heartbeatColumns, nil, nil)

func heartbeatRow(sc scope, s model.HeartbeatSample) []any {
	return []any{sc.tenant, sc.user, sc.provider, s.SeriesID, s.Timestamp, s.RRMS, sc.hash(model.HeartbeatSampleContentHash(s))}
}

// writeHeartbeatSamples writes heartbeat samples, using COPY above threshold.
func (w *Writer) writeHeartbeatSamples(ctx context.Context, tx TxLike, sc scope, samples []model.HeartbeatSample) (written, skipped int, err error) {
	return copyOrInsert(ctx, w, tx, sc, heartbeatShape, hyperConflict(`"timestamp"`), samples, heartbeatRow,
		func(s model.HeartbeatSample) (string, time.Time) {
			return sc.hash(model.HeartbeatSampleContentHash(s)), s.Timestamp
		})
}

// --- observations ---

var observationColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "source_name", "device_id", "ts", "end_ts",
	"canonical_code", "value_num", "value_code", "value_text", "unit_ucum", "value_canonical",
	"is_primary_source", "type_concept", "source_value", "raw_ref", "content_hash", "metadata",
}

var observationShape = newShape(tObservation, observationColumns, nil, nil)

// observationHash is the stored hash of o: the mapper's (keyed already by
// scope.keyed), or one computed here and keyed.
func observationHash(sc scope, o model.Observation) string {
	if o.ContentHash != "" {
		return o.ContentHash
	}
	return sc.hash(model.ObservationContentHash(o))
}

func observationRow(sc scope, o model.Observation) []any {
	p := sc.providerOr(o.Provider)
	return []any{
		sc.tenant, sc.user, p, sourceSlugOrProvider("", p), o.SourceName, o.DeviceID, o.TS, nullTime(o.EndTS),
		o.CanonicalCode, o.ValueNum, o.ValueCode, o.ValueText, o.UnitUCUM, o.ValueCanonical,
		o.IsPrimarySource, o.TypeConcept, o.SourceValue, o.S3Ref, observationHash(sc, o), normalizeMetadata(o.Metadata),
	}
}

// writeObservations writes generic coded observations, the long tail of any
// metric without a typed table, switching to COPY above the threshold.
func (w *Writer) writeObservations(ctx context.Context, tx TxLike, sc scope, obs []model.Observation) (written, skipped int, err error) {
	return copyOrInsert(ctx, w, tx, sc, observationShape, hyperConflict("ts"), obs, observationRow,
		func(o model.Observation) (string, time.Time) { return observationHash(sc, o), o.TS })
}

// --- daily_summaries ---

var dailyColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "summary_date",
	"readiness_score", "activity_score", "sleep_score", "recovery_score", "strain_score",
	"step_count", "active_kcal", "total_kcal", "distance_m", "floors_climbed",
	"resting_hr", "hrv_avg_ms", "content_hash", "metadata",
}

var dailyFigures = []string{"readiness_score", "activity_score", "sleep_score", "recovery_score", "strain_score",
	"step_count", "active_kcal", "total_kcal", "distance_m", "floors_climbed", "resting_hr", "hrv_avg_ms"}

func dailyRow(sc scope, s model.DailySummary) []any {
	p := sc.providerOr(s.Provider)
	return []any{
		sc.tenant, sc.user, p, sourceSlugOrProvider("", p), dateOnly(s.SummaryDate),
		s.ReadinessScore, s.ActivityScore, s.SleepScore, s.RecoveryScore, s.StrainScore,
		s.StepCount, s.ActiveKcal, s.TotalKcal, s.DistanceM, s.FloorsClimbed,
		s.RestingHR, s.HRVAvgMs, s.ContentHash, normalizeMetadata(s.Metadata),
	}
}

var dailyShape = newShape(tDaily, dailyColumns, derivedPromoted, deriveDerived)

var (
	summaryLatestWins       = latestWins(tDaily, hyperConflict("summary_date"), dailyFigures, derivedPromoted)
	summaryLatestWinsSealed = dailyShape.latestWinsSealed(hyperConflict("summary_date"), dailyFigures)
)

// writeDailySummaries batch-inserts provider daily summaries. A duplicate is
// skipped, except a Replaceable summary, whose stored figures the new copy
// replaces.
func (w *Writer) writeDailySummaries(ctx context.Context, tx TxLike, sc scope, summaries []model.DailySummary) (written, skipped int, err error) {
	insertOnly, upsert, superseded := replaceableSplit(summaries,
		func(s model.DailySummary) bool { return s.Replaceable },
		func(s model.DailySummary) copyDedupeKey {
			return copyDedupeKey{hash: s.ContentHash, stamp: dateOnly(s.SummaryDate).UnixNano()}
		})
	n, k, err := w.writeGroups(ctx, tx, sc, dailyShape, "summary_date", "date", hyperConflict("summary_date"),
		dailyFigures, summaryLatestWins, summaryLatestWinsSealed,
		rowsOf(insertOnly, sc, dailyRow), rowsOf(upsert, sc, dailyRow))
	return n, k + superseded, err
}

// writeGroups batch-inserts the insert-only rows of a table (first write
// wins) and its Replaceable rows (latest wins: clearWins in SQL without keys;
// with keys merged in Go by mergeUpserts, then sealedWins). The rows are base
// rows of s.
func (w *Writer) writeGroups(ctx context.Context, tx TxLike, sc scope, s *shape, timeCol, timeType, conflict string,
	figures []string, clearWins, sealedWins string, insertOnly, upsert [][]any) (written, skipped int, err error) {
	columns := s.columns(sc)
	head := insertHead(s.t.Name, columns)
	rows, err := s.finishAll(sc, insertOnly)
	if err != nil {
		return 0, 0, err
	}
	written, skipped, err = w.insertBatches(ctx, tx, s.t.Name, head, doNothing(conflict), len(columns), rows)
	if err != nil || len(upsert) == 0 {
		return written, skipped, err
	}
	onConflict := clearWins
	if sc.uv == nil {
		rows, err = s.finishAll(sc, upsert)
	} else {
		var stored, fresh [][]any
		var unchanged int
		stored, fresh, unchanged, err = mergeUpserts(ctx, tx, sc, s, timeCol, timeType, upsert, figures)
		rows, onConflict, skipped = append(stored, fresh...), sealedWins, skipped+unchanged
	}
	if err != nil {
		return written, skipped, err
	}
	n, k, err := w.insertBatches(ctx, tx, s.t.Name, head, onConflict, len(columns), rows)
	return written + n, skipped + k, err
}
