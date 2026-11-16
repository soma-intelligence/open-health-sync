// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/vault"
	"github.com/soma-intelligence/open-health-sync/internal/vault/series"
)

// MaxHRVBeats bounds the beats one HRV page reads: a page stops adding
// series once their beats reach it (a page always holds at least one
// series), and the cursor continues from there.
const MaxHRVBeats = 200_000

// RRQuery filters and pages RR intervals or HRV series.
type RRQuery struct {
	// SeriesID keeps one series; empty keeps all.
	SeriesID string
	// Start and End bound the beat time (RR intervals) or the series start
	// (HRV), [Start, End). Zero is open.
	Start, End time.Time
	Cursor     string
	// Limit is the page size: 0 means DefaultLimit, at most MaxLimit.
	Limit int
}

func (q RRQuery) normalize() (normalized, error) {
	n, err := Query{Start: q.Start, End: q.End, Limit: q.Limit}.normalize(false)
	return n, err
}

// encodeKeyCursor returns the cursor after a row at t whose tie-breaking
// key is key (a content hash, or a provider and series id).
func encodeKeyCursor(t time.Time, key ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + strings.Join(key, "|")))
}

// decodeKeyCursor reverses encodeKeyCursor for a key of n parts; the last
// part keeps any "|" it holds.
func decodeKeyCursor(c string, n int) (time.Time, []string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, nil, ErrInvalidCursor
	}
	parts := strings.SplitN(string(raw), "|", n+1)
	if len(parts) != n+1 {
		return time.Time{}, nil, ErrInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, nil, ErrInvalidCursor
	}
	for _, p := range parts[1:] {
		if p == "" {
			return time.Time{}, nil, ErrInvalidCursor
		}
	}
	return t, parts[1:], nil
}

// rrSel reads an RR interval and beatSel a beat of a series: the interval is
// sealed on a sealed row.
var (
	rrSel   = vault.Select("heartbeat_samples", `"timestamp", rr_ms, series_id, provider, ingested_at`)
	beatSel = vault.Select("heartbeat_samples", `provider, series_id, "timestamp", rr_ms`)
)

// RRInterval is one beat-to-beat interval of a heartbeat series.
type RRInterval struct {
	Timestamp  time.Time `json:"timestamp"`
	RRMs       float64   `json:"rr_ms"`
	SeriesID   string    `json:"series_id"`
	Provider   string    `json:"provider"`
	IngestedAt time.Time `json:"ingested_at"`
}

// rrCompare orders RR intervals by beat time, then provider, series id and
// interval (bytewise): a total order over the stored beats, since a series
// holds a beat once.
func rrCompare(a, b RRInterval) int {
	return cmp.Or(a.Timestamp.Compare(b.Timestamp), strings.Compare(a.Provider, b.Provider),
		strings.Compare(a.SeriesID, b.SeriesID), cmp.Compare(a.RRMs, b.RRMs))
}

// rrCursor is the cursor after r: its time, interval, provider and series id
// (last, so it may hold a "|").
func rrCursor(r RRInterval) string {
	return encodeKeyCursor(r.Timestamp, strconv.FormatFloat(r.RRMs, 'g', -1, 64), r.Provider, r.SeriesID)
}

// RRIntervals lists the stored RR intervals in rrCompare order, which the
// cursor carries. A series is read from its blob, or from its per-point rows
// until those are converted; the two are merged here.
func RRIntervals(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q RRQuery) (Page[RRInterval], error) {
	n, err := q.normalize()
	if err != nil {
		return Page[RRInterval]{}, err
	}
	var after *RRInterval
	if q.Cursor != "" {
		t, key, err := decodeKeyCursor(q.Cursor, 3)
		if err != nil {
			return Page[RRInterval]{}, err
		}
		rr, err := strconv.ParseFloat(key[0], 64)
		if err != nil {
			return Page[RRInterval]{}, ErrInvalidCursor
		}
		after = &RRInterval{Timestamp: t, RRMs: rr, Provider: key[1], SeriesID: key[2]}
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[RRInterval]{}, err
	}
	// lower is the earliest beat time the page may hold.
	lower := n.Start
	if after != nil && after.Timestamp.After(lower) {
		lower = after.Timestamp
	}
	keep := func(r RRInterval) bool {
		return (lower.IsZero() || !r.Timestamp.Before(lower)) && (n.End.IsZero() || r.Timestamp.Before(n.End)) &&
			(after == nil || rrCompare(r, *after) > 0)
	}
	cands, upper, err := legacyRR(ctx, db, tenantID, userID, q.SeriesID, lower, n.End, n.Limit+1, keep)
	if err != nil {
		return Page[RRInterval]{}, err
	}
	// trim keeps the limit+1 first candidates; bound is then the time past
	// which no beat can enter the page.
	trim := func() (time.Time, bool) {
		slices.SortFunc(cands, rrCompare)
		if len(cands) > n.Limit+1 {
			cands = cands[:n.Limit+1]
		}
		if len(cands) == n.Limit+1 {
			return cands[n.Limit].Timestamp, true
		}
		return time.Time{}, false
	}
	bound, full := trim()
	if !upper.IsZero() && (!full || upper.Before(bound)) {
		bound, full = upper, true
	}
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args)+2)
	}
	var where []string
	if q.SeriesID != "" {
		where = append(where, "parent = "+arg(q.SeriesID))
	}
	if !lower.IsZero() {
		where = append(where, "end_at >= "+arg(lower))
	}
	if !n.End.IsZero() {
		where = append(where, "start_at < "+arg(n.End))
	}
	err = eachBlob(ctx, db, tenantID, userID, series.Heartbeat, strings.Join(where, " AND "), "start_at, provider, parent", args,
		func(start time.Time) bool { return full && start.After(bound) },
		func(b seriesBlob) error {
			for _, p := range b.points {
				r := RRInterval{Timestamp: p.Time(), RRMs: p.V[series.HeartbeatRR], SeriesID: b.parent,
					Provider: b.provider, IngestedAt: b.ingested}
				if keep(r) && (!full || !r.Timestamp.After(bound)) {
					cands = append(cands, r)
				}
			}
			if t, ok := trim(); ok && (!full || t.Before(bound)) {
				bound, full = t, true
			}
			return nil
		})
	if err != nil {
		return Page[RRInterval]{}, err
	}
	trim()
	out := Page[RRInterval]{Records: cands}
	if len(cands) > n.Limit {
		out.Records = cands[:n.Limit]
		out.NextCursor = rrCursor(out.Records[n.Limit-1])
	}
	return out, nil
}

// legacyRR reads the RR intervals still stored as per-point rows that keep
// accepts, beat time at or after lower and before end (zero: open): at least
// the limit first of them in rrCompare order when there are that many, with
// every beat at the time of the last one read. upper is that time when more
// rows follow it (no row left out is before or at it), zero when every row
// was read.
func legacyRR(ctx context.Context, db Querier, tenantID, userID uuid.UUID, seriesID string, lower, end time.Time,
	limit int, keep func(RRInterval) bool) ([]RRInterval, time.Time, error) {
	for read := limit; ; read *= 2 {
		out, upper, err := legacyRROnce(ctx, db, tenantID, userID, seriesID, lower, end, read, keep)
		// keep leaves out the beats at the cursor's time that precede it, so
		// a read can come back short of limit: read further then.
		if err != nil || upper.IsZero() || len(out) >= limit {
			return out, upper, err
		}
	}
}

// legacyRROnce reads the rows up to the time of the read-th row in time
// order (every row when there are fewer), keeping those keep accepts.
func legacyRROnce(ctx context.Context, db Querier, tenantID, userID uuid.UUID, seriesID string, lower, end time.Time,
	read int, keep func(RRInterval) bool) ([]RRInterval, time.Time, error) {
	args := []any{tenantID, userID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := []string{"tenant_id = $1", "user_id = $2"}
	if seriesID != "" {
		where = append(where, "series_id = "+arg(seriesID))
	}
	if !lower.IsZero() {
		where = append(where, `"timestamp" >= `+arg(lower))
	}
	if !end.IsZero() {
		where = append(where, `"timestamp" < `+arg(end))
	}
	// The interval is sealed, so SQL cannot order the beats that share a
	// time: the time of the read-th row is found first, then every row up to
	// it is read and ordered by the caller.
	var upper time.Time
	err := db.QueryRow(ctx, `SELECT "timestamp" FROM ohs_data.heartbeat_samples WHERE `+strings.Join(where, " AND ")+
		` ORDER BY "timestamp" OFFSET `+strconv.Itoa(read-1)+` LIMIT 1`, args...).Scan(&upper)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		upper = time.Time{}
	case err != nil:
		return nil, time.Time{}, fmt.Errorf("read: heartbeat_samples: %w", err)
	default:
		upper = upper.UTC()
		where = append(where, `"timestamp" <= `+arg(upper))
	}
	rows, err := db.Query(ctx, `SELECT `+rrSel.List("")+`
		FROM ohs_data.heartbeat_samples WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read: heartbeat_samples: %w", err)
	}
	defer rows.Close()
	var out []RRInterval
	keys := keysFor(ctx, tenantID, userID)
	for rows.Next() {
		var r RRInterval
		if err := rrSel.Scan(ctx, keys, rows, []any{&r.Timestamp, &r.RRMs, &r.SeriesID, &r.Provider, &r.IngestedAt}); err != nil {
			return nil, time.Time{}, fmt.Errorf("read: heartbeat_samples: scan: %w", err)
		}
		utc(&r.Timestamp)
		utc(&r.IngestedAt)
		if keep(r) {
			out = append(out, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, fmt.Errorf("read: heartbeat_samples: %w", err)
	}
	return out, upper, nil
}

// HRVSeries is the HRV derived from one heartbeat series.
type HRVSeries struct {
	SeriesID  string    `json:"series_id"`
	Provider  string    `json:"provider"`
	StartDate time.Time `json:"start_date"`
	EndDate   time.Time `json:"end_date"`
	HRVStats
}

type seriesKey struct {
	provider, series string
	first            time.Time
	beats            int64
}

// HRV derives the HRV of each heartbeat series (see SummarizeBeats), ordered
// by the series' first beat, then provider and series id. A series is in the
// window when its first beat is; it is always summarized whole, beats after
// End included, so a series reads the same whatever the window.
func HRV(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q RRQuery) (Page[HRVSeries], error) {
	n, err := q.normalize()
	if err != nil {
		return Page[HRVSeries]{}, err
	}
	var afterT time.Time
	var afterKey []string
	if q.Cursor != "" {
		if afterT, afterKey, err = decodeKeyCursor(q.Cursor, 2); err != nil {
			return Page[HRVSeries]{}, err
		}
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[HRVSeries]{}, err
	}
	keys, more, err := hrvSeries(ctx, db, tenantID, userID, q.SeriesID, n, afterT, afterKey)
	if err != nil {
		return Page[HRVSeries]{}, err
	}
	out := Page[HRVSeries]{Records: []HRVSeries{}}
	if len(keys) == 0 {
		return out, nil
	}
	beats, err := seriesBeats(ctx, db, tenantID, userID, keys)
	if err != nil {
		return Page[HRVSeries]{}, err
	}
	for _, k := range keys {
		bs := beats[[2]string{k.provider, k.series}]
		if len(bs) == 0 {
			// Deleted between the two reads.
			continue
		}
		out.Records = append(out.Records, HRVSeries{SeriesID: k.series, Provider: k.provider,
			StartDate: bs[0].At, EndDate: bs[len(bs)-1].At, HRVStats: SummarizeBeats(bs)})
	}
	if more {
		last := keys[len(keys)-1]
		out.NextCursor = encodeKeyCursor(last.first, last.provider, last.series)
	}
	return out, nil
}

// hrvSeries lists the next page of series keys. more reports a next page.
func hrvSeries(ctx context.Context, db Querier, tenantID, userID uuid.UUID, seriesID string, n normalized,
	afterT time.Time, afterKey []string) ([]seriesKey, bool, error) {
	args := []any{tenantID, userID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	// lower is where the series may start: the window start or the cursor.
	// Beats before it are not grouped, and a series with any beat before it
	// started earlier and is left out.
	lower := n.Start
	if afterKey != nil && afterT.After(lower) {
		lower = afterT
	}
	where := []string{"tenant_id = $1", "user_id = $2"}
	if seriesID != "" {
		where = append(where, "series_id = "+arg(seriesID))
	}
	var outer []string
	if !lower.IsZero() {
		l := arg(lower)
		where = append(where, `"timestamp" >= `+l)
		outer = append(outer, `NOT EXISTS (SELECT 1 FROM ohs_data.heartbeat_samples h
			WHERE h.tenant_id = $1 AND h.user_id = $2 AND h.series_id = g.series_id
				AND h.provider = g.provider AND h."timestamp" < `+l+`)`)
	}
	if !n.End.IsZero() {
		where = append(where, `"timestamp" < `+arg(n.End))
	}
	if afterKey != nil {
		outer = append(outer, `(g.first, g.provider, g.series_id) > (`+arg(afterT)+`, `+arg(afterKey[0])+`, `+arg(afterKey[1])+`)`)
	}
	sql := `SELECT g.provider, g.series_id, g.first, g.beats FROM (
			SELECT provider, series_id, min("timestamp") AS first, count(*) AS beats
			FROM ohs_data.heartbeat_samples WHERE ` + strings.Join(where, " AND ") + `
			GROUP BY provider, series_id) g`
	if len(outer) > 0 {
		sql += " WHERE " + strings.Join(outer, " AND ")
	}
	// The series held by a blob, under the same window and cursor: the blob
	// keeps the first beat (start_at) and the beat count clear.
	blob := []string{"b.tenant_id = $1", "b.user_id = $2", "b.kind = " + strconv.Itoa(int(series.Heartbeat))}
	if seriesID != "" {
		blob = append(blob, "b.parent = "+arg(seriesID))
	}
	if !lower.IsZero() {
		blob = append(blob, "b.start_at >= "+arg(lower))
	}
	if !n.End.IsZero() {
		blob = append(blob, "b.start_at < "+arg(n.End))
	}
	if afterKey != nil {
		blob = append(blob, `(b.start_at, b.provider, b.parent) > (`+arg(afterT)+`, `+arg(afterKey[0])+`, `+arg(afterKey[1])+`)`)
	}
	sql = `SELECT provider, series_id, first, beats FROM (` + sql + `
		UNION ALL
		SELECT b.provider, b.parent, b.start_at, b.point_count::bigint FROM ohs_data.series_blobs b
		WHERE ` + strings.Join(blob, " AND ") + `) u
		ORDER BY first, provider, series_id LIMIT ` + arg(n.Limit+1)
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, fmt.Errorf("read: heartbeat series: %w", err)
	}
	defer rows.Close()
	var keys []seriesKey
	var total int64
	more := false
	for rows.Next() {
		var k seriesKey
		if err := rows.Scan(&k.provider, &k.series, &k.first, &k.beats); err != nil {
			return nil, false, fmt.Errorf("read: heartbeat series: scan: %w", err)
		}
		if len(keys) == n.Limit || (len(keys) > 0 && total+k.beats > MaxHRVBeats) {
			more = true
			break
		}
		total += k.beats
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("read: heartbeat series: %w", err)
	}
	return keys, more, nil
}

// seriesBeats reads every beat of the series in keys, by series and time.
func seriesBeats(ctx context.Context, db Querier, tenantID, userID uuid.UUID, keys []seriesKey) (map[[2]string][]Beat, error) {
	providers := make([]string, len(keys))
	ids := make([]string, len(keys))
	from := keys[0].first
	for i, k := range keys {
		providers[i], ids[i] = k.provider, k.series
		if k.first.Before(from) {
			from = k.first
		}
	}
	// No beat of these series is before the earliest first beat: the bound
	// lets TimescaleDB skip the older chunks.
	// Beats that share a time are ordered by their interval, which is sealed:
	// SQL orders by time and the ties are settled below, on the opened beats.
	out := make(map[[2]string][]Beat, len(keys))
	err := eachBlob(ctx, db, tenantID, userID, series.Heartbeat,
		"(provider, parent) IN (SELECT * FROM unnest($3::text[], $4::text[]))", "", []any{providers, ids}, nil,
		func(b seriesBlob) error {
			k := [2]string{b.provider, b.parent}
			for _, p := range b.points {
				out[k] = append(out[k], Beat{At: p.Time(), RRMs: p.V[series.HeartbeatRR]})
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT `+beatSel.List("")+` FROM ohs_data.heartbeat_samples
		WHERE tenant_id = $1 AND user_id = $2 AND "timestamp" >= $3
			AND (provider, series_id) IN (SELECT * FROM unnest($4::text[], $5::text[]))
		ORDER BY provider, series_id, "timestamp"`,
		tenantID, userID, from, providers, ids)
	if err != nil {
		return nil, fmt.Errorf("read: heartbeat beats: %w", err)
	}
	defer rows.Close()
	rd := keysFor(ctx, tenantID, userID)
	for rows.Next() {
		var p, s string
		var b Beat
		if err := beatSel.Scan(ctx, rd, rows, []any{&p, &s, &b.At, &b.RRMs}); err != nil {
			return nil, fmt.Errorf("read: heartbeat beats: scan: %w", err)
		}
		b.At = b.At.UTC()
		k := [2]string{p, s}
		out[k] = append(out[k], b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: heartbeat beats: %w", err)
	}
	for _, beats := range out {
		slices.SortStableFunc(beats, func(a, b Beat) int {
			return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.RRMs, b.RRMs))
		})
	}
	return out, nil
}
