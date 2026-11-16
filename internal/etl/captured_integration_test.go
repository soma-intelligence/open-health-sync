// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// writeAt is write for an object captured at the given time: sealed under the
// user's keys, as every job writes, or clear when the env is (eachMode).
func (e *env) writeAt(captured time.Time, acc *model.MapResult) Counts {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	require.NoError(e.t, err)
	defer tx.Rollback(ctx) //nolint:errcheck
	sc := scope{tenant: e.tenant, user: e.user, provider: "dev", captured: captured}
	if !e.clear {
		sc.uv = e.keys(e.tenant, e.user)
	}
	c, err := NewWriter(smallCopy).WriteAccumulated(ctx, tx, sc, acc)
	require.NoError(e.t, err)
	require.NoError(e.t, tx.Commit(ctx))
	return c
}

// eachMode runs a writer test twice: with the rows sealed under the user's
// keys, and with clear rows (no vault configured). The capture guard holds
// either way: in the conflict clauses for clear rows, in the Go merge and the
// sealed conflict clauses for sealed ones.
func eachMode(t *testing.T, fn func(t *testing.T, e *env)) {
	for _, m := range []struct {
		name  string
		clear bool
	}{{"sealed", false}, {"clear", true}} {
		t.Run(m.name, func(t *testing.T) {
			e := newEnv(t)
			e.clear = m.clear
			fn(t, e)
		})
	}
}

// uploadedAt is upload with the raw object's created_at set.
func (e *env) uploadedAt(at time.Time, provider string, lines ...string) queue.ETLJob {
	e.t.Helper()
	job := e.upload(e.tenant, e.user, provider, "q", lines...)
	e.exec(`UPDATE ohs_sync.raw_objects SET created_at = $2 WHERE key = $1`, job.Key, at)
	return job
}

func rq(qt string, v float64, start time.Time) string {
	return line(map[string]any{"type": qt, "value": v, "unit": "count", "start": start.UTC().Format(time.RFC3339), "replaceable": true})
}

// A job for an older raw object, run after a newer object revised the row,
// does not bring the old figure back. The same object processed again does
// replace (a mapper fix), and so does a newer one.
func TestProcess_StaleObjectDoesNotOverwriteNewerRevision(t *testing.T) {
	e := newEnv(t)
	// bump stands for a mapper fix: the same line maps to another value.
	bump := 0.0
	e.register("prov", func(r connector.RawRecord) (model.MapResult, error) {
		mr, err := quantityMap(r)
		for i := range mr.QuantitySamples {
			mr.QuantitySamples[i].Value += bump
		}
		return mr, err
	})
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	value := func() any {
		return e.scalar(`SELECT value FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user)
	}

	older := e.uploadedAt(t1, "prov", rq("step_count", 3000, day))
	newer := e.uploadedAt(t1.Add(time.Hour), "prov", rq("step_count", 9000, day))
	e.process(older)
	e.process(newer)
	assert.EqualValues(t, 9000, value())

	// The older object's job is retried or replayed.
	c := e.process(older)
	assert.Equal(t, TableCount{Skipped: 1}, c[tQuantity])
	assert.EqualValues(t, 9000, value(), "the provider's revision stays")
	var captured time.Time
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT captured_at FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user).Scan(&captured))
	assert.True(t, captured.Equal(t1.Add(time.Hour)))

	// The newer object processed again after a mapper fix: equal capture time wins.
	bump = 1
	c = e.process(newer)
	assert.Equal(t, 1, c[tQuantity].Written)
	assert.EqualValues(t, 9001, value())

	// A later object replaces as before.
	bump = 0
	e.process(e.uploadedAt(t1.Add(2*time.Hour), "prov", rq("step_count", 12000, day)))
	assert.EqualValues(t, 12000, value())
}

// The capture guard on every replaceable table: daily summaries, nutrition
// logs, sleep sessions with their phases, workouts with their routes.
func TestWriter_StaleCopyIsHeldBackOnEveryReplaceableTable(t *testing.T) {
	eachMode(t, testWriterStaleCopyIsHeldBackOnEveryReplaceableTable)
}

func testWriterStaleCopyIsHeldBackOnEveryReplaceableTable(t *testing.T, e *env) {
	day := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	at := day.Add(22 * time.Hour)
	t1 := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	ip := func(v int) *int { return &v }
	fp := func(v float64) *float64 { return &v }

	batch := func(steps int, kcal float64, total int, dist float64, phase string) *model.MapResult {
		return &model.MapResult{
			DailySummaries: []model.DailySummary{{Provider: "dev", SummaryDate: day, StepCount: ip(steps), ContentHash: "d", Replaceable: true}},
			NutritionLogs: []model.NutritionLog{{Provider: "dev", StartDate: at, EnergyKcal: fp(kcal),
				ContentHash: "n", Replaceable: true}},
			SleepSessions: []model.SleepSession{{Provider: "dev", BedtimeStart: at, BedtimeEnd: at.Add(8 * time.Hour),
				TotalSleepSeconds: ip(total), ContentHash: "s", Replaceable: true}},
			SleepPhases: []model.SleepPhase{{Phase: phase, StartDate: at, EndDate: at.Add(time.Hour), DurationS: 3600, ParentContentHash: "s"}},
			Workouts: []model.Workout{{Provider: "dev", ActivityType: "running", StartDate: at, EndDate: at.Add(time.Hour),
				DistanceM: fp(dist), ContentHash: "w", Replaceable: true}},
			RoutePoints: []model.RoutePoint{{Timestamp: at.Add(time.Minute), Latitude: dist / 1000, Longitude: 1, ParentContentHash: "w"}},
		}
	}
	check := func(steps int, kcal float64, total int, dist float64, phase string) {
		t.Helper()
		assert.EqualValues(t, steps, e.scalar(`SELECT step_count FROM ohs_data.daily_summaries WHERE tenant_id = $1`, e.tenant))
		assert.EqualValues(t, kcal, e.scalar(`SELECT energy_kcal FROM ohs_data.nutrition_logs WHERE tenant_id = $1`, e.tenant))
		assert.EqualValues(t, total, e.scalar(`SELECT total_sleep_seconds FROM ohs_data.sleep_sessions WHERE tenant_id = $1`, e.tenant))
		assert.EqualValues(t, dist, e.scalar(`SELECT distance_m FROM ohs_data.workouts WHERE tenant_id = $1`, e.tenant))
		assert.Equal(t, phase, e.scalar(`SELECT string_agg(phase, ',' ORDER BY phase) FROM ohs_data.sleep_phases WHERE tenant_id = $1`, e.tenant))
	}

	e.writeAt(t2, batch(9000, 500, 28000, 5200, "deep"))
	check(9000, 500, 28000, 5200, "deep")

	// An older copy arrives late: nothing moves, and its children are not mixed in.
	c := e.writeAt(t1, batch(3000, 100, 20000, 4000, "light"))
	check(9000, 500, 28000, 5200, "deep")
	for _, table := range []string{tDaily, tNutrition, tSleep, tWorkouts} {
		assert.Equal(t, TableCount{Skipped: 1}, c[table], table)
	}
	assert.Equal(t, TableCount{Skipped: 1}, c[tPhases])
	assert.Equal(t, TableCount{Skipped: 1}, c[tRoutes])
	assert.Equal(t, 1, e.count(tRoutes))

	// The same capture time replaces, and so does a later one.
	e.writeAt(t2, batch(9100, 510, 28100, 5300, "rem"))
	check(9100, 510, 28100, 5300, "rem")
	e.writeAt(t2.Add(time.Hour), batch(9200, 520, 28200, 5400, "deep"))
	check(9200, 520, 28200, 5400, "deep")

	// A row stored before captured_at existed carries no order: it is replaced.
	for _, table := range []string{"daily_summaries", "nutrition_logs", "sleep_sessions", "workouts"} {
		e.exec(`UPDATE ohs_data.` + table + ` SET captured_at = NULL`)
	}
	e.writeAt(t1, batch(1, 2, 3, 4, "light"))
	check(1, 2, 3, 4, "light")
}

// A stale copy of a record whose start moved neither deletes the newer copy
// nor lands beside it.
func TestWriter_StaleMovedCopyDoesNotSupersedeNewer(t *testing.T) {
	eachMode(t, testWriterStaleMovedCopyDoesNotSupersedeNewer)
}

func testWriterStaleMovedCopyDoesNotSupersedeNewer(t *testing.T, e *env) {
	start := time.Date(2026, 9, 1, 22, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	fp := func(v float64) *float64 { return &v }
	batch := func(at time.Time, v float64) *model.MapResult {
		return &model.MapResult{
			QuantitySamples: []model.QuantitySample{{Provider: "dev", ExternalID: "c-1", QuantityType: "strain", Value: v, Unit: "index",
				StartDate: at, EndDate: at, ContentHash: "q", Replaceable: true, SupersedeMoved: true}},
			NutritionLogs: []model.NutritionLog{{Provider: "dev", ExternalID: "m-1", StartDate: at, EnergyKcal: fp(v),
				ContentHash: "n", Replaceable: true, SupersedeMoved: true}},
			Workouts: []model.Workout{{Provider: "dev", ExternalID: "w-1", ActivityType: "running", StartDate: at, EndDate: at.Add(time.Hour),
				DistanceM: fp(v), ContentHash: "w", Replaceable: true}},
			SleepSessions: []model.SleepSession{{Provider: "dev", ExternalID: "s-1", BedtimeStart: at, BedtimeEnd: at.Add(8 * time.Hour),
				SleepScore: fp(v), ContentHash: "s", Replaceable: true}},
		}
	}
	e.writeAt(t1.Add(time.Hour), batch(start.Add(time.Hour), 11))
	c := e.writeAt(t1, batch(start, 10))
	for table, col := range map[string]string{
		"quantity_samples": "start_date", "nutrition_logs": "start_date", "workouts": "start_date", "sleep_sessions": "bedtime_start",
	} {
		assert.Equal(t, 1, e.count(table), table)
		got := e.scalar(`SELECT `+col+` FROM ohs_data.`+table+` WHERE tenant_id = $1`, e.tenant).(time.Time)
		assert.True(t, got.Equal(start.Add(time.Hour)), table)
		assert.Equal(t, TableCount{Skipped: 1}, c[table], table)
	}

	// A newer copy with a moved start still supersedes.
	e.writeAt(t1.Add(2*time.Hour), batch(start.Add(2*time.Hour), 12))
	for _, table := range []string{"quantity_samples", "nutrition_logs", "workouts", "sleep_sessions"} {
		assert.Equal(t, 1, e.count(table), table)
	}
	assert.EqualValues(t, 12, e.scalar(`SELECT value FROM ohs_data.quantity_samples WHERE tenant_id = $1`, e.tenant))
}

// A sparse part of several types dedups each type against its own window.
func TestWriter_QuantityDedupPerTypeWindows(t *testing.T) {
	e := newEnv(t)
	y2021 := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	y2026 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2024, 3, 3, 9, 0, 0, 0, time.UTC)
	sample := func(qt, hash string, at time.Time) model.QuantitySample {
		return model.QuantitySample{Provider: "dev", QuantityType: qt, Value: 1, Unit: "count", StartDate: at, EndDate: at, ContentHash: hash}
	}
	part := []model.QuantitySample{
		sample("body_mass", "w1", y2021), sample("body_mass", "w2", y2026),
		sample("step_count", "s1", mid), sample("step_count", "s2", mid.Add(time.Hour)),
	}
	// Stored beforehand: one row of the part, and rows of other types inside its span.
	e.write(smallCopy, &model.MapResult{QuantitySamples: append(points(8, mid), part[2])})
	c := e.write(smallCopy, &model.MapResult{QuantitySamples: part})
	assert.Equal(t, TableCount{Written: 3, Skipped: 1}, c[tQuantity])
	c = e.write(smallCopy, &model.MapResult{QuantitySamples: part})
	assert.Equal(t, TableCount{Skipped: 4}, c[tQuantity])
	assert.Equal(t, 12, e.count("quantity_samples"))
}

// rowVersions is the xmin of every row of table for the tenant: a row that
// was written again gets a new one.
func (e *env) rowVersions(table string) string {
	e.t.Helper()
	// Read from the table itself: e.scalar reads a copy, whose rows are new.
	var v string
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`SELECT coalesce(string_agg(xmin::text, ',' ORDER BY xmin::text), '') FROM ohs_data.`+table+` WHERE tenant_id = $1`, e.tenant).Scan(&v))
	return v
}

// A newer copy with the same figures moves the stored capture time forward
// without counting as a write, so a late older object cannot overwrite what
// the newest object confirmed. The same object processed again, and an older
// copy with the same figures, write nothing at all.
func TestWriter_NewerIdenticalCopyAdvancesCaptureTime(t *testing.T) {
	eachMode(t, testWriterNewerIdenticalCopyAdvancesCaptureTime)
}

func testWriterNewerIdenticalCopyAdvancesCaptureTime(t *testing.T, e *env) {
	day := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	at := day.Add(22 * time.Hour)
	t1 := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Hour)
	ip := func(v int) *int { return &v }
	fp := func(v float64) *float64 { return &v }
	tables := map[string]string{
		"quantity_samples": "value", "daily_summaries": "step_count", "nutrition_logs": "energy_kcal",
		"sleep_sessions": "total_sleep_seconds", "workouts": "distance_m",
	}
	batch := func(v int) *model.MapResult {
		return &model.MapResult{
			QuantitySamples: []model.QuantitySample{{Provider: "dev", QuantityType: "step_count", Value: float64(v), Unit: "count",
				StartDate: day, EndDate: day, ContentHash: "q", Replaceable: true}},
			DailySummaries: []model.DailySummary{{Provider: "dev", SummaryDate: day, StepCount: ip(v), ContentHash: "d", Replaceable: true}},
			NutritionLogs: []model.NutritionLog{{Provider: "dev", StartDate: at, EnergyKcal: fp(float64(v)),
				ContentHash: "n", Replaceable: true}},
			SleepSessions: []model.SleepSession{{Provider: "dev", BedtimeStart: at, BedtimeEnd: at.Add(8 * time.Hour),
				TotalSleepSeconds: ip(v), ContentHash: "s", Replaceable: true}},
			Workouts: []model.Workout{{Provider: "dev", ActivityType: "running", StartDate: at, EndDate: at.Add(time.Hour),
				DistanceM: fp(float64(v)), ContentHash: "w", Replaceable: true}},
		}
	}
	check := func(want int, captured time.Time) {
		t.Helper()
		for table, col := range tables {
			assert.EqualValues(t, want, e.scalar(`SELECT `+col+` FROM ohs_data.`+table+` WHERE tenant_id = $1`, e.tenant), table)
			got, _ := e.scalar(`SELECT captured_at FROM ohs_data.`+table+` WHERE tenant_id = $1`, e.tenant).(time.Time)
			assert.True(t, got.Equal(captured), "%s captured_at = %s, want %s", table, got, captured)
		}
	}
	skippedEverywhere := func(c Counts) {
		t.Helper()
		for table := range tables {
			assert.Equal(t, TableCount{Skipped: 1}, c[table], table)
		}
	}
	versions := func() map[string]string {
		out := map[string]string{}
		for table := range tables {
			out[table] = e.rowVersions(table)
		}
		return out
	}

	e.writeAt(t1, batch(5))
	check(5, t1)

	// The same figures from a newer object: not a write, but the newest truth.
	skippedEverywhere(e.writeAt(t2, batch(5)))
	check(5, t2)

	// A late object captured between the two does not overwrite it.
	skippedEverywhere(e.writeAt(t1.Add(time.Hour), batch(7)))
	check(5, t2)

	// The same object again, and an older identical copy, rewrite no row.
	before := versions()
	skippedEverywhere(e.writeAt(t2, batch(5)))
	skippedEverywhere(e.writeAt(t1, batch(5)))
	assert.Equal(t, before, versions(), "no row is written again")
	check(5, t2)

	// A row stored before captured_at existed takes the time of the copy that confirms it.
	for table := range tables {
		e.exec(`UPDATE ohs_data.` + table + ` SET captured_at = NULL`)
	}
	skippedEverywhere(e.writeAt(t1, batch(5)))
	check(5, t1)
}

// A pull stamps its raw objects with when the fetch started
// (raw_objects.captured_at), which may be well before they are registered.
// That time orders the copies, not the registration time.
func TestProcess_FetchTimeOrdersCopiesNotRegistrationTime(t *testing.T) {
	e := newEnv(t)
	e.register("prov", quantityMap)
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)

	// Fetched first, registered last (a slow pull).
	slow := e.uploadedAt(t1.Add(time.Hour), "prov", rq("step_count", 3000, day))
	e.exec(`UPDATE ohs_sync.raw_objects SET captured_at = $2 WHERE key = $1`, slow.Key, t1)
	fast := e.uploadedAt(t1.Add(30*time.Minute), "prov", rq("step_count", 9000, day))

	e.process(fast)
	c := e.process(slow)
	assert.Equal(t, TableCount{Skipped: 1}, c[tQuantity])
	assert.EqualValues(t, 9000, e.scalar(`SELECT value FROM ohs_data.quantity_samples WHERE tenant_id = $1`, e.tenant))
}
