// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func mapFixture(t *testing.T, dataType, file, id string) model.MapResult {
	t.Helper()
	res, err := New(Options{}).Map(record(t, dataType, fixture(t, file, id)))
	require.NoError(t, err)
	return res
}

func mapLine(t *testing.T, dataType, line string) (model.MapResult, error) {
	t.Helper()
	return New(Options{}).Map(record(t, dataType, []byte(line)))
}

func TestMapActivity(t *testing.T) {
	res := mapFixture(t, DataTypeActivity, "activities.json", "i1001")
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	start := time.Date(2026, 3, 1, 7, 0, 0, 0, time.UTC)
	assert.Equal(t, Slug, w.Provider)
	assert.Equal(t, "user-1", w.UserID)
	assert.Equal(t, "i1001", w.ExternalID)
	assert.Equal(t, "cycling", w.ActivityType)
	assert.Equal(t, start, w.StartDate)
	assert.Equal(t, start.Add(3900*time.Second), w.EndDate, "end from elapsed_time")
	assert.Equal(t, 3600, *w.DurationS, "duration is moving_time")
	assert.Equal(t, 800.0, *w.ActiveEnergyKcal)
	assert.Equal(t, 30000.0, *w.DistanceM)
	assert.Equal(t, 250.0, *w.ElevationGainM)
	assert.Equal(t, 140.0, *w.AvgHeartRate)
	assert.Equal(t, 170.0, *w.MaxHeartRate)
	assert.Equal(t, 8.33, *w.AvgSpeedMps)
	assert.True(t, w.Replaceable)
	assert.Equal(t, "Europe/Paris", w.Metadata["timezone"])
	assert.Equal(t, "GARMIN_CONNECT", w.Metadata["intervals_source"])
	assert.Equal(t, "garmin", w.Metadata["source_provider"], "upstream attribution")
	assert.Equal(t, "Test Bike Computer", w.Metadata["device_name"])
	assert.Equal(t, "calories", w.Metadata["active_energy_source"])
	assert.Equal(t, "Ride", w.Metadata["intervals_type"])
	assert.Nil(t, w.Metadata["indoor"])
	assert.NotContains(t, w.Metadata, "name", "free text is not collected")

	again := mapFixture(t, DataTypeActivity, "activities.json", "i1001")
	assert.Equal(t, w.ContentHash, again.Workouts[0].ContentHash, "hash is stable")
}

func TestMapActivityJoulesAndIndoor(t *testing.T) {
	res := mapFixture(t, DataTypeActivity, "activities.json", "i1002")
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	assert.Equal(t, "cycling", w.ActivityType)
	assert.Equal(t, true, w.Metadata["indoor"])
	assert.Equal(t, "zwift", w.Metadata["source_provider"])
	require.NotNil(t, w.ActiveEnergyKcal)
	assert.InDelta(t, 500.0, *w.ActiveEnergyKcal, 1e-9, "500 kJ of work shown as 500 kcal")
	assert.Equal(t, "joules", w.Metadata["active_energy_source"])
	assert.Nil(t, w.AvgHeartRate, "null heart rate stays empty")
	assert.Nil(t, w.MaxHeartRate)
}

func TestMapActivityZeroDistance(t *testing.T) {
	res := mapFixture(t, DataTypeActivity, "activities.json", "i1004")
	w := res.Workouts[0]
	assert.Equal(t, "traditional_strength_training", w.ActivityType)
	assert.Nil(t, w.DistanceM)
	assert.Nil(t, w.AvgSpeedMps)
	assert.Equal(t, "upload", w.Metadata["source_provider"])
}

func TestMapActivityStravaStub(t *testing.T) {
	res := mapFixture(t, DataTypeActivity, "activities.json", "i1003")
	assert.True(t, res.IsEmpty())
}

func TestMapActivityLocalStart(t *testing.T) {
	res, err := mapLine(t, DataTypeActivity, `{"id":"i5","type":"Kitesurf","start_date_local":"2026-07-01T10:00:00","timezone":"America/New_York","elapsed_time":600}`)
	require.NoError(t, err)
	w := res.Workouts[0]
	assert.Equal(t, time.Date(2026, 7, 1, 14, 0, 0, 0, time.UTC), w.StartDate, "EDT is UTC-4")
	assert.Equal(t, "surfing_sports", w.ActivityType)
	assert.Equal(t, 600, *w.DurationS)
}

func TestMapActivityNumericIDAndUnknownType(t *testing.T) {
	res, err := mapLine(t, DataTypeActivity, `{"id":77,"type":"Underwater Basket Weaving","start_date":"2026-03-01T07:00:00Z"}`)
	require.NoError(t, err)
	w := res.Workouts[0]
	assert.Equal(t, "77", w.ExternalID)
	assert.Equal(t, "other", w.ActivityType)
	assert.Equal(t, w.StartDate, w.EndDate)
	assert.Nil(t, w.DurationS)
	assert.NotContains(t, w.Metadata, "timezone")
}

func TestMapActivityErrors(t *testing.T) {
	for _, line := range []string{
		`{"type":"Run","start_date":"2026-03-01T07:00:00Z"}`,
		`{"id":"i1","type":"Run"}`,
		`{"id":"i1","type":"Run","start_date":"yesterday"}`,
		`{"id":"i1","type":"Run","start_date_local":"2026-03-01T07:00:00"}`,
		`{"id":"i1","type":"Run","start_date":"2026-03-01T07:00:00Z","moving_time":"long"}`,
	} {
		_, err := mapLine(t, DataTypeActivity, line)
		assert.Error(t, err, line)
	}
}

func samplesByType(res model.MapResult) map[string]model.QuantitySample {
	out := map[string]model.QuantitySample{}
	for _, s := range res.QuantitySamples {
		out[s.QuantityType] = s
	}
	return out
}

func TestMapWellness(t *testing.T) {
	res := mapFixture(t, DataTypeWellness, "wellness.json", "2026-03-01")
	got := samplesByType(res)
	want := map[string]struct {
		value float64
		unit  string
	}{
		"body_mass":                {70.5, "kg"},
		"resting_heart_rate":       {52, "bpm"},
		"hrv_rmssd":                {65, "ms"},
		"hrv_sdnn":                 {80, "ms"},
		"sleep_score":              {85, "score"},
		"step_count":               {9000, "count"},
		"oxygen_saturation":        {97, "percent"},
		"respiratory_rate":         {14.5, "bpm"},
		"body_fat_percentage":      {18.5, "percent"},
		"vo2_max":                  {52, "mL/kg/min"},
		"blood_pressure_systolic":  {120, "mmHg"},
		"blood_pressure_diastolic": {80, "mmHg"},
	}
	require.Len(t, got, len(want))
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for qt, w := range want {
		s, ok := got[qt]
		require.True(t, ok, qt)
		assert.Equal(t, w.value, s.Value, qt)
		assert.Equal(t, w.unit, s.Unit, qt)
		su, known := model.StorageUnit(qt)
		require.True(t, known, "%s is a canonical quantity", qt)
		assert.Equal(t, su, s.Unit, "%s is emitted in its storage unit", qt)
		assert.Equal(t, start, s.StartDate, qt)
		assert.Equal(t, start.Add(24*time.Hour-time.Second), s.EndDate, qt)
		assert.Equal(t, "2026-03-01_"+qt, s.ExternalID)
		assert.True(t, s.Replaceable)
		assert.Equal(t, Slug, s.Provider)
		assert.Equal(t, 27000.0, s.Metadata["sleep_secs"], "no canonical sleep duration: kept in metadata")
		assert.Equal(t, 2400.0, s.Metadata["kcal_consumed"])
	}
	assert.NotEqual(t, got["body_mass"].ContentHash, got["step_count"].ContentHash)

	// The value is not hashed: a revised figure replaces the stored one.
	revised, err := mapLine(t, DataTypeWellness, `{"id":"2026-03-01","weight":70.1}`)
	require.NoError(t, err)
	assert.Equal(t, got["body_mass"].ContentHash, revised.QuantitySamples[0].ContentHash)
}

func TestMapWellnessNullsAndZeros(t *testing.T) {
	res := mapFixture(t, DataTypeWellness, "wellness.json", "2026-03-02")
	got := samplesByType(res)
	assert.Len(t, got, 1, "null weight, hrv, sleep and zero steps give nothing")
	assert.Equal(t, 54.0, got["resting_heart_rate"].Value)

	res, err := mapLine(t, DataTypeWellness, `{"id":"2026-03-05","updated":"2026-03-05T07:00:00Z"}`)
	require.NoError(t, err)
	assert.True(t, res.IsEmpty())
}

func TestMapWellnessFractionsConvertToPercent(t *testing.T) {
	res := mapFixture(t, DataTypeWellness, "wellness.json", "2026-03-03")
	require.Len(t, res.QuantitySamples, 2)
	for _, s := range res.QuantitySamples {
		assert.Equal(t, "fraction", s.Unit)
	}
	require.Empty(t, model.CanonicalizeUnits(&res))
	got := samplesByType(res)
	assert.InDelta(t, 96.0, got["oxygen_saturation"].Value, 1e-9)
	assert.InDelta(t, 18.5, got["body_fat_percentage"].Value, 1e-9)
	assert.Equal(t, "percent", got["oxygen_saturation"].Unit)
}

func TestMapWellnessErrors(t *testing.T) {
	for _, line := range []string{
		`{"weight":70}`,
		`{"id":"March 1st","weight":70}`,
		`{"id":"2026-03-01","weight":"heavy"}`,
	} {
		_, err := mapLine(t, DataTypeWellness, line)
		assert.Error(t, err, line)
	}
}

// An elapsed or moving time too large for a time.Duration used to wrap:
// 2^55 + 3600 seconds put the end one hour after the start while the stored
// duration read 36028797018967568 s. Such a figure is not a duration.
func TestMapActivityDurationThatOverflowsIsDropped(t *testing.T) {
	res, err := mapLine(t, DataTypeActivity, `{"id":"i1","type":"Run","start_date":"2026-03-01T07:00:00Z",
		"elapsed_time":36028797018967568,"moving_time":36028797018967568}`)
	require.NoError(t, err)
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	assert.True(t, w.EndDate.Equal(w.StartDate))
	assert.Nil(t, w.DurationS)
	assert.NotContains(t, w.Metadata, "elapsed_time_s")
	assert.NotContains(t, w.Metadata, "moving_time_s")
}

func TestMapUnknownDataType(t *testing.T) {
	res, err := mapLine(t, "calendar", `{"id":"x"}`)
	require.NoError(t, err)
	assert.True(t, res.IsEmpty())
}

func TestUpstreamSlug(t *testing.T) {
	for in, want := range map[string]string{
		"GARMIN_CONNECT": "garmin",
		"wahoo":          "wahoo",
		" COROS ":        "coros",
		"OAUTH_CLIENT":   "oauth_client",
		"":               "",
	} {
		assert.Equal(t, want, upstreamSlug(in), in)
	}
}
