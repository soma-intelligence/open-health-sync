// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// coreNutrientFields maps canonical nutrient names to setter functions
// on a NutritionLog pointer. Only "core" nutrients with dedicated DB columns
// are mapped here; all others go to NutrientsJSON.
var coreNutrientFields = map[string]func(nl *model.NutritionLog, value float64){
	"energy_consumed": func(nl *model.NutritionLog, v float64) { nl.EnergyKcal = &v },
	"protein":         func(nl *model.NutritionLog, v float64) { nl.ProteinG = &v },
	"carbohydrates":   func(nl *model.NutritionLog, v float64) { nl.CarbsG = &v },
	"fat_total":       func(nl *model.NutritionLog, v float64) { nl.FatG = &v },
	"fiber":           func(nl *model.NutritionLog, v float64) { nl.FiberG = &v },
	"sugar":           func(nl *model.NutritionLog, v float64) { nl.SugarG = &v },
	"sodium":          func(nl *model.NutritionLog, v float64) { nl.SodiumMg = &v },
}

// nutrientDestinationUnit names the unit each core nutrient column is DECLARED in.
// Everything else lands in NutrientsJSON, where the unit travels with the value.
var nutrientDestinationUnit = map[string]string{
	"protein": "g", "carbohydrates": "g", "fat_total": "g",
	"fiber": "g", "sugar": "g",
	"sodium": "mg",
}

// massUnitInGrams is the value of one of each mass unit, expressed in grams.
var massUnitInGrams = map[string]float64{
	"kg": 1000, "g": 1, "mg": 0.001, "mcg": 0.000001,
}

// scaleToDestination converts a nutrient value into the unit its destination
// column is named for, and reports whether it could.
//
// This exists because every dietary mass reached the database in KILOGRAMS.
// The SDK's extractQuantityValue returns the FIRST unit a sample is
// compatible with from an ordered list where "kg" sits at index 4 and "g", "mg"
// and "mcg" at 20, 21 and 22. Every mass quantity is compatible with kilograms,
// so those later entries are unreachable and the wire record says "kg" honestly.
// A mapper that wrote that number into a column named _g stored every mass
// 1,000x (grams) or 1,000,000x (milligrams) too low, while energy_kcal stayed
// correct: energy is not mass, so it never matches "kg".
//
// Scoped deliberately to nutrition. A global kg->g rule in wireUnitConversion
// would also catch bodyMass, which is legitimately kilograms, and turn a correct
// 78 kg into 78,000.
//
// An unrecognised unit returns ok=false rather than a guess: a silent wrong
// number is what produced this defect, and a dropped one is recoverable.
func scaleToDestination(nutrient string, value float64, unit string) (float64, bool) {
	dest, known := nutrientDestinationUnit[nutrient]
	if !known {
		return value, true // not a core column; the unit travels with the value
	}
	from, ok := massUnitInGrams[unit]
	if !ok {
		return value, false
	}
	to := massUnitInGrams[dest]
	return value * from / to, true
}

// mapDietaryQuantity handles individual dietary quantity records (e.g.,
// HKQuantityTypeIdentifierDietaryProtein). Each record produces a NutritionLog
// with a single nutrient field populated.
func (m *mapper) mapDietaryQuantity(record map[string]any, userID string) (*model.MapResult, error) {
	qt, _ := record["quantityType"].(string)

	nutrientName, ok := dietaryNutrient(qt)
	if !ok {
		return nil, nil
	}

	value, err := extractFloat(record, "value")
	if err != nil {
		return nil, fmt.Errorf("dietary %s: %w", qt, err)
	}

	unit, _ := record["unit"].(string)
	value, canonicalUnit := ConvertValue(value, unit)

	scaled, ok := scaleToDestination(nutrientName, value, canonicalUnit)
	if !ok {
		return nil, fmt.Errorf("dietary %s: unit %q does not convert to the %s column's unit",
			qt, canonicalUnit, nutrientName)
	}
	value = scaled

	startDate, endDate, tz, err := ParseTimestamp(record)
	if err != nil {
		return nil, fmt.Errorf("dietary %s: %w", qt, err)
	}

	nl := model.NutritionLog{
		UserID:    userID,
		Provider:  "apple_health",
		StartDate: startDate,
		EndDate:   &endDate,
		MealType:  "", // the SDK sends no meal type
		Metadata:  withTimezone(buildMetadata(record, nil), tz),
	}

	// Set the appropriate nutrient field
	if setter, ok := coreNutrientFields[nutrientName]; ok {
		setter(&nl, value)
	} else {
		// Store in NutrientsJSON for non-core nutrients
		nl.NutrientsJSON = map[string]any{
			nutrientName: value,
		}
	}

	nl.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":    userID,
		"provider":   "apple_health",
		"nutrient":   nutrientName,
		"start_date": startDate.UTC().Format(time.RFC3339Nano),
		"value":      model.CanonicalFloat(value),
	})

	return &model.MapResult{
		NutritionLogs: []model.NutritionLog{nl},
	}, nil
}

// mapFoodCorrelation handles food correlation records that contain multiple
// nested dietary objects. All nested dietary quantities are merged into a
// single NutritionLog.
func (m *mapper) mapFoodCorrelation(record map[string]any, userID string) (*model.MapResult, error) {
	startDate, endDate, tz, err := ParseTimestamp(record)
	if err != nil {
		return nil, fmt.Errorf("food correlation: %w", err)
	}

	objects, ok := record["objects"].([]any)
	if !ok {
		return nil, fmt.Errorf("food correlation: missing objects array")
	}

	nl := model.NutritionLog{
		UserID:    userID,
		Provider:  "apple_health",
		StartDate: startDate,
		EndDate:   &endDate,
		MealType:  "", // the SDK sends no meal type
		Metadata:  withTimezone(buildMetadata(record, nil), tz),
	}

	// Build hash fields with all nutrient values for deterministic hash
	hashFields := map[string]string{
		"user_id":    userID,
		"provider":   "apple_health",
		"start_date": startDate.UTC().Format(time.RFC3339Nano),
	}

	for _, obj := range objects {
		objMap, ok := obj.(map[string]any)
		if !ok {
			continue
		}

		sampleType, _ := objMap["sampleType"].(string)
		nutrientName, ok := dietaryNutrient(sampleType)
		if !ok {
			continue
		}

		value, err := extractFloat(objMap, "value")
		if err != nil {
			continue
		}

		unit, _ := objMap["unit"].(string)
		value, canonicalUnit := ConvertValue(value, unit)
		// Scaled like a single dietary record; a unit that does not convert
		// drops this nutrient, never the rest of the meal.
		if value, ok = scaleToDestination(nutrientName, value, canonicalUnit); !ok {
			continue
		}

		// Set the appropriate nutrient field
		if setter, ok := coreNutrientFields[nutrientName]; ok {
			setter(&nl, value)
		} else {
			if nl.NutrientsJSON == nil {
				nl.NutrientsJSON = make(map[string]any)
			}
			nl.NutrientsJSON[nutrientName] = value
		}

		hashFields[nutrientName] = model.CanonicalFloat(value)
	}

	nl.ContentHash = model.ComputeContentHash(hashFields)

	return &model.MapResult{
		NutritionLogs: []model.NutritionLog{nl},
	}, nil
}
