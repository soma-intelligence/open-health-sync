// SPDX-License-Identifier: AGPL-3.0-only

package fhir

import (
	"slices"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// vitalSign is how a quantity type meets an R4 vital signs profile.
type vitalSign struct {
	// loinc is the code the catalog must give the type for the profile to
	// apply; extra is a LOINC code the profile also requires (added first).
	loinc, extra string
	// profile is the StructureDefinition id ("vitalsigns" for the base
	// profile alone); empty claims no profile.
	profile string
	// units are the UCUM codes the profile accepts for the value.
	units []string
	// componentOnly marks a LOINC code R4 allows only as a component of a
	// panel: the Observation leaves it out of its code.
	componentOnly bool
}

func (v vitalSign) allows(ucum string) bool { return slices.Contains(v.units, ucum) }

// vitalSigns are the quantity types in the vital-signs category, with the
// R4 profile each one meets. R4 binds every code of its vital signs table to
// a profile: resting heart rate (40443-4) must also carry 8867-4 and meet
// the heart rate profile, and systolic (8480-6) and diastolic (8462-4)
// pressure may appear only as the two components of the blood pressure
// panel (85354-9). Systolic and diastolic are stored as separate samples, so
// until they are paired into that panel each one is an Observation coded
// with the Open Health Sync code alone.
var vitalSigns = map[string]vitalSign{
	"heart_rate":               {loinc: "8867-4", profile: "heartrate", units: []string{"/min"}},
	"respiratory_rate":         {loinc: "9279-1", profile: "resprate", units: []string{"/min"}},
	"oxygen_saturation":        {loinc: "59408-5", extra: "2708-6", profile: "oxygensat", units: []string{"%"}},
	"body_temperature":         {loinc: "8310-5", profile: "bodytemp", units: []string{"Cel", "[degF]"}},
	"body_mass":                {loinc: "29463-7", profile: "bodyweight", units: []string{"kg", "g", "[lb_av]"}},
	"height":                   {loinc: "8302-2", profile: "bodyheight", units: []string{"cm", "[in_i]"}},
	"body_mass_index":          {loinc: "39156-5", profile: "bmi", units: []string{"kg/m2"}},
	"resting_heart_rate":       {loinc: "40443-4", extra: "8867-4", profile: "heartrate", units: []string{"/min"}},
	"blood_pressure_systolic":  {loinc: "8480-6", componentOnly: true},
	"blood_pressure_diastolic": {loinc: "8462-4", componentOnly: true},
}

// ComponentOnlyLOINC reports whether R4 allows code only as a panel
// component, so no Observation this package makes carries it as its code.
func ComponentOnlyLOINC(code string) bool {
	for _, vs := range vitalSigns {
		if vs.componentOnly && vs.loinc == code {
			return true
		}
	}
	return false
}

// laboratory are the quantity types measured on a sample of blood.
var laboratory = map[string]bool{
	"blood_glucose":         true,
	"blood_alcohol_content": true,
}

// activity are the quantity types that measure physical activity.
var activity = map[string]bool{
	"step_count":                        true,
	"step_count_intraday":               true,
	"active_energy_burned":              true,
	"active_energy_burned_intraday":     true,
	"total_energy_burned":               true,
	"distance_walking_running":          true,
	"distance_walking_running_intraday": true,
	"distance_cycling":                  true,
	"distance_swimming":                 true,
	"distance_wheelchair":               true,
	"distance_downhill_snow_sports":     true,
	"flights_climbed":                   true,
	"push_count":                        true,
	"apple_exercise_time":               true,
	"moderate_activity_time":            true,
	"vigorous_activity_time":            true,
	"apple_move_time":                   true,
	"apple_stand_time":                  true,
	"physical_effort":                   true,
	"walking_speed":                     true,
	"running_speed":                     true,
	"running_power":                     true,
	"cycling_power":                     true,
	"running_stride_length":             true,
	"running_vertical_oscillation":      true,
	"running_ground_contact_time":       true,
	"nike_fuel":                         true,
	"strain":                            true,
	"workout_strain":                    true,
}

// physicalActivityMeasures are the quantity types whose LOINC code is one of
// the HL7 Physical Activity IG's measures: their Observations also carry the
// IG's PhysicalActivity category.
var physicalActivityMeasures = map[string]bool{
	model.QTModerateActivityTime: true,
	model.QTVigorousActivityTime: true,
}

// survey are the quantity types a person answers about themselves (the self
// report ratings): HL7 files assessment answers under survey.
var survey = map[string]bool{
	model.QTReportedFeeling:        true,
	model.QTReportedMood:           true,
	model.QTReportedStress:         true,
	model.QTReportedEnergy:         true,
	model.QTReportedSleepQuality:   true,
	model.QTReportedMuscleSoreness: true,
	model.QTReportedExertion:       true,
	model.QTReportedPain:           true,
}

// category is the observation category of a quantity type, or "" for the
// types none of the HL7 categories describes (body composition, heart rate
// variability, provider indices...): no category is better than a wrong one.
func category(qt string) string {
	if _, ok := vitalSigns[qt]; ok {
		return CategoryVitalSigns
	}
	switch {
	case laboratory[qt]:
		return CategoryLaboratory
	case activity[qt]:
		return CategoryActivity
	case survey[qt]:
		return CategorySurvey
	}
	return ""
}

// ucumUnits gives the UCUM code of each storage unit spelling
// (internal/model/units.go), for the types whose catalog row has no UCUM
// unit and for samples outside the catalog. Dimensionless counts, indices
// and scores are UCUM annotations, which mean 1.
var ucumUnits = map[string]string{
	"bpm":       "/min",
	"ms":        "ms",
	"mL/kg/min": "mL/kg/min",
	"percent":   "%",
	"mmHg":      "mm[Hg]",
	"m/s":       "m/s",
	"years":     "a",
	"celsius":   "Cel",
	"kg":        "kg",
	"kg/m2":     "kg/m2",
	"cm":        "cm",
	"m":         "m",
	"mg/dL":     "mg/dL",
	"kcal":      "kcal",
	"kcal/d":    "kcal/d",
	"kcal/kg/h": "kcal/kg/h",
	"min":       "min",
	"W":         "W",
	"uS":        "uS",
	"dBASPL":    "dB[SPL]",
	"L":         "L",
	"L/min":     "L/min",
	"IU":        "[IU]",
	"count":     "{count}",
	"index":     "{index}",
	"score":     "{score}",
}

// TypesWithCategory lists, sorted, the quantity types whose Observations
// carry category cat.
func TypesWithCategory(cat string) []string {
	var out []string
	for _, m := range []map[string]bool{laboratory, activity, survey} {
		for qt := range m {
			if category(qt) == cat {
				out = append(out, qt)
			}
		}
	}
	for qt := range vitalSigns {
		if category(qt) == cat {
			out = append(out, qt)
		}
	}
	slices.Sort(out)
	return out
}

// TypesWithExtraLOINC lists, sorted, the quantity types whose Observations
// carry code on top of the catalog's own code, because their profile
// requires it (8867-4 for resting heart rate, 2708-6 for SpO2).
func TypesWithExtraLOINC(code string) []string {
	var out []string
	for qt, vs := range vitalSigns {
		if code != "" && vs.extra == code {
			out = append(out, qt)
		}
	}
	slices.Sort(out)
	return out
}
