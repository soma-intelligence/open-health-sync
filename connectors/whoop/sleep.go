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
// no SleepPhase is produced. Slow wave sleep is the deep stage.
//
// The session is hashed on the WHOOP sleep id alone and is Replaceable: a
// re-scored copy, or one whose times the user edited, replaces the stored
// figures (a copy whose start moved supersedes the stored one, by the id). A
// sleep still PENDING_SCORE is therefore stored with its times and no figures,
// and its scored copy fills them in; an UNSCORABLE sleep stays without
// figures. score_state in metadata says which.
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
		Replaceable:  true,
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
