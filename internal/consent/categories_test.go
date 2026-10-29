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
		"reported_feeling":             Mental,
		"reported_mood":                Mental,
		"reported_stress":              Mental,
		"reported_energy":              Mental,
		"reported_sleep_quality":       Sleep,
		"reported_muscle_soreness":     Activity,
		"reported_exertion":            Activity,
		"reported_pain":                Other,
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

// TestFilterFigures: a sleep session, a workout or a daily summary keeps its
// place under its own category, but a figure it carries of a category that
// is not allowed is dropped (the figures of docs/share-links.md).
func TestFilterFigures(t *testing.T) {
	v := func(x float64) *float64 { return &x }
	mr := &model.MapResult{
		SleepSessions: []model.SleepSession{{SleepScore: v(80), HRVAvgMs: v(50), RestingHR: v(52),
			RespiratoryRate: v(14), SpO2Avg: v(96)}},
		Workouts:       []model.Workout{{AvgHeartRate: v(140), MaxHeartRate: v(170), VO2Max: v(48)}},
		DailySummaries: []model.DailySummary{{SleepScore: v(80), RestingHR: v(52), HRVAvgMs: v(50)}},
	}
	assert.Zero(t, Filter(mr, func(c string) bool { return c == Activity || c == Sleep }), "no record goes")
	s, w, d := mr.SleepSessions[0], mr.Workouts[0], mr.DailySummaries[0]
	assert.Equal(t, v(80), s.SleepScore)
	assert.Nil(t, s.HRVAvgMs)
	assert.Nil(t, s.RestingHR)
	assert.Nil(t, s.RespiratoryRate)
	assert.Nil(t, s.SpO2Avg)
	assert.Nil(t, w.AvgHeartRate)
	assert.Nil(t, w.MaxHeartRate)
	assert.Nil(t, w.VO2Max)
	assert.Equal(t, v(80), d.SleepScore)
	assert.Nil(t, d.RestingHR)
	assert.Nil(t, d.HRVAvgMs)

	mr = &model.MapResult{DailySummaries: []model.DailySummary{{SleepScore: v(80), RestingHR: v(52)}}}
	Filter(mr, func(c string) bool { return c != Sleep })
	assert.Nil(t, mr.DailySummaries[0].SleepScore)
	assert.Equal(t, v(52), mr.DailySummaries[0].RestingHR)
}

func TestPolicyAllows(t *testing.T) {
	assert.True(t, Policy{}.Allows(Heart), "no consent recorded, not required: everything")
	assert.False(t, Policy{Required: true}.Allows(Heart), "no consent recorded, required: nothing")
	p := Policy{Recorded: true, Required: true, granted: map[string]bool{Heart: true, Sleep: false}}
	assert.True(t, p.Allows(Heart))
	assert.False(t, p.Allows(Sleep))
	assert.False(t, p.Allows(Glucose), "a category without a row is refused once consent is recorded")
}
