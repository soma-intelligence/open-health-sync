// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"fmt"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapSleep turns one sleep (a night or a nap) into a SleepSession with its
// stage durations, plus a respiratory_rate sample over it.
//
// WHOOP's public API gives the time spent in each stage, not a hypnogram, so
// no SleepPhase is produced. Slow wave sleep is the deep stage. A sleep still
// PENDING_SCORE gives nothing: the writer keeps the first copy of a session
// it sees, so storing it unscored would freeze it without its figures. WHOOP
// sends it again once scored. An UNSCORABLE sleep is stored without figures.
func mapSleep(line []byte, userID string) (*model.MapResult, error) {
	var s Sleep
	if err := decode(DataTypeSleep, line, &s); err != nil {
		return nil, err
	}
	if s.ID == "" {
		return nil, fmt.Errorf("whoop: sleep: missing id")
	}
	start, err := requireTime(DataTypeSleep, "start", s.Start)
	if err != nil {
		return nil, err
	}
	end, err := requireTime(DataTypeSleep, "end", s.End)
	if err != nil {
		return nil, err
	}
	if s.ScoreState == scorePending {
		return nil, nil
	}
	id := string(s.ID)

	md := map[string]any{"nap": s.Nap, "score_state": s.ScoreState}
	if s.CycleID != "" {
		md["whoop_cycle_id"] = string(s.CycleID)
	}
	session := model.SleepSession{
		UserID:       userID,
		Provider:     Slug,
		ExternalID:   id,
		BedtimeStart: start,
		BedtimeEnd:   end,
	}
	var respiratory *float64
	if sc := s.Score; sc != nil && s.ScoreState == scoreScored {
		session.SleepScore = sc.SleepPerformancePercentage
		session.RespiratoryRate = sc.RespiratoryRate
		respiratory = sc.RespiratoryRate
		setIf(md, "sleep_efficiency_percentage", sc.SleepEfficiencyPercentage)
		setIf(md, "sleep_consistency_percentage", sc.SleepConsistencyPercentage)
		if sc.SleepNeeded != nil {
			md["sleep_needed"] = sc.SleepNeeded
		}
		if st := sc.StageSummary; st != nil {
			session.DeepSleepSeconds = msToSeconds(st.TotalSlowWaveSleepTimeMilli)
			session.REMSleepSeconds = msToSeconds(st.TotalREMSleepTimeMilli)
			session.LightSleepSeconds = msToSeconds(st.TotalLightSleepTimeMilli)
			session.AwakeSeconds = msToSeconds(st.TotalAwakeTimeMilli)
			setIf(md, "in_bed_seconds", msToSeconds(st.TotalInBedTimeMilli))
			setIf(md, "no_data_seconds", msToSeconds(st.TotalNoDataTimeMilli))
			setIf(md, "sleep_cycle_count", st.SleepCycleCount)
			setIf(md, "disturbance_count", st.DisturbanceCount)
		}
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
	session.Metadata = withTimezone(md, s.TimezoneOffset)
	session.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":     userID,
		"provider":    Slug,
		"external_id": id,
	})

	res := &model.MapResult{SleepSessions: []model.SleepSession{session}}
	if respiratory != nil {
		m := withTimezone(map[string]any{"whoop_sleep_id": id, "nap": s.Nap}, s.TimezoneOffset)
		res.QuantitySamples = append(res.QuantitySamples,
			quantity(userID, id, model.QTRespiratoryRate, *respiratory, "count/min", start, end, m))
	}
	return res, nil
}
