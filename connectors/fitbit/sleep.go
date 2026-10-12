// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sleepStages maps the API's stage types to the canonical stages. Classic
// sleep (no stages, older devices or short naps) reports ASLEEP, RESTLESS
// and AWAKE: ASLEEP counts as light sleep (the canonical vocabulary has no
// "unspecified asleep" phase) and RESTLESS as awake, so neither inflates
// deep or REM. The session keeps the original type (sleep_type CLASSIC).
var sleepStages = map[string]string{
	"AWAKE":    model.SleepStageAwake,
	"LIGHT":    model.SleepStageLight,
	"DEEP":     model.SleepStageDeep,
	"REM":      model.SleepStageREM,
	"ASLEEP":   model.SleepStageLight,
	"RESTLESS": model.SleepStageAwake,
}

// mapSleep turns one sleep data point into a SleepSession and its phases.
//
// Durations come from summary (minutes, times 60): minutesAsleep is the
// total, minutesAwake the awake time, and stagesSummary the deep, REM and
// light minutes. The session is identified by the data point id (its
// resource name) and its start, never by its values, and is Replaceable: a
// sleep Google reprocesses (stages computed later, an edit) replaces the
// stored figures and phases.
func mapSleep(p point, userID string) (*model.MapResult, error) {
	var v sleepValue
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	md := p.metadata()
	start, end, err := spanOf(v.Interval, md)
	if err != nil {
		return nil, err
	}
	if !end.After(start) {
		return nil, nil
	}
	id := p.recordID()
	if id == "" {
		id = fmt.Sprintf("sleep_%d", start.Unix())
	}
	if v.Type != "" {
		md["sleep_type"] = v.Type
	}
	if m := v.Metadata; m != nil {
		for k, b := range map[string]*bool{"main_sleep": m.MainSleep, "nap": m.Nap, "manually_edited": m.ManuallyEdited, "processed": m.Processed} {
			if b != nil {
				md[k] = *b
			}
		}
		if m.StagesStatus != "" {
			md["stages_status"] = m.StagesStatus
		}
	}

	s := model.SleepSession{
		UserID:       userID,
		Provider:     Slug,
		ExternalID:   id,
		BedtimeStart: start,
		BedtimeEnd:   end,
		Metadata:     md,
		Replaceable:  true,
	}
	secs := func(n *num) *int {
		x, ok := val(n)
		if !ok || x < 0 {
			return nil
		}
		s := int(x * 60)
		return &s
	}
	if sum := v.Summary; sum != nil {
		s.TotalSleepSeconds = secs(sum.MinutesAsleep)
		s.AwakeSeconds = secs(sum.MinutesAwake)
		for k, n := range map[string]*num{
			"minutes_to_fall_asleep":  sum.MinutesToFallAsleep,
			"minutes_after_wake_up":   sum.MinutesAfterWakeUp,
			"minutes_in_sleep_period": sum.MinutesInSleepPeriod,
		} {
			if x, ok := val(n); ok {
				md[k] = x
			}
		}
		for _, st := range sum.StagesSummary {
			switch st.Type {
			case "DEEP":
				s.DeepSleepSeconds = secs(st.Minutes)
			case "REM":
				s.REMSleepSeconds = secs(st.Minutes)
			case "LIGHT":
				s.LightSleepSeconds = secs(st.Minutes)
			case "RESTLESS":
				if x, ok := val(st.Minutes); ok {
					md["minutes_restless"] = x
				}
			}
		}
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"external_id":   id,
		"bedtime_start": start.Format(time.RFC3339),
	})

	res := &model.MapResult{SleepSessions: []model.SleepSession{s}}
	for _, st := range v.Stages {
		phase, ok := sleepStages[st.Type]
		if !ok {
			continue
		}
		ps, err := parseTime(st.StartTime)
		if err != nil {
			return nil, fmt.Errorf("sleep stage startTime: %w", err)
		}
		pe, err := parseTime(st.EndTime)
		if err != nil {
			return nil, fmt.Errorf("sleep stage endTime: %w", err)
		}
		if !pe.After(ps) {
			continue
		}
		res.SleepPhases = append(res.SleepPhases, model.SleepPhase{
			UserID:            userID,
			Phase:             phase,
			StartDate:         ps,
			EndDate:           pe,
			DurationS:         int(pe.Sub(ps).Seconds()),
			ParentContentHash: s.ContentHash,
		})
	}
	return res, nil
}
