// SPDX-License-Identifier: AGPL-3.0-only

package model

import "testing"

func TestCanonicalQuantityType(t *testing.T) {
	cases := map[string]string{
		"heartRate":                "heart_rate",
		"stepCount":                "step_count",
		"oxygenSaturation":         "oxygen_saturation",
		"bloodPressureSystolic":    "blood_pressure_systolic",
		"activeEnergyBurned":       "active_energy_burned",
		"heartRateVariabilitySDNN": "hrv_sdnn",
		"fatMass":                  "body_fat_mass",
		"bodyWaterMass":            "water_mass",
		"visceralFat":              "visceral_fat_rating",
		"vo2Max":                   "vo2_max",
		"vo2_max":                  "vo2_max",
		"daily_sleep":              "daily_sleep",
		"cardiovascular_age":       "cardiovascular_age",
		"readinessScore":           "readiness_score",
		"recoveryScore":            "recovery_score",
	}
	for in, want := range cases {
		if got := CanonicalQuantityType(in); got != want {
			t.Errorf("CanonicalQuantityType(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCanonicalQuantityConstants pins the canonical vocabulary constants to their
// string values. Provider streams reference these symbols; the string is the wire
// key stored in the registry, so it must not drift.
func TestCanonicalQuantityConstants(t *testing.T) {
	cases := map[string]string{
		QTStepCount:                "step_count",
		QTActiveEnergyBurned:       "active_energy_burned",
		QTBasalEnergyBurned:        "basal_energy_burned",
		QTDistanceWalkingRunning:   "distance_walking_running",
		QTFlightsClimbed:           "flights_climbed",
		QTHeartRate:                "heart_rate",
		QTRestingHeartRate:         "resting_heart_rate",
		QTHRVSDNN:                  "hrv_sdnn",
		QTHRVRMSSD:                 "hrv_rmssd",
		QTOxygenSaturation:         "oxygen_saturation",
		QTRespiratoryRate:          "respiratory_rate",
		QTBodyTemperature:          "body_temperature",
		QTSkinTemperature:          "skin_temperature",
		QTBodyTemperatureDeviation: "body_temperature_deviation",
		QTReadinessHRVBalance:      "readiness_hrv_balance",
		QTStrain:                   "strain",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("canonical constant = %q, want %q", got, want)
		}
	}
	// The oura 0-100 index must be its own key, never the ms HRV key.
	if QTReadinessHRVBalance == QTHRVSDNN {
		t.Fatal("readiness_hrv_balance must not equal hrv_sdnn (index vs milliseconds)")
	}
	// RMSSD and SDNN are different statistics: two keys.
	if QTHRVRMSSD == QTHRVSDNN {
		t.Fatal("hrv_rmssd must not equal hrv_sdnn")
	}
}
