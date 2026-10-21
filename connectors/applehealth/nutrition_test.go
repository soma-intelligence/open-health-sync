// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"testing"

	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNutritionDietaryProtein(t *testing.T) {
	m := New()
	record := loadTestFixture(t, "nutrition_dietary.json")

	result, err := m.mapRecord(record, "user-123")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.NutritionLogs, 1)

	nl := result.NutritionLogs[0]
	assert.Equal(t, "user-123", nl.UserID)
	assert.Equal(t, "apple_health", nl.Provider)
	require.NotNil(t, nl.ProteinG)
	assert.Equal(t, 25.0, *nl.ProteinG)
	assert.NotEmpty(t, nl.ContentHash)
}

func TestNutritionDietaryEnergy(t *testing.T) {
	m := New()
	record := map[string]any{
		"startDate":    "2025-03-01T12:00:00Z",
		"endDate":      "2025-03-01T12:30:00Z",
		"quantityType": "HKQuantityTypeIdentifierDietaryEnergyConsumed",
		"value":        500.0,
		"unit":         "kcal",
		"sourceName":   "Example Food Log",
		"metadata":     map[string]any{},
	}

	result, err := m.mapRecord(record, "user-123")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.NutritionLogs, 1)

	nl := result.NutritionLogs[0]
	require.NotNil(t, nl.EnergyKcal)
	assert.Equal(t, 500.0, *nl.EnergyKcal)
}

func TestNutritionFoodCorrelation(t *testing.T) {
	m := New()
	record := loadTestFixture(t, "correlation_food.json")

	result, err := m.mapRecord(record, "user-123")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.NutritionLogs, 1)

	nl := result.NutritionLogs[0]
	assert.Equal(t, "user-123", nl.UserID)
	assert.Equal(t, "apple_health", nl.Provider)

	require.NotNil(t, nl.EnergyKcal)
	assert.Equal(t, 450.0, *nl.EnergyKcal)

	require.NotNil(t, nl.ProteinG)
	assert.Equal(t, 25.0, *nl.ProteinG)

	require.NotNil(t, nl.CarbsG)
	assert.Equal(t, 55.0, *nl.CarbsG)

	require.NotNil(t, nl.FatG)
	assert.Equal(t, 12.0, *nl.FatG)

	assert.NotEmpty(t, nl.ContentHash)
}

func TestNutritionUnknownDietaryType(t *testing.T) {
	m := New()
	record := map[string]any{
		"startDate":    "2025-03-01T12:00:00Z",
		"endDate":      "2025-03-01T12:30:00Z",
		"quantityType": "HKQuantityTypeIdentifierDietaryVitaminB6",
		"value":        1.5,
		"unit":         "mg",
		"sourceName":   "Example Food Log",
		"metadata":     map[string]any{},
	}

	result, err := m.mapRecord(record, "user-123")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.NutritionLogs, 1)

	nl := result.NutritionLogs[0]
	// vitamin_b6 is not in the core fields -- should be in NutrientsJSON
	assert.NotNil(t, nl.NutrientsJSON)
	assert.Equal(t, 1.5, nl.NutrientsJSON["vitamin_b6"])
}

func TestNutritionContentHash(t *testing.T) {
	m := New()
	record := loadTestFixture(t, "nutrition_dietary.json")

	result1, err := m.mapRecord(record, "user-123")
	require.NoError(t, err)

	result2, err := m.mapRecord(record, "user-123")
	require.NoError(t, err)

	assert.Equal(t, result1.NutritionLogs[0].ContentHash,
		result2.NutritionLogs[0].ContentHash,
		"same nutrition record must produce same content hash")
}

// TestMapDietaryQuantity_ScalesMassToTheDestinationColumn guards against mass values stored in the wrong unit.
//
// Every dietary mass reached the database in KILOGRAMS. The SDK's
// extractQuantityValue walks an ordered unit list and returns the FIRST unit the
// sample is compatible with; "kg" sits at index 4 and "g"/"mg"/"mcg" at 20/21/22,
// and every mass quantity is compatible with kilograms, so the later entries are
// unreachable code. The wire record then says unit "kg" honestly, and this mapper
// wrote it straight into a column named _g.
//
// Unscaled, a gram column reads 1,000x low and a milligram column 1,000,000x
// low, while energy_kcal is right because energy is not mass.
//
// The conversion must be scoped to nutrition. A global kg->g rule would also hit
// bodyMass, which is legitimately kilograms, and turn 78 kg into 78,000.
func TestMapDietaryQuantity_ScalesMassToTheDestinationColumn(t *testing.T) {
	m := newMapper()
	cases := []struct {
		name, qt, unit string
		in, want       float64
		get            func(*model.NutritionLog) *float64
	}{
		{"protein in kg lands as grams", "HKQuantityTypeIdentifierDietaryProtein", "kg",
			0.136, 136, func(n *model.NutritionLog) *float64 { return n.ProteinG }},
		{"protein already in grams is untouched", "HKQuantityTypeIdentifierDietaryProtein", "g",
			136, 136, func(n *model.NutritionLog) *float64 { return n.ProteinG }},
		{"fat in kg lands as grams", "HKQuantityTypeIdentifierDietaryFatTotal", "kg",
			0.28, 280, func(n *model.NutritionLog) *float64 { return n.FatG }},
		{"sodium in kg lands as milligrams", "HKQuantityTypeIdentifierDietarySodium", "kg",
			0.0538, 53800, func(n *model.NutritionLog) *float64 { return n.SodiumMg }},
		{"sodium already in mg is untouched", "HKQuantityTypeIdentifierDietarySodium", "mg",
			2300, 2300, func(n *model.NutritionLog) *float64 { return n.SodiumMg }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := m.mapDietaryQuantity(map[string]any{
				"quantityType": c.qt,
				"value":        c.in,
				"unit":         c.unit,
				"startDate":    "2026-09-01T12:00:00Z",
				"endDate":      "2026-09-01T12:00:00Z",
			}, "user-1")
			require.NoError(t, err)
			require.NotNil(t, res)
			require.Len(t, res.NutritionLogs, 1)
			got := c.get(&res.NutritionLogs[0])
			require.NotNil(t, got, "the nutrient field must be set")
			assert.InDelta(t, c.want, *got, 0.001,
				"%s in %s must be stored in the destination column's unit", c.qt, c.unit)
		})
	}
}

// A food entry goes through the same unit conversion as a single dietary
// sample: its masses used to land in their column unconverted.
func TestMapFoodCorrelation_ConvertsUnits(t *testing.T) {
	m := newMapper()
	res, err := m.mapFoodCorrelation(map[string]any{
		"startDate":       "2026-09-01T12:00:00Z",
		"endDate":         "2026-09-01T12:20:00Z",
		"correlationType": "HKCorrelationTypeIdentifierFood",
		"objects": []any{
			map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryProtein", "value": 0.025, "unit": "kg"},
			map[string]any{"sampleType": "HKQuantityTypeIdentifierDietarySodium", "value": 0.0008, "unit": "kg"},
			map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryCaffeine", "value": 0.0001, "unit": "kg"},
			map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryWater", "value": 0.33, "unit": "L"},
			map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryVitaminD", "value": 0.000005, "unit": "g"},
		},
	}, "user-1")
	require.NoError(t, err)
	require.Len(t, res.NutritionLogs, 1)
	nl := res.NutritionLogs[0]
	require.NotNil(t, nl.ProteinG)
	require.NotNil(t, nl.SodiumMg)
	assert.InDelta(t, 25, *nl.ProteinG, 1e-9)
	assert.InDelta(t, 800, *nl.SodiumMg, 1e-9)
	assert.InDelta(t, 100, nl.NutrientsJSON["caffeine"], 1e-9, "caffeine in mg")
	assert.InDelta(t, 330, nl.NutrientsJSON["water"], 1e-9, "water in mL")
	assert.InDelta(t, 5, nl.NutrientsJSON["vitamin_d"], 1e-9, "vitamin D in mcg")
}

func TestMapFoodCorrelation_RefusesAnUnknownUnit(t *testing.T) {
	m := newMapper()
	_, err := m.mapFoodCorrelation(map[string]any{
		"startDate":       "2026-09-01T12:00:00Z",
		"endDate":         "2026-09-01T12:20:00Z",
		"correlationType": "HKCorrelationTypeIdentifierFood",
		"objects": []any{
			map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryProtein", "value": 25.0, "unit": "oz"},
		},
	}, "user-1")
	assert.Error(t, err)
}

func TestMapDietaryQuantity_ExtendedNutrientsInTheirUnit(t *testing.T) {
	m := newMapper()
	for _, c := range []struct {
		qt, unit, nutrient string
		in, want           float64
	}{
		{"HKQuantityTypeIdentifierDietaryCaffeine", "kg", "caffeine", 0.000095, 95},
		{"HKQuantityTypeIdentifierDietaryCaffeine", "mg", "caffeine", 95, 95},
		{"HKQuantityTypeIdentifierDietaryWater", "mL", "water", 250, 250},
		{"HKQuantityTypeIdentifierDietaryWater", "fl_oz_us", "water", 8, 236.5882365},
		{"HKQuantityTypeIdentifierDietaryVitaminB12", "mcg", "vitamin_b12", 2.4, 2.4},
	} {
		res, err := m.mapDietaryQuantity(map[string]any{
			"quantityType": c.qt, "value": c.in, "unit": c.unit,
			"startDate": "2026-09-01T12:00:00Z", "endDate": "2026-09-01T12:00:00Z",
		}, "user-1")
		require.NoError(t, err, c.qt)
		require.Len(t, res.NutritionLogs, 1)
		assert.InDelta(t, c.want, res.NutritionLogs[0].NutrientsJSON[c.nutrient], 1e-9, "%s in %s", c.qt, c.unit)
	}
}

// Every dietary type the connector stores has a declared unit.
func TestDietaryNutrientsHaveAUnit(t *testing.T) {
	for hk, n := range dietaryNutrients {
		_, ok := model.NutrientUnit(n)
		assert.True(t, ok, "%s (%s) has no unit in model.Nutrients", hk, n)
	}
}
