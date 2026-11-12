// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// perDayDerive is the derivation as it was before it became set based: an
// EXISTS, a sum and an upsert (or a delete) per day. It is kept here as the
// reference the set based statements are compared with.
func perDayDerive(ctx context.Context, tx TxLike, sc scope, days map[dayKey]*time.Location) (written, skipped int, err error) {
	const drop = `DELETE FROM ohs_data.daily_summaries
		WHERE tenant_id = $1 AND user_id = $2 AND content_hash = $3 AND summary_date = $4`
	upsert := insertHead(tDaily, dailyColumns) + placeholders(1, len(dailyColumns)) + derivedUpsertTail
	for _, k := range sortedDays(days) {
		hash := derivedHash(sc, k.provider, k.day)
		var own bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM ohs_data.daily_summaries
				WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND summary_date = $4 AND content_hash <> $5)`,
			sc.tenant, sc.user, k.provider, k.day, hash).Scan(&own); err != nil {
			return written, skipped, err
		}
		if own {
			if _, err := tx.Exec(ctx, drop, sc.tenant, sc.user, hash, k.day); err != nil {
				return written, skipped, err
			}
			continue
		}
		loc := days[k]
		lo := time.Date(k.day.Year(), k.day.Month(), k.day.Day(), 0, 0, 0, 0, loc)
		rows, err := tx.Query(ctx, `
			SELECT quantity_type, max(total) FROM (
				SELECT quantity_type,
				       COALESCE(NULLIF(source_bundle_id, ''), NULLIF(source_name, ''), NULLIF(device_id, ''), '') AS source,
				       sum(value::numeric)::double precision AS total
				FROM ohs_data.quantity_samples
				WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND quantity_type = ANY($4)
				  AND start_date >= $5 AND start_date < $6
				GROUP BY 1, 2
			) per_source
			GROUP BY quantity_type`,
			sc.tenant, sc.user, k.provider, additiveTypes, lo, lo.AddDate(0, 0, 1))
		if err != nil {
			return written, skipped, err
		}
		sums := map[string]float64{}
		var qt string
		var v float64
		if _, err := pgx.ForEachRow(rows, []any{&qt, &v}, func() error { sums[qt] = v; return nil }); err != nil {
			return written, skipped, err
		}
		if len(sums) == 0 {
			if _, err := tx.Exec(ctx, drop, sc.tenant, sc.user, hash, k.day); err != nil {
				return written, skipped, err
			}
			continue
		}
		tag, err := tx.Exec(ctx, upsert, derivedRow(sc, k, loc, hash, sums)...)
		if err != nil {
			return written, skipped, err
		}
		if tag.RowsAffected() > 0 {
			written++
		} else {
			skipped++
		}
	}
	return written, skipped, nil
}

type deriveFunc func(context.Context, TxLike, scope, map[dayKey]*time.Location) (int, int, error)

func (e *env) derive(user uuid.UUID, fn deriveFunc, days map[dayKey]*time.Location) (int, int) {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	require.NoError(e.t, err)
	defer tx.Rollback(ctx) //nolint:errcheck
	n, k, err := fn(ctx, tx, scope{tenant: e.tenant, user: user, provider: "dev"}, days)
	require.NoError(e.t, err)
	require.NoError(e.t, tx.Commit(ctx))
	return n, k
}

// summaries dumps a user's daily summaries without the columns that name the
// user (the derived hash covers the user id).
func (e *env) summaries(user uuid.UUID) []string {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `
		SELECT concat_ws('|', provider, source_slug, summary_date, step_count, active_kcal, total_kcal, distance_m,
		                 floors_climbed, readiness_score, resting_hr, metadata::text, captured_at)
		FROM ohs_data.daily_summaries WHERE tenant_id = $1 AND user_id = $2
		ORDER BY provider, summary_date, content_hash = 'own'`, e.tenant, user)
	require.NoError(e.t, err)
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(e.t, err)
	return out
}

// The set based derivation writes exactly what the per-day one wrote, on days
// in several zones (a DST day among them), two providers, overlapping
// sources, a day that has a provider summary and a day whose samples are gone.
func TestDeriveDailySummariesMatchesPerDayReference(t *testing.T) {
	e := newEnv(t)
	ref, set := e.user, e.newUser(e.tenant)
	la, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)
	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)

	n := 0
	sample := func(provider, qt string, v float64, at time.Time, tz, source string) model.QuantitySample {
		n++
		md := map[string]any{}
		if tz != "" {
			md[MetadataTimezone] = tz
		}
		return model.QuantitySample{Provider: provider, QuantityType: qt, Value: v, Unit: "count", StartDate: at.UTC(), EndDate: at.UTC(),
			SourceName: source, Metadata: md, ContentHash: fmt.Sprintf("h%d", n)}
	}
	var samples []model.QuantitySample
	// Los Angeles, around the 2026-03-08 spring forward (a 23 hour day), two
	// sources that count the same steps.
	for d := 6; d <= 9; d++ {
		for h := 0; h < 24; h += 5 {
			at := time.Date(2026, 3, d, h, 30, 0, 0, la)
			samples = append(samples,
				sample("dev", model.QTStepCount, float64(100+d*h), at, "America/Los_Angeles", "phone"),
				sample("dev", model.QTStepCount, float64(90+d*h)+0.1, at, "America/Los_Angeles", "watch"),
				sample("dev", model.QTActiveEnergyBurned, 12.3+float64(h), at, "America/Los_Angeles", "watch"),
				sample("dev", model.QTBasalEnergyBurned, 70.07, at, "America/Los_Angeles", "watch"),
			)
		}
	}
	// Paris, the same instants land on other calendar days.
	for h := 0; h < 48; h += 7 {
		at := time.Date(2026, 3, 20, h, 10, 0, 0, paris)
		samples = append(samples,
			sample("dev", model.QTDistanceWalkingRunning, 300.5+float64(h), at, "Europe/Paris", "phone"),
			sample("dev", "distance_cycling", 1000.25, at, "Europe/Paris", "bike"),
			sample("dev", model.QTFlightsClimbed, 2, at, "Europe/Paris", "phone"),
			sample("dev", model.QTTotalEnergyBurned, 99.9, at, "Europe/Paris", "phone"),
		)
	}
	// No zone: UTC days. A second provider on the same days.
	for h := 0; h < 72; h += 9 {
		at := time.Date(2026, 4, 1, h, 0, 0, 0, time.UTC)
		samples = append(samples,
			sample("dev", model.QTStepCount, 250, at, "", ""),
			sample("other", model.QTStepCount, 333, at, "", "ring"),
			sample("other", model.QTHeartRate, 61, at, "", "ring"), // not additive
		)
	}
	steps := 4321
	acc := &model.MapResult{
		QuantitySamples: samples,
		// 2026-03-07 has a provider summary of its own: no derived row.
		DailySummaries: []model.DailySummary{{Provider: "dev", SummaryDate: time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC),
			StepCount: &steps, ContentHash: "own"}},
	}
	gone := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	for _, user := range []uuid.UUID{ref, set} {
		e.writeFor(e.tenant, user, smallCopy, acc)
		// A day whose only sample is deleted after its derived row was written.
		e.writeFor(e.tenant, user, smallCopy, &model.MapResult{QuantitySamples: []model.QuantitySample{
			sample("dev", model.QTStepCount, 10, gone, "", "")}})
	}
	days := touchedDays(scope{provider: "dev"}, acc)
	goneDays := map[dayKey]*time.Location{{provider: "dev", day: dateOnly(gone)}: time.UTC}
	require.Greater(t, len(days), 10)

	for _, fn := range []struct {
		user uuid.UUID
		fn   deriveFunc
	}{{ref, perDayDerive}, {set, deriveDailySummaries}} {
		e.derive(fn.user, fn.fn, goneDays)
	}
	require.Equal(t, e.summaries(ref), e.summaries(set))
	e.exec(`DELETE FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND start_date = $2`, e.tenant, gone)
	for k, loc := range goneDays {
		days[k] = loc
	}

	// First derivation, then a second that changes nothing, then one after a
	// sample changed: same counts and same rows each time.
	for round := 0; round < 3; round++ {
		if round == 2 {
			e.exec(`UPDATE ohs_data.quantity_samples SET value = value + 5
				WHERE tenant_id = $1 AND source_name = 'watch' AND quantity_type = 'step_count'`, e.tenant)
		}
		wantN, wantK := e.derive(ref, perDayDerive, days)
		gotN, gotK := e.derive(set, deriveDailySummaries, days)
		assert.Equal(t, [2]int{wantN, wantK}, [2]int{gotN, gotK}, "round %d", round)
		want := e.summaries(ref)
		require.NotEmpty(t, want)
		assert.Equal(t, want, e.summaries(set), "round %d", round)
	}
	assert.EqualValues(t, 0, e.scalar(`SELECT count(*) FROM ohs_data.daily_summaries
		WHERE tenant_id = $1 AND summary_date = $2`, e.tenant, dateOnly(gone)), "the emptied day lost its derived row")
}

// The statements a derivation runs do not grow with the days it touches.
func TestDeriveDailySummariesStatementCount(t *testing.T) {
	e := newEnv(t)
	days := map[dayKey]*time.Location{}
	var samples []model.QuantitySample
	for d := 0; d < 40; d++ {
		at := time.Date(2026, 1, 1+d, 12, 0, 0, 0, time.UTC)
		samples = append(samples, model.QuantitySample{Provider: "dev", QuantityType: model.QTStepCount, Value: 10, Unit: "count",
			StartDate: at, EndDate: at, ContentHash: fmt.Sprintf("s%d", d)})
		days[dayKey{provider: "dev", day: dateOnly(at)}] = time.UTC
	}
	e.write(smallCopy, &model.MapResult{QuantitySamples: samples})

	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck
	counting := &countingTx{Tx: tx}
	n, k, err := deriveDailySummaries(ctx, counting, scope{tenant: e.tenant, user: e.user, provider: "dev"}, days)
	require.NoError(t, err)
	assert.Equal(t, [2]int{40, 0}, [2]int{n, k})
	// One lock, one summary lookup, one sum, one upsert.
	assert.Equal(t, 4, counting.n)
}

// countingTx counts the statements sent through a transaction.
type countingTx struct {
	pgx.Tx
	n int
}

func (c *countingTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.n++
	return c.Tx.Exec(ctx, sql, args...)
}

func (c *countingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.n++
	return c.Tx.Query(ctx, sql, args...)
}

func (c *countingTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.n++
	return c.Tx.QueryRow(ctx, sql, args...)
}
