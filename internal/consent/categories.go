// SPDX-License-Identifier: AGPL-3.0-only

// Package consent keeps each end user's consent per source and per data
// category, its history, and the purge of a revoked category. The ETL asks
// Load for the policy of a job and drops what it does not allow before
// anything is written; a revocation queues a purge of the records already
// stored, which the etl role's worker carries out.
package consent

import (
	"slices"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Data categories a user consents to, per source.
const (
	Activity      = "activity"       // steps, distances, energy, workouts, daily summaries
	Location      = "location"       // workout GPS routes
	Heart         = "heart"          // heart rate, HRV, RR intervals, rhythm events
	ECG           = "ecg"            // electrocardiograms
	BloodPressure = "blood_pressure" // systolic and diastolic
	Glucose       = "glucose"        // blood glucose (CGM included), insulin
	Respiratory   = "respiratory"    // respiratory rate, SpO2, VO2 max, lung function
	Body          = "body"           // weight, height, composition, temperatures
	Sleep         = "sleep"          // sleep sessions, phases, scores
	Nutrition     = "nutrition"      // food, drinks, water
	Reproductive  = "reproductive"   // menstrual cycle, ovulation, sexual activity, pregnancy
	Mental        = "mental"         // mindfulness, state of mind, stress
	Other         = "other"          // anything no category above describes
)

// Categories lists every category, in documentation order.
var Categories = []string{Activity, Location, Heart, ECG, BloodPressure, Glucose, Respiratory, Body,
	Sleep, Nutrition, Reproductive, Mental, Other}

// Known reports whether c is a category.
func Known(c string) bool { return slices.Contains(Categories, c) }

// rule files a type name under a category by exact name or prefix. The first
// matching rule wins, so the more specific ones come first.
type rule struct {
	category string
	exact    []string
	prefixes []string
	contains []string
}

var rules = []rule{
	{category: ECG, prefixes: []string{"ecg", "electrocardiogram"}},
	{category: BloodPressure, prefixes: []string{"blood_pressure"}},
	{category: Glucose, prefixes: []string{"blood_glucose", "glucose", "insulin"}},
	{category: Reproductive, prefixes: []string{"menstrual", "intermenstrual", "ovulation", "cervical_mucus",
		"sexual_activity", "pregnancy", "lactation", "contraceptive", "basal_body_temperature", "progesterone_test",
		"infrequent_menstrual", "irregular_menstrual", "persistent_intermenstrual", "prolonged_menstrual"}},
	{category: Heart, exact: []string{"pulse_wave_velocity", "vascular_age", "atrial_fibrillation_burden"},
		prefixes: []string{"heart_rate", "resting_heart_rate", "walking_heart_rate", "hrv", "heartbeat",
			"high_heart_rate", "low_heart_rate", "irregular_heart_rhythm", "heart_rate_variability"},
		contains: []string{"_hrv"}},
	{category: Respiratory, prefixes: []string{"respiratory", "oxygen_saturation", "spo2", "vo2", "forced_",
		"peak_expiratory", "inhaler", "breathing"}},
	{category: Sleep, exact: []string{"reported_sleep_quality"}, prefixes: []string{"sleep"}},
	{category: Nutrition, prefixes: []string{"dietary", "nutrition", "water_intake", "caffeine"}},
	{category: Mental, exact: []string{"reported_feeling", "reported_mood", "reported_stress", "reported_energy"},
		prefixes: []string{"mindful", "state_of_mind", "stress", "mood"}},
	{category: Body, exact: []string{"height", "bone_mass", "muscle_mass", "lean_body_mass", "water_mass",
		"visceral_fat_rating", "waist_circumference", "basal_metabolic_rate"},
		prefixes: []string{"body_", "skin_temperature", "wrist_temperature"}, contains: []string{"temperature"}},
	{category: Activity, exact: []string{"strain", "workout_strain", "push_count", "physical_effort",
		"reported_exertion", "reported_muscle_soreness", "total_energy_burned", "moderate_activity_time",
		"vigorous_activity_time"},
		prefixes: []string{"step", "distance", "flights", "active_energy", "basal_energy", "apple_", "workout",
			"running_", "walking_", "cycling_", "swimming", "stand", "exercise", "activity", "cadence",
			"wheelchair", "downhill", "elevation"}, contains: []string{"_speed", "_power"}},
}

// Of returns the category of a canonical quantity, category or observation
// type name ("heart_rate", "menstrual_flow"). A name no rule knows is Other.
func Of(typeName string) string {
	n := strings.ToLower(typeName)
	for _, r := range rules {
		if slices.Contains(r.exact, n) {
			return r.category
		}
		for _, p := range r.prefixes {
			if strings.HasPrefix(n, p) {
				return r.category
			}
		}
		for _, c := range r.contains {
			if strings.Contains(n, c) {
				return r.category
			}
		}
	}
	return Other
}

// Filter removes from mr every record whose category allowed refuses, and
// returns how many it removed. A child goes with its parent: route points
// follow their workout (and Location), sleep phases their session, ECG
// voltage its recording.
func Filter(mr *model.MapResult, allowed func(category string) bool) int {
	n := 0
	keep := func(ok bool) bool {
		if !ok {
			n++
		}
		return ok
	}
	mr.QuantitySamples = slices.DeleteFunc(mr.QuantitySamples, func(s model.QuantitySample) bool {
		return !keep(allowed(Of(s.QuantityType)))
	})
	mr.CategorySamples = slices.DeleteFunc(mr.CategorySamples, func(s model.CategorySample) bool {
		return !keep(allowed(Of(s.CategoryType)))
	})
	mr.Observations = slices.DeleteFunc(mr.Observations, func(o model.Observation) bool {
		return !keep(allowed(Of(o.CanonicalCode)))
	})
	if !allowed(Sleep) {
		n += len(mr.SleepSessions) + len(mr.SleepPhases)
		mr.SleepSessions, mr.SleepPhases = nil, nil
	}
	if !allowed(Activity) {
		n += len(mr.Workouts) + len(mr.RoutePoints) + len(mr.DailySummaries)
		mr.Workouts, mr.RoutePoints, mr.DailySummaries = nil, nil, nil
	}
	if !allowed(Location) {
		n += len(mr.RoutePoints)
		mr.RoutePoints = nil
	}
	if !allowed(ECG) {
		n += len(mr.ECGRecordings) + len(mr.ECGSamples)
		mr.ECGRecordings, mr.ECGSamples = nil, nil
	}
	if !allowed(Heart) {
		n += len(mr.HeartbeatSamples)
		mr.HeartbeatSamples = nil
	}
	if !allowed(Nutrition) {
		n += len(mr.NutritionLogs)
		mr.NutritionLogs = nil
	}
	return n
}
