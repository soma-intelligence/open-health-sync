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

// sampleLocation returns the time zone named in metadata, or UTC.
func sampleLocation(md map[string]any) *time.Location {
	if name, ok := md[MetadataTimezone].(string); ok && name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
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

// derivedDrop deletes the derived row of one day.
const derivedDrop = `DELETE FROM ohs_data.daily_summaries
	WHERE tenant_id = $1 AND user_id = $2 AND content_hash = $3 AND summary_date = $4`

var (
	derivedUpsert = insertHead(tDaily, dailyShape.columnsFor(false)) + placeholders(1, len(dailyShape.columnsFor(false))) + `
	ON CONFLICT (tenant_id, user_id, content_hash, summary_date) DO UPDATE SET ` + derivedSet() + `
	WHERE (` + derivedCols("daily_summaries.") + `) IS DISTINCT FROM (` + derivedCols("EXCLUDED.") + `)`
	// derivedUpsertSealed replaces a sealed derived row whose content MAC
	// differs: every figure and the metadata are in the sealed value.
	derivedUpsertSealed = insertHead(tDaily, dailyShape.columnsFor(true)) + placeholders(1, len(dailyShape.columnsFor(true))) + `
	ON CONFLICT (tenant_id, user_id, content_hash, summary_date) DO UPDATE SET ` +
		strings.Join(dailyShape.sealedSet("EXCLUDED", nil), ", ") + `
	WHERE ` + dailyShape.sealedDistinct("daily_summaries", "EXCLUDED", nil)
)

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

// deriveDailySummaries recomputes the derived row of every touched day from
// the stored samples, inside the job's transaction (so it sees what the job
// just wrote). A day with a provider summary of its own gets no derived row,
// and loses one written earlier, as does a day left without any additive
// sample (every one of them deleted).
func deriveDailySummaries(ctx context.Context, tx TxLike, sc scope, days map[dayKey]*time.Location) (written, skipped int, err error) {
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

	locked := map[string]bool{}
	for _, k := range keys {
		// Serialize derivations of one (tenant, user, provider): a concurrent job
		// writing samples for the same day either commits before this sum (and is
		// seen) or sums after this commit (and sees these rows). Without it two
		// jobs each sum without the other's rows and the last upsert loses data.
		// Keys are sorted, so locks are taken in a stable order.
		if !locked[k.provider] {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
				"ohs_daily|"+sc.tenant.String()+"|"+sc.user.String()+"|"+k.provider); err != nil {
				return written, skipped, fmt.Errorf("lock daily derivation: %w", err)
			}
			locked[k.provider] = true
		}
		// A derived row stored clear is converted first, or the lookup
		// below would take it for a provider summary.
		if err := convertLegacy(ctx, tx, sc, tDaily, `r.content_hash = $3 AND r.summary_date = $4`,
			derivedPlainHash(sc, k.provider, k.day), k.day); err != nil {
			return written, skipped, err
		}
		hash := derivedHash(sc, k.provider, k.day)
		var own bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM ohs_data.daily_summaries
				WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND summary_date = $4 AND content_hash <> $5)`,
			sc.tenant, sc.user, k.provider, k.day, hash).Scan(&own); err != nil {
			return written, skipped, fmt.Errorf("look up provider summary: %w", err)
		}
		if own {
			if _, err := tx.Exec(ctx, derivedDrop, sc.tenant, sc.user, hash, k.day); err != nil {
				return written, skipped, fmt.Errorf("drop derived summary: %w", err)
			}
			continue
		}

		loc := days[k]
		lo := time.Date(k.day.Year(), k.day.Month(), k.day.Day(), 0, 0, 0, 0, loc)
		hi := lo.AddDate(0, 0, 1)
		sums, err := sumAdditive(ctx, tx, sc, k.provider, lo, hi)
		if err != nil {
			return written, skipped, err
		}
		if len(sums) == 0 {
			if _, err := tx.Exec(ctx, derivedDrop, sc.tenant, sc.user, hash, k.day); err != nil {
				return written, skipped, fmt.Errorf("drop derived summary: %w", err)
			}
			continue
		}
		row, err := dailyShape.finish(sc, derivedRow(sc, k, loc, hash, sums))
		if err != nil {
			return written, skipped, err
		}
		upsert := derivedUpsert
		if sc.uv != nil {
			upsert = derivedUpsertSealed
		}
		tag, err := tx.Exec(ctx, upsert, row...)
		if err != nil {
			return written, skipped, fmt.Errorf("upsert derived summary: %w", err)
		}
		if tag.RowsAffected() > 0 {
			written++
		} else {
			skipped++
		}
	}
	return written, skipped, nil
}

// sumAdditive computes the daily value of each additive type of one provider
// over samples whose start falls in [lo, hi): the per-source sum, then the
// largest of those. Sources inside one provider overlap (a phone and a watch
// both count the same steps), so adding them would double count. The values
// may be sealed, so SQL selects the samples and Go sums them. The sum is exact
// and takes each value as the SQL it replaces did (value::numeric, 15
// significant digits), so a recompute of unchanged samples gives the same
// bits whatever order the rows come in, and never rewrites the row.
func sumAdditive(ctx context.Context, tx TxLike, sc scope, provider string, lo, hi time.Time) (map[string]float64, error) {
	rows := openRows(ctx, tx, sc.uv, tQuantity, `
		SELECT quantity_type, content_hash, start_date, sealed, value, source_bundle_id, source_name, device_id
		FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND quantity_type = ANY($4)
		  AND start_date >= $5 AND start_date < $6`,
		[]any{sc.tenant, sc.user, provider, additiveTypes, lo, hi})
	type group struct{ qt, source string }
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
		g := group{r.text("quantity_type"), firstNonEmpty(r["source_bundle_id"], r["source_name"], r["device_id"])}
		if t := totals[g]; t != nil {
			t.Add(t, d)
		} else {
			totals[g] = d
		}
	}
	best := map[string]*big.Rat{}
	for g, t := range totals {
		if b := best[g.qt]; b == nil || t.Cmp(b) > 0 {
			best[g.qt] = t
		}
	}
	sums := make(map[string]float64, len(best))
	for qt, t := range best {
		sums[qt], _ = t.Float64()
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
	}
}
