// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Provider deletions (model.Deletion).
//
// A deletion runs inside the job's transaction, scoped by the job's tenant and
// user and by the record's provider and external id:
//
//  1. a tombstone is inserted into deleted_records (idempotent);
//  2. the sleep sessions of that id are deleted with their phases, the
//     workouts with their route points and track summaries;
//  3. the quantity samples derived from the record are deleted: those whose
//     external_id is the id or "<id>_<suffix>", starting within a day of a
//     deleted sleep or workout. The window keeps the delete on the
//     (tenant_id, user_id, start_date) index of the largest table;
//  4. the category samples and nutrition logs of that id are deleted.
//
// Every record of the batch is then checked against the tombstones before it
// is written (dropTombstoned), so a delete wins whichever arrives first.

// tDeleted is the tombstone table, a key of Counts: Written counts new
// tombstones, Skipped the deletions already applied.
const tDeleted = "deleted_records"

// deletionSampleSlack widens the window of step 3 around a deleted parent:
// the samples of a sleep or workout start at its start, but a copy whose start
// later moved may have left samples under the old start.
const deletionSampleSlack = 24 * time.Hour

var (
	deletionTombstone = `INSERT INTO ohs_data.deleted_records (tenant_id, user_id, provider, external_id)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`
	deletionSleep = `DELETE FROM ohs_data.sleep_sessions
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND external_id = $4
		RETURNING id, bedtime_start, COALESCE(bedtime_end, bedtime_start)`
	deletionPhases = `DELETE FROM ohs_data.sleep_phases
		WHERE tenant_id = $1 AND user_id = $2 AND session_id = ANY($3)`
	deletionWorkout = `DELETE FROM ohs_data.workouts
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND external_id = $4
		RETURNING id, start_date, COALESCE(end_date, start_date)`
	deletionRoutes = `DELETE FROM ohs_data.route_points
		WHERE tenant_id = $1 AND user_id = $2 AND workout_id = ANY($3)`
	deletionTracks = `DELETE FROM ohs_data.workout_tracks
		WHERE tenant_id = $1 AND user_id = $2 AND workout_id = ANY($3)`
	// deletionSamples: $4 is the id, $5 the id followed by "_", $6 and $7 the
	// window on start_date.
	deletionSamples = `DELETE FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
		  AND start_date >= $6 AND start_date <= $7
		  AND (external_id = $4 OR starts_with(external_id, $5))`
	deletionCategory = `DELETE FROM ohs_data.category_samples
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND external_id = $4`
	deletionNutrition = `DELETE FROM ohs_data.nutrition_logs
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND external_id = $4`
	tombstonesAny = `SELECT EXISTS (SELECT 1 FROM ohs_data.deleted_records WHERE tenant_id = $1 AND user_id = $2)`
	tombstonesOf  = `SELECT provider, external_id FROM ohs_data.deleted_records
		WHERE tenant_id = $1 AND user_id = $2 AND provider = ANY($3) AND external_id = ANY($4)`
)

// deletedSpan is the stored span of one deleted parent row.
type deletedSpan struct {
	id         int64
	start, end time.Time
}

// applyDeletions applies every deletion of the batch. Rows removed are
// counted in the log line only; tombstones go to c.
func applyDeletions(ctx context.Context, tx TxLike, sc scope, dels []model.Deletion, c Counts) error {
	removed := 0
	seen := make(map[recordKey]bool, len(dels))
	for _, d := range dels {
		if d.ExternalID == "" {
			continue
		}
		k := recordKey{provider: sc.providerOr(d.Provider), id: d.ExternalID}
		if seen[k] {
			c.add(tDeleted, 0, 1)
			continue
		}
		seen[k] = true
		n, fresh, err := applyDeletion(ctx, tx, sc, k)
		if err != nil {
			return err
		}
		removed += n
		if fresh {
			c.add(tDeleted, 1, 0)
		} else {
			c.add(tDeleted, 0, 1)
		}
	}
	if removed > 0 {
		slog.InfoContext(ctx, "etl: provider deletions applied", "deletions", len(seen), "rows_removed", removed)
	}
	return nil
}

// applyDeletion deletes one provider record and its derived rows, and reports
// the rows removed and whether the tombstone is new.
func applyDeletion(ctx context.Context, tx TxLike, sc scope, k recordKey) (int, bool, error) {
	tag, err := tx.Exec(ctx, deletionTombstone, sc.tenant, sc.user, k.provider, k.id)
	if err != nil {
		return 0, false, fmt.Errorf("insert tombstone: %w", err)
	}
	fresh := tag.RowsAffected() == 1
	removed := 0

	sleeps, err := deleteSpans(ctx, tx, deletionSleep, sc, k)
	if err != nil {
		return 0, false, fmt.Errorf("delete sleep session: %w", err)
	}
	if len(sleeps) > 0 {
		tag, err := tx.Exec(ctx, deletionPhases, sc.tenant, sc.user, spanIDs(sleeps))
		if err != nil {
			return 0, false, fmt.Errorf("delete sleep phases: %w", err)
		}
		removed += len(sleeps) + int(tag.RowsAffected())
	}

	workouts, err := deleteSpans(ctx, tx, deletionWorkout, sc, k)
	if err != nil {
		return 0, false, fmt.Errorf("delete workout: %w", err)
	}
	if len(workouts) > 0 {
		ids := spanIDs(workouts)
		for _, q := range []string{deletionRoutes, deletionTracks} {
			tag, err := tx.Exec(ctx, q, sc.tenant, sc.user, ids)
			if err != nil {
				return 0, false, fmt.Errorf("delete workout children: %w", err)
			}
			removed += int(tag.RowsAffected())
		}
		removed += len(workouts)
	}

	if spans := append(append([]deletedSpan{}, sleeps...), workouts...); len(spans) > 0 {
		from, to := spans[0].start, spans[0].end
		for _, s := range spans[1:] {
			if s.start.Before(from) {
				from = s.start
			}
			if s.end.After(to) {
				to = s.end
			}
		}
		tag, err := tx.Exec(ctx, deletionSamples, sc.tenant, sc.user, k.provider, k.id, k.id+"_",
			from.Add(-deletionSampleSlack), to.Add(deletionSampleSlack))
		if err != nil {
			return 0, false, fmt.Errorf("delete derived samples: %w", err)
		}
		removed += int(tag.RowsAffected())
	}

	for _, q := range []string{deletionCategory, deletionNutrition} {
		tag, err := tx.Exec(ctx, q, sc.tenant, sc.user, k.provider, k.id)
		if err != nil {
			return 0, false, fmt.Errorf("delete record: %w", err)
		}
		removed += int(tag.RowsAffected())
	}
	return removed, fresh, nil
}

// deleteSpans runs a parent DELETE ... RETURNING id, start, end.
func deleteSpans(ctx context.Context, tx TxLike, q string, sc scope, k recordKey) ([]deletedSpan, error) {
	rows, err := tx.Query(ctx, q, sc.tenant, sc.user, k.provider, k.id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (deletedSpan, error) {
		var s deletedSpan
		err := r.Scan(&s.id, &s.start, &s.end)
		return s, err
	})
}

func spanIDs(spans []deletedSpan) []int64 {
	ids := make([]int64, len(spans))
	for i, s := range spans {
		ids[i] = s.id
	}
	return ids
}

// recordKey is one provider record: its provider and external id.
type recordKey struct {
	provider, id string
}

// sampleRecordIDs are the record ids a quantity sample may belong to: its
// external id, and each prefix of it that ends before an underscore
// ("<record id>_<quantity type>").
func sampleRecordIDs(externalID string) []string {
	if externalID == "" {
		return nil
	}
	out := []string{externalID}
	for i := 0; i < len(externalID); i++ {
		if externalID[i] == '_' && i > 0 {
			out = append(out, externalID[:i])
		}
	}
	return out
}

// dropTombstoned removes from acc every record whose provider record is
// tombstoned for the job's user, and the children of a dropped parent, and
// counts them skipped. One indexed EXISTS answers the common case of a user
// with no tombstone at all.
func dropTombstoned(ctx context.Context, tx TxLike, sc scope, acc *model.MapResult, c Counts) error {
	candidates := map[recordKey]bool{}
	add := func(provider, id string) {
		if id != "" {
			candidates[recordKey{provider: sc.providerOr(provider), id: id}] = true
		}
	}
	for _, s := range acc.SleepSessions {
		add(s.Provider, s.ExternalID)
	}
	for _, w := range acc.Workouts {
		add(w.Provider, w.ExternalID)
	}
	for _, s := range acc.CategorySamples {
		add(s.Provider, s.ExternalID)
	}
	for _, n := range acc.NutritionLogs {
		add(n.Provider, n.ExternalID)
	}
	for _, s := range acc.QuantitySamples {
		for _, id := range sampleRecordIDs(s.ExternalID) {
			add(s.Provider, id)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	var anyTombstone bool
	if err := tx.QueryRow(ctx, tombstonesAny, sc.tenant, sc.user).Scan(&anyTombstone); err != nil {
		return fmt.Errorf("look up tombstones: %w", err)
	}
	if !anyTombstone {
		return nil
	}
	providers, ids := map[string]bool{}, map[string]bool{}
	for k := range candidates {
		providers[k.provider], ids[k.id] = true, true
	}
	rows, err := tx.Query(ctx, tombstonesOf, sc.tenant, sc.user, keys(providers), keys(ids))
	if err != nil {
		return fmt.Errorf("look up tombstones: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (recordKey, error) {
		var k recordKey
		err := r.Scan(&k.provider, &k.id)
		return k, err
	})
	if err != nil {
		return fmt.Errorf("look up tombstones: %w", err)
	}
	dead := make(map[recordKey]bool, len(found))
	for _, k := range found {
		if candidates[k] {
			dead[k] = true
		}
	}
	if len(dead) == 0 {
		return nil
	}
	isDead := func(provider, id string) bool {
		return id != "" && dead[recordKey{provider: sc.providerOr(provider), id: id}]
	}
	dropTombstonedRecords(acc, isDead, c)
	return nil
}

// dropTombstonedRecords filters acc with isDead. A phase or route point goes
// with its dropped parent; when every parent of a non-empty family is dropped,
// the family's unstamped children go too, or the writer would attach them to
// another record (or synthesize a sleep from them).
func dropTombstonedRecords(acc *model.MapResult, isDead func(provider, id string) bool, c Counts) {
	var deadSleep map[string]bool
	acc.SleepSessions, deadSleep = filterParents(acc.SleepSessions, tSleep, c,
		func(s model.SleepSession) bool { return isDead(s.Provider, s.ExternalID) },
		func(s model.SleepSession) string { return s.ContentHash })
	if len(deadSleep) > 0 {
		allGone := len(acc.SleepSessions) == 0
		acc.SleepPhases = filterKeep(acc.SleepPhases, tPhases, c, func(p model.SleepPhase) bool {
			return !deadSleep[p.ParentContentHash] && !(allGone && p.ParentContentHash == "")
		})
	}

	var deadWorkout map[string]bool
	acc.Workouts, deadWorkout = filterParents(acc.Workouts, tWorkouts, c,
		func(w model.Workout) bool { return isDead(w.Provider, w.ExternalID) },
		func(w model.Workout) string { return w.ContentHash })
	if len(deadWorkout) > 0 {
		allGone := len(acc.Workouts) == 0
		acc.RoutePoints = filterKeep(acc.RoutePoints, tRoutes, c, func(p model.RoutePoint) bool {
			return !deadWorkout[p.ParentContentHash] && !(allGone && p.ParentContentHash == "")
		})
	}

	acc.CategorySamples = filterKeep(acc.CategorySamples, tCategory, c, func(s model.CategorySample) bool {
		return !isDead(s.Provider, s.ExternalID)
	})
	acc.NutritionLogs = filterKeep(acc.NutritionLogs, tNutrition, c, func(n model.NutritionLog) bool {
		return !isDead(n.Provider, n.ExternalID)
	})
	acc.QuantitySamples = filterKeep(acc.QuantitySamples, tQuantity, c, func(s model.QuantitySample) bool {
		for _, id := range sampleRecordIDs(s.ExternalID) {
			if isDead(s.Provider, id) {
				return false
			}
		}
		return true
	})
}

// filterParents drops the parents dead reports, counting them skipped, and
// returns the content hashes of the dropped ones.
func filterParents[T any](in []T, table string, c Counts, dead func(T) bool, hash func(T) string) ([]T, map[string]bool) {
	gone := map[string]bool{}
	out := in[:0:0]
	for _, r := range in {
		if dead(r) {
			gone[hash(r)] = true
			c.add(table, 0, 1)
			continue
		}
		out = append(out, r)
	}
	return out, gone
}

// filterKeep keeps the records keep accepts and counts the others skipped.
func filterKeep[T any](in []T, table string, c Counts, keep func(T) bool) []T {
	out := in[:0:0]
	for _, r := range in {
		if keep(r) {
			out = append(out, r)
		} else {
			c.add(table, 0, 1)
		}
	}
	return out
}

// keys returns the keys of a set, for an ANY($n) argument.
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
