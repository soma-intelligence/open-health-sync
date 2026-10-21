// SPDX-License-Identifier: AGPL-3.0-only

package fhir

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Codes of the nutrition log mapping
// (docs/specs/nutrition-health-connect-fhir.md).
const (
	// RecordNutritionLog is the record type of a nutrition log: one food or
	// drink entry.
	RecordNutritionLog = "nutrition_log"
	// NutritionIDPrefix starts the id of the Observation of a nutrition log.
	NutritionIDPrefix = "nutrition-"
	// MealTypeSystem codes the meal a nutrition log belongs to: breakfast,
	// lunch, dinner, snack.
	MealTypeSystem = Namespace + "/CodeSystem/meal-type"
)

// nutrientLOINC are the point in time LOINC codes of the nutrients that
// have one: the shape of one entry, not of a 24 hour total.
var nutrientLOINC = map[string]string{
	"energy_consumed": "9052-2", // Calorie intake total
	"protein":         "9079-5", // Protein intake Estimated
	"carbohydrates":   "9059-7", // Carbohydrate intake Estimated
	"fat_total":       "9066-2", // Fat intake Estimated
	"water":           "8999-5", // Fluid intake oral Estimated
}

// nutrientUCUM is the UCUM code of each unit of model.Nutrients.
var nutrientUCUM = map[string]string{"kcal": "kcal", "g": "g", "mg": "mg", "mcg": "ug", "mL": "mL"}

// nutrientText is how a component names a nutrient: "saturated fat",
// "vitamin B6".
func nutrientText(name string) string {
	switch {
	case name == "energy_consumed":
		return "energy"
	case name == "fat_total":
		return "total fat"
	case strings.HasPrefix(name, "fat_"):
		return strings.TrimPrefix(name, "fat_") + " fat"
	case strings.HasPrefix(name, "vitamin_"):
		return "vitamin " + strings.ToUpper(strings.TrimPrefix(name, "vitamin_"))
	}
	return strings.ReplaceAll(name, "_", " ")
}

// FromNutritionLog maps one nutrition log to an Observation over the entry's
// interval: the meal type and every figure as components, a figure coded by
// its REST field (energy_kcal, nutrients.caffeine) and by LOINC where a point
// in time code exists. No category: none of the HL7 ones describes intake.
func FromNutritionLog(user uuid.UUID, n read.NutritionLog) Observation {
	o := newObservation(user, NutritionIDPrefix+strconv.FormatInt(n.ID, 10), n.Provenance, n.ExternalID)
	o.Code = recordCode("", RecordNutritionLog, "food and drink")
	o.effective(n.StartDate, n.EndDate)
	if mt := deref(n.MealType); mt != "" {
		o.Component = append(o.Component, Component{
			Code: CodeableConcept{Coding: []Coding{{System: ComponentSystem, Code: "meal_type"}}, Text: "meal type"},
			ValueCodeableConcept: &CodeableConcept{
				Coding: []Coding{{System: MealTypeSystem, Code: mt}},
				Text:   mt,
			},
		})
	}
	columns := map[string]*float64{
		"energy_kcal": n.EnergyKcal, "protein_g": n.ProteinG, "carbs_g": n.CarbsG, "fat_g": n.FatG,
		"fiber_g": n.FiberG, "sugar_g": n.SugarG, "sodium_mg": n.SodiumMg,
	}
	var extended map[string]any
	_ = json.Unmarshal(n.Nutrients, &extended)
	var fields []field
	for _, nu := range model.Nutrients {
		f := field{loinc: nutrientLOINC[nu.Name], text: nutrientText(nu.Name), unit: nu.Unit, ucum: nutrientUCUM[nu.Unit]}
		if nu.Column != "" {
			f.name, f.v = nu.Column, columns[nu.Column]
		} else if v, ok := extended[nu.Name].(float64); ok {
			f.name, f.v = "nutrients."+nu.Name, &v
		}
		fields = append(fields, f)
	}
	figures := components(fields...)
	if len(figures) == 0 {
		o.DataAbsentReason = absent("unknown", "Unknown")
	}
	o.Component = append(o.Component, figures...)
	return o
}
