// SPDX-License-Identifier: AGPL-3.0-only

// Package whoop renders a synthetic timeline as WHOOP API v2 records.
//
// Source: the WHOOP developer platform OpenAPI document
// (https://api.prod.whoop.com/developer/doc/openapi.json), schemas Cycle,
// Recovery, Sleep, WorkoutV2 and UserBodyMeasurement, and the sport table of
// https://developer.whoop.com/docs/developing/user-data/workout. Each line is
// one record of a collection page. The connector adds the recovery's cycle
// bounds as "cycle" and a "snapshot_date" to the body measurement, which the
// lines carry as well.
package whoop

import (
	"math"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "whoop"

// iso is WHOOP's date-time: UTC with milliseconds, "2022-04-24T11:25:44.774Z".
const iso = "2006-01-02T15:04:05.000Z07:00"

var (
	cycleShape = synth.Shape{
		ID:       "id",
		Required: []string{"id", "user_id", "created_at", "updated_at", "start", "timezone_offset", "score_state"},
		Optional: []string{"end", "score", "step_count"},
		Numbers: map[string][2]float64{"score.strain": {0, 21}, "score.kilojoule": {0, 1e5}, "score.average_heart_rate": {25, 250},
			"score.max_heart_rate": {25, 250}, "step_count": {0, 1e5}},
		Counts: []string{"score.kilojoule", "step_count"},
		Times:  []string{"start", "end", "created_at"}, Start: "start", End: "end",
		Enums: []string{"score_state", "timezone_offset"}, TimeLayout: iso,
	}
	recoveryShape = synth.Shape{
		ID:       "sleep_id",
		Required: []string{"cycle_id", "sleep_id", "user_id", "created_at", "updated_at", "score_state"},
		Optional: []string{"score", "cycle", "score.spo2_percentage", "score.skin_temp_celsius"},
		Numbers: map[string][2]float64{"score.recovery_score": {0, 100}, "score.resting_heart_rate": {25, 200},
			"score.hrv_rmssd_milli": {1, 300}, "score.spo2_percentage": {70, 100}, "score.skin_temp_celsius": {25, 40}},
		Times: []string{"created_at", "cycle.start"}, Enums: []string{"score_state"}, TimeLayout: iso,
	}
	sleepShape = synth.Shape{
		ID:       "id",
		Required: []string{"id", "cycle_id", "user_id", "created_at", "updated_at", "start", "end", "timezone_offset", "nap", "score_state"},
		Optional: []string{"score", "v1_id", "score.respiratory_rate", "score.sleep_performance_percentage"},
		Numbers: map[string][2]float64{"score.respiratory_rate": {4, 40}, "score.sleep_efficiency_percentage": {0, 100},
			"score.stage_summary.total_in_bed_time_milli": {0, 86400000}},
		Counts: []string{"score.stage_summary.total_rem_sleep_time_milli", "score.stage_summary.disturbance_count"},
		Times:  []string{"start", "end"}, Start: "start", End: "end",
		Enums: []string{"score_state", "timezone_offset"}, TimeLayout: iso,
	}
	workoutShape = synth.Shape{
		ID:       "id",
		Required: []string{"id", "user_id", "created_at", "updated_at", "start", "end", "timezone_offset", "sport_name", "score_state"},
		Optional: []string{"score", "sport_id", "v1_id", "score.distance_meter", "score.altitude_gain_meter", "score.altitude_change_meter"},
		Numbers: map[string][2]float64{"score.strain": {0, 21}, "score.average_heart_rate": {25, 250}, "score.kilojoule": {0, 1e5},
			"score.distance_meter": {0, 3e5}, "score.percent_recorded": {0, 100}},
		Counts: []string{"score.distance_meter", "score.zone_durations.zone_two_milli"},
		Times:  []string{"start", "end"}, Start: "start", End: "end",
		Enums: []string{"sport_name", "score_state", "timezone_offset"}, TimeLayout: iso,
	}
	bodyShape = synth.Shape{
		// snapshot_date is the connector's own field, always written.
		Required: []string{"height_meter", "weight_kilogram", "max_heart_rate", "snapshot_date"},
		Numbers:  map[string][2]float64{"height_meter": {0.5, 2.5}, "weight_kilogram": {20, 300}, "max_heart_rate": {100, 230}},
	}
)

// sports maps the generator's sports to WHOOP's sport ids and names.
var sports = map[synth.Sport]struct {
	id   int
	name string
}{
	synth.SportRun: {0, "running"}, synth.SportCycle: {1, "cycling"}, synth.SportRow: {18, "rowing"}, synth.SportSwim: {33, "swimming"},
	synth.SportYoga: {44, "yoga"}, synth.SportStrength: {45, "weightlifting"}, synth.SportHike: {52, "hiking/rucking"},
	synth.SportWalk: {63, "walking"}, synth.SportOther: {71, "other"}, synth.SportHIIT: {96, "hiit"},
}

type renderer struct {
	tl     *synth.Timeline
	e      *synth.Emitter
	r      *synth.Rand
	userID int
}

// Lines renders tl as the records the connector stores, one per line.
func Lines(tl *synth.Timeline) []synth.Line {
	g := &renderer{tl: tl, e: synth.NewEmitter(tl, Slug), r: synth.NewRand(tl.Opt.Seed, "whoop"), userID: 10129}
	g.e.UnknownType = "journal"
	g.cycles()
	for _, w := range tl.Recorded() {
		g.workout(w)
	}
	g.body()
	return g.e.Lines()
}

func (g *renderer) offset(t time.Time) string { return g.tl.Local(t).Format("-07:00") }

func (g *renderer) stamp(t time.Time) string { return t.UTC().Format(iso) }

// cycles writes one physiological cycle per night, from falling asleep to the
// next night, its sleep and naps and its recovery. The last cycle is open.
func (g *renderer) cycles() {
	tl := g.tl
	var nights []synth.Sleep
	for _, s := range tl.Sleeps {
		if !s.Nap && tl.Worn(s.Start) {
			nights = append(nights, s)
		}
	}
	for i, n := range nights {
		id := 93845 + i
		start := n.Start
		var end time.Time
		if i+1 < len(nights) {
			end = nights[i+1].Start
		}
		till := end
		if till.IsZero() {
			till = tl.Opt.Now.Add(-time.Hour)
			if last := tl.Days[len(tl.Days)-1].End; till.After(last) {
				till = last
			}
		}
		cyc := synth.Doc{"id": id, "user_id": g.userID, "created_at": g.stamp(start.Add(time.Hour)), "updated_at": g.stamp(till),
			"start": g.stamp(start), "timezone_offset": g.offset(start), "score_state": "SCORED"}
		if !end.IsZero() {
			cyc["end"] = g.stamp(end)
		}
		hrSum, hrN, hrMax := 0.0, 0.0, 0.0
		for t := start; t.Before(till); t = t.Add(5 * time.Minute) {
			if tl.Worn(t) {
				hr := tl.HeartRate(t)
				hrSum += hr
				hrN++
				hrMax = math.Max(hrMax, hr)
			}
		}
		kj := (tl.ActiveEnergy(start, till) + tl.BasalEnergy(start, till)) * 4.184
		if hrN > 0 {
			strain := math.Min(21, 4+tl.ActiveEnergy(start, till)/80)
			cyc["score"] = synth.Doc{"strain": synth.Round(strain, 4), "kilojoule": synth.Round(kj, 3),
				"average_heart_rate": math.Round(hrSum / hrN), "max_heart_rate": hrMax}
			cyc["step_count"] = tl.Steps(start, till)
		} else {
			cyc["score_state"] = "UNSCORABLE"
			cyc["step_count"] = nil
		}
		g.e.Add("cycle", start, cyc, cycleShape)
		sleepID := g.sleep(n, id)
		for _, s := range tl.Sleeps {
			if s.Nap && !s.Start.Before(start) && (end.IsZero() || s.Start.Before(end)) && tl.Worn(s.Start) {
				g.sleep(s, id)
			}
		}
		g.recovery(n, id, sleepID, cyc)
	}
}

func (g *renderer) sleep(s synth.Sleep, cycleID int) string {
	id := strings.ToLower(g.r.UUID())
	ms := func(d time.Duration) float64 { return float64(d.Milliseconds()) }
	need := g.tl.P.SleepH * 3600 * 1000
	disturb := 0
	prev := synth.SleepStage("")
	for _, st := range s.Stages {
		if st.Stage == synth.StageAwake && prev != "" && prev != synth.StageAwake {
			disturb++
		}
		prev = st.Stage
	}
	doc := synth.Doc{
		"id": id, "cycle_id": cycleID, "user_id": g.userID, "created_at": g.stamp(s.End.Add(10 * time.Minute)),
		"updated_at": g.stamp(s.End.Add(20 * time.Minute)), "start": g.stamp(s.Start), "end": g.stamp(s.End),
		"timezone_offset": g.offset(s.Start), "nap": s.Nap, "score_state": "SCORED",
		"score": synth.Doc{
			"stage_summary": synth.Doc{
				"total_in_bed_time_milli": ms(s.End.Sub(s.Start)), "total_awake_time_milli": ms(s.In(synth.StageAwake)),
				"total_no_data_time_milli": 0, "total_light_sleep_time_milli": ms(s.In(synth.StageLight)),
				"total_slow_wave_sleep_time_milli": ms(s.In(synth.StageDeep)), "total_rem_sleep_time_milli": ms(s.In(synth.StageREM)),
				"sleep_cycle_count": math.Max(1, math.Round(s.Asleep().Hours()/1.5)), "disturbance_count": disturb,
			},
			"sleep_needed": synth.Doc{"baseline_milli": need, "need_from_sleep_debt_milli": 352230, "need_from_recent_strain_milli": 208595,
				"need_from_recent_nap_milli": 0},
			"respiratory_rate":             synth.Round(s.Resp, 6),
			"sleep_performance_percentage": math.Min(100, math.Round(ms(s.Asleep())/need*100)),
			"sleep_consistency_percentage": 85 + float64(s.Day%10),
			"sleep_efficiency_percentage":  synth.Round(100*s.Asleep().Seconds()/s.End.Sub(s.Start).Seconds(), 6),
		},
	}
	if s.Nap {
		doc["score"].(synth.Doc)["sleep_needed"].(synth.Doc)["need_from_recent_nap_milli"] = -ms(s.Asleep())
	}
	g.e.Add("sleep", s.Start, doc, sleepShape)
	return id
}

func (g *renderer) recovery(n synth.Sleep, cycleID int, sleepID string, cyc synth.Doc) {
	ref := synth.Doc{"start": cyc["start"], "timezone_offset": cyc["timezone_offset"]}
	if e, ok := cyc["end"]; ok {
		ref["end"] = e
	}
	g.e.Add("recovery", n.End, synth.Doc{
		"cycle_id": cycleID, "sleep_id": sleepID, "user_id": g.userID, "created_at": g.stamp(n.End.Add(15 * time.Minute)),
		"updated_at": g.stamp(n.End.Add(16 * time.Minute)), "score_state": "SCORED",
		"score": synth.Doc{"user_calibrating": n.Day < 4, "recovery_score": float64(n.Score), "resting_heart_rate": n.LowestHR + 2,
			"hrv_rmssd_milli": synth.Round(n.HRV*1.1, 6), "spo2_percentage": synth.Round(n.SpO2, 4),
			"skin_temp_celsius": synth.Round(33.6+n.TempDelta, 2)},
		"cycle": ref,
	}, recoveryShape)
}

func (g *renderer) workout(w synth.Workout) {
	sp := sports[w.Sport]
	hr := g.tl.P
	zone := func(lo, hi float64) float64 {
		var n float64
		for t := w.Start; t.Before(w.End); t = t.Add(5 * time.Second) {
			pct := g.tl.HeartRate(t) / hr.MaxHR
			if pct >= lo && pct < hi {
				n += 5000
			}
		}
		return n
	}
	score := synth.Doc{
		"strain": synth.Round(math.Min(21, 3+w.EnergyKcal/60), 4), "average_heart_rate": w.AvgHR, "max_heart_rate": w.MaxHR,
		"kilojoule": synth.Round(w.EnergyKcal*4.184, 6), "percent_recorded": 100.0,
		"zone_durations": synth.Doc{"zone_zero_milli": zone(0, 0.5), "zone_one_milli": zone(0.5, 0.6), "zone_two_milli": zone(0.6, 0.7),
			"zone_three_milli": zone(0.7, 0.8), "zone_four_milli": zone(0.8, 0.9), "zone_five_milli": zone(0.9, 2)},
	}
	if w.DistanceM > 0 {
		score["distance_meter"] = synth.Round(w.DistanceM, 8)
		score["altitude_gain_meter"] = synth.Round(w.AscentM, 8)
		score["altitude_change_meter"] = synth.Round(-0.78, 8)
	}
	g.e.Add("workout", w.Start, synth.Doc{
		"id": strings.ToLower(g.r.UUID()), "user_id": g.userID, "created_at": g.stamp(w.End.Add(2 * time.Minute)),
		"updated_at": g.stamp(w.End.Add(3 * time.Minute)), "start": g.stamp(w.Start), "end": g.stamp(w.End),
		"timezone_offset": g.offset(w.Start), "sport_name": sp.name, "sport_id": sp.id, "score_state": "SCORED", "score": score,
	}, workoutShape)
}

// body writes the current measurements as the connector stores them: one
// line, dated the day of the pull.
func (g *renderer) body() {
	now := g.tl.Opt.Now.UTC()
	w := g.tl.P.WeightKG
	if n := len(g.tl.Weighs); n > 0 {
		w = g.tl.Weighs[n-1].WeightKG
	}
	g.e.Add("body", now, synth.Doc{"height_meter": synth.Round(g.tl.P.HeightCM/100, 4), "weight_kilogram": synth.Round(w, 4),
		"max_heart_rate": g.tl.P.MaxHR, "snapshot_date": now.Format(time.DateOnly)}, bodyShape)
}
