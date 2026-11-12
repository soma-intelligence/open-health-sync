// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Derived daily summaries.
//
// The read API serves daily summaries without TimescaleDB continuous
// aggregates, so the ETL writes them. A provider that ships its own daily
// summary keeps it. For every other day an object touches, the additive
// quantity types are summed per user, provider, source and LOCAL calendar day,
// the largest source total is kept, and it is
// upserted as one derived row (latest wins), recomputed from the stored
// samples so a later upload for the same day corrects the total.
//
// The local day comes from the sample's "timezone" metadata (an IANA name,
// the key connectors set), falling back to UTC. A day is the interval from
// local midnight to the next local midnight, so a daylight saving transition
// gives a 23 or 25 hour day.

// MetadataTimezone is the metadata key connectors set to the IANA time zone a
// sample was recorded in.
const MetadataTimezone = "timezone"

// derivedKind is the identity of the derived activity row of a day.
const derivedKind = "derived_activity"

// additiveTypes are the quantity types a daily sum is meaningful for.
var additiveTypes = []string{
	model.QTStepCount,
	model.QTActiveEnergyBurned,
	model.QTBasalEnergyBurned,
	model.QTTotalEnergyBurned,
	model.QTDistanceWalkingRunning,
	"distance_cycling",
	model.QTFlightsClimbed,
}

func isAdditive(qt string) bool {
	for _, a := range additiveTypes {
		if a == qt {
			return true
		}
	}
	return false
}

// zoneCache holds the zones sampleLocation has loaded, by name. LoadLocation
// reads and parses tzdata on every call and nearly every sample of a batch
// names the same zone. Only zones that load are kept, so the cache is bounded
// by the tz database, whatever names the metadata carries.
var zoneCache sync.Map // string -> *time.Location

// sampleLocation returns the time zone named in metadata, or UTC. "Local" (the
// server's own zone) names no zone of the user's and reads as UTC too.
func sampleLocation(md map[string]any) *time.Location {
	if name, ok := md[MetadataTimezone].(string); ok && name != "" && name != "Local" {
		if loc, ok := zoneCache.Load(name); ok {
			return loc.(*time.Location)
		}
		if loc, err := time.LoadLocation(name); err == nil {
			zoneCache.Store(name, loc)
			return loc
		}
	}
	return time.UTC
}

// localDate is the calendar day of t in the zone named by md, as midnight UTC.
func localDate(t time.Time, md map[string]any) time.Time {
	return dateOnly(t.In(sampleLocation(md)))
}

// dayKey is one (provider, local calendar day) a job touched.
type dayKey struct {
	provider string
	day      time.Time // calendar date as midnight UTC
}

// touchedDays collects the days the batch touches: the local day of every
// additive sample (with the first zone seen for that day) and the day of every
// provider summary, whose presence suppresses the derived row.
func touchedDays(sc scope, acc *model.MapResult) map[dayKey]*time.Location {
	days := map[dayKey]*time.Location{}
	for _, s := range acc.QuantitySamples {
		if !isAdditive(s.QuantityType) {
			continue
		}
		loc := sampleLocation(s.Metadata)
		k := dayKey{provider: sc.providerOr(s.Provider), day: dateOnly(s.StartDate.In(loc))}
		if _, ok := days[k]; !ok {
			days[k] = loc
		}
	}
	for _, d := range acc.DailySummaries {
		k := dayKey{provider: sc.providerOr(d.Provider), day: dateOnly(d.SummaryDate)}
		if _, ok := days[k]; !ok {
			days[k] = time.UTC
		}
	}
	return days
}

// derivedHash is the content hash of the derived row of one day. It leaves the
// values out, so a recompute hits the same row and replaces it.
func derivedHash(sc scope, provider string, day time.Time) string {
	return model.ComputeContentHash(map[string]string{
		"tenant_id":    sc.tenant.String(),
		"user_id":      sc.user.String(),
		"provider":     provider,
		"summary_date": day.Format("2006-01-02"),
		"kind":         derivedKind,
	})
}

// derivedOwn returns the positions ($3) of the days ($4, $5: provider, day)
// that have a provider summary of their own: a daily_summaries row of that
// provider and day under another content hash than the derived row's ($6).
const derivedOwn = `SELECT d.i
	FROM unnest($3::int[], $4::text[], $5::date[], $6::text[]) AS d(i, provider, day, hash)
	WHERE EXISTS (SELECT 1 FROM ohs_data.daily_summaries s
		WHERE s.tenant_id = $1 AND s.user_id = $2 AND s.provider = d.provider
		  AND s.summary_date = d.day AND s.content_hash <> d.hash)`

// derivedDrop deletes the derived rows of the days in $3 (content hash) and
// $4 (day).
const derivedDrop = `DELETE FROM ohs_data.daily_summaries s
	USING unnest($3::text[], $4::date[]) AS d(hash, day)
	WHERE s.tenant_id = $1 AND s.user_id = $2 AND s.content_hash = d.hash AND s.summary_date = d.day`

// derivedSums computes, for each day in $3 to $6 (position, provider, start
// and end of the local day), the daily value of each additive type ($7) of
// that provider over samples whose start falls in [lo, hi): the per-source
// sum, then the largest of those. Sources inside one provider overlap (a phone
// and a watch both count the same steps), so adding them would double count.
// The sum is exact (numeric): a float sum depends on the order Postgres reads
// the rows, so a recompute of unchanged samples could differ in the last bit
// and rewrite the row. $8 and $9 bound every day of the statement, so chunks
// outside them are excluded when it is planned.
const derivedSums = `SELECT i, quantity_type, max(total) FROM (
		SELECT d.i, q.quantity_type,
		       COALESCE(NULLIF(q.source_bundle_id, ''), NULLIF(q.source_name, ''), NULLIF(q.device_id, ''), '') AS source,
		       sum(q.value::numeric)::double precision AS total
		FROM unnest($3::int[], $4::text[], $5::timestamptz[], $6::timestamptz[]) AS d(i, provider, lo, hi)
		JOIN ohs_data.quantity_samples q
		  ON q.provider = d.provider AND q.start_date >= d.lo AND q.start_date < d.hi
		WHERE q.tenant_id = $1 AND q.user_id = $2 AND q.quantity_type = ANY($7)
		  AND q.start_date >= $8 AND q.start_date < $9
		GROUP BY 1, 2, 3
	) per_source
	GROUP BY i, quantity_type`

// derivedUpsertTail is the conflict clause of the derived rows: the row of a
// day is replaced when any figure or its metadata changed.
var derivedUpsertTail = `
	ON CONFLICT (tenant_id, user_id, content_hash, summary_date) DO UPDATE SET ` + derivedSet() + `
	WHERE (` + derivedCols("daily_summaries.") + `) IS DISTINCT FROM (` + derivedCols("EXCLUDED.") + `)`

// derivedBatch bounds the rows of one derived upsert, far under the 65535
// parameter ceiling.
const derivedBatch = 500

func derivedSet() string {
	set := make([]string, 0, len(dailyFigures)+1)
	for _, f := range append(append([]string{}, dailyFigures...), "metadata") {
		set = append(set, f+" = EXCLUDED."+f)
	}
	return strings.Join(set, ", ")
}

func derivedCols(prefix string) string {
	cols := make([]string, 0, len(dailyFigures)+1)
	for _, f := range append(append([]string{}, dailyFigures...), "metadata") {
		cols = append(cols, prefix+f)
	}
	return strings.Join(cols, ", ")
}

// sortedDays returns the keys of days by provider, then day.
func sortedDays(days map[dayKey]*time.Location) []dayKey {
	keys := make([]dayKey, 0, len(days))
	for k := range days {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].provider != keys[j].provider {
			return keys[i].provider < keys[j].provider
		}
		return keys[i].day.Before(keys[j].day)
	})
	return keys
}

// deriveDailySummaries recomputes the derived row of every touched day from
// the stored samples, inside the job's transaction (so it sees what the job
// just wrote). A day with a provider summary of its own gets no derived row,
// and loses one written earlier, as does a day left without any additive
// sample (every one of them deleted).
//
// The work is done per set of days, not per day: one lookup of the days that
// have a provider summary, one sum over the others, one delete and one upsert
// per derivedBatch rows, whatever the number of days an object touches.
func deriveDailySummaries(ctx context.Context, tx TxLike, sc scope, days map[dayKey]*time.Location) (written, skipped int, err error) {
	keys := sortedDays(days)
	if len(keys) == 0 {
		return 0, 0, nil
	}

	// Serialize derivations of one (tenant, user, provider): a concurrent job
	// writing samples for the same day either commits before this sum (and is
	// seen) or sums after this commit (and sees these rows). Without it two
	// jobs each sum without the other's rows and the last upsert loses data.
	// Keys are sorted, so locks are taken in a stable order.
	locked := map[string]bool{}
	for _, k := range keys {
		if locked[k.provider] {
			continue
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			"ohs_daily|"+sc.tenant.String()+"|"+sc.user.String()+"|"+k.provider); err != nil {
			return 0, 0, fmt.Errorf("lock daily derivation: %w", err)
		}
		locked[k.provider] = true
	}

	idx := make([]int32, len(keys))
	providers := make([]string, len(keys))
	dates := make([]time.Time, len(keys))
	hashes := make([]string, len(keys))
	for i, k := range keys {
		idx[i], providers[i], dates[i], hashes[i] = int32(i), k.provider, k.day, derivedHash(sc, k.provider, k.day)
	}
	own, err := queryPositions(ctx, tx, derivedOwn, sc.tenant, sc.user, idx, providers, dates, hashes)
	if err != nil {
		return 0, 0, fmt.Errorf("look up provider summary: %w", err)
	}

	// The days left to sum, with the bounds of each local day.
	var sumIdx []int32
	var sumProviders []string
	var los, his []time.Time
	var all window
	for i, k := range keys {
		if own[i] {
			continue
		}
		loc := days[k]
		lo := time.Date(k.day.Year(), k.day.Month(), k.day.Day(), 0, 0, 0, 0, loc)
		hi := lo.AddDate(0, 0, 1)
		sumIdx, sumProviders, los, his = append(sumIdx, int32(i)), append(sumProviders, k.provider), append(los, lo), append(his, hi)
		all.extend(lo)
		all.extend(hi)
	}
	sums := map[int]map[string]float64{}
	if len(sumIdx) > 0 {
		if sums, err = sumAdditive(ctx, tx, sc, sumIdx, sumProviders, los, his, all); err != nil {
			return 0, 0, err
		}
	}

	// A day with a provider summary, or without any additive sample left,
	// loses its derived row; every other day gets one.
	var dropHashes []string
	var dropDays []time.Time
	rows := make([][]any, 0, len(sumIdx))
	for i, k := range keys {
		if own[i] || len(sums[i]) == 0 {
			dropHashes, dropDays = append(dropHashes, hashes[i]), append(dropDays, k.day)
			continue
		}
		rows = append(rows, derivedRow(sc, k, days[k], hashes[i], sums[i]))
	}
	if len(dropHashes) > 0 {
		if _, err := tx.Exec(ctx, derivedDrop, sc.tenant, sc.user, dropHashes, dropDays); err != nil {
			return 0, 0, fmt.Errorf("drop derived summary: %w", err)
		}
	}
	head := insertHead(tDaily, dailyColumns)
	for i := 0; i < len(rows); i += derivedBatch {
		batch := rows[i:min(i+derivedBatch, len(rows))]
		args := make([]any, 0, len(batch)*len(dailyColumns))
		phs := make([]string, 0, len(batch))
		for j, r := range batch {
			phs = append(phs, placeholders(j*len(dailyColumns)+1, len(dailyColumns)))
			args = append(args, r...)
		}
		tag, err := tx.Exec(ctx, head+strings.Join(phs, ", ")+derivedUpsertTail, args...)
		if err != nil {
			return written, skipped, fmt.Errorf("upsert derived summary: %w", err)
		}
		n := int(tag.RowsAffected())
		written, skipped = written+n, skipped+len(batch)-n
	}
	return written, skipped, nil
}

// sumAdditive runs derivedSums for the days at positions idx and returns the
// daily value of each additive type, by position. A day without any additive
// sample has no entry.
func sumAdditive(ctx context.Context, tx TxLike, sc scope, idx []int32, providers []string, los, his []time.Time, all window) (map[int]map[string]float64, error) {
	rows, err := tx.Query(ctx, derivedSums, sc.tenant, sc.user, idx, providers, los, his, additiveTypes, all.lo, all.hi)
	if err != nil {
		return nil, fmt.Errorf("sum daily samples: %w", err)
	}
	defer rows.Close()
	sums := map[int]map[string]float64{}
	for rows.Next() {
		var i int32
		var qt string
		var v float64
		if err := rows.Scan(&i, &qt, &v); err != nil {
			return nil, fmt.Errorf("scan daily sum: %w", err)
		}
		day, ok := sums[int(i)]
		if !ok {
			day = map[string]float64{}
			sums[int(i)] = day
		}
		day[qt] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sum daily samples: %w", err)
	}
	return sums, nil
}

// derivedRow builds the daily_summaries row of one derived day, in
// dailyColumns order. Figures without samples stay NULL.
func derivedRow(sc scope, k dayKey, loc *time.Location, hash string, sums map[string]float64) []any {
	f := func(qt string) *float64 {
		if v, ok := sums[qt]; ok {
			return &v
		}
		return nil
	}
	i := func(qt string) *int {
		if v, ok := sums[qt]; ok {
			n := int(math.Round(v))
			return &n
		}
		return nil
	}
	active, basal := f(model.QTActiveEnergyBurned), f(model.QTBasalEnergyBurned)
	// A source's own total wins; otherwise basal plus active energy, when
	// the basal part is known.
	total := f(model.QTTotalEnergyBurned)
	if total == nil && basal != nil {
		t := *basal
		if active != nil {
			t += *active
		}
		total = &t
	}
	md := map[string]any{"derived": true, MetadataTimezone: loc.String()}
	if v := f(model.QTBasalEnergyBurned); v != nil {
		md["basal_kcal"] = *v
	}
	if v := f("distance_cycling"); v != nil {
		md["distance_cycling_m"] = *v
	}
	return []any{
		sc.tenant, sc.user, k.provider, sourceSlugOrProvider("", k.provider), k.day,
		nil, nil, nil, nil, nil, // readiness, activity, sleep, recovery, strain
		i(model.QTStepCount), active, total, f(model.QTDistanceWalkingRunning), i(model.QTFlightsClimbed),
		nil, nil, // resting_hr, hrv_avg_ms
		hash, md,
		nil, // captured_at: a derived row comes from no one object
	}
}
