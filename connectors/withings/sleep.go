// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sleepStates maps Withings phase states to the canonical stages. A segment
// of any other state is dropped (see noteDroppedSleepStates).
var sleepStates = map[int]string{
	0: model.SleepStageAwake,
	1: model.SleepStageLight,
	2: model.SleepStageDeep,
	3: model.SleepStageREM,
}

// sleepColumns are the data fields that land in typed session columns; every
// other data field is kept in the session metadata. The resting heart rate is
// hr_min, the night's lowest heart rate (as Oura defines it); hr_average, the
// mean heart rate asleep, has no column and stays in metadata.
var sleepColumns = map[string]bool{
	"hr_min": true, "rr_average": true, "sleep_score": true,
	"deepsleepduration": true, "remsleepduration": true,
	"lightsleepduration": true, "wakeupduration": true,
}

// mapSleep turns one sleep summary into a SleepSession, plus one SleepPhase
// per detailed segment when the line carries "sleep_phases".
func mapSleep(line []byte, userID string) (*model.MapResult, error) {
	var head struct {
		Startdate *int64 `json:"startdate"`
		Enddate   *int64 `json:"enddate"`
	}
	if err := decode(DataTypeSleep, line, &head); err != nil {
		return nil, err
	}
	if head.Startdate == nil {
		return nil, fmt.Errorf("withings: sleep: missing startdate")
	}
	if head.Enddate == nil {
		return nil, fmt.Errorf("withings: sleep: missing enddate")
	}
	var s SleepSeries
	if err := decode(DataTypeSleep, line, &s); err != nil {
		return nil, err
	}
	data := s.Data
	if data == nil {
		data = map[string]any{}
	}
	start := ParseWithingsTimestamp(s.Startdate)
	end := ParseWithingsTimestamp(s.Enddate)

	md := map[string]any{"state": s.State}
	for k, v := range data {
		if !sleepColumns[k] {
			md[k] = v
		}
	}
	if s.Timezone != "" {
		md = withTimezone(md, s.Timezone)
	}
	session := model.SleepSession{
		UserID:            userID,
		Provider:          Slug,
		ExternalID:        string(s.ID),
		BedtimeStart:      start,
		BedtimeEnd:        end,
		DeepSleepSeconds:  optInt(data, "deepsleepduration"),
		REMSleepSeconds:   optInt(data, "remsleepduration"),
		LightSleepSeconds: optInt(data, "lightsleepduration"),
		AwakeSeconds:      optInt(data, "wakeupduration"),
		RestingHR:         optFloat(data, "hr_min"),
		RespiratoryRate:   optFloat(data, "rr_average"),
		SleepScore:        optFloat(data, "sleep_score"),
		Metadata:          md,
	}
	if session.DeepSleepSeconds != nil || session.REMSleepSeconds != nil || session.LightSleepSeconds != nil {
		total := 0
		for _, p := range []*int{session.DeepSleepSeconds, session.REMSleepSeconds, session.LightSleepSeconds} {
			if p != nil {
				total += *p
			}
		}
		session.TotalSleepSeconds = &total
	}
	session.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"bedtime_start": start.Format(time.RFC3339),
		"bedtime_end":   end.Format(time.RFC3339),
	})

	res := &model.MapResult{SleepSessions: []model.SleepSession{session}}
	dropped := 0
	for _, ph := range s.SleepPhases {
		stage, ok := sleepStates[ph.State]
		if !ok {
			dropped++
			continue
		}
		res.SleepPhases = append(res.SleepPhases, model.SleepPhase{
			UserID:            userID,
			Phase:             stage,
			StartDate:         ParseWithingsTimestamp(ph.Startdate),
			EndDate:           ParseWithingsTimestamp(ph.Enddate),
			DurationS:         int(ph.Enddate - ph.Startdate),
			ParentContentHash: session.ContentHash,
		})
	}
	noteDroppedSleepStates(dropped)
	return res, nil
}

// droppedSleepStates counts the phase segments of an unknown state dropped
// since the process started.
var droppedSleepStates atomic.Int64

// DroppedSleepStates returns how many phase segments of an unknown state were
// dropped so far.
func DroppedSleepStates() int64 { return droppedSleepStates.Load() }

// noteDroppedSleepStates counts n dropped segments and logs one line for them.
func noteDroppedSleepStates(n int) {
	if n == 0 {
		return
	}
	total := droppedSleepStates.Add(int64(n))
	slog.Warn("withings: sleep segments of an unknown state dropped", "provider", Slug, "dropped", n, "total", total)
}
