// SPDX-License-Identifier: AGPL-3.0-only

package eightsleep

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sourceName is the source_name of every record: the vendor.
const sourceName = "Eight Sleep"

// stages maps the stage names of a session to canonical stages. "out" (out
// of bed) and any other name are not stored as phases, but their duration
// still moves the clock.
var stages = map[string]string{
	"awake": model.SleepStageAwake,
	"light": model.SleepStageLight,
	"deep":  model.SleepStageDeep,
	"rem":   model.SleepStageREM,
}

// Plausible ranges; readings outside are sensor noise and dropped.
const (
	minHR, maxHR     = 25.0, 220.0
	minHRV, maxHRV   = 1.0, 300.0
	minResp, maxResp = 4.0, 60.0
	minTemp, maxTemp = -10.0, 60.0
)

// maxSession bounds a session's length.
const maxSession = 24 * time.Hour

// mapTrendDay turns one trends day into a SleepSession and its phases per
// session, the night's heart rate, HRV and breathing rate readings as
// samples, and a nightly hrv_rmssd sample when the day carries the night's
// HRV but no readings.
//
// The day's score and quality figures belong to the night as a whole: they
// go on its longest session. Bed and room temperature have no canonical
// quantity type; each session keeps their average, minimum and maximum in
// its metadata, and the readings stay in the raw store.
//
// A session is hashed on its id (else its start) and Replaceable: Eight
// Sleep re-sends a night while it processes it, and the newest copy wins.
// Readings are hashed on their type and time, and the first write wins.
func mapTrendDay(fields map[string]any, userID string) (*model.MapResult, error) {
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("eight_sleep: trends: re-encode line: %w", err)
	}
	var d TrendDay
	if err := json.Unmarshal(line, &d); err != nil {
		return nil, fmt.Errorf("eight_sleep: trends: %w", err)
	}
	if _, err := time.Parse(time.DateOnly, d.Day); err != nil {
		return nil, fmt.Errorf("eight_sleep: trends: day %q is not a date", d.Day)
	}

	res := &model.MapResult{}
	type built struct {
		session model.SleepSession
		sleep   int
	}
	var sessions []built
	for _, s := range d.Sessions {
		sid := sessionID(s)
		md := map[string]any{"date": d.Day}
		if sid != "" {
			md["session_id"] = sid
		}
		res.QuantitySamples = append(res.QuantitySamples, readings(userID, sid, s)...)

		start, err := parseTime(s.Ts)
		if err != nil || len(s.Stages) == 0 || sid == "" {
			continue
		}
		session, phases, sleep, ok := sessionOf(userID, d.Day, sid, start, s, md)
		if !ok {
			continue
		}
		res.SleepPhases = append(res.SleepPhases, phases...)
		sessions = append(sessions, built{session: session, sleep: sleep})
	}

	if len(sessions) > 0 {
		// The night's figures go on the longest session (the first of
		// equal ones).
		main := 0
		for i, b := range sessions {
			if b.sleep > sessions[main].sleep {
				main = i
			}
		}
		m := &sessions[main].session
		dayFigures(m, d)
		if !hasSeries(d, seriesHRV) && m.HRVAvgMs != nil {
			s := sample(userID, "hrv_"+d.Day, model.QTHRVRMSSD, "ms", *m.HRVAvgMs, m.BedtimeStart, m.BedtimeEnd,
				map[string]any{model.MetadataSummaryDate: d.Day, "session_id": m.ExternalID})
			s.ContentHash = hash(userID, "date", d.Day, "quantity_type", model.QTHRVRMSSD)
			s.Replaceable = true
			res.QuantitySamples = append(res.QuantitySamples, s)
		}
		for _, b := range sessions {
			res.SleepSessions = append(res.SleepSessions, b.session)
		}
	}
	if len(res.QuantitySamples) == 0 && len(res.SleepSessions) == 0 {
		return nil, nil
	}
	return res, nil
}

// sessionOf builds one session and its phases from its stages. It returns
// the seconds asleep, and false when the stages add up to nothing.
func sessionOf(userID, day, sid string, start time.Time, s Session, md map[string]any) (model.SleepSession, []model.SleepPhase, int, bool) {
	type phase struct {
		stage      string
		start, end time.Time
	}
	var phases []phase
	byStage := map[string]int{}
	at := start
	for _, st := range s.Stages {
		if !st.Duration.ok || st.Duration.v <= 0 || st.Duration.v > maxSession.Seconds() {
			continue
		}
		next := at.Add(time.Duration(st.Duration.v * float64(time.Second)))
		if next.Sub(start) > maxSession {
			break
		}
		if stage, ok := stages[st.Stage]; ok {
			phases = append(phases, phase{stage: stage, start: at, end: next})
			byStage[stage] += int(next.Sub(at) / time.Second)
		}
		at = next
	}
	if len(phases) == 0 {
		return model.SleepSession{}, nil, 0, false
	}
	// The night ends with its last phase: a trailing "out" is time out of
	// bed, not in it.
	end := phases[len(phases)-1].end

	for _, k := range []struct {
		key, series string
	}{{"bed_temperature", seriesBedTemp}, {"room_temperature", seriesRoomTemp}} {
		if avg, lo, hi, ok := stats(s.Timeseries[k.series], minTemp, maxTemp); ok {
			md[k.key+"_avg_c"] = avg
			md[k.key+"_min_c"] = lo
			md[k.key+"_max_c"] = hi
		}
	}

	secs := func(stage string) *int {
		v, ok := byStage[stage]
		if !ok {
			return nil
		}
		return &v
	}
	sleep := byStage[model.SleepStageLight] + byStage[model.SleepStageDeep] + byStage[model.SleepStageREM]
	session := model.SleepSession{
		UserID:            userID,
		Provider:          Slug,
		ExternalID:        "session_" + sid,
		BedtimeStart:      start,
		BedtimeEnd:        end,
		TotalSleepSeconds: &sleep,
		DeepSleepSeconds:  secs(model.SleepStageDeep),
		LightSleepSeconds: secs(model.SleepStageLight),
		REMSleepSeconds:   secs(model.SleepStageREM),
		AwakeSeconds:      secs(model.SleepStageAwake),
		Metadata:          md,
		Replaceable:       true,
	}
	if avg, _, _, ok := stats(s.Timeseries[seriesRespiratory], minResp, maxResp); ok {
		session.RespiratoryRate = &avg
	}
	if avg, _, _, ok := stats(s.Timeseries[seriesHRV], minHRV, maxHRV); ok {
		session.HRVAvgMs = &avg
	}
	session.ContentHash = hash(userID, "session_id", sid)

	out := make([]model.SleepPhase, 0, len(phases))
	for _, p := range phases {
		out = append(out, model.SleepPhase{
			UserID:            userID,
			Phase:             p.stage,
			StartDate:         p.start,
			EndDate:           p.end,
			DurationS:         int(p.end.Sub(p.start) / time.Second),
			ParentContentHash: session.ContentHash,
		})
	}
	return session, out, sleep, true
}

// dayFigures puts the day's figures on the night's main session: the sleep
// score, the night's HRV and breathing rate (they replace the averages of
// the readings, being Eight Sleep's own figure), the quality and routine
// scores and the toss and turns.
func dayFigures(m *model.SleepSession, d TrendDay) {
	if v := d.Score.get(); v != nil && *v >= 0 && *v <= 100 {
		m.SleepScore = v
	}
	if q := d.SleepQualityScore; q != nil {
		if q.HRV != nil {
			if v := q.HRV.Current.get(); v != nil && *v >= minHRV && *v <= maxHRV {
				m.HRVAvgMs = v
			}
		}
		if q.RespiratoryRate != nil {
			if v := q.RespiratoryRate.Current.get(); v != nil && *v >= minResp && *v <= maxResp {
				m.RespiratoryRate = v
			}
		}
		if v := q.Total.get(); v != nil {
			m.Metadata["sleep_quality_score"] = *v
		}
	}
	if r := d.SleepRoutineScore; r != nil {
		if v := r.Total.get(); v != nil {
			m.Metadata["sleep_routine_score"] = *v
		}
	}
	if v := d.TNT.get(); v != nil && *v >= 0 {
		m.Metadata["toss_and_turns"] = *v
	}
	if d.Processing {
		m.Metadata["processing"] = true
	}
}

// readings turns a session's heart rate, HRV and breathing rate series into
// point samples.
func readings(userID, sid string, s Session) []model.QuantitySample {
	var out []model.QuantitySample
	for _, k := range []struct {
		series, qt, unit string
		lo, hi           float64
	}{
		{seriesHeartRate, model.QTHeartRate, "bpm", minHR, maxHR},
		{seriesHRV, model.QTHRVRMSSD, "ms", minHRV, maxHRV},
		{seriesRespiratory, model.QTRespiratoryRate, "bpm", minResp, maxResp},
	} {
		for _, p := range s.Timeseries[k.series] {
			if p.v < k.lo || p.v > k.hi {
				continue
			}
			md := map[string]any{}
			if sid != "" {
				md["session_id"] = sid
			}
			smp := sample(userID, "", k.qt, k.unit, p.v, p.at, p.at, md)
			smp.ContentHash = hash(userID, "quantity_type", k.qt, "start_date", p.at.Format(time.RFC3339))
			out = append(out, smp)
		}
	}
	return out
}

// sessionID is a session's id (sent as a string or a number), else its
// start; "" when it has neither.
func sessionID(s Session) string {
	if id := jsonID(s.ID); id != "" {
		return id
	}
	if t, err := parseTime(s.Ts); err == nil {
		return t.Format(time.RFC3339)
	}
	return ""
}

// hasSeries reports whether any session of d carries readings of key.
func hasSeries(d TrendDay, key string) bool {
	for _, s := range d.Sessions {
		if len(s.Timeseries[key]) > 0 {
			return true
		}
	}
	return false
}

// stats returns the mean (to two decimals), minimum and maximum of the
// readings in [lo, hi].
func stats(s series, lo, hi float64) (avg, minV, maxV float64, ok bool) {
	vals := make([]float64, 0, len(s))
	for _, p := range s {
		if p.v >= lo && p.v <= hi {
			vals = append(vals, p.v)
		}
	}
	if len(vals) == 0 {
		return 0, 0, 0, false
	}
	sort.Float64s(vals) // a fixed order keeps the sum deterministic
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return math.Round(sum/float64(len(vals))*100) / 100, vals[0], vals[len(vals)-1], true
}

// sample builds one quantity sample of this connector; the caller sets the
// content hash and Replaceable.
func sample(userID, externalID, qt, unit string, v float64, start, end time.Time, md map[string]any) model.QuantitySample {
	return model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      sourceName,
		ExternalID:      externalID,
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       start,
		EndDate:         end,
		Metadata:        md,
	}
}

// hash is the content hash of a record of this connector.
func hash(userID string, kv ...string) string {
	m := map[string]string{"user_id": userID, "provider": Slug, "data_type": DataTypeTrends}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return model.ComputeContentHash(m)
}
