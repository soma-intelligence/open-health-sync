// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// dayWindow resolves the span of a daily document: its "day" from local
// midnight to one second before the next, in the zone of its "timestamp"
// offset (UTC when the document has none or the offset has no IANA name). The
// zone goes into md.
func dayWindow(dataType, day, timestamp string, md map[string]any) (time.Time, time.Time, error) {
	if day == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("oura: %s: missing day", dataType)
	}
	loc := time.UTC
	if timestamp != "" {
		ts, err := parseTime(timestamp)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("oura: %s: %w", dataType, err)
		}
		loc = zoneMetadata(md, ts)
	}
	start, end, err := localDay(day, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("oura: %s: %w", dataType, err)
	}
	return start.UTC(), end.UTC(), nil
}

// mapDailyActivity turns one daily_activity document into step_count,
// active_energy_burned, distance_walking_running, moderate_activity_time and
// vigorous_activity_time samples for its day. Oura's distance is the
// equivalent walking distance of the day's movement; its medium and high
// activity time (seconds) are the moderate and vigorous minutes. The activity
// score and the time spent at each intensity stay in metadata too.
func mapDailyActivity(line []byte, userID string) (*model.MapResult, error) {
	var a DailyActivity
	if err := decode(DataTypeDailyActivity, line, &a); err != nil {
		return nil, err
	}
	md := map[string]any{"oura_id": a.ID}
	start, end, err := dayWindow(DataTypeDailyActivity, a.Day, a.Timestamp, md)
	if err != nil {
		return nil, err
	}
	for k, v := range map[string]*float64{
		"activity_score":       a.Score,
		"total_calories":       a.TotalCalories,
		"high_activity_time":   a.HighActivityTime,
		"medium_activity_time": a.MediumActivityTime,
		"low_activity_time":    a.LowActivityTime,
		"sedentary_time":       a.SedentaryTime,
		"non_wear_time":        a.NonWearTime,
	} {
		if v != nil {
			md[k] = *v
		}
	}

	var samples []model.QuantitySample
	for _, m := range []struct {
		quantityType, unit string
		value              *float64
	}{
		{quantityType: model.QTStepCount, unit: "count", value: a.Steps},
		{quantityType: model.QTActiveEnergyBurned, unit: "kcal", value: a.ActiveCalories},
		{quantityType: model.QTDistanceWalkingRunning, unit: "m", value: a.EquivalentWalkingDistance},
		{quantityType: model.QTModerateActivityTime, unit: "s", value: a.MediumActivityTime},
		{quantityType: model.QTVigorousActivityTime, unit: "s", value: a.HighActivityTime},
	} {
		if m.value == nil {
			continue
		}
		samples = append(samples, dailySample(userID, a.Day, m.quantityType, m.unit, *m.value, start, end, copyMetadata(md)))
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: samples}, nil
}

// mapDailyReadiness turns one daily_readiness document into readiness_score
// (Oura's 0-100 index), readiness_hrv_balance (the HRV balance contributor,
// a 0-100 index, never milliseconds) and body_temperature_deviation (degrees
// Celsius from the user's baseline). The other contributors and the
// temperature trend stay in metadata.
func mapDailyReadiness(line []byte, userID string) (*model.MapResult, error) {
	var r DailyReadiness
	if err := decode(DataTypeDailyReadiness, line, &r); err != nil {
		return nil, err
	}
	md := map[string]any{"oura_id": r.ID}
	start, end, err := dayWindow(DataTypeDailyReadiness, r.Day, r.Timestamp, md)
	if err != nil {
		return nil, err
	}
	if r.TemperatureTrendDeviation != nil {
		md["temperature_trend_deviation"] = *r.TemperatureTrendDeviation
	}
	if c := contributorMetadata(r.Contributors); c != nil {
		md["contributors"] = c
	}
	// A contributor Oura could not compute is null: no sample then.
	hrvBalance := r.Contributors["hrv_balance"]
	var samples []model.QuantitySample
	for _, m := range []struct {
		quantityType, unit string
		value              *float64
	}{
		{quantityType: model.QTReadinessScore, unit: "score", value: r.Score},
		{quantityType: model.QTReadinessHRVBalance, unit: "index", value: hrvBalance},
		{quantityType: model.QTBodyTemperatureDeviation, unit: "celsius", value: r.TemperatureDeviation},
	} {
		if m.value == nil {
			continue
		}
		samples = append(samples, dailySample(userID, r.Day, m.quantityType, m.unit, *m.value, start, end, copyMetadata(md)))
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: samples}, nil
}

// mapDailySpO2 turns one daily_spo2 document into an oxygen_saturation
// sample: the night's average, in percent. The document carries no
// timestamp, so its day is a UTC day. The breathing disturbance index stays
// in metadata.
func mapDailySpO2(line []byte, userID string) (*model.MapResult, error) {
	var s DailySpO2
	if err := decode(DataTypeDailySpO2, line, &s); err != nil {
		return nil, err
	}
	md := map[string]any{"oura_id": s.ID}
	start, end, err := dayWindow(DataTypeDailySpO2, s.Day, "", md)
	if err != nil {
		return nil, err
	}
	if s.SpO2Percentage == nil || s.SpO2Percentage.Average == nil {
		return nil, nil
	}
	if s.BreathingDisturbanceIndex != nil {
		md["breathing_disturbance_index"] = *s.BreathingDisturbanceIndex
	}
	return &model.MapResult{QuantitySamples: []model.QuantitySample{
		dailySample(userID, s.Day, model.QTOxygenSaturation, "percent", *s.SpO2Percentage.Average, start, end, md),
	}}, nil
}
