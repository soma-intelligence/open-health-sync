// SPDX-License-Identifier: AGPL-3.0-only

package consent

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func TestOf(t *testing.T) {
	cases := map[string]string{
		"step_count":                   Activity,
		"active_energy_burned":         Activity,
		"running_power":                Activity,
		"apple_exercise_time":          Activity,
		"heart_rate":                   Heart,
		"hrv_rmssd":                    Heart,
		"readiness_hrv_balance":        Heart,
		"resting_heart_rate":           Heart,
		"ecg_classification":           ECG,
		"blood_pressure_systolic":      BloodPressure,
		"blood_glucose":                Glucose,
		"insulin_delivery":             Glucose,
		"oxygen_saturation":            Respiratory,
		"vo2_max":                      Respiratory,
		"body_mass":                    Body,
		"height":                       Body,
		"body_temperature":             Body,
		"sleep_analysis":               Sleep,
		"dietary_energy_consumed":      Nutrition,
		"menstrual_flow":               Reproductive,
		"basal_body_temperature":       Reproductive,
		"mindful_session":              Mental,
		"mood":                         Mental,
		"environmental_audio_exposure": Other,
		"HEART_RATE":                   Heart,
	}
	for name, want := range cases {
		assert.Equal(t, want, Of(name), name)
	}
	for _, c := range Categories {
		assert.True(t, Known(c), c)
	}
	assert.False(t, Known("genome"))
}

func TestFilter(t *testing.T) {
	mr := func() *model.MapResult {
		return &model.MapResult{
			QuantitySamples:  []model.QuantitySample{{QuantityType: "heart_rate"}, {QuantityType: "step_count"}},
			CategorySamples:  []model.CategorySample{{CategoryType: "menstrual_flow"}},
			Observations:     []model.Observation{{CanonicalCode: "blood_glucose"}, {CanonicalCode: "mood"}},
			SleepSessions:    []model.SleepSession{{}},
			SleepPhases:      []model.SleepPhase{{}, {}},
			Workouts:         []model.Workout{{}},
			RoutePoints:      []model.RoutePoint{{}, {}, {}},
			DailySummaries:   []model.DailySummary{{}},
			ECGRecordings:    []model.ECGRecording{{}},
			ECGSamples:       []model.ECGVoltageSample{{}, {}},
			HeartbeatSamples: []model.HeartbeatSample{{}},
			NutritionLogs:    []model.NutritionLog{{}},
		}
	}

	all := mr()
	assert.Zero(t, Filter(all, func(string) bool { return true }))
	assert.Len(t, all.QuantitySamples, 2)
	assert.Len(t, all.RoutePoints, 3)

	none := mr()
	assert.Equal(t, 18, Filter(none, func(string) bool { return false }), "route points count once")
	assert.Empty(t, none.QuantitySamples)
	assert.Empty(t, none.RoutePoints)
	assert.Empty(t, none.ECGSamples)

	activityNoLocation := mr()
	n := Filter(activityNoLocation, func(c string) bool { return c != Location })
	assert.Equal(t, 3, n)
	assert.Len(t, activityNoLocation.Workouts, 1, "the workout stays without its route")
	assert.Empty(t, activityNoLocation.RoutePoints)

	onlyHeart := mr()
	Filter(onlyHeart, func(c string) bool { return c == Heart })
	assert.Equal(t, []model.QuantitySample{{QuantityType: "heart_rate"}}, onlyHeart.QuantitySamples)
	assert.Len(t, onlyHeart.HeartbeatSamples, 1)
	assert.Empty(t, onlyHeart.Observations)
	assert.Empty(t, onlyHeart.CategorySamples)
	assert.Empty(t, onlyHeart.SleepSessions)
	assert.Empty(t, onlyHeart.SleepPhases)
	assert.Empty(t, onlyHeart.NutritionLogs)
}

func TestPolicyAllows(t *testing.T) {
	assert.True(t, Policy{}.Allows(Heart), "no consent recorded, not required: everything")
	assert.False(t, Policy{Required: true}.Allows(Heart), "no consent recorded, required: nothing")
	p := Policy{Recorded: true, Required: true, granted: map[string]bool{Heart: true, Sleep: false}}
	assert.True(t, p.Allows(Heart))
	assert.False(t, p.Allows(Sleep))
	assert.False(t, p.Allows(Glucose), "a category without a row is refused once consent is recorded")
}
