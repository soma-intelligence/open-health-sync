// SPDX-License-Identifier: AGPL-3.0-only

package model

import "strings"

// Canonical quantity_type vocabulary (single source of truth for metric naming).
// Provider mappers (oura, whoop, withings, apple_health) MUST emit these
// exact string keys so the same metric carries one key regardless of source, and
// so the quantity_codes rows recognize it. Apple Health is the reference; every
// other provider aligns TO these. Constants exist so provider packages reference a
// symbol instead of a bare string literal (typo-proof, greppable). Adding a new
// metric goes here and in the quantity_codes seed migration, nowhere else.
const (
	// Activity / energy.
	QTStepCount          = "step_count"
	QTActiveEnergyBurned = "active_energy_burned" // withings "active_energy" aligns here
	QTBasalEnergyBurned  = "basal_energy_burned"
	// total_energy_burned is a source's own total of the energy burned over
	// an interval, basal and active together (Health Connect
	// TotalCaloriesBurned). A day's total_kcal is its sum when a source sends it.
	QTTotalEnergyBurned = "total_energy_burned"
	// basal_metabolic_rate is a rate in kilocalories per day, an estimate a
	// smart scale or an app makes (Health Connect BasalMetabolicRate). It is
	// not an energy and is never summed: basal_energy_burned is.
	QTBasalMetabolicRate     = "basal_metabolic_rate"
	QTDistanceWalkingRunning = "distance_walking_running" // oura/withings distance aligns here
	QTFlightsClimbed         = "flights_climbed"

	// Cardiovascular / respiratory.
	QTHeartRate        = "heart_rate"
	QTRestingHeartRate = "resting_heart_rate" // oura/whoop RE-EMIT from metadata
	QTHRVSDNN          = "hrv_sdnn"           // time-domain HRV in milliseconds ONLY
	// hrv_rmssd is the OTHER time-domain HRV statistic (root mean square of
	// successive RR differences), in milliseconds: what whoop (and oura)
	// report. RMSSD runs well below SDNN for the same heart, so the two never
	// share a key: an RMSSD under hrv_sdnn would read as poor variability.
	QTHRVRMSSD         = "hrv_rmssd"
	QTOxygenSaturation = "oxygen_saturation" // withings "spo2"; oura/whoop RE-EMIT from metadata
	QTRespiratoryRate  = "respiratory_rate"  // oura/whoop RE-EMIT from metadata

	// Temperature. Absolute vs skin/wrist vs nightly deviation are distinct keys.
	QTBodyTemperature          = "body_temperature"           // absolute
	QTSkinTemperature          = "skin_temperature"           // skin/wrist absolute
	QTBodyTemperatureDeviation = "body_temperature_deviation" // oura nightly deviation

	// Provider index metrics that are NOT physiological quantities.
	// readiness_hrv_balance is oura's 0-100 INDEX (NOT milliseconds): never write
	// it into hrv_sdnn / an ms column. It is its own dedicated key.
	QTReadinessHRVBalance = "readiness_hrv_balance"
	QTStrain              = "strain" // whoop DAILY cycle strain (dimensionless index)
	// workout_strain is whoop's PER-WORKOUT strain. It is deliberately a
	// separate key from the daily cycle strain: both under 'strain' would make
	// the whoop.strain today-scalar binding nondeterministic (workout vs day).
	QTWorkoutStrain = "workout_strain"
	// readiness_score is oura's 0-100 daily readiness INDEX, re-emitted as a
	// quantity sample so score bindings that read quantities have a producer
	// (they do not read daily summaries).
	QTReadinessScore = "readiness_score"
	// sleep_score is a provider's 0-100 daily sleep INDEX (Oura daily_sleep),
	// a quantity sample like readiness_score. It is not the per-session
	// sleep_sessions.sleep_score column, which belongs to one night.
	QTSleepScore = "sleep_score"
	// stress_high_time and recovery_high_time are the minutes of a day a
	// provider classed as high stress or high recovery (Oura daily_stress).
	QTStressHighTime   = "stress_high_time"
	QTRecoveryHighTime = "recovery_high_time"
	// moderate_activity_time and vigorous_activity_time are the minutes of a
	// day a provider classed as moderate or vigorous activity (Oura medium and
	// high activity, Withings moderate and intense), each in the provider's
	// own classes. Kept apart rather than summed: guidelines weigh them
	// differently.
	QTModerateActivityTime = "moderate_activity_time"
	QTVigorousActivityTime = "vigorous_activity_time"

	// A person's own ratings, recorded through the self report route: whole
	// numbers on a fixed scale (SelfReportScales), unit "score". They are
	// answers, not measurements, so no provider index shares their keys.
	QTReportedFeeling        = "reported_feeling"
	QTReportedMood           = "reported_mood"
	QTReportedStress         = "reported_stress"
	QTReportedEnergy         = "reported_energy"
	QTReportedSleepQuality   = "reported_sleep_quality"
	QTReportedMuscleSoreness = "reported_muscle_soreness"
	QTReportedExertion       = "reported_exertion"
	QTReportedPain           = "reported_pain"

	// Intraday interval variants of the SUM-aggregated activity metrics.
	// Withings ships both a daily activity total AND per-interval intraday
	// values for the same day; if both landed under the canonical key,
	// a daily sum over the canonical key would double count the day. The daily
	// total keeps the canonical key (it is what rollups and scoring read); the
	// per-interval stream gets its own *_intraday key for fine-grained charts.
	// AVG-aggregated metrics (heart_rate) do NOT get a variant: intraday HR
	// points are real readings that belong in the canonical average.
	QTStepCountIntraday     = "step_count_intraday"
	QTActiveEnergyIntraday  = "active_energy_burned_intraday"
	QTDistanceWalkRunIntday = "distance_walking_running_intraday"
)

// Scale is the range of a self report rating: whole numbers from Min to Max.
type Scale struct{ Min, Max int }

// SelfReportScales are the self report quantity types and their scales, in
// documentation order. A scale is part of the API: changing one is a breaking
// change, adding a type is not.
var SelfReportScales = []struct {
	Type  string
	Scale Scale
}{
	{QTReportedFeeling, Scale{1, 5}},
	{QTReportedMood, Scale{1, 5}},
	{QTReportedStress, Scale{1, 5}},
	{QTReportedEnergy, Scale{1, 5}},
	{QTReportedSleepQuality, Scale{1, 5}},
	{QTReportedMuscleSoreness, Scale{1, 5}},
	{QTReportedExertion, Scale{0, 10}},
	{QTReportedPain, Scale{0, 10}},
}

// SelfReportScale returns the scale of a self report quantity type, and false
// for any other type.
func SelfReportScale(qt string) (Scale, bool) {
	for _, s := range SelfReportScales {
		if s.Type == qt {
			return s.Scale, true
		}
	}
	return Scale{}, false
}

// quantityTypeOverrides handles collector quantity_type values where a naive
// camelCase -> snake_case conversion would diverge from the canonical
// vocabulary already used by the apple_health and withings mappers (acronyms
// like SDNN, or metrics whose canonical name was chosen differently).
var quantityTypeOverrides = map[string]string{
	"heartRateVariabilitySDNN": "hrv_sdnn",
	"fatMass":                  "body_fat_mass",
	"bodyWaterMass":            "water_mass",
	"visceralFat":              "visceral_fat_rating",
}

// CanonicalQuantityType converts a normalized record quantity_type
// (camelCase, e.g. "heartRate") into the snake_case canonical name stored in
// the hypertables (e.g. "heart_rate"), matching the apple_health vocabulary so
// the same metric carries one key regardless of its source provider.
// Values already in snake_case (e.g. "daily_sleep", "vo2_max") pass through.
func CanonicalQuantityType(qt string) string {
	if c, ok := quantityTypeOverrides[qt]; ok {
		return c
	}
	return camelToSnake(qt)
}

// camelToSnake lowercases a camelCase identifier into snake_case. Strings with
// no uppercase letters (already snake_case) are returned unchanged.
func camelToSnake(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
