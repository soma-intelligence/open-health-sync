// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapActivity turns one getactivity day into step_count, distance, active
// energy, moderate_activity_time and vigorous_activity_time samples. The day is the user's local day: it runs from local midnight
// (in the zone Withings gave, else UTC) to one second before the next.
//
// A day's total is re-sent with a larger value while the day is open, so the
// samples are Replaceable and their hash leaves the value out: the newest
// total replaces the stored one.
func mapActivity(line []byte, userID string) (*model.MapResult, error) {
	var a Activity
	if err := decode(DataTypeActivity, line, &a); err != nil {
		return nil, err
	}
	loc := zone(a.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	date, err := time.ParseInLocation("2006-01-02", a.Date, loc)
	if err != nil {
		return nil, fmt.Errorf("withings: activity date %q: %w", a.Date, err)
	}
	start := date
	end := date.AddDate(0, 0, 1).Add(-time.Second)

	md := func() map[string]any {
		m := map[string]any{}
		for k, v := range map[string]*float64{
			"total_calories":    a.TotalCalories,
			"intense_duration":  a.Intense,
			"moderate_duration": a.Moderate,
			"soft_duration":     a.Soft,
			"elevation":         a.Elevation,
		} {
			if v != nil {
				m[k] = *v
			}
		}
		return withTimezone(m, a.Timezone)
	}

	// Two trackers report their own day total: the device keeps them apart.
	externalID := func(qt string) string {
		if a.DeviceID != "" {
			return fmt.Sprintf("%s_%s_%s", a.Date, qt, a.DeviceID)
		}
		return fmt.Sprintf("%s_%s", a.Date, qt)
	}

	var samples []model.QuantitySample
	add := func(qt, unit string, v *float64) {
		if v == nil {
			return
		}
		s := model.QuantitySample{
			UserID:          userID,
			Provider:        Slug,
			SourceSlug:      Slug,
			SourceName:      sourceName,
			DeviceID:        a.DeviceID,
			ExternalID:      externalID(qt),
			QuantityType:    qt,
			Value:           *v,
			Unit:            unit,
			IsPrimarySource: true,
			StartDate:       start,
			EndDate:         end,
			Metadata:        md(),
			Replaceable:     true,
		}
		s.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id":       userID,
			"provider":      Slug,
			"quantity_type": qt,
			"date":          a.Date,
			"device_id":     a.DeviceID,
		})
		samples = append(samples, s)
	}
	for _, m := range []struct {
		quantityType, unit string
		value              *float64
	}{
		{quantityType: model.QTStepCount, unit: "count", value: a.Steps},
		{quantityType: model.QTDistanceWalkingRunning, unit: "m", value: a.Distance},
		{quantityType: model.QTActiveEnergyBurned, unit: "kcal", value: a.Calories},
		// Durations in seconds: the day's moderate and intense activity.
		{quantityType: model.QTModerateActivityTime, unit: "s", value: a.Moderate},
		{quantityType: model.QTVigorousActivityTime, unit: "s", value: a.Intense},
	} {
		add(m.quantityType, m.unit, m.value)
	}

	if len(samples) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: samples}, nil
}
