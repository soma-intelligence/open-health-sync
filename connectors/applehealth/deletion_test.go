// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const testUUID = "6F1A9D2B-0C4E-4B8A-9E61-3D2B7A5C1F00"

func TestDeletionLine(t *testing.T) {
	m := New()
	result, err := m.mapRecord(map[string]any{"deletedUuid": testUUID}, "user-123")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []model.Deletion{{ExternalID: testUUID}}, result.Deletions)
	assert.Equal(t, 1, result.RecordCount(), "a deletion line gives nothing but the deletion")
}

// The deletion field wins over any other discriminator on the line.
func TestDeletionLineWinsOverTypeFields(t *testing.T) {
	m := New()
	result, err := m.mapRecord(map[string]any{
		"deletedUuid":  testUUID,
		"quantityType": "HKQuantityTypeIdentifierBodyMass",
	}, "user-123")
	require.NoError(t, err)
	assert.Len(t, result.Deletions, 1)
	assert.Empty(t, result.QuantitySamples)
}

func TestDeletionLineRejectsBadUUID(t *testing.T) {
	m := New()
	for name, v := range map[string]any{
		"empty":     "",
		"number":    float64(42),
		"null":      nil,
		"too long":  strings.Repeat("A", maxUUIDLength+1),
		"an object": map[string]any{"uuid": testUUID},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := m.mapRecord(map[string]any{"deletedUuid": v}, "user-123")
			assert.Error(t, err)
		})
	}
}

// Every record a line with a "uuid" gives carries it as its external id, the
// key a deletion line names. A line without one (an export) keeps none.
func TestRecordsCarryTheirHealthKitUUID(t *testing.T) {
	m := New()
	base := func(fields map[string]any) map[string]any {
		out := map[string]any{
			"startDate": "2026-03-01T07:00:00Z", "endDate": "2026-03-01T07:00:00Z",
			"uuid": testUUID, "metadata": map[string]any{"HKTimeZone": "Europe/Paris"},
		}
		for k, v := range fields {
			out[k] = v
		}
		return out
	}

	res, err := m.mapRecord(base(map[string]any{
		"quantityType": "HKQuantityTypeIdentifierBodyMass", "value": 78.4, "unit": "kg",
	}), "u")
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 1)
	assert.Equal(t, testUUID, res.QuantitySamples[0].ExternalID)

	res, err = m.mapRecord(base(map[string]any{
		"categoryType": "HKCategoryTypeIdentifierMindfulSession", "value": float64(0),
	}), "u")
	require.NoError(t, err)
	require.Len(t, res.CategorySamples, 1)
	assert.Equal(t, testUUID, res.CategorySamples[0].ExternalID)

	res, err = m.mapRecord(base(map[string]any{
		"quantityType": "HKQuantityTypeIdentifierDietaryProtein", "value": 30.0, "unit": "g",
	}), "u")
	require.NoError(t, err)
	require.Len(t, res.NutritionLogs, 1)
	assert.Equal(t, testUUID, res.NutritionLogs[0].ExternalID)

	res, err = m.mapRecord(base(map[string]any{
		"correlationType": "HKCorrelationTypeIdentifierFood",
		"objects": []any{map[string]any{
			"sampleType": "HKQuantityTypeIdentifierDietaryProtein", "value": 30.0, "unit": "g",
		}},
	}), "u")
	require.NoError(t, err)
	require.Len(t, res.NutritionLogs, 1)
	assert.Equal(t, testUUID, res.NutritionLogs[0].ExternalID)

	res, err = m.mapRecord(base(map[string]any{
		"workoutActivityType": float64(37), "endDate": "2026-03-01T07:30:00Z", "duration": 1800.0,
	}), "u")
	require.NoError(t, err)
	require.Len(t, res.Workouts, 1)
	assert.Equal(t, testUUID, res.Workouts[0].ExternalID)

	// Blood pressure: each member is "<correlation uuid>_<type>", so the
	// deletion of the correlation removes both.
	res, err = m.mapRecord(base(map[string]any{
		"correlationType": "HKCorrelationTypeIdentifierBloodPressure",
		"objects": []any{
			map[string]any{"sampleType": "HKQuantityTypeIdentifierBloodPressureSystolic", "value": 121.0, "unit": "mmHg"},
			map[string]any{"sampleType": "HKQuantityTypeIdentifierBloodPressureDiastolic", "value": 79.0, "unit": "mmHg"},
		},
	}), "u")
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 2)
	for _, s := range res.QuantitySamples {
		assert.Equal(t, testUUID+"_"+s.QuantityType, s.ExternalID)
	}

	noUUID := base(map[string]any{"quantityType": "HKQuantityTypeIdentifierBodyMass", "value": 78.4, "unit": "kg"})
	delete(noUUID, "uuid")
	res, err = m.mapRecord(noUUID, "u")
	require.NoError(t, err)
	assert.Empty(t, res.QuantitySamples[0].ExternalID)
}

// The UUID is not part of the content hash: the same reading from an export
// (no UUID) and from the SDK is still stored once.
func TestUUIDLeavesContentHashAlone(t *testing.T) {
	m := New()
	line := map[string]any{
		"quantityType": "HKQuantityTypeIdentifierBodyMass", "value": 78.4, "unit": "kg",
		"startDate": "2026-03-01T07:00:00Z", "endDate": "2026-03-01T07:00:00Z",
	}
	plain, err := m.mapRecord(line, "u")
	require.NoError(t, err)
	line["uuid"] = testUUID
	withID, err := m.mapRecord(line, "u")
	require.NoError(t, err)
	assert.Equal(t, plain.QuantitySamples[0].ContentHash, withID.QuantitySamples[0].ContentHash)
}
