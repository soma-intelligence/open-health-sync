// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/oura/ourafake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const testUserID = "00000000-0000-0000-0000-0000000000aa"

// mapLine runs Map on one raw line of dataType, the way the ETL hands it over
// (a json.Unmarshal into map[string]any).
func mapLine(t *testing.T, dataType, line string) (model.MapResult, error) {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &fields))
	return New(Options{}).Map(connector.RawRecord{
		TenantID: "00000000-0000-0000-0000-0000000000bb", UserID: testUserID,
		Provider: Slug, DataType: dataType, SourceKey: "raw/test", Fields: fields,
	})
}

func mustMap(t *testing.T, dataType, line string) model.MapResult {
	t.Helper()
	res, err := mapLine(t, dataType, line)
	require.NoError(t, err)
	return res
}

// fixture returns document i of a recorded answer, as one line.
func fixture(t *testing.T, dataType string, i int) string {
	t.Helper()
	var p struct {
		Data []json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(ourafake.File(dataType+".json"), &p))
	require.Greater(t, len(p.Data), i)
	return string(p.Data[i])
}

// edit returns line with the given fields replaced.
func edit(t *testing.T, line string, fields map[string]any) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &m))
	for k, v := range fields {
		m[k] = v
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

func byType(samples []model.QuantitySample) map[string]model.QuantitySample {
	out := map[string]model.QuantitySample{}
	for _, s := range samples {
		out[s.QuantityType] = s
	}
	return out
}

// Every quantity name the connector emits is canonical, with its storage
// unit, so nothing is parked as a unit error.
func assertCanonical(t *testing.T, samples []model.QuantitySample) {
	t.Helper()
	for _, s := range samples {
		unit, known := model.StorageUnit(s.QuantityType)
		require.True(t, known, "%s is a canonical name", s.QuantityType)
		assert.Equal(t, unit, s.Unit, s.QuantityType)
		assert.Equal(t, Slug, s.Provider)
		assert.Equal(t, Slug, s.SourceSlug)
		assert.Equal(t, testUserID, s.UserID)
		assert.NotEmpty(t, s.ContentHash)
	}
}

func paris(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Etc/GMT-1")
	require.NoError(t, err)
	return loc
}

// --- daily_activity ---

func TestMapDailyActivity(t *testing.T) {
	line := fixture(t, DataTypeDailyActivity, 0)
	res := mustMap(t, DataTypeDailyActivity, line)
	require.Len(t, res.QuantitySamples, 5)
	require.Empty(t, model.CanonicalizeUnits(&res), "seconds convert to the storage unit")
	assertCanonical(t, res.QuantitySamples)
	m := byType(res.QuantitySamples)

	assert.InDelta(t, 10000.0, m[model.QTStepCount].Value, 1e-9)
	assert.Equal(t, "count", m[model.QTStepCount].Unit)
	assert.InDelta(t, 500.0, m[model.QTActiveEnergyBurned].Value, 1e-9)
	assert.Equal(t, "kcal", m[model.QTActiveEnergyBurned].Unit)
	assert.InDelta(t, 8000.0, m[model.QTDistanceWalkingRunning].Value, 1e-9)
	assert.Equal(t, "m", m[model.QTDistanceWalkingRunning].Unit)

	s := m[model.QTStepCount]
	loc := paris(t)
	assert.True(t, time.Date(2026, 3, 1, 0, 0, 0, 0, loc).Equal(s.StartDate), "local midnight, %s", s.StartDate)
	assert.True(t, time.Date(2026, 3, 1, 23, 59, 59, 0, loc).Equal(s.EndDate))
	assert.Equal(t, time.UTC, s.StartDate.Location())
	assert.Equal(t, "Etc/GMT-1", s.Metadata["timezone"], "the +01:00 offset as an IANA zone")
	assert.Equal(t, "+01:00", s.Metadata["utc_offset"])
	assert.Equal(t, 80.0, s.Metadata["activity_score"])
	assert.Equal(t, 2500.0, s.Metadata["total_calories"])
	assert.Equal(t, "2026-03-01_step_count", s.ExternalID)
	assert.True(t, s.Replaceable)
	assert.False(t, s.SupersedeMoved, "keyed by the day, not by one record's start")
	assert.True(t, s.IsPrimarySource)
}

// Oura's medium and high activity time (seconds) are the day's moderate and
// vigorous minutes, over the same local day as the steps.
func TestMapDailyActivity_IntensityMinutes(t *testing.T) {
	res := mustMap(t, DataTypeDailyActivity, fixture(t, DataTypeDailyActivity, 0))
	require.Empty(t, model.CanonicalizeUnits(&res))
	m := byType(res.QuantitySamples)

	moderate, vigorous := m[model.QTModerateActivityTime], m[model.QTVigorousActivityTime]
	assert.InDelta(t, 60.0, moderate.Value, 1e-9, "3600 s of medium activity")
	assert.InDelta(t, 10.0, vigorous.Value, 1e-9, "600 s of high activity")
	for _, s := range []model.QuantitySample{moderate, vigorous} {
		assert.Equal(t, "min", s.Unit)
		assert.True(t, m[model.QTStepCount].StartDate.Equal(s.StartDate), "the steps' day")
		assert.True(t, m[model.QTStepCount].EndDate.Equal(s.EndDate))
		assert.True(t, s.Replaceable)
		assert.Equal(t, "2026-03-01_"+s.QuantityType, s.ExternalID)
	}
	assert.NotEqual(t, moderate.ContentHash, vigorous.ContentHash)
	assert.Equal(t, 3600.0, moderate.Metadata["medium_activity_time"], "the metadata copy stays")

	// A day with no high activity stores a zero; a figure Oura leaves out gives no sample.
	res = mustMap(t, DataTypeDailyActivity, fixture(t, DataTypeDailyActivity, 1))
	require.Empty(t, model.CanonicalizeUnits(&res))
	assert.InDelta(t, 0.0, byType(res.QuantitySamples)[model.QTVigorousActivityTime].Value, 1e-9)
	line := edit(t, fixture(t, DataTypeDailyActivity, 0), map[string]any{"medium_activity_time": nil})
	m = byType(mustMap(t, DataTypeDailyActivity, line).QuantitySamples)
	assert.NotContains(t, m, model.QTModerateActivityTime)
	assert.Contains(t, m, model.QTVigorousActivityTime)
}

// A day's document re-sent with new values keeps its identity, so the
// writer replaces the stored value (Replaceable) instead of adding a row.
func TestMapDailyActivity_UpdateReplaces(t *testing.T) {
	line := fixture(t, DataTypeDailyActivity, 0)
	a := byType(mustMap(t, DataTypeDailyActivity, line).QuantitySamples)
	b := byType(mustMap(t, DataTypeDailyActivity, edit(t, line, map[string]any{"steps": 12345, "score": 90})).QuantitySamples)
	assert.Equal(t, a[model.QTStepCount].ContentHash, b[model.QTStepCount].ContentHash)
	assert.True(t, a[model.QTStepCount].StartDate.Equal(b[model.QTStepCount].StartDate), "same conflict key")
	assert.InDelta(t, 12345.0, b[model.QTStepCount].Value, 1e-9)
	assert.True(t, b[model.QTStepCount].Replaceable)

	other := byType(mustMap(t, DataTypeDailyActivity, fixture(t, DataTypeDailyActivity, 1)).QuantitySamples)
	assert.NotEqual(t, a[model.QTStepCount].ContentHash, other[model.QTStepCount].ContentHash, "another day")
	assert.NotEqual(t, a[model.QTStepCount].ContentHash, a[model.QTActiveEnergyBurned].ContentHash, "another type")
}

func TestMapDailyActivity_MissingValuesSkipped(t *testing.T) {
	res := mustMap(t, DataTypeDailyActivity, `{"id":"a","day":"2026-03-01","timestamp":"2026-03-01T04:00:00+01:00","steps":100}`)
	require.Len(t, res.QuantitySamples, 1)
	assert.Equal(t, model.QTStepCount, res.QuantitySamples[0].QuantityType)

	res = mustMap(t, DataTypeDailyActivity, `{"id":"a","day":"2026-03-01"}`)
	assert.True(t, res.IsEmpty())
}

// An offset that is not a whole number of hours names a zone using it, so the
// day is the user's local day.
func TestMapDailyActivity_HalfHourOffset(t *testing.T) {
	res := mustMap(t, DataTypeDailyActivity, `{"id":"a","day":"2026-03-01","timestamp":"2026-03-01T04:00:00+05:30","steps":100}`)
	require.Len(t, res.QuantitySamples, 1)
	s := res.QuantitySamples[0]
	assert.Equal(t, "Asia/Kolkata", s.Metadata["timezone"])
	assert.Equal(t, "+05:30", s.Metadata["utc_offset"])
	assert.True(t, time.Date(2026, 2, 28, 18, 30, 0, 0, time.UTC).Equal(s.StartDate), "local midnight")

	// An offset no zone uses: the day is a UTC day, and no timezone is claimed.
	res = mustMap(t, DataTypeDailyActivity, `{"id":"a","day":"2026-03-01","timestamp":"2026-03-01T04:00:00+05:20","steps":100}`)
	s = res.QuantitySamples[0]
	assert.NotContains(t, s.Metadata, "timezone")
	assert.True(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).Equal(s.StartDate))
}

// --- sleep ---

func TestMapSleep_Night(t *testing.T) {
	res := mustMap(t, DataTypeSleep, fixture(t, DataTypeSleep, 1))
	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	loc := paris(t)
	assert.Equal(t, "sleep-0001", s.ExternalID)
	assert.Equal(t, Slug, s.Provider)
	assert.Equal(t, testUserID, s.UserID)
	assert.True(t, time.Date(2026, 3, 1, 23, 0, 0, 0, loc).Equal(s.BedtimeStart))
	assert.True(t, time.Date(2026, 3, 2, 7, 0, 0, 0, loc).Equal(s.BedtimeEnd))
	assert.Equal(t, time.UTC, s.BedtimeStart.Location())
	assert.Equal(t, 27600, *s.TotalSleepSeconds)
	assert.Equal(t, 5400, *s.DeepSleepSeconds)
	assert.Equal(t, 6600, *s.REMSleepSeconds)
	assert.Equal(t, 15600, *s.LightSleepSeconds)
	assert.Equal(t, 1200, *s.AwakeSeconds)
	assert.InDelta(t, 40.0, *s.HRVAvgMs, 1e-9)
	assert.InDelta(t, 50.0, *s.RestingHR, 1e-9, "lowest heart rate")
	assert.InDelta(t, 15.0, *s.RespiratoryRate, 1e-9)
	assert.Nil(t, s.SleepScore, "the sleep score lives in daily_sleep")
	assert.Equal(t, "Etc/GMT-1", s.Metadata["timezone"])
	assert.Equal(t, "rmssd", s.Metadata["hrv_method"])
	assert.Equal(t, "long_sleep", s.Metadata["oura_type"])
	assert.Equal(t, "2026-03-02", s.Metadata["day"])
	assert.Equal(t, 55.0, s.Metadata["average_heart_rate"])

	// Stages from the hypnogram: runs merged, durations add up.
	require.Len(t, res.SleepPhases, 11)
	totals := map[string]int{}
	for i, p := range res.SleepPhases {
		assert.Equal(t, s.ContentHash, p.ParentContentHash)
		assert.Equal(t, testUserID, p.UserID)
		assert.Equal(t, int(p.EndDate.Sub(p.StartDate).Seconds()), p.DurationS)
		if i > 0 {
			assert.True(t, p.StartDate.Equal(res.SleepPhases[i-1].EndDate), "contiguous")
			assert.NotEqual(t, p.Phase, res.SleepPhases[i-1].Phase, "runs merged")
		}
		totals[p.Phase] += p.DurationS
	}
	assert.True(t, res.SleepPhases[0].StartDate.Equal(s.BedtimeStart))
	assert.True(t, res.SleepPhases[10].EndDate.Equal(s.BedtimeEnd))
	assert.Equal(t, map[string]int{
		model.SleepStageDeep: 5400, model.SleepStageLight: 15600,
		model.SleepStageREM: 6600, model.SleepStageAwake: 1200,
	}, totals)
	assert.Equal(t, model.SleepStageAwake, res.SleepPhases[0].Phase)
	assert.Equal(t, 600, res.SleepPhases[0].DurationS)

	// The main sleep re-emits resting heart rate and respiratory rate.
	require.Len(t, res.QuantitySamples, 2)
	assertCanonical(t, res.QuantitySamples)
	m := byType(res.QuantitySamples)
	assert.InDelta(t, 50.0, m[model.QTRestingHeartRate].Value, 1e-9)
	assert.Equal(t, "bpm", m[model.QTRestingHeartRate].Unit)
	assert.InDelta(t, 15.0, m[model.QTRespiratoryRate].Value, 1e-9)
	assert.True(t, m[model.QTRestingHeartRate].Replaceable)
	assert.True(t, m[model.QTRestingHeartRate].SupersedeMoved, "a night whose start moved replaces its samples")
	assert.True(t, m[model.QTRespiratoryRate].SupersedeMoved)
	assert.Equal(t, "Etc/GMT-1", m[model.QTRespiratoryRate].Metadata["timezone"])
	assert.Equal(t, "sleep-0001_resting_heart_rate", m[model.QTRestingHeartRate].ExternalID)
	_, hasSDNN := m[model.QTHRVSDNN]
	assert.False(t, hasSDNN, "Oura HRV is RMSSD, never hrv_sdnn")
}

// A nap is a session with its stages, without the resting figures.
func TestMapSleep_Nap(t *testing.T) {
	res := mustMap(t, DataTypeSleep, fixture(t, DataTypeSleep, 2))
	require.Len(t, res.SleepSessions, 1)
	assert.Equal(t, "late_nap", res.SleepSessions[0].Metadata["oura_type"])
	require.Len(t, res.SleepPhases, 2)
	assert.Equal(t, model.SleepStageAwake, res.SleepPhases[0].Phase)
	assert.Equal(t, model.SleepStageLight, res.SleepPhases[1].Phase)
	assert.Empty(t, res.QuantitySamples)
}

// A period Oura marks deleted, or one the user rejected (rest), maps to the
// deletion of its Oura id and nothing else.
func TestMapSleep_DeletedAndRestAreDeletions(t *testing.T) {
	res := mustMap(t, DataTypeSleep, fixture(t, DataTypeSleep, 0))
	assert.Equal(t, []model.Deletion{{Provider: Slug, ExternalID: "sleep-0003"}}, res.Deletions)
	assert.Equal(t, 1, res.RecordCount())

	night := fixture(t, DataTypeSleep, 1)
	for _, typ := range []string{"deleted", "rest"} {
		res = mustMap(t, DataTypeSleep, edit(t, night, map[string]any{"type": typ}))
		assert.Equal(t, []model.Deletion{{Provider: Slug, ExternalID: "sleep-0001"}}, res.Deletions, typ)
		assert.Empty(t, res.SleepSessions, typ)
		assert.Empty(t, res.QuantitySamples, typ)
	}
}

// The same Oura period re-sent with corrected values keeps its identity: no
// second session, and the re-emitted figures are replaced.
func TestMapSleep_UpdateKeepsIdentity(t *testing.T) {
	night := fixture(t, DataTypeSleep, 1)
	a := mustMap(t, DataTypeSleep, night)
	b := mustMap(t, DataTypeSleep, edit(t, night, map[string]any{
		"lowest_heart_rate": 48, "average_breath": 14.5, "deep_sleep_duration": 6000,
		"bedtime_end": "2026-03-02T07:10:00+01:00",
	}))
	assert.Equal(t, a.SleepSessions[0].ContentHash, b.SleepSessions[0].ContentHash)
	assert.True(t, a.SleepSessions[0].Replaceable, "a corrected night replaces the stored session")
	assert.True(t, b.SleepSessions[0].Replaceable)
	assert.Equal(t, a.SleepSessions[0].ContentHash, b.SleepPhases[0].ParentContentHash, "the new hypnogram hangs off the same session")
	ma, mb := byType(a.QuantitySamples), byType(b.QuantitySamples)
	for _, qt := range []string{model.QTRestingHeartRate, model.QTRespiratoryRate} {
		assert.Equal(t, ma[qt].ContentHash, mb[qt].ContentHash, qt)
		assert.True(t, ma[qt].StartDate.Equal(mb[qt].StartDate), qt)
	}
	assert.InDelta(t, 48.0, mb[model.QTRestingHeartRate].Value, 1e-9)

	other := mustMap(t, DataTypeSleep, edit(t, night, map[string]any{"id": "sleep-9999"}))
	assert.NotEqual(t, a.SleepSessions[0].ContentHash, other.SleepSessions[0].ContentHash, "another Oura id")
}

func TestHypnogram(t *testing.T) {
	start := time.Date(2026, 3, 1, 22, 0, 0, 0, time.UTC)
	end := start.Add(22 * time.Minute)
	before := DroppedHypnogramSteps()
	phases := hypnogram(testUserID, "11x23", start, end, "parent")
	require.Len(t, phases, 3)
	assert.Equal(t, model.SleepStageDeep, phases[0].Phase)
	assert.Equal(t, 600, phases[0].DurationS)
	assert.Equal(t, model.SleepStageLight, phases[1].Phase)
	assert.True(t, phases[1].StartDate.Equal(start.Add(15*time.Minute)), "the unknown step leaves a gap")
	assert.Equal(t, model.SleepStageREM, phases[2].Phase)
	assert.True(t, phases[2].EndDate.Equal(end), "clamped to bedtime_end")
	assert.Equal(t, 120, phases[2].DurationS)
	assert.Equal(t, int64(1), DroppedHypnogramSteps()-before)

	assert.Empty(t, hypnogram(testUserID, "", start, end, "parent"))
}

// --- daily_readiness ---

func TestMapDailyReadiness(t *testing.T) {
	res := mustMap(t, DataTypeDailyReadiness, fixture(t, DataTypeDailyReadiness, 0))
	require.Len(t, res.QuantitySamples, 3)
	assertCanonical(t, res.QuantitySamples)
	m := byType(res.QuantitySamples)
	assert.InDelta(t, 85.0, m[model.QTReadinessScore].Value, 1e-9)
	assert.Equal(t, "score", m[model.QTReadinessScore].Unit)
	assert.InDelta(t, 75.0, m[model.QTReadinessHRVBalance].Value, 1e-9)
	assert.Equal(t, "index", m[model.QTReadinessHRVBalance].Unit, "an index, never milliseconds")
	assert.InDelta(t, -0.2, m[model.QTBodyTemperatureDeviation].Value, 1e-9)
	assert.Equal(t, "celsius", m[model.QTBodyTemperatureDeviation].Unit)

	s := m[model.QTReadinessScore]
	assert.True(t, time.Date(2026, 3, 1, 0, 0, 0, 0, paris(t)).Equal(s.StartDate))
	assert.Equal(t, "Etc/GMT-1", s.Metadata["timezone"])
	assert.Equal(t, 0.1, s.Metadata["temperature_trend_deviation"])
	contributors, ok := s.Metadata["contributors"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 95.0, contributors["resting_heart_rate"])
	assert.True(t, s.Replaceable)

	upd := byType(mustMap(t, DataTypeDailyReadiness, edit(t, fixture(t, DataTypeDailyReadiness, 0), map[string]any{"score": 60})).QuantitySamples)
	assert.Equal(t, s.ContentHash, upd[model.QTReadinessScore].ContentHash, "the newest score replaces the stored one")
}

// --- daily_spo2 ---

func TestMapDailySpO2(t *testing.T) {
	res := mustMap(t, DataTypeDailySpO2, fixture(t, DataTypeDailySpO2, 0))
	require.Len(t, res.QuantitySamples, 1)
	assertCanonical(t, res.QuantitySamples)
	s := res.QuantitySamples[0]
	assert.Equal(t, model.QTOxygenSaturation, s.QuantityType)
	assert.InDelta(t, 97.0, s.Value, 1e-9)
	assert.Equal(t, "percent", s.Unit)
	assert.True(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).Equal(s.StartDate), "no timestamp: a UTC day")
	assert.Equal(t, 5.0, s.Metadata["breathing_disturbance_index"])
	assert.True(t, s.Replaceable)

	assert.True(t, isEmpty(mustMap(t, DataTypeDailySpO2, fixture(t, DataTypeDailySpO2, 1))), "no average, nothing stored")
}

// --- heartrate ---

func TestMapHeartRate(t *testing.T) {
	res := mustMap(t, DataTypeHeartRate, fixture(t, DataTypeHeartRate, 0))
	require.Len(t, res.QuantitySamples, 1)
	assertCanonical(t, res.QuantitySamples)
	s := res.QuantitySamples[0]
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	assert.Equal(t, model.QTHeartRate, s.QuantityType)
	assert.InDelta(t, 60.0, s.Value, 1e-9)
	assert.Equal(t, "bpm", s.Unit)
	assert.True(t, at.Equal(s.StartDate))
	assert.True(t, at.Equal(s.EndDate))
	assert.Equal(t, "awake", s.Metadata["oura_source"])
	assert.NotContains(t, s.Metadata, "timezone", "Oura sends heart rate in UTC: the user's zone is unknown")
	assert.False(t, s.Replaceable)

	other := mustMap(t, DataTypeHeartRate, fixture(t, DataTypeHeartRate, 1)).QuantitySamples[0]
	assert.NotEqual(t, s.ContentHash, other.ContentHash)

	assert.True(t, isEmpty(mustMap(t, DataTypeHeartRate, `{"bpm":0,"source":"rest","timestamp":"2026-03-01T10:00:00+00:00"}`)))
	assert.True(t, isEmpty(mustMap(t, DataTypeHeartRate, `{"bpm":null,"source":"rest","timestamp":"2026-03-01T10:00:00+00:00"}`)))
}

// --- workout ---

func TestMapWorkout(t *testing.T) {
	res := mustMap(t, DataTypeWorkout, fixture(t, DataTypeWorkout, 0))
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	loc := paris(t)
	assert.Equal(t, "workout-0001", w.ExternalID)
	assert.Equal(t, "running", w.ActivityType)
	assert.Equal(t, Slug, w.Provider)
	assert.True(t, time.Date(2026, 3, 2, 18, 0, 0, 0, loc).Equal(w.StartDate))
	assert.True(t, time.Date(2026, 3, 2, 18, 45, 0, 0, loc).Equal(w.EndDate))
	assert.Equal(t, 2700, *w.DurationS)
	assert.InDelta(t, 400.0, *w.ActiveEnergyKcal, 1e-9)
	assert.InDelta(t, 5000.0, *w.DistanceM, 1e-9)
	assert.Equal(t, "Etc/GMT-1", w.Metadata["timezone"])
	assert.Equal(t, "moderate", w.Metadata["intensity"])
	assert.Equal(t, "confirmed", w.Metadata["oura_source"])
	assert.NotContains(t, w.Metadata, "label", "a null label is left out")

	upd := mustMap(t, DataTypeWorkout, edit(t, fixture(t, DataTypeWorkout, 0), map[string]any{"calories": 420}))
	assert.Equal(t, w.ContentHash, upd.Workouts[0].ContentHash, "same Oura id and start: one workout")
	assert.True(t, w.Replaceable, "an edited workout replaces the stored one")
	assert.True(t, upd.Workouts[0].Replaceable)
}

func TestWorkoutActivity(t *testing.T) {
	cases := map[string]string{
		"running":           "running",
		"cycling":           "cycling",
		"strengthTraining":  "traditional_strength_training",
		"strength_training": "traditional_strength_training",
		"hiit":              "high_intensity_interval_training",
		"Yoga":              "yoga",
		"dancing":           "dance",
		"housework":         "other",
		"":                  "other",
	}
	for in, want := range cases {
		assert.Equal(t, want, workoutActivity(in), in)
	}
}

// --- contract cases ---

func TestMap_UnknownTypeIsEmpty(t *testing.T) {
	res, err := mapLine(t, "daily_stress", `{"id":"x","day":"2026-03-01"}`)
	require.NoError(t, err)
	assert.True(t, res.IsEmpty())
}

func TestMap_MalformedIsAnError(t *testing.T) {
	cases := []struct{ dataType, line string }{
		{DataTypeDailyActivity, `{"id":"a","steps":100}`},                                    // no day
		{DataTypeDailyActivity, `{"id":"a","day":"03/01/2026","steps":100}`},                 // bad day
		{DataTypeDailyActivity, `{"id":"a","day":"2026-03-01","timestamp":"x","steps":100}`}, // bad timestamp
		{DataTypeDailyActivity, `{"id":"a","day":"2026-03-01","steps":"many"}`},              // wrong type
		{DataTypeSleep, `{"type":"long_sleep","bedtime_start":"2026-03-01T23:00:00+01:00","bedtime_end":"2026-03-02T07:00:00+01:00"}`},
		{DataTypeSleep, `{"id":"s","type":"long_sleep","bedtime_end":"2026-03-02T07:00:00+01:00"}`},
		{DataTypeSleep, `{"id":"s","type":"long_sleep","bedtime_start":"2026-03-02T07:00:00+01:00","bedtime_end":"2026-03-01T23:00:00+01:00"}`},
		{DataTypeDailyReadiness, `{"id":"r","score":80}`},
		{DataTypeDailySpO2, `{"id":"o","spo2_percentage":{"average":97}}`},
		{DataTypeHeartRate, `{"bpm":60,"source":"awake"}`},
		{DataTypeHeartRate, `{"bpm":60,"source":"awake","timestamp":"yesterday"}`},
		{DataTypeWorkout, `{"activity":"running","start_datetime":"2026-03-02T18:00:00+01:00","end_datetime":"2026-03-02T18:45:00+01:00"}`},
		{DataTypeWorkout, `{"id":"w","activity":"running","start_datetime":"2026-03-02T18:00:00+01:00"}`},
	}
	for _, c := range cases {
		_, err := mapLine(t, c.dataType, c.line)
		assert.Error(t, err, "%s %s", c.dataType, c.line)
		if err != nil {
			assert.True(t, strings.HasPrefix(err.Error(), "oura: "), err.Error())
		}
	}
}

// Map is deterministic: the same line gives the same records and hashes.
func TestMap_StableHashes(t *testing.T) {
	for _, dt := range AllDataTypes {
		var p struct {
			Data []json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(ourafake.File(dt+".json"), &p))
		for _, doc := range p.Data {
			a := mustMap(t, dt, string(doc))
			b := mustMap(t, dt, string(doc))
			assert.Equal(t, a, b, "%s: Map is pure", dt)
			for i := range a.QuantitySamples {
				assert.Equal(t, a.QuantitySamples[i].ContentHash, b.QuantitySamples[i].ContentHash)
			}
		}
	}
}

// A different user never shares a hash.
func TestMap_HashesAreScopedToTheUser(t *testing.T) {
	line := fixture(t, DataTypeSleep, 1)
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &fields))
	c := New(Options{})
	a, err := c.Map(connector.RawRecord{UserID: "user-a", DataType: DataTypeSleep, Fields: fields})
	require.NoError(t, err)
	b, err := c.Map(connector.RawRecord{UserID: "user-b", DataType: DataTypeSleep, Fields: fields})
	require.NoError(t, err)
	assert.NotEqual(t, a.SleepSessions[0].ContentHash, b.SleepSessions[0].ContentHash)
}

func TestOffsetZone(t *testing.T) {
	cases := map[string]string{
		"2026-03-01T00:00:00Z":      "UTC",
		"2026-03-01T00:00:00+00:00": "UTC",
		"2026-03-01T00:00:00+01:00": "Etc/GMT-1",
		"2026-03-01T00:00:00-05:00": "Etc/GMT+5",
		"2026-03-01T00:00:00+14:00": "Etc/GMT-14",
		"2026-03-01T00:00:00+05:30": "Asia/Kolkata",
		"2026-03-01T00:00:00+05:45": "Asia/Kathmandu",
		"2026-03-01T00:00:00-03:30": "America/St_Johns",
		"2026-07-01T00:00:00-02:30": "America/St_Johns",
		"2026-03-01T00:00:00+09:30": "Australia/Darwin",
		"2026-01-01T00:00:00+10:30": "Australia/Adelaide",
		"2026-01-01T00:00:00+13:45": "Pacific/Chatham",
		"2026-03-01T00:00:00+05:20": "",
		"2026-03-01T00:00:00+15:00": "",
		"2026-03-01T00:00:00-12:00": "Etc/GMT+12",
	}
	for in, want := range cases {
		ts, err := parseTime(in)
		require.NoError(t, err)
		got, _ := offsetZone(ts)
		assert.Equal(t, want, got, in)
		if got != "" {
			_, err := time.LoadLocation(got)
			assert.NoError(t, err, got)
		}
	}
}

// isEmpty reports whether a returned result holds no record.
func isEmpty(r model.MapResult) bool { return r.IsEmpty() }
