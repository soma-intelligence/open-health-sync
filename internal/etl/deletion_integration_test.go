// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// recordsMap is a provider whose lines are whole records: a sleep with two
// phases and a derived sample ("<id>_resting_heart_rate"), a workout with
// two route points, a mindful category sample, or a deletion of an id.
func recordsMap(r connector.RawRecord) (model.MapResult, error) {
	id, _ := r.Fields["id"].(string)
	kind, _ := r.Fields["kind"].(string)
	if kind == "delete" {
		d := model.Deletion{ExternalID: id}
		if cl, ok := r.Fields["clear"].([]any); ok {
			for _, f := range cl {
				d.ClearSummaryFields = append(d.ClearSummaryFields, f.(string))
			}
		}
		return model.MapResult{Deletions: []model.Deletion{d}}, nil
	}
	start, err := time.Parse(time.RFC3339, r.Fields["start"].(string))
	if err != nil {
		return model.MapResult{}, err
	}
	end := start.Add(time.Hour)
	hash := model.ComputeContentHash(map[string]string{"user_id": r.UserID, "provider": r.Provider, "id": id, "kind": kind})
	switch kind {
	case "sleep":
		hr := 50.0
		return model.MapResult{
			SleepSessions: []model.SleepSession{{
				Provider: r.Provider, ExternalID: id, BedtimeStart: start, BedtimeEnd: end,
				RestingHR: &hr, ContentHash: hash, Replaceable: true,
			}},
			SleepPhases: []model.SleepPhase{
				{Phase: "deep", StartDate: start, EndDate: start.Add(30 * time.Minute), DurationS: 1800, ParentContentHash: hash},
				{Phase: "rem", StartDate: start.Add(30 * time.Minute), EndDate: end, DurationS: 1800, ParentContentHash: hash},
			},
			QuantitySamples: []model.QuantitySample{{
				Provider: r.Provider, ExternalID: id + "_" + model.QTRestingHeartRate, QuantityType: model.QTRestingHeartRate,
				Value: hr, Unit: "bpm", StartDate: start, EndDate: end, ContentHash: hash + "-rhr", Replaceable: true,
			}},
		}, nil
	case "workout":
		return model.MapResult{
			Workouts: []model.Workout{{
				Provider: r.Provider, ExternalID: id, ActivityType: "running", StartDate: start, EndDate: end,
				ContentHash: hash, Replaceable: true,
			}},
			RoutePoints: []model.RoutePoint{
				{Timestamp: start, Latitude: 48.1, Longitude: 2.1, ParentContentHash: hash},
				{Timestamp: start.Add(time.Minute), Latitude: 48.2, Longitude: 2.2, ParentContentHash: hash},
			},
		}, nil
	case "samples":
		// A record that only gives samples: one under the id itself, one
		// under "<id>_<quantity type>", and a category sample "<id>_<suffix>".
		return model.MapResult{
			QuantitySamples: []model.QuantitySample{
				{Provider: r.Provider, ExternalID: id, QuantityType: model.QTStrain, Value: 10, Unit: "index",
					StartDate: start, EndDate: end, ContentHash: hash + "-strain", Replaceable: true},
				{Provider: r.Provider, ExternalID: id + "_" + model.QTRestingHeartRate, QuantityType: model.QTRestingHeartRate,
					Value: 50, Unit: "bpm", StartDate: start, EndDate: end, ContentHash: hash + "-rhr", Replaceable: true},
			},
			CategorySamples: []model.CategorySample{{
				Provider: r.Provider, ExternalID: id + "_note", CategoryType: "mindful_session", StartDate: start, EndDate: end,
				ContentHash: hash + "-note",
			}},
		}, nil
	case "recovery":
		// A WHOOP style recovery of the sleep id: samples under
		// "<id>_recovery_<qt>" naming their summary day, and the recovery
		// figures of that day's summary.
		day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		md := func() map[string]any { return map[string]any{model.MetadataSummaryDate: day.Format("2006-01-02")} }
		score, rhr := 60.0, 52.0
		return model.MapResult{
			QuantitySamples: []model.QuantitySample{
				{Provider: r.Provider, ExternalID: id + "_recovery_" + model.QTRestingHeartRate, QuantityType: model.QTRestingHeartRate,
					Value: rhr, Unit: "bpm", StartDate: start, EndDate: start, ContentHash: hash + "-rhr", Replaceable: true, Metadata: md()},
				{Provider: r.Provider, ExternalID: id + "_recovery_" + model.QTHRVRMSSD, QuantityType: model.QTHRVRMSSD,
					Value: 40, Unit: "ms", StartDate: start, EndDate: start, ContentHash: hash + "-hrv", Replaceable: true, Metadata: md()},
			},
			DailySummaries: []model.DailySummary{{
				Provider: r.Provider, SummaryDate: day, RecoveryScore: &score, RestingHR: &rhr, Replaceable: true,
				RecordID:    id + "_recovery",
				ContentHash: model.ComputeContentHash(map[string]string{"user_id": r.UserID, "day": day.Format("2006-01-02")}),
			}},
		}, nil
	case "cycle":
		// The cycle figures of the same day's summary (same identity).
		day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		strain := 12.5
		return model.MapResult{DailySummaries: []model.DailySummary{{
			Provider: r.Provider, SummaryDate: day, StrainScore: &strain, Replaceable: true,
			ContentHash: model.ComputeContentHash(map[string]string{"user_id": r.UserID, "day": day.Format("2006-01-02")}),
		}}}, nil
	case "mindful":
		return model.MapResult{CategorySamples: []model.CategorySample{{
			Provider: r.Provider, ExternalID: id, CategoryType: "mindful_session", StartDate: start, EndDate: end, ContentHash: hash,
		}}}, nil
	}
	return model.MapResult{}, fmt.Errorf("unknown kind %q", kind)
}

func rec(kind, id string, start time.Time) string {
	return line(map[string]any{"kind": kind, "id": id, "start": start.UTC().Format(time.RFC3339)})
}

func del(id string) string { return line(map[string]any{"kind": "delete", "id": id}) }

// delClear is a deletion that clears daily summary figures.
func delClear(id string, fields ...string) string {
	return line(map[string]any{"kind": "delete", "id": id, "clear": fields})
}

// summaryFigures is the recovery, resting heart rate and strain of the
// provider's summary of day, nil when there is no row.
type summaryFigures struct{ recovery, rhr, strain *float64 }

func (e *env) summaryOf(tenant, user any, day time.Time) *summaryFigures {
	e.t.Helper()
	var f summaryFigures
	err := e.pool.QueryRow(context.Background(), `SELECT recovery_score, resting_hr, strain_score
		FROM ohs_data.daily_summaries WHERE tenant_id = $1 AND user_id = $2 AND provider = 'prov' AND summary_date = $3`,
		tenant, user, day).Scan(&f.recovery, &f.rhr, &f.strain)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	require.NoError(e.t, err)
	return &f
}

// recordCounts is the rows of every table a deletion touches, for one user.
func (e *env) recordCounts(tenant, user uuid.UUID) map[string]int {
	e.t.Helper()
	out := map[string]int{}
	for _, tbl := range []string{"sleep_sessions", "sleep_phases", "workouts", "route_points", "quantity_samples", "category_samples"} {
		out[tbl] = e.countFor(tbl, tenant, user)
	}
	return out
}

func (e *env) tombstones(tenant, user uuid.UUID) int {
	e.t.Helper()
	return e.countFor("deleted_records", tenant, user)
}

var deletionBase = time.Date(2026, 9, 1, 22, 0, 0, 0, time.UTC)

func TestDeletion_RemovesRecordAndChildren(t *testing.T) {
	e := newEnv(t)
	e.register("prov", recordsMap)
	e.process(e.upload(e.tenant, e.user, "prov", "records",
		rec("sleep", "s-1", deletionBase), rec("sleep", "s-2", deletionBase.Add(24*time.Hour)),
		rec("workout", "w-1", deletionBase.Add(-6*time.Hour)), rec("mindful", "m-1", deletionBase.Add(-2*time.Hour))))
	// A workout track summary, as the track builder leaves for a traced workout.
	e.exec(`INSERT INTO ohs_data.workout_tracks (tenant_id, user_id, workout_id, length_m, point_count, started_at, ended_at)
		SELECT tenant_id, user_id, id, 100, 2, start_date, end_date FROM ohs_data.workouts WHERE tenant_id = $1 AND user_id = $2`,
		e.tenant, e.user)
	require.Equal(t, map[string]int{
		"sleep_sessions": 2, "sleep_phases": 4, "workouts": 1, "route_points": 2, "quantity_samples": 2, "category_samples": 1,
	}, e.recordCounts(e.tenant, e.user))

	c := e.process(e.upload(e.tenant, e.user, "prov", "records", del("s-1"), del("w-1"), del("m-1"), del("s-1")))
	assert.Equal(t, TableCount{Written: 3, Skipped: 1}, c[tDeleted], "the repeated deletion is applied once")
	assert.Equal(t, map[string]int{
		"sleep_sessions": 1, "sleep_phases": 2, "workouts": 0, "route_points": 0, "quantity_samples": 1, "category_samples": 0,
	}, e.recordCounts(e.tenant, e.user))
	assert.Equal(t, 0, e.count("workout_tracks"))
	assert.Equal(t, 3, e.tombstones(e.tenant, e.user))
	var left string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT external_id FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user).Scan(&left))
	assert.Equal(t, "s-2_resting_heart_rate", left)

	// The same deletion again changes nothing and fails nothing.
	c = e.process(e.upload(e.tenant, e.user, "prov", "records", del("s-1")))
	assert.Equal(t, TableCount{Skipped: 1}, c[tDeleted])
	assert.Equal(t, 1, e.count("sleep_sessions"))
}

func TestDeletion_ReprocessingTheOriginalStoresNothing(t *testing.T) {
	e := newEnv(t)
	e.register("prov", recordsMap)
	orig := e.upload(e.tenant, e.user, "prov", "records", rec("sleep", "s-1", deletionBase), rec("workout", "w-1", deletionBase.Add(-6*time.Hour)))
	e.process(orig)
	e.process(e.upload(e.tenant, e.user, "prov", "records", del("s-1"), del("w-1")))

	c := e.process(orig)
	assert.Equal(t, 0, c.TotalWritten())
	assert.Equal(t, TableCount{Skipped: 1}, c[tSleep])
	assert.Equal(t, TableCount{Skipped: 2}, c[tPhases])
	assert.Equal(t, TableCount{Skipped: 1}, c[tQuantity])
	assert.Equal(t, TableCount{Skipped: 1}, c[tWorkouts])
	assert.Equal(t, TableCount{Skipped: 2}, c[tRoutes])
	for tbl, n := range e.recordCounts(e.tenant, e.user) {
		assert.Zero(t, n, tbl)
	}
}

func TestDeletion_BeforeTheRecordStillWins(t *testing.T) {
	e := newEnv(t)
	e.register("prov", recordsMap)
	c := e.process(e.upload(e.tenant, e.user, "prov", "records", del("s-1")))
	assert.Equal(t, TableCount{Written: 1}, c[tDeleted])

	e.process(e.upload(e.tenant, e.user, "prov", "records", rec("sleep", "s-1", deletionBase), rec("sleep", "s-2", deletionBase.Add(24*time.Hour))))
	got := e.recordCounts(e.tenant, e.user)
	assert.Equal(t, 1, got["sleep_sessions"])
	assert.Equal(t, 2, got["sleep_phases"])
	assert.Equal(t, 1, got["quantity_samples"])

	// A record and its deletion in one object: the deletion wins too.
	c = e.process(e.upload(e.tenant, e.user, "prov", "records", rec("workout", "w-9", deletionBase), del("w-9")))
	assert.Equal(t, TableCount{Skipped: 1}, c[tWorkouts])
	assert.Equal(t, 0, e.count("workouts"))
}

// A deletion whose id matches no stored sleep or workout (a record that only
// gave samples, or a WHOOP recovery whose sleep is still stored) removes the
// samples stored under the id or "<id>_<suffix>", whenever they start. Ids
// that merely share a prefix stay.
func TestDeletion_SamplesOnlyRecordRemovesStoredSamples(t *testing.T) {
	e := newEnv(t)
	e.register("prov", recordsMap)
	e.process(e.upload(e.tenant, e.user, "prov", "records",
		rec("sleep", "s-1", deletionBase),
		rec("samples", "s-1_recovery", deletionBase.Add(9*time.Hour)),
		// Months away from any parent: no time window may hide them.
		rec("samples", "r-9", deletionBase.Add(-120*24*time.Hour)),
		rec("samples", "r-9x", deletionBase), rec("samples", "r-9-2", deletionBase), rec("samples", "r-90", deletionBase)))
	require.Equal(t, 1+5*2, e.countFor("quantity_samples", e.tenant, e.user))
	require.Equal(t, 5, e.countFor("category_samples", e.tenant, e.user))

	c := e.process(e.upload(e.tenant, e.user, "prov", "records", del("s-1_recovery"), del("r-9")))
	assert.Equal(t, TableCount{Written: 2}, c[tDeleted])
	assert.Equal(t, 1, e.countFor("sleep_sessions", e.tenant, e.user), "the sleep of the recovery stays")
	rows, err := e.pool.Query(context.Background(), `SELECT external_id FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2 ORDER BY external_id`, e.tenant, e.user)
	require.NoError(t, err)
	left, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	assert.Equal(t, []string{"r-9-2", "r-9-2_resting_heart_rate", "r-90", "r-90_resting_heart_rate",
		"r-9x", "r-9x_resting_heart_rate", "s-1_resting_heart_rate"}, left)
	rows, err = e.pool.Query(context.Background(), `SELECT external_id FROM ohs_data.category_samples
		WHERE tenant_id = $1 AND user_id = $2 ORDER BY external_id`, e.tenant, e.user)
	require.NoError(t, err)
	left, err = pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	assert.Equal(t, []string{"r-9-2_note", "r-90_note", "r-9x_note"}, left)

	// The lookups use the external id index, not a scan of the hypertable.
	for _, q := range []string{
		`EXPLAIN DELETE FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2 AND provider = 'prov'` + deletionExternalMatch("'r-1'", "'r-1_'", "'r-1`'"),
		`EXPLAIN DELETE FROM ohs_data.category_samples WHERE tenant_id = $1 AND user_id = $2 AND provider = 'prov'` + deletionExternalMatch("'r-1'", "'r-1_'", "'r-1`'"),
	} {
		plan := e.explain(q, e.tenant, e.user)
		assert.Contains(t, plan, "_external on", "uses the external id index: %s", plan)
		assert.Regexp(t, "Index Cond: .*external_id.* < 'r-1`'", plan, "bounded by the id: %s", plan)
	}
}

// A deletion clears the daily summary figures its record contributed, on the
// day its samples name, and only those: the other figures of the day, other
// days, users and tenants stay. A summary left empty goes, and reprocessing
// the deleted record does not bring its figures back.
func TestDeletion_ClearsSummaryFigures(t *testing.T) {
	e := newEnv(t)
	e.register("prov", recordsMap)
	day1 := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)
	day3 := day2.AddDate(0, 0, 1)
	// Cycles and recoveries come in separate objects, as WHOOP data types do.
	e.process(e.upload(e.tenant, e.user, "prov", "records",
		rec("cycle", "c-1", day1.Add(time.Hour)), rec("cycle", "c-2", day2.Add(time.Hour))))
	orig := e.upload(e.tenant, e.user, "prov", "records",
		rec("sleep", "s-1", day1.Add(-2*time.Hour)), rec("recovery", "s-1", day1.Add(7*time.Hour)),
		rec("recovery", "s-2", day2.Add(7*time.Hour)), rec("recovery", "s-3", day3.Add(7*time.Hour)))
	e.process(orig)
	other := e.newUser(e.tenant)
	e.process(e.upload(e.tenant, other, "prov", "records", rec("recovery", "s-1", day1.Add(7*time.Hour))))
	require.NotNil(t, e.summaryOf(e.tenant, e.user, day1))
	require.NotNil(t, e.summaryOf(e.tenant, e.user, day1).recovery)

	clear := []string{"recovery_score", "resting_hr"}
	// The recovery of night 1 alone; the sleep of night 1 (which takes its
	// recovery with it); and the recovery of night 3, the only figures of its day.
	e.process(e.upload(e.tenant, e.user, "prov", "records",
		delClear("s-1_recovery", clear...), delClear("s-3", clear...)))

	d1 := e.summaryOf(e.tenant, e.user, day1)
	require.NotNil(t, d1, "the cycle's figures keep the row")
	assert.Nil(t, d1.recovery)
	assert.Nil(t, d1.rhr)
	require.NotNil(t, d1.strain)
	assert.InDelta(t, 12.5, *d1.strain, 1e-9)
	d2 := e.summaryOf(e.tenant, e.user, day2)
	require.NotNil(t, d2)
	require.NotNil(t, d2.recovery, "another day stays")
	assert.Nil(t, e.summaryOf(e.tenant, e.user, day3), "a summary left without figures goes")
	assert.Equal(t, 1, e.countFor("sleep_sessions", e.tenant, e.user), "the recovery deletion leaves the sleep")
	require.NotNil(t, e.summaryOf(e.tenant, other, day1).recovery, "another user's summary stays")

	// A re-pull of the deleted recoveries, or the old raw object again, does
	// not restore their figures; the cycle's still update.
	e.process(orig)
	d1 = e.summaryOf(e.tenant, e.user, day1)
	require.NotNil(t, d1)
	assert.Nil(t, d1.recovery)
	assert.Nil(t, e.summaryOf(e.tenant, e.user, day3))
	assert.NotNil(t, e.summaryOf(e.tenant, e.user, day2).recovery)

	// The sleep's own deletion arriving later has nothing left to clear and
	// fails nothing; deleting night 2's sleep clears its recovery figures.
	e.process(e.upload(e.tenant, e.user, "prov", "records", delClear("s-1", clear...), delClear("s-2", clear...)))
	assert.Nil(t, e.summaryOf(e.tenant, e.user, day2).recovery)
	assert.NotNil(t, e.summaryOf(e.tenant, e.user, day2).strain)

	// Names outside the summary figures are ignored, never written as SQL.
	e.process(e.upload(e.tenant, e.user, "prov", "records", rec("recovery", "s-9", day3.Add(7*time.Hour))))
	e.process(e.upload(e.tenant, e.user, "prov", "records",
		delClear("s-9_recovery", "recovery_score; DROP TABLE ohs_data.daily_summaries", "metadata", "tenant_id")))
	d3 := e.summaryOf(e.tenant, e.user, day3)
	require.NotNil(t, d3)
	assert.NotNil(t, d3.recovery, "no allowed figure named: nothing cleared")
}

func TestDeletion_OtherUsersAndProvidersUntouched(t *testing.T) {
	e := newEnv(t)
	e.register("prov", recordsMap)
	e.register("other", recordsMap)
	sameTenant := e.newUser(e.tenant)
	otherTenant := e.newTenant()
	otherUser := e.newUser(otherTenant)
	lines := []string{rec("sleep", "s-1", deletionBase), rec("workout", "w-1", deletionBase.Add(-6*time.Hour))}
	for _, o := range []struct{ tenant, user uuid.UUID }{{e.tenant, e.user}, {e.tenant, sameTenant}, {otherTenant, otherUser}} {
		e.process(e.upload(o.tenant, o.user, "prov", "records", lines...))
	}
	// The same ids under another provider for the deleting user.
	e.process(e.upload(e.tenant, e.user, "other", "records", lines...))
	full := map[string]int{
		"sleep_sessions": 1, "sleep_phases": 2, "workouts": 1, "route_points": 2, "quantity_samples": 1, "category_samples": 0,
	}

	e.process(e.upload(e.tenant, e.user, "prov", "records", del("s-1"), del("w-1")))

	var remaining int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_data.sleep_sessions
		WHERE tenant_id = $1 AND user_id = $2 AND provider = 'other'`, e.tenant, e.user).Scan(&remaining))
	assert.Equal(t, 1, remaining, "the other provider's record of the same id stays")
	assert.Equal(t, 1, e.count("sleep_sessions"))
	assert.Equal(t, full, e.recordCounts(e.tenant, sameTenant))
	assert.Equal(t, full, e.recordCounts(otherTenant, otherUser))
	assert.Zero(t, e.tombstones(e.tenant, sameTenant))
	assert.Zero(t, e.tombstones(otherTenant, otherUser))

	// Their copies still come in after the deletion.
	e.exec(`DELETE FROM ohs_data.sleep_sessions WHERE tenant_id = $1 AND user_id = $2`, otherTenant, otherUser)
	e.process(e.upload(otherTenant, otherUser, "prov", "records", lines[0]))
	assert.Equal(t, 1, e.countFor("sleep_sessions", otherTenant, otherUser))
}

// explain returns the plan of q as one string.
func (e *env) explain(q string, args ...any) string {
	e.t.Helper()
	conn, err := e.pool.Acquire(context.Background())
	require.NoError(e.t, err)
	defer conn.Release()
	_, err = conn.Exec(context.Background(), `SET enable_seqscan = off`)
	require.NoError(e.t, err)
	rows, err := conn.Query(context.Background(), q, args...)
	require.NoError(e.t, err)
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(e.t, err)
	return strings.Join(lines, "\n")
}

// A provider deletion names only an id, so on compressed chunks TimescaleDB
// decompresses the user's batches to find its samples: more rows than the
// default per-transaction decompression limit must not fail the job.
func TestDeletion_RemovesSamplesInCompressedChunks(t *testing.T) {
	e := newEnv(t)
	e.register("prov", recordsMap)
	e.process(e.upload(e.tenant, e.user, "prov", "records", rec("samples", "r-9", deletionBase.Add(-60*24*time.Hour))))
	// A dense heart rate history of the same user, over the limit of 100,000.
	e.write(WriterConfig{}, &model.MapResult{QuantitySamples: points(120_000, deletionBase.Add(-90*24*time.Hour))})
	e.compressQuantity()
	require.EqualValues(t, 0, e.scalar(`SELECT count(*) FROM timescaledb_information.chunks
		WHERE hypertable_name = 'quantity_samples' AND NOT is_compressed`), "every chunk is compressed")

	c := e.process(e.upload(e.tenant, e.user, "prov", "records", del("r-9")))
	assert.Equal(t, TableCount{Written: 1}, c[tDeleted])
	assert.EqualValues(t, 0, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2 AND provider = 'prov'`, e.tenant, e.user))
	assert.Equal(t, 120_000, e.countFor("quantity_samples", e.tenant, e.user))
}
