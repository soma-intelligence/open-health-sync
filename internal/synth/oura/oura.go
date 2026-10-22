// SPDX-License-Identifier: AGPL-3.0-only

// Package oura renders a synthetic timeline as Oura API v2 documents.
//
// Source: the Oura API v2 OpenAPI document, version 1.42
// (https://cloud.ouraring.com/v2/static/json/openapi-1.42.json), schemas
// PublicDailyActivity, PublicModifiedSleepModel, PublicDailyReadiness,
// PublicDailySleep, PublicDailyStress, PublicHeartRateRow, PublicWorkout and
// PublicSession. Each line is one document of a usercollection page's data
// array, as the connector stores it.
package oura

import (
	"math"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "oura"

// local is Oura's LocalizedDateTime: ISO 8601 with the offset of the zone
// the ring was in, "2021-11-01T00:00:00-08:00". utc is UtcDateTime.
const local = "2006-01-02T15:04:05-07:00"

var (
	activityShape = synth.Shape{
		ID: "id",
		Required: []string{"id", "active_calories", "average_met_minutes", "contributors", "day", "equivalent_walking_distance",
			"high_activity_met_minutes", "high_activity_time", "inactivity_alerts", "low_activity_met_minutes", "low_activity_time",
			"medium_activity_met_minutes", "medium_activity_time", "met", "meters_to_target", "non_wear_time", "resting_time",
			"sedentary_met_minutes", "sedentary_time", "steps", "target_calories", "target_meters", "timestamp", "total_calories"},
		Optional: []string{"class_5_min", "score"},
		Numbers: map[string][2]float64{"steps": {0, 1e5}, "active_calories": {0, 1e4}, "total_calories": {0, 2e4},
			"equivalent_walking_distance": {0, 2e5}, "score": {1, 100}, "contributors.stay_active": {1, 100}},
		Counts: []string{"steps", "active_calories", "high_activity_time", "non_wear_time"},
		Times:  []string{"timestamp"}, TimeLayout: local,
	}
	sleepShape = synth.Shape{
		ID:       "id",
		Required: []string{"id", "bedtime_end", "bedtime_start", "day", "low_battery_alert", "period", "time_in_bed"},
		Optional: []string{"average_breath", "average_heart_rate", "average_hrv", "awake_time", "deep_sleep_duration", "efficiency",
			"heart_rate", "hrv", "latency", "light_sleep_duration", "lowest_heart_rate", "movement_30_sec", "readiness",
			"readiness_score_delta", "rem_sleep_duration", "restless_periods", "sleep_algorithm_version", "sleep_analysis_reason",
			"sleep_phase_30_sec", "sleep_phase_5_min", "sleep_score_delta", "total_sleep_duration", "type", "app_sleep_phase_5_min"},
		Numbers: map[string][2]float64{"average_heart_rate": {25, 200}, "lowest_heart_rate": {25, 200}, "average_hrv": {1, 300},
			"average_breath": {4, 40}, "efficiency": {1, 100}, "total_sleep_duration": {0, 86400}, "time_in_bed": {0, 86400}},
		Counts: []string{"deep_sleep_duration", "rem_sleep_duration", "time_in_bed"},
		Times:  []string{"bedtime_start", "bedtime_end"}, Start: "bedtime_start", End: "bedtime_end",
		Enums: []string{"type", "sleep_algorithm_version", "sleep_analysis_reason"}, TimeLayout: local,
	}
	readinessShape = synth.Shape{
		ID:       "id",
		Required: []string{"id", "contributors", "day", "timestamp"},
		Optional: []string{"score", "temperature_deviation", "temperature_trend_deviation"},
		Numbers:  map[string][2]float64{"score": {1, 100}, "temperature_deviation": {-5, 5}},
		Times:    []string{"timestamp"}, TimeLayout: local,
	}
	dailySleepShape = synth.Shape{
		ID:       "id",
		Required: []string{"id", "contributors", "day", "timestamp"},
		Optional: []string{"score"},
		Numbers:  map[string][2]float64{"score": {1, 100}},
		Times:    []string{"timestamp"}, TimeLayout: local,
	}
	stressShape = synth.Shape{
		ID:       "id",
		Required: []string{"id", "day"},
		Optional: []string{"day_summary", "recovery_high", "stress_high"},
		Numbers:  map[string][2]float64{"stress_high": {0, 86400}, "recovery_high": {0, 86400}},
		Counts:   []string{"stress_high"},
		Enums:    []string{"day_summary"},
	}
	heartrateShape = synth.Shape{
		Required: []string{"timestamp", "timestamp_unix", "bpm", "source"},
		Numbers:  map[string][2]float64{"bpm": {25, 250}},
		Times:    []string{"timestamp"}, Enums: []string{"source"}, TimeLayout: local,
	}
	workoutShape = synth.Shape{
		ID:       "id",
		Required: []string{"id", "activity", "day", "end_datetime", "intensity", "source", "start_datetime"},
		Optional: []string{"calories", "distance", "label"},
		Numbers:  map[string][2]float64{"calories": {0, 2e4}, "distance": {0, 3e5}},
		Counts:   []string{"calories", "distance"},
		Times:    []string{"start_datetime", "end_datetime"}, Start: "start_datetime", End: "end_datetime",
		Enums: []string{"intensity", "source"}, Strings: []string{"label"}, TimeLayout: local,
	}
	sessionShape = synth.Shape{
		ID:       "id",
		Required: []string{"id", "day", "end_datetime", "start_datetime", "type"},
		Optional: []string{"heart_rate", "heart_rate_variability", "mood", "motion_count"},
		Times:    []string{"start_datetime", "end_datetime"}, Start: "start_datetime", End: "end_datetime",
		Enums: []string{"type", "mood"}, TimeLayout: local,
	}
)

type renderer struct {
	tl *synth.Timeline
	e  *synth.Emitter
	r  *synth.Rand
}

// Lines renders tl as the documents the connector stores, one per line.
func Lines(tl *synth.Timeline) []synth.Line {
	g := &renderer{tl: tl, e: synth.NewEmitter(tl, Slug), r: synth.NewRand(tl.Opt.Seed, "oura")}
	g.e.UnknownType = "daily_cardiovascular_age"
	for _, d := range tl.Days {
		if d.Missing {
			continue
		}
		g.activity(d)
		g.readiness(d)
		g.dailySleep(d)
		g.stress(d)
		g.heartrate(d)
	}
	for _, s := range tl.Sleeps {
		if tl.Worn(s.Start) {
			g.sleep(s)
		}
	}
	for _, w := range tl.Recorded() {
		g.workout(w)
	}
	for _, m := range tl.Mindful {
		if tl.Worn(m.Start) {
			g.session(m)
		}
	}
	return g.e.Lines()
}

func (g *renderer) at(t time.Time) string { return g.tl.Local(t).Format(local) }

func (g *renderer) id() string { return strings.ToLower(g.r.UUID()) }

func (g *renderer) activity(d synth.Day) {
	tl := g.tl
	t := tl.Totals(d)
	var class strings.Builder
	var mets []any
	for m := d.Start; m.Before(d.End); m = m.Add(time.Minute) {
		met := 0.9
		if tl.Worn(m) {
			if w, ok := tl.WorkoutAt(m); ok {
				met = 3 + 9*w.Sport.Intensity()
			} else if _, ok := tl.SleepAt(m); !ok {
				met = 1.2 + float64(int(tl.Steps(m, m.Add(time.Minute))))/30
			}
			mets = append(mets, synth.Round(met, 1))
		} else {
			mets = append(mets, nil)
		}
		if m.Sub(d.Start)%(5*time.Minute) == 0 {
			c := byte('1')
			switch {
			case !tl.Worn(m):
				c = '0'
			case met >= 6:
				c = '5'
			case met >= 3:
				c = '4'
			case met >= 1.5:
				c = '3'
			case met > 1:
				c = '2'
			}
			class.WriteByte(c)
		}
	}
	target := 450.0
	nonWear := (d.End.Sub(d.Start).Minutes() - t.WornMin) * 60
	score := int(math.Min(100, 60+t.Steps/400))
	g.e.Add("daily_activity", d.Start, synth.Doc{
		"id": g.id(), "class_5_min": class.String(), "score": score,
		"active_calories": math.Round(t.ActiveKcal), "average_met_minutes": synth.Round(1+t.ActiveKcal/1000, 3),
		"contributors": synth.Doc{"meet_daily_targets": 80, "move_every_hour": 95, "recovery_time": 90, "stay_active": 85,
			"training_frequency": 90, "training_volume": 88},
		"equivalent_walking_distance": math.Round(t.DistanceM * 1.1),
		"high_activity_met_minutes":   math.Round(t.VigorousMin * 8), "high_activity_time": t.VigorousMin * 60,
		"medium_activity_met_minutes": math.Round(t.ModerateMin * 4), "medium_activity_time": t.ModerateMin * 60,
		"low_activity_met_minutes": math.Round(t.LightMin * 2), "low_activity_time": t.LightMin * 60,
		"sedentary_met_minutes": math.Round(t.SedentaryMin), "sedentary_time": t.SedentaryMin * 60,
		"inactivity_alerts": float64(int(t.SedentaryMin / 240)),
		"met":               synth.Doc{"interval": 60.0, "items": mets, "timestamp": g.at(d.Start)},
		"meters_to_target":  math.Max(0, math.Round(8000-t.DistanceM)), "non_wear_time": nonWear,
		"resting_time": math.Round(d.End.Sub(d.Start).Seconds() - (t.LightMin+t.ModerateMin+t.VigorousMin+t.SedentaryMin)*60 - nonWear),
		"steps":        t.Steps, "target_calories": target, "target_meters": 8000.0,
		"timestamp": g.at(d.Start), "total_calories": math.Round(t.ActiveKcal + t.BasalKcal), "day": d.Date,
	}, activityShape)
}

func (g *renderer) readiness(d synth.Day) {
	s, ok := g.tl.NightOf(d.Index)
	if !ok || !g.tl.Worn(s.Start) {
		return
	}
	g.e.Add("daily_readiness", d.Start, synth.Doc{
		"id": g.id(), "day": d.Date, "timestamp": g.at(d.Start), "score": s.Score,
		"temperature_deviation": synth.Round(s.TempDelta, 2), "temperature_trend_deviation": synth.Round(s.TempDelta/2, 2),
		"contributors": g.readinessContributors(s),
	}, readinessShape)
}

func (g *renderer) readinessContributors(s synth.Sleep) synth.Doc {
	return synth.Doc{"activity_balance": 85, "body_temperature": 98, "hrv_balance": 80, "previous_day_activity": 76,
		"previous_night": s.Score, "recovery_index": 90, "resting_heart_rate": 92, "sleep_balance": 84, "sleep_regularity": 88}
}

func (g *renderer) dailySleep(d synth.Day) {
	s, ok := g.tl.NightOf(d.Index)
	if !ok || !g.tl.Worn(s.Start) {
		return
	}
	g.e.Add("daily_sleep", d.Start, synth.Doc{
		"id": g.id(), "day": d.Date, "timestamp": g.at(d.Start), "score": s.Score,
		"contributors": synth.Doc{"deep_sleep": 90, "efficiency": 92, "latency": 85, "rem_sleep": 88, "restfulness": 80, "timing": 95,
			"total_sleep": int(math.Min(100, s.Asleep().Hours()/8*100))},
	}, dailySleepShape)
}

func (g *renderer) stress(d synth.Day) {
	t := g.tl.Totals(d)
	high := math.Round(t.VigorousMin*60 + 1800)
	sum := "normal"
	switch {
	case high > 7200:
		sum = "stressful"
	case high < 2400:
		sum = "restored"
	}
	g.e.Add("daily_stress", d.Start, synth.Doc{"id": g.id(), "day": d.Date, "stress_high": high,
		"recovery_high": math.Round(3600 + 600*float64(d.Index%5)), "day_summary": sum}, stressShape)
}

func (g *renderer) heartrate(d synth.Day) {
	tl := g.tl
	step := time.Duration(float64(5*time.Minute) / tl.Opt.Volume)
	if step < time.Second {
		step = time.Second
	}
	for _, t := range tl.Every(d.Start, d.End, step) {
		src := "awake"
		if _, ok := tl.WorkoutAt(t); ok {
			src = "workout"
		} else if _, ok := tl.SleepAt(t); ok {
			src = "sleep"
		}
		g.e.Add("heartrate", t, synth.Doc{"timestamp": t.UTC().Format(local), "timestamp_unix": t.UnixMilli(),
			"bpm": tl.HeartRate(t), "source": src}, heartrateShape)
	}
}

// phases writes the stages as Oura's phase string: 1 deep, 2 light, 3 REM,
// 4 awake, one character per step.
func phases(s synth.Sleep, step time.Duration) string {
	var b strings.Builder
	for t := s.Start; t.Before(s.End); t = t.Add(step) {
		switch s.StageAt(t) {
		case synth.StageDeep:
			b.WriteByte('1')
		case synth.StageLight:
			b.WriteByte('2')
		case synth.StageREM:
			b.WriteByte('3')
		default:
			b.WriteByte('4')
		}
	}
	return b.String()
}

func (g *renderer) sample(start, end time.Time, step time.Duration, f func(time.Time) any) synth.Doc {
	var items []any
	for t := start; t.Before(end); t = t.Add(step) {
		items = append(items, f(t))
	}
	return synth.Doc{"interval": step.Seconds(), "items": items, "timestamp": g.at(start)}
}

func (g *renderer) sleep(s synth.Sleep) {
	tl := g.tl
	typ := "long_sleep"
	if s.Nap {
		typ = "late_nap"
		if s.End.Sub(s.Start) < 30*time.Minute {
			typ = "rest"
		}
	}
	day := tl.Days[s.Day].Date
	var moves strings.Builder
	for t := s.Start; t.Before(s.End); t = t.Add(30 * time.Second) {
		c := byte('1')
		if s.StageAt(t) == synth.StageAwake {
			c = '3'
		} else if synth.Round(tl.HeartRate(t), 0) > s.AvgHR+3 {
			c = '2'
		}
		moves.WriteByte(c)
	}
	inBed := math.Round(s.End.Sub(s.Start).Seconds())
	doc := synth.Doc{
		"id": g.id(), "day": day, "type": typ, "period": float64(len(tl.SleepsOn(s.Day)) - 1),
		"bedtime_start": g.at(s.Start), "bedtime_end": g.at(s.End), "time_in_bed": inBed,
		"total_sleep_duration": math.Round(s.Asleep().Seconds()), "awake_time": math.Round(s.In(synth.StageAwake).Seconds()),
		"deep_sleep_duration": math.Round(s.In(synth.StageDeep).Seconds()), "light_sleep_duration": math.Round(s.In(synth.StageLight).Seconds()),
		"rem_sleep_duration": math.Round(s.In(synth.StageREM).Seconds()),
		"efficiency":         math.Round(100 * s.Asleep().Seconds() / math.Max(inBed, 1)),
		"latency":            math.Round(firstAsleep(s).Sub(s.Start).Seconds()),
		"average_breath":     synth.Round(s.Resp, 3), "average_heart_rate": synth.Round(s.AvgHR, 3),
		"lowest_heart_rate": s.LowestHR, "average_hrv": math.Round(s.HRV),
		"low_battery_alert": false, "restless_periods": float64(strings.Count(moves.String(), "3")),
		"sleep_phase_5_min": phases(s, 5*time.Minute), "sleep_phase_30_sec": phases(s, 30*time.Second),
		"app_sleep_phase_5_min": phases(s, 5*time.Minute), "movement_30_sec": moves.String(),
		"heart_rate": g.sample(s.Start, s.End, 5*time.Minute, func(t time.Time) any { return tl.HeartRate(t) }),
		"hrv": g.sample(s.Start, s.End, 5*time.Minute, func(t time.Time) any {
			if s.StageAt(t) == synth.StageAwake {
				return nil
			}
			return math.Round(s.HRV + 8*math.Sin(float64(t.Unix())/900))
		}),
		"readiness": synth.Doc{"score": s.Score, "temperature_deviation": synth.Round(s.TempDelta, 2),
			"temperature_trend_deviation": synth.Round(s.TempDelta/2, 2), "contributors": g.readinessContributors(s)},
		"readiness_score_delta": nil, "sleep_score_delta": nil, "ring_id": nil,
		"sleep_algorithm_version": "v2", "sleep_analysis_reason": "foreground_sleep_analysis",
	}
	g.e.Add("sleep", s.Start, doc, sleepShape)
}

func firstAsleep(s synth.Sleep) time.Time {
	for _, st := range s.Stages {
		if st.Stage != synth.StageAwake {
			return st.Start
		}
	}
	return s.Start
}

// activities are Oura's workout activity names for each sport. The
// specification types activity as a free string.
var activities = map[synth.Sport]string{
	synth.SportRun: "running", synth.SportWalk: "walking", synth.SportCycle: "cycling", synth.SportSwim: "swimming",
	synth.SportStrength: "strengthTraining", synth.SportYoga: "yoga", synth.SportHIIT: "hiit", synth.SportRow: "rowing",
	synth.SportHike: "hiking", synth.SportOther: "other",
}

func (g *renderer) workout(w synth.Workout) {
	intensity := "moderate"
	switch {
	case w.Sport.Intensity() >= 0.75:
		intensity = "hard"
	case w.Sport.Intensity() < 0.45:
		intensity = "easy"
	}
	doc := synth.Doc{
		"id": g.id(), "activity": activities[w.Sport], "day": g.tl.Days[w.Day].Date,
		"start_datetime": g.at(w.Start), "end_datetime": g.at(w.End), "intensity": intensity,
		"source":   synth.Pick(g.r, "manual", "autodetected", "confirmed", "workout_heart_rate"),
		"calories": synth.Round(w.EnergyKcal, 1), "label": nil,
	}
	if w.DistanceM > 0 {
		doc["distance"] = synth.Round(w.DistanceM, 1)
	} else {
		doc["distance"] = nil
	}
	g.e.Add("workout", w.Start, doc, workoutShape)
}

func (g *renderer) session(m synth.Interval) {
	tl := g.tl
	typ := synth.Pick(g.r, "meditation", "breathing", "relaxation")
	d, _ := tl.DayOf(m.Start)
	g.e.Add("session", m.Start, synth.Doc{
		"id": g.id(), "day": d.Date, "type": typ, "start_datetime": g.at(m.Start), "end_datetime": g.at(m.End),
		"mood":       synth.Pick[any](g.r, "good", "great", "same", nil),
		"heart_rate": g.sample(m.Start, m.End, 5*time.Second, func(t time.Time) any { return tl.HeartRate(t) - 4 }),
		"heart_rate_variability": g.sample(m.Start, m.End, 5*time.Second, func(t time.Time) any {
			return math.Round(tl.P.HRVms + 10*math.Sin(float64(t.Unix())/30))
		}),
		"motion_count": g.sample(m.Start, m.End, 5*time.Second, func(time.Time) any { return 0.0 }),
	}, sessionShape)
}
