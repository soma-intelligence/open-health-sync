// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Point measurements and nightly or daily figures: body composition, blood
// pressure, HRV, pulse ox, respiration, user metrics, skin temperature.

// mapBodyComp turns one body composition measurement into body_mass,
// body_fat_percentage, muscle_mass, bone_mass and body_mass_index samples.
// Garmin gives masses in grams; they are stored in kilograms. Body water
// arrives as a percent while water_mass is stored in kilograms, so it stays
// in metadata.
func mapBodyComp(line []byte, userID string) (*model.MapResult, error) {
	var b BodyComp
	if err := decode(DataTypeBodyComps, line, &b); err != nil {
		return nil, err
	}
	at, err := unixTime("measurementTimeInSeconds", b.MeasurementTimeInSeconds)
	if err != nil {
		return nil, fmt.Errorf("garmin: bodyComps: %w", err)
	}
	md := dayMeta(b.SummaryID, b.MeasurementTimeOffsetInSeconds)
	setIf(md, "body_water_percent", b.BodyWaterInPercent)
	kg := func(g *float64, max float64) *float64 {
		if !valid(g) || *g == 0 || *g > max {
			return nil
		}
		v := *g / 1000
		return &v
	}
	var pct, bmi *float64
	if inBounds(b.BodyFatInPercent, 0, 100) {
		pct = b.BodyFatInPercent
	}
	if inBounds(b.BodyMassIndex, 5, 200) {
		bmi = b.BodyMassIndex
	}
	return points(userID, DataTypeBodyComps, b.SummaryID, at, md, []point{
		{"body_mass", "kg", kg(b.WeightInGrams, 700000)},
		{"body_fat_percentage", "percent", pct},
		{"muscle_mass", "kg", kg(b.MuscleMassInGrams, 500000)},
		{"bone_mass", "kg", kg(b.BoneMassInGrams, 100000)},
		{"body_mass_index", "kg/m2", bmi},
	}), nil
}

// mapBloodPressure turns one reading into blood_pressure_systolic,
// blood_pressure_diastolic (mmHg) and heart_rate (bpm) samples.
func mapBloodPressure(line []byte, userID string) (*model.MapResult, error) {
	var b BloodPressure
	if err := decode(DataTypeBloodPressures, line, &b); err != nil {
		return nil, err
	}
	at, err := unixTime("measurementTimeInSeconds", b.MeasurementTimeInSeconds)
	if err != nil {
		return nil, fmt.Errorf("garmin: bloodPressures: %w", err)
	}
	md := dayMeta(b.SummaryID, b.MeasurementTimeOffsetInSeconds)
	if b.SourceType != "" {
		md["source_type"] = b.SourceType
	}
	pick := func(v *float64, lo, hi float64) *float64 {
		if inBounds(v, lo, hi) {
			return v
		}
		return nil
	}
	return points(userID, DataTypeBloodPressures, b.SummaryID, at, md, []point{
		{"blood_pressure_systolic", "mmHg", pick(b.Systolic, 30, 300)},
		{"blood_pressure_diastolic", "mmHg", pick(b.Diastolic, 10, 250)},
		{model.QTHeartRate, "bpm", pick(b.Pulse, 20, 250)},
	}), nil
}

// point is one figure of a point measurement.
type point struct {
	qt, unit string
	v        *float64
}

// points builds the samples of one measurement at one instant, hashed on
// their type and time (first write wins).
func points(userID, dataType, summaryID string, at time.Time, md map[string]any, ps []point) *model.MapResult {
	var out []model.QuantitySample
	for _, p := range ps {
		if p.v == nil {
			continue
		}
		ext := summaryID
		if ext != "" {
			ext += "_" + p.qt
		}
		s := sample(userID, ext, p.qt, p.unit, *p.v, at, at, copyMap(md))
		s.ContentHash = hash(userID, dataType, "quantity_type", p.qt, "start_date", at.Format(time.RFC3339))
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return &model.MapResult{QuantitySamples: out}
}

// summaryDate returns a summary's calendar date, else the local date of
// start.
func summaryDate(calendarDate string, start time.Time, off *int64) (string, error) {
	if calendarDate != "" {
		if _, err := parseDate(calendarDate); err != nil {
			return "", err
		}
		return calendarDate, nil
	}
	if start.IsZero() {
		return "", fmt.Errorf("missing calendarDate and startTimeInSeconds")
	}
	return localDate(start, off), nil
}

// nightly builds one Replaceable figure of a night or a day, hashed on the
// date: Garmin may re-send it with a new value.
func nightly(userID, dataType, qt, unit, date string, v float64, start, end time.Time, md map[string]any) *model.MapResult {
	s := sample(userID, dataType+"_"+date+"_"+qt, qt, unit, v, start, end, md)
	s.Replaceable = true
	s.ContentHash = hash(userID, dataType, "quantity_type", qt, "date", date)
	return &model.MapResult{QuantitySamples: []model.QuantitySample{s}}
}

// optionalStart reads a start time that may be absent (zero).
func optionalStart(secs *int64) (time.Time, error) {
	if secs == nil {
		return time.Time{}, nil
	}
	return unixTime("startTimeInSeconds", secs)
}

// dateSpan is the span of a summary: start to start + duration, else the
// calendar date from midnight UTC to one second before the next.
func dateSpan(date string, start time.Time, dur *int64) (time.Time, time.Time) {
	if !start.IsZero() {
		return start, start.Add(time.Duration(seconds(dur, 0)) * time.Second)
	}
	d, _ := parseDate(date)
	return d, d.AddDate(0, 0, 1).Add(-time.Second)
}

// mapHRV turns one nightly HRV summary into an hrv_rmssd sample: Garmin's
// lastNightAvg is the night's average RMSSD in milliseconds (not SDNN). The
// five minute values (hrvValues) stay raw.
func mapHRV(line []byte, userID string) (*model.MapResult, error) {
	var h HRV
	if err := decode(DataTypeHRV, line, &h); err != nil {
		return nil, err
	}
	start, err := optionalStart(h.StartTimeInSeconds)
	if err != nil {
		return nil, fmt.Errorf("garmin: hrv: %w", err)
	}
	date, err := summaryDate(h.CalendarDate, start, h.StartTimeOffsetInSeconds)
	if err != nil {
		return nil, fmt.Errorf("garmin: hrv: %w", err)
	}
	if !inBounds(h.LastNightAvg, 1, 500) {
		return nil, nil
	}
	md := dayMeta(h.SummaryID, h.StartTimeOffsetInSeconds)
	md["date"] = date
	setIf(md, "last_night_5min_high", h.LastNight5MinHigh)
	s, e := dateSpan(date, start, h.DurationInSeconds)
	return nightly(userID, DataTypeHRV, model.QTHRVRMSSD, "ms", date, *h.LastNightAvg, s, e, md), nil
}

// mapPulseOx turns the SpO2 readings of one pulse ox summary into
// oxygen_saturation samples (percent).
func mapPulseOx(line []byte, userID string) (*model.MapResult, error) {
	var p PulseOx
	if err := decode(DataTypePulseOx, line, &p); err != nil {
		return nil, err
	}
	start, err := unixTime("startTimeInSeconds", p.StartTimeInSeconds)
	if err != nil {
		return nil, fmt.Errorf("garmin: pulseox: %w", err)
	}
	md := withOffset(map[string]any{}, p.StartTimeOffsetInSeconds)
	setIf(md, "on_demand", p.OnDemand)
	ext := p.SummaryID
	if ext == "" {
		ext = "pulseox_" + localDate(start, p.StartTimeOffsetInSeconds)
	}
	out := seriesSamples(userID, DataTypePulseOx, ext, model.QTOxygenSaturation, "percent", start, p.TimeOffsetSpo2Values, 50, 100, md)
	if len(out) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: out}, nil
}

// mapRespiration turns one all-day respiration summary into
// respiratory_rate samples (breaths per minute, stored unit "bpm").
func mapRespiration(line []byte, userID string) (*model.MapResult, error) {
	var r Respiration
	if err := decode(DataTypeRespiration, line, &r); err != nil {
		return nil, err
	}
	start, err := unixTime("startTimeInSeconds", r.StartTimeInSeconds)
	if err != nil {
		return nil, fmt.Errorf("garmin: respiration: %w", err)
	}
	md := withOffset(map[string]any{}, r.StartTimeOffsetInSeconds)
	ext := r.SummaryID
	if ext == "" {
		ext = "respiration_" + localDate(start, r.StartTimeOffsetInSeconds)
	}
	out := seriesSamples(userID, DataTypeRespiration, ext, model.QTRespiratoryRate, "bpm", start, r.TimeOffsetEpochToBreaths, 1, 80, md)
	if len(out) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: out}, nil
}

// mapUserMetrics turns one day's user metrics into a vo2_max sample
// (mL/kg/min) spanning the calendar date. Fitness age stays in metadata.
func mapUserMetrics(line []byte, userID string) (*model.MapResult, error) {
	var u UserMetrics
	if err := decode(DataTypeUserMetrics, line, &u); err != nil {
		return nil, err
	}
	if _, err := parseDate(u.CalendarDate); err != nil {
		return nil, fmt.Errorf("garmin: userMetrics: %w", err)
	}
	if !inBounds(u.VO2Max, 5, 100) {
		return nil, nil
	}
	md := map[string]any{"date": u.CalendarDate}
	if u.SummaryID != "" {
		md["summary_id"] = u.SummaryID
	}
	setIf(md, "fitness_age", u.FitnessAge)
	setIf(md, "vo2_max_cycling", u.VO2MaxCycle)
	setIf(md, "enhanced", u.Enhanced)
	s, e := dateSpan(u.CalendarDate, time.Time{}, nil)
	return nightly(userID, DataTypeUserMetrics, "vo2_max", "mL/kg/min", u.CalendarDate, *u.VO2Max, s, e, md), nil
}

// mapSkinTemp turns one nightly skin temperature summary into a
// body_temperature_deviation sample: Garmin gives the deviation from the
// user's baseline in degrees Celsius.
func mapSkinTemp(line []byte, userID string) (*model.MapResult, error) {
	var t SkinTemp
	if err := decode(DataTypeSkinTemp, line, &t); err != nil {
		return nil, err
	}
	start, err := optionalStart(t.StartTimeInSeconds)
	if err != nil {
		return nil, fmt.Errorf("garmin: skinTemp: %w", err)
	}
	date, err := summaryDate(t.CalendarDate, start, t.StartTimeOffsetInSeconds)
	if err != nil {
		return nil, fmt.Errorf("garmin: skinTemp: %w", err)
	}
	v := t.AvgDeviationCelsius
	if v == nil || *v != *v || *v < -10 || *v > 10 {
		return nil, nil
	}
	md := dayMeta(t.SummaryID, t.StartTimeOffsetInSeconds)
	md["date"] = date
	s, e := dateSpan(date, start, t.DurationInSeconds)
	return nightly(userID, DataTypeSkinTemp, model.QTBodyTemperatureDeviation, "celsius", date, *v, s, e, md), nil
}
