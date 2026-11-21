// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/fusion"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// Stats intervals.
const (
	IntervalHour = "hour"
	IntervalDay  = "day"
)

// MaxStatsBuckets bounds the number of intervals one stats query spans (an
// hourly range of about 13 months, a daily range of 27 years). A variable so
// tests can lower it.
var MaxStatsBuckets = 10000

// MaxStatsRows bounds one stats answer; past it Stats.Truncated is set.
var MaxStatsRows = 50000

// StatsQuery asks for the statistics of one quantity type per interval.
type StatsQuery struct {
	Type     string
	Interval string
	// Start and End bound the range, [Start, End); both are required. They
	// are widened to whole intervals in Location.
	Start, End time.Time
	// Location is the time zone of the intervals (a day is local midnight to
	// local midnight). Nil means UTC.
	Location *time.Location
	// Provider keeps one provider when set.
	Provider string
	// View is ViewMerged (the default) or ViewAll: every stream's series,
	// or per interval only the stream the merged reads rank first.
	View View
	// Policy ranks the user's sources in the merged view; nil reads the
	// user's stored order.
	Policy *fusion.Policy
}

// Stats is the answer of SampleStats.
type Stats struct {
	Type     string `json:"type"`
	Interval string `json:"interval"`
	TimeZone string `json:"tz"`
	View     View   `json:"view"`
	// Start and End are the range after widening to whole intervals.
	Start   time.Time     `json:"start"`
	End     time.Time     `json:"end"`
	Buckets []StatsBucket `json:"buckets"`
	// Truncated is true when more than MaxStatsRows rows matched: Buckets
	// holds the earliest ones.
	Truncated bool `json:"truncated"`
}

// StatsBucket is one interval of one stream: the samples of one provider,
// source and unit whose start falls in [Start, Start + interval). Streams are
// never added up, so a phone and a watch counting the same steps do not
// count twice: the merged view keeps the best ranked one per interval, the
// all view returns each.
type StatsBucket struct {
	Start    time.Time `json:"start"`
	Provider string    `json:"provider"`
	// Source is the app, bundle or device that recorded the samples, empty
	// when the provider names none.
	Source string  `json:"source"`
	Unit   *string `json:"unit"`
	Count  int64   `json:"count"`
	Sum    float64 `json:"sum"`
	Avg    float64 `json:"avg"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`

	// The keys that rank the bucket's stream in the merged view, while the
	// buckets are folded in Go (stats_fold.go); valued is true once a value
	// was folded in. They are cleared before the answer leaves.
	slug   string
	worn   int
	key    string
	valued bool
}

// statsSource is the source of a sample, as the ETL's daily sums and the
// quantity_hourly aggregate name it.
const statsSource = `COALESCE(NULLIF(source_bundle_id, ''), NULLIF(source_name, ''), NULLIF(device_id, ''), '')`

// statsWorn is 0 for a sample a watch recorded and 1 otherwise, as the
// merged view ranks streams and quantity_hourly stores it.
const statsWorn = `CASE WHEN (coalesce(product_type, '') || ' ' || coalesce(device_model, '') || ' ' ||
	coalesce(source_name, '')) ILIKE '%watch%' THEN 0 ELSE 1 END`

// SampleStats returns count, sum, mean, min and max of a quantity type per
// hour or per local day. It reads hourly statistics when the database has
// them and every interval is a whole number of hours in UTC (zones with a
// whole-hour offset), and the samples otherwise.
//
// Under a vault (WithVault) the values are sealed and the statistics are
// computed in Go (sealedStats): from the sealed hourly buckets, and from the
// quantity_hourly continuous aggregate for the hours that hold only rows
// not converted yet. Without one SQL computes them, as before the vault,
// from the continuous aggregate or the samples.
func SampleStats(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q StatsQuery) (Stats, error) {
	loc := q.Location
	if loc == nil {
		loc = time.UTC
	}
	var step time.Duration
	switch q.Interval {
	case IntervalHour:
		step = time.Hour
	case IntervalDay:
		step = 24 * time.Hour
	default:
		return Stats{}, &QueryError{Msg: `interval must be "hour" or "day"`}
	}
	if q.Type == "" {
		return Stats{}, &QueryError{Msg: "the type parameter is required"}
	}
	if _, err := ParseView(string(q.View)); err != nil {
		return Stats{}, err
	}
	if q.Start.IsZero() || q.End.IsZero() {
		return Stats{}, &QueryError{Msg: "start and end are required"}
	}
	start, end := floorInterval(q.Start, q.Interval, loc), ceilInterval(q.End, q.Interval, loc)
	if !start.Before(end) {
		return Stats{}, &QueryError{Msg: "start must be before end"}
	}
	if n := end.Sub(start) / step; n > time.Duration(MaxStatsBuckets) {
		return Stats{}, &QueryError{Msg: fmt.Sprintf("the range spans %d intervals, more than %d: narrow start and end or use a longer interval", n, MaxStatsBuckets)}
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Stats{}, err
	}
	if err := knownQuantityType(ctx, db, tenantID, userID, q.Type); err != nil {
		return Stats{}, err
	}

	if vault.FromContext(ctx) != nil {
		return sealedStats(ctx, db, tenantID, userID, q, start, end, step, loc)
	}

	useAggregate := false
	if wholeHourOffsets(start, end, loc) {
		if err := db.QueryRow(ctx, `SELECT to_regclass('ohs_data.quantity_hourly') IS NOT NULL`).Scan(&useAggregate); err != nil {
			return Stats{}, fmt.Errorf("read: stats: %w", err)
		}
	}
	// $6 is the interval width, $7 the zone: time_bucket with a zone puts day
	// boundaries at local midnight, also across a daylight saving change.
	args := []any{tenantID, userID, q.Type, start, end, "1 " + q.Interval, loc.String()}
	where := "tenant_id = $1 AND user_id = $2 AND quantity_type = $3"
	if q.Provider != "" {
		args = append(args, q.Provider)
		where += " AND provider = $" + strconv.Itoa(len(args))
	}
	// g holds one row per interval and stream (provider and source), with
	// the stream's rank keys in the merged view.
	var g string
	if useAggregate {
		g = `SELECT time_bucket($6::interval, bucket, $7) AS b, provider, source, slug, worn, unit,
				sum(n)::bigint AS n, sum(total) AS total, min(low) AS low, max(high) AS high
			FROM ohs_data.quantity_hourly
			WHERE ` + where + ` AND bucket >= $4 AND bucket < $5`
	} else {
		g = `SELECT time_bucket($6::interval, start_date, $7) AS b, provider, ` + statsSource + ` AS source,
				coalesce(source_slug, provider) AS slug, ` + statsWorn + ` AS worn, unit,
				count(*) AS n, sum(value) AS total, min(value) AS low, max(value) AS high
			FROM ohs_data.quantity_samples
			WHERE ` + where + ` AND start_date >= $4 AND start_date < $5`
	}
	g += ` GROUP BY 1, 2, 3, 4, 5, 6`
	var sql string
	if q.View == ViewAll {
		sql = `SELECT b, provider, source, unit, sum(n)::bigint, sum(total), min(low), max(high)
			FROM (` + g + `) g GROUP BY 1, 2, 3, 4 ORDER BY 1, 2, 3, 4`
	} else {
		// The merged view keeps, per interval, the stream the merged reads
		// rank first: by the user's order of sources for this type, then
		// worn on the body, then by stream key.
		p, err := policy(ctx, db, tenantID, userID, Query{Policy: q.Policy})
		if err != nil {
			return Stats{}, err
		}
		args = append(args, p.Order(q.Type))
		sql = `SELECT DISTINCT ON (b) b, provider, source, unit, n, total, low, high
			FROM (` + g + `) g
			ORDER BY b, coalesce(array_position($` + strconv.Itoa(len(args)) + `::text[], slug), 1000000), worn,
				provider || '|' || source`
	}
	sql += ` LIMIT ` + strconv.Itoa(MaxStatsRows+1)

	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return Stats{}, fmt.Errorf("read: stats: %w", err)
	}
	defer rows.Close()
	view := ViewMerged
	if q.View == ViewAll {
		view = ViewAll
	}
	s := Stats{Type: q.Type, Interval: q.Interval, TimeZone: loc.String(), View: view,
		Start: start, End: end, Buckets: []StatsBucket{}}
	for rows.Next() {
		if len(s.Buckets) == MaxStatsRows {
			s.Truncated = true
			break
		}
		var b StatsBucket
		if err := rows.Scan(&b.Start, &b.Provider, &b.Source, &b.Unit, &b.Count, &b.Sum, &b.Min, &b.Max); err != nil {
			return Stats{}, fmt.Errorf("read: stats: scan: %w", err)
		}
		b.Start = b.Start.In(loc)
		if b.Count > 0 {
			b.Avg = b.Sum / float64(b.Count)
		}
		s.Buckets = append(s.Buckets, b)
	}
	if err := rows.Err(); err != nil {
		return Stats{}, fmt.Errorf("read: stats: %w", err)
	}
	return s, nil
}

// floorInterval returns the start of the interval holding t in loc.
func floorInterval(t time.Time, interval string, loc *time.Location) time.Time {
	l := t.In(loc)
	if interval == IntervalDay {
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	}
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), 0, 0, 0, loc)
}

// ceilInterval returns t when it starts an interval in loc, else the start
// of the next one.
func ceilInterval(t time.Time, interval string, loc *time.Location) time.Time {
	f := floorInterval(t, interval, loc)
	if f.Equal(t) {
		return f
	}
	if interval == IntervalDay {
		l := f.In(loc)
		return time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc)
	}
	return f.Add(time.Hour)
}

// wholeHourOffsets reports whether loc's UTC offset is a whole number of
// hours over all of [start, end), so its hours and days are sums of UTC hours.
func wholeHourOffsets(start, end time.Time, loc *time.Location) bool {
	for t := start.In(loc); t.Before(end); {
		if _, off := t.Zone(); off%3600 != 0 {
			return false
		}
		_, next := t.ZoneBounds()
		if next.IsZero() || !next.After(t) {
			return true
		}
		t = next
	}
	return true
}
