// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
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
// the key connectors set), then from the user's time zone (end_users.timezone,
// set through the API or learnt from device uploads), then UTC. A day is the
// interval from local midnight to the next local midnight, so a daylight
// saving transition gives a 23 or 25 hour day. The zone a sample's day was
// attributed in is stored in its tz column, so a later change of the user's
// zone moves no stored sample to another day.

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

// zoneCache holds the zones loadZone has loaded, by name. LoadLocation reads
// and parses tzdata on every call and nearly every sample of a batch names the
// same zone. Only zones that load are kept, so the cache is bounded by the tz
// database, whatever names the metadata carries.
var zoneCache sync.Map // string -> *time.Location

// loadZone returns the IANA zone called name. "" and "Local" (the server's
// own zone) name no zone of the user's.
func loadZone(name string) (*time.Location, bool) {
	if name == "" || name == "Local" {
		return nil, false
	}
	if loc, ok := zoneCache.Load(name); ok {
		return loc.(*time.Location), true
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, false
	}
	zoneCache.Store(name, loc)
	return loc, true
}

// ownZone returns the zone a record's metadata names, when it names one that
// loads.
func ownZone(md map[string]any) (*time.Location, bool) {
	name, _ := md[MetadataTimezone].(string)
	return loadZone(name)
}

// sampleLocation returns the time zone named in metadata, or UTC. "Local" (the
// server's own zone) names no zone of the user's and reads as UTC too. It is
// the zone of a stored row, whose tz column already holds the zone its day
// was attributed in; a record being written goes through scope.location.
func sampleLocation(md map[string]any) *time.Location {
	if loc, ok := ownZone(md); ok {
		return loc
	}
	return time.UTC
}

// location is the zone a record's local day is attributed in: the zone its
// metadata names, else the user's zone (scope.zone), else UTC.
func (s scope) location(md map[string]any) *time.Location {
	if loc, ok := ownZone(md); ok {
		return loc
	}
	if s.zone != nil {
		return s.zone
	}
	return time.UTC
}

// tzColumn is the tz column of a written row: the zone its metadata names
// when that zone loads; else the zone a stored row already holds (a row keeps
// the zone its day was attributed in); else the user's zone; else what the
// metadata says, as before there was a user zone. It is the name of the zone
// scope.location attributes the record's day in, so a deletion that reads tz
// back finds the same day.
func (s scope) tzColumn(md, stored any) any {
	own := metaText(md, MetadataTimezone)
	if name, _ := own.(string); name != "" {
		if _, ok := loadZone(name); ok {
			return own
		}
	}
	if st := textOf(stored); st != "" {
		return st
	}
	if s.zone != nil {
		return s.zone.String()
	}
	return own
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
// provider summary, whose presence suppresses the derived row. The local day
// of a sample is its day in scope.location: its own zone, else the user's,
// else UTC.
func touchedDays(sc scope, acc *model.MapResult) map[dayKey]*time.Location {
	days := map[dayKey]*time.Location{}
	for _, s := range acc.QuantitySamples {
		if !isAdditive(s.QuantityType) {
			continue
		}
		loc := sc.location(s.Metadata)
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

// derivedHash is the content hash of the derived row of one day (keyed with
// keys). It leaves the values out, so a recompute hits the same row and
// replaces it.
func derivedHash(sc scope, provider string, day time.Time) string {
	return sc.hash(derivedPlainHash(sc, provider, day))
}

// derivedPlainHash is derivedHash without keys, the hash of a derived row
// stored clear.
func derivedPlainHash(sc scope, provider string, day time.Time) string {
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

// derivedSamples selects, for each day in $3 to $6 (position, provider, start
// and end of the local day), the additive samples ($7) of that provider whose
// start falls in [lo, hi), with the position of the day: the identity, the
// sealed value and the clear columns a row not converted yet holds. The
// values may be sealed, so SQL only selects and Go sums (sumAdditive). $8 and
// $9 bound every day of the statement, so chunks outside them are excluded
// when it is planned.
const derivedSamples = `SELECT d.i, q.quantity_type, q.content_hash, q.start_date, q.sealed, q.value,
		q.source_bundle_id, q.source_name, q.device_id
	FROM unnest($3::int[], $4::text[], $5::timestamptz[], $6::timestamptz[]) AS d(i, provider, lo, hi)
	JOIN ohs_data.quantity_samples q
	  ON q.provider = d.provider AND q.start_date >= d.lo AND q.start_date < d.hi
	WHERE q.tenant_id = $1 AND q.user_id = $2 AND q.quantity_type = ANY($7)
	  AND q.start_date >= $8 AND q.start_date < $9`

// derivedLegacy picks the derived rows of the days in $3 (plain content hash)
// and $4 (day) that are still stored clear, for convertLegacy; $5 and $6
// bound the days.
const derivedLegacy = `r.summary_date BETWEEN $5 AND $6
	AND (r.content_hash, r.summary_date) IN (SELECT * FROM unnest($3::text[], $4::date[]))`

// derivedUpsertTail is the conflict clause of the derived rows written clear:
// the row of a day is replaced when any figure or its metadata changed.
// derivedUpsertTailSealed is the one of the rows written sealed: every figure
// and the metadata are in the sealed value, so the row is replaced when its
// content MAC differs.
var (
	derivedUpsertTail = `
	ON CONFLICT (tenant_id, user_id, content_hash, summary_date) DO UPDATE SET ` + derivedSet() + `
	WHERE (` + derivedCols("daily_summaries.") + `) IS DISTINCT FROM (` + derivedCols("EXCLUDED.") + `)`
	derivedUpsertTailSealed = `
	ON CONFLICT (tenant_id, user_id, content_hash, summary_date) DO UPDATE SET ` +
		strings.Join(dailyShape.sealedSet("EXCLUDED", nil), ", ") + `
	WHERE ` + dailyShape.sealedDistinct("daily_summaries", "EXCLUDED", nil)
)

// derivedBatch bounds the rows of one derived upsert, far under the 65535
// parameter ceiling.
const derivedBatch = 500

func derivedSet() string {
	set := make([]string, 0, len(dailyFigures)+1)
	for _, f := range append(append([]string{}, dailyFigures...), "metadata", "derived") {
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
// have a provider summary, one read of the samples of the others, one delete
// and one upsert per derivedBatch rows, whatever the number of days an object
// touches. The samples may be sealed, so the read streams them and Go sums
// them (sumAdditive); with keys the rows are written sealed.
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
	plain := make([]string, len(keys))
	var span window
	for i, k := range keys {
		plain[i] = derivedPlainHash(sc, k.provider, k.day)
		idx[i], providers[i], dates[i], hashes[i] = int32(i), k.provider, k.day, sc.hash(plain[i])
		span.extend(k.day)
	}
	// The derived rows stored clear are converted first, in one pass, or the
	// lookup below would take them for provider summaries.
	if err := convertLegacy(ctx, tx, sc, tDaily, derivedLegacy, plain, dates, span.lo, span.hi); err != nil {
		return 0, 0, err
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
		row, err := dailyShape.finish(sc, derivedRow(sc, k, days[k], hashes[i], sums[i]))
		if err != nil {
			return 0, 0, err
		}
		rows = append(rows, row)
	}
	if len(dropHashes) > 0 {
		if _, err := tx.Exec(ctx, derivedDrop, sc.tenant, sc.user, dropHashes, dropDays); err != nil {
			return 0, 0, fmt.Errorf("drop derived summary: %w", err)
		}
	}
	columns := dailyShape.columns(sc)
	head, tail := insertHead(tDaily, columns), derivedUpsertTail
	if sc.uv != nil {
		tail = derivedUpsertTailSealed
	}
	for i := 0; i < len(rows); i += derivedBatch {
		batch := rows[i:min(i+derivedBatch, len(rows))]
		args := make([]any, 0, len(batch)*len(columns))
		phs := make([]string, 0, len(batch))
		for j, r := range batch {
			phs = append(phs, placeholders(j*len(columns)+1, len(columns)))
			args = append(args, r...)
		}
		tag, err := tx.Exec(ctx, head+strings.Join(phs, ", ")+tail, args...)
		if err != nil {
			return written, skipped, fmt.Errorf("upsert derived summary: %w", err)
		}
		n := int(tag.RowsAffected())
		written, skipped = written+n, skipped+len(batch)-n
	}
	return written, skipped, nil
}

// sumAdditive reads the additive samples of the days at positions idx in one
// statement (derivedSamples) and returns the daily value of each additive
// type, by position: the per-source sum, then the largest of those. Sources
// inside one provider overlap (a phone and a watch both count the same
// steps), so adding them would double count. A day without any additive
// sample has no entry.
//
// The values may be sealed, so SQL selects the samples and Go sums them as
// they stream: only the running totals are held. The sum is exact and takes
// each value as the SQL it replaces did (value::numeric, 15 significant
// digits), so a recompute of unchanged samples gives the same bits whatever
// order the rows come in, and never rewrites the row.
func sumAdditive(ctx context.Context, tx TxLike, sc scope, idx []int32, providers []string, los, his []time.Time, all window) (map[int]map[string]float64, error) {
	rows := openRows(ctx, tx, sc.uv, tQuantity, derivedSamples,
		[]any{sc.tenant, sc.user, idx, providers, los, his, additiveTypes, all.lo, all.hi})
	type group struct {
		day        int
		qt, source string
	}
	totals := map[group]*big.Rat{}
	for r, err := range rows {
		if err != nil {
			return nil, fmt.Errorf("sum daily samples: %w", err)
		}
		v, ok := r.float("value")
		if !ok {
			continue
		}
		d, ok := new(big.Rat).SetString(strconv.FormatFloat(v, 'g', 15, 64))
		if !ok {
			return nil, fmt.Errorf("sum daily samples: %v is not a finite value", v)
		}
		day, _ := r.int("i")
		g := group{int(day), r.text("quantity_type"), firstNonEmpty(r["source_bundle_id"], r["source_name"], r["device_id"])}
		if t := totals[g]; t != nil {
			t.Add(t, d)
		} else {
			totals[g] = d
		}
	}
	type dayType struct {
		day int
		qt  string
	}
	best := map[dayType]*big.Rat{}
	for g, t := range totals {
		k := dayType{g.day, g.qt}
		if b := best[k]; b == nil || t.Cmp(b) > 0 {
			best[k] = t
		}
	}
	sums := map[int]map[string]float64{}
	for k, t := range best {
		day, ok := sums[k.day]
		if !ok {
			day = map[string]float64{}
			sums[k.day] = day
		}
		day[k.qt], _ = t.Float64()
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
