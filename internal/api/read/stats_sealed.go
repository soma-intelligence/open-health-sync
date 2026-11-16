// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// errHourlyIncomplete means the hourly statistics cannot answer the range:
// a sealed bucket names a source no sample of its hour carries any more (its
// source_key was computed under an older index key), or an hour holds sealed
// samples and has no sealed bucket (the continuous aggregate then counts
// values it cannot see). The statistics are computed from the samples
// instead.
var errHourlyIncomplete = errors.New("read: stats: the hourly statistics do not cover the range")

var (
	// hourSel reads a sealed hourly bucket: its group keys are clear, its
	// statistics sealed.
	hourSel = vault.SelectSealedOnly("quantity_hourly_sealed", `bucket, provider, source_key, slug, worn, unit, n, sum, min, max`)
	// statSampleSel reads what the statistics take from a sample: its
	// value and the columns that name its stream.
	statSampleSel = vault.Select("quantity_samples", `start_date, provider, source_slug, unit, value, source_name,
		source_bundle_id, device_id, device_model, product_type`)
	// sourceSel reads the columns a sample's source string is made of.
	sourceSel = vault.Select("quantity_samples", `source_name, source_bundle_id, device_id`)
)

// sealedStats is SampleStats under a vault: the rows are selected on clear
// columns, opened, and folded in Go (stats_fold.go).
func sealedStats(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q StatsQuery,
	start, end time.Time, step time.Duration, loc *time.Location) (Stats, error) {
	keys := keysFor(ctx, tenantID, userID)
	args := []any{tenantID, userID, q.Type, start, end}
	provider := ""
	if q.Provider != "" {
		args = append(args, q.Provider)
		provider = " AND provider = $" + strconv.Itoa(len(args))
	}

	hourly := false
	if wholeHourOffsets(start, end, loc) {
		// The hours with rows not converted yet are read from the continuous
		// aggregate; without it (no TimescaleDB) the samples are read.
		if err := db.QueryRow(ctx, `SELECT to_regclass('ohs_data.quantity_hourly') IS NOT NULL`).Scan(&hourly); err != nil {
			return Stats{}, fmt.Errorf("read: stats: %w", err)
		}
	}
	var buckets []StatsBucket
	var err error
	if hourly {
		buckets, err = foldHours(hourRows(ctx, db, keys, args, provider), step, loc)
		if errors.Is(err, errHourlyIncomplete) {
			hourly = false
		} else if err != nil {
			return Stats{}, err
		}
	}
	if !hourly {
		if buckets, err = foldSamples(sampleRows(ctx, db, keys, args, provider), step, loc); err != nil {
			return Stats{}, err
		}
	}

	view := ViewMerged
	var order []string
	if q.View == ViewAll {
		view = ViewAll
	} else {
		p, err := policy(ctx, db, tenantID, userID, Query{Policy: q.Policy})
		if err != nil {
			return Stats{}, err
		}
		order = p.Order(q.Type)
	}
	rank, err := textOrder(ctx, db, buckets, view)
	if err != nil {
		return Stats{}, err
	}
	s := Stats{Type: q.Type, Interval: q.Interval, TimeZone: loc.String(), View: view,
		Start: start, End: end, Buckets: statsView(buckets, view, order, rank)}
	if s.Buckets == nil {
		s.Buckets = []StatsBucket{}
	}
	if len(s.Buckets) > MaxStatsRows {
		s.Buckets, s.Truncated = s.Buckets[:MaxStatsRows], true
	}
	for i := range s.Buckets {
		b := &s.Buckets[i]
		if b.Count > 0 {
			b.Avg = b.Sum / float64(b.Count)
		}
		b.slug, b.worn, b.key, b.valued = "", 0, "", false
	}
	return s, nil
}

// textOrder returns the rank of each text the view orders by (provider,
// source and unit in the all view, stream keys in the merged one) in the
// database's order of text, as the SQL path compared them.
func textOrder(ctx context.Context, db Querier, buckets []StatsBucket, view View) (func(string) int, error) {
	seen := map[string]int{}
	for _, b := range buckets {
		if view == ViewAll {
			seen[b.Provider], seen[b.Source] = 0, 0
			if b.Unit != nil {
				seen[*b.Unit] = 0
			}
		} else {
			seen[b.key] = 0
		}
	}
	if len(seen) > 1 {
		all := make([]string, 0, len(seen))
		for k := range seen {
			all = append(all, k)
		}
		var sorted []string
		if err := db.QueryRow(ctx, "SELECT array_agg(k ORDER BY k) FROM unnest($1::text[]) k", all).Scan(&sorted); err != nil {
			return nil, fmt.Errorf("read: stats: order: %w", err)
		}
		for i, k := range sorted {
			seen[k] = i
		}
	}
	return func(s string) int { return seen[s] }, nil
}

// streamKeyOf is the key that orders two streams of one source: the keyed
// source of a sealed row, as the merged reads order them (merge.go), and the
// source string of a clear one.
func streamKeyOf(provider, source string, sourceKey []byte, sealed bool) string {
	if sealed {
		return provider + "|" + hex.EncodeToString(sourceKey)
	}
	return provider + "|" + source
}

// hourRows yields the hourly statistics of the range: the sealed buckets,
// opened, then the rows of the continuous aggregate for the hours that have
// no sealed bucket (hours whose samples are all still clear). args are the
// tenant, the user, the type, the range, then the provider when provider
// (a condition) filters on one.
func hourRows(ctx context.Context, db Querier, keys *vault.Reader, args []any, provider string) iter.Seq2[HourRow, error] {
	return func(yield func(HourRow, error) bool) {
		sources, err := hourSources(ctx, db, keys, args, provider)
		if err != nil {
			yield(HourRow{}, err)
			return
		}
		const where = `tenant_id = $1 AND user_id = $2 AND quantity_type = $3 AND bucket >= $4 AND bucket < $5`
		rows, err := db.Query(ctx, `SELECT `+hourSel.List("")+` FROM ohs_data.quantity_hourly_sealed
			WHERE `+where+provider+` ORDER BY bucket`, args...)
		if err != nil {
			yield(HourRow{}, fmt.Errorf("read: stats: %w", err))
			return
		}
		for rows.Next() {
			var r HourRow
			var sourceKey []byte
			var worn bool
			var unit string
			var sum, low, high *float64
			if err := hourSel.Scan(ctx, keys, rows, []any{&r.Bucket, &r.Provider, &sourceKey, &r.Slug, &worn, &unit,
				&r.N, &sum, &low, &high}); err != nil {
				rows.Close()
				yield(HourRow{}, fmt.Errorf("read: stats: scan: %w", err))
				return
			}
			r.Source = sources[r.Provider+"|"+string(sourceKey)]
			r.StreamKey = streamKeyOf(r.Provider, r.Source, sourceKey, true)
			if !worn {
				r.Worn = 1
			}
			// The sealed table keeps a missing unit as ''.
			if unit != "" {
				r.Unit = &unit
			}
			if sum != nil && low != nil && high != nil {
				r.HasValue, r.Sum, r.Min, r.Max = true, *sum, *low, *high
			}
			if !yield(r, nil) {
				rows.Close()
				return
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			yield(HourRow{}, fmt.Errorf("read: stats: %w", err))
			return
		}

		rows, err = db.Query(ctx, `SELECT bucket, provider, source, slug, worn, unit, n, total, low, high
			FROM ohs_data.quantity_hourly h WHERE `+where+provider+`
			  AND NOT EXISTS (SELECT 1 FROM ohs_data.quantity_hourly_sealed s
				WHERE s.tenant_id = h.tenant_id AND s.user_id = h.user_id AND s.quantity_type = h.quantity_type
				  AND s.bucket = h.bucket)
			ORDER BY bucket`, args...)
		if err != nil {
			yield(HourRow{}, fmt.Errorf("read: stats: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var r HourRow
			var sum, low, high *float64
			if err := rows.Scan(&r.Bucket, &r.Provider, &r.Source, &r.Slug, &r.Worn, &r.Unit, &r.N, &sum, &low, &high); err != nil {
				yield(HourRow{}, fmt.Errorf("read: stats: scan: %w", err))
				return
			}
			r.StreamKey = streamKeyOf(r.Provider, r.Source, nil, false)
			if sum == nil || low == nil || high == nil {
				// Samples with no clear value and no sealed bucket.
				yield(HourRow{}, errHourlyIncomplete)
				return
			}
			r.HasValue, r.Sum, r.Min, r.Max = true, *sum, *low, *high
			if !yield(r, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(HourRow{}, fmt.Errorf("read: stats: %w", err))
		}
	}
}

// hourSources resolves the source string of every stream of the sealed
// buckets of the range, keyed by provider and source_key: the bucket keeps
// only the keyed hash of the source, so one sample of the stream, from one
// of its hours, is opened to read it. It is errHourlyIncomplete when a
// stream has no such sample.
func hourSources(ctx context.Context, db Querier, keys *vault.Reader, args []any, provider string) (map[string]string, error) {
	rows, err := db.Query(ctx, `SELECT DISTINCT ON (provider, source_key) provider, source_key, bucket
		FROM ohs_data.quantity_hourly_sealed
		WHERE tenant_id = $1 AND user_id = $2 AND quantity_type = $3 AND bucket >= $4 AND bucket < $5`+provider+`
		ORDER BY provider, source_key, bucket`, args...)
	if err != nil {
		return nil, fmt.Errorf("read: stats: sources: %w", err)
	}
	var providers []string
	var sourceKeys [][]byte
	var buckets []time.Time
	for rows.Next() {
		var p string
		var k []byte
		var b time.Time
		if err := rows.Scan(&p, &k, &b); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read: stats: sources: %w", err)
		}
		providers, sourceKeys, buckets = append(providers, p), append(sourceKeys, k), append(buckets, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: stats: sources: %w", err)
	}
	out := make(map[string]string, len(providers))
	if len(providers) == 0 {
		return out, nil
	}
	rows, err = db.Query(ctx, `SELECT `+sourceSel.List("s")+`, k.i
		FROM unnest($4::text[], $5::bytea[], $6::timestamptz[]) WITH ORDINALITY AS k(provider, source_key, bucket, i)
		CROSS JOIN LATERAL (
			SELECT * FROM ohs_data.quantity_samples q
			WHERE q.tenant_id = $1 AND q.user_id = $2 AND q.quantity_type = $3 AND q.provider = k.provider
			  AND q.source_key = k.source_key
			  AND q.start_date >= k.bucket AND q.start_date < k.bucket + INTERVAL '1 hour'
			LIMIT 1) s`, args[0], args[1], args[2], providers, sourceKeys, buckets)
	if err != nil {
		return nil, fmt.Errorf("read: stats: sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, bundle, device *string
		var i int64
		if err := sourceSel.Scan(ctx, keys, rows, []any{&name, &bundle, &device}, &i); err != nil {
			return nil, fmt.Errorf("read: stats: sources: scan: %w", err)
		}
		out[providers[i-1]+"|"+string(sourceKeys[i-1])] = firstText(bundle, name, device)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: stats: sources: %w", err)
	}
	if len(out) != len(providers) {
		return nil, errHourlyIncomplete
	}
	return out, nil
}

// firstText is the first of vals that is not NULL or empty, else "": the
// source of a sample is its bundle, else its name, else its device.
func firstText(vals ...*string) string {
	for _, v := range vals {
		if v != nil && *v != "" {
			return *v
		}
	}
	return ""
}

// namesWatch is the watch test of the merged view and of quantity_hourly:
// one of the texts holds "watch", in any case.
func namesWatch(vals ...*string) bool {
	for _, v := range vals {
		if v != nil && strings.Contains(strings.ToLower(*v), "watch") {
			return true
		}
	}
	return false
}

// sampleRows yields the samples of the range, opened, by start then id.
// args and provider are those of hourRows.
func sampleRows(ctx context.Context, db Querier, keys *vault.Reader, args []any, provider string) iter.Seq2[SampleRow, error] {
	return func(yield func(SampleRow, error) bool) {
		rows, err := db.Query(ctx, `SELECT `+statSampleSel.List("")+`, source_key, sealed IS NOT NULL
			FROM ohs_data.quantity_samples
			WHERE tenant_id = $1 AND user_id = $2 AND quantity_type = $3 AND start_date >= $4 AND start_date < $5`+provider+`
			ORDER BY start_date, id`, args...)
		if err != nil {
			yield(SampleRow{}, fmt.Errorf("read: stats: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var r SampleRow
			var slug, name, bundle, device, model, product *string
			var sourceKey []byte
			var sealed bool
			if err := statSampleSel.Scan(ctx, keys, rows, []any{&r.Start, &r.Provider, &slug, &r.Unit, &r.Value, &name,
				&bundle, &device, &model, &product}, &sourceKey, &sealed); err != nil {
				yield(SampleRow{}, fmt.Errorf("read: stats: scan: %w", err))
				return
			}
			r.Slug = r.Provider
			if slug != nil {
				r.Slug = *slug
			}
			r.Source = firstText(bundle, name, device)
			if !namesWatch(product, model, name) {
				r.Worn = 1
			}
			r.StreamKey = streamKeyOf(r.Provider, r.Source, sourceKey, sealed && sourceKey != nil)
			if !yield(r, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(SampleRow{}, fmt.Errorf("read: stats: %w", err))
		}
	}
}
