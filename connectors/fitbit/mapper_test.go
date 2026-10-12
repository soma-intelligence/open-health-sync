// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// single maps a fixture and returns its only quantity sample.
func single(t *testing.T, dt string, line []byte) model.QuantitySample {
	t.Helper()
	res := mapLine(t, dt, line)
	require.Len(t, res.QuantitySamples, 1, dt)
	return res.QuantitySamples[0]
}

func TestMapQuantities(t *testing.T) {
	cases := []struct {
		dt, qt, unit string
		value        float64
		start, end   time.Time
		replaceable  bool
	}{
		{DataTypeSteps, "step_count_intraday", "count", 120, ts("2026-03-09T07:00:00Z"), ts("2026-03-09T07:01:00Z"), true},
		{DataTypeDistance, "distance_walking_running_intraday", "m", 90, ts("2026-03-09T07:00:00Z"), ts("2026-03-09T07:01:00Z"), true},
		{DataTypeFloors, "flights_climbed", "count", 2, ts("2026-03-09T07:00:00Z"), ts("2026-03-09T07:01:00Z"), true},
		{DataTypeActiveEnergy, "active_energy_burned_intraday", "kcal", 5.5, ts("2026-03-09T07:00:00Z"), ts("2026-03-09T07:01:00Z"), true},
		{DataTypeHeartRate, "heart_rate", "bpm", 62, ts("2026-03-09T07:00:00Z"), ts("2026-03-09T07:00:00Z"), false},
		{DataTypeHRV, "hrv_rmssd", "ms", 40, ts("2026-03-09T02:00:00Z"), ts("2026-03-09T02:00:00Z"), false},
		{DataTypeOxygenSaturation, "oxygen_saturation", "percent", 96, ts("2026-03-09T02:00:00Z"), ts("2026-03-09T02:00:00Z"), false},
		{DataTypeWeight, "body_mass", "kg", 72.5, ts("2026-03-09T06:30:00Z"), ts("2026-03-09T06:30:00Z"), false},
		{DataTypeBodyFat, "body_fat_percentage", "percent", 18.5, ts("2026-03-09T06:30:00Z"), ts("2026-03-09T06:30:00Z"), false},
		{DataTypeHeight, "height", "cm", 178, ts("2026-01-01T09:00:00Z"), ts("2026-01-01T09:00:00Z"), false},
	}
	for _, tc := range cases {
		s := single(t, tc.dt, fixture(t, tc.dt))
		assert.Equal(t, tc.qt, s.QuantityType, tc.dt)
		assert.Equal(t, tc.unit, s.Unit, tc.dt)
		assert.InDelta(t, tc.value, s.Value, 1e-9, tc.dt)
		assert.Equal(t, tc.start, s.StartDate, tc.dt)
		assert.Equal(t, tc.end, s.EndDate, tc.dt)
		assert.Equal(t, tc.replaceable, s.Replaceable, tc.dt)
		assert.Equal(t, Slug, s.Provider)
		assert.Equal(t, testUserID, s.UserID)
		assert.Equal(t, "+01:00", s.Metadata["utc_offset"], tc.dt)
		assert.Equal(t, "Etc/GMT-1", s.Metadata["timezone"], tc.dt)
		assert.Equal(t, tc.dt, s.Metadata["google_data_type"])
		assert.NotEmpty(t, s.ContentHash)
		// Deterministic.
		assert.Equal(t, s.ContentHash, single(t, tc.dt, fixture(t, tc.dt)).ContentHash, tc.dt)
	}
	hr := single(t, DataTypeHeartRate, fixture(t, DataTypeHeartRate))
	assert.Equal(t, "WRIST", hr.Metadata["sensor_location"])
	assert.Equal(t, "Charge 6", hr.Metadata["device_name"])
}

func TestMapIntervalHashIgnoresValue(t *testing.T) {
	a := single(t, DataTypeSteps, fixture(t, DataTypeSteps))
	b := single(t, DataTypeSteps, []byte(`{"dataSource":{"platform":"FITBIT","recordingMethod":"PASSIVELY_MEASURED","device":{"formFactor":"FITNESS_BAND","displayName":"Charge 6","manufacturer":"Fitbit"}},
		"steps":{"interval":{"startTime":"2026-03-09T07:00:00Z","startUtcOffset":"3600s","endTime":"2026-03-09T07:01:00Z","endUtcOffset":"3600s"},"count":"130"}}`))
	assert.Equal(t, a.ContentHash, b.ContentHash, "a revised minute replaces the stored one")
	c := single(t, DataTypeSteps, []byte(`{"dataSource":{"platform":"ANDROID"},
		"steps":{"interval":{"startTime":"2026-03-09T07:00:00Z","startUtcOffset":"3600s","endTime":"2026-03-09T07:01:00Z","endUtcOffset":"3600s"},"count":"120"}}`))
	assert.NotEqual(t, a.ContentHash, c.ContentHash, "another source of the same minute is kept apart")
}

func TestMapDailies(t *testing.T) {
	day := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		dt, qt, unit string
		value        float64
	}{
		{DataTypeDailyRestingHR, "resting_heart_rate", "bpm", 55},
		{DataTypeDailyHRV, "hrv_rmssd", "ms", 42.5},
		{DataTypeDailyOxygen, "oxygen_saturation", "percent", 95.5},
		{DataTypeDailyRespiratory, "respiratory_rate", "bpm", 14.5},
		{DataTypeDailyVO2Max, "vo2_max", "mL/kg/min", 45},
	}
	for _, tc := range cases {
		s := single(t, tc.dt, fixture(t, tc.dt))
		assert.Equal(t, tc.qt, s.QuantityType, tc.dt)
		assert.Equal(t, tc.unit, s.Unit, tc.dt)
		assert.InDelta(t, tc.value, s.Value, 1e-9, tc.dt)
		assert.Equal(t, day, s.StartDate, tc.dt)
		assert.Equal(t, day.Add(24*time.Hour-time.Second), s.EndDate, tc.dt)
		assert.True(t, s.Replaceable, tc.dt)
		assert.Equal(t, "2026-03-09", s.Metadata["date"])
		assert.NotContains(t, s.Metadata, "timezone", "a date claims no zone")
		assert.Equal(t, tc.dt+"_2026-03-09_"+tc.qt, s.ExternalID)
	}
	hrv := single(t, DataTypeDailyHRV, fixture(t, DataTypeDailyHRV))
	assert.Equal(t, "rmssd", hrv.Metadata["hrv_method"])
	assert.InDelta(t, 48.0, hrv.Metadata["deep_sleep_rmssd_ms"], 1e-9)
}

func TestMapSleepTemperature(t *testing.T) {
	res := mapLine(t, DataTypeSleepTemperature, fixture(t, DataTypeSleepTemperature))
	require.Len(t, res.QuantitySamples, 2)
	assert.Equal(t, "skin_temperature", res.QuantitySamples[0].QuantityType)
	assert.InDelta(t, 34.0, res.QuantitySamples[0].Value, 1e-9)
	assert.Equal(t, "body_temperature_deviation", res.QuantitySamples[1].QuantityType)
	assert.InDelta(t, 0.5, res.QuantitySamples[1].Value, 1e-9)
	assert.Equal(t, "celsius", res.QuantitySamples[1].Unit)
}

func TestMapOxygenFraction(t *testing.T) {
	s := single(t, DataTypeOxygenSaturation, []byte(`{"oxygenSaturation":{"sampleTime":{"physicalTime":"2026-03-09T02:00:00Z","utcOffset":"0s"},"percentage":0.97}}`))
	assert.InDelta(t, 97.0, s.Value, 1e-9, "a fraction is scaled to percent")
	assert.Equal(t, "UTC", s.Metadata["timezone"])
	res := mapLine(t, DataTypeOxygenSaturation, []byte(`{"oxygenSaturation":{"sampleTime":{"physicalTime":"2026-03-09T02:00:00Z","utcOffset":"0s"},"percentage":140}}`))
	assert.Empty(t, res.QuantitySamples, "an impossible percentage is dropped")
}

func TestMapSleep(t *testing.T) {
	res := mapLine(t, DataTypeSleep, fixture(t, DataTypeSleep))
	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	assert.Equal(t, "9000000001", s.ExternalID)
	assert.Equal(t, ts("2026-03-08T22:30:00Z"), s.BedtimeStart)
	assert.Equal(t, ts("2026-03-09T06:00:00Z"), s.BedtimeEnd)
	assert.Equal(t, 435*60, *s.TotalSleepSeconds)
	assert.Equal(t, 15*60, *s.AwakeSeconds)
	assert.Equal(t, 90*60, *s.DeepSleepSeconds)
	assert.Equal(t, 90*60, *s.REMSleepSeconds)
	assert.Equal(t, 255*60, *s.LightSleepSeconds)
	assert.True(t, s.Replaceable)
	assert.Equal(t, true, s.Metadata["main_sleep"])
	assert.Equal(t, "STAGES", s.Metadata["sleep_type"])
	assert.Equal(t, "Etc/GMT-1", s.Metadata["timezone"])

	require.Len(t, res.SleepPhases, 5)
	assert.Equal(t, []string{"awake", "light", "deep", "rem", "light"}, phases(res.SleepPhases))
	assert.Equal(t, 15*60, res.SleepPhases[0].DurationS)
	for _, p := range res.SleepPhases {
		assert.Equal(t, s.ContentHash, p.ParentContentHash)
	}
}

func phases(ps []model.SleepPhase) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Phase
	}
	return out
}

func TestMapClassicSleep(t *testing.T) {
	res := mapLine(t, DataTypeSleep, []byte(`{"sleep":{"type":"CLASSIC",
		"interval":{"startTime":"2026-03-09T13:00:00Z","startUtcOffset":"-18000s","endTime":"2026-03-09T14:00:00Z","endUtcOffset":"-18000s"},
		"stages":[{"type":"ASLEEP","startTime":"2026-03-09T13:00:00Z","endTime":"2026-03-09T13:40:00Z"},
		          {"type":"RESTLESS","startTime":"2026-03-09T13:40:00Z","endTime":"2026-03-09T13:45:00Z"},
		          {"type":"SLEEP_STAGE_TYPE_UNSPECIFIED","startTime":"2026-03-09T13:45:00Z","endTime":"2026-03-09T13:50:00Z"},
		          {"type":"ASLEEP","startTime":"2026-03-09T13:50:00Z","endTime":"2026-03-09T14:00:00Z"}],
		"summary":{"minutesAsleep":"50","stagesSummary":[{"type":"RESTLESS","minutes":"5"}]}}}`))
	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	assert.Equal(t, "sleep_1773061200", s.ExternalID, "no resource name: keyed by start")
	assert.Equal(t, "-05:00", s.Metadata["utc_offset"])
	assert.Equal(t, "Etc/GMT+5", s.Metadata["timezone"])
	assert.InDelta(t, 5.0, s.Metadata["minutes_restless"], 1e-9)
	assert.Nil(t, s.DeepSleepSeconds)
	assert.Equal(t, []string{"light", "awake", "light"}, phases(res.SleepPhases))
}

func TestMapExercise(t *testing.T) {
	res := mapLine(t, DataTypeExercise, fixture(t, DataTypeExercise))
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	assert.Equal(t, "7000000001", w.ExternalID)
	assert.Equal(t, "running", w.ActivityType)
	assert.Equal(t, ts("2026-03-09T17:00:00Z"), w.StartDate)
	assert.Equal(t, ts("2026-03-09T17:45:00Z"), w.EndDate)
	assert.Equal(t, 2400, *w.DurationS, "active duration, pauses excluded")
	assert.Equal(t, 2700, w.Metadata["elapsed_seconds"])
	assert.InDelta(t, 450.0, *w.ActiveEnergyKcal, 1e-9)
	assert.InDelta(t, 8000.0, *w.DistanceM, 1e-9, "mm to m")
	assert.InDelta(t, 50.0, *w.ElevationGainM, 1e-9, "mm to m")
	assert.InDelta(t, 3.333, *w.AvgSpeedMps, 1e-9, "mm/s to m/s")
	assert.InDelta(t, 150.0, *w.AvgHeartRate, 1e-9)
	assert.InDelta(t, 46.0, w.Metadata["run_vo2_max"], 1e-9)
	assert.Equal(t, "RUNNING", w.Metadata["google_exercise_type"])
	assert.Equal(t, true, w.Metadata["has_gps"])
	assert.True(t, w.Replaceable)
}

func TestExerciseActivity(t *testing.T) {
	for in, want := range map[string]string{
		"RUNNING": "running", "TREADMILL": "running", "OUTDOOR_BIKE": "cycling", "SWIMMING_POOL": "swimming",
		"HIIT": "high_intensity_interval_training", "WEIGHTS": "traditional_strength_training",
		"YOGA_VINYASA": "yoga", "PICKELBALL": "pickleball", "MOWING_LAWN": "other", "": "other",
		"FOOTBALL_AMERICAN": "american_football",
	} {
		assert.Equal(t, want, exerciseActivity(in), in)
	}
}

func TestMapEdgeCases(t *testing.T) {
	c := New(Options{})
	res, err := c.Map(connector.RawRecord{DataType: "nutrition-log", Fields: map[string]any{"x": 1.0}})
	require.NoError(t, err, "an unknown type maps to nothing")
	assert.Empty(t, res.QuantitySamples)

	_, err = c.Map(connector.RawRecord{DataType: DataTypeSteps, Fields: map[string]any{"floors": map[string]any{}}})
	require.Error(t, err, "a point without its member")

	_, err = c.Map(connector.RawRecord{DataType: DataTypeHeartRate, Fields: map[string]any{
		"heartRate": map[string]any{"sampleTime": map[string]any{"physicalTime": "yesterday"}, "beatsPerMinute": "60"}}})
	require.Error(t, err, "a bad timestamp")

	_, err = c.Map(connector.RawRecord{DataType: DataTypeSteps, Fields: map[string]any{
		"steps": map[string]any{"count": "lots"}}})
	require.Error(t, err, "a bad number")

	empty := mapLine(t, DataTypeHeartRate, []byte(`{"heartRate":{"sampleTime":{"physicalTime":"2026-03-09T07:00:00Z","utcOffset":"0s"}}}`))
	assert.Empty(t, empty.QuantitySamples, "a missing value is skipped")

	odd := single(t, DataTypeHeartRate, []byte(`{"heartRate":{"sampleTime":{"physicalTime":"2026-03-09T07:00:00Z","utcOffset":"19800s"},"beatsPerMinute":70}}`))
	assert.Equal(t, "+05:30", odd.Metadata["utc_offset"])
	assert.Equal(t, "Asia/Kolkata", odd.Metadata["timezone"])
	assert.InDelta(t, 70.0, odd.Value, 1e-9, "a number where a string is documented")
}
