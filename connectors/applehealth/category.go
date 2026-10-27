// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// registerCategoryHandlers registers a handler for each known category type,
// excluding sleep analysis which has its own dedicated handler.
func (m *mapper) registerCategoryHandlers() {
	for hkType := range categoryTypeCanonical {
		// Sleep analysis is handled by the sleep handler, not the category handler
		if hkType == "HKCategoryTypeIdentifierSleepAnalysis" {
			continue
		}
		m.categoryHandlers[hkType] = m.mapCategory
	}
}

// categoryValueLabels maps category types to their value label lookups.
// Only populated for types where the integer value has a meaningful label.
var categoryValueLabels = map[string]map[int]string{
	"menstrual_flow": {
		1: "unspecified",
		2: "light",
		3: "medium",
		4: "heavy",
		5: "none",
	},
	"cervical_mucus_quality": {
		1: "dry",
		2: "sticky",
		3: "creamy",
		4: "watery",
		5: "egg_white",
	},
	"ovulation_test_result": {
		1: "negative",
		2: "luteinizing_hormone_surge",
		3: "indeterminate",
		4: "estrogen_surge",
	},
	"apple_stand_hour": {
		0: "idle",
		1: "stood",
	},
	"pregnancy_test_result": {
		1: "negative",
		2: "positive",
		3: "indeterminate",
	},
	"progesterone_test_result": {
		1: "negative",
		2: "positive",
		3: "indeterminate",
	},
}

// mapCategory transforms a raw Apple Health category record into a canonical CategorySample.
func (m *mapper) mapCategory(record map[string]any, userID string) (*model.MapResult, error) {
	ct, _ := record["categoryType"].(string)

	canonicalType, ok := categoryTypeCanonical[ct]
	if !ok {
		return nil, nil
	}

	// Extract value (integer for category types)
	valueInt := 0
	if v, ok := record["value"].(float64); ok {
		valueInt = int(v)
	}

	// Look up human-readable label if available
	valueLabel := ""
	if labels, ok := categoryValueLabels[canonicalType]; ok {
		if label, ok := labels[valueInt]; ok {
			valueLabel = label
		}
	}

	startDate, endDate, tz, err := ParseTimestamp(record)
	if err != nil {
		return nil, fmt.Errorf("category %s: %w", ct, err)
	}

	metadata := buildMetadata(record, nil)
	if tz != "" {
		metadata["timezone"] = tz
	}

	sample := model.CategorySample{
		UserID:       userID,
		Provider:     "apple_health",
		CategoryType: canonicalType,
		ValueInt:     valueInt,
		ValueLabel:   valueLabel,
		StartDate:    startDate,
		EndDate:      endDate,
		SourceName:   stringField(record, "sourceName"),
		ExternalID:   sampleExternalID(record),
		Metadata:     metadata,
	}

	sample.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      "apple_health",
		"category_type": canonicalType,
		"start_date":    startDate.UTC().Format(time.RFC3339Nano),
		"value_int":     fmt.Sprintf("%d", valueInt),
	})

	return &model.MapResult{
		CategorySamples: []model.CategorySample{sample},
	}, nil
}
