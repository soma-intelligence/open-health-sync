// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sourceName labels the samples of this connector.
const sourceName = "intervals.icu"

// mapWellness turns one daily wellness record into quantity samples, one per
// figure the record holds, spanning the record's day. intervals.icu gives the
// day as a local date without a zone, so the span is that date in UTC.
//
// A null, missing, zero or negative figure gives no sample: intervals.icu
// writes null for what no device or entry provided. The sleep duration and
// quality, the average sleeping heart rate and the food energy have no
// canonical quantity: they stay in the raw record and in the samples'
// metadata.
func mapWellness(line []byte, userID string) (*model.MapResult, error) {
	var w Wellness
	if err := decode(DataTypeWellness, line, &w); err != nil {
		return nil, err
	}
	if w.ID == "" {
		return nil, fmt.Errorf("intervals_icu: wellness: missing id")
	}
	day, err := time.Parse(dateLayout, w.ID)
	if err != nil {
		return nil, fmt.Errorf("intervals_icu: wellness: id %q is not a date", w.ID)
	}
	start := day
	end := day.Add(24*time.Hour - time.Second)

	md := map[string]any{"day": w.ID}
	putFloat(md, "sleep_secs", w.SleepSecs)
	putFloat(md, "sleep_quality", w.SleepQuality)
	putFloat(md, "avg_sleeping_hr", w.AvgSleepingHR)
	putFloat(md, "kcal_consumed", w.KcalConsumed)

	var samples []model.QuantitySample
	for _, m := range []struct {
		quantityType, unit string
		value              *float64
	}{
		{"body_mass", "kg", w.Weight},
		{model.QTRestingHeartRate, "bpm", w.RestingHR},
		{model.QTHRVRMSSD, "ms", w.HRV},
		{model.QTHRVSDNN, "ms", w.HRVSDNN},
		{model.QTSleepScore, "score", w.SleepScore},
		{model.QTStepCount, "count", w.Steps},
		{model.QTOxygenSaturation, percentUnit(w.SpO2), w.SpO2},
		{model.QTRespiratoryRate, "bpm", w.Respiration},
		{"body_fat_percentage", percentUnit(w.BodyFat), w.BodyFat},
		{"vo2_max", "mL/kg/min", w.VO2Max},
		{"blood_pressure_systolic", "mmHg", w.Systolic},
		{"blood_pressure_diastolic", "mmHg", w.Diastolic},
	} {
		if !positive(m.value) {
			continue
		}
		samples = append(samples, dailySample(userID, w.ID, m.quantityType, m.unit, *m.value, start, end, copyMetadata(md)))
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: samples}, nil
}

// percentUnit is "percent" for a value on the 0-100 scale intervals.icu
// documents, and "fraction" for one at or below 1, which an upstream may have
// written as a 0-1 fraction; the unit table turns a fraction into percent.
func percentUnit(v *float64) string {
	if v != nil && *v > 0 && *v <= 1 {
		return "fraction"
	}
	return "percent"
}

// dailySample builds the sample of one figure of one day. It is Replaceable
// and hashed without its value: a figure intervals.icu revises later in the
// day (more steps, a weight entered again) replaces the stored one.
func dailySample(userID, day, qt, unit string, v float64, start, end time.Time, md map[string]any) model.QuantitySample {
	s := model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceName:      sourceName,
		ExternalID:      fmt.Sprintf("%s_%s", day, qt),
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       start,
		EndDate:         end,
		Metadata:        md,
		Replaceable:     true,
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"quantity_type": qt,
		"date":          day,
	})
	return s
}

// copyMetadata returns a shallow copy of md, so samples of one record do not
// share a map.
func copyMetadata(md map[string]any) map[string]any {
	out := make(map[string]any, len(md))
	for k, v := range md {
		out[k] = v
	}
	return out
}
