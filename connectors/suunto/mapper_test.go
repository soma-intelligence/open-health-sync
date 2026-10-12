// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/suunto/suuntofake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const testUser = "00000000-0000-0000-0000-000000000001"

// mapLine maps one raw line the way the ETL does: parsed into fields first.
func mapLine(t *testing.T, dataType string, line []byte) (model.MapResult, error) {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal(line, &fields))
	return New(Options{}).Map(connector.RawRecord{UserID: testUser, Provider: Slug, DataType: dataType, Fields: fields})
}

func mapWorkoutFixture(t *testing.T, i int) model.Workout {
	t.Helper()
	res, err := mapLine(t, DataTypeWorkout, suuntofake.Workouts()[i])
	require.NoError(t, err)
	require.Len(t, res.Workouts, 1)
	return res.Workouts[0]
}

func TestMapRun(t *testing.T) {
	w := mapWorkoutFixture(t, 0)
	assert.Equal(t, testUser, w.UserID)
	assert.Equal(t, Slug, w.Provider)
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaa0001", w.ExternalID)
	assert.Equal(t, "running", w.ActivityType)
	assert.Equal(t, time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC), w.StartDate)
	assert.Equal(t, time.Date(2026, 3, 2, 10, 2, 0, 0, time.UTC), w.EndDate, "stopTime, pauses included")
	require.NotNil(t, w.DurationS)
	assert.Equal(t, 3600, *w.DurationS, "totalTime, rounded")
	assert.InDelta(t, 650, *w.ActiveEnergyKcal, 1e-9, "energyConsumption is kcal: no conversion")
	assert.InDelta(t, 10000, *w.DistanceM, 1e-9)
	assert.InDelta(t, 2.777, *w.AvgSpeedMps, 1e-9)
	assert.InDelta(t, 120, *w.ElevationGainM, 1e-9)
	assert.InDelta(t, 150, *w.AvgHeartRate, 1e-9)
	assert.InDelta(t, 180, *w.MaxHeartRate, 1e-9, "workoutMaxHR, not the user's max (195)")
	assert.Nil(t, w.VO2Max)
	assert.True(t, w.Replaceable)
	assert.NotEmpty(t, w.ContentHash)

	md := w.Metadata
	assert.Equal(t, "+01:00", md["utc_offset"])
	assert.Equal(t, 1, md["suunto_activity_id"])
	assert.Equal(t, "Running", md["suunto_activity"])
	assert.Equal(t, "running", md["activity_type"])
	assert.Equal(t, "900001", md["suunto_workout_id"])
	assert.InDelta(t, 3600.4, md["total_time_s"], 1e-9)
	assert.InDelta(t, 118, md["total_descent_m"], 1e-9)
	assert.InDelta(t, 9000, md["step_count"], 1e-9)
	assert.InDelta(t, 4.5, md["max_speed_mps"], 1e-9)
	assert.InDelta(t, 90, md["min_heart_rate"], 1e-9)
	assert.Equal(t, "Invented Watch 1", md["device"])
	assert.Equal(t, "1.0.0", md["device_sw_version"])
	assert.NotContains(t, md, "indoor")
}

func TestMapIndoorCyclingWithFallbacks(t *testing.T) {
	w := mapWorkoutFixture(t, 1)
	assert.Equal(t, "cycling", w.ActivityType)
	assert.Equal(t, true, w.Metadata["indoor"])
	assert.Equal(t, "Indoor cycling", w.Metadata["suunto_activity"])
	assert.Nil(t, w.DistanceM, "a zero distance is not a measurement")
	assert.Nil(t, w.AvgSpeedMps)
	assert.Equal(t, w.StartDate.Add(45*time.Minute), w.EndDate, "no stopTime: start + totalTime")
	assert.InDelta(t, 140, *w.AvgHeartRate, 1e-9, "avg when workoutAvgHR is missing")
	assert.InDelta(t, 170, *w.MaxHeartRate, 1e-9, "hrmax, never max")
	assert.Equal(t, "-05:30", w.Metadata["utc_offset"])
}

func TestMapUnknownActivity(t *testing.T) {
	w := mapWorkoutFixture(t, 3)
	assert.Equal(t, "other", w.ActivityType)
	assert.Equal(t, 999, w.Metadata["suunto_activity_id"])
	assert.NotContains(t, w.Metadata, "suunto_activity")
	assert.Nil(t, w.ActiveEnergyKcal)
	assert.Nil(t, w.AvgHeartRate)
}

func TestMapHashStableAcrossEdits(t *testing.T) {
	a := mapWorkoutFixture(t, 0)
	var edited map[string]any
	require.NoError(t, json.Unmarshal(suuntofake.Workouts()[0], &edited))
	edited["activityId"] = 22
	edited["energyConsumption"] = 700
	line, err := json.Marshal(edited)
	require.NoError(t, err)
	res, err := mapLine(t, DataTypeWorkout, line)
	require.NoError(t, err)
	b := res.Workouts[0]
	assert.Equal(t, a.ContentHash, b.ContentHash, "an edited workout replaces the stored one")
	assert.InDelta(t, 700, *b.ActiveEnergyKcal, 1e-9)
	assert.Equal(t, "Trail running", b.Metadata["suunto_activity"])

	other := mapWorkoutFixture(t, 2)
	assert.NotEqual(t, a.ContentHash, other.ContentHash)
}

func TestMapWorkoutIDFallback(t *testing.T) {
	res, err := mapLine(t, DataTypeWorkout, []byte(`{"workoutId":42,"startTime":1772442000000,"activityId":0,"totalTime":60}`))
	require.NoError(t, err)
	assert.Equal(t, "42", res.Workouts[0].ExternalID)
	assert.Equal(t, "walking", res.Workouts[0].ActivityType)
	assert.NotContains(t, res.Workouts[0].Metadata, "suunto_workout_id")
}

func TestMapImplausibleFigures(t *testing.T) {
	res, err := mapLine(t, DataTypeWorkout, []byte(`{"workoutKey":"k","startTime":1772442000000,"stopTime":1772441000000,
		"totalTime":-5,"totalAscent":-3,"energyConsumption":-1,"timeOffsetInMinutes":5000,
		"hrdata":{"workoutAvgHR":0,"avg":400,"workoutMaxHR":-1}}`))
	require.NoError(t, err)
	w := res.Workouts[0]
	assert.Equal(t, w.StartDate, w.EndDate, "a stop before the start is ignored")
	assert.Nil(t, w.DurationS)
	assert.Nil(t, w.ElevationGainM)
	assert.Nil(t, w.ActiveEnergyKcal)
	assert.Nil(t, w.AvgHeartRate)
	assert.Nil(t, w.MaxHeartRate)
	assert.NotContains(t, w.Metadata, "utc_offset")
}

func TestMapWorkoutErrors(t *testing.T) {
	for _, line := range []string{
		`{"startTime":1772442000000}`,
		`{"workoutKey":"k"}`,
		`{"workoutKey":"k","startTime":0}`,
		`{"workoutKey":"k","startTime":"soon"}`,
	} {
		_, err := mapLine(t, DataTypeWorkout, []byte(line))
		assert.Error(t, err, line)
	}
}

func TestMapRawOnlyAndUnknown(t *testing.T) {
	for _, dt := range []string{DataTypeSleep, DataTypeRecovery, DataTypeDailyActivity, "something_else"} {
		res, err := mapLine(t, dt, []byte(`{"a":1}`))
		require.NoError(t, err, dt)
		assert.Equal(t, model.MapResult{}, res, dt)
	}
}

func TestOffsetString(t *testing.T) {
	for in, want := range map[int]string{0: "+00:00", 60: "+01:00", -330: "-05:30", 345: "+05:45", 840: "+14:00"} {
		got, ok := offsetString(in)
		assert.True(t, ok)
		assert.Equal(t, want, got)
	}
	_, ok := offsetString(19 * 60)
	assert.False(t, ok)
}

// Every activity maps to a canonical name other connectors use too.
func TestActivityTable(t *testing.T) {
	known := map[string]bool{}
	for _, n := range []string{
		"walking", "running", "cycling", "cross_country_skiing", "other", "hiking", "skating_sports",
		"downhill_skiing", "paddle_sports", "rowing", "golf", "functional_strength_training", "swimming",
		"traditional_strength_training", "equestrian_sports", "water_sports", "climbing", "snowboarding",
		"snow_sports", "mixed_cardio", "soccer", "tennis", "basketball", "badminton", "baseball", "volleyball",
		"american_football", "table_tennis", "racquetball", "squash", "hockey", "handball", "softball", "bowling",
		"cricket", "rugby", "yoga", "cross_training", "elliptical", "flexibility", "track_and_field",
		"martial_arts", "dance", "disc_sports", "sailing", "boxing", "underwater_diving", "gymnastics",
		"swimming_open_water", "surfing_sports", "fishing", "hunting", "jump_rope", "hand_cycling",
		"mind_and_body", "pilates",
	} {
		known[n] = true
	}
	for id, s := range activities {
		assert.True(t, known[s.activity], "activity %d maps to %q", id, s.activity)
	}
	assert.Equal(t, "other", activity(-1).activity)
}
