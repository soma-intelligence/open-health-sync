// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"fmt"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// registerQuantityHandlers registers a handler for each known quantity type.
func (m *mapper) registerQuantityHandlers() {
	for hkType := range quantityTypeCanonical {
		m.quantityHandlers[hkType] = m.mapQuantity
	}
}

// mapQuantity transforms a raw Apple Health quantity record into a canonical QuantitySample.
func (m *mapper) mapQuantity(record map[string]any, userID string) (*model.MapResult, error) {
	qt, _ := record["quantityType"].(string)

	canonicalType, ok := quantityTypeCanonical[qt]
	if !ok {
		return nil, nil
	}

	// A quantity sample with no value field carries no measurement to store
	// (e.g. user-entered ElectrodermalActivity placeholders arrive value-less).
	// Skip it gracefully rather than failing the record into the failure log.
	if _, present := record["value"]; !present {
		return nil, nil
	}

	// Extract value with best-effort coercion
	value, err := extractFloat(record, "value")
	if err != nil {
		return nil, fmt.Errorf("quantity %s: %w", qt, err)
	}

	unit, _ := record["unit"].(string)

	// Parse timestamps
	startDate, endDate, tz, err := ParseTimestamp(record)
	if err != nil {
		return nil, fmt.Errorf("quantity %s: %w", qt, err)
	}

	// Convert units to canonical form
	originalUnit := unit
	originalValue := value
	value, unit = ConvertValueForType(canonicalType, value, unit)

	// Build metadata preserving original values
	metadata := buildMetadata(record, map[string]any{
		"original_unit":  originalUnit,
		"original_value": originalValue,
	})
	if tz != "" {
		metadata["timezone"] = tz
	}

	sample := model.QuantitySample{
		UserID:         userID,
		Provider:       "apple_health",
		SourceName:     stringField(record, "sourceName"),
		SourceBundleID: stringField(record, "sourceBundleId"),
		DeviceID:       stringField(record, "device"),
		ProductType:    stringField(record, "productType"),
		DeviceModel:    stringField(record, "deviceModel"),
		WasUserEntered: boolField(record, "wasUserEntered"),
		ExternalID:     sampleExternalID(record),
		QuantityType:   canonicalType,
		Value:          value,
		Unit:           unit,
		StartDate:      startDate,
		EndDate:        endDate,
		Metadata:       metadata,
	}

	// Compute content hash from semantic identity fields
	sample.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      "apple_health",
		"quantity_type": canonicalType,
		"start_date":    startDate.UTC().Format(time.RFC3339Nano),
		"value":         model.CanonicalFloat(value),
	})

	return &model.MapResult{
		QuantitySamples: []model.QuantitySample{sample},
	}, nil
}

// handleCorrelation expands a correlation record (e.g., blood pressure)
// into individual QuantitySamples.
func (m *mapper) handleCorrelation(record map[string]any, userID string) (*model.MapResult, error) {
	corrType, _ := record["correlationType"].(string)

	// Food correlations dispatch to the nutrition handler
	if corrType == "HKCorrelationTypeIdentifierFood" {
		return m.mapFoodCorrelation(record, userID)
	}

	objects, ok := record["objects"].([]any)
	if !ok {
		return nil, fmt.Errorf("correlation %s: missing objects array", corrType)
	}

	result := &model.MapResult{}

	for _, obj := range objects {
		objMap, ok := obj.(map[string]any)
		if !ok {
			continue
		}

		// Extract fields from the sub-object
		sampleType, _ := objMap["sampleType"].(string)
		canonicalType, ok := quantityTypeCanonical[sampleType]
		if !ok {
			continue
		}

		value, err := extractFloat(objMap, "value")
		if err != nil {
			continue
		}

		unit, _ := objMap["unit"].(string)

		startDate, endDate, tz, err := ParseTimestamp(objMap)
		if err != nil {
			// Fall back to parent record timestamps
			startDate, endDate, tz, err = ParseTimestamp(record)
			if err != nil {
				return nil, fmt.Errorf("correlation %s: %w", corrType, err)
			}
		}

		// The objects carry no metadata of their own; the zone is the parent's.
		if tz == "" {
			tz = timezoneOf(record)
		}

		originalUnit := unit
		originalValue := value
		value, unit = ConvertValueForType(canonicalType, value, unit)

		metadata := buildMetadata(record, map[string]any{
			"original_unit":    originalUnit,
			"original_value":   originalValue,
			"correlation_type": corrType,
		})
		if tz != "" {
			metadata["timezone"] = tz
		}

		sample := model.QuantitySample{
			UserID:         userID,
			Provider:       "apple_health",
			SourceName:     stringField(record, "sourceName"),
			SourceBundleID: stringField(record, "sourceBundleId"),
			DeviceID:       stringField(record, "device"),
			ExternalID:     memberExternalID(record, canonicalType),
			QuantityType:   canonicalType,
			Value:          value,
			Unit:           unit,
			StartDate:      startDate,
			EndDate:        endDate,
			Metadata:       metadata,
		}

		sample.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id":       userID,
			"provider":      "apple_health",
			"quantity_type": canonicalType,
			"start_date":    startDate.UTC().Format(time.RFC3339Nano),
			"value":         model.CanonicalFloat(value),
		})

		result.QuantitySamples = append(result.QuantitySamples, sample)
	}

	if len(result.QuantitySamples) == 0 {
		return nil, nil
	}

	return result, nil
}

// extractFloat extracts a float64 from a record field with best-effort coercion.
// Handles both float64 (from JSON number) and string (from some sources).
func extractFloat(record map[string]any, field string) (float64, error) {
	v, ok := record[field]
	if !ok {
		return 0, fmt.Errorf("missing %s field", field)
	}

	switch val := v.(type) {
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	case string:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return 0, fmt.Errorf("unparseable %s %q: %w", field, val, err)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("unexpected type for %s: %T", field, v)
	}
}

// stringField safely extracts a string field from a record.
func stringField(record map[string]any, key string) string {
	s, _ := record[key].(string)
	return s
}

// boolField extracts an optional boolean, returning nil when the key is absent
// or is not a boolean. The nil matters: for wasUserEntered, "absent" means the
// client predates the field and genuinely does not know, which is not the same
// claim as "measured". Collapsing the two into false would assert something the
// data never said.
func boolField(record map[string]any, key string) *bool {
	b, ok := record[key].(bool)
	if !ok {
		return nil
	}
	return &b
}

// buildMetadata creates a metadata map preserving raw record fields
// and adding extra key-value pairs.
func buildMetadata(record map[string]any, extra map[string]any) map[string]any {
	md := make(map[string]any)

	// Preserve source information
	if sn, ok := record["sourceName"]; ok {
		md["sourceName"] = sn
	}
	if sb, ok := record["sourceBundleId"]; ok {
		md["sourceBundleId"] = sb
	}
	if dev, ok := record["device"]; ok {
		md["device"] = dev
	}
	// buildMetadata is an allow-list, not a passthrough: a top-level field that
	// is not named here is dropped without a trace. These four are the half of
	// the 0.2.0 provenance that gets no column, so this is the only thing
	// keeping them.
	for _, k := range []string{"osVersion", "deviceHardwareVersion", "deviceSoftwareVersion", "deviceManufacturer"} {
		if v, ok := record[k]; ok {
			md[k] = v
		}
	}
	if origMD, ok := record["metadata"].(map[string]any); ok {
		for k, v := range origMD {
			md[k] = v
		}
	}

	// Add extra fields
	for k, v := range extra {
		md[k] = v
	}

	return md
}
