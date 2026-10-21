// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sleepLevels maps the sleepLevelsMap keys to canonical stages. Other keys
// ("unmeasurable") are not stored as phases.
var sleepLevels = map[string]string{
	"deep":  model.SleepStageDeep,
	"light": model.SleepStageLight,
	"rem":   model.SleepStageREM,
	"awake": model.SleepStageAwake,
}

// mapSleep turns one sleep summary into a SleepSession and one SleepPhase per
// sleepLevelsMap interval. The night's SpO2 and respiration series
// (timeOffsetSleepSpo2, timeOffsetSleepRespiration) give the session's
// averages; they are not stored as samples, since the all-day pulse ox and
// respiration summaries carry the same readings.
//
// Garmin keys a night by its calendar date, so the session is hashed on it
// and Replaceable: a night Garmin re-sends (re-scored, or edited by the
// user) replaces the stored figures and phases, and a night whose start
// moved supersedes the stored one of the same date.
func mapSleep(line []byte, userID string) (*model.MapResult, error) {
	var s Sleep
	if err := decode(DataTypeSleeps, line, &s); err != nil {
		return nil, err
	}
	start, err := unixTime("startTimeInSeconds", s.StartTimeInSeconds)
	if err != nil {
		return nil, fmt.Errorf("garmin: sleeps: %w", err)
	}
	date := localDate(start, s.StartTimeOffsetInSeconds)
	if s.CalendarDate != "" {
		if _, err := parseDate(s.CalendarDate); err != nil {
			return nil, fmt.Errorf("garmin: sleeps: %w", err)
		}
		date = s.CalendarDate
	}

	type phase struct {
		stage      string
		start, end time.Time
	}
	var phases []phase
	end := start.Add(time.Duration(seconds(s.DurationInSeconds, 0)) * time.Second)
	for key, spans := range s.SleepLevelsMap {
		stage, ok := sleepLevels[key]
		if !ok {
			continue
		}
		for _, sp := range spans {
			ps, err1 := unixTime("startTimeInSeconds", sp.StartTimeInSeconds)
			pe, err2 := unixTime("endTimeInSeconds", sp.EndTimeInSeconds)
			if err1 != nil || err2 != nil || !pe.After(ps) || pe.Sub(ps) > 24*time.Hour {
				continue
			}
			phases = append(phases, phase{stage: stage, start: ps, end: pe})
			if pe.After(end) && pe.Sub(start) <= 36*time.Hour {
				end = pe
			}
		}
	}
	sort.Slice(phases, func(i, j int) bool {
		if !phases[i].start.Equal(phases[j].start) {
			return phases[i].start.Before(phases[j].start)
		}
		return phases[i].stage < phases[j].stage
	})
	if !end.After(start) {
		return nil, fmt.Errorf("garmin: sleeps: no duration")
	}

	md := dayMeta(s.SummaryID, s.StartTimeOffsetInSeconds)
	md["date"] = date
	if s.Validation != "" {
		md["validation"] = s.Validation
	}
	setIf(md, "unmeasurable_sleep_s", s.UnmeasurableSleepInSeconds)

	sec := func(v *int64) *int {
		if v == nil || *v < 0 || *v > 86400 {
			return nil
		}
		n := int(*v)
		return &n
	}
	session := model.SleepSession{
		UserID:            userID,
		Provider:          Slug,
		ExternalID:        "sleep_" + date,
		BedtimeStart:      start,
		BedtimeEnd:        end,
		DeepSleepSeconds:  sec(s.DeepSleepDurationInSeconds),
		LightSleepSeconds: sec(s.LightSleepDurationInSecs),
		REMSleepSeconds:   sec(s.REMSleepInSeconds),
		AwakeSeconds:      sec(s.AwakeDurationInSeconds),
		Metadata:          md,
		Replaceable:       true,
	}
	if session.DeepSleepSeconds != nil || session.LightSleepSeconds != nil || session.REMSleepSeconds != nil {
		total := 0
		for _, p := range []*int{session.DeepSleepSeconds, session.LightSleepSeconds, session.REMSleepSeconds} {
			if p != nil {
				total += *p
			}
		}
		session.TotalSleepSeconds = &total
	} else {
		session.TotalSleepSeconds = sec(s.DurationInSeconds)
	}
	if s.OverallSleepScore != nil && inBounds(s.OverallSleepScore.Value, 0, 100) {
		session.SleepScore = s.OverallSleepScore.Value
		if s.OverallSleepScore.QualifierKey != "" {
			md["sleep_score_qualifier"] = s.OverallSleepScore.QualifierKey
		}
	}
	session.SpO2Avg = average(s.TimeOffsetSleepSpo2, 50, 100)
	session.RespiratoryRate = average(s.TimeOffsetSleepRespiration, 1, 80)
	session.ContentHash = hash(userID, DataTypeSleeps, "date", date)

	res := &model.MapResult{SleepSessions: []model.SleepSession{session}}
	for _, p := range phases {
		res.SleepPhases = append(res.SleepPhases, model.SleepPhase{
			UserID:            userID,
			Phase:             p.stage,
			StartDate:         p.start,
			EndDate:           p.end,
			DurationS:         int(p.end.Sub(p.start) / time.Second),
			ParentContentHash: session.ContentHash,
		})
	}
	return res, nil
}

// average is the mean of the series values in [lo, hi], or nil.
func average(series map[string]float64, lo, hi float64) *float64 {
	vals := make([]float64, 0, len(series))
	for _, v := range series {
		if math.IsNaN(v) || v < lo || v > hi {
			continue
		}
		vals = append(vals, v)
	}
	if len(vals) == 0 {
		return nil
	}
	sort.Float64s(vals) // a fixed order keeps the sum deterministic
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	avg := math.Round(sum/float64(len(vals))*100) / 100
	return &avg
}
