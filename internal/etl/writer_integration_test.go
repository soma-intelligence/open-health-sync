// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// write runs one WriteAccumulated for the env's user in its own transaction.
func (e *env) write(cfg WriterConfig, acc *model.MapResult) Counts {
	e.t.Helper()
	return e.writeFor(e.tenant, e.user, cfg, acc)
}

func (e *env) writeFor(tenant, user uuid.UUID, cfg WriterConfig, acc *model.MapResult) Counts {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	require.NoError(e.t, err)
	defer tx.Rollback(ctx) //nolint:errcheck
	c, err := NewWriter(cfg).WriteAccumulated(ctx, tx, scope{tenant: tenant, user: user, provider: "dev"}, acc)
	require.NoError(e.t, err)
	require.NoError(e.t, tx.Commit(ctx))
	return c
}

var smallCopy = WriterConfig{BatchSize: 3, CopyThreshold: 5}

func dayFigure(hash string, day time.Time, value float64, replaceable bool) model.QuantitySample {
	return model.QuantitySample{
		Provider: "dev", QuantityType: "step_count", Value: value, Unit: "count",
		StartDate: day, EndDate: day.Add(24*time.Hour - time.Second), ContentHash: hash, Replaceable: replaceable,
	}
}

func points(n int, base time.Time) []model.QuantitySample {
	out := make([]model.QuantitySample, n)
	for i := range out {
		at := base.Add(time.Duration(i) * time.Minute)
		out[i] = model.QuantitySample{Provider: "dev", QuantityType: "heart_rate", Value: 60, Unit: "bpm",
			StartDate: at, EndDate: at, ContentHash: fmt.Sprintf("p%d", i)}
	}
	return out
}

func (e *env) scalar(sql string, args ...any) any {
	e.t.Helper()
	var v any
	require.NoError(e.t, e.pool.QueryRow(context.Background(), sql, args...).Scan(&v))
	return v
}

func TestWriter_QuantityInsertAndCopyPathsAgree(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	// Below the threshold: batched INSERTs (batch size 3).
	c := e.write(smallCopy, &model.MapResult{QuantitySamples: points(4, base)})
	assert.Equal(t, TableCount{Written: 4}, c["quantity_samples"])
	// Above it: COPY, with an in-file duplicate and four rows already stored.
	more := append(points(12, base), points(1, base)...)
	c = e.write(smallCopy, &model.MapResult{QuantitySamples: more})
	assert.Equal(t, TableCount{Written: 8, Skipped: 5}, c["quantity_samples"])
	assert.Equal(t, 12, e.count("quantity_samples"))
}

func TestWriter_ReplaceableQuantityLatestWins(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	c := e.write(smallCopy, &model.MapResult{QuantitySamples: []model.QuantitySample{
		dayFigure("steps-day", day, 3000, true),
		dayFigure("steps-day", day, 9000, true), // superseded in-file
	}})
	assert.Equal(t, TableCount{Written: 1, Skipped: 1}, c["quantity_samples"])
	assert.EqualValues(t, 9000, e.scalar(`SELECT value FROM ohs_data.quantity_samples WHERE tenant_id = $1`, e.tenant))

	c = e.write(smallCopy, &model.MapResult{QuantitySamples: []model.QuantitySample{dayFigure("steps-day", day, 14000, true)}})
	assert.Equal(t, 1, c["quantity_samples"].Written)
	assert.EqualValues(t, 14000, e.scalar(`SELECT value FROM ohs_data.quantity_samples WHERE tenant_id = $1`, e.tenant))

	// A re-send that changes nothing writes nothing.
	c = e.write(smallCopy, &model.MapResult{QuantitySamples: []model.QuantitySample{dayFigure("steps-day", day, 14000, true)}})
	assert.Equal(t, TableCount{Skipped: 1}, c["quantity_samples"])

	// Not replaceable: first write wins.
	e.write(smallCopy, &model.MapResult{QuantitySamples: []model.QuantitySample{dayFigure("fixed", day, 1, false)}})
	e.write(smallCopy, &model.MapResult{QuantitySamples: []model.QuantitySample{dayFigure("fixed", day, 2, false)}})
	assert.EqualValues(t, 1, e.scalar(`SELECT value FROM ohs_data.quantity_samples WHERE content_hash = 'fixed'`))
}

func TestWriter_ReplaceableAboveTheCopyThresholdStillReplaces(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	var figs []model.QuantitySample
	for i := range 12 {
		figs = append(figs, dayFigure(fmt.Sprintf("f%d", i), day.AddDate(0, 0, i), 100, true))
	}
	e.write(smallCopy, &model.MapResult{QuantitySamples: figs})
	for i := range figs {
		figs[i].Value = 200
	}
	c := e.write(smallCopy, &model.MapResult{QuantitySamples: figs})
	assert.Equal(t, 12, c["quantity_samples"].Written)
	assert.EqualValues(t, 12, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples WHERE value = 200`))
}

func TestWriter_ReplaceableDailySummaryAndNutritionMerge(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	steps, steps2, kcal := 9000, 12000, 300.0
	e.write(smallCopy, &model.MapResult{DailySummaries: []model.DailySummary{
		{Provider: "dev", SummaryDate: day, StepCount: &steps, ActiveKcal: &kcal, ContentHash: "d", Replaceable: true},
	}})
	// The re-send omits active_kcal: the stored value is kept, step_count replaced.
	e.write(smallCopy, &model.MapResult{DailySummaries: []model.DailySummary{
		{Provider: "dev", SummaryDate: day, StepCount: &steps2, ContentHash: "d", Replaceable: true},
	}})
	var gotSteps int
	var gotKcal float64
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT step_count, active_kcal FROM ohs_data.daily_summaries WHERE tenant_id = $1`, e.tenant).Scan(&gotSteps, &gotKcal))
	assert.Equal(t, 12000, gotSteps)
	assert.Equal(t, 300.0, gotKcal)

	at := day.Add(10 * time.Hour)
	a, b := 95.0, 120.0
	e.write(smallCopy, &model.MapResult{NutritionLogs: []model.NutritionLog{{Provider: "dev", StartDate: at, EnergyKcal: &a, ContentHash: "n", Replaceable: true}}})
	e.write(smallCopy, &model.MapResult{NutritionLogs: []model.NutritionLog{{Provider: "dev", StartDate: at, EnergyKcal: &b, ContentHash: "n", Replaceable: true}}})
	assert.EqualValues(t, 120, e.scalar(`SELECT energy_kcal FROM ohs_data.nutrition_logs WHERE tenant_id = $1`, e.tenant))
}

func TestWriter_CategoryObservationHeartbeatCopy(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	var cats []model.CategorySample
	var obs []model.Observation
	var hbs []model.HeartbeatSample
	for i := range 7 {
		at := base.Add(time.Duration(i) * time.Minute)
		v := float64(i)
		cats = append(cats, model.CategorySample{Provider: "dev", CategoryType: "c", StartDate: at, EndDate: at, ContentHash: fmt.Sprintf("c%d", i)})
		obs = append(obs, model.Observation{Provider: "dev", CanonicalCode: "x", TS: at, ValueNum: &v})
		hbs = append(hbs, model.HeartbeatSample{SeriesID: "s", Timestamp: at, RRMS: 800})
	}
	for _, cfg := range []WriterConfig{smallCopy, {BatchSize: 100, CopyThreshold: 500}} {
		c := e.write(cfg, &model.MapResult{CategorySamples: cats, Observations: obs, HeartbeatSamples: hbs})
		assert.Equal(t, 7, c["category_samples"].Written+c["category_samples"].Skipped)
		assert.Equal(t, 7, c["observations"].Written+c["observations"].Skipped)
		assert.Equal(t, 7, c["heartbeat_samples"].Written+c["heartbeat_samples"].Skipped)
	}
	assert.Equal(t, 7, e.count("category_samples"))
	assert.Equal(t, 7, e.count("observations"))
	assert.Equal(t, 7, e.count("heartbeat_samples"))
}

func sleepSession(hash string, start time.Time, hours int) model.SleepSession {
	return model.SleepSession{Provider: "dev", BedtimeStart: start, BedtimeEnd: start.Add(time.Duration(hours) * time.Hour), ContentHash: hash}
}

func phase(name string, start time.Time, minutes int, parent string) model.SleepPhase {
	return model.SleepPhase{Phase: name, StartDate: start, EndDate: start.Add(time.Duration(minutes) * time.Minute), DurationS: minutes * 60, ParentContentHash: parent}
}

func TestWriter_SleepWithPhases(t *testing.T) {
	e := newEnv(t)
	n1 := time.Date(2026, 8, 17, 22, 0, 0, 0, time.UTC)
	n2 := n1.Add(24 * time.Hour)
	acc := &model.MapResult{
		SleepSessions: []model.SleepSession{sleepSession("s1", n1, 8), sleepSession("s2", n2, 8)},
		SleepPhases: []model.SleepPhase{
			phase("deep", n1, 30, "s1"),
			phase("rem", n2, 30, "s2"),
			phase("light", n2.Add(time.Hour), 30, ""),     // unstamped, contained in s2
			phase("light", n1.Add(-48*time.Hour), 30, ""), // covered by no session
		},
	}
	c := e.write(smallCopy, acc)
	assert.Equal(t, TableCount{Written: 2}, c["sleep_sessions"])
	assert.Equal(t, TableCount{Written: 3, Skipped: 1}, c["sleep_phases"])

	var s2ID int64
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT id FROM ohs_data.sleep_sessions WHERE content_hash = 's2'`).Scan(&s2ID))
	assert.EqualValues(t, 2, e.scalar(`SELECT count(*) FROM ohs_data.sleep_phases WHERE session_id = $1 AND tenant_id = $2`, s2ID, e.tenant))

	// Replaying resolves the existing parents and skips everything.
	c = e.write(smallCopy, acc)
	assert.Equal(t, TableCount{Skipped: 2}, c["sleep_sessions"])
	assert.Equal(t, TableCount{Skipped: 4}, c["sleep_phases"])
}

func TestWriter_SleepPhasesWithoutSessionAreSynthesized(t *testing.T) {
	e := newEnv(t)
	n := time.Date(2026, 8, 17, 22, 0, 0, 0, time.UTC)
	c := e.write(smallCopy, &model.MapResult{SleepPhases: []model.SleepPhase{
		phase(model.SleepStageLight, n, 30, ""), phase(model.SleepStageDeep, n.Add(30*time.Minute), 30, ""),
	}})
	assert.Equal(t, 1, c["sleep_sessions"].Written)
	assert.Equal(t, 2, c["sleep_phases"].Written)
	assert.EqualValues(t, 3600, e.scalar(`SELECT total_sleep_seconds FROM ohs_data.sleep_sessions WHERE tenant_id = $1`, e.tenant))
}

func TestWriter_WorkoutRoutesInBatchAndOrphan(t *testing.T) {
	e := newEnv(t)
	start := time.Date(2026, 8, 18, 7, 0, 0, 0, time.UTC)
	w := model.Workout{Provider: "dev", ActivityType: "running", StartDate: start, EndDate: start.Add(time.Hour), ContentHash: "w1"}
	pt := func(m int, parent string) model.RoutePoint {
		return model.RoutePoint{Timestamp: start.Add(time.Duration(m) * time.Minute), Latitude: 37.4, Longitude: -122.1 + float64(m)/1000, ParentContentHash: parent}
	}
	c := e.write(smallCopy, &model.MapResult{Workouts: []model.Workout{w}, RoutePoints: []model.RoutePoint{pt(1, "w1"), pt(2, "")}})
	assert.Equal(t, 1, c["workouts"].Written)
	assert.Equal(t, 2, c["route_points"].Written)

	// A route uploaded on its own attaches to the workout by time overlap,
	// through the COPY path (above the threshold of 5).
	var orphans []model.RoutePoint
	for m := 10; m < 20; m++ {
		orphans = append(orphans, pt(m, ""))
	}
	c = e.write(smallCopy, &model.MapResult{RoutePoints: orphans})
	assert.Equal(t, 10, c["route_points"].Written)

	// No workout covers this span: skipped, not failed.
	late := pt(0, "")
	late.Timestamp = start.Add(48 * time.Hour)
	c = e.write(smallCopy, &model.MapResult{RoutePoints: []model.RoutePoint{late}})
	assert.Equal(t, TableCount{Skipped: 1}, c["route_points"])

	// Another user of the same tenant never borrows this user's workout.
	other := e.newUser(e.tenant)
	c = e.writeFor(e.tenant, other, smallCopy, &model.MapResult{RoutePoints: []model.RoutePoint{pt(30, "")}})
	assert.Equal(t, TableCount{Skipped: 1}, c["route_points"])
	assert.Equal(t, 12, e.count("route_points"))
}

func TestWriter_ECGWithSamples(t *testing.T) {
	e := newEnv(t)
	start := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	rec := model.ECGRecording{StartDate: start, EndDate: start.Add(30 * time.Second), SamplingFrequency: 512,
		NumberOfMeasurements: 3, VoltagePath: "raw/x/voltage.ndjson.gz", ContentHash: "ecg1"}
	sample := func(i int, parent string) model.ECGVoltageSample {
		return model.ECGVoltageSample{TimeSinceSampleStart: float64(i) / 512, VoltageUV: float64(i), ParentContentHash: parent}
	}
	c := e.write(smallCopy, &model.MapResult{ECGRecordings: []model.ECGRecording{rec}, ECGSamples: []model.ECGVoltageSample{sample(0, "ecg1"), sample(1, "")}})
	assert.Equal(t, 1, c["ecg_recordings"].Written)
	assert.Equal(t, 2, c["ecg_samples"].Written)

	// ts is the recording start plus the offset.
	var ts time.Time
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT ts FROM ohs_data.ecg_samples WHERE tenant_id = $1 ORDER BY ts DESC LIMIT 1`, e.tenant).Scan(&ts))
	assert.WithinDuration(t, start.Add(time.Second/512), ts, time.Microsecond)

	// Orphans linked by parent content hash.
	c = e.write(smallCopy, &model.MapResult{ECGSamples: []model.ECGVoltageSample{sample(2, "ecg1")}})
	assert.Equal(t, 1, c["ecg_samples"].Written)

	// A voltage-only object (no parent hash) links through voltage_path = its source key.
	var vs []model.ECGVoltageSample
	for i := 3; i < 10; i++ {
		vs = append(vs, sample(i, ""))
	}
	c = e.write(smallCopy, &model.MapResult{ECGSamples: vs, SourceKey: "raw/x/voltage.ndjson.gz"})
	assert.Equal(t, 7, c["ecg_samples"].Written)

	// Without a source key nothing can be resolved: skipped.
	c = e.write(smallCopy, &model.MapResult{ECGSamples: []model.ECGVoltageSample{sample(20, "")}})
	assert.Equal(t, TableCount{Skipped: 1}, c["ecg_samples"])
	// Unknown parent hash: skipped.
	c = e.write(smallCopy, &model.MapResult{ECGSamples: []model.ECGVoltageSample{sample(21, "nope")}})
	assert.Equal(t, TableCount{Skipped: 1}, c["ecg_samples"])

	// Another tenant cannot resolve this tenant's recording by hash.
	t2 := e.newTenant()
	u2 := e.newUser(t2)
	c = e.writeFor(t2, u2, smallCopy, &model.MapResult{ECGSamples: []model.ECGVoltageSample{sample(22, "ecg1")}})
	assert.Equal(t, TableCount{Skipped: 1}, c["ecg_samples"])
	assert.Equal(t, 10, e.count("ecg_samples"))
}

func TestWriter_SecondCopyInOneTransactionReusesStaging(t *testing.T) {
	e := newEnv(t)
	start := time.Date(2026, 8, 18, 7, 0, 0, 0, time.UTC)
	var ws []model.Workout
	var pts []model.RoutePoint
	for k := range 2 {
		h := fmt.Sprintf("w%d", k)
		s := start.Add(time.Duration(k) * 3 * time.Hour)
		ws = append(ws, model.Workout{Provider: "dev", ActivityType: "running", StartDate: s, EndDate: s.Add(time.Hour), ContentHash: h})
		for m := range 8 {
			pts = append(pts, model.RoutePoint{Timestamp: s.Add(time.Duration(m) * time.Minute), Latitude: 1, Longitude: float64(m), ParentContentHash: h})
		}
	}
	c := e.write(smallCopy, &model.MapResult{Workouts: ws, RoutePoints: pts})
	assert.Equal(t, 16, c["route_points"].Written)
}

// compressQuantity compresses every chunk of quantity_samples, as the policy
// does once a chunk is 30 days old.
func (e *env) compressQuantity() {
	e.t.Helper()
	e.exec(`SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('ohs_data.quantity_samples') c`)
}

func TestWriter_QuantityDedupsAgainstCompressedChunks(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	day := time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)
	c := e.write(smallCopy, &model.MapResult{QuantitySamples: append(points(300, base), dayFigure("d1", day, 100, true))})
	assert.Equal(t, TableCount{Written: 301}, c["quantity_samples"])
	e.compressQuantity()

	// A re-upload of the same period: every row is a duplicate, found in the
	// compressed chunk with the default decompression limit.
	c = e.write(smallCopy, &model.MapResult{QuantitySamples: points(300, base)})
	assert.Equal(t, TableCount{Skipped: 300}, c["quantity_samples"])

	// New rows in the compressed period are written, a changed Replaceable
	// figure replaces the stored one, an unchanged one is skipped.
	late := points(310, base)[300:]
	for i := range late {
		late[i].ContentHash = fmt.Sprintf("late%d", i)
	}
	c = e.write(smallCopy, &model.MapResult{QuantitySamples: append(late, dayFigure("d1", day, 250, true))})
	assert.Equal(t, TableCount{Written: 11}, c["quantity_samples"])
	c = e.write(smallCopy, &model.MapResult{QuantitySamples: []model.QuantitySample{dayFigure("d1", day, 250, true)}})
	assert.Equal(t, TableCount{Skipped: 1}, c["quantity_samples"])

	assert.Equal(t, 311, e.count("quantity_samples"))
	assert.EqualValues(t, 250, e.scalar(`SELECT value FROM ohs_data.quantity_samples WHERE content_hash = 'd1'`))
}

func TestWriter_QuantityConcurrentJobsOfOneUserWriteOnce(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	const writers = 4
	results := make(chan Counts, writers)
	for range writers {
		go func() {
			ctx := context.Background()
			tx, err := e.pool.Begin(ctx)
			if err != nil {
				results <- nil
				return
			}
			defer tx.Rollback(ctx) //nolint:errcheck
			c, err := NewWriter(smallCopy).WriteAccumulated(ctx, tx, scope{tenant: e.tenant, user: e.user, provider: "dev"},
				&model.MapResult{QuantitySamples: points(200, base)})
			if err != nil || tx.Commit(ctx) != nil {
				results <- nil
				return
			}
			results <- c
		}()
	}
	written := 0
	for range writers {
		c := <-results
		require.NotNil(t, c, "a writer failed")
		written += c["quantity_samples"].Written
	}
	assert.Equal(t, 200, written, "the per-user lock lets exactly one job write each row")
	assert.Equal(t, 200, e.count("quantity_samples"))
}
