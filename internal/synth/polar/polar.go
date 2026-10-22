// SPDX-License-Identifier: AGPL-3.0-only

// Package polar renders a synthetic timeline as Polar AccessLink v3
// documents.
//
// Source: the AccessLink OpenAPI document
// (https://www.polar.com/accesslink-api/swagger.yaml), schemas
// exerciseHashId, sleep, nightly-recharge, activity-summary-with-all-samples,
// continuous-heartrate and physical-info. Each line is one item of a list
// response. The connector adds "utc_offset_minutes" to a continuous heart
// rate day when the pull learned the offset, and "fetched_at" to the
// physical information; the lines carry both.
package polar

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "polar"

// Polar's local timestamps have no offset: "2008-10-13T10:40:02".
const localNoZone = "2006-01-02T15:04:05"

var (
	exerciseShape = synth.Shape{
		ID:       "id",
		Required: []string{"id", "start_time", "start_time_utc_offset", "duration", "sport"},
		Optional: []string{"upload_time", "device", "calories", "distance", "heart_rate", "training_load", "has_route",
			"detailed_sport_info", "fat_percentage", "heart_rate_zones", "samples", "route"},
		Numbers: map[string][2]float64{"calories": {0, 2e4}, "distance": {0, 3e5}, "heart_rate.average": {25, 250},
			"start_time_utc_offset": {-720, 840}},
		Counts: []string{"calories", "distance"},
		Times:  []string{"start_time", "upload_time"}, Enums: []string{"sport"}, Strings: []string{"device"},
		TimeLayout: localNoZone,
	}
	sleepShape = synth.Shape{
		Required: []string{"date", "sleep_start_time", "sleep_end_time"},
		Optional: []string{"polar_user", "device_id", "continuity", "continuity_class", "light_sleep", "deep_sleep", "rem_sleep",
			"unrecognized_sleep_stage", "sleep_score", "total_interruption_duration", "sleep_charge", "sleep_goal", "sleep_rating",
			"short_interruption_duration", "long_interruption_duration", "sleep_cycles", "group_duration_score",
			"group_solidity_score", "group_regeneration_score", "hypnogram", "heart_rate_samples"},
		Numbers: map[string][2]float64{"light_sleep": {0, 86400}, "deep_sleep": {0, 86400}, "sleep_score": {1, 100}, "continuity": {1, 5}},
		Counts:  []string{"deep_sleep", "rem_sleep"},
		Times:   []string{"sleep_start_time", "sleep_end_time"}, Start: "sleep_start_time", End: "sleep_end_time",
	}
	rechargeShape = synth.Shape{
		Required: []string{"date"},
		Optional: []string{"polar_user", "heart_rate_avg", "beat_to_beat_avg", "heart_rate_variability_avg", "breathing_rate_avg",
			"nightly_recharge_status", "ans_charge", "ans_charge_status", "hrv_samples", "breathing_samples"},
		Numbers: map[string][2]float64{"heart_rate_avg": {25, 200}, "heart_rate_variability_avg": {1, 300}, "breathing_rate_avg": {4, 40},
			"nightly_recharge_status": {1, 6}},
		Enums: []string{"nightly_recharge_status"},
	}
	activityShape = synth.Shape{
		Required: []string{"start_time", "end_time"},
		Optional: []string{"active_duration", "inactive_duration", "daily_activity", "calories", "active_calories", "steps",
			"inactivity_alert_count", "distance_from_steps", "samples"},
		Numbers: map[string][2]float64{"steps": {0, 1e5}, "calories": {0, 2e4}, "active_calories": {0, 1e4}, "distance_from_steps": {0, 2e5}},
		Counts:  []string{"steps", "active_calories"},
		Times:   []string{"start_time", "end_time"}, Start: "start_time", End: "end_time", TimeLayout: localNoZone,
	}
	hrShape = synth.Shape{
		Required: []string{"date", "heart_rate_samples"},
		Optional: []string{"polar_user", "utc_offset_minutes"},
		Numbers:  map[string][2]float64{"utc_offset_minutes": {-720, 840}},
	}
	physicalShape = synth.Shape{
		Required: []string{"fetched_at"},
		Optional: []string{"weight", "height", "created", "modified", "birthday", "gender", "maximum_heart_rate", "resting_heart_rate",
			"aerobic_threshold", "anaerobic_threshold", "vo2_max", "weight_source", "training_background", "typical_day", "sleep_goal"},
		Numbers: map[string][2]float64{"weight": {20, 300}, "height": {50, 250}, "resting_heart_rate": {25, 150}, "vo2_max": {10, 95}},
		Times:   []string{"created", "modified", "fetched_at"}, Enums: []string{"gender", "weight_source", "training_background", "typical_day"},
	}
)

// sports are Polar sport names; the specification gives only "OTHER" as an
// example and types the field as a string.
var sports = map[synth.Sport][2]string{
	synth.SportRun: {"RUNNING", "RUNNING"}, synth.SportWalk: {"WALKING", "WALKING"}, synth.SportCycle: {"CYCLING", "ROAD_BIKING"},
	synth.SportSwim: {"SWIMMING", "POOL_SWIMMING"}, synth.SportStrength: {"OTHER", "STRENGTH_TRAINING"}, synth.SportYoga: {"OTHER", "YOGA"},
	synth.SportHIIT: {"OTHER", "CIRCUIT_TRAINING"}, synth.SportRow: {"OTHER", "INDOOR_ROWING"}, synth.SportHike: {"HIKING", "HIKING"},
	synth.SportOther: {"OTHER", "OTHER_INDOOR"},
}

const user = "https://www.polaraccesslink.com/v3/users/1"

type renderer struct {
	tl *synth.Timeline
	e  *synth.Emitter
	r  *synth.Rand
}

// Lines renders tl as the items the connector stores, one per line.
func Lines(tl *synth.Timeline) []synth.Line {
	g := &renderer{tl: tl, e: synth.NewEmitter(tl, Slug), r: synth.NewRand(tl.Opt.Seed, "polar")}
	g.e.UnknownType = "cardio_load"
	for _, w := range tl.Recorded() {
		g.exercise(w)
	}
	for _, s := range tl.Sleeps {
		if !s.Nap && tl.Worn(s.Start) {
			g.sleep(s)
			g.recharge(s)
		}
	}
	for _, d := range tl.Days {
		if !d.Missing {
			g.activity(d)
			g.heartRate(d)
		}
	}
	g.physical()
	return g.e.Lines()
}

// duration writes an ISO 8601 duration as Polar does: PT2H44M, PT0S.
func duration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	var b strings.Builder
	b.WriteString("PT")
	if h > 0 {
		fmt.Fprintf(&b, "%dH", h)
	}
	if m > 0 {
		fmt.Fprintf(&b, "%dM", m)
	}
	if s > 0 || (h == 0 && m == 0) {
		fmt.Fprintf(&b, "%dS", s)
	}
	return b.String()
}

func (g *renderer) offsetMin(t time.Time) int {
	_, off := g.tl.Local(t).Zone()
	return off / 60
}

func (g *renderer) exercise(w synth.Workout) {
	tl := g.tl
	sp := sports[w.Sport]
	var hr []string
	for t := w.Start; t.Before(w.End); t = t.Add(5 * time.Second) {
		hr = append(hr, fmt.Sprint(tl.HeartRate(t)))
	}
	zones := []any{}
	for i, lim := range [][2]float64{{0.5, 0.6}, {0.6, 0.7}, {0.7, 0.8}, {0.8, 0.9}, {0.9, 1}} {
		var in time.Duration
		for t := w.Start; t.Before(w.End); t = t.Add(5 * time.Second) {
			p := tl.HeartRate(t) / tl.P.MaxHR
			if p >= lim[0] && p < lim[1] {
				in += 5 * time.Second
			}
		}
		zones = append(zones, synth.Doc{"index": i + 1, "lower-limit": math.Round(lim[0] * tl.P.MaxHR),
			"upper-limit": math.Round(lim[1] * tl.P.MaxHR), "in-zone": duration(in)})
	}
	doc := synth.Doc{
		"id": strings.ToUpper(g.r.Hex(7)), "upload_time": w.End.Add(20 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z"),
		"polar_user": user, "device": "Polar Vantage V3", "device_id": "1111AAAA",
		"start_time": tl.Local(w.Start).Format(localNoZone), "start_time_utc_offset": g.offsetMin(w.Start),
		"duration": duration(w.End.Sub(w.Start)), "calories": math.Round(w.EnergyKcal),
		"heart_rate": synth.Doc{"average": w.AvgHR, "maximum": w.MaxHR}, "training_load": synth.Round(w.EnergyKcal/4, 2),
		"sport": sp[0], "detailed_sport_info": sp[1], "has_route": !w.Indoor && w.Sport.Outdoor(),
		"fat_percentage": 55, "carbohydrate_percentage": 43, "protein_percentage": 2,
		"heart_rate_zones": zones,
		"samples":          []any{synth.Doc{"recording-rate": 5, "sample-type": "0", "data": strings.Join(hr, ",")}},
	}
	if w.DistanceM > 0 {
		doc["distance"] = synth.Round(w.DistanceM, 1)
	}
	if doc["has_route"] == true {
		var route []any
		for t := w.Start; t.Before(w.End); t = t.Add(30 * time.Second) {
			lat, lon, _, _ := tl.Route(w, t)
			route = append(route, synth.Doc{"latitude": lat, "longitude": lon,
				"time": fmt.Sprintf("PT%.3fS", t.Sub(w.Start).Seconds()), "satellites": 9, "fix": 3})
		}
		doc["route"] = route
	}
	g.e.Add("exercise", w.Start, doc, exerciseShape)
}

// hypno is Polar's 30 second hypnogram code: 0 wake, 1 REM, 3 light, 4 deep.
func hypno(s synth.SleepStage) int {
	switch s {
	case synth.StageREM:
		return 1
	case synth.StageLight:
		return 3
	case synth.StageDeep:
		return 4
	}
	return 0
}

func (g *renderer) sleep(s synth.Sleep) {
	tl := g.tl
	hyp := synth.Doc{}
	prev := -1
	for _, st := range s.Stages {
		if c := hypno(st.Stage); c != prev {
			hyp[tl.Local(st.Start).Format("15:04")] = c
			prev = c
		}
	}
	hr := synth.Doc{}
	for t := s.Start; t.Before(s.End); t = t.Add(5 * time.Minute) {
		hr[tl.Local(t).Format("15:04")] = tl.HeartRate(t)
	}
	awake := s.In(synth.StageAwake).Seconds()
	g.e.Add("sleep", s.End, synth.Doc{
		"polar_user": user, "date": tl.Days[s.Day].Date,
		"sleep_start_time": tl.LocalISO(s.Start), "sleep_end_time": tl.LocalISO(s.End), "device_id": "1111AAAA",
		"continuity": synth.Round(3.2+float64(s.Score%10)/10, 1), "continuity_class": 3,
		"light_sleep": math.Round(s.In(synth.StageLight).Seconds()), "deep_sleep": math.Round(s.In(synth.StageDeep).Seconds()),
		"rem_sleep": math.Round(s.In(synth.StageREM).Seconds()), "unrecognized_sleep_stage": 0, "sleep_score": s.Score,
		"total_interruption_duration": math.Round(awake), "sleep_charge": 3, "sleep_goal": 28800, "sleep_rating": 3,
		"short_interruption_duration": math.Round(awake * 0.6), "long_interruption_duration": math.Round(awake * 0.4),
		"sleep_cycles": math.Max(1, math.Round(s.Asleep().Hours()/1.5)), "group_duration_score": 100.0,
		"group_solidity_score": 75.0, "group_regeneration_score": 54.2, "hypnogram": hyp, "heart_rate_samples": hr,
	}, sleepShape)
}

func (g *renderer) recharge(s synth.Sleep) {
	tl := g.tl
	hrv, br := synth.Doc{}, synth.Doc{}
	from := s.Start.Add(30 * time.Minute)
	for t := from; t.Before(from.Add(4*time.Hour)) && t.Before(s.End); t = t.Add(5 * time.Minute) {
		k := tl.Local(t).Format("15:04")
		hrv[k] = math.Round(s.HRV + 5*math.Sin(float64(t.Unix())/1200))
		br[k] = synth.Round(s.Resp+0.3*math.Sin(float64(t.Unix())/900), 1)
	}
	g.e.Add("nightly_recharge", s.End, synth.Doc{
		"polar_user": user, "date": tl.Days[s.Day].Date, "heart_rate_avg": math.Round(s.AvgHR),
		"beat_to_beat_avg": math.Round(60000 / s.AvgHR), "heart_rate_variability_avg": math.Round(s.HRV),
		"breathing_rate_avg": synth.Round(s.Resp, 1), "nightly_recharge_status": 1 + s.Score%6,
		"ans_charge": synth.Round(float64(s.Score-70)/3, 1), "ans_charge_status": 3, "hrv_samples": hrv, "breathing_samples": br,
	}, rechargeShape)
}

func (g *renderer) activity(d synth.Day) {
	tl := g.tl
	t := tl.Totals(d)
	var steps, zones []any
	for m := d.Start; m.Before(d.End); m = m.Add(5 * time.Minute) {
		n := tl.Steps(m, m.Add(5*time.Minute))
		steps = append(steps, synth.Doc{"steps": n, "timestamp": tl.Local(m).Format("2006-01-02T15:04")})
	}
	for m := d.Start; m.Before(d.End); m = m.Add(30 * time.Minute) {
		z := "SEDENTARY"
		switch {
		case !tl.Worn(m):
			continue
		case func() bool { _, ok := tl.SleepAt(m); return ok }():
			z = "SLEEP"
		case func() bool { w, ok := tl.WorkoutAt(m); return ok && w.Sport.Intensity() >= 0.75 }():
			z = "VIGOROUS"
		case func() bool { _, ok := tl.WorkoutAt(m); return ok }():
			z = "MODERATE"
		case tl.Steps(m, m.Add(30*time.Minute)) > 300:
			z = "LIGHT"
		}
		zones = append(zones, synth.Doc{"zone": z, "timestamp": tl.Local(m).Format(localNoZone)})
	}
	active := time.Duration(t.LightMin+t.ModerateMin+t.VigorousMin) * time.Minute
	end := d.End.Add(-time.Second)
	g.e.Add("activity", d.Start, synth.Doc{
		"start_time": d.Start.UTC().Format(localNoZone), "end_time": end.UTC().Format(localNoZone),
		"active_duration": duration(active), "inactive_duration": duration(time.Duration(t.SedentaryMin) * time.Minute),
		"daily_activity": synth.Round(math.Min(100, t.Steps/100), 1), "calories": math.Round(t.ActiveKcal + t.BasalKcal),
		"active_calories": math.Round(t.ActiveKcal), "steps": t.Steps, "inactivity_alert_count": int(t.SedentaryMin / 240),
		"distance_from_steps": synth.Round(t.DistanceM, 2),
		"samples": synth.Doc{"date": d.Date,
			"steps":             synth.Doc{"interval_ms": 300000, "total_steps": t.Steps, "samples": steps},
			"activity_zones":    synth.Doc{"samples": zones},
			"inactivity_stamps": []any{}},
	}, activityShape)
}

func (g *renderer) heartRate(d synth.Day) {
	tl := g.tl
	var rows []any
	step := time.Duration(float64(5*time.Minute) / tl.Opt.Volume)
	if step < time.Second {
		step = time.Second
	}
	for _, t := range tl.Every(d.Start, d.End, step) {
		rows = append(rows, synth.Doc{"heart_rate": tl.HeartRate(t), "sample_time": tl.Local(t).Format("15:04:05")})
	}
	if len(rows) == 0 {
		return
	}
	g.e.Add("continuous_heart_rate", d.Start, synth.Doc{"polar_user": user, "date": d.Date, "heart_rate_samples": rows,
		"utc_offset_minutes": g.offsetMin(d.Start.Add(12 * time.Hour))}, hrShape)
}

func (g *renderer) physical() {
	tl := g.tl
	now := tl.Opt.Now.UTC()
	w := tl.P.WeightKG
	if n := len(tl.Weighs); n > 0 {
		w = tl.Weighs[n-1].WeightKG
	}
	g.e.Add("physical_info", now, synth.Doc{
		"weight": synth.Round(w, 1), "height": tl.P.HeightCM, "created": now.AddDate(-1, 0, 0).Format(time.RFC3339),
		"modified": now.Add(-48 * time.Hour).Format(time.RFC3339), "birthday": tl.P.BirthDate, "gender": strings.ToUpper(tl.P.Sex),
		"maximum_heart_rate": tl.P.MaxHR, "resting_heart_rate": tl.P.RestingHR, "aerobic_threshold": math.Round(tl.P.MaxHR * 0.75),
		"anaerobic_threshold": math.Round(tl.P.MaxHR * 0.9), "vo2_max": tl.P.VO2Max, "weight_source": "SOURCE_MEASURED",
		"training_background": "REGULAR", "typical_day": "MOSTLY_STANDING", "sleep_goal": "PT8H",
		"fetched_at": now.Format(time.RFC3339),
	}, physicalShape)
}
