// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
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

// RRInterval is one beat-to-beat interval of a heartbeat series.
type RRInterval struct {
	Timestamp  time.Time `json:"timestamp"`
	RRMs       float64   `json:"rr_ms"`
	SeriesID   string    `json:"series_id"`
	Provider   string    `json:"provider"`
	IngestedAt time.Time `json:"ingested_at"`
}

// RRIntervals lists the stored RR intervals by beat time. Beats that share a
// time are ordered by their content hash, which the cursor carries.
func RRIntervals(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q RRQuery) (Page[RRInterval], error) {
	n, err := q.normalize()
	if err != nil {
		return Page[RRInterval]{}, err
	}
	var afterT time.Time
	var afterKey []string
	if q.Cursor != "" {
		if afterT, afterKey, err = decodeKeyCursor(q.Cursor, 1); err != nil {
			return Page[RRInterval]{}, err
		}
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[RRInterval]{}, err
	}
	args := []any{tenantID, userID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := []string{"tenant_id = $1", "user_id = $2"}
	if q.SeriesID != "" {
		where = append(where, "series_id = "+arg(q.SeriesID))
	}
	if !n.Start.IsZero() {
		where = append(where, `"timestamp" >= `+arg(n.Start))
	}
	if !n.End.IsZero() {
		where = append(where, `"timestamp" < `+arg(n.End))
	}
	if afterKey != nil {
		// The scalar bound lets TimescaleDB exclude chunks before the cursor.
		at := arg(afterT)
		where = append(where, `"timestamp" >= `+at, `("timestamp", content_hash) > (`+at+`, `+arg(afterKey[0])+`)`)
	}
	rows, err := db.Query(ctx, `SELECT "timestamp", rr_ms, series_id, provider, ingested_at, content_hash
		FROM ohs_data.heartbeat_samples WHERE `+strings.Join(where, " AND ")+
		` ORDER BY "timestamp", content_hash LIMIT `+arg(n.Limit+1), args...)
	if err != nil {
		return Page[RRInterval]{}, fmt.Errorf("read: heartbeat_samples: %w", err)
	}
	defer rows.Close()
	out := Page[RRInterval]{Records: []RRInterval{}}
	var lastHash string
	for rows.Next() {
		if len(out.Records) == n.Limit {
			out.NextCursor = encodeKeyCursor(out.Records[len(out.Records)-1].Timestamp, lastHash)
			break
		}
		var r RRInterval
		if err := rows.Scan(&r.Timestamp, &r.RRMs, &r.SeriesID, &r.Provider, &r.IngestedAt, &lastHash); err != nil {
			return Page[RRInterval]{}, fmt.Errorf("read: heartbeat_samples: scan: %w", err)
		}
		utc(&r.Timestamp)
		utc(&r.IngestedAt)
		out.Records = append(out.Records, r)
	}
	if err := rows.Err(); err != nil {
		return Page[RRInterval]{}, fmt.Errorf("read: heartbeat_samples: %w", err)
	}
	return out, nil
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
	sql += " ORDER BY g.first, g.provider, g.series_id LIMIT " + arg(n.Limit+1)
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
	series := make([]string, len(keys))
	from := keys[0].first
	for i, k := range keys {
		providers[i], series[i] = k.provider, k.series
		if k.first.Before(from) {
			from = k.first
		}
	}
	// No beat of these series is before the earliest first beat: the bound
	// lets TimescaleDB skip the older chunks.
	rows, err := db.Query(ctx, `SELECT provider, series_id, "timestamp", rr_ms FROM ohs_data.heartbeat_samples
		WHERE tenant_id = $1 AND user_id = $2 AND "timestamp" >= $3
			AND (provider, series_id) IN (SELECT * FROM unnest($4::text[], $5::text[]))
		ORDER BY provider, series_id, "timestamp", rr_ms`,
		tenantID, userID, from, providers, series)
	if err != nil {
		return nil, fmt.Errorf("read: heartbeat beats: %w", err)
	}
	defer rows.Close()
	out := make(map[[2]string][]Beat, len(keys))
	for rows.Next() {
		var p, s string
		var b Beat
		if err := rows.Scan(&p, &s, &b.At, &b.RRMs); err != nil {
			return nil, fmt.Errorf("read: heartbeat beats: scan: %w", err)
		}
		b.At = b.At.UTC()
		k := [2]string{p, s}
		out[k] = append(out[k], b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: heartbeat beats: %w", err)
	}
	return out, nil
}
