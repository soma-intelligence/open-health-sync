// SPDX-License-Identifier: AGPL-3.0-only

package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToNutrientUnit(t *testing.T) {
	for _, c := range []struct {
		name, nutrient, unit string
		in, want             float64
	}{
		{"protein in kg", "protein", "kg", 0.136, 136},
		{"protein in g", "protein", "g", 136, 136},
		{"sodium in g", "sodium", "g", 2.3, 2300},
		{"sodium in mg", "sodium", "mg", 2300, 2300},
		{"caffeine in kg", "caffeine", "kg", 0.0001, 100},
		{"vitamin D in g", "vitamin_d", "g", 0.00002, 20},
		{"vitamin D in ug", "vitamin_d", "ug", 20, 20},
		{"vitamin D in mg", "vitamin_d", "mg", 0.02, 20},
		{"iron in mcg", "iron", "mcg", 8000, 8},
		{"water in L", "water", "L", 0.25, 250},
		{"water in mL", "water", "mL", 250, 250},
		{"water in US fluid ounces", "water", "fl_oz_us", 8, 236.5882365},
		{"water in imperial fluid ounces", "water", "fl_oz_imp", 10, 284.130625},
		{"energy in kJ", "energy_consumed", "kJ", 2092, 500},
		{"energy in kcal", "energy_consumed", "kcal", 500, 500},
		{"energy in Cal", "energy_consumed", "Cal", 640, 640},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ToNutrientUnit(c.nutrient, c.in, c.unit)
			require.NoError(t, err)
			assert.InDelta(t, c.want, got, 1e-9)
		})
	}

	// A value already in the nutrient's unit is not touched at all.
	got, err := ToNutrientUnit("sodium", 2300.1, "mg")
	require.NoError(t, err)
	assert.Equal(t, 2300.1, got)

	for _, c := range []struct{ nutrient, unit string }{
		{"protein", ""},   // nothing says which unit
		{"protein", "mL"}, // another dimension
		{"water", "g"},    // another dimension
		{"energy_consumed", "g"},
		{"energy_consumed", "cal"}, // the small calorie: Apple's mapper converts it
		{"caffeine", "grain"},      // unknown
	} {
		_, err := ToNutrientUnit(c.nutrient, 1, c.unit)
		assert.Error(t, err, "%s in %q", c.nutrient, c.unit)
	}

	// A nutrient the table does not know keeps its value.
	got, err = ToNutrientUnit("lutein", 3, "mg")
	require.NoError(t, err)
	assert.Equal(t, 3.0, got)
}

func TestNutrientsTable(t *testing.T) {
	seen := map[string]bool{}
	columns := 0
	for _, n := range Nutrients {
		assert.False(t, seen[n.Name], "%s listed twice", n.Name)
		seen[n.Name] = true
		_, ok := nutrientUnitScale[strings.ToLower(n.Unit)]
		assert.True(t, ok, "%s: unit %s has no scale", n.Name, n.Unit)
		if n.Column != "" {
			columns++
		}
	}
	assert.Equal(t, 7, columns, "the seven nutrition_logs columns")
}

func TestSetNutrient(t *testing.T) {
	var nl NutritionLog
	nl.SetNutrient("energy_consumed", 500)
	nl.SetNutrient("sodium", 2300)
	nl.SetNutrient("caffeine", 95)
	require.NotNil(t, nl.EnergyKcal)
	require.NotNil(t, nl.SodiumMg)
	assert.Equal(t, 500.0, *nl.EnergyKcal)
	assert.Equal(t, 2300.0, *nl.SodiumMg)
	assert.Equal(t, map[string]any{"caffeine": 95.0}, nl.NutrientsJSON)
}
