// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/polar/polarfake"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// fixtureItem returns item i of a recorded list, as one JSON line.
func fixtureItem(t *testing.T, file, key string, i int) string {
	t.Helper()
	items, err := decodeList(polarfake.File(file), key)
	require.NoError(t, err)
	line, err := compact(items[i])
	require.NoError(t, err)
	return string(line)
}

func TestMapExercise(t *testing.T) {
	res, err := mapLine(t, DataTypeExercise, fixtureItem(t, "exercises.json", "exercises", 0))
	require.NoError(t, err)
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	assert.Equal(t, testUserID, w.UserID)
	assert.Equal(t, Slug, w.Provider)
	assert.Equal(t, "EXC0001", w.ExternalID)
	assert.Equal(t, "running", w.ActivityType)
	assert.Equal(t, time.Date(2026, 3, 5, 6, 30, 0, 0, time.UTC), w.StartDate, "07:30 local at +60 min")
	assert.Equal(t, time.Date(2026, 3, 5, 7, 30, 30, 0, time.UTC), w.EndDate)
	require.NotNil(t, w.DurationS)
	assert.Equal(t, 3630, *w.DurationS)
	assert.Equal(t, 500.0, *w.ActiveEnergyKcal)
	assert.Equal(t, 10000.0, *w.DistanceM)
	assert.Equal(t, 140.0, *w.AvgHeartRate)
	assert.Equal(t, 170.0, *w.MaxHeartRate)
	assert.Equal(t, "Etc/GMT-1", w.Metadata["timezone"])
	assert.Equal(t, 60, w.Metadata["utc_offset_minutes"])
	assert.Equal(t, "TRAIL_RUNNING", w.Metadata["polar_detailed_sport"])
	assert.Equal(t, "RUNNING", w.Metadata["polar_sport"])
	assert.Equal(t, 100.0, w.Metadata["training_load"])
	assert.NotEmpty(t, w.ContentHash)

	// An edited exercise (same Polar id) keeps its identity.
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(fixtureItem(t, "exercises.json", "exercises", 0)), &m))
	m["calories"] = 510
	m["duration"] = "PT1H5M"
	b, _ := json.Marshal(m)
	res2, err := mapLine(t, DataTypeExercise, string(b))
	require.NoError(t, err)
	assert.Equal(t, w.ContentHash, res2.Workouts[0].ContentHash)
	assert.True(t, w.Replaceable, "an edited exercise replaces the stored workout")
	assert.True(t, res2.Workouts[0].Replaceable)

	// A moved start keeps the identity too: the hash is the Polar id alone.
	m["start_time"] = "2026-03-05T07:45:00"
	b, _ = json.Marshal(m)
	res4, err := mapLine(t, DataTypeExercise, string(b))
	require.NoError(t, err)
	assert.Equal(t, w.ContentHash, res4.Workouts[0].ContentHash)
	assert.Equal(t, w.ExternalID, res4.Workouts[0].ExternalID)

	res3, err := mapLine(t, DataTypeExercise, fixtureItem(t, "exercises.json", "exercises", 1))
	require.NoError(t, err)
	assert.Equal(t, "cycling", res3.Workouts[0].ActivityType)
	assert.Equal(t, true, res3.Workouts[0].Metadata["indoor"])
	assert.Nil(t, res3.Workouts[0].DistanceM)
}

func TestMapExercise_Edges(t *testing.T) {
	res, err := mapLine(t, DataTypeExercise, `{"id":"X1","start_time":"2026-03-05T07:30:00","sport":"UNHEARD_OF"}`)
	require.NoError(t, err)
	w := res.Workouts[0]
	assert.Equal(t, "other", w.ActivityType)
	assert.Equal(t, time.Date(2026, 3, 5, 7, 30, 0, 0, time.UTC), w.StartDate, "no offset: read as UTC")
	assert.Nil(t, w.DurationS)
	assert.NotContains(t, w.Metadata, "timezone")

	res, err = mapLine(t, DataTypeExercise, `{"id":"X2","start_time":"2026-03-05T07:30:00","start_time_utc_offset":330,"duration":"PT30M"}`)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 3, 5, 2, 0, 0, 0, time.UTC), res.Workouts[0].StartDate)
	assert.NotContains(t, res.Workouts[0].Metadata, "timezone", "+05:30 has no Etc zone")
	assert.Equal(t, 330, res.Workouts[0].Metadata["utc_offset_minutes"])

	for name, line := range map[string]string{
		"no id":        `{"start_time":"2026-03-05T07:30:00"}`,
		"no start":     `{"id":"X"}`,
		"bad start":    `{"id":"X","start_time":"yesterday"}`,
		"bad duration": `{"id":"X","start_time":"2026-03-05T07:30:00","duration":"1 hour"}`,
		"wrong type":   `{"id":7,"start_time":"2026-03-05T07:30:00"}`,
	} {
		_, err := mapLine(t, DataTypeExercise, line)
		assert.Error(t, err, name)
	}
}

func TestMapSleep(t *testing.T) {
	res, err := mapLine(t, DataTypeSleep, fixtureItem(t, "sleep.json", "nights", 0))
	require.NoError(t, err)
	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	assert.Equal(t, "2026-03-04", s.ExternalID)
	assert.Equal(t, time.Date(2026, 3, 3, 22, 30, 0, 0, time.UTC), s.BedtimeStart)
	assert.Equal(t, time.Date(2026, 3, 4, 6, 0, 0, 0, time.UTC), s.BedtimeEnd)
	assert.Equal(t, 14400, *s.LightSleepSeconds)
	assert.Equal(t, 5400, *s.DeepSleepSeconds)
	assert.Equal(t, 5400, *s.REMSleepSeconds)
	assert.Equal(t, 1800, *s.AwakeSeconds)
	assert.Equal(t, 25200, *s.TotalSleepSeconds)
	assert.Equal(t, 80.0, *s.SleepScore)
	assert.Nil(t, s.HRVAvgMs)
	assert.Equal(t, "Etc/GMT-1", s.Metadata["timezone"])
	assert.Equal(t, 3.5, s.Metadata["continuity"])

	type ph struct {
		phase string
		start time.Time
		secs  int
	}
	var got []ph
	for _, p := range res.SleepPhases {
		assert.Equal(t, s.ContentHash, p.ParentContentHash)
		assert.Equal(t, int(p.EndDate.Sub(p.StartDate).Seconds()), p.DurationS)
		got = append(got, ph{p.Phase, p.StartDate, p.DurationS})
	}
	at := func(d, h, m int) time.Time { return time.Date(2026, 3, d, h, m, 0, 0, time.UTC) }
	assert.Equal(t, []ph{
		{model.SleepStageAwake, at(3, 22, 30), 600},
		{model.SleepStageLight, at(3, 22, 40), 3000},
		{model.SleepStageDeep, at(3, 23, 30), 5400},
		{model.SleepStageLight, at(4, 1, 0), 7200},
		{model.SleepStageREM, at(4, 3, 0), 5400},
		// 05:30 local is stage 5 (unknown): dropped.
		{model.SleepStageLight, at(4, 5, 0), 3000},
		{model.SleepStageAwake, at(4, 5, 50), 600},
	}, got)

	require.Len(t, res.QuantitySamples, 2)
	assert.Equal(t, model.QTHeartRate, res.QuantitySamples[0].QuantityType)
	assert.Equal(t, at(3, 22, 35), res.QuantitySamples[0].StartDate)
	assert.Equal(t, 60.0, res.QuantitySamples[0].Value)
	assert.Equal(t, at(3, 23, 35), res.QuantitySamples[1].StartDate)
	assert.Equal(t, "sleep", res.QuantitySamples[1].Metadata["context"])

	// The same night re-scored keeps its identity.
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(fixtureItem(t, "sleep.json", "nights", 0)), &m))
	m["sleep_score"] = 85
	b, _ := json.Marshal(m)
	res2, err := mapLine(t, DataTypeSleep, string(b))
	require.NoError(t, err)
	assert.Equal(t, s.ContentHash, res2.SleepSessions[0].ContentHash)
	assert.True(t, s.Replaceable, "a re-scored night replaces the stored session and its phases")
	assert.True(t, res2.SleepSessions[0].Replaceable)
	assert.Equal(t, s.ContentHash, res2.SleepPhases[0].ParentContentHash)
}

func TestMapSleep_Edges(t *testing.T) {
	res, err := mapLine(t, DataTypeSleep, fixtureItem(t, "sleep.json", "nights", 1))
	require.NoError(t, err)
	assert.Len(t, res.SleepSessions, 1)
	assert.Empty(t, res.SleepPhases)
	assert.Nil(t, res.SleepSessions[0].AwakeSeconds)

	for name, line := range map[string]string{
		"no date":       `{"sleep_start_time":"2026-03-03T23:30:00+01:00","sleep_end_time":"2026-03-04T07:00:00+01:00"}`,
		"no start":      `{"date":"2026-03-04","sleep_end_time":"2026-03-04T07:00:00+01:00"}`,
		"bad end":       `{"date":"2026-03-04","sleep_start_time":"2026-03-03T23:30:00+01:00","sleep_end_time":"soon"}`,
		"bad hypnogram": `{"date":"2026-03-04","sleep_start_time":"2026-03-03T23:30:00+01:00","sleep_end_time":"2026-03-04T07:00:00+01:00","hypnogram":{"25:99":1}}`,
		"wrong type":    `{"date":"2026-03-04","sleep_start_time":"2026-03-03T23:30:00+01:00","sleep_end_time":"2026-03-04T07:00:00+01:00","light_sleep":"lots"}`,
	} {
		_, err := mapLine(t, DataTypeSleep, line)
		assert.Error(t, err, name)
	}
}

func TestMapNightlyRecharge(t *testing.T) {
	res, err := mapLine(t, DataTypeNightlyRecharge, fixtureItem(t, "nightly_recharge.json", "recharges", 0))
	require.NoError(t, err)
	byType := map[string]model.QuantitySample{}
	for _, q := range res.QuantitySamples {
		byType[q.QuantityType] = q
		assert.True(t, q.Replaceable)
		assert.Equal(t, time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), q.StartDate)
		assert.Equal(t, 1090.0, q.Metadata["beat_to_beat_avg_ms"])
		assert.Equal(t, 55.0, q.Metadata["heart_rate_avg"])
	}
	require.Len(t, byType, 2)
	assert.NotContains(t, byType, model.QTHRVSDNN, "Polar HRV is RMSSD, never SDNN")
	assert.Equal(t, 45.0, byType[model.QTHRVRMSSD].Value)
	assert.Equal(t, "ms", byType[model.QTHRVRMSSD].Unit)
	assert.Equal(t, 14.5, byType[model.QTRespiratoryRate].Value)
	assert.Equal(t, "bpm", byType[model.QTRespiratoryRate].Unit)
	assert.Equal(t, 2.5, byType[model.QTHRVRMSSD].Metadata["ans_charge"])

	// A recomputed night replaces: the hash leaves the value out.
	res2, err := mapLine(t, DataTypeNightlyRecharge, `{"date":"2026-03-04","heart_rate_variability_avg":47}`)
	require.NoError(t, err)
	require.Len(t, res2.QuantitySamples, 1)
	assert.Equal(t, byType[model.QTHRVRMSSD].ContentHash, res2.QuantitySamples[0].ContentHash)

	res3, err := mapLine(t, DataTypeNightlyRecharge, `{"date":"2026-03-04"}`)
	require.NoError(t, err)
	assert.True(t, res3.IsEmpty(), "missing values are skipped")

	_, err = mapLine(t, DataTypeNightlyRecharge, `{"heart_rate_variability_avg":47}`)
	assert.Error(t, err)
}

func TestMapActivity(t *testing.T) {
	line := fixtureItem(t, "activities.json", "activities", 0)
	res, err := mapLine(t, DataTypeActivity, line)
	require.NoError(t, err)
	byType := map[string]model.QuantitySample{}
	for _, q := range res.QuantitySamples {
		byType[q.QuantityType] = q
		assert.True(t, q.Replaceable)
		assert.Equal(t, time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), q.StartDate)
		assert.Equal(t, time.Date(2026, 3, 4, 23, 59, 59, 0, time.UTC), q.EndDate)
		assert.Equal(t, 2500.0, q.Metadata["total_calories"])
		assert.Equal(t, 11400, q.Metadata["active_duration_s"])
	}
	require.Len(t, byType, 3)
	assert.Equal(t, 10000.0, byType[model.QTStepCount].Value)
	assert.Equal(t, "count", byType[model.QTStepCount].Unit)
	assert.Equal(t, 700.0, byType[model.QTActiveEnergyBurned].Value)
	assert.Equal(t, 7500.0, byType[model.QTDistanceWalkingRunning].Value)

	res2, err := mapLine(t, DataTypeActivity, `{"start_time":"2026-03-04T00:00:00","steps":12000}`)
	require.NoError(t, err)
	assert.Equal(t, byType[model.QTStepCount].ContentHash, res2.QuantitySamples[0].ContentHash, "a later total replaces")

	_, err = mapLine(t, DataTypeActivity, `{"steps":1}`)
	assert.Error(t, err)
	_, err = mapLine(t, DataTypeActivity, `{"start_time":"2026-03-04T00:00:00","active_duration":"three hours","steps":1}`)
	assert.Error(t, err)
}

func TestMapContinuousHR(t *testing.T) {
	line := fixtureItem(t, "continuous_hr.json", "heart_rates", 0)
	res, err := mapLine(t, DataTypeContinuousHR, line)
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 2, "a zero reading is skipped")
	q := res.QuantitySamples[0]
	assert.Equal(t, model.QTHeartRate, q.QuantityType)
	assert.Equal(t, "bpm", q.Unit)
	assert.Equal(t, 62.0, q.Value)
	assert.Equal(t, time.Date(2026, 3, 4, 8, 0, 0, 0, time.UTC), q.StartDate, "no offset: wall clock read as UTC")
	assert.Equal(t, true, q.Metadata["local_time_as_utc"])
	assert.True(t, q.Replaceable)

	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &m))
	m["utc_offset_minutes"] = -300
	b, _ := json.Marshal(m)
	res, err = mapLine(t, DataTypeContinuousHR, string(b))
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 3, 4, 13, 0, 0, 0, time.UTC), res.QuantitySamples[0].StartDate)
	assert.Equal(t, "Etc/GMT+5", res.QuantitySamples[0].Metadata["timezone"])

	_, err = mapLine(t, DataTypeContinuousHR, `{"date":"2026-03-04","heart_rate_samples":[{"heart_rate":60,"sample_time":"8 am"}]}`)
	assert.Error(t, err)
	_, err = mapLine(t, DataTypeContinuousHR, `{"heart_rate_samples":[]}`)
	assert.Error(t, err)
}

func TestMapPhysicalInfo(t *testing.T) {
	res, err := mapLine(t, DataTypePhysicalInfo, string(polarfake.File("physical_info.json")))
	require.NoError(t, err)
	byType := map[string]model.QuantitySample{}
	for _, q := range res.QuantitySamples {
		byType[q.QuantityType] = q
		assert.Equal(t, time.Date(2026, 3, 6, 8, 0, 0, 0, time.UTC), q.StartDate, "modified time")
		assert.Equal(t, 190.0, q.Metadata["maximum_heart_rate"])
	}
	require.Len(t, byType, 4)
	assert.Equal(t, 70.5, byType["body_mass"].Value)
	assert.Equal(t, "kg", byType["body_mass"].Unit)
	assert.Equal(t, 175.0, byType["height"].Value)
	assert.Equal(t, "cm", byType["height"].Unit)
	assert.Equal(t, 50.0, byType[model.QTRestingHeartRate].Value)
	assert.Equal(t, 48.0, byType["vo2_max"].Value)
	assert.Equal(t, "mL/kg/min", byType["vo2_max"].Unit)

	res, err = mapLine(t, DataTypePhysicalInfo, `{"weight":70,"fetched_at":"2026-03-10T15:04:05Z"}`)
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 1)
	assert.Equal(t, time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), res.QuantitySamples[0].StartDate, "pull time: stamped at the day")

	_, err = mapLine(t, DataTypePhysicalInfo, `{"weight":70}`)
	assert.Error(t, err)
}

func TestMap_UnknownTypeIsEmpty(t *testing.T) {
	res, err := mapLine(t, "sleep_wise", `{"anything":1}`)
	require.NoError(t, err)
	assert.True(t, res.IsEmpty())
}

func TestMap_MalformedIsError(t *testing.T) {
	for _, dt := range AllDataTypes {
		_, err := mapLine(t, dt, `{"date":12,"start_time":12,"id":12,"heart_rate_samples":"x","modified":5}`)
		assert.Error(t, err, dt)
	}
}
