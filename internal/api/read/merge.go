// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/fusion"
)

// View says whether a read merges overlapping sources.
type View string

// Views.
const (
	// ViewMerged, the default, leaves out a record that a better ranked
	// source covers (see package fusion), so nothing is counted twice.
	ViewMerged View = "merged"
	// ViewAll returns every stored record of every source.
	ViewAll View = "all"
)

// ParseView reads a view parameter; empty is ViewMerged.
func ParseView(v string) (View, error) {
	switch View(v) {
	case "", ViewMerged:
		return ViewMerged, nil
	case ViewAll:
		return ViewAll, nil
	}
	return "", &QueryError{Msg: "view must be merged or all"}
}

// merged reports whether q reads the merged view.
func (q Query) merged() bool { return q.View != ViewAll }

// mergeParams bind the SQL parameters of a merged read on first use
// (Postgres refuses a parameter the statement does not use). Each is read
// through an uncorrelated scalar subquery, which Postgres evaluates once per
// statement rather than once per row.
type mergeParams struct {
	p       fusion.Policy
	metrics []string
	arg     func(any) string
	orders  map[string]string
	lists   map[string]string
}

// order is the SQL text[] of metric's order of sources.
func (mp *mergeParams) order(metric string) string {
	o := mp.p.Order(metric)
	k := strings.Join(o, "\x00")
	if ref, ok := mp.orders[k]; ok {
		return ref
	}
	ref := "(SELECT " + mp.arg(o) + "::text[])"
	mp.orders[k] = ref
	return ref
}

// list is the SQL text[] of a set of metric names.
func (mp *mergeParams) list(metrics []string) string {
	k := strings.Join(metrics, "\x00")
	if ref, ok := mp.lists[k]; ok {
		return ref
	}
	// The cast makes ANY take the array, not the subquery's rows.
	ref := "(SELECT " + mp.arg(metrics) + "::text[])::text[]"
	mp.lists[k] = ref
	return ref
}

// policy returns the user's policy: q.Policy, or the stored one.
func policy(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) (fusion.Policy, error) {
	if q.Policy != nil {
		return *q.Policy, nil
	}
	p, err := fusion.Load(ctx, db, tenantID, userID, nil)
	if err != nil {
		return fusion.Policy{}, fmt.Errorf("read: %w", err)
	}
	return p, nil
}

// bindMerge returns the parameters of p, bound through arg when used. A
// statement over records of several metrics resolves the order and the rule
// of each of metrics, of every known metric and of every key the user set;
// any other metric takes the '*' order and the strict rule.
func bindMerge(p fusion.Policy, metrics []string, arg func(any) string) *mergeParams {
	all := append(append(fusion.KnownMetrics(), p.Keys()...), metrics...)
	slices.Sort(all)
	return &mergeParams{p: p, metrics: slices.Compact(all), arg: arg, orders: map[string]string{}, lists: map[string]string{}}
}

// family describes a table of records the merged view ranks.
type family struct {
	// key is the metric of every record of the table, when it has one.
	key        string
	table      string
	start, end string
	// metric is the SQL metric key of a record of alias x.
	metric func(x string) string
	// same is the extra condition for b to be the same metric as a.
	same func(a, b string) string
	// stream is the SQL key of the stream of x inside its provider (a phone
	// and a watch behind one provider are two streams).
	stream func(x string) string
	// wear is 0 for a stream worn on the body, 1 otherwise: it breaks a tie
	// between two streams of one source.
	wear func(x string) string
}

var (
	quantityFamily = family{
		table: "ohs_data.quantity_samples", start: "start_date", end: "end_date",
		metric: func(x string) string { return x + ".quantity_type" },
		same:   func(a, b string) string { return b + ".quantity_type = " + a + ".quantity_type" },
		stream: func(x string) string {
			return "coalesce(nullif(" + x + ".source_bundle_id, ''), nullif(" + x + ".source_name, ''), nullif(" +
				x + ".device_id, ''), '')"
		},
		wear: func(x string) string {
			return "CASE WHEN concat_ws(' ', " + x + ".product_type, " + x + ".device_model, " + x +
				".source_name) ILIKE '%watch%' THEN 0 ELSE 1 END"
		},
	}
	categoryFamily = family{
		table: "ohs_data.category_samples", start: "start_date", end: "end_date",
		metric: func(x string) string { return x + ".category_type" },
		same:   func(a, b string) string { return b + ".category_type = " + a + ".category_type" },
		stream: func(x string) string {
			return "coalesce(nullif(" + x + ".source_name, ''), nullif(" + x + ".device_id, ''), '')"
		},
		wear: func(string) string { return "0" },
	}
	sleepFamily = family{
		key:   fusion.MetricSleep,
		table: "ohs_data.sleep_sessions", start: "bedtime_start", end: "bedtime_end",
		metric: func(string) string { return "'" + fusion.MetricSleep + "'" },
		same:   func(string, string) string { return "TRUE" },
		stream: parentStream,
		wear:   parentWear,
	}
	workoutFamily = family{
		key:   fusion.MetricWorkouts,
		table: "ohs_data.workouts", start: "start_date", end: "end_date",
		metric: func(string) string { return "'" + fusion.MetricWorkouts + "'" },
		same:   func(string, string) string { return "TRUE" },
		stream: parentStream,
		wear:   parentWear,
	}
)

// parentStream is the stream of a sleep session or a workout. These tables
// have no source column: Apple Health keeps the app that wrote the record
// (the watch, the phone, a sleep app) in metadata. Without it every Apple
// Health night was one stream, which never covers itself, so a night that
// two apps recorded counted twice.
func parentStream(x string) string {
	return "coalesce(nullif(" + x + ".device_id, ''), nullif(" + x + ".metadata->>'sourceBundleId', ''), nullif(" +
		x + ".metadata->>'sourceName', ''), '')"
}

// parentWear puts a watch's record first among the streams of one source.
func parentWear(x string) string {
	return "CASE WHEN " + x + ".metadata->>'sourceName' ILIKE '%watch%' THEN 0 ELSE 1 END"
}

// rank is the SQL rank of x's source in an order (lower is better); a
// source missing from it ranks after every listed one.
func rank(order, x string) string {
	return "coalesce(array_position(" + order + ", coalesce(" + x + ".source_slug, " + x + ".provider)), 1000000)"
}

// orderBy is the order of sources (text[]) of the SQL expression metric: a
// CASE over the metrics grouped by their order.
func (mp *mergeParams) orderBy(metric string) string {
	groups := map[string][]string{}
	var keys []string
	for _, m := range mp.metrics {
		k := strings.Join(mp.p.Order(m), "\x00")
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], m)
	}
	var b strings.Builder
	b.WriteString("(CASE")
	for _, k := range keys {
		ms := groups[k]
		b.WriteString(" WHEN " + metric + " = ANY(" + mp.list(ms) + ") THEN " + mp.order(ms[0]))
	}
	b.WriteString(" ELSE " + mp.order(fusion.All) + " END)")
	return b.String()
}

// ruleBy is the rule of the SQL expression metric: tolerance, max span and
// min share, each a CASE over the metrics grouped by rule.
func (mp *mergeParams) ruleBy(metric string) (tol, span, share string) {
	groups := map[fusion.Rule][]string{}
	var rules []fusion.Rule
	for _, m := range mp.metrics {
		r := fusion.RuleFor(m)
		if _, ok := groups[r]; !ok {
			rules = append(rules, r)
		}
		groups[r] = append(groups[r], m)
	}
	strict := fusion.RuleFor("")
	pick := func(lit func(fusion.Rule) string) string {
		var b strings.Builder
		b.WriteString("(CASE")
		for _, r := range rules {
			b.WriteString(" WHEN " + metric + " = ANY(" + mp.list(groups[r]) + ") THEN " + lit(r))
		}
		b.WriteString(" ELSE " + lit(strict) + " END)")
		return b.String()
	}
	return pick(func(r fusion.Rule) string { return seconds(r.Tolerance) }),
		pick(func(r fusion.Rule) string { return seconds(r.MaxSpan) }),
		pick(func(r fusion.Rule) string { return strconv.FormatFloat(r.MinShare, 'f', -1, 64) })
}

// seconds is d as an SQL interval literal.
func seconds(d time.Duration) string {
	return fmt.Sprintf("interval '%d seconds'", int64(d.Seconds()))
}

// covered is a SQL condition true when a better ranked source has a record
// that matches the record of alias a under its metric's rule. metric names
// the metric of every record of the statement when there is one (a list of
// one type, sleep, workouts): its rule and order are then inlined, which
// lets Postgres plan the lookup as one index range per record.
func (f family) covered(mp *mergeParams, a, metric string) string {
	b, p := a+"_cover", a+"_rule"
	var tol, span, share, order string
	if metric != "" {
		r := fusion.RuleFor(metric)
		tol, span = seconds(r.Tolerance), seconds(r.MaxSpan)
		share = strconv.FormatFloat(r.MinShare, 'f', -1, 64)
		order = mp.order(metric)
	} else {
		// Resolved once per record, in the subquery p below, rather than for
		// every record it is compared with.
		tol, span, share, order = p+".tol", p+".span", p+".share", p+".ord"
	}
	key := func(x string) string { return x + ".provider || '|' || " + f.stream(x) }
	// b's stream ranks before a's: by source, then worn on the body first,
	// then by stream key, so every pair of streams is ordered and a stream
	// never covers itself.
	better := "(" + rank(order, b) + ", " + f.wear(b) + ", " + key(b) + ") < (" +
		rank(order, a) + ", " + f.wear(a) + ", " + key(a) + ")"
	aS, bS := a+"."+f.start, b+"."+f.start
	aE, bE := "coalesce("+a+"."+f.end+", "+aS+")", "coalesce("+b+"."+f.end+", "+bS+")"
	// A scalar subquery with LIMIT, not EXISTS: Postgres would turn NOT
	// EXISTS into an anti join, hashed on the metric, that compares every
	// pair of the user's records; this runs per record on the index range
	// and stops at the first match.
	sql := "coalesce((SELECT true FROM " + f.table + " " + b + " WHERE " +
		b + ".tenant_id = " + a + ".tenant_id AND " + b + ".user_id = " + a + ".user_id AND " + f.same(a, b) +
		// The two bounds on b's start are the index range.
		" AND " + bS + " >= " + aS + " - " + span + " - " + tol + " AND " + bS + " <= " + aE + " + " + tol +
		" AND " + bE + " >= " + aS + " - " + tol +
		// With no tolerance, two intervals that only touch (9:00-9:10 and
		// 9:10-9:20) do not match; two points at one time do.
		" AND NOT (" + tol + " = interval '0' AND " + aS + " < " + aE + " AND " + bS + " < " + bE +
		" AND (" + bS + " = " + aE + " OR " + bE + " = " + aS + "))" +
		" AND (" + share + " = 0 OR extract(epoch FROM least(" + aE + ", " + bE + ") - greatest(" + aS + ", " + bS + ")) >= " +
		share + " * least(extract(epoch FROM " + aE + " - " + aS + "), extract(epoch FROM " + bE + " - " + bS + ")))" +
		" AND " + better + " LIMIT 1), false)"
	if metric != "" {
		return sql
	}
	t, sp, sh := mp.ruleBy(f.metric(a))
	return "(SELECT " + sql + " FROM (SELECT " + mp.orderBy(f.metric(a)) + " AS ord, " + t + " AS tol, " +
		sp + " AS span, " + sh + " AS share) " + p + ")"
}

// dailyFields maps each figure of a daily summary to the metric whose order
// ranks it, in the order of the summary's columns.
var dailyFields = []struct{ col, json, metric string }{
	{"readiness_score", "readiness_score", "readiness_score"},
	{"activity_score", "activity_score", "activity_score"},
	{"sleep_score", "sleep_score", "sleep_score"},
	{"recovery_score", "recovery_score", "recovery_score"},
	{"strain_score", "strain_score", "strain"},
	{"step_count", "step_count", "step_count"},
	{"active_kcal", "active_kcal", "active_energy_burned"},
	{"total_kcal", "total_kcal", "basal_energy_burned"},
	{"distance_m", "distance_m", "distance_walking_running"},
	{"floors_climbed", "floors_climbed", "flights_climbed"},
	{"resting_hr", "resting_hr", "resting_heart_rate"},
	{"hrv_avg_ms", "hrv_avg_ms", "hrv_rmssd"},
}

// dailyRank is the rank of daily row x for one figure's metric, then its id.
func dailyRank(mp *mergeParams, x, metric string) string {
	return "(" + rank(mp.order(metric), x) + ", " + x + ".id)"
}

// dailyOwned is a SQL condition true when daily row d holds col and no
// better ranked row of the same day does.
func dailyOwned(mp *mergeParams, d, col, metric string) string {
	b := d + "_own"
	return d + "." + col + " IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ohs_data.daily_summaries " + b +
		" WHERE " + b + ".tenant_id = " + d + ".tenant_id AND " + b + ".user_id = " + d + ".user_id AND " +
		b + ".summary_date = " + d + ".summary_date AND " + b + "." + col + " IS NOT NULL AND " +
		dailyRank(mp, b, metric) + " < " + dailyRank(mp, d, metric) + ")"
}

// dailyOwnsAny is true when daily row d holds at least one figure it owns.
func dailyOwnsAny(mp *mergeParams, d string) string {
	conds := make([]string, len(dailyFields))
	for i, f := range dailyFields {
		conds[i] = "(" + dailyOwned(mp, d, f.col, f.metric) + ")"
	}
	return "(" + strings.Join(conds, " OR ") + ")"
}

// dailyMaskedCols are dailyCols over alias d with every figure a better
// ranked row of the day holds set to NULL.
func dailyMaskedCols(mp *mergeParams, d string) string {
	cols := []string{d + ".id", d + ".summary_date"}
	for _, f := range dailyFields {
		cols = append(cols, "CASE WHEN "+dailyOwned(mp, d, f.col, f.metric)+" THEN "+d+"."+f.col+" END")
	}
	return strings.Join(append(cols, d+".metadata", d+".provider", d+".source_slug", "NULL::text", d+".ingested_at"), ", ")
}

// dailyMerged lists one summary per day: each figure from the best ranked
// source that has it, with Sources naming that source per figure. The rest
// (id, provenance, metadata) is the day's best ranked row under '*'.
func dailyMerged(ctx context.Context, db Querier, tenantID, userID uuid.UUID, n normalized, p fusion.Policy) (Page[DailySummary], error) {
	args := []any{tenantID, userID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	mp := bindMerge(p, nil, arg)
	where := []string{"d.tenant_id = $1", "d.user_id = $2"}
	date := func(t time.Time) string { return arg(t.UTC().Format(time.DateOnly)) + "::date" }
	if !n.Start.IsZero() {
		where = append(where, "d.summary_date >= "+date(n.Start))
	}
	if !n.End.IsZero() {
		where = append(where, "d.summary_date < "+date(n.End))
	}
	if n.paged {
		where = append(where, "d.summary_date > "+date(n.afterT))
	}
	slug := "coalesce(d.source_slug, d.provider)"
	primary := func(col string) string {
		return "(array_agg(" + col + " ORDER BY " + dailyRank(mp, "d", fusion.All) + "))[1]"
	}
	cols := []string{primary("d.id"), "d.summary_date"}
	for _, f := range dailyFields {
		order := " ORDER BY " + dailyRank(mp, "d", f.metric) + ") FILTER (WHERE d." + f.col + " IS NOT NULL))[1]"
		cols = append(cols, "(array_agg(d."+f.col+order, "(array_agg("+slug+order)
	}
	cols = append(cols, primary("d.metadata"), primary("d.provider"), primary("d.source_slug"), primary("d.ingested_at"))
	sql := "SELECT " + strings.Join(cols, ", ") + " FROM ohs_data.daily_summaries d WHERE " + strings.Join(where, " AND ") +
		" GROUP BY d.summary_date ORDER BY d.summary_date LIMIT " + arg(n.Limit+1)
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return Page[DailySummary]{}, fmt.Errorf("read: daily merged: %w", err)
	}
	defer rows.Close()
	out := Page[DailySummary]{Records: []DailySummary{}}
	var last time.Time
	for rows.Next() {
		if len(out.Records) == n.Limit {
			out.NextCursor = EncodeCursor(last, 0)
			break
		}
		d, day, err := scanMergedDay(rows)
		if err != nil {
			return Page[DailySummary]{}, fmt.Errorf("read: daily merged: scan: %w", err)
		}
		out.Records = append(out.Records, d)
		last = day
	}
	if err := rows.Err(); err != nil {
		return Page[DailySummary]{}, fmt.Errorf("read: daily merged: %w", err)
	}
	return out, nil
}

// scanMergedDay reads one row of dailyMerged.
func scanMergedDay(r pgx.Rows) (DailySummary, time.Time, error) {
	var d DailySummary
	var day time.Time
	src := make([]*string, len(dailyFields))
	figures := []any{&d.ReadinessScore, &d.ActivityScore, &d.SleepScore, &d.RecoveryScore, &d.StrainScore,
		&d.StepCount, &d.ActiveKcal, &d.TotalKcal, &d.DistanceM, &d.FloorsClimbed, &d.RestingHR, &d.HRVAvgMs}
	dest := []any{&d.ID, &day}
	for i := range dailyFields {
		dest = append(dest, figures[i], &src[i])
	}
	dest = append(dest, &d.Metadata, &d.Provider, &d.Source, &d.IngestedAt)
	if err := r.Scan(dest...); err != nil {
		return d, day, err
	}
	d.Sources = map[string]string{}
	for i, f := range dailyFields {
		if src[i] != nil {
			d.Sources[f.json] = *src[i]
		}
	}
	day = d.finish(day)
	return d, day, nil
}
