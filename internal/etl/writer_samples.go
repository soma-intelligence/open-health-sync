// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

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

// quantityShape is the write form of quantity_samples (vaultrows.go).
var quantityShape = newShape(tQuantity, quantitySampleColumns, quantityPromoted, deriveQuantity)

// quantityFigures are the columns a Replaceable quantity sample replaces.
var quantityFigures = []string{"value", "unit"}

// quantityFollowMetadata are the promoted columns read from metadata, which a
// clear update replaces with it.
var quantityFollowMetadata = []string{"tz", "derived", "synthesized", "summary_date"}

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
	columns := quantityShape.columns(sc)
	if len(insertOnly) > 0 {
		seen := make(copyDedupe, len(insertOnly))
		var kw kindWindows
		temp, _, err := stage(ctx, tx, tQuantity, columns, lazyRows(insertOnly, quantityFinished(sc),
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
		n, err := insertMissing(ctx, tx, sc, tQuantity, "start_date", "quantity_type", temp, columns, kw)
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

// quantityFinished builds the written row of a sample for sc: its base row
// with the promoted columns, sealed with keys.
func quantityFinished(sc scope) func(model.QuantitySample) ([]any, error) {
	return func(s model.QuantitySample) ([]any, error) {
		return quantityShape.finish(sc, quantitySampleRow(sc, s))
	}
}

// upsertQuantityClear applies latest wins in SQL to clear rows: the stored
// copies are updated from the staged rows, then the missing ones inserted,
// each through its quantity type's own window. The copies that changed
// nothing still confirm the row at their capture time (advanceStored).
func upsertQuantityClear(ctx context.Context, tx TxLike, sc scope, upsert []model.QuantitySample) (changed, added int, err error) {
	columns := quantityShape.columns(sc)
	var kw kindWindows
	for _, s := range upsert {
		kw.extend(s.QuantityType, s.StartDate)
	}
	temp, _, err := stage(ctx, tx, tQuantity, columns, lazyRows(upsert, quantityFinished(sc), nil))
	if err != nil {
		return 0, 0, err
	}
	if changed, err = updateStored(ctx, tx, sc, tQuantity, "start_date", "quantity_type", temp, quantityFigures, quantityFollowMetadata, kw.all); err != nil {
		return 0, 0, err
	}
	if added, err = insertMissing(ctx, tx, sc, tQuantity, "start_date", "quantity_type", temp, columns, kw); err != nil {
		return changed, 0, err
	}
	// Only then is there anything to move.
	if changed+added < len(upsert) && !sc.captured.IsZero() {
		if err := advanceStored(ctx, tx, sc, tQuantity, "start_date", "quantity_type", temp, kw.all); err != nil {
			return changed, added, err
		}
	}
	return changed, added, nil
}

// upsertQuantitySealed applies latest wins in Go (mergeUpserts), then
// updates the stored copies that changed, inserts the missing ones and moves
// the capture time of the copies a newer object confirmed.
func upsertQuantitySealed(ctx context.Context, tx TxLike, sc scope, upsert []model.QuantitySample) (changed, added int, err error) {
	columns := quantityShape.columns(sc)
	bases := rowsOf(upsert, sc, quantitySampleRow)
	var kw kindWindows
	for _, s := range upsert {
		kw.extend(s.QuantityType, s.StartDate)
	}
	m, err := mergeUpserts(ctx, tx, sc, quantityShape, "start_date", "timestamptz", "quantity_type", bases, quantityFigures)
	if err != nil {
		return 0, 0, err
	}
	if len(m.stored) > 0 {
		// Each stored copy is updated by its id with its own merge.
		for i := range m.stored {
			m.stored[i] = append(m.stored[i], m.ids[i])
		}
		temp, _, err := stageAs(ctx, tx, tQuantity, "_tmp_quantity_merged", append(columns, "id"), pgx.CopyFromRows(m.stored))
		if err != nil {
			return 0, 0, err
		}
		if changed, err = updateStoredSealed(ctx, tx, sc, quantityShape, "start_date", "quantity_type", temp, quantityFigures, kw.all); err != nil {
			return 0, 0, err
		}
	}
	if len(m.fresh) > 0 {
		temp, _, err := stage(ctx, tx, tQuantity, columns, pgx.CopyFromRows(m.fresh))
		if err != nil {
			return changed, 0, err
		}
		if added, err = insertMissing(ctx, tx, sc, tQuantity, "start_date", "quantity_type", temp, columns, kw); err != nil {
			return changed, 0, err
		}
	}
	if err := advanceCaptured(ctx, tx, sc, tQuantity, "start_date", "timestamptz", "quantity_type", m.advance); err != nil {
		return changed, added, err
	}
	return changed, added, nil
}

// upsertMerge is what mergeUpserts made of an upsert group: the rows that
// replace a stored copy (stored, with that copy's id in ids), the rows with
// no stored copy (fresh), both finished (sealed); the stored copies the new
// copy left as they were but confirmed at a later capture time (advance); and
// how many copies wrote nothing (unchanged).
type upsertMerge struct {
	stored    [][]any
	ids       []any
	fresh     [][]any
	advance   advanceSet
	unchanged int
}

// mergeUpserts prepares the sealed rows of an upsert group (base rows of s, a
// content_hash and timeCol column each, and a kindCol column when the table
// has a type column its index leads with): a row with stored copies is merged
// with each of them (shape.mergeStored, as the SQL merge coalesced each copy
// with itself) and goes to stored with that copy's id, or is left out and
// counted in unchanged when it changes no figure of it; a row without one
// goes to fresh.
//
// The capture guard of the SQL merge (notStale, newerCapture) is applied here
// on the clear captured_at of the stored copy: a copy from an object captured
// before the stored one is left out (unchanged), and a copy that changes no
// figure but was captured after it goes to advance.
func mergeUpserts(ctx context.Context, tx TxLike, sc scope, s *shape, timeCol, timeType, kindCol string, bases [][]any, figures []string) (upsertMerge, error) {
	var m upsertMerge
	hp, tp := s.index["content_hash"], s.index[timeCol]
	hashes := make([]string, len(bases))
	times := make([]time.Time, len(bases))
	var kinds []string
	if kindCol != "" {
		kinds = make([]string, len(bases))
	}
	var wd window
	for i, b := range bases {
		hashes[i], _ = b[hp].(string)
		times[i], _ = b[tp].(time.Time)
		if kindCol != "" {
			kinds[i], _ = b[s.index[kindCol]].(string)
		}
		wd.extend(times[i])
	}
	have, err := storedRows(ctx, tx, sc, s.t.Name, timeCol, timeType, kindCol, hashes, times, kinds, wd.lo, wd.hi)
	if err != nil {
		return m, err
	}
	_, ordered := s.index["captured_at"]
	captured := sc.captured.Truncate(time.Microsecond)
	ordered = ordered && !sc.captured.IsZero()
	for i, b := range bases {
		copies := have[keyOf(hashes[i], times[i])]
		if len(copies) == 0 {
			row, err := s.finish(sc, b)
			if err != nil {
				return m, err
			}
			m.fresh = append(m.fresh, row)
			continue
		}
		for _, st := range copies {
			at, known := st["captured_at"].(time.Time)
			if ordered && known && captured.Before(at) {
				// Stale: the stored copy came from a newer object.
				m.unchanged++
				continue
			}
			merged, changed := s.mergeStored(b, st, figures)
			if !changed {
				m.unchanged++
				if ordered && (!known || captured.After(at)) {
					id, _ := st.int("id")
					kind := ""
					if kindCol != "" {
						kind = kinds[i]
					}
					m.advance.add(id, times[i], kind)
				}
				continue
			}
			row, err := s.finish(sc, merged)
			if err != nil {
				return m, err
			}
			m.stored, m.ids = append(m.stored, row), append(m.ids, st["id"])
		}
	}
	return m, nil
}

// quantitySupersede deletes the stored copies of SupersedeMoved samples under
// another start: same provider, external id and quantity type ($3 to $5, one
// element per sample), another start_date ($6). The dedup unique includes
// start_date, so the upsert alone cannot reach them. The external id is
// matched on the index of migration 0014. It returns the type and start of each
// copy removed, whose hour is recomputed (hourly.go).
const quantitySupersede = `DELETE FROM ohs_data.quantity_samples q
	USING unnest($3::text[], $4::text[], $5::text[], $6::timestamptz[]) AS n(provider, external_id, quantity_type, start_date)
	WHERE q.tenant_id = $1 AND q.user_id = $2
	  AND q.provider = n.provider AND q.external_id IS NOT NULL AND q.external_id COLLATE "C" = n.external_id
	  AND q.quantity_type = n.quantity_type AND q.start_date <> n.start_date
	RETURNING q.quantity_type, q.start_date`

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
	rows, err := tx.Query(ctx, quantitySupersede, sc.tenant, sc.user, providers, ids, types, starts)
	if err != nil {
		return fmt.Errorf("supersede moved samples: %w", err)
	}
	var (
		qt string
		at time.Time
	)
	_, err = pgx.ForEachRow(rows, []any{&qt, &at}, func() error {
		sc.hours.add(qt, at)
		return nil
	})
	if err != nil {
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
// threshold (dropping in-batch duplicates by key, and building and sealing each
// row as the COPY reads it), batched INSERTs below. key gives each record's
// stored hash and time once; row builds the record's row with that hash.
func copyOrInsert[T any](ctx context.Context, w *Writer, tx TxLike, sc scope, s *shape, conflict string,
	recs []T, row func(scope, T, string) []any, key func(T) (string, time.Time)) (written, skipped int, err error) {
	table, columns := s.t.Name, s.columns(sc)
	if len(recs) > w.cfg.CopyThreshold {
		seen := make(copyDedupe, len(recs))
		dupes, i := 0, 0
		rows := pgx.CopyFromFunc(func() ([]any, error) {
			for i < len(recs) {
				r := recs[i]
				i++
				h, at := key(r)
				if seen.staged(h, at) {
					dupes++
					continue
				}
				return s.finish(sc, row(sc, r, h))
			}
			return nil, nil
		})
		written, skipped, err = w.writeHighFrequencyCopy(ctx, tx, table, columns, rows, conflict)
		if err != nil {
			return 0, 0, err
		}
		return written, skipped + dupes, nil
	}
	rows := make([][]any, len(recs))
	for i, r := range recs {
		h, _ := key(r)
		if rows[i], err = s.finish(sc, row(sc, r, h)); err != nil {
			return 0, 0, err
		}
	}
	return w.insertBatches(ctx, tx, table, insertHead(table, columns), doNothing(conflict), len(columns), rows)
}

// --- category_samples ---

var categorySampleColumns = []string{
	"tenant_id", "user_id", "provider", "source_slug", "source_name", "external_id", "category_type",
	"value_int", "value_label", "start_date", "end_date", "content_hash", "metadata",
}

var categoryShape = newShape(tCategory, categorySampleColumns, tzPromoted, deriveTZ)

func categorySampleRow(sc scope, s model.CategorySample, hash string) []any {
	p := sc.providerOr(s.Provider)
	return []any{
		sc.tenant, sc.user, p, sourceSlugOrProvider("", p), s.SourceName, s.ExternalID, s.CategoryType,
		s.ValueInt, s.ValueLabel, s.StartDate, nullTime(s.EndDate), hash, normalizeMetadata(s.Metadata),
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
	upsert, stale, err := dropStaleMovedLogs(ctx, tx, sc, upsert)
	if err != nil {
		return written, skipped, err
	}
	superseded += stale
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

var heartbeatShape = newShape(tHeartbeat, heartbeatColumns, nil, nil)

func heartbeatRow(sc scope, s model.HeartbeatSample, hash string) []any {
	return []any{sc.tenant, sc.user, sc.provider, s.SeriesID, s.Timestamp, s.RRMS, hash}
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

func observationRow(sc scope, o model.Observation, hash string) []any {
	p := sc.providerOr(o.Provider)
	return []any{
		sc.tenant, sc.user, p, sourceSlugOrProvider("", p), o.SourceName, o.DeviceID, o.TS, nullTime(o.EndTS),
		o.CanonicalCode, o.ValueNum, o.ValueCode, o.ValueText, o.UnitUCUM, o.ValueCanonical,
		o.IsPrimarySource, o.TypeConcept, o.SourceValue, o.S3Ref, hash, normalizeMetadata(o.Metadata),
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
// with keys merged in Go by mergeUpserts, then sealedWins). Either way a copy
// from an older object than the stored row changes nothing, and a newer copy
// that changes nothing still moves the stored capture time forward. The rows
// are base rows of s.
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
	if sc.uv == nil {
		if rows, err = s.finishAll(sc, upsert); err != nil {
			return written, skipped, err
		}
		n, k, err := w.upsertBatches(ctx, tx, sc, s.t.Name, head, clearWins, advanceCapture(s.t.Name, conflict), len(columns), rows)
		return written + n, skipped + k, err
	}
	m, err := mergeUpserts(ctx, tx, sc, s, timeCol, timeType, "", upsert, figures)
	if err != nil {
		return written, skipped, err
	}
	n, k, err := w.insertBatches(ctx, tx, s.t.Name, head, sealedWins, len(columns), append(m.stored, m.fresh...))
	written, skipped = written+n, skipped+k+m.unchanged
	if err != nil {
		return written, skipped, err
	}
	return written, skipped, advanceCaptured(ctx, tx, sc, s.t.Name, timeCol, timeType, "", m.advance)
}
