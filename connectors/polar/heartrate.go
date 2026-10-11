// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapContinuousHR turns one day of continuous heart rate into heart_rate
// samples, one per AccessLink sample (Polar records one every 5 minutes).
//
// Polar gives the sample times as local wall clock on the line's date,
// without an offset. When Fetch added "utc_offset_minutes" (learned from a
// sleep or an exercise of the same pull) the times are converted with it and
// the matching zone is set; otherwise they are read as UTC and the metadata
// says so ("local_time_as_utc"). A zero heart rate is a missing reading and
// is skipped. A sample Polar sends again replaces the stored one: the
// samples are Replaceable and hashed on their time, not the value.
func mapContinuousHR(line []byte, userID string) (*model.MapResult, error) {
	var d ContinuousHRDay
	if err := decode(DataTypeContinuousHR, line, &d); err != nil {
		return nil, err
	}
	day, err := parseDate(d.Date)
	if err != nil {
		return nil, fmt.Errorf("polar: continuous heart rate: %w", err)
	}
	base := map[string]any{"source": "continuous_heart_rate"}
	offset := 0
	if d.UTCOffsetMinutes != nil {
		offset = *d.UTCOffsetMinutes
		base = withOffset(base, offset)
	} else {
		base["local_time_as_utc"] = true
	}

	var samples []model.QuantitySample
	for _, p := range d.HeartRateSamples {
		if p.HeartRate == nil || *p.HeartRate <= 0 {
			continue
		}
		local, err := clockOnDay(day, p.SampleTime)
		if err != nil {
			return nil, fmt.Errorf("polar: continuous heart rate: %w", err)
		}
		at := local.Add(-time.Duration(offset) * time.Minute).UTC()
		s := sample(userID, "", "continuous_hr_"+d.Date, model.QTHeartRate, "bpm", *p.HeartRate, at, at, copyMap(base))
		s.Replaceable = true
		s.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id":       userID,
			"provider":      Slug,
			"data_type":     DataTypeContinuousHR,
			"quantity_type": model.QTHeartRate,
			"start_date":    at.Format(time.RFC3339),
		})
		samples = append(samples, s)
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: samples}, nil
}
