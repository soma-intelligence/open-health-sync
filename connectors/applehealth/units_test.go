// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func TestNormalizeUnit(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"count/min to bpm", "count/min", "bpm"},
		{"degC to celsius", "degC", "celsius"},
		// HKUnit.percent() is a 0..1 fraction that spells itself "%", so the
		// honest rename is to "fraction". model.ToStorageUnit turns that into
		// a real percentage for the types that declare one.
		{"percent is a fraction", "%", "fraction"},
		{"kg passthrough", "kg", "kg"},
		{"m passthrough", "m", "m"},
		{"kcal passthrough", "kcal", "kcal"},
		{"mmHg passthrough", "mmHg", "mmHg"},
		{"ms passthrough", "ms", "ms"},
		{"km to m", "km", "m"},
		{"lb to kg", "lb", "kg"},
		{"cal to kcal", "cal", "kcal"},
		{"Cal is a kilocalorie", "Cal", "kcal"},
		{"unknown passthrough", "unknown_unit", "unknown_unit"},
		{"dBASPL passthrough", "dBASPL", "dBASPL"},
		{"W passthrough", "W", "W"},
		{"m/s passthrough", "m/s", "m/s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeUnit(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertValue(t *testing.T) {
	tests := []struct {
		name         string
		value        float64
		unit         string
		expectedVal  float64
		expectedUnit string
		tolerance    float64
	}{
		{"km to m", 1.0, "km", 1000.0, "m", 0.001},
		{"lb to kg", 150.0, "lb", 68.0388555, "kg", 0.001},
		{"cal to kcal", 500.0, "cal", 0.5, "kcal", 0.0001},
		{"Cal no conversion", 1.2, "Cal", 1.2, "kcal", 0.0001},
		{"count/min no conversion", 72.5, "count/min", 72.5, "bpm", 0.001},
		{"kg no conversion", 70.0, "kg", 70.0, "kg", 0.001},
		{"m no conversion", 1500.0, "m", 1500.0, "m", 0.001},
		{"unknown no conversion", 42.0, "unknown_unit", 42.0, "unknown_unit", 0.001},
		// Rename only: the x100 belongs to the pipeline choke point, which knows
		// the quantity type and so knows whether a fraction has a percent target.
		{"percent renames without scaling", 0.97, "%", 0.97, "fraction", 0.0001},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, unit := ConvertValue(tt.value, tt.unit)
			assert.Equal(t, tt.expectedUnit, unit)
			assert.True(t, math.Abs(val-tt.expectedVal) < tt.tolerance,
				"expected %f, got %f", tt.expectedVal, val)
		})
	}
}

// TestConvertValueForTypeCountIsNotAlwaysATally pins the fix for unit
// failures on dimensionless quantities: an exporter
// exports every dimensionless HealthKit quantity as "count" (its unitMap tests
// .count() before .percent(), and a percent quantity is compatible with both), so
// SpO2 arrived as 0.97 "count" and BMI as 37.29 "count" with no conversion rule to
// their declared storage unit. The assertions run the full path, mapper rename
// then pipeline choke point, because the rename alone proves nothing: what matters
// is the number that reaches the column.
func TestConvertValueForTypeCountIsNotAlwaysATally(t *testing.T) {
	tests := []struct {
		name          string
		canonicalType string
		value         float64
		unit          string
		wantMapped    string  // unit after the apple_health rename
		wantStored    float64 // value after model.ToStorageUnit
		wantStoreUnit string
	}{
		// Payload shapes that would fail without the rename.
		{"spo2 fraction labelled count", "oxygen_saturation", 0.97, "count", "fraction", 97, "percent"},
		{"body fat fraction labelled count", "body_fat_percentage", 0.2766700172424316, "count", "fraction", 27.66700172424316, "percent"},
		// HealthKit really does report BMI in HKUnit.count(); the number is already
		// kg/m2, so this one is a rename with no rescale.
		{"bmi labelled count", "body_mass_index", 37.28691101074219, "count", "kg/m2", 37.28691101074219, "kg/m2"},
		// Regression guard: "count" still means a tally everywhere else.
		{"step count stays a tally", "step_count", 8421, "count", "count", 8421, "count"},
		{"flights climbed stays a tally", "flights_climbed", 12, "count", "count", 12, "count"},
		// The "%" spelling keeps working for the HealthKit sources that do send it.
		{"spo2 spelled percent", "oxygen_saturation", 0.97, "%", "fraction", 97, "percent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, unit := ConvertValueForType(tt.canonicalType, tt.value, tt.unit)
			assert.Equal(t, tt.wantMapped, unit, "unit after apple_health rename")

			stored, storedUnit, err := model.ToStorageUnit(tt.canonicalType, val, unit)
			require.NoError(t, err, "sample must not fail the unit choke point")
			assert.Equal(t, tt.wantStoreUnit, storedUnit)
			assert.InDelta(t, tt.wantStored, stored, 1e-9)
		})
	}
}

// TestEveryPercentMetricHasACountRoute closes the bug class rather than the three
// metrics that happened to hit it.
//
// The invariant: a dimensionless HealthKit quantity ALWAYS reaches this mapper
// spelled "count", never "%". HKQuantity.is(compatibleWith:) is true for .count()
// on every dimensionless quantity and the SDK's unitMap tests ("count",
// .count()) before ("%", .percent()), so the "%" branch is unreachable for them.
// Percent is dimensionless. Therefore every quantity type that apple_health can
// produce AND that mapper/units.go stores in "percent" must resolve from "count",
// or every one of its samples is dropped as a reason_code='unit' record.
//
// That is exactly how oxygen_saturation and body_fat_percentage were lost, silently,
// until the failure table had tens of thousands of rows. Adding a third percent
// metric without a count route would repeat it, and nothing else in the suite would
// notice. This test does.
func TestEveryPercentMetricHasACountRoute(t *testing.T) {
	// The canonical types apple_health can emit.
	produced := make(map[string]bool, len(quantityTypeCanonical))
	for _, canonical := range quantityTypeCanonical {
		produced[canonical] = true
	}

	checked := 0
	for qt, storage := range model.StorageUnits() {
		if storage != "percent" || !produced[qt] {
			continue
		}
		checked++
		t.Run(qt, func(t *testing.T) {
			require.Contains(t, dimensionlessCountUnit, qt,
				"%s is stored in percent and apple_health can emit it, so it will arrive "+
					"spelled \"count\" and needs an entry in dimensionlessCountUnit", qt)

			// And the route must actually land, not just exist.
			val, unit := ConvertValueForType(qt, 0.97, "count")
			stored, storedUnit, err := model.ToStorageUnit(qt, val, unit)
			require.NoError(t, err, "%s must survive the unit choke point when sent as \"count\"", qt)
			assert.Equal(t, "percent", storedUnit)
			assert.InDelta(t, 97, stored, 1e-9, "a 0..1 fraction must be rescaled, not stored raw")
		})
	}

	require.NotZero(t, checked, "no percent-stored apple_health metric found: the guard has "+
		"stopped guarding anything, which means a rename broke it rather than that the risk went away")
}

// TestConvertValueForTypeLeavesOtherProviderSpellingsAlone guards the blast radius
// of the rename: it is keyed by canonical quantity type, so it must not disturb a
// unit that already resolves, and it must not fire on a spelling that is not a
// dimensionless count.
func TestConvertValueForTypeLeavesOtherProviderSpellingsAlone(t *testing.T) {
	val, unit := ConvertValueForType("oxygen_saturation", 95, "percent")
	assert.Equal(t, "percent", unit)
	assert.Equal(t, float64(95), val)

	val, unit = ConvertValueForType("heart_rate", 72.5, "count/min")
	assert.Equal(t, "bpm", unit)
	assert.Equal(t, 72.5, val)

	// An unknown canonical type falls straight through to ConvertValue.
	val, unit = ConvertValueForType("blood_alcohol_content", 0.02, "%")
	assert.Equal(t, "fraction", unit)
	assert.Equal(t, 0.02, val)
}

// An export written with Calories as the energy unit spells every energy
// record "Cal", HealthKit's large calorie. The full path, mapper then
// pipeline choke point, must keep the number: the choke point lowercases
// units, and "cal" (the small calorie) is a thousandth of a kilocalorie.
func TestLargeCalorieKeepsItsValue(t *testing.T) {
	m := newMapper()
	for _, qt := range []string{
		"HKQuantityTypeIdentifierBasalEnergyBurned",
		"HKQuantityTypeIdentifierActiveEnergyBurned",
	} {
		res, err := m.mapRecord(map[string]any{
			"quantityType": qt, "value": 1.2, "unit": "Cal", "sourceName": "Apple Watch",
			"startDate": "2026-09-01T12:00:00Z", "endDate": "2026-09-01T12:01:00Z",
		}, "user-1")
		require.NoError(t, err, qt)
		require.Len(t, res.QuantitySamples, 1, qt)
		require.Empty(t, model.CanonicalizeUnits(res), qt)
		s := res.QuantitySamples[0]
		assert.Equal(t, "kcal", s.Unit, qt)
		assert.InDelta(t, 1.2, s.Value, 1e-12, qt)
	}

	res, err := m.mapRecord(map[string]any{
		"quantityType": "HKQuantityTypeIdentifierDietaryEnergyConsumed", "value": 640.0, "unit": "Cal",
		"startDate": "2026-09-01T12:00:00Z", "endDate": "2026-09-01T12:00:00Z",
	}, "user-1")
	require.NoError(t, err)
	require.Len(t, res.NutritionLogs, 1)
	require.NotNil(t, res.NutritionLogs[0].EnergyKcal)
	assert.Equal(t, 640.0, *res.NutritionLogs[0].EnergyKcal)
}
