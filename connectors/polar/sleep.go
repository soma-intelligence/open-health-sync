// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"fmt"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// hypnogramStages maps the AccessLink hypnogram codes to the canonical
// stages: 0 wake, 1 REM, 2 lighter non-REM (N1), 3 light non-REM (N2),
// 4 deep non-REM (N3). 5 (unknown, for example poor skin contact) and any
// other code are dropped (see noteDroppedSleepStates).
var hypnogramStages = map[int]string{
	0: model.SleepStageAwake,
	1: model.SleepStageREM,
	2: model.SleepStageLight,
	3: model.SleepStageLight,
	4: model.SleepStageDeep,
}

// mapSleep turns one night into a SleepSession, one SleepPhase per
// hypnogram segment and one heart_rate sample per sleep heart rate sample.
//
// AccessLink keys a night by its date, so the hash is the date: the same
// night sent again, or re-scored by Polar, maps to the same session. The
// session is Replaceable: a re-scored night replaces the stored figures and
// its hypnogram the stored phases, and a night whose start moved supersedes
// the stored session of the same date (ExternalID).
//
// The hypnogram and the heart rate samples are keyed by local "HH:MM" wall
// clock times. They are placed on the night starting at sleep_start_time
// (a time earlier in the day than the start belongs to the next day) and
// converted to UTC with the start's offset. Each hypnogram entry is read as
// the stage from that time until the next entry, the last one until
// sleep_end_time.
func mapSleep(line []byte, userID string) (*model.MapResult, error) {
	var s Sleep
	if err := decode(DataTypeSleep, line, &s); err != nil {
		return nil, err
	}
	if _, err := parseDate(s.Date); err != nil {
		return nil, fmt.Errorf("polar: sleep: %w", err)
	}
	if s.SleepStartTime == "" || s.SleepEndTime == "" {
		return nil, fmt.Errorf("polar: sleep: missing sleep_start_time or sleep_end_time")
	}
	start, zoned, err := parseTime(s.SleepStartTime)
	if err != nil {
		return nil, fmt.Errorf("polar: sleep: %w", err)
	}
	end, _, err := parseTime(s.SleepEndTime)
	if err != nil {
		return nil, fmt.Errorf("polar: sleep: %w", err)
	}
	offset := 0
	if zoned {
		offset = offsetMinutes(start)
	}

	md := map[string]any{"date": s.Date}
	if s.DeviceID != "" {
		md["device_id"] = s.DeviceID
	}
	setIf(md, "continuity", s.Continuity)
	setIf(md, "continuity_class", s.ContinuityClass)
	setIf(md, "unrecognized_sleep_stage", s.UnrecognizedSleepStage)
	setIf(md, "sleep_charge", s.SleepCharge)
	setIf(md, "sleep_goal", s.SleepGoal)
	setIf(md, "sleep_rating", s.SleepRating)
	setIf(md, "short_interruption_duration", s.ShortInterruptionDuration)
	setIf(md, "long_interruption_duration", s.LongInterruptionDuration)
	setIf(md, "sleep_cycles", s.SleepCycles)
	setIf(md, "group_duration_score", s.GroupDurationScore)
	setIf(md, "group_solidity_score", s.GroupSolidityScore)
	setIf(md, "group_regeneration_score", s.GroupRegenerationScore)
	if zoned {
		md = withOffset(md, offset)
	}

	session := model.SleepSession{
		UserID:            userID,
		Provider:          Slug,
		ExternalID:        s.Date,
		BedtimeStart:      start.UTC(),
		BedtimeEnd:        end.UTC(),
		DeepSleepSeconds:  s.DeepSleep,
		REMSleepSeconds:   s.REMSleep,
		LightSleepSeconds: s.LightSleep,
		AwakeSeconds:      s.TotalInterruptionDuration,
		SleepScore:        s.SleepScore,
		Metadata:          md,
		Replaceable:       true,
	}
	if s.DeepSleep != nil || s.REMSleep != nil || s.LightSleep != nil {
		total := 0
		for _, p := range []*int{s.DeepSleep, s.REMSleep, s.LightSleep} {
			if p != nil {
				total += *p
			}
		}
		session.TotalSleepSeconds = &total
	}
	session.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":   userID,
		"provider":  Slug,
		"data_type": DataTypeSleep,
		"date":      s.Date,
	})
	res := &model.MapResult{SleepSessions: []model.SleepSession{session}}

	// The wall clock of the start, as a zoneless time like the clock keys.
	localStart := time.Date(start.Year(), start.Month(), start.Day(), start.Hour(), start.Minute(), start.Second(), 0, time.UTC)
	place := func(clock string) (time.Time, error) {
		t, err := clockOnDay(utcDay(localStart), clock)
		if err != nil {
			return time.Time{}, err
		}
		// Up to an hour before the start still belongs to this evening.
		if t.Before(localStart.Add(-time.Hour)) {
			t = t.AddDate(0, 0, 1)
		}
		return t.Add(-time.Duration(offset) * time.Minute).UTC(), nil
	}

	type mark struct {
		at    time.Time
		stage int
	}
	var marks []mark
	for clock, stage := range s.Hypnogram {
		at, err := place(clock)
		if err != nil {
			return nil, fmt.Errorf("polar: sleep hypnogram: %w", err)
		}
		marks = append(marks, mark{at: at, stage: stage})
	}
	sort.Slice(marks, func(i, j int) bool { return marks[i].at.Before(marks[j].at) })
	dropped := 0
	sessionEnd := end.UTC()
	for i, m := range marks {
		segEnd := sessionEnd
		if i+1 < len(marks) {
			segEnd = marks[i+1].at
		}
		if !segEnd.After(m.at) {
			continue
		}
		stage, ok := hypnogramStages[m.stage]
		if !ok {
			dropped++
			continue
		}
		res.SleepPhases = append(res.SleepPhases, model.SleepPhase{
			UserID:            userID,
			Phase:             stage,
			StartDate:         m.at,
			EndDate:           segEnd,
			DurationS:         int(segEnd.Sub(m.at) / time.Second),
			ParentContentHash: session.ContentHash,
		})
	}
	noteDroppedSleepStates(dropped)

	type reading struct {
		at time.Time
		v  float64
	}
	readings := make([]reading, 0, len(s.HeartRateSamples))
	for clock, v := range s.HeartRateSamples {
		if v <= 0 {
			continue
		}
		at, err := place(clock)
		if err != nil {
			return nil, fmt.Errorf("polar: sleep heart rate: %w", err)
		}
		readings = append(readings, reading{at: at, v: v})
	}
	sort.Slice(readings, func(i, j int) bool { return readings[i].at.Before(readings[j].at) })
	for _, rd := range readings {
		at, v := rd.at, rd.v
		hmd := map[string]any{"context": "sleep"}
		if zoned {
			hmd = withOffset(hmd, offset)
		}
		q := sample(userID, s.DeviceID, "sleep_"+s.Date, model.QTHeartRate, "bpm", v, at, at, hmd)
		q.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id":       userID,
			"provider":      Slug,
			"data_type":     DataTypeSleep,
			"quantity_type": model.QTHeartRate,
			"start_date":    at.Format(time.RFC3339),
		})
		res.QuantitySamples = append(res.QuantitySamples, q)
	}
	return res, nil
}

// droppedSleepStates counts the hypnogram segments of an unknown stage
// dropped since the process started.
var droppedSleepStates atomic.Int64

// DroppedSleepStates returns how many hypnogram segments of an unknown stage
// were dropped so far.
func DroppedSleepStates() int64 { return droppedSleepStates.Load() }

// noteDroppedSleepStates counts n dropped segments and logs one line for them.
func noteDroppedSleepStates(n int) {
	if n == 0 {
		return
	}
	total := droppedSleepStates.Add(int64(n))
	slog.Warn("polar: hypnogram segments of an unknown stage dropped", "provider", Slug, "dropped", n, "total", total)
}
