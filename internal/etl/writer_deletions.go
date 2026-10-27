// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
//  3. the quantity and category samples of the record are deleted: those
//     whose external_id is the id or "<id>_<suffix>", whenever they start,
//     whether or not a sleep or workout of that id was stored (a record that
//     only gave samples, a WHOOP recovery whose sleep stays). The match is a
//     range on the (tenant_id, user_id, provider, external_id COLLATE "C")
//     index (migration 0014), see deletionExternalMatch;
//  4. the nutrition logs of that id are deleted;
//  5. when the deletion names ClearSummaryFields, those figures are set to
//     NULL in the provider's daily summaries of the days the removed quantity
//     samples name (model.MetadataSummaryDate), and a summary left without
//     any figure is deleted. The names are resolved against dailyFigures, an
//     allow-list: no name from a connector reaches the SQL text;
//  6. the local days of the removed additive samples are returned, so the
//     derived daily row of each is computed again without them (a deleted
//     step sample no longer counts in its day).
//
// Every record of the batch is then checked against the tombstones before it
// is written (dropTombstoned), so a delete wins whichever arrives first.

// tDeleted is the tombstone table, a key of Counts: Written counts new
// tombstones, Skipped the deletions already applied.
const tDeleted = "deleted_records"

var (
	deletionTombstone = `INSERT INTO ohs_data.deleted_records (tenant_id, user_id, provider, external_id)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`
	deletionSleep = `DELETE FROM ohs_data.sleep_sessions
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND external_id = $4
		RETURNING id`
	deletionPhases = `DELETE FROM ohs_data.sleep_phases
		WHERE tenant_id = $1 AND user_id = $2 AND session_id = ANY($3)`
	deletionWorkout = `DELETE FROM ohs_data.workouts
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND external_id = $4
		RETURNING id`
	deletionRoutes = `DELETE FROM ohs_data.route_points
		WHERE tenant_id = $1 AND user_id = $2 AND workout_id = ANY($3)`
	deletionTracks = `DELETE FROM ohs_data.workout_tracks
		WHERE tenant_id = $1 AND user_id = $2 AND workout_id = ANY($3)`
	// deletionSamples and deletionCategory: $4 is the id, $5 the id followed
	// by "_", $6 the id followed by "`" (see deletionExternalMatch).
	// It returns, for each removed sample, the summary day it names (if any),
	// its type, its start and its time zone.
	deletionSamples = `DELETE FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3` + deletionExternalMatch("$4", "$5", "$6") + `
		RETURNING metadata->>'` + model.MetadataSummaryDate + `', quantity_type, start_date, metadata->>'` + MetadataTimezone + `'`
	deletionCategory = `DELETE FROM ohs_data.category_samples
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3` + deletionExternalMatch("$4", "$5", "$6")
	deletionNutrition = `DELETE FROM ohs_data.nutrition_logs
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND external_id = $4`
	tombstonesAny = `SELECT EXISTS (SELECT 1 FROM ohs_data.deleted_records WHERE tenant_id = $1 AND user_id = $2)`
	tombstonesOf  = `SELECT provider, external_id FROM ohs_data.deleted_records
		WHERE tenant_id = $1 AND user_id = $2 AND provider = ANY($3) AND external_id = ANY($4)`
)

// deletionExternalMatch is the predicate of the rows stored under a record
// id or "<id>_<suffix>": id, prefix and upper are the placeholders of the id,
// the id followed by "_", and the id followed by "`", the byte after "_".
//
// external_id is compared in the "C" collation, the one of the index of
// migration 0014: in byte order every "<id>_..." lies in [id, id + "`"), so
// the index answers a range scan bounded by the id, and starts_with (never
// LIKE, so an id holding "%" or "_" is not a pattern) drops the few other ids
// of that range ("<id>-2", "<id>0"). No time bound is needed, and none is
// known for a deletion that names only an id: on a hypertable the scan is one
// index probe per chunk.
func deletionExternalMatch(id, prefix, upper string) string {
	return ` AND external_id IS NOT NULL
		  AND external_id COLLATE "C" >= ` + id + ` AND external_id COLLATE "C" < ` + upper + `
		  AND (external_id = ` + id + ` OR starts_with(external_id, ` + prefix + `))`
}

// applyDeletions applies every deletion of the batch. Rows removed are
// counted in the log line only; tombstones go to c. It returns the local days
// of the additive samples it removed, whose derived daily rows are stale.
func applyDeletions(ctx context.Context, tx TxLike, sc scope, dels []model.Deletion, c Counts) (map[dayKey]*time.Location, error) {
	removed := 0
	days := map[dayKey]*time.Location{}
	var order []recordKey
	clear := make(map[recordKey][]string, len(dels))
	for _, d := range dels {
		if d.ExternalID == "" {
			continue
		}
		k := recordKey{provider: sc.providerOr(d.Provider), id: d.ExternalID}
		if _, seen := clear[k]; seen {
			c.add(tDeleted, 0, 1)
		} else {
			order = append(order, k)
			clear[k] = []string{}
		}
		clear[k] = append(clear[k], d.ClearSummaryFields...)
	}
	for _, k := range order {
		n, fresh, err := applyDeletion(ctx, tx, sc, k, clear[k], days)
		if err != nil {
			return nil, err
		}
		removed += n
		if fresh {
			c.add(tDeleted, 1, 0)
		} else {
			c.add(tDeleted, 0, 1)
		}
	}
	if removed > 0 {
		slog.InfoContext(ctx, "etl: provider deletions applied", "deletions", len(order), "rows_removed", removed)
	}
	return days, nil
}

// applyDeletion deletes one provider record and its derived rows, clears the
// summary figures named by clear on the days of its samples, adds the local
// days of its removed additive samples to days, and reports the rows removed
// or cleared and whether the tombstone is new.
func applyDeletion(ctx context.Context, tx TxLike, sc scope, k recordKey, clear []string, days map[dayKey]*time.Location) (int, bool, error) {
	tag, err := tx.Exec(ctx, deletionTombstone, sc.tenant, sc.user, k.provider, k.id)
	if err != nil {
		return 0, false, fmt.Errorf("insert tombstone: %w", err)
	}
	fresh := tag.RowsAffected() == 1
	removed := 0

	sleeps, err := deleteParents(ctx, tx, deletionSleep, sc, k)
	if err != nil {
		return 0, false, fmt.Errorf("delete sleep session: %w", err)
	}
	if len(sleeps) > 0 {
		tag, err := tx.Exec(ctx, deletionPhases, sc.tenant, sc.user, sleeps)
		if err != nil {
			return 0, false, fmt.Errorf("delete sleep phases: %w", err)
		}
		removed += len(sleeps) + int(tag.RowsAffected())
	}

	workouts, err := deleteParents(ctx, tx, deletionWorkout, sc, k)
	if err != nil {
		return 0, false, fmt.Errorf("delete workout: %w", err)
	}
	if len(workouts) > 0 {
		for _, q := range []string{deletionRoutes, deletionTracks} {
			tag, err := tx.Exec(ctx, q, sc.tenant, sc.user, workouts)
			if err != nil {
				return 0, false, fmt.Errorf("delete workout children: %w", err)
			}
			removed += int(tag.RowsAffected())
		}
		removed += len(workouts)
	}

	rows, err := tx.Query(ctx, deletionSamples, sc.tenant, sc.user, k.provider, k.id, k.id+"_", k.id+"`")
	if err != nil {
		return 0, false, fmt.Errorf("delete derived samples: %w", err)
	}
	gone, err := pgx.CollectRows(rows, pgx.RowToStructByPos[removedSample])
	if err != nil {
		return 0, false, fmt.Errorf("delete derived samples: %w", err)
	}
	removed += len(gone)
	summaryDays := make([]*string, len(gone))
	for i, s := range gone {
		summaryDays[i] = s.SummaryDate
		if isAdditive(s.QuantityType) {
			md := map[string]any{}
			if s.Timezone != nil {
				md[MetadataTimezone] = *s.Timezone
			}
			loc := sampleLocation(md)
			dk := dayKey{provider: k.provider, day: dateOnly(s.StartDate.In(loc))}
			if _, ok := days[dk]; !ok {
				days[dk] = loc
			}
		}
	}
	tag, err = tx.Exec(ctx, deletionCategory, sc.tenant, sc.user, k.provider, k.id, k.id+"_", k.id+"`")
	if err != nil {
		return 0, false, fmt.Errorf("delete derived samples: %w", err)
	}
	removed += int(tag.RowsAffected())

	if days := parseSummaryDays(summaryDays); len(days) > 0 && len(clear) > 0 {
		n, err := clearSummaryFields(ctx, tx, sc, k.provider, clear, days)
		if err != nil {
			return 0, false, err
		}
		removed += n
	}

	tag, err = tx.Exec(ctx, deletionNutrition, sc.tenant, sc.user, k.provider, k.id)
	if err != nil {
		return 0, false, fmt.Errorf("delete record: %w", err)
	}
	removed += int(tag.RowsAffected())
	return removed, fresh, nil
}

// removedSample is one quantity sample a deletion removed, as deletionSamples
// returns it.
type removedSample struct {
	SummaryDate  *string
	QuantityType string
	StartDate    time.Time
	Timezone     *string
}

// deleteParents runs a parent DELETE ... RETURNING id.
func deleteParents(ctx context.Context, tx TxLike, q string, sc scope, k recordKey) ([]int64, error) {
	rows, err := tx.Query(ctx, q, sc.tenant, sc.user, k.provider, k.id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// parseSummaryDays returns the distinct days of MetadataSummaryDate values
// ("YYYY-MM-DD"); NULLs and values that do not parse are left out.
func parseSummaryDays(raw []*string) []time.Time {
	seen := map[time.Time]bool{}
	var out []time.Time
	for _, r := range raw {
		if r == nil {
			continue
		}
		d, err := time.Parse(time.DateOnly, *r)
		if err != nil || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// summaryClearFigures resolves the figure names of a deletion against
// dailyFigures, in that order and once each. A name not in the list is
// dropped: the result holds only constants of this package, so it can be
// written into SQL text.
func summaryClearFigures(names []string) (figures []string, unknown int) {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	for _, f := range dailyFigures {
		if want[f] {
			figures = append(figures, f)
			delete(want, f)
		}
	}
	return figures, len(want)
}

// summaryScope selects the provider's own summaries ($3) of the days $4 for
// the job's tenant and user; the derived activity rows are the ETL's, not a
// record's.
const summaryScope = `tenant_id = $1 AND user_id = $2 AND provider = $3 AND summary_date = ANY($4::date[])
		  AND metadata->>'derived' IS DISTINCT FROM 'true'`

// summaryClearStatement is the UPDATE that sets figures to NULL. figures must
// come from summaryClearFigures.
func summaryClearStatement(figures []string) string {
	set := make([]string, len(figures))
	some := make([]string, len(figures))
	for i, f := range figures {
		set[i] = f + " = NULL"
		some[i] = f + " IS NOT NULL"
	}
	return `UPDATE ohs_data.daily_summaries SET ` + strings.Join(set, ", ") + `
		WHERE ` + summaryScope + ` AND (` + strings.Join(some, " OR ") + `)`
}

// summaryDropEmpty deletes the summaries of those days left without any
// figure.
var summaryDropEmpty = func() string {
	none := make([]string, len(dailyFigures))
	for i, f := range dailyFigures {
		none[i] = f + " IS NULL"
	}
	return `DELETE FROM ohs_data.daily_summaries
		WHERE ` + summaryScope + ` AND ` + strings.Join(none, " AND ")
}()

// clearSummaryFields clears the named figures of the provider's summaries of
// days and drops the summaries left empty. It returns the rows changed.
func clearSummaryFields(ctx context.Context, tx TxLike, sc scope, provider string, names []string, days []time.Time) (int, error) {
	figures, unknown := summaryClearFigures(names)
	if unknown > 0 {
		slog.WarnContext(ctx, "etl: deletion names unknown summary figures, ignored", "provider", provider, "unknown", unknown)
	}
	if len(figures) == 0 {
		return 0, nil
	}
	tag, err := tx.Exec(ctx, summaryClearStatement(figures), sc.tenant, sc.user, provider, days)
	if err != nil {
		return 0, fmt.Errorf("clear summary figures: %w", err)
	}
	n := int(tag.RowsAffected())
	if n == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, summaryDropEmpty, sc.tenant, sc.user, provider, days); err != nil {
		return 0, fmt.Errorf("drop empty summaries: %w", err)
	}
	return n, nil
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
	for _, d := range acc.DailySummaries {
		for _, id := range sampleRecordIDs(d.RecordID) {
			add(d.Provider, id)
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
			return !deadSleep[p.ParentContentHash] && (!allGone || p.ParentContentHash != "")
		})
	}

	var deadWorkout map[string]bool
	acc.Workouts, deadWorkout = filterParents(acc.Workouts, tWorkouts, c,
		func(w model.Workout) bool { return isDead(w.Provider, w.ExternalID) },
		func(w model.Workout) string { return w.ContentHash })
	if len(deadWorkout) > 0 {
		allGone := len(acc.Workouts) == 0
		acc.RoutePoints = filterKeep(acc.RoutePoints, tRoutes, c, func(p model.RoutePoint) bool {
			return !deadWorkout[p.ParentContentHash] && (!allGone || p.ParentContentHash != "")
		})
	}

	acc.CategorySamples = filterKeep(acc.CategorySamples, tCategory, c, func(s model.CategorySample) bool {
		return !isDead(s.Provider, s.ExternalID)
	})
	acc.NutritionLogs = filterKeep(acc.NutritionLogs, tNutrition, c, func(n model.NutritionLog) bool {
		return !isDead(n.Provider, n.ExternalID)
	})
	acc.QuantitySamples = filterKeep(acc.QuantitySamples, tQuantity, c, func(s model.QuantitySample) bool {
		return !anyDead(s.Provider, s.ExternalID, isDead)
	})
	acc.DailySummaries = filterKeep(acc.DailySummaries, tDaily, c, func(d model.DailySummary) bool {
		return !anyDead(d.Provider, d.RecordID, isDead)
	})
}

// anyDead reports whether the record id, or a record it derives from, is dead.
func anyDead(provider, id string, isDead func(provider, id string) bool) bool {
	for _, r := range sampleRecordIDs(id) {
		if isDead(provider, r) {
			return true
		}
	}
	return false
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
