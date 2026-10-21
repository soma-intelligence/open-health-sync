// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/fusion"
)

// CodedSample is a quantity sample with the standard codes the quantity type
// catalog gives its type: nil when the catalog has none (or does not know
// the type).
type CodedSample struct {
	Sample
	LOINCCode *string
	// UCUMUnit is the UCUM spelling of StorageUnit.
	UCUMUnit    *string
	StorageUnit *string
}

func (s *CodedSample) dest() []any {
	return append(s.Sample.dest(), &s.LOINCCode, &s.UCUMUnit, &s.StorageUnit)
}

// Kind is the table an observation record comes from. Its number orders
// records of the same time in a merged list.
type Kind int

// Record kinds.
const (
	KindQuantity  Kind = 1
	KindSleep     Kind = 2
	KindWorkout   Kind = 3
	KindDaily     Kind = 4
	KindECG       Kind = 5
	KindNutrition Kind = 6
)

// The two halves of a blood pressure reading.
const (
	Systolic  = "blood_pressure_systolic"
	Diastolic = "blood_pressure_diastolic"
)

// ObservationRecord is one record of a merged observation list; the field
// of its Kind is set.
type ObservationRecord struct {
	Kind   Kind
	Sample *CodedSample
	// Partner is the diastolic half when Sample is a systolic sample paired
	// with it (see Observations).
	Partner *CodedSample
	Sleep   *SleepSession
	Workout *Workout
	Daily   *DailySummary
	// ECG is an ECG recording with its voltage samples.
	ECG       *ECGRecordingDetail
	Nutrition *NutritionLog
}

// ObservationFilter says which records a merged list holds.
type ObservationFilter struct {
	Quantities bool
	// Types keeps the quantity samples of these types; nil keeps every type.
	// A list with the diastolic type also keeps the paired systolic samples,
	// which carry their diastolic half.
	Types []string
	// PanelsOnly keeps only the systolic samples paired with a diastolic one.
	PanelsOnly bool
	Sleep      bool
	Workouts   bool
	Daily      bool
	ECG        bool
	Nutrition  bool
}

// AllObservations is the filter that keeps every record.
func AllObservations() ObservationFilter {
	return ObservationFilter{Quantities: true, Sleep: true, Workouts: true, Daily: true, ECG: true, Nutrition: true}
}

// And is the filter that keeps what both f and g keep.
func (f ObservationFilter) And(g ObservationFilter) ObservationFilter {
	out := ObservationFilter{
		Quantities: f.Quantities && g.Quantities,
		Sleep:      f.Sleep && g.Sleep,
		Workouts:   f.Workouts && g.Workouts,
		Daily:      f.Daily && g.Daily,
		ECG:        f.ECG && g.ECG,
		Nutrition:  f.Nutrition && g.Nutrition,
	}
	if !out.Quantities {
		return out
	}
	// A panel is kept by a type list that names either half.
	keepsPanels := func(h ObservationFilter) bool {
		return h.PanelsOnly || h.Types == nil || slices.Contains(h.Types, Systolic) || slices.Contains(h.Types, Diastolic)
	}
	switch {
	case f.PanelsOnly || g.PanelsOnly:
		out.PanelsOnly = true
		out.Quantities = keepsPanels(f) && keepsPanels(g)
	case f.Types == nil:
		out.Types = g.Types
	case g.Types == nil:
		out.Types = f.Types
	default:
		out.Types = []string{}
		for _, t := range f.Types {
			if slices.Contains(g.Types, t) {
				out.Types = append(out.Types, t)
			}
		}
	}
	return out
}

// Empty reports whether the filter keeps nothing.
func (f ObservationFilter) Empty() bool {
	q := f.Quantities && (f.Types == nil || len(f.Types) > 0)
	return !q && !f.Sleep && !f.Workouts && !f.Daily && !f.ECG && !f.Nutrition
}

// Blood pressure pairing. A systolic and a diastolic sample pair when they
// share user, provider, start, end, source and device (an unknown source or
// device matches only an unknown one). When duplicates share that key, the
// lowest id of each type pairs and the others stay single, so a sample is in
// at most one pair and the pairing does not depend on the page.
//
// The pairing subqueries are correlated, so the planner cannot drop any chunk
// of quantity_samples from them: for a user with a long history, planning
// them costs far more than running them. A bound, when the caller has one,
// repeats a's time bounds on the subquery's alias as constants; since the two
// samples start at the same time it changes no result, and it lets the
// planner keep only the chunks in range.
type bound func(alias string) string

func (bd bound) on(alias string) string {
	if bd == nil {
		return ""
	}
	return bd(alias)
}

func bpMatch(a, b string, bd bound) string {
	return b + ".tenant_id = " + a + ".tenant_id AND " + b + ".user_id = " + a + ".user_id AND " +
		b + ".provider = " + a + ".provider AND " + b + ".start_date = " + a + ".start_date AND " +
		b + ".end_date IS NOT DISTINCT FROM " + a + ".end_date AND " +
		b + ".source_slug IS NOT DISTINCT FROM " + a + ".source_slug AND " +
		b + ".device_id IS NOT DISTINCT FROM " + a + ".device_id" + bd.on(b)
}

// firstOfKey: no sample of a's type and key has a lower id than a.
func firstOfKey(a string, bd bound) string {
	return "NOT EXISTS (SELECT 1 FROM ohs_data.quantity_samples x WHERE x.quantity_type = " + a +
		".quantity_type AND " + bpMatch(a, "x", bd) + " AND x.id < " + a + ".id)"
}

// hasKey: a sample of type typ shares a's key.
func hasKey(a, typ string, bd bound) string {
	return "EXISTS (SELECT 1 FROM ohs_data.quantity_samples y WHERE y.quantity_type = '" + typ + "' AND " +
		bpMatch(a, "y", bd) + ")"
}

// paired: a is the systolic sample of a pair.
func paired(a string, bd bound) string {
	return "(" + a + ".quantity_type = '" + Systolic + "' AND " + firstOfKey(a, bd) + " AND " + hasKey(a, Diastolic, bd) + ")"
}

// absorbed: a is the diastolic sample of a pair.
func absorbed(a string, bd bound) string {
	return "(" + a + ".quantity_type = '" + Diastolic + "' AND " + firstOfKey(a, bd) + " AND " + hasKey(a, Systolic, bd) + ")"
}

// qualified prefixes every column of a column list with alias.
func qualified(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// codedSelect reads CodedSamples (alias q), then the id of the diastolic
// partner of a paired systolic sample, then whether the row is the diastolic
// half of a pair.
func codedSelect(bd bound) string {
	return qualified("q", sampleCols) + `,
	(SELECT c.loinc_code FROM ohs_data.quantity_codes c WHERE c.canonical_code = q.quantity_type),
	(SELECT c.ucum_unit FROM ohs_data.quantity_codes c WHERE c.canonical_code = q.quantity_type),
	(SELECT c.storage_unit FROM ohs_data.quantity_codes c WHERE c.canonical_code = q.quantity_type),
	(SELECT d.id FROM ohs_data.quantity_samples d WHERE q.quantity_type = '` + Systolic + `'
		AND d.quantity_type = '` + Diastolic + `' AND ` + bpMatch("q", "d", bd) + ` AND ` + firstOfKey("q", bd) + `
		ORDER BY d.id LIMIT 1),
	` + absorbed("q", bd)
}

// The merged list is ordered by (time, kind, id); its cursor is the last
// record's three.
func encodeObsCursor(t time.Time, k Kind, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" +
		strconv.Itoa(int(k)) + "|" + strconv.FormatInt(id, 10)))
}

func decodeObsCursor(c string) (time.Time, Kind, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, 0, 0, ErrInvalidCursor
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return time.Time{}, 0, 0, ErrInvalidCursor
	}
	t, err1 := time.Parse(time.RFC3339Nano, parts[0])
	k, err2 := strconv.Atoi(parts[1])
	id, err3 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || k < int(KindQuantity) || k > int(KindNutrition) {
		return time.Time{}, 0, 0, ErrInvalidCursor
	}
	return t, Kind(k), id, nil
}

// CheckObservationQuery reports a mistake in q for Observations (a
// QueryError or ErrInvalidCursor) without running it.
func CheckObservationQuery(q Query) error {
	c := q.Cursor
	q.Cursor = ""
	if _, err := q.normalize(false); err != nil {
		return err
	}
	if c != "" {
		if _, _, _, err := decodeObsCursor(c); err != nil {
			return err
		}
	}
	return nil
}

type obsKey struct {
	kind Kind
	t    time.Time
	id   int64
}

// cutPage keeps the keys of one page of limit records out of keys (read
// with one more than limit, to know whether another page follows) and
// returns the cursor of the next page, "" on the last one. A page ends
// early at its MaxECGPerPage-th ECG recording when records follow it.
func cutPage(keys []obsKey, limit int) ([]obsKey, string) {
	end := min(len(keys), limit)
	ecgs := 0
	for i, k := range keys[:end] {
		if k.kind != KindECG {
			continue
		}
		if ecgs++; ecgs == MaxECGPerPage && i+1 < len(keys) {
			end = i + 1
			break
		}
	}
	if end == len(keys) {
		return keys, ""
	}
	last := keys[end-1]
	return keys[:end], encodeObsCursor(last.t, last.kind, last.id)
}

// Observations lists, in one page, the records f keeps: quantity samples,
// sleep sessions, workouts, daily summaries, ECG recordings and nutrition
// logs, ordered by time (a sample's start, a session's bedtime, a workout's
// start, a day's date at midnight UTC, a recording's or a log's start), then
// kind, then id. q.Type is
// ignored (it belongs in f). A systolic sample paired with a diastolic one
// comes with it as Partner, and the diastolic one is not listed on its own,
// so a pair is never split across pages. A page holds at most MaxECGPerPage
// ECG recordings (each comes with its samples): one that reaches it ends
// there, with a cursor.
//
// A page of the merged view with no end is read through windows (see
// windowed).
func Observations(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query, f ObservationFilter) (Page[ObservationRecord], error) {
	if !q.merged() || !q.End.IsZero() || f.Empty() {
		return observations(ctx, db, tenantID, userID, q, f)
	}
	since := q.Start
	if q.Cursor != "" {
		t, _, _, err := decodeObsCursor(q.Cursor)
		if err != nil {
			return Page[ObservationRecord]{}, err
		}
		if t.After(since) {
			since = t
		}
	}
	mine := "tenant_id = $1 AND user_id = $2"
	sets := []timed{
		{"ohs_data.quantity_samples WHERE " + mine, "start_date"},
		{"ohs_data.sleep_sessions WHERE " + mine, "bedtime_start"},
		{"ohs_data.workouts WHERE " + mine, "start_date"},
		{"ohs_data.daily_summaries WHERE " + mine, "(summary_date::timestamp AT TIME ZONE 'UTC')"},
		{"ohs_data.ecg_recordings WHERE " + mine, "start_date"},
		{"ohs_data.nutrition_logs WHERE " + mine, "start_date"},
	}
	first := func(from time.Time) (time.Time, error) {
		return firstFrom(ctx, db, sets, []any{tenantID, userID}, from)
	}
	return windowed(since, first, func(end time.Time) (Page[ObservationRecord], error) {
		wq := q
		wq.End = end
		return observations(ctx, db, tenantID, userID, wq, f)
	})
}

func observations(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query, f ObservationFilter) (Page[ObservationRecord], error) {
	cursor := q.Cursor
	q.Cursor = ""
	n, err := q.normalize(false)
	if err != nil {
		return Page[ObservationRecord]{}, err
	}
	var after *obsKey
	if cursor != "" {
		t, k, id, err := decodeObsCursor(cursor)
		if err != nil {
			return Page[ObservationRecord]{}, err
		}
		after = &obsKey{k, t, id}
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[ObservationRecord]{}, err
	}
	out := Page[ObservationRecord]{Records: []ObservationRecord{}}
	if f.Empty() {
		return out, nil
	}
	var pol *fusion.Policy
	if n.merged() {
		p, err := policy(ctx, db, tenantID, userID, n.Query)
		if err != nil {
			return Page[ObservationRecord]{}, err
		}
		pol = &p
	}

	args := []any{tenantID, userID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	var mp *mergeParams
	if pol != nil {
		mp = bindMerge(*pol, append(fusion.KnownMetrics(), f.Types...), arg)
		since := n.Start
		if after != nil && after.t.After(since) {
			since = after.t
		}
		mp.window(since, n.End)
	}
	// mergedOut adds the merged view's condition for a branch.
	mergedOut := func(where []string, cond func() string) []string {
		if pol == nil {
			return where
		}
		return append(where, cond())
	}
	limit := arg(n.Limit + 1)
	var branches []string
	branch := func(k Kind, from, alias, timeExpr string, where []string) {
		where = append([]string{alias + ".tenant_id = $1", alias + ".user_id = $2"}, where...)
		if !n.Start.IsZero() {
			where = append(where, timeExpr+" >= "+arg(n.Start)+"::timestamptz")
		}
		if !n.End.IsZero() {
			where = append(where, timeExpr+" < "+arg(n.End)+"::timestamptz")
		}
		if after != nil {
			// The scalar bound lets TimescaleDB skip chunks before the cursor.
			t := arg(after.t) + "::timestamptz"
			where = append(where, timeExpr+" >= "+t, fmt.Sprintf("(%s, %d, %s.id) > (%s, %s::int, %s::bigint)",
				timeExpr, k, alias, t, arg(int(after.kind)), arg(after.id)))
		}
		branches = append(branches, fmt.Sprintf("(SELECT %d AS kind, %s AS ts, %s.id AS id FROM %s %s WHERE %s ORDER BY 2, 3 LIMIT %s)",
			k, timeExpr, alias, from, alias, strings.Join(where, " AND "), limit))
	}
	if f.Quantities && (f.Types == nil || len(f.Types) > 0) {
		// The pairing subqueries get the page's time bounds too (see bound).
		var bounds []string
		if !n.Start.IsZero() {
			bounds = append(bounds, ".start_date >= "+arg(n.Start)+"::timestamptz")
		}
		if !n.End.IsZero() {
			bounds = append(bounds, ".start_date < "+arg(n.End)+"::timestamptz")
		}
		if after != nil {
			bounds = append(bounds, ".start_date >= "+arg(after.t)+"::timestamptz")
		}
		var bd bound
		if len(bounds) > 0 {
			bd = func(a string) string { return " AND " + a + strings.Join(bounds, " AND "+a) }
		}
		where := []string{"NOT " + absorbed("q", bd)}
		switch {
		case f.PanelsOnly:
			where = append(where, paired("q", bd))
		case f.Types != nil:
			cond := "q.quantity_type = ANY(" + arg(f.Types) + "::text[])"
			if slices.Contains(f.Types, Diastolic) {
				cond = "(" + cond + " OR " + paired("q", bd) + ")"
			}
			where = append(where, cond)
		}
		where = mergedOut(where, func() string { return "NOT " + quantityFamily.covered(mp, "q", "") })
		branch(KindQuantity, "ohs_data.quantity_samples", "q", "q.start_date", where)
	}
	if f.Sleep {
		branch(KindSleep, "ohs_data.sleep_sessions", "s", "s.bedtime_start",
			mergedOut(nil, func() string { return "NOT " + sleepFamily.covered(mp, "s", fusion.MetricSleep) }))
	}
	if f.Workouts {
		branch(KindWorkout, "ohs_data.workouts", "w", "w.start_date",
			mergedOut(nil, func() string { return "NOT " + workoutFamily.covered(mp, "w", fusion.MetricWorkouts) }))
	}
	if f.Daily {
		// A day's row stays when it holds a figure no better ranked row of the
		// day holds; fetchRecords blanks the figures it does not own.
		branch(KindDaily, "ohs_data.daily_summaries", "d", "(d.summary_date::timestamp AT TIME ZONE 'UTC')",
			mergedOut(nil, func() string { return dailyOwnsAny(mp, "d") }))
	}
	if f.ECG {
		// No merged view: two recordings of one moment are two recordings.
		branch(KindECG, "ohs_data.ecg_recordings", "e", "e.start_date", nil)
	}
	if f.Nutrition {
		// No merged view either: two apps logging one meal are two entries.
		branch(KindNutrition, "ohs_data.nutrition_logs", "n", "n.start_date", nil)
	}
	sql := "SELECT kind, ts, id FROM (" + strings.Join(branches, " UNION ALL ") + ") k ORDER BY ts, kind, id LIMIT " + limit
	if pol != nil {
		args = append([]any{customPlan}, args...)
	}
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return Page[ObservationRecord]{}, fmt.Errorf("read: observations: %w", err)
	}
	var keys []obsKey
	for rows.Next() {
		var k obsKey
		if err := rows.Scan(&k.kind, &k.t, &k.id); err != nil {
			rows.Close()
			return Page[ObservationRecord]{}, fmt.Errorf("read: observations: scan: %w", err)
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Page[ObservationRecord]{}, fmt.Errorf("read: observations: %w", err)
	}
	keys, out.NextCursor = cutPage(keys, n.Limit)
	recs, err := fetchRecords(ctx, db, tenantID, userID, keys, pol)
	if err != nil {
		return Page[ObservationRecord]{}, err
	}
	for _, k := range keys {
		// A record deleted between the two queries is left out.
		if r, ok := recs[obsKey{kind: k.kind, id: k.id}]; ok {
			out.Records = append(out.Records, r)
		}
	}
	return out, nil
}

// ObservationByID reads one record of the user. For KindQuantity, panel
// asks for a systolic sample paired with a diastolic one, and !panel for a
// sample that is in no pair. ErrNotFound when it is not the tenant's, does
// not exist or does not have that shape. In the merged view a daily summary
// carries only the figures it owns, as in Observations; any other record
// reads as stored.
func ObservationByID(ctx context.Context, db Querier, tenantID, userID uuid.UUID, k Kind, panel bool, id int64, view View) (ObservationRecord, error) {
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return ObservationRecord{}, err
	}
	var pol *fusion.Policy
	if k == KindDaily && view != ViewAll {
		p, err := policy(ctx, db, tenantID, userID, Query{})
		if err != nil {
			return ObservationRecord{}, err
		}
		pol = &p
	}
	recs, err := fetchRecords(ctx, db, tenantID, userID, []obsKey{{kind: k, id: id}}, pol)
	if err != nil {
		return ObservationRecord{}, err
	}
	r, ok := recs[obsKey{kind: k, id: id}]
	if !ok {
		return ObservationRecord{}, ErrNotFound
	}
	if k == KindQuantity && panel != (r.Partner != nil) {
		return ObservationRecord{}, ErrNotFound
	}
	return r, nil
}

// timeSpan bounds the time column of the rows a by-id read looks for, so
// TimescaleDB reads only the chunks that can hold them instead of every
// chunk of the user (each one decompressed when it is compressed). The zero
// value bounds nothing.
type timeSpan struct{ from, to time.Time }

// add widens s to cover t. A zero t (a key read by id alone) leaves s as is.
func (s timeSpan) add(t time.Time) timeSpan {
	if t.IsZero() {
		return s
	}
	if s.from.IsZero() || t.Before(s.from) {
		s.from = t
	}
	if s.to.IsZero() || t.After(s.to) {
		s.to = t
	}
	return s
}

// where is the condition on col for s, with its arguments numbered after n
// arguments, or "" when s bounds nothing.
func (s timeSpan) where(col string, n int, cast string) (string, []any) {
	if s.from.IsZero() {
		return "", nil
	}
	if cast == "date" {
		// A day's key is its midnight UTC: pass the day itself, so the
		// session time zone cannot move it.
		return fmt.Sprintf(" AND %s BETWEEN $%d::date AND $%d::date", col, n+1, n+2),
			[]any{s.from.UTC().Format(time.DateOnly), s.to.UTC().Format(time.DateOnly)}
	}
	return fmt.Sprintf(" AND %s BETWEEN $%d::%s AND $%d::%s", col, n+1, cast, n+2, cast), []any{s.from, s.to}
}

// fetchRecords reads the records of keys, by kind, keyed by kind and id. The
// keys' times, when set, only narrow the chunks read. The diastolic half of
// a pair is left out unless it is asked for as a partner. With a policy, a
// daily summary's figures that a better ranked row of the day holds read as
// null.
func fetchRecords(ctx context.Context, db Querier, tenantID, userID uuid.UUID, keys []obsKey, pol *fusion.Policy) (map[obsKey]ObservationRecord, error) {
	ids := map[Kind][]int64{}
	spans := map[Kind]timeSpan{}
	for _, k := range keys {
		ids[k.kind] = append(ids[k.kind], k.id)
		spans[k.kind] = spans[k.kind].add(k.t)
	}
	out := map[obsKey]ObservationRecord{}
	if len(ids[KindQuantity]) > 0 {
		samples, partners, err := fetchSamples(ctx, db, tenantID, userID, ids[KindQuantity], spans[KindQuantity], false)
		if err != nil {
			return nil, err
		}
		var want []int64
		for _, p := range partners {
			want = append(want, p)
		}
		halves := map[int64]*CodedSample{}
		if len(want) > 0 {
			// A diastolic half starts when its systolic sample does.
			hs, _, err := fetchSamples(ctx, db, tenantID, userID, want, spans[KindQuantity], true)
			if err != nil {
				return nil, err
			}
			for _, h := range hs {
				halves[h.ID] = h
			}
		}
		for _, s := range samples {
			r := ObservationRecord{Kind: KindQuantity, Sample: s}
			if p, ok := partners[s.ID]; ok {
				if r.Partner = halves[p]; r.Partner == nil {
					continue // the half went away between the queries
				}
			}
			out[obsKey{kind: KindQuantity, id: s.ID}] = r
		}
	}
	if len(ids[KindSleep]) > 0 {
		err := fetchByIDs(ctx, db, "ohs_data.sleep_sessions", sleepCols, tenantID, userID, ids[KindSleep], spans[KindSleep], "bedtime_start", "timestamptz", func(r pgx.Rows) error {
			var s SleepSession
			if err := r.Scan(s.dest()...); err != nil {
				return err
			}
			s.utc()
			out[obsKey{kind: KindSleep, id: s.ID}] = ObservationRecord{Kind: KindSleep, Sleep: &s}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(ids[KindWorkout]) > 0 {
		err := fetchByIDs(ctx, db, "ohs_data.workouts", workoutCols, tenantID, userID, ids[KindWorkout], spans[KindWorkout], "start_date", "timestamptz", func(r pgx.Rows) error {
			var w Workout
			if err := r.Scan(w.dest()...); err != nil {
				return err
			}
			w.utc()
			out[obsKey{kind: KindWorkout, id: w.ID}] = ObservationRecord{Kind: KindWorkout, Workout: &w}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(ids[KindDaily]) > 0 {
		cols := dailyCols
		var extra []any
		if pol != nil {
			// fetchByIDs binds $1 to $3.
			args := []any{nil, nil, nil}
			mp := bindMerge(*pol, nil, func(v any) string {
				args = append(args, v)
				return "$" + strconv.Itoa(len(args))
			})
			mp.window(spans[KindDaily].from, spans[KindDaily].to)
			cols, extra = dailyMaskedCols(mp, "r"), args[3:]
		}
		err := fetchByIDs(ctx, db, "ohs_data.daily_summaries", cols, tenantID, userID, ids[KindDaily], spans[KindDaily], "summary_date", "date", func(r pgx.Rows) error {
			var d DailySummary
			var day time.Time
			if err := r.Scan(d.dest(&day)...); err != nil {
				return err
			}
			d.finish(day)
			out[obsKey{kind: KindDaily, id: d.ID}] = ObservationRecord{Kind: KindDaily, Daily: &d}
			return nil
		}, extra...)
		if err != nil {
			return nil, err
		}
	}
	if len(ids[KindECG]) > 0 {
		var recs []ECGRecording
		err := fetchByIDs(ctx, db, "ohs_data.ecg_recordings", ecgCols, tenantID, userID, ids[KindECG], spans[KindECG], "start_date", "timestamptz", func(r pgx.Rows) error {
			var e ecgRow
			if err := r.Scan(e.dest()...); err != nil {
				return err
			}
			recs = append(recs, e.finish())
			return nil
		})
		if err != nil {
			return nil, err
		}
		samples, err := ecgSamples(ctx, db, tenantID, userID, recs)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out[obsKey{kind: KindECG, id: rec.ID}] = ObservationRecord{Kind: KindECG,
				ECG: &ECGRecordingDetail{ECGRecording: rec, Samples: samples[rec.ID]}}
		}
	}
	if len(ids[KindNutrition]) > 0 {
		err := fetchByIDs(ctx, db, "ohs_data.nutrition_logs", nutritionCols, tenantID, userID, ids[KindNutrition], spans[KindNutrition], "start_date", "timestamptz", func(r pgx.Rows) error {
			var x NutritionLog
			if err := r.Scan(x.dest()...); err != nil {
				return err
			}
			x.utc()
			out[obsKey{kind: KindNutrition, id: x.ID}] = ObservationRecord{Kind: KindNutrition, Nutrition: &x}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// fetchSamples reads quantity samples by id with their catalog codes, and
// the id of the diastolic partner of each paired systolic sample. Unless
// halves, the diastolic half of a pair is left out.
func fetchSamples(ctx context.Context, db Querier, tenantID, userID uuid.UUID, ids []int64, span timeSpan, halves bool) ([]*CodedSample, map[int64]int64, error) {
	cond, extra := span.where("q.start_date", 3, "timestamptz")
	var bd bound
	if cond != "" {
		bd = func(a string) string { return " AND " + a + ".start_date BETWEEN $4::timestamptz AND $5::timestamptz" }
	}
	rows, err := db.Query(ctx, `SELECT `+codedSelect(bd)+` FROM ohs_data.quantity_samples q
		WHERE q.tenant_id = $1 AND q.user_id = $2 AND q.id = ANY($3::bigint[])`+cond,
		append([]any{tenantID, userID, ids}, extra...)...)
	if err != nil {
		return nil, nil, fmt.Errorf("read: quantity samples: %w", err)
	}
	defer rows.Close()
	var out []*CodedSample
	partners := map[int64]int64{}
	for rows.Next() {
		var s CodedSample
		var partner *int64
		var half bool
		if err := rows.Scan(append(s.dest(), &partner, &half)...); err != nil {
			return nil, nil, fmt.Errorf("read: quantity samples: scan: %w", err)
		}
		s.utc()
		if half && !halves {
			continue
		}
		if partner != nil {
			partners[s.ID] = *partner
		}
		out = append(out, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read: quantity samples: %w", err)
	}
	return out, partners, nil
}

// fetchByIDs reads rows of table (alias r) by id, within span on timeCol
// when it bounds anything; extra binds $4 onwards.
func fetchByIDs(ctx context.Context, db Querier, table, cols string, tenantID, userID uuid.UUID, ids []int64,
	span timeSpan, timeCol, cast string, scan func(pgx.Rows) error, extra ...any) error {
	cond, bounds := span.where("r."+timeCol, 3+len(extra), cast)
	args := append(append([]any{tenantID, userID, ids}, extra...), bounds...)
	rows, err := db.Query(ctx, `SELECT `+cols+` FROM `+table+` r
		WHERE r.tenant_id = $1 AND r.user_id = $2 AND r.id = ANY($3::bigint[])`+cond, args...)
	if err != nil {
		return fmt.Errorf("read: %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return fmt.Errorf("read: %s: scan: %w", table, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read: %s: %w", table, err)
	}
	return nil
}

// TypesForLOINC lists the quantity types the catalog codes with code.
func TypesForLOINC(ctx context.Context, db Querier, code string) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT canonical_code FROM ohs_data.quantity_codes WHERE loinc_code = $1 ORDER BY 1`, code)
	if err != nil {
		return nil, fmt.Errorf("read: quantity types: %w", err)
	}
	types, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read: quantity types: %w", err)
	}
	return types, nil
}

// CheckQuantityType returns a QueryError for a type outside the quantity
// type catalog that this user has no samples of (see Samples).
func CheckQuantityType(ctx context.Context, db Querier, tenantID, userID uuid.UUID, qt string) error {
	return knownQuantityType(ctx, db, tenantID, userID, qt)
}

// CheckUser returns ErrNotFound unless user is an active user of tenant.
func CheckUser(ctx context.Context, db Querier, tenantID, userID uuid.UUID) error {
	return userExists(ctx, db, tenantID, userID)
}

// Check reports a mistake in q (a QueryError or ErrInvalidCursor) without
// running it.
func (q Query) Check() error {
	_, err := q.normalize(false)
	return err
}
