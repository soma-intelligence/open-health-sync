// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import "strings"

// Scope prefix and the read scopes asked for at consent.
const scopePrefix = "https://www.googleapis.com/auth/googlehealth."

// Read scopes. Only these three are asked for: every Google Health API scope
// is restricted, and each extra one makes Google's app verification harder.
const (
	ScopeActivity = scopePrefix + "activity_and_fitness.readonly"
	ScopeMetrics  = scopePrefix + "health_metrics_and_measurements.readonly"
	ScopeSleep    = scopePrefix + "sleep.readonly"
)

// Scopes requested at consent.
var Scopes = []string{ScopeActivity, ScopeMetrics, ScopeSleep}

// kind is how a data type is filtered and timed (the API's record kinds).
type kind int

const (
	kindInterval kind = iota // interval.startTime .. interval.endTime
	kindSample               // sampleTime.physicalTime
	kindDaily                // date (the user's calendar day)
	kindSession              // interval, filtered on the civil start (exercise)
	kindSleep                // interval, filtered on the physical end (sleep)
)

// Page sizes: the API caps sessions (exercise, sleep) at 25 points a page and
// everything else at 10,000.
const (
	maxPageSize     = 10000
	sessionPageSize = 25
)

// dataType describes one Google Health API data type this connector pulls.
type dataType struct {
	// ID is the data type id of the REST path (users/me/dataTypes/<ID>) and
	// the raw data type of the stored lines.
	ID string
	// Field is the DataPoint union member holding the values ("heartRate").
	Field string
	// Filter is the name the list filter uses ("heart_rate").
	Filter string
	Kind   kind
	// Scope is the read scope the data type needs.
	Scope string
}

// pageSize is the page size asked for the data type.
func (d dataType) pageSize() int {
	if d.Kind == kindSession || d.Kind == kindSleep {
		return sessionPageSize
	}
	return maxPageSize
}

// Data type ids.
const (
	DataTypeSteps            = "steps"
	DataTypeDistance         = "distance"
	DataTypeFloors           = "floors"
	DataTypeActiveEnergy     = "active-energy-burned"
	DataTypeExercise         = "exercise"
	DataTypeVO2Max           = "vo2-max"
	DataTypeDailyVO2Max      = "daily-vo2-max"
	DataTypeHeartRate        = "heart-rate"
	DataTypeDailyRestingHR   = "daily-resting-heart-rate"
	DataTypeDailyHRV         = "daily-heart-rate-variability"
	DataTypeHRV              = "heart-rate-variability"
	DataTypeOxygenSaturation = "oxygen-saturation"
	DataTypeDailyOxygen      = "daily-oxygen-saturation"
	DataTypeDailyRespiratory = "daily-respiratory-rate"
	DataTypeWeight           = "weight"
	DataTypeBodyFat          = "body-fat"
	DataTypeHeight           = "height"
	DataTypeBloodGlucose     = "blood-glucose"
	DataTypeCoreBodyTemp     = "core-body-temperature"
	DataTypeSleepTemperature = "daily-sleep-temperature-derivations"
	DataTypeSleep            = "sleep"
)

// dataTypes is every data type Fetch pulls, in pull order. The scope of each
// follows Google's scopes page; the ones it does not list by name (VO2 max,
// blood glucose, core body temperature, sleep temperature) are
// unverified, and a data type answering 403 is skipped anyway.
var dataTypes = []dataType{
	{ID: DataTypeSteps, Field: "steps", Filter: "steps", Kind: kindInterval, Scope: ScopeActivity},
	{ID: DataTypeDistance, Field: "distance", Filter: "distance", Kind: kindInterval, Scope: ScopeActivity},
	{ID: DataTypeFloors, Field: "floors", Filter: "floors", Kind: kindInterval, Scope: ScopeActivity},
	{ID: DataTypeActiveEnergy, Field: "activeEnergyBurned", Filter: "active_energy_burned", Kind: kindInterval, Scope: ScopeActivity},
	{ID: DataTypeExercise, Field: "exercise", Filter: "exercise", Kind: kindSession, Scope: ScopeActivity},
	{ID: DataTypeVO2Max, Field: "vo2Max", Filter: "vo2_max", Kind: kindSample, Scope: ScopeActivity},
	{ID: DataTypeDailyVO2Max, Field: "dailyVo2Max", Filter: "daily_vo2_max", Kind: kindDaily, Scope: ScopeActivity},
	{ID: DataTypeHeartRate, Field: "heartRate", Filter: "heart_rate", Kind: kindSample, Scope: ScopeMetrics},
	{ID: DataTypeDailyRestingHR, Field: "dailyRestingHeartRate", Filter: "daily_resting_heart_rate", Kind: kindDaily, Scope: ScopeMetrics},
	{ID: DataTypeDailyHRV, Field: "dailyHeartRateVariability", Filter: "daily_heart_rate_variability", Kind: kindDaily, Scope: ScopeMetrics},
	{ID: DataTypeHRV, Field: "heartRateVariability", Filter: "heart_rate_variability", Kind: kindSample, Scope: ScopeMetrics},
	{ID: DataTypeOxygenSaturation, Field: "oxygenSaturation", Filter: "oxygen_saturation", Kind: kindSample, Scope: ScopeMetrics},
	{ID: DataTypeDailyOxygen, Field: "dailyOxygenSaturation", Filter: "daily_oxygen_saturation", Kind: kindDaily, Scope: ScopeMetrics},
	{ID: DataTypeDailyRespiratory, Field: "dailyRespiratoryRate", Filter: "daily_respiratory_rate", Kind: kindDaily, Scope: ScopeMetrics},
	{ID: DataTypeWeight, Field: "weight", Filter: "weight", Kind: kindSample, Scope: ScopeMetrics},
	{ID: DataTypeBodyFat, Field: "bodyFat", Filter: "body_fat", Kind: kindSample, Scope: ScopeMetrics},
	{ID: DataTypeHeight, Field: "height", Filter: "height", Kind: kindSample, Scope: ScopeMetrics},
	{ID: DataTypeBloodGlucose, Field: "bloodGlucose", Filter: "blood_glucose", Kind: kindSample, Scope: ScopeMetrics},
	{ID: DataTypeCoreBodyTemp, Field: "coreBodyTemperature", Filter: "core_body_temperature", Kind: kindSample, Scope: ScopeMetrics},
	{ID: DataTypeSleepTemperature, Field: "dailySleepTemperatureDerivations", Filter: "daily_sleep_temperature_derivations", Kind: kindDaily, Scope: ScopeMetrics},
	{ID: DataTypeSleep, Field: "sleep", Filter: "sleep", Kind: kindSleep, Scope: ScopeSleep},
}

// AllDataTypes lists the ids of every pulled data type.
var AllDataTypes = func() []string {
	out := make([]string, len(dataTypes))
	for i, d := range dataTypes {
		out[i] = d.ID
	}
	return out
}()

// lookupDataType returns the spec of a data type id.
func lookupDataType(id string) (dataType, bool) {
	for _, d := range dataTypes {
		if d.ID == id {
			return d, true
		}
	}
	return dataType{}, false
}

// normalizeDataType turns a data type name as a notification may spell it
// ("heart-rate", "heart_rate", "heartRate", "HEART_RATE") into the id.
func normalizeDataType(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			// camelCase boundary, unless the whole name is upper case.
			if i > 0 && s != strings.ToUpper(s) {
				b.WriteByte('-')
			}
			b.WriteRune(r - 'A' + 'a')
		case r == '_' || r == ' ':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
