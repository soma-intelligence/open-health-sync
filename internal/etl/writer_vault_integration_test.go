// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
	"github.com/soma-intelligence/open-health-sync/internal/vault/series"
)

// everyFamily is one batch with a record of every family the writer stores,
// children with their parents.
func everyFamily(at time.Time) *model.MapResult {
	f := func(v float64) *float64 { return &v }
	n := func(v int) *int { return &v }
	return &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{Provider: "dev", QuantityType: "heart_rate", Value: 61, Unit: "bpm", StartDate: at, EndDate: at,
				SourceName: "Apple Watch", DeviceID: "w1", ContentHash: "q1", Metadata: map[string]any{"timezone": "Europe/Paris"}},
			{Provider: "dev", QuantityType: model.QTStepCount, Value: 120, Unit: "count", StartDate: at, EndDate: at.Add(time.Minute),
				ContentHash: "q2", Replaceable: true},
		},
		CategorySamples: []model.CategorySample{{Provider: "dev", CategoryType: "menstrual_flow", ValueInt: 2, ValueLabel: "medium",
			StartDate: at, EndDate: at.Add(time.Hour), ContentHash: "c1", Metadata: map[string]any{"timezone": "UTC"}}},
		SleepSessions: []model.SleepSession{{Provider: "dev", ExternalID: "night-1", BedtimeStart: at.Add(-8 * time.Hour), BedtimeEnd: at,
			TotalSleepSeconds: n(25000), SleepScore: f(80), ContentHash: "s1", Metadata: map[string]any{"sourceName": "Watch"}}},
		SleepPhases: []model.SleepPhase{{Phase: model.SleepStageDeep, StartDate: at.Add(-7 * time.Hour), EndDate: at.Add(-6 * time.Hour),
			DurationS: 3600, ParentContentHash: "s1"}},
		Workouts: []model.Workout{{Provider: "dev", ExternalID: "run-1", ActivityType: "running", StartDate: at.Add(2 * time.Hour),
			EndDate: at.Add(3 * time.Hour), DurationS: n(3600), DistanceM: f(10000), ContentHash: "w1", Replaceable: true}},
		RoutePoints: []model.RoutePoint{{Timestamp: at.Add(2*time.Hour + time.Minute), Latitude: 48.85, Longitude: 2.35, ParentContentHash: "w1"}},
		NutritionLogs: []model.NutritionLog{{Provider: "dev", ExternalID: "meal-1", StartDate: at, MealType: "lunch", EnergyKcal: f(650),
			NutrientsJSON: map[string]any{"water": 0.3}, ContentHash: "n1", Replaceable: true}},
		ECGRecordings: []model.ECGRecording{{StartDate: at, EndDate: at.Add(30 * time.Second), Classification: 1, SamplingFrequency: 512,
			NumberOfMeasurements: 2, ContentHash: "e1"}},
		ECGSamples: []model.ECGVoltageSample{
			{TimeSinceSampleStart: 0, VoltageUV: 12.5, ParentContentHash: "e1"},
			{TimeSinceSampleStart: 1.0 / 512, VoltageUV: 13.5, ParentContentHash: "e1"},
		},
		HeartbeatSamples: []model.HeartbeatSample{{SeriesID: "hb-1", Timestamp: at, RRMS: 812}},
		DailySummaries: []model.DailySummary{{Provider: "dev", SummaryDate: dateOnly(at.AddDate(0, 0, -3)), RestingHR: f(52),
			StepCount: n(9000), ContentHash: "d1", Replaceable: true}},
		Observations: []model.Observation{{TS: at, CanonicalCode: "body_temperature", ValueNum: f(36.6), UnitUCUM: "Cel"}},
	}
}

// etlWritten are the sealed tables the ETL writes with clear columns for
// their sealed values. time_tags (the API) and workout_tracks (built on read)
// are written elsewhere; quantity_hourly_sealed, which has no such columns,
// is checked apart.
var etlWritten = []string{
	tQuantity, tCategory, tSleep, tPhases, tWorkouts, tNutrition, tECG, tSeries,
	tDaily, tObservation, "failed_records",
}

// Every row the ETL writes for a user with keys is sealed: the sealed value is
// set, every sensitive column is NULL, and content_hash is the keyed hash, not
// the plain one.
func TestWriterSealsEveryTable(t *testing.T) {
	e := newEnv(t)
	at := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	e.write(WriterConfig{}, everyFamily(at))
	// The job path: a failed record, the derived daily rows and the DST
	// fixture's provider samples.
	e.now = afterFixture
	e.register("apple_health", appleShapedSteps)
	e.process(e.upload(e.tenant, e.user, "apple_health", "step_count", append(fixtureLines(t, "dst_steps.ndjson"), `not json`)...))

	for _, table := range etlWritten {
		require.Positive(t, e.count(table), table)
		sc := vault.Schema[table]
		nulls := make([]string, 0, len(sc.Sealed))
		for _, c := range sc.Sealed {
			nulls = append(nulls, c.Name+" IS NOT NULL")
		}
		var clear int
		require.NoError(t, e.pool.QueryRow(context.Background(), fmt.Sprintf(
			`SELECT count(*) FROM ohs_data.%s WHERE tenant_id = $1 AND (sealed IS NULL OR %s)`, table, strings.Join(nulls, " OR ")),
			e.tenant).Scan(&clear))
		assert.Zero(t, clear, "%s: a row with a clear value or no sealed value", table)
		if sc.MAC {
			var noMAC int
			require.NoError(t, e.pool.QueryRow(context.Background(),
				`SELECT count(*) FROM ohs_data.`+table+` WHERE tenant_id = $1 AND content_mac IS NULL`, e.tenant).Scan(&noMAC))
			assert.Zero(t, noMAC, "%s: a row without content_mac", table)
		}
	}

	require.Positive(t, e.count("quantity_hourly_sealed"))
	assert.Zero(t, e.scalar(`SELECT count(*) FROM ohs_data.quantity_hourly_sealed
		WHERE tenant_id = $1 AND (sealed IS NULL OR content_mac IS NULL)`, e.tenant), "a bucket without a sealed value or a MAC")

	// content_hash is keyed: the plain hash of the mapper is nowhere, and the
	// keyed one finds the row.
	for table, plain := range map[string]string{tQuantity: "q1", tCategory: "c1", tSleep: "s1", tWorkouts: "w1",
		tNutrition: "n1", tECG: "e1", tDaily: "d1"} {
		var keyed, clear int
		require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE content_hash = $2),
			count(*) FILTER (WHERE content_hash = $3) FROM ohs_data.`+table+` WHERE tenant_id = $1`,
			e.tenant, e.hash(plain), plain).Scan(&keyed, &clear))
		assert.Equal(t, 1, keyed, table)
		assert.Zero(t, clear, table)
		assert.NotEqual(t, plain, e.hash(plain))
	}
	phase := model.SleepPhase{Phase: model.SleepStageDeep, StartDate: at.Add(-7 * time.Hour), EndDate: at.Add(-6 * time.Hour),
		DurationS: 3600, ParentContentHash: e.hash("s1")}
	assert.EqualValues(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.sleep_phases WHERE content_hash = $1`,
		e.hash(model.SleepPhaseContentHash(phase))), "a child hashes over its keyed parent hash")

	// The promoted columns follow the record.
	var worn bool
	var tz string
	var sourceKey []byte
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT worn, tz, source_key FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND content_hash = $2`, e.tenant, e.hash("q1")).Scan(&worn, &tz, &sourceKey))
	assert.True(t, worn)
	assert.Equal(t, "Europe/Paris", tz)
	assert.Equal(t, e.keys(e.tenant, e.user).SourceKey("Apple Watch"), sourceKey)

	// And the rows open to what was written.
	assert.EqualValues(t, 61, e.scalar(`SELECT value FROM ohs_data.quantity_samples WHERE content_hash = $1`, e.hash("q1")))
	assert.Equal(t, "medium", e.scalar(`SELECT value_label FROM ohs_data.category_samples WHERE content_hash = $1`, e.hash("c1")))
	assert.EqualValues(t, 3600, e.scalar(`SELECT sum(duration_s) FROM ohs_data.sleep_phases`))
	for _, pts := range e.blobPoints(series.Heartbeat) {
		assert.EqualValues(t, 812, pts[0].V[series.HeartbeatRR])
	}
	assert.Equal(t, 1, e.count(tHeartbeat))
	assert.Positive(t, e.count(tRoutes))
	assert.Positive(t, e.count(tECGSamples))
	assert.EqualValues(t, 36.6, e.scalar(`SELECT value_num FROM ohs_data.observations`))
	assert.EqualValues(t, 0.3, e.scalar(`SELECT (nutrients_json->>'water')::float8 FROM ohs_data.nutrition_logs`))
	assert.Contains(t, e.scalar(`SELECT error_message FROM ohs_data.failed_records`), "parse error")
	assert.Equal(t, []daySteps{{"2026-10-31", 10}, {"2026-11-01", 500}, {"2026-11-02", 40}}, e.dailySteps("apple_health"))
}

// Without a vault the writer writes clear rows, as before: no sealed value,
// the values in their columns, the plain hash.
func TestWriterClearWithoutVault(t *testing.T) {
	e := newEnv(t)
	at := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck
	_, err = NewWriter(WriterConfig{}).WriteAccumulated(ctx, tx, scope{tenant: e.tenant, user: e.user, provider: "dev"}, everyFamily(at))
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	for _, table := range etlWritten[:len(etlWritten)-1] {
		var sealed int
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_data.`+table+` WHERE tenant_id = $1 AND sealed IS NOT NULL`,
			e.tenant).Scan(&sealed))
		assert.Zero(t, sealed, table)
		assert.Positive(t, e.count(table), table)
	}
	var v float64
	var worn bool
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT value, worn FROM ohs_data.quantity_samples WHERE content_hash = 'q1'`).Scan(&v, &worn))
	assert.EqualValues(t, 61, v)
	assert.True(t, worn)
}

// xmin is the transaction that last wrote a row.
func (e *env) xmin(table, hash string) string {
	e.t.Helper()
	var x string
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`SELECT xmin::text FROM ohs_data.`+table+` WHERE tenant_id = $1 AND content_hash = $2`, e.tenant, hash).Scan(&x))
	return x
}

// An unchanged copy of an upserted row is not rewritten: the content MAC
// compares, though the sealed bytes differ on every seal.
func TestUpsertSkipsUnchanged(t *testing.T) {
	e := newEnv(t)
	at := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	e.write(WriterConfig{}, everyFamily(at))
	day := dateOnly(at)
	n, _ := e.derive(day)
	require.Equal(t, 1, n, "the day's derived row")
	type stored struct{ table, hash string }
	rows := []stored{
		{tDaily, e.hash("d1")}, {tQuantity, e.hash("q2")}, {tNutrition, e.hash("n1")}, {tWorkouts, e.hash("w1")},
		{tDaily, derivedHash(scope{uv: e.keys(e.tenant, e.user), tenant: e.tenant, user: e.user}, "dev", day)},
	}
	before := make([]string, len(rows))
	for i, r := range rows {
		before[i] = e.xmin(r.table, r.hash)
	}

	c := e.write(WriterConfig{}, everyFamily(at))
	assert.Zero(t, c[tDaily].Written+c[tQuantity].Written+c[tNutrition].Written+c[tWorkouts].Written, "%v", c)
	n, k := e.derive(day)
	assert.Equal(t, [2]int{0, 1}, [2]int{n, k}, "the derived row of the day is unchanged")
	for i, r := range rows {
		assert.Equal(t, before[i], e.xmin(r.table, r.hash), r.table)
	}

	// A changed figure is written, and an omitted one keeps the stored value.
	acc := everyFamily(at)
	acc.DailySummaries[0].RestingHR = nil
	steps := 9500
	acc.DailySummaries[0].StepCount = &steps
	c = e.write(WriterConfig{}, &model.MapResult{DailySummaries: acc.DailySummaries})
	assert.Equal(t, TableCount{Written: 1}, c[tDaily])
	assert.EqualValues(t, 9500, e.scalar(`SELECT step_count FROM ohs_data.daily_summaries WHERE content_hash = $1`, e.hash("d1")))
	assert.EqualValues(t, 52, e.scalar(`SELECT resting_hr FROM ohs_data.daily_summaries WHERE content_hash = $1`, e.hash("d1")))
}

// derive recomputes the derived daily row of the env's user for day.
func (e *env) derive(day time.Time) (int, int) {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	require.NoError(e.t, err)
	defer tx.Rollback(ctx) //nolint:errcheck
	n, k, err := deriveDailySummaries(ctx, tx, scope{tenant: e.tenant, user: e.user, provider: "dev", uv: e.keys(e.tenant, e.user)},
		map[dayKey]*time.Location{{provider: "dev", day: day}: time.UTC})
	require.NoError(e.t, err)
	require.NoError(e.t, tx.Commit(ctx))
	return n, k
}

// A Replaceable copy of a row stored in clear (before the vault, with the
// plain hash) converts it in place and merges with it.
func TestUpsertConvertsAClearRow(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	e.exec(`INSERT INTO ohs_data.daily_summaries (tenant_id, user_id, provider, source_slug, summary_date, resting_hr, step_count, content_hash, metadata)
		VALUES ($1, $2, 'dev', 'dev', $3, 50, 7000, 'd-clear', '{}')`, e.tenant, e.user, day)
	steps := 8000
	c := e.write(WriterConfig{}, &model.MapResult{DailySummaries: []model.DailySummary{{Provider: "dev", SummaryDate: day,
		StepCount: &steps, ContentHash: "d-clear", Replaceable: true}}})
	assert.Equal(t, TableCount{Written: 1}, c[tDaily])
	require.Equal(t, 1, e.count(tDaily))
	var sealed bool
	var hr *float64
	var hash string
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT sealed IS NOT NULL, resting_hr, content_hash
		FROM ohs_data.daily_summaries WHERE tenant_id = $1`, e.tenant).Scan(&sealed, &hr, &hash))
	assert.True(t, sealed)
	assert.Nil(t, hr)
	assert.Equal(t, e.hash("d-clear"), hash)
	assert.EqualValues(t, 8000, e.scalar(`SELECT step_count FROM ohs_data.daily_summaries`))
	assert.EqualValues(t, 50, e.scalar(`SELECT resting_hr FROM ohs_data.daily_summaries`))
}

// An additive sample stored clear and delivered again with the vault on is
// converted, not stored twice: one row, and the day sums it once.
func TestLegacySampleRedeliveredCountsOnce(t *testing.T) {
	e := newEnv(t)
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	e.exec(`INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, source_slug, source_name, external_id,
		quantity_type, value, unit, start_date, end_date, content_hash, metadata)
		VALUES ($1, $2, 'dev', 'dev', 'Phone', 'st-1', 'step_count', 500, 'count', $3, $4, 'legacy-steps', '{}')`,
		e.tenant, e.user, at, at.Add(time.Hour))
	e.exec(`INSERT INTO ohs_data.record_aliases (tenant_id, user_id, provider, external_id, record_table, content_hash, start_time)
		VALUES ($1, $2, 'dev', 'st-1', 'quantity_samples', 'legacy-steps', $3)`, e.tenant, e.user, at)
	c := e.write(WriterConfig{}, &model.MapResult{QuantitySamples: []model.QuantitySample{{Provider: "dev",
		QuantityType: model.QTStepCount, Value: 500, Unit: "count", SourceName: "Phone", ExternalID: "st-1",
		StartDate: at, EndDate: at.Add(time.Hour), ContentHash: "legacy-steps"}}})
	assert.Equal(t, TableCount{Skipped: 1}, c[tQuantity])
	require.Equal(t, 1, e.count(tQuantity))
	n, _ := e.derive(dateOnly(at))
	require.Equal(t, 1, n)
	assert.EqualValues(t, 500, e.scalar(`SELECT step_count FROM ohs_data.daily_summaries`))
	var clear int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND (sealed IS NULL OR content_hash <> $2)`, e.tenant, e.hash("legacy-steps")).Scan(&clear))
	assert.Zero(t, clear, "the clear row is sealed under its keyed hash")
	assert.EqualValues(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.record_aliases WHERE content_hash = $1`, e.hash("legacy-steps")),
		"its alias follows")
}

// A recording and its samples stored clear, delivered again with the vault
// on: the parent is converted with its children, so nothing lands twice.
func TestLegacyParentConvertsItsChildren(t *testing.T) {
	e := newEnv(t)
	at := time.Date(2026, 9, 2, 6, 0, 0, 0, time.UTC)
	samples := []model.ECGVoltageSample{
		{TimeSinceSampleStart: 0, VoltageUV: 10, ParentContentHash: "ecg"},
		{TimeSinceSampleStart: 1.0 / 512, VoltageUV: 11, ParentContentHash: "ecg"},
	}
	var id int64
	require.NoError(t, e.pool.QueryRow(context.Background(), `INSERT INTO ohs_data.ecg_recordings
		(tenant_id, user_id, provider, source_slug, start_date, end_date, classification, symptoms_status,
		 sampling_frequency, number_of_measurements, content_hash, metadata)
		VALUES ($1, $2, 'dev', 'dev', $3, $4, 1, 0, 512, 2, 'ecg', '{}') RETURNING id`,
		e.tenant, e.user, at, at.Add(30*time.Second)).Scan(&id))
	for _, s := range samples {
		e.exec(`INSERT INTO ohs_data.ecg_samples (tenant_id, user_id, ecg_id, ts, time_since_sample_start, voltage_uv, content_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, e.tenant, e.user, id,
			at.Add(time.Duration(s.TimeSinceSampleStart*float64(time.Second))).Round(time.Microsecond),
			s.TimeSinceSampleStart, s.VoltageUV, model.ECGVoltageSampleContentHash(s))
	}
	c := e.write(WriterConfig{}, &model.MapResult{
		ECGRecordings: []model.ECGRecording{{StartDate: at, EndDate: at.Add(30 * time.Second), Classification: 1,
			SamplingFrequency: 512, NumberOfMeasurements: 2, ContentHash: "ecg"}},
		ECGSamples: samples,
	})
	assert.Equal(t, TableCount{Skipped: 1}, c[tECG])
	assert.Equal(t, TableCount{Skipped: 2}, c[tECGSamples])
	assert.Equal(t, 1, e.count(tECG))
	assert.Equal(t, 2, e.count(tECGSamples))
	// The rows went into the recording's blob, sealed.
	assert.EqualValues(t, 0, e.scalar(`SELECT count(*) FROM ohs_data.ecg_samples WHERE tenant_id = $1`, e.tenant))
	assert.EqualValues(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.series_blobs WHERE tenant_id = $1 AND sealed IS NOT NULL`, e.tenant))
	sum := 0.0
	for _, pts := range e.blobPoints(series.ECG) {
		for _, p := range pts {
			sum += p.V[series.ECGVoltage]
		}
	}
	assert.EqualValues(t, 21, sum)
}

// Two jobs of one user writing complementary figures of one summary: the
// second merges with what the first committed, whatever their timing.
func TestConcurrentMergesKeepBothFigures(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	hr, steps := 51.0, 9100
	write := func(tx pgx.Tx, s model.DailySummary) error {
		s.Provider, s.SummaryDate, s.ContentHash, s.Replaceable = "dev", day, "d-race", true
		_, err := NewWriter(WriterConfig{}).WriteAccumulated(ctx, tx,
			scope{tenant: e.tenant, user: e.user, provider: "dev", uv: e.keys(e.tenant, e.user)},
			&model.MapResult{DailySummaries: []model.DailySummary{s}})
		return err
	}
	first, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	defer first.Rollback(ctx) //nolint:errcheck
	require.NoError(t, write(first, model.DailySummary{RestingHR: &hr}))

	// The second job starts while the first holds its row uncommitted, and
	// must wait for it rather than read the summary as absent.
	done := make(chan error, 1)
	go func() {
		tx, err := e.pool.Begin(ctx)
		if err != nil {
			done <- err
			return
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err := write(tx, model.DailySummary{StepCount: &steps}); err != nil {
			done <- err
			return
		}
		done <- tx.Commit(ctx)
	}()
	require.Eventually(t, func() bool {
		var waiting int
		_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting)
		return waiting > 0
	}, 10*time.Second, 20*time.Millisecond, "the second job waits")
	require.NoError(t, first.Commit(ctx))
	require.NoError(t, <-done)

	assert.Equal(t, 1, e.count(tDaily))
	assert.EqualValues(t, 51, e.scalar(`SELECT resting_hr FROM ohs_data.daily_summaries`))
	assert.EqualValues(t, 9100, e.scalar(`SELECT step_count FROM ohs_data.daily_summaries`))
}

// A child hashes over its parent's keyed hash on every path, so the same child
// written with its parent and attached to the stored parent later is stored
// once.
func TestChildStoredOnceWithParentOrLater(t *testing.T) {
	e := newEnv(t)
	at := time.Date(2026, 9, 2, 6, 0, 0, 0, time.UTC)
	ecg := model.ECGRecording{StartDate: at, EndDate: at.Add(30 * time.Second), Classification: 1, SamplingFrequency: 512,
		NumberOfMeasurements: 2, ContentHash: "ecg"}
	samples := func() []model.ECGVoltageSample {
		return []model.ECGVoltageSample{
			{TimeSinceSampleStart: 0, VoltageUV: 10, ParentContentHash: "ecg"},
			{TimeSinceSampleStart: 1.0 / 512, VoltageUV: 11, ParentContentHash: "ecg"},
		}
	}
	c := e.write(WriterConfig{}, &model.MapResult{ECGRecordings: []model.ECGRecording{ecg}, ECGSamples: samples()})
	assert.Equal(t, TableCount{Written: 2}, c[tECGSamples])
	c = e.write(WriterConfig{}, &model.MapResult{ECGSamples: samples()})
	assert.Equal(t, TableCount{Skipped: 2}, c[tECGSamples], "attached to the stored recording, the samples are the same rows")
	assert.Equal(t, 2, e.count(tECGSamples))

	// A night: its time in bed with its stages, then the same stages alone,
	// put back under the stored session by the night grouping.
	bed := at.Add(-8 * time.Hour)
	stage := func(parent string) model.SleepPhase {
		return model.SleepPhase{Phase: model.SleepStageDeep, StartDate: bed.Add(time.Hour), EndDate: bed.Add(2 * time.Hour),
			DurationS: 3600, ParentContentHash: parent}
	}
	c = e.write(WriterConfig{}, &model.MapResult{
		SleepSessions: []model.SleepSession{{Provider: "dev", BedtimeStart: bed, BedtimeEnd: at, ContentHash: "bed"}},
		SleepPhases:   []model.SleepPhase{stage("bed")},
	})
	assert.Equal(t, TableCount{Written: 1}, c[tPhases])
	c = e.write(WriterConfig{}, &model.MapResult{SleepPhases: []model.SleepPhase{stage("")}})
	assert.Equal(t, TableCount{Skipped: 1}, c[tPhases])
	assert.Equal(t, 1, e.count(tPhases))
	assert.EqualValues(t, 3600, e.scalar(`SELECT deep_sleep_seconds FROM ohs_data.sleep_sessions WHERE content_hash = $1`, e.hash("bed")))
}

// A keyed hash is the user's own: the same record of two users hashes apart.
// An empty hash stays empty.
func TestKeyedHashIsPerUser(t *testing.T) {
	e := newEnv(t)
	other := e.newUser(e.tenant)
	assert.NotEqual(t, scope{uv: e.keys(e.tenant, other)}.hash("q1"), e.hash("q1"))
	assert.Equal(t, "", e.hash(""))
}
