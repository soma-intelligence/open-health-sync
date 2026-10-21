// SPDX-License-Identifier: AGPL-3.0-only

package model

import (
	"fmt"
	"strings"
)

// Canonical STORAGE units (single source of truth for the `unit` column).
//
// Why this file exists: before it existed, unit handling lived inside the Apple Health
// mapper, private to one provider. Every other mapper wrote whatever unit its
// upstream API returned, so one canonical metric carried several unit strings:
// heart_rate shipped as "bpm" (oura, withings) AND "count/min" (whoop), while
// withings height arrived in metres against a centimetre-declared registry (a
// silent 100x error in any fused height). Nothing rejected either case, and
// fusion picks values across providers with no unit comparison, so a
// single fused series could mix kg with lb or mg/dL with mmol/L.
//
// The vocabulary chosen here is the apple_health one, because canonical_quantity.go
// already declares Apple Health the reference every other provider aligns to, and
// because it is what the existing rows and the clients already carry.
// quantity_codes.ucum_unit keeps the interoperable UCUM spelling for FHIR export;
// storage_unit (seeded from this table) is what the column must literally contain.
//
// Enforcement is a single pipeline choke point (between mapping
// and validation), NOT 40 assignments spread across the mapper packages. A new
// provider mapper is covered whether or not anyone remembered to review its units.
//
// Metrics deliberately absent from storageUnits pass through untouched: a
// connector may emit a metric this table does not declare yet, and it lands
// unconverted instead of failing every record of that type.
var storageUnits = map[string]string{
	// Cardiovascular. Note respiratory_rate stores "bpm" rather than a
	// breaths-per-minute spelling: HealthKit reports it as count/min and
	// apple_health has always canonicalized count/min to "bpm", so every existing
	// row uses it. UCUM "/min" in the registry carries the correct semantics.
	QTHeartRate:                  "bpm",
	QTRestingHeartRate:           "bpm",
	"walking_heart_rate_average": "bpm",
	QTRespiratoryRate:            "bpm",
	QTHRVSDNN:                    "ms",
	QTHRVRMSSD:                   "ms",
	"vo2_max":                    "mL/kg/min",
	QTOxygenSaturation:           "percent",
	"blood_pressure_systolic":    "mmHg",
	"blood_pressure_diastolic":   "mmHg",
	"pulse_wave_velocity":        "m/s",
	"vascular_age":               "years",

	// Temperature.
	QTBodyTemperature:          "celsius",
	QTSkinTemperature:          "celsius",
	QTBodyTemperatureDeviation: "celsius",

	// Body composition.
	"body_mass":           "kg",
	"lean_body_mass":      "kg",
	"body_fat_mass":       "kg",
	"muscle_mass":         "kg",
	"bone_mass":           "kg",
	"water_mass":          "kg",
	"body_mass_index":     "kg/m2",
	"body_fat_percentage": "percent",
	"visceral_fat_rating": "index",
	"height":              "cm",
	"blood_glucose":       "mg/dL",

	// Activity. Intraday variants share their parent's unit.
	QTStepCount:              "count",
	QTStepCountIntraday:      "count",
	QTFlightsClimbed:         "count",
	QTActiveEnergyBurned:     "kcal",
	QTActiveEnergyIntraday:   "kcal",
	QTBasalEnergyBurned:      "kcal",
	QTTotalEnergyBurned:      "kcal",
	QTBasalMetabolicRate:     "kcal/d",
	QTDistanceWalkingRunning: "m",
	QTDistanceWalkRunIntday:  "m",

	// Apple ring times. HealthKit reports these as a time quantity and
	// the Apple Health client's unit map lists "ms" before "min", so every row arrived as
	// milliseconds (a 5-minute day stored as 300000). Nothing declared a unit
	// for them, so the choke point let that through and every reader had to
	// guess: a scoring engine could feed the raw number to a strain formula as if it were
	// minutes. Minutes is the unit the clients already display
	// ("exerciseMin", "exercise_minutes"), so it is the one declared here.
	"apple_exercise_time": "min",
	"apple_move_time":     "min",
	"apple_stand_time":    "min",

	// Apple Health long tail. Each storage unit is the spelling the SDK
	// already sends (HKUnit.unitString of its fixed catalog unit), so the
	// declaration pins the unit without converting anything, except where a
	// conversion is listed below (waist in cm like height; blood alcohol as
	// a percentage like every other ratio; UV as an index).
	"distance_cycling":                 "m",
	"distance_swimming":                "m",
	"distance_wheelchair":              "m",
	"distance_downhill_snow_sports":    "m",
	"push_count":                       "count",
	"nike_fuel":                        "count",
	"physical_effort":                  "kcal/kg/h",
	"walking_speed":                    "m/s",
	"running_speed":                    "m/s",
	"running_power":                    "W",
	"cycling_power":                    "W",
	"running_stride_length":            "m",
	"running_vertical_oscillation":     "cm",
	"running_ground_contact_time":      "ms",
	"underwater_depth":                 "m",
	"waist_circumference":              "cm",
	"apple_sleeping_wrist_temperature": "celsius",
	"basal_body_temperature":           "celsius",
	"water_temperature":                "celsius",
	"blood_alcohol_content":            "percent",
	"number_of_alcoholic_beverages":    "count",
	"electrodermal_activity":           "uS",
	"environmental_audio_exposure":     "dBASPL",
	"headphone_audio_exposure":         "dBASPL",
	"environmental_sound_reduction":    "dBASPL",
	"forced_expiratory_volume_1":       "L",
	"forced_vital_capacity":            "L",
	"peak_expiratory_flow_rate":        "L/min",
	"inhaler_usage":                    "count",
	"insulin_delivery":                 "IU",
	"number_of_times_fallen":           "count",
	"uv_exposure":                      "index",

	// Dimensionless provider indices. Declared so a provider cannot start
	// shipping them with a unit that implies a physical quantity.
	QTReadinessHRVBalance: "index",
	QTStrain:              "index",
	QTWorkoutStrain:       "index",
	QTReadinessScore:      "score",
	QTSleepScore:          "score",

	// Provider day classifications, in minutes of the day.
	QTStressHighTime:   "min",
	QTRecoveryHighTime: "min",

	// Activity intensity, in minutes of the day.
	QTModerateActivityTime: "min",
	QTVigorousActivityTime: "min",

	// A person's own ratings (SelfReportScales).
	QTReportedFeeling:        "score",
	QTReportedMood:           "score",
	QTReportedStress:         "score",
	QTReportedEnergy:         "score",
	QTReportedSleepQuality:   "score",
	QTReportedMuscleSoreness: "score",
	QTReportedExertion:       "score",
	QTReportedPain:           "score",
}

// unitAliases renames a unit without touching the value. Keyed by the source
// spelling, lowercased at lookup so "Kg" and "kg" agree.
var unitAliases = map[string]string{
	"bpm":       "bpm",
	"count/min": "bpm",
	"/min":      "bpm",
	"beats/min": "bpm",
	"celsius":   "celsius",
	"degc":      "celsius",
	"c":         "celsius",
	"cel":       "celsius",
	"percent":   "percent",
	"%":         "percent",
	"mmhg":      "mmHg",
	"mm[hg]":    "mmHg",
	"count":     "count",
	"{steps}":   "count",
	"steps":     "count",
	"{flights}": "count",
	"flights":   "count",
	"kcal":      "kcal",
	"kcal/day":  "kcal/d",
	"m":         "m",
	"meters":    "m",
	"metres":    "m",
	"ms":        "ms",
	"msec":      "ms",
	"min":       "min",
	"mins":      "min",
	"minutes":   "min",
	"kg":        "kg",
	"index":     "index",
	"score":     "score",
	"years":     "years",
	"mg/dl":     "mg/dL",
	"ml/kg/min": "mL/kg/min",
	// HKUnit spellings of the same VO2 max unit.
	"ml/kg·min":   "mL/kg/min",
	"ml/min·kg":   "mL/kg/min",
	"ml/(kg·min)": "mL/kg/min",
	"m/s":         "m/s",
	"kg/m2":       "kg/m2",
	"cm":          "cm",
	"w":           "W",
	"dbaspl":      "dBASPL",
	"l":           "L",
	"l/min":       "L/min",
	"iu":          "IU",
	"us":          "uS",
	"mcs":         "uS", // HKUnit spells microsiemens "mcS"
	// HealthKit physical effort, kcal per kilogram per hour, in the
	// spellings HKUnit and UCUM use for it.
	"kcal/kg/h":    "kcal/kg/h",
	"kcal/hr·kg":   "kcal/kg/h",
	"kcal/kg·hr":   "kcal/kg/h",
	"kcal/(kg·hr)": "kcal/kg/h",
	"kcal/(hr·kg)": "kcal/kg/h",
	"kcal/kg/hr":   "kcal/kg/h",
	"kcal/(kg.h)":  "kcal/kg/h",
}

// conversion is an affine transform to a storage unit: storage = value*Factor + Offset.
// Offset exists for temperature, which is not a pure scaling.
type conversion struct {
	Factor float64
	Offset float64
	To     string
}

// unitConversions are qt-agnostic: the transform holds for any metric measured in
// the source unit. Keyed by the lowercased source unit.
var unitConversions = map[string]conversion{
	"lb":     {Factor: 0.45359237, To: "kg"},
	"lbs":    {Factor: 0.45359237, To: "kg"},
	"st":     {Factor: 6.35029318, To: "kg"},
	"g":      {Factor: 0.001, To: "kg"},
	"km":     {Factor: 1000, To: "m"},
	"mi":     {Factor: 1609.344, To: "m"},
	"ft":     {Factor: 0.3048, To: "m"},
	"cal":    {Factor: 0.001, To: "kcal"},
	"kj":     {Factor: 0.2390057, To: "kcal"},
	"j":      {Factor: 0.0002390057, To: "kcal"},
	"degf":   {Factor: 5.0 / 9.0, Offset: -32.0 * 5.0 / 9.0, To: "celsius"},
	"f":      {Factor: 5.0 / 9.0, Offset: -32.0 * 5.0 / 9.0, To: "celsius"},
	"[degf]": {Factor: 5.0 / 9.0, Offset: -32.0 * 5.0 / 9.0, To: "celsius"},
	// Speeds. Apple Health spells walking and running speed "km/hr" (HKUnit),
	// or "mi/hr" on an imperial phone.
	"km/hr": {Factor: 1.0 / 3.6, To: "m/s"},
	"km/h":  {Factor: 1.0 / 3.6, To: "m/s"},
	"kph":   {Factor: 1.0 / 3.6, To: "m/s"},
	"mi/hr": {Factor: 0.44704, To: "m/s"},
	"mi/h":  {Factor: 0.44704, To: "m/s"},
	"mph":   {Factor: 0.44704, To: "m/s"},
}

// metricUnitConversions are transforms that are only valid for a specific metric,
// so they cannot live in the qt-agnostic table. Two reasons a conversion lands here:
//
//   - the same unit pair means different things per metric: centimetres are the
//     storage unit for height but a source unit for distance, so cm<->m must not be
//     a global rule.
//   - molar to mass concentration depends on the analyte's molar mass: 18.0182 for
//     glucose, a different factor for anything else, so mmol/L is never global.
//
// Keyed by "{quantityType}|{lowercased source unit}".
var metricUnitConversions = map[string]conversion{
	"height|m":                             {Factor: 100, To: "cm"},
	"height|in":                            {Factor: 2.54, To: "cm"},
	"distance_walking_running|cm":          {Factor: 0.01, To: "m"},
	"distance_walking_running_intraday|cm": {Factor: 0.01, To: "m"},
	"blood_glucose|mmol/l":                 {Factor: 18.0182, To: "mg/dL"},
	// Blood pressure cuffs on the Bluetooth Blood Pressure profile may report
	// kPa. Scoped to the two pressures: kPa means nothing for other metrics.
	"blood_pressure_systolic|kpa":  {Factor: 7.50062, To: "mmHg"},
	"blood_pressure_diastolic|kpa": {Factor: 7.50062, To: "mmHg"},
	// Withings visceral fat arrives as "count" and is stored as "index", one
	// dimensionless rating under two names. Unconverted, every reading was a
	// unit error and nothing was ever stored.
	"visceral_fat_rating|count": {Factor: 1, To: "index"},
	"hrv_sdnn|s":                {Factor: 1000, To: "ms"},
	"hrv_sdnn|sec":              {Factor: 1000, To: "ms"},
	// Fractional 0..1 spellings. Several aggregator APIs report saturation and
	// body-fat as a fraction, and so does Apple Health: HKUnit.percent() is a
	// 0..1 fraction that spells itself "%", which the apple_health mapper now
	// renames to "fraction" rather than to "percent". These two entries are
	// what makes an iPhone SpO2 of 0.97 land as 97 like every other provider's.
	// A pure magnitude guard on the read side cannot replace this: it only
	// works for metrics bounded well above 1, and it has to be repeated in
	// every reader of the column.
	"oxygen_saturation|fraction":     {Factor: 100, To: "percent"},
	"body_fat_percentage|fraction":   {Factor: 100, To: "percent"},
	"blood_alcohol_content|fraction": {Factor: 100, To: "percent"},
	// Waist is a body dimension, stored in centimetres like height.
	"waist_circumference|m": {Factor: 100, To: "cm"},
	// HealthKit reports the UV index as a dimensionless count.
	"uv_exposure|count": {Factor: 1, To: "index"},
	// Apple ring times: the Apple Health client sends milliseconds (see storageUnits).
	// Time is not qt-agnostic here because "ms" is itself the storage unit for
	// hrv_sdnn, so a global ms -> min rule would wreck HRV.
	"apple_exercise_time|ms": {Factor: 1.0 / 60000, To: "min"},
	"apple_exercise_time|s":  {Factor: 1.0 / 60, To: "min"},
	"apple_move_time|ms":     {Factor: 1.0 / 60000, To: "min"},
	"apple_move_time|s":      {Factor: 1.0 / 60, To: "min"},
	"apple_stand_time|ms":    {Factor: 1.0 / 60000, To: "min"},
	"apple_stand_time|s":     {Factor: 1.0 / 60, To: "min"},
	// Oura daily_stress reports seconds.
	"stress_high_time|s":   {Factor: 1.0 / 60, To: "min"},
	"recovery_high_time|s": {Factor: 1.0 / 60, To: "min"},
	// Oura daily_activity and Withings getactivity report seconds.
	"moderate_activity_time|s": {Factor: 1.0 / 60, To: "min"},
	"vigorous_activity_time|s": {Factor: 1.0 / 60, To: "min"},
	// Strain: Whoop and some aggregators call the same dimensionless index a "score". Scoped
	// to the two strain types because readiness_score IS stored as "score", so a
	// global score -> index rule would be wrong for it. Without these, every
	// strain value was a unit error, and a unit error parks the whole record.
	"strain|score":         {Factor: 1, To: "index"},
	"workout_strain|score": {Factor: 1, To: "index"},
}

// StorageUnit returns the declared storage unit for a canonical quantity type.
// The second result is false for types with no declared unit, which pass through
// unconverted.
func StorageUnit(quantityType string) (string, bool) {
	u, ok := storageUnits[quantityType]
	return u, ok
}

// StorageUnits returns a copy of the declared quantity type to storage unit table,
// for the quantity_codes seed and the alignment guard.
func StorageUnits() map[string]string {
	out := make(map[string]string, len(storageUnits))
	for k, v := range storageUnits {
		out[k] = v
	}
	return out
}

// ToStorageUnit converts a value into the storage unit declared for its quantity
// type. It returns the converted value and the storage unit.
//
// Behaviour, in order:
//   - quantity type with no declared unit: value and unit pass through unchanged.
//   - unit already the storage unit (case-insensitively, or via an alias that only
//     renames): value unchanged, unit normalized to the declared spelling.
//   - a known conversion exists: affine transform applied.
//   - empty unit: assumed to already be in the storage unit (the pre-existing
//     behaviour of the blood_glucose validator), unit filled in.
//   - anything else: error. The caller records the sample as a RECOVERABLE failure
//     so adding a conversion entry and replaying lands it.
func ToStorageUnit(quantityType string, value float64, unit string) (float64, string, error) {
	target, declared := storageUnits[quantityType]
	if !declared {
		return value, unit, nil
	}

	src := strings.ToLower(strings.TrimSpace(unit))
	if src == "" {
		return value, target, nil
	}

	if strings.EqualFold(src, target) {
		return value, target, nil
	}

	if alias, ok := unitAliases[src]; ok && alias == target {
		return value, target, nil
	}

	if c, ok := metricUnitConversions[quantityType+"|"+src]; ok && c.To == target {
		return value*c.Factor + c.Offset, target, nil
	}

	if c, ok := unitConversions[src]; ok && c.To == target {
		return value*c.Factor + c.Offset, target, nil
	}

	return value, unit, fmt.Errorf("no conversion from %q to storage unit %q for %s", unit, target, quantityType)
}

// UnitError reports one sample whose unit could not be reconciled with the storage
// unit declared for its quantity type.
type UnitError struct {
	QuantityType string
	Unit         string
	Value        float64
	Err          error
}

func (e UnitError) Error() string { return e.Err.Error() }

// CanonicalizeUnits rewrites every quantity sample in the result into its declared
// storage unit, and reports the samples it could not convert. Converted samples are
// mutated in place; unconvertible ones are left untouched for the caller to record
// and drop.
//
// Content hashes are deliberately NOT recomputed. The hash is the dedup identity of
// the SOURCE record (the collectors hash the raw value), so leaving it alone keeps a replay of the same file dedupping against
// rows written before this pass existed. The transform is deterministic, so the
// same input always produces the same stored value.
func CanonicalizeUnits(mr *MapResult) []UnitError {
	if mr == nil {
		return nil
	}
	var errs []UnitError
	for i := range mr.QuantitySamples {
		s := &mr.QuantitySamples[i]
		v, u, err := ToStorageUnit(s.QuantityType, s.Value, s.Unit)
		if err != nil {
			errs = append(errs, UnitError{
				QuantityType: s.QuantityType,
				Unit:         s.Unit,
				Value:        s.Value,
				Err:          err,
			})
			continue
		}
		s.Value, s.Unit = v, u
	}
	return errs
}
