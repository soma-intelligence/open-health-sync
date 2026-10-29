// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// categoryObjective marks a measure group that is a user objective (a target
// weight), not a reading.
const categoryObjective = 2

// maxMeasureUnit bounds the power of ten of a measure. Withings writes a few
// digits (-3 for a weight in grams); past this a value is no reading, and
// Float64 would loop once per power or scale to +Inf.
const maxMeasureUnit = 18

// mapMeasure turns one getmeas group into one QuantitySample per known
// meastype. Unknown meastypes are skipped with a warning.
func mapMeasure(line []byte, userID string) (*model.MapResult, error) {
	var head struct {
		Date *int64 `json:"date"`
	}
	if err := decode(DataTypeMeasure, line, &head); err != nil {
		return nil, err
	}
	if head.Date == nil {
		return nil, fmt.Errorf("withings: measure: missing date")
	}
	var g MeasureGroup
	if err := decode(DataTypeMeasure, line, &g); err != nil {
		return nil, err
	}
	if g.Category == categoryObjective || len(g.Measures) == 0 {
		return nil, nil
	}
	ts := ParseWithingsTimestamp(g.Date)

	var samples []model.QuantitySample
	for _, m := range g.Measures {
		info, known := withingsMeasTypes[m.Type]
		if !known {
			slog.Warn("withings: unknown meastype skipped", "provider", Slug, "meastype", m.Type)
			continue
		}
		if m.Unit < -maxMeasureUnit || m.Unit > maxMeasureUnit {
			slog.Warn("withings: measure with an out of range power of ten skipped", "provider", Slug, "meastype", m.Type, "unit", m.Unit)
			continue
		}
		value := m.Float64()
		md := withTimezone(map[string]any{
			"meastype":          m.Type,
			"withings_attrib":   g.Attrib,
			"withings_category": g.Category,
		}, g.Timezone)
		s := model.QuantitySample{
			UserID:     userID,
			Provider:   Slug,
			SourceSlug: Slug,
			SourceName: sourceName,
			DeviceID:   g.DeviceID,
			// The meastype keeps 12 and 71 (both body_temperature) apart.
			ExternalID:      fmt.Sprintf("%d_%s_%d", g.GrpID, info.QuantityType, m.Type),
			QuantityType:    info.QuantityType,
			Value:           value,
			Unit:            info.Unit,
			IsPrimarySource: true,
			StartDate:       ts,
			EndDate:         ts, // point-in-time measurement
			Metadata:        md,
		}
		s.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id":       userID,
			"provider":      Slug,
			"quantity_type": info.QuantityType,
			"start_date":    ts.Format(time.RFC3339),
			"value":         model.CanonicalFloat(value),
		})
		samples = append(samples, s)
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: samples}, nil
}
