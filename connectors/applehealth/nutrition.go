// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// nutrientValue converts a dietary value into the unit its nutrient is
// stored in (model.Nutrients). The line's unit is HealthKit's spelling,
// normalized first ("cal" is a small calorie).
//
// The conversion matters because dietary masses have reached the server in
// kilograms: an SDK build picked the first unit a sample was compatible with
// from an ordered list where "kg" came before "g", "mg" and "mcg", so every
// mass said "kg", honestly, and landed 1,000x (grams) or 1,000,000x
// (milligrams) too low in its column. It is scoped to nutrition: a global
// kg to g rule would also turn a body mass of 78 kg into 78,000. A unit that
// does not convert is an error rather than a guess.
func nutrientValue(nutrient string, value float64, unit any) (float64, error) {
	u, _ := unit.(string)
	value, canonical := ConvertValue(value, u)
	return model.ToNutrientUnit(nutrient, value, canonical)
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

	value, err = nutrientValue(nutrientName, value, record["unit"])
	if err != nil {
		return nil, fmt.Errorf("dietary %s: %w", qt, err)
	}

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

	nl.SetNutrient(nutrientName, value)

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

		value, err = nutrientValue(nutrientName, value, objMap["unit"])
		if err != nil {
			return nil, fmt.Errorf("food correlation: %s: %w", sampleType, err)
		}
		nl.SetNutrient(nutrientName, value)

		hashFields[nutrientName] = model.CanonicalFloat(value)
	}

	nl.ContentHash = model.ComputeContentHash(hashFields)

	return &model.MapResult{
		NutritionLogs: []model.NutritionLog{nl},
	}, nil
}
