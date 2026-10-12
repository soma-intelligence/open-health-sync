// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapDailySleep turns one daily_sleep document into a sleep_score sample for
// its day: Oura's 0-100 daily sleep index, a Replaceable daily figure like
// readiness_score. The contributors (deep sleep, efficiency, latency, REM,
// restfulness, timing, total) are 0-100 indices and stay in metadata. It is
// not written into a session's sleep_score: the day's score may cover more
// than one period.
func mapDailySleep(line []byte, userID string) (*model.MapResult, error) {
	var d DailySleep
	if err := decode(DataTypeDailySleep, line, &d); err != nil {
		return nil, err
	}
	md := map[string]any{"oura_id": d.ID}
	start, end, err := dayWindow(DataTypeDailySleep, d.Day, d.Timestamp, md)
	if err != nil {
		return nil, err
	}
	if d.Score == nil {
		return nil, nil
	}
	if c := contributorMetadata(d.Contributors); c != nil {
		md["contributors"] = c
	}
	return &model.MapResult{QuantitySamples: []model.QuantitySample{
		dailySample(userID, d.Day, model.QTSleepScore, "score", *d.Score, start, end, md),
	}}, nil
}

// mapDailyStress turns one daily_stress document into stress_high_time and
// recovery_high_time samples for its day: the time Oura classed as high
// stress and as high recovery, sent in seconds and stored in minutes. Oura's
// summary of the day ("restored", "normal", "stressful") goes into the
// metadata of both. The document carries no timestamp, so its day is a UTC
// day, as for daily_spo2.
func mapDailyStress(line []byte, userID string) (*model.MapResult, error) {
	var d DailyStress
	if err := decode(DataTypeDailyStress, line, &d); err != nil {
		return nil, err
	}
	md := map[string]any{"oura_id": d.ID}
	start, end, err := dayWindow(DataTypeDailyStress, d.Day, "", md)
	if err != nil {
		return nil, err
	}
	if d.DaySummary != nil && *d.DaySummary != "" {
		md["day_summary"] = *d.DaySummary
	}
	var samples []model.QuantitySample
	for _, m := range []struct {
		quantityType string
		value        *float64
	}{
		{quantityType: model.QTStressHighTime, value: d.StressHigh},
		{quantityType: model.QTRecoveryHighTime, value: d.RecoveryHigh},
	} {
		if m.value == nil {
			continue
		}
		samples = append(samples, dailySample(userID, d.Day, m.quantityType, "s", *m.value, start, end, copyMetadata(md)))
	}
	if len(samples) == 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: samples}, nil
}

// mindfulSessionTypes are the Oura session types stored as a mindful
// session. A nap is sleep (Oura reports it as a sleep period too), and
// "rest" and "body_status" are checks of the body, not a practice: they stay
// in the raw store only.
var mindfulSessionTypes = map[string]bool{
	"meditation": true,
	"breathing":  true,
	"relaxation": true,
}

// mapSession turns one session document of a mindful type into a
// mindful_session category sample, the record Apple Health's mindful minutes
// land in, so the two providers read alike. The session type is the value
// label and the mood (when the user gave one) is in metadata; the heart rate
// and HRV series the session carries stay in the raw line.
//
// The sample is identified by the Oura id and its start, and carries the Oura
// id as ExternalID, so a delete webhook for the session removes it.
func mapSession(line []byte, userID string) (*model.MapResult, error) {
	var s Session
	if err := decode(DataTypeSession, line, &s); err != nil {
		return nil, err
	}
	if s.ID == "" {
		return nil, fmt.Errorf("oura: session: missing id")
	}
	if !mindfulSessionTypes[s.Type] {
		return nil, nil
	}
	startLocal, err := parseTime(s.StartDatetime)
	if err != nil {
		return nil, fmt.Errorf("oura: session: start_datetime: %w", err)
	}
	endLocal, err := parseTime(s.EndDatetime)
	if err != nil {
		return nil, fmt.Errorf("oura: session: end_datetime: %w", err)
	}
	start, end := startLocal.UTC(), endLocal.UTC()
	if end.Before(start) {
		return nil, fmt.Errorf("oura: session: end_datetime before start_datetime")
	}
	md := map[string]any{"oura_type": s.Type, "day": s.Day}
	if s.Mood != nil && *s.Mood != "" {
		md["mood"] = *s.Mood
	}
	zoneMetadata(md, startLocal)
	cs := model.CategorySample{
		UserID:       userID,
		Provider:     Slug,
		ExternalID:   s.ID,
		CategoryType: categoryMindfulSession,
		ValueInt:     0,
		ValueLabel:   s.Type,
		StartDate:    start,
		EndDate:      end,
		SourceName:   sourceName,
		Metadata:     md,
	}
	cs.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"category_type": categoryMindfulSession,
		"external_id":   s.ID,
		"start":         start.Format(time.RFC3339),
	})
	return &model.MapResult{CategorySamples: []model.CategorySample{cs}}, nil
}

// categoryMindfulSession is the category type of a meditation or breathing
// session, shared with the Apple Health connector.
const categoryMindfulSession = "mindful_session"

// mapVO2Max turns one vo2_max document into a vo2_max sample (mL/kg/min) for
// its day: Oura's estimate, Replaceable like every daily figure.
func mapVO2Max(line []byte, userID string) (*model.MapResult, error) {
	var v VO2Max
	if err := decode(DataTypeVO2Max, line, &v); err != nil {
		return nil, err
	}
	md := map[string]any{"oura_id": v.ID}
	start, end, err := dayWindow(DataTypeVO2Max, v.Day, v.Timestamp, md)
	if err != nil {
		return nil, err
	}
	if v.VO2Max == nil || *v.VO2Max <= 0 {
		return nil, nil
	}
	return &model.MapResult{QuantitySamples: []model.QuantitySample{
		dailySample(userID, v.Day, "vo2_max", "mL/kg/min", *v.VO2Max, start, end, md),
	}}, nil
}
