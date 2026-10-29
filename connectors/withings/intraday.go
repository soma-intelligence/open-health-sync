// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// intradayMetric is one metric of an intraday interval and the sample it
// becomes.
type intradayMetric struct {
	quantityType string
	unit         string
	value        func(p IntradayPoint) *float64
}

// intradayMetrics fans one interval out into one sample per present metric.
//
// The summed activity metrics go under the *_intraday keys: getactivity ships
// a daily total for the same day, and both under the canonical key would
// double count the day. heart_rate stays canonical: it averages, and these are
// real readings.
var intradayMetrics = []intradayMetric{
	{quantityType: model.QTStepCountIntraday, unit: "count", value: func(p IntradayPoint) *float64 { return p.Steps }},
	{quantityType: model.QTActiveEnergyIntraday, unit: "kcal", value: func(p IntradayPoint) *float64 { return p.Calories }},
	{quantityType: model.QTDistanceWalkRunIntday, unit: "m", value: func(p IntradayPoint) *float64 { return p.Distance }},
	{quantityType: model.QTHeartRate, unit: "bpm", value: func(p IntradayPoint) *float64 { return p.HeartRate }},
}

// maxIntradayDuration bounds an interval's duration in seconds. Past a day
// it is no intraday interval, and a large one overflows time.Duration into
// a wrong, plausible looking end.
const maxIntradayDuration = 24 * 60 * 60

// mapIntraday maps one intraday interval. A zero heart rate is a missing
// reading and is skipped; zero steps, calories or distance are real zeros.
// Withings gives no time zone on this endpoint. An interval Withings sends
// again (a later pull of the same day) replaces the stored value: the samples
// are Replaceable and hashed on the interval start, not the value.
func mapIntraday(line []byte, userID string) (*model.MapResult, error) {
	var p IntradayPoint
	if err := decode(DataTypeIntraday, line, &p); err != nil {
		return nil, err
	}
	if p.Timestamp == 0 {
		return nil, fmt.Errorf("withings: intraday: missing timestamp")
	}
	start := ParseWithingsTimestamp(p.Timestamp)
	end := start
	if p.Duration != nil && *p.Duration > 0 && *p.Duration <= maxIntradayDuration {
		end = start.Add(time.Duration(*p.Duration) * time.Second)
	}

	var samples []model.QuantitySample
	for _, m := range intradayMetrics {
		v := m.value(p)
		if v == nil {
			continue
		}
		if m.quantityType == model.QTHeartRate && *v == 0 {
			continue
		}
		s := model.QuantitySample{
			UserID:          userID,
			Provider:        Slug,
			SourceSlug:      Slug,
			SourceName:      sourceName,
			DeviceID:        p.DeviceID,
			ExternalID:      fmt.Sprintf("withings_intraday_%d", p.Timestamp),
			QuantityType:    m.quantityType,
			Value:           *v,
			Unit:            m.unit,
			IsPrimarySource: true,
			StartDate:       start,
			EndDate:         end,
			Metadata:        map[string]any{"source": "intraday_activity"},
			Replaceable:     true,
		}
		s.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id":       userID,
			"provider":      Slug,
			"quantity_type": m.quantityType,
			"start_date":    start.Format(time.RFC3339),
		})
		samples = append(samples, s)
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: samples}, nil
}
