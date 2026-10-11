// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Oura sleep period types.
const (
	sleepTypeDeleted   = "deleted"    // removed by the user or by Oura; never stored
	sleepTypeLongSleep = "long_sleep" // the main sleep of a day
)

// hypnogramStep is the resolution of sleep_phase_5_min: one character per
// five minutes from bedtime_start.
const hypnogramStep = 5 * time.Minute

// hypnogramStages maps the characters of sleep_phase_5_min to the canonical
// stages. A character outside this set is dropped (see
// noteDroppedHypnogramSteps).
var hypnogramStages = map[byte]string{
	'1': model.SleepStageDeep,
	'2': model.SleepStageLight,
	'3': model.SleepStageREM,
	'4': model.SleepStageAwake,
}

// mapSleep turns one sleep period into a SleepSession, its stages from the 5
// minute hypnogram (consecutive steps of one stage merged into one phase), and
// for the main sleep of a day (type long_sleep) resting_heart_rate (the
// lowest heart rate of the night, Oura's definition of resting heart rate) and
// respiratory_rate samples.
//
// Identity. The session is hashed on the Oura id and its start, so a re-sent
// period is a duplicate however its values moved. The writer keeps the first
// version of a session (it does not replace sleep rows); the two samples are
// Replaceable, hashed on the Oura id, so they follow a corrected night. A
// period of type "deleted" gives nothing.
//
// HRV. Oura's average_hrv is an RMSSD in milliseconds. It fills the session's
// HRV column, labelled in metadata ("hrv_method": "rmssd"), and is never
// emitted as hrv_sdnn.
func mapSleep(line []byte, userID string) (*model.MapResult, error) {
	var s Sleep
	if err := decode(DataTypeSleep, line, &s); err != nil {
		return nil, err
	}
	if s.ID == "" {
		return nil, fmt.Errorf("oura: sleep: missing id")
	}
	if s.Type == sleepTypeDeleted {
		return nil, nil
	}
	startLocal, err := parseTime(s.BedtimeStart)
	if err != nil {
		return nil, fmt.Errorf("oura: sleep: bedtime_start: %w", err)
	}
	endLocal, err := parseTime(s.BedtimeEnd)
	if err != nil {
		return nil, fmt.Errorf("oura: sleep: bedtime_end: %w", err)
	}
	start, end := startLocal.UTC(), endLocal.UTC()
	if end.Before(start) {
		return nil, fmt.Errorf("oura: sleep: bedtime_end before bedtime_start")
	}

	md := map[string]any{"oura_type": s.Type, "day": s.Day}
	zoneMetadata(md, startLocal)
	for k, v := range map[string]*float64{
		"efficiency":         s.Efficiency,
		"average_heart_rate": s.AverageHeartRate,
	} {
		if v != nil {
			md[k] = *v
		}
	}
	for k, v := range map[string]*int{
		"time_in_bed":      s.TimeInBed,
		"latency":          s.Latency,
		"restless_periods": s.RestlessPeriods,
		"period":           s.Period,
	} {
		if v != nil {
			md[k] = *v
		}
	}
	if s.AverageHRV != nil {
		md["hrv_method"] = "rmssd"
	}

	session := model.SleepSession{
		UserID:            userID,
		Provider:          Slug,
		ExternalID:        s.ID,
		BedtimeStart:      start,
		BedtimeEnd:        end,
		TotalSleepSeconds: s.TotalSleepDuration,
		DeepSleepSeconds:  s.DeepSleepDuration,
		REMSleepSeconds:   s.REMSleepDuration,
		LightSleepSeconds: s.LightSleepDuration,
		AwakeSeconds:      s.AwakeTime,
		HRVAvgMs:          s.AverageHRV,
		RestingHR:         s.LowestHeartRate,
		RespiratoryRate:   s.AverageBreath,
		Metadata:          md,
	}
	session.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"external_id":   s.ID,
		"bedtime_start": start.Format(time.RFC3339),
	})

	res := &model.MapResult{
		SleepSessions: []model.SleepSession{session},
		SleepPhases:   hypnogram(userID, s.SleepPhase5Min, start, end, session.ContentHash),
	}
	if s.Type == sleepTypeLongSleep {
		for _, m := range []struct {
			quantityType string
			value        *float64
		}{
			{quantityType: model.QTRestingHeartRate, value: s.LowestHeartRate},
			{quantityType: model.QTRespiratoryRate, value: s.AverageBreath},
		} {
			if m.value == nil || *m.value <= 0 {
				continue
			}
			res.QuantitySamples = append(res.QuantitySamples, sleepSample(userID, s.ID, m.quantityType, *m.value, start, end, md))
		}
	}
	return res, nil
}

// sleepSample is one figure of a night, Replaceable and keyed by the Oura id.
// Both figures are per minute and stored as "bpm".
func sleepSample(userID, ouraID, qt string, v float64, start, end time.Time, md map[string]any) model.QuantitySample {
	smd := map[string]any{"oura_sleep_id": ouraID}
	if tz, ok := md["timezone"]; ok {
		smd["timezone"] = tz
	}
	s := model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      sourceName,
		ExternalID:      fmt.Sprintf("%s_%s", ouraID, qt),
		QuantityType:    qt,
		Value:           v,
		Unit:            "bpm",
		IsPrimarySource: true,
		StartDate:       start,
		EndDate:         end,
		Metadata:        smd,
		Replaceable:     true,
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"quantity_type": qt,
		"external_id":   ouraID,
	})
	return s
}

// hypnogram expands sleep_phase_5_min into phases: step i covers
// [start + 5i min, start + 5(i+1) min), consecutive steps of one stage form one
// phase, and the last phase ends at end at the latest. Unknown characters are
// dropped and break a run.
func hypnogram(userID, steps string, start, end time.Time, parentHash string) []model.SleepPhase {
	var phases []model.SleepPhase
	dropped := 0
	flush := func(stage string, from, to time.Time) {
		if to.After(end) {
			to = end
		}
		if !to.After(from) {
			return
		}
		phases = append(phases, model.SleepPhase{
			UserID:            userID,
			Phase:             stage,
			StartDate:         from,
			EndDate:           to,
			DurationS:         int(to.Sub(from).Seconds()),
			ParentContentHash: parentHash,
		})
	}
	cur, runStart := "", time.Time{}
	for i := 0; i < len(steps); i++ {
		at := start.Add(time.Duration(i) * hypnogramStep)
		stage, ok := hypnogramStages[steps[i]]
		if !ok {
			dropped++
		}
		if stage == cur {
			continue
		}
		if cur != "" {
			flush(cur, runStart, at)
		}
		cur, runStart = stage, at
	}
	if cur != "" {
		flush(cur, runStart, start.Add(time.Duration(len(steps))*hypnogramStep))
	}
	noteDroppedHypnogramSteps(dropped)
	return phases
}

// droppedHypnogramSteps counts the hypnogram steps of an unknown character
// dropped since the process started.
var droppedHypnogramSteps atomic.Int64

// DroppedHypnogramSteps returns how many hypnogram steps of an unknown
// character were dropped so far.
func DroppedHypnogramSteps() int64 { return droppedHypnogramSteps.Load() }

// noteDroppedHypnogramSteps counts n dropped steps and logs one line for them.
func noteDroppedHypnogramSteps(n int) {
	if n == 0 {
		return
	}
	total := droppedHypnogramSteps.Add(int64(n))
	slog.Warn("oura: hypnogram steps of an unknown stage dropped", "provider", Slug, "dropped", n, "total", total)
}
