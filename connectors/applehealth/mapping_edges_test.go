// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractInt(t *testing.T) {
	record := map[string]any{
		"float":    2.9,
		"int":      7,
		"string":   "3.9",
		"negative": "-1",
		"bad":      "two",
		"bool":     true,
	}
	cases := []struct {
		field   string
		want    int
		wantErr string
	}{
		{"float", 2, ""},
		{"int", 7, ""},
		{"string", 3, ""},
		{"negative", -1, ""},
		{"bad", 0, `unparseable bad "two"`},
		{"bool", 0, "unexpected type for bool: bool"},
		{"absent", 0, "missing absent field"},
	}
	for _, c := range cases {
		t.Run(c.field, func(t *testing.T) {
			got, err := extractInt(record, c.field)
			if c.wantErr != "" {
				require.ErrorContains(t, err, c.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestECGRecordingLenientFields(t *testing.T) {
	m := New()
	record := map[string]any{
		"type":                        "HKElectrocardiogram",
		"startDate":                   "2026-02-03T08:00:00Z",
		"endDate":                     "2026-02-03T08:00:30Z",
		"classification":              "2",
		"symptomsStatus":              "1",
		"samplingFrequency":           "512",
		"numberOfVoltageMeasurements": 15360.0,
		"metadata":                    map[string]any{"HKTimeZone": "Europe/Berlin"},
	}
	result, err := m.mapRecord(record, "user-1")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.ECGRecordings, 1)
	ecg := result.ECGRecordings[0]
	assert.Equal(t, 2, ecg.Classification, "a classification sent as a string is read")
	assert.Equal(t, 1, ecg.SymptomsStatus)
	assert.Equal(t, 512.0, ecg.SamplingFrequency)
	assert.Equal(t, 15360, ecg.NumberOfMeasurements)
	assert.Nil(t, ecg.AverageHeartRate, "no average heart rate without the field")
	assert.Empty(t, ecg.VoltagePath)
	assert.Equal(t, "Europe/Berlin", ecg.Metadata["timezone"])
	assert.Equal(t, time.Date(2026, 2, 3, 8, 0, 30, 0, time.UTC), ecg.EndDate)

	record["classification"] = "sinus"
	_, err = m.mapRecord(record, "user-1")
	require.ErrorContains(t, err, "ecg metadata:")

	delete(record, "startDate")
	_, err = m.mapRecord(record, "user-1")
	require.ErrorContains(t, err, "ecg metadata: missing startDate")
}

func TestECGVoltageMissingTime(t *testing.T) {
	m := newMapper()
	_, err := m.mapECGVoltage(map[string]any{"value": 0.0004}, "user-1")
	require.ErrorContains(t, err, "ecg voltage: missing timeSinceSampleStart field")

	res, err := m.mapECGVoltage(map[string]any{"value": "-0.00025", "timeSinceSampleStart": 0.5}, "user-1")
	require.NoError(t, err)
	require.Len(t, res.ECGSamples, 1)
	assert.InDelta(t, -250.0, res.ECGSamples[0].VoltageUV, 1e-9, "volts are stored as microvolts")
	assert.Equal(t, 0.5, res.ECGSamples[0].TimeSinceSampleStart)
}

// foodRecord is a food correlation with the given nested objects.
func foodRecord(objects ...any) map[string]any {
	return map[string]any{
		"startDate":       "2026-04-01T18:00:00Z",
		"endDate":         "2026-04-01T18:20:00Z",
		"correlationType": "HKCorrelationTypeIdentifierFood",
		"objects":         objects,
		"sourceName":      "Example Food Log",
		"metadata":        map[string]any{"HKTimeZone": "America/Chicago"},
	}
}

func TestFoodCorrelationSkipsUnusableObjects(t *testing.T) {
	m := New()
	record := foodRecord(
		"not an object",
		map[string]any{"sampleType": "HKQuantityTypeIdentifierHeartRate", "value": 70.0, "unit": "count/min"},
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietarySugar", "value": "lots", "unit": "g"},
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryFiber", "value": 6.0, "unit": "g"},
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietarySodium", "value": 480.0, "unit": "mg"},
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryCaffeine", "value": 95.0, "unit": "mg"},
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryIron", "value": "2.5", "unit": "mg"},
	)
	result, err := m.mapRecord(record, "user-1")
	require.NoError(t, err)
	require.Len(t, result.NutritionLogs, 1)
	nl := result.NutritionLogs[0]

	assert.Equal(t, time.Date(2026, 4, 1, 18, 0, 0, 0, time.UTC), nl.StartDate)
	require.NotNil(t, nl.EndDate)
	assert.Equal(t, time.Date(2026, 4, 1, 18, 20, 0, 0, time.UTC), *nl.EndDate)
	assert.Equal(t, "America/Chicago", nl.Metadata["timezone"])
	assert.Nil(t, nl.SugarG, "an unreadable value is skipped")
	assert.Nil(t, nl.EnergyKcal)
	require.NotNil(t, nl.FiberG)
	assert.Equal(t, 6.0, *nl.FiberG)
	require.NotNil(t, nl.SodiumMg)
	assert.Equal(t, 480.0, *nl.SodiumMg)
	assert.Equal(t, map[string]any{"caffeine": 95.0, "iron": 2.5}, nl.NutrientsJSON)

	// The hash covers every nutrient value.
	again, err := m.mapRecord(foodRecord(
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryFiber", "value": 6.0, "unit": "g"},
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietarySodium", "value": 480.0, "unit": "mg"},
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryCaffeine", "value": 95.0, "unit": "mg"},
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryIron", "value": 2.5, "unit": "mg"},
	), "user-1")
	require.NoError(t, err)
	assert.Equal(t, nl.ContentHash, again.NutritionLogs[0].ContentHash, "skipped objects do not change the hash")

	changed, err := m.mapRecord(foodRecord(
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryFiber", "value": 7.0, "unit": "g"},
	), "user-1")
	require.NoError(t, err)
	assert.NotEqual(t, nl.ContentHash, changed.NutritionLogs[0].ContentHash)
}

func TestFoodCorrelationEmptyAndBroken(t *testing.T) {
	m := New()
	result, err := m.mapRecord(foodRecord(), "user-1")
	require.NoError(t, err)
	require.Len(t, result.NutritionLogs, 1, "a meal without nutrients is still logged")
	assert.Nil(t, result.NutritionLogs[0].NutrientsJSON)
	assert.Nil(t, result.NutritionLogs[0].ProteinG)

	noObjects := foodRecord()
	delete(noObjects, "objects")
	_, err = m.mapRecord(noObjects, "user-1")
	require.EqualError(t, err, "food correlation: missing objects array")

	badStart := foodRecord()
	badStart["startDate"] = "April 1st"
	_, err = m.mapRecord(badStart, "user-1")
	require.ErrorContains(t, err, "food correlation: invalid startDate")
}

func TestDietaryQuantityErrors(t *testing.T) {
	m := newMapper()
	base := func() map[string]any {
		return map[string]any{
			"quantityType": "HKQuantityTypeIdentifierDietaryProtein",
			"value":        30.0,
			"unit":         "g",
			"startDate":    "2026-04-01T08:00:00Z",
		}
	}

	res, err := m.mapDietaryQuantity(base(), "user-1")
	require.NoError(t, err)
	require.NotNil(t, res.NutritionLogs[0].EndDate)
	assert.Equal(t, res.NutritionLogs[0].StartDate, *res.NutritionLogs[0].EndDate, "a missing endDate is the start")

	noValue := base()
	delete(noValue, "value")
	_, err = m.mapDietaryQuantity(noValue, "user-1")
	require.ErrorContains(t, err, "dietary HKQuantityTypeIdentifierDietaryProtein: missing value field")

	energyUnit := base()
	energyUnit["unit"] = "kcal"
	_, err = m.mapDietaryQuantity(energyUnit, "user-1")
	require.ErrorContains(t, err, `does not convert to the protein column's unit`)

	badDate := base()
	badDate["startDate"] = "yesterday"
	_, err = m.mapDietaryQuantity(badDate, "user-1")
	require.ErrorContains(t, err, "invalid startDate")

	notDietary := base()
	notDietary["quantityType"] = "HKQuantityTypeIdentifierStepCount"
	res, err = m.mapDietaryQuantity(notDietary, "user-1")
	require.NoError(t, err)
	assert.Nil(t, res)
}

func TestScaleToDestination(t *testing.T) {
	v, ok := scaleToDestination("sodium", 2, "g")
	require.True(t, ok)
	assert.InDelta(t, 2000.0, v, 1e-9)
	v, ok = scaleToDestination("fiber", 500, "mcg")
	require.True(t, ok)
	assert.InDelta(t, 0.0005, v, 1e-12)
	v, ok = scaleToDestination("caffeine", 80, "mg")
	require.True(t, ok, "a nutrient without a column keeps its unit")
	assert.Equal(t, 80.0, v)
	_, ok = scaleToDestination("sugar", 1, "oz")
	assert.False(t, ok)
}

func TestBloodPressureCorrelationEdges(t *testing.T) {
	m := New()
	record := map[string]any{
		"startDate":       "2026-04-02T07:00:00Z",
		"endDate":         "2026-04-02T07:00:00Z",
		"correlationType": "HKCorrelationTypeIdentifierBloodPressure",
		"sourceName":      "Example Cuff",
		"metadata":        map[string]any{"HKTimeZone": "Asia/Tokyo"},
		"objects": []any{
			42.0,
			map[string]any{"sampleType": "HKQuantityTypeIdentifierUnknownThing", "value": 1.0},
			map[string]any{"sampleType": "HKQuantityTypeIdentifierBloodPressureSystolic", "value": "high", "unit": "mmHg"},
			map[string]any{"sampleType": "HKQuantityTypeIdentifierBloodPressureDiastolic", "value": 78.0, "unit": "mmHg"},
		},
	}
	result, err := m.mapRecord(record, "user-1")
	require.NoError(t, err)
	require.Len(t, result.QuantitySamples, 1)
	s := result.QuantitySamples[0]
	assert.Equal(t, "blood_pressure_diastolic", s.QuantityType)
	assert.Equal(t, 78.0, s.Value)
	assert.Equal(t, "mmHg", s.Unit)
	assert.Equal(t, time.Date(2026, 4, 2, 7, 0, 0, 0, time.UTC), s.StartDate, "the objects take the parent's time")
	assert.Equal(t, "Asia/Tokyo", s.Metadata["timezone"], "and the parent's zone")
	assert.Equal(t, "HKCorrelationTypeIdentifierBloodPressure", s.Metadata["correlation_type"])

	// Nothing usable: no samples, no error.
	record["objects"] = []any{map[string]any{"sampleType": "HKQuantityTypeIdentifierUnknownThing"}}
	result, err = m.mapRecord(record, "user-1")
	require.NoError(t, err)
	assert.Nil(t, result)

	delete(record, "objects")
	_, err = m.mapRecord(record, "user-1")
	require.ErrorContains(t, err, "missing objects array")

	record["objects"] = []any{map[string]any{"sampleType": "HKQuantityTypeIdentifierBloodPressureSystolic", "value": 120.0, "unit": "mmHg"}}
	record["startDate"] = "not a date"
	_, err = m.mapRecord(record, "user-1")
	require.ErrorContains(t, err, "correlation HKCorrelationTypeIdentifierBloodPressure:")
}

// Nested nutrients are scaled to their column's unit, as single dietary
// records are: kilograms of protein are stored as grams, sodium as mg.
func TestFoodCorrelationScalesToColumnUnits(t *testing.T) {
	m := New()
	result, err := m.mapRecord(foodRecord(
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryProtein", "value": 0.025, "unit": "kg"},
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietarySodium", "value": 0.0005, "unit": "kg"},
		map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryFiber", "value": 4.0, "unit": "count"},
	), "user-1")
	require.NoError(t, err)
	require.Len(t, result.NutritionLogs, 1)
	nl := result.NutritionLogs[0]
	require.NotNil(t, nl.ProteinG)
	assert.InDelta(t, 25.0, *nl.ProteinG, 1e-9)
	require.NotNil(t, nl.SodiumMg)
	assert.InDelta(t, 500.0, *nl.SodiumMg, 1e-9)
	assert.Nil(t, nl.FiberG, "a unit that is not a mass is dropped, never stored as grams")

	single, err := m.mapRecord(map[string]any{
		"quantityType": "HKQuantityTypeIdentifierDietaryProtein", "value": 0.025, "unit": "kg",
		"startDate": "2026-04-01T18:00:00Z", "endDate": "2026-04-01T18:20:00Z",
	}, "user-1")
	require.NoError(t, err)
	assert.Equal(t, *single.NutritionLogs[0].ProteinG, *nl.ProteinG, "same value as a single dietary record")
}
