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

	"github.com/soma-intelligence/open-health-sync/internal/fusion"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// The merged view of the daily summaries. A day has one row per source, and
// each figure of the day belongs to the best ranked row that holds it: by the
// user's order of sources for the figure's metric, then by id. The figures
// are sealed, so SQL cannot tell which rows hold one: it selects the rows of
// whole days on clear columns and the ranking runs here on the opened rows.

// dailyRow is one stored summary with what ranks it.
type dailyRow struct {
	d    DailySummary
	day  time.Time // midnight UTC of the summary's date
	slug string    // coalesce(source_slug, provider)
}

// figures are the figures of d, in the order of dailyFields.
func (d *DailySummary) figures() []any {
	return []any{&d.ReadinessScore, &d.ActivityScore, &d.SleepScore, &d.RecoveryScore, &d.StrainScore,
		&d.StepCount, &d.ActiveKcal, &d.TotalKcal, &d.DistanceM, &d.FloorsClimbed, &d.RestingHR, &d.HRVAvgMs}
}

// figureSet reports whether a figure (an element of figures) holds a value.
func figureSet(f any) bool {
	switch p := f.(type) {
	case **float64:
		return *p != nil
	case **int:
		return *p != nil
	}
	return false
}

// figureCopy sets the figure dst to the value of src.
func figureCopy(dst, src any) {
	switch p := dst.(type) {
	case **float64:
		*p = *src.(**float64)
	case **int:
		*p = *src.(**int)
	}
}

// figureClear empties a figure.
func figureClear(f any) {
	switch p := f.(type) {
	case **float64:
		*p = nil
	case **int:
		*p = nil
	}
}

// sourceRank is the rank of a source in an order (lower is better); a source
// missing from it ranks after every listed one.
func sourceRank(order []string, slug string) int {
	if i := slices.Index(order, slug); i >= 0 {
		return i + 1
	}
	return 1000000
}

// before reports whether row a ranks before row b under order: by source,
// then by id.
func (a *dailyRow) before(b *dailyRow, order []string) bool {
	ra, rb := sourceRank(order, a.slug), sourceRank(order, b.slug)
	if ra != rb {
		return ra < rb
	}
	return a.d.ID < b.d.ID
}

// owners returns, for each figure of dailyFields, the row of the day (an
// index into rows) that owns it, -1 when no row holds it.
func dailyOwners(rows []dailyRow, p fusion.Policy) []int {
	figs := make([][]any, len(rows))
	for i := range rows {
		figs[i] = rows[i].d.figures()
	}
	out := make([]int, len(dailyFields))
	for f, field := range dailyFields {
		out[f] = -1
		order := p.Order(field.metric)
		for i := range rows {
			if !figureSet(figs[i][f]) {
				continue
			}
			if out[f] < 0 || rows[i].before(&rows[out[f]], order) {
				out[f] = i
			}
		}
	}
	return out
}

// mergeDay is the merged summary of one day: each figure from the row that
// owns it, with Sources naming that row's source, and the rest (id,
// provenance, metadata) from the day's best ranked row under '*'.
func mergeDay(rows []dailyRow, p fusion.Policy) DailySummary {
	primary := 0
	all := p.Order(fusion.All)
	for i := range rows {
		if rows[i].before(&rows[primary], all) {
			primary = i
		}
	}
	src := rows[primary].d
	out := DailySummary{ID: src.ID, Date: src.Date, Metadata: src.Metadata, Sources: map[string]string{}}
	out.Provider, out.Source, out.IngestedAt = src.Provider, src.Source, src.IngestedAt
	dst := out.figures()
	for f, owner := range dailyOwners(rows, p) {
		if owner < 0 {
			continue
		}
		figureCopy(dst[f], rows[owner].d.figures()[f])
		out.Sources[dailyFields[f].json] = rows[owner].slug
	}
	return out
}

// maskDay blanks, in every row of one day, the figures a better ranked row
// of the day holds, and reports for each row whether it owns a figure.
func maskDay(rows []dailyRow, p fusion.Policy) []bool {
	owners := dailyOwners(rows, p)
	owns := make([]bool, len(rows))
	for i := range rows {
		figs := rows[i].d.figures()
		for f, owner := range owners {
			if owner == i {
				owns[i] = true
			} else {
				figureClear(figs[f])
			}
		}
	}
	return owns
}

// dailyDays reads the rows of whole days: every row of the user whose date
// is one of the dates the subquery days selects (a SELECT of summary_date
// over alias s, with its own conditions), by date then id, grouped by day.
// args are the statement's arguments; $1 and $2 are the tenant and the user.
func dailyDays(ctx context.Context, db Querier, keys *vault.Reader, days string, args []any) ([][]dailyRow, error) {
	rows, err := db.Query(ctx, `SELECT `+dailySel.List("d")+` FROM ohs_data.daily_summaries d
		WHERE d.tenant_id = $1 AND d.user_id = $2 AND d.summary_date IN (`+days+`)
		ORDER BY d.summary_date, d.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("read: daily summaries: %w", err)
	}
	defer rows.Close()
	var out [][]dailyRow
	for rows.Next() {
		var r dailyRow
		var day time.Time
		if err := dailySel.Scan(ctx, keys, rows, r.d.dest(&day)); err != nil {
			return nil, fmt.Errorf("read: daily summaries: scan: %w", err)
		}
		r.day = r.d.finish(day)
		r.slug = r.d.Provider
		if r.d.Source != nil {
			r.slug = *r.d.Source
		}
		if n := len(out); n > 0 && out[n-1][0].day.Equal(r.day) {
			out[n-1] = append(out[n-1], r)
		} else {
			out = append(out, []dailyRow{r})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: daily summaries: %w", err)
	}
	return out, nil
}

// dailyMerged lists one summary per day (mergeDay), a page of n.Limit days.
func dailyMerged(ctx context.Context, db Querier, tenantID, userID uuid.UUID, n normalized, p fusion.Policy) (Page[DailySummary], error) {
	args := []any{tenantID, userID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := []string{"s.tenant_id = $1", "s.user_id = $2"}
	date := func(t time.Time) string { return arg(t.UTC().Format(time.DateOnly)) + "::date" }
	if !n.Start.IsZero() {
		where = append(where, "s.summary_date >= "+date(n.Start))
	}
	if !n.End.IsZero() {
		where = append(where, "s.summary_date < "+date(n.End))
	}
	if n.paged {
		where = append(where, "s.summary_date > "+date(n.afterT))
	}
	days, err := dailyDays(ctx, db, keysFor(ctx, tenantID, userID),
		"SELECT DISTINCT s.summary_date FROM ohs_data.daily_summaries s WHERE "+strings.Join(where, " AND ")+
			" ORDER BY 1 LIMIT "+arg(n.Limit+1), args)
	if err != nil {
		return Page[DailySummary]{}, err
	}
	out := Page[DailySummary]{Records: []DailySummary{}}
	for _, rows := range days {
		if len(out.Records) == n.Limit {
			out.NextCursor = EncodeCursor(days[n.Limit-1][0].day, 0)
			break
		}
		out.Records = append(out.Records, mergeDay(rows, p))
	}
	return out, nil
}

// dailyUTC is the time of a daily summary in a merged list: its date at
// midnight UTC.
func dailyUTC(alias string) string {
	return "(" + alias + ".summary_date::timestamp AT TIME ZONE 'UTC')"
}

// dailyOwnerKeys lists, for the merged observation list, the keys of the
// daily rows that own at least one figure of their day, by (time, id): those
// in [n.Start, n.End) after the cursor, at most need of them. It reads whole
// days, need days per statement, until it has enough keys or no day is left.
func dailyOwnerKeys(ctx context.Context, db Querier, tenantID, userID uuid.UUID, n normalized, after *obsKey,
	p fusion.Policy, need int) ([]obsKey, error) {
	keys := keysFor(ctx, tenantID, userID)
	var out []obsKey
	var last time.Time
	for {
		args := []any{tenantID, userID}
		arg := func(v any) string {
			args = append(args, v)
			return "$" + strconv.Itoa(len(args))
		}
		where := []string{"s.tenant_id = $1", "s.user_id = $2"}
		if !n.Start.IsZero() {
			where = append(where, dailyUTC("s")+" >= "+arg(n.Start)+"::timestamptz")
		}
		if !n.End.IsZero() {
			where = append(where, dailyUTC("s")+" < "+arg(n.End)+"::timestamptz")
		}
		if after != nil {
			where = append(where, dailyUTC("s")+" >= "+arg(after.t)+"::timestamptz")
		}
		if !last.IsZero() {
			where = append(where, "s.summary_date > "+arg(last.Format(time.DateOnly))+"::date")
		}
		days, err := dailyDays(ctx, db, keys,
			"SELECT DISTINCT s.summary_date FROM ohs_data.daily_summaries s WHERE "+strings.Join(where, " AND ")+
				" ORDER BY 1 LIMIT "+arg(need), args)
		if err != nil {
			return nil, err
		}
		for _, rows := range days {
			last = rows[0].day
			for i, owns := range maskDay(rows, p) {
				k := obsKey{kind: KindDaily, t: rows[i].day, id: rows[i].d.ID}
				if !owns || (after != nil && !obsAfter(k, *after)) {
					continue
				}
				if out = append(out, k); len(out) == need {
					return out, nil
				}
			}
		}
		if len(days) < need {
			return out, nil
		}
	}
}

// obsAfter reports whether k comes after c in the order of the merged
// observation list: time, then kind, then id.
func obsAfter(k, c obsKey) bool {
	if !k.t.Equal(c.t) {
		return k.t.After(c.t)
	}
	if k.kind != c.kind {
		return k.kind > c.kind
	}
	return k.id > c.id
}

// dailyMasked reads the daily rows of ids with, in each, only the figures it
// owns in its day (maskDay). span, when set, bounds the days read.
func dailyMasked(ctx context.Context, db Querier, tenantID, userID uuid.UUID, ids []int64, span timeSpan, p fusion.Policy) ([]DailySummary, error) {
	args := []any{tenantID, userID, ids}
	cond, bounds := span.where("s.summary_date", len(args), "date")
	days, err := dailyDays(ctx, db, keysFor(ctx, tenantID, userID),
		"SELECT s.summary_date FROM ohs_data.daily_summaries s WHERE s.tenant_id = $1 AND s.user_id = $2"+
			" AND s.id = ANY($3::bigint[])"+cond, append(args, bounds...))
	if err != nil {
		return nil, err
	}
	var out []DailySummary
	for _, rows := range days {
		maskDay(rows, p)
		for _, r := range rows {
			if slices.Contains(ids, r.d.ID) {
				out = append(out, r.d)
			}
		}
	}
	return out, nil
}
