// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/garmin/garminfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func byType(res model.MapResult) map[string][]model.QuantitySample {
	out := map[string][]model.QuantitySample{}
	for _, s := range res.QuantitySamples {
		out[s.QuantityType] = append(out[s.QuantityType], s)
	}
	return out
}

func TestMapDaily(t *testing.T) {
	res := mapFixture(t, DataTypeDailies)
	require.Len(t, res.DailySummaries, 1)
	d := res.DailySummaries[0]
	assert.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), d.SummaryDate)
	assert.Equal(t, 8000, *d.StepCount)
	assert.Equal(t, 6400.5, *d.DistanceM)
	assert.Equal(t, 400.0, *d.ActiveKcal)
	assert.Equal(t, 2000.0, *d.TotalKcal, "active + BMR")
	assert.Equal(t, 10, *d.FloorsClimbed)
	assert.Equal(t, 55.0, *d.RestingHR)
	assert.True(t, d.Replaceable)
	assert.Equal(t, "Etc/GMT-1", d.Metadata["timezone"])
	assert.Equal(t, int64(3600), d.Metadata["utc_offset_seconds"])
	assert.Equal(t, 30.0, d.Metadata["average_stress_level"])

	q := byType(res)
	start := time.Unix(1772319600, 0).UTC()
	for qt, want := range map[string]struct {
		v    float64
		unit string
	}{
		model.QTStepCount:              {8000, "count"},
		model.QTDistanceWalkingRunning: {6400.5, "m"},
		model.QTActiveEnergyBurned:     {400, "kcal"},
		model.QTBasalEnergyBurned:      {1600, "kcal"},
		model.QTFlightsClimbed:         {10, "count"},
		model.QTRestingHeartRate:       {55, "bpm"},
	} {
		require.Len(t, q[qt], 1, qt)
		s := q[qt][0]
		assert.Equal(t, want.v, s.Value, qt)
		assert.Equal(t, want.unit, s.Unit, qt)
		assert.Equal(t, start, s.StartDate)
		assert.Equal(t, start.Add(24*time.Hour), s.EndDate)
		assert.True(t, s.Replaceable)
		assert.Equal(t, "2026-03-01", s.Metadata[model.MetadataSummaryDate])
		assert.Equal(t, "garmin", s.Provider)
		assert.Equal(t, "Garmin", s.SourceName)
	}
	hr := q[model.QTHeartRate]
	require.Len(t, hr, 2, "the zero reading is dropped")
	assert.Equal(t, start.Add(15*time.Minute), hr[1].StartDate)
	assert.Equal(t, 60.0, hr[1].Value)
	assert.False(t, hr[0].Replaceable)
}

func TestMapDaily_HashIgnoresValueAndDependsOnDate(t *testing.T) {
	line := garminfake.Summaries(DataTypeDailies)[0]
	a := mapLine(t, DataTypeDailies, line)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(line, &obj))
	obj["steps"] = 9000
	more, _ := json.Marshal(obj)
	b := mapLine(t, DataTypeDailies, more)
	assert.Equal(t, a.DailySummaries[0].ContentHash, b.DailySummaries[0].ContentHash, "a growing day replaces the stored one")
	assert.Equal(t, byType(a)[model.QTStepCount][0].ContentHash, byType(b)[model.QTStepCount][0].ContentHash)
	obj["calendarDate"] = "2026-03-02"
	other, _ := json.Marshal(obj)
	c := mapLine(t, DataTypeDailies, other)
	assert.NotEqual(t, a.DailySummaries[0].ContentHash, c.DailySummaries[0].ContentHash)
}

func TestMapEpochs_ActivityTypeIsPartOfIdentity(t *testing.T) {
	items := garminfake.Summaries(DataTypeEpochs)
	a := byType(mapLine(t, DataTypeEpochs, items[0]))
	b := byType(mapLine(t, DataTypeEpochs, items[1]))
	require.Len(t, a[model.QTStepCountIntraday], 1)
	assert.Equal(t, 500.0, a[model.QTStepCountIntraday][0].Value)
	assert.Equal(t, 400.0, a[model.QTDistanceWalkRunIntday][0].Value)
	assert.Equal(t, "m", a[model.QTDistanceWalkRunIntday][0].Unit)
	assert.Equal(t, 25.0, a[model.QTActiveEnergyIntraday][0].Value)
	assert.Equal(t, 15*time.Minute, a[model.QTStepCountIntraday][0].EndDate.Sub(a[model.QTStepCountIntraday][0].StartDate))
	assert.NotEqual(t, a[model.QTStepCountIntraday][0].ContentHash, b[model.QTStepCountIntraday][0].ContentHash,
		"walking and sedentary epochs of one slice are two samples")
	assert.Equal(t, "WALKING", a[model.QTStepCountIntraday][0].Metadata["activity_type"])
}

func TestMapSleep(t *testing.T) {
	res := mapFixture(t, DataTypeSleeps)
	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	assert.Equal(t, time.Unix(1772404200, 0).UTC(), s.BedtimeStart)
	assert.Equal(t, time.Unix(1772433000, 0).UTC(), s.BedtimeEnd)
	assert.Equal(t, 27000, *s.TotalSleepSeconds, "deep + light + REM")
	assert.Equal(t, 5400, *s.DeepSleepSeconds)
	assert.Equal(t, 14400, *s.LightSleepSeconds)
	assert.Equal(t, 7200, *s.REMSleepSeconds)
	assert.Equal(t, 1800, *s.AwakeSeconds)
	assert.Equal(t, 82.0, *s.SleepScore)
	assert.Equal(t, 96.0, *s.SpO2Avg)
	assert.Equal(t, 14.5, *s.RespiratoryRate)
	assert.Equal(t, "sleep_2026-03-02", s.ExternalID)
	assert.True(t, s.Replaceable)
	assert.Equal(t, "GOOD", s.Metadata["sleep_score_qualifier"])

	require.Len(t, res.SleepPhases, 4)
	want := []string{model.SleepStageDeep, model.SleepStageLight, model.SleepStageREM, model.SleepStageAwake}
	for i, p := range res.SleepPhases {
		assert.Equal(t, want[i], p.Phase)
		assert.Equal(t, s.ContentHash, p.ParentContentHash)
		assert.Equal(t, int(p.EndDate.Sub(p.StartDate)/time.Second), p.DurationS)
	}
	assert.Empty(t, res.QuantitySamples)
}

func TestMapSleep_WithoutLevelsUsesDuration(t *testing.T) {
	res := mapLine(t, DataTypeSleeps, []byte(`{"summaryId":"s","calendarDate":"2026-03-02","startTimeInSeconds":1772404200,"durationInSeconds":3600}`))
	require.Len(t, res.SleepSessions, 1)
	assert.Equal(t, time.Hour, res.SleepSessions[0].BedtimeEnd.Sub(res.SleepSessions[0].BedtimeStart))
	assert.Equal(t, 3600, *res.SleepSessions[0].TotalSleepSeconds)
	assert.Empty(t, res.SleepPhases)
}

func TestMapActivity(t *testing.T) {
	res := mapFixture(t, DataTypeActivities)
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	assert.Equal(t, "running", w.ActivityType)
	assert.Equal(t, "12345678901", w.ExternalID)
	assert.Equal(t, time.Unix(1772467200, 0).UTC(), w.StartDate)
	assert.Equal(t, 30*time.Minute, w.EndDate.Sub(w.StartDate))
	assert.Equal(t, 1800, *w.DurationS)
	assert.Equal(t, 5000.0, *w.DistanceM)
	assert.Equal(t, 350.0, *w.ActiveEnergyKcal)
	assert.Equal(t, 150.0, *w.AvgHeartRate)
	assert.Equal(t, 172.0, *w.MaxHeartRate)
	assert.Equal(t, 2.78, *w.AvgSpeedMps)
	assert.Equal(t, 40.0, *w.ElevationGainM)
	assert.True(t, w.Replaceable)
	assert.Equal(t, "RUNNING", w.Metadata["garmin_activity_type"])
	assert.Equal(t, "Etc/GMT-1", w.Metadata["timezone"])
}

func TestActivityTypeTable(t *testing.T) {
	for in, want := range map[string]workoutType{
		"RUNNING":             {activity: "running"},
		"treadmill_running":   {activity: "running", indoor: true},
		"LAP_SWIMMING":        {activity: "swimming", indoor: true},
		"OPEN_WATER_SWIMMING": {activity: "swimming_open_water"},
		"STRENGTH_TRAINING":   {activity: "traditional_strength_training"},
		"INDOOR_ROWING":       {activity: "rowing", indoor: true},
		"RESORT_SNOWBOARDING": {activity: "snowboarding"},
		"SOMETHING_NEW":       {activity: "other"},
		"":                    {activity: "other"},
	} {
		assert.Equal(t, want, activityType(in), in)
	}
}

func TestMapBodyComp_GramsToKilograms(t *testing.T) {
	q := byType(mapFixture(t, DataTypeBodyComps))
	at := time.Unix(1772434800, 0).UTC()
	for qt, want := range map[string]struct {
		v    float64
		unit string
	}{
		"body_mass":           {72.5, "kg"},
		"muscle_mass":         {33, "kg"},
		"bone_mass":           {3.1, "kg"},
		"body_fat_percentage": {18.5, "percent"},
		"body_mass_index":     {22.4, "kg/m2"},
	} {
		require.Len(t, q[qt], 1, qt)
		assert.InDelta(t, want.v, q[qt][0].Value, 1e-9, qt)
		assert.Equal(t, want.unit, q[qt][0].Unit, qt)
		assert.Equal(t, at, q[qt][0].StartDate)
		assert.Equal(t, 55.2, q[qt][0].Metadata["body_water_percent"])
	}
	assert.Len(t, q, 5, "body water stays in metadata")
}

func TestMapBloodPressure(t *testing.T) {
	q := byType(mapFixture(t, DataTypeBloodPressures))
	assert.Equal(t, 118.0, q["blood_pressure_systolic"][0].Value)
	assert.Equal(t, "mmHg", q["blood_pressure_systolic"][0].Unit)
	assert.Equal(t, 76.0, q["blood_pressure_diastolic"][0].Value)
	assert.Equal(t, 62.0, q[model.QTHeartRate][0].Value)
	assert.Equal(t, "bpm", q[model.QTHeartRate][0].Unit)
}

func TestMapHRV_IsRMSSD(t *testing.T) {
	q := byType(mapFixture(t, DataTypeHRV))
	require.Len(t, q[model.QTHRVRMSSD], 1)
	assert.Empty(t, q[model.QTHRVSDNN], "Garmin's value is RMSSD, never SDNN")
	s := q[model.QTHRVRMSSD][0]
	assert.Equal(t, 42.0, s.Value)
	assert.Equal(t, "ms", s.Unit)
	assert.True(t, s.Replaceable)
	assert.Equal(t, 8*time.Hour, s.EndDate.Sub(s.StartDate))
}

func TestMapPulseOxAndRespiration(t *testing.T) {
	ox := byType(mapFixture(t, DataTypePulseOx))[model.QTOxygenSaturation]
	require.Len(t, ox, 2, "the implausible 12 percent is dropped")
	assert.Equal(t, 96.0, ox[0].Value)
	assert.Equal(t, "percent", ox[0].Unit)
	assert.Equal(t, time.Unix(1772404260, 0).UTC(), ox[1].StartDate)

	rr := byType(mapFixture(t, DataTypeRespiration))[model.QTRespiratoryRate]
	require.Len(t, rr, 2)
	assert.Equal(t, 14.5, rr[0].Value)
	assert.Equal(t, "bpm", rr[0].Unit)
}

func TestMapUserMetricsAndSkinTemp(t *testing.T) {
	v := byType(mapFixture(t, DataTypeUserMetrics))["vo2_max"]
	require.Len(t, v, 1)
	assert.Equal(t, 48.0, v[0].Value)
	assert.Equal(t, "mL/kg/min", v[0].Unit)
	assert.Equal(t, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), v[0].StartDate)
	assert.Equal(t, 35.0, v[0].Metadata["fitness_age"])

	st := byType(mapFixture(t, DataTypeSkinTemp))[model.QTBodyTemperatureDeviation]
	require.Len(t, st, 1)
	assert.Equal(t, -0.3, st[0].Value)
	assert.Equal(t, "celsius", st[0].Unit)
}

func TestMap_RawOnlyUnknownAndMalformed(t *testing.T) {
	assert.True(t, func() bool { r := mapFixture(t, DataTypeStressDetails); return r.IsEmpty() }(), "stress stays raw")

	c := New(Options{})
	res, err := c.Map(connector.RawRecord{DataType: "activityDetails", Fields: map[string]any{"x": 1.0}})
	require.NoError(t, err)
	assert.True(t, res.IsEmpty())

	for dt, fields := range map[string]map[string]any{
		DataTypeDailies:    {"steps": 5.0},
		DataTypeSleeps:     {"startTimeInSeconds": "x"},
		DataTypeActivities: {"activityId": 1.0},
		DataTypeBodyComps:  {"weightInGrams": 1.0},
		DataTypeUserMetrics: {
			"calendarDate": "03/02/2026", "vo2Max": 40.0,
		},
	} {
		_, err := c.Map(connector.RawRecord{DataType: dt, Fields: fields})
		assert.Error(t, err, dt)
	}

	// A missing value is skipped, not an error.
	res, err = c.Map(connector.RawRecord{DataType: DataTypeHRV, Fields: map[string]any{"calendarDate": "2026-03-02"}})
	require.NoError(t, err)
	assert.True(t, res.IsEmpty())
}

func TestMap_IsDeterministic(t *testing.T) {
	for _, dt := range AllDataTypes {
		for _, it := range garminfake.Summaries(dt) {
			a, _ := json.Marshal(mapLine(t, dt, it))
			b, _ := json.Marshal(mapLine(t, dt, it))
			assert.JSONEq(t, string(a), string(b), dt)
		}
	}
}

func TestOffsetZone(t *testing.T) {
	assert.Equal(t, "Etc/UTC", offsetZone(0))
	assert.Equal(t, "Etc/GMT-2", offsetZone(7200))
	assert.Equal(t, "Etc/GMT+5", offsetZone(-18000))
	assert.Equal(t, "", offsetZone(19800), "a half hour offset has no Etc zone")
	md := withOffset(map[string]any{}, ptr(int64(19800)))
	assert.Equal(t, int64(19800), md["utc_offset_seconds"])
	assert.NotContains(t, md, "timezone")
	assert.Empty(t, withOffset(map[string]any{}, ptr(int64(99999))))
}

func ptr[T any](v T) *T { return &v }
