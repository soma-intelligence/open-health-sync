// SPDX-License-Identifier: AGPL-3.0-only

package model

import (
	"fmt"
	"strings"
)

// Nutrient is one figure a nutrition log can carry.
type Nutrient struct {
	// Name is the canonical nutrient name ("protein", "vitamin_d").
	Name string
	// Unit is the unit its value is stored in: kcal, g, mg, mcg or mL.
	Unit string
	// Column is the nutrition_logs column that holds it, "" for the
	// extended nutrients of nutrients_json.
	Column string
}

// Nutrients are the nutrients of the nutrition logs, the seven columns
// first, in documentation order. Every mapper converts a nutrient into its
// Unit (ToNutrientUnit), so one name means one unit whatever the source:
// the units are the ones the iOS SDK sends. Changing a unit is a breaking
// change, adding a nutrient is not.
var Nutrients = []Nutrient{
	{"energy_consumed", "kcal", "energy_kcal"},
	{"protein", "g", "protein_g"},
	{"carbohydrates", "g", "carbs_g"},
	{"fat_total", "g", "fat_g"},
	{"fiber", "g", "fiber_g"},
	{"sugar", "g", "sugar_g"},
	{"sodium", "mg", "sodium_mg"},
	{"water", "mL", ""},
	{"energy_from_fat", "kcal", ""},
	{"fat_saturated", "g", ""},
	{"fat_monounsaturated", "g", ""},
	{"fat_polyunsaturated", "g", ""},
	{"fat_unsaturated", "g", ""},
	{"fat_trans", "g", ""},
	{"cholesterol", "mg", ""},
	{"caffeine", "mg", ""},
	{"calcium", "mg", ""},
	{"chloride", "mg", ""},
	{"copper", "mg", ""},
	{"iron", "mg", ""},
	{"magnesium", "mg", ""},
	{"manganese", "mg", ""},
	{"phosphorus", "mg", ""},
	{"potassium", "mg", ""},
	{"zinc", "mg", ""},
	{"niacin", "mg", ""},
	{"pantothenic_acid", "mg", ""},
	{"riboflavin", "mg", ""},
	{"thiamin", "mg", ""},
	{"vitamin_b6", "mg", ""},
	{"vitamin_c", "mg", ""},
	{"vitamin_e", "mg", ""},
	{"biotin", "mcg", ""},
	{"chromium", "mcg", ""},
	{"folate", "mcg", ""},
	{"folic_acid", "mcg", ""},
	{"iodine", "mcg", ""},
	{"molybdenum", "mcg", ""},
	{"selenium", "mcg", ""},
	{"vitamin_a", "mcg", ""},
	{"vitamin_b12", "mcg", ""},
	{"vitamin_d", "mcg", ""},
	{"vitamin_k", "mcg", ""},
}

// NutrientUnit returns the unit a nutrient is stored in, and false for a
// name the table does not know.
func NutrientUnit(name string) (string, bool) {
	for _, n := range Nutrients {
		if n.Name == name {
			return n.Unit, true
		}
	}
	return "", false
}

// nutrientUnitScale is the value of one of each unit in the smallest unit of
// its dimension (micrograms, millilitres, kilojoules), keyed by lowercased
// spelling. Whole numbers where they can be, so a conversion between metric
// units multiplies or divides by an exact power of ten.
var nutrientUnitScale = map[string]struct {
	dim   string
	scale float64
}{
	"kg":  {"mass", 1e9},
	"g":   {"mass", 1e6},
	"mg":  {"mass", 1e3},
	"mcg": {"mass", 1},
	"ug":  {"mass", 1},
	"µg":  {"mass", 1},
	"l":   {"volume", 1000},
	"ml":  {"volume", 1},
	// HealthKit's US and imperial fluid ounces.
	"fl_oz_us":  {"volume", 29.5735295625},
	"fl_oz_imp": {"volume", 28.4130625},
	"kcal":      {"energy", 4.184},
	// HealthKit's large calorie, a kilocalorie; matched before lowercasing,
	// since "cal" is the small one.
	"Cal": {"energy", 4.184},
	"kj":  {"energy", 1},
}

// ToNutrientUnit converts a nutrient value into the unit the nutrient is
// stored in. A unit of another dimension, or one the table does not know,
// is an error rather than a guess: a silently wrong number is what stored
// food masses in kilograms under gram columns, and the raw line is kept, so
// a refused value is recoverable. An empty unit is too, since nothing says
// which unit the value is in. A nutrient the table does not know keeps its
// value.
func ToNutrientUnit(name string, value float64, unit string) (float64, error) {
	target, ok := NutrientUnit(name)
	if !ok {
		return value, nil
	}
	unit = strings.TrimSpace(unit)
	from, ok1 := nutrientUnitScale[unit]
	if !ok1 {
		from, ok1 = nutrientUnitScale[strings.ToLower(unit)]
	}
	to := nutrientUnitScale[strings.ToLower(target)]
	switch {
	case !ok1 || from.dim != to.dim:
		return value, fmt.Errorf("%s: unit %q does not convert to %s", name, unit, target)
	case from.scale == to.scale:
		return value, nil
	case from.scale > to.scale:
		return value * (from.scale / to.scale), nil
	}
	return value / (to.scale / from.scale), nil
}

// SetNutrient sets a nutrient of a nutrition log, already in its unit: the
// column for the seven core nutrients, NutrientsJSON for the others.
func (nl *NutritionLog) SetNutrient(name string, value float64) {
	v := value
	switch name {
	case "energy_consumed":
		nl.EnergyKcal = &v
	case "protein":
		nl.ProteinG = &v
	case "carbohydrates":
		nl.CarbsG = &v
	case "fat_total":
		nl.FatG = &v
	case "fiber":
		nl.FiberG = &v
	case "sugar":
		nl.SugarG = &v
	case "sodium":
		nl.SodiumMg = &v
	default:
		if nl.NutrientsJSON == nil {
			nl.NutrientsJSON = map[string]any{}
		}
		nl.NutrientsJSON[name] = v
	}
}
