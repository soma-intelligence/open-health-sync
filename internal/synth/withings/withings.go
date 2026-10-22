// SPDX-License-Identifier: AGPL-3.0-only

// Package withings renders a synthetic timeline as Withings Public API
// documents.
//
// Source: the Withings API OpenAPI document
// (https://developer.withings.com/api-reference/openapi.yaml): schemas
// measuregrp_object and measure_object with the meastype table of Measure
// Getmeas, activity_object (Measure v2 Getactivity), the series object of
// Measure v2 Getintradayactivity, sleep_summary_object (Sleep v2 Getsummary)
// with the state table of Sleep v2 Get, workout_object with its category
// table (Measure v2 Getworkouts), and heart_measurement_object with the body
// of Heart v2 Get. Each line is one item, with what the connector adds:
// the response's "timezone" on a measure group, the series key as
// "timestamp" on an intraday entry, the night's stages as "sleep_phases" and
// the waveform as "signal" on an ECG.
package withings

import (
	"math"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "withings"

var (
	measureShape = synth.Shape{
		ID:       "grpid",
		Required: []string{"grpid", "attrib", "date", "category", "measures"},
		Optional: []string{"created", "modified", "deviceid", "hash_deviceid", "model", "model_id", "comment", "timezone"},
		Numbers:  map[string][2]float64{"date": {1e9, 2e9}},
		Epoch:    true, Times: []string{"date"}, Enums: []string{"attrib", "category", "timezone"}, Strings: []string{"model"},
	}
	activityShape = synth.Shape{
		Required: []string{"date", "timezone"},
		Optional: []string{"deviceid", "hash_deviceid", "brand", "is_tracker", "modified", "model", "modelid", "steps", "distance",
			"elevation", "soft", "moderate", "intense", "active", "calories", "totalcalories", "hr_average", "hr_min", "hr_max",
			"hr_zone_0", "hr_zone_1", "hr_zone_2", "hr_zone_3"},
		Numbers: map[string][2]float64{"steps": {0, 1e5}, "distance": {0, 2e5}, "calories": {0, 1e4}, "totalcalories": {0, 2e4},
			"hr_average": {25, 250}},
		Counts: []string{"steps", "distance", "soft"},
		Enums:  []string{"timezone"},
	}
	intradayShape = synth.Shape{
		Required: []string{"timestamp"},
		Optional: []string{"deviceid", "model", "model_id", "steps", "elevation", "calories", "distance", "heart_rate", "duration"},
		Numbers:  map[string][2]float64{"steps": {0, 500}, "heart_rate": {25, 250}, "calories": {0, 100}},
		Counts:   []string{"steps"},
		Epoch:    true, Times: []string{"timestamp"},
	}
	sleepShape = synth.Shape{
		ID:       "id",
		Required: []string{"id", "startdate", "enddate", "date", "data"},
		Optional: []string{"timezone", "model", "model_id", "created", "modified", "hash_deviceid", "completed", "sleep_phases",
			"data.sleep_score", "data.hr_average", "data.rr_average"},
		Numbers: map[string][2]float64{"data.deepsleepduration": {0, 86400}, "data.hr_average": {25, 200}, "data.sleep_score": {0, 100},
			"data.sleep_efficiency": {0, 1}},
		Counts: []string{"data.deepsleepduration", "data.wakeupcount"},
		Epoch:  true, Times: []string{"startdate", "enddate"}, Start: "startdate", End: "enddate", Enums: []string{"timezone", "model"},
	}
	workoutShape = synth.Shape{
		ID:       "id",
		Required: []string{"id", "category", "startdate", "enddate", "date"},
		Optional: []string{"timezone", "model", "attrib", "modified", "deviceid", "data", "data.distance", "data.calories"},
		Numbers: map[string][2]float64{"data.calories": {0, 2e4}, "data.distance": {0, 3e5}, "data.hr_average": {25, 250},
			"data.steps": {0, 1e5}},
		Counts: []string{"data.calories", "data.distance", "data.steps"},
		Epoch:  true, Times: []string{"startdate", "enddate"}, Start: "startdate", End: "enddate", Enums: []string{"category", "timezone"},
	}
	heartShape = synth.Shape{
		Required: []string{"timestamp", "ecg"},
		Optional: []string{"deviceid", "model", "heart_rate", "modified", "bloodpressure", "signal"},
		Numbers:  map[string][2]float64{"heart_rate": {25, 250}, "signal.sampling_frequency": {1, 2000}},
		Epoch:    true, Times: []string{"timestamp"}, Enums: []string{"ecg.afib", "model"},
	}
)

// Measure types from the meastype table.
const (
	typeWeight      = 1
	typeHeight      = 4
	typeFatFree     = 5
	typeFatRatio    = 6
	typeFatMass     = 8
	typeDiastolic   = 9
	typeSystolic    = 10
	typePulse       = 11
	typeSpO2        = 54
	typeBodyTemp    = 71
	typeSkinTemp    = 73
	typeMuscle      = 76
	typeHydration   = 77
	typeBone        = 88
	typePWV         = 91
	typeVO2         = 123
	typeVisceralFat = 170
	typeBMR         = 226
	typeMetabolic   = 227
)

// categories maps the generator's sports to the workout category table.
var categories = map[synth.Sport]int{
	synth.SportWalk: 1, synth.SportRun: 2, synth.SportHike: 3, synth.SportCycle: 6, synth.SportSwim: 7, synth.SportStrength: 16,
	synth.SportHIIT: 17, synth.SportYoga: 28, synth.SportOther: 36, synth.SportRow: 187,
}

type renderer struct {
	tl  *synth.Timeline
	e   *synth.Emitter
	r   *synth.Rand
	grp int64
}

// Lines renders tl as the items the connector stores, one per line.
func Lines(tl *synth.Timeline) []synth.Line {
	g := &renderer{tl: tl, e: synth.NewEmitter(tl, Slug), r: synth.NewRand(tl.Opt.Seed, "withings"), grp: 5586822735}
	g.e.UnknownType = "stetho"
	for _, w := range tl.Weighs {
		g.weigh(w)
	}
	for _, b := range tl.BPs {
		g.bp(b)
	}
	g.extras()
	for _, d := range tl.Days {
		if !d.Missing {
			g.activity(d)
			g.intraday(d)
		}
	}
	for _, s := range tl.Sleeps {
		if !s.Nap && tl.Worn(s.Start) {
			g.sleep(s)
		}
	}
	for _, w := range tl.Recorded() {
		g.workout(w)
	}
	for _, x := range tl.ECGs {
		if tl.Worn(x.Start) {
			g.ecg(x)
		}
	}
	return g.e.Lines()
}

func (g *renderer) zone(t time.Time) string { return g.tl.ZoneAt(t).String() }

// measure writes a value as Withings does: an integer and a power of ten.
func measure(typ int, v float64, digits int) synth.Doc {
	p := math.Pow10(digits)
	return synth.Doc{"value": math.Round(v * p), "type": typ, "unit": -digits, "algo": 0, "fm": 3, "position": nil}
}

func (g *renderer) group(at time.Time, category, attrib int, model string, modelID int, measures ...synth.Doc) {
	g.grp++
	ms := make([]any, len(measures))
	for i, m := range measures {
		ms[i] = m
	}
	dev := g.r.Hex(40)
	g.e.Add("measure", at, synth.Doc{
		"grpid": g.grp, "attrib": attrib, "date": at.Unix(), "created": at.Add(30 * time.Second).Unix(),
		"modified": at.Add(30 * time.Second).Unix(), "category": category, "deviceid": dev, "hash_deviceid": dev,
		"measures": ms, "model": model, "model_id": modelID, "comment": nil, "timezone": g.zone(at),
	}, measureShape)
}

func (g *renderer) weigh(w synth.Weigh) {
	fatKG := w.WeightKG * w.BodyFat
	g.group(w.At, 1, 0, "Body Comp", 13,
		measure(typeWeight, w.WeightKG, 3), measure(typeFatFree, w.LeanKG, 3), measure(typeFatRatio, w.BodyFat*100, 3),
		measure(typeFatMass, fatKG, 3), measure(typeMuscle, w.MuscleKG, 2), measure(typeHydration, w.WaterKG, 2),
		measure(typeBone, w.BoneKG, 2), measure(typeVisceralFat, w.VisceralF, 0), measure(typePWV, 7.1, 1),
		measure(typeBMR, g.tl.BasalEnergy(w.At, w.At.Add(24*time.Hour)), 0), measure(typeMetabolic, float64(g.tl.Age(w.At)-2), 0),
		measure(typePulse, g.tl.P.RestingHR+6, 0))
}

func (g *renderer) bp(b synth.BP) {
	g.group(b.At, 1, 0, "BPM Connect", 45, measure(typeDiastolic, b.Diastolic, 0), measure(typeSystolic, b.Systolic, 0),
		measure(typePulse, b.Pulse, 0))
}

// extras writes the measures a household takes now and then: a height
// entered by hand, a thermometer reading, a SpO2 spot check, a VO2 max.
func (g *renderer) extras() {
	tl := g.tl
	if len(tl.Days) == 0 {
		return
	}
	d := tl.Days[0]
	g.group(synth.AtLocal(d, 9), 1, 2, "", 0, measure(typeHeight, tl.P.HeightCM/100, 2))
	for i, day := range tl.Days {
		if day.Missing || i%3 != 0 {
			continue
		}
		g.group(synth.AtLocal(day, 8.2), 1, 0, "Thermo", 70, measure(typeBodyTemp, 36.6+0.2*math.Sin(float64(i)), 2),
			measure(typeSkinTemp, 33.8, 2))
		g.group(synth.AtLocal(day, 10.4), 1, 0, "ScanWatch 2", 94, measure(typeSpO2, tl.SpO2(synth.AtLocal(day, 10.4))*100, 0),
			measure(typeVO2, tl.P.VO2Max, 0))
	}
}

func (g *renderer) activity(d synth.Day) {
	t := g.tl.Totals(d)
	dev := g.r.Hex(40)
	g.e.Add("activity", d.Start, synth.Doc{
		"date": d.Date, "timezone": d.Loc.String(), "deviceid": dev, "hash_deviceid": dev, "brand": 18, "is_tracker": true,
		"modified": d.End.Unix(), "model": "ScanWatch 2", "modelid": 94, "steps": t.Steps, "distance": t.DistanceM,
		"elevation": t.FloorsUp, "soft": t.LightMin * 60, "moderate": t.ModerateMin * 60, "intense": t.VigorousMin * 60,
		"active": (t.ModerateMin + t.VigorousMin) * 60, "calories": t.ActiveKcal, "totalcalories": synth.Round(t.ActiveKcal+t.BasalKcal, 1),
		"hr_average": t.AvgHR, "hr_min": t.MinHR, "hr_max": t.MaxHR,
		"hr_zone_0": 36000, "hr_zone_1": t.ModerateMin * 60, "hr_zone_2": t.VigorousMin * 60, "hr_zone_3": 0,
	}, activityShape)
}

// intraday writes one entry a minute with steps or a heart rate, as a
// tracker does.
func (g *renderer) intraday(d synth.Day) {
	tl := g.tl
	step := time.Duration(float64(10*time.Minute) / tl.Opt.Volume)
	if step < time.Minute {
		step = time.Minute
	}
	for _, t := range tl.Every(d.Start, d.End, step) {
		doc := synth.Doc{"timestamp": t.Unix(), "deviceid": "892359876fd8805ac45bab078c4828692f0276b1", "model": "ScanWatch 2", "model_id": 94}
		if n := tl.Steps(t, t.Add(time.Minute)); n > 0 {
			doc["steps"] = n
			doc["duration"] = 60
			doc["calories"] = synth.Round(tl.ActiveEnergy(t, t.Add(time.Minute)), 2)
			doc["distance"] = tl.Distance(t, t.Add(time.Minute))
		} else {
			doc["heart_rate"] = tl.HeartRate(t)
			doc["duration"] = 60
		}
		g.e.Add("intraday", t, doc, intradayShape)
	}
}

// state is the Sleep v2 Get state: 0 awake, 1 light, 2 deep, 3 REM.
func state(s synth.SleepStage) int {
	switch s {
	case synth.StageLight:
		return 1
	case synth.StageDeep:
		return 2
	case synth.StageREM:
		return 3
	}
	return 0
}

func (g *renderer) sleep(s synth.Sleep) {
	tl := g.tl
	var phases []any
	for _, st := range s.Stages {
		phases = append(phases, synth.Doc{"startdate": st.Start.Unix(), "enddate": st.End.Unix(), "state": state(st.Stage)})
	}
	inBed := s.End.Sub(s.Start).Seconds()
	lat := 0.0
	for _, st := range s.Stages {
		if st.Stage != synth.StageAwake {
			lat = st.Start.Sub(s.Start).Seconds()
			break
		}
	}
	wake := 0
	for i := 1; i < len(s.Stages); i++ {
		if s.Stages[i].Stage == synth.StageAwake {
			wake++
		}
	}
	g.e.Add("sleep", s.Start, synth.Doc{
		"id": 12345 + s.Day, "timezone": g.zone(s.Start), "model": 32, "model_id": 63, "startdate": s.Start.Unix(),
		"enddate": s.End.Unix(), "date": tl.Days[s.Day].Date, "created": s.End.Add(time.Hour).Unix(),
		"modified": s.End.Add(time.Hour).Unix(), "hash_deviceid": g.r.Hex(40), "completed": true,
		"data": synth.Doc{
			"total_timeinbed": math.Round(inBed), "total_sleep_time": math.Round(s.Asleep().Seconds()),
			"lightsleepduration": math.Round(s.In(synth.StageLight).Seconds()), "remsleepduration": math.Round(s.In(synth.StageREM).Seconds()),
			"deepsleepduration": math.Round(s.In(synth.StageDeep).Seconds()), "sleep_efficiency": synth.Round(s.Asleep().Seconds()/inBed, 2),
			"sleep_latency": lat, "wakeup_latency": 300, "wakeupduration": math.Round(s.In(synth.StageAwake).Seconds()),
			"wakeupcount": wake, "waso": math.Round(s.In(synth.StageAwake).Seconds() - lat), "nb_rem_episodes": 4,
			"out_of_bed_count": 1, "hr_average": math.Round(s.AvgHR), "hr_min": s.LowestHR, "hr_max": math.Round(s.AvgHR + 14),
			"rr_average": math.Round(s.Resp), "rr_min": math.Round(s.Resp - 2), "rr_max": math.Round(s.Resp + 3),
			"breathing_disturbances_intensity": 8, "snoring": 600, "snoringepisodecount": 3, "sleep_score": s.Score,
			"apnea_hypopnea_index": 4, "mvt_score_avg": 20, "rmssd_start_avg": synth.Round(s.HRV*0.9, 1),
			"rmssd_end_avg": synth.Round(s.HRV*1.1, 1),
		},
		"sleep_phases": phases,
	}, sleepShape)
}

func (g *renderer) workout(w synth.Workout) {
	var pause float64
	for _, p := range w.Pauses {
		pause += p.End.Sub(p.Start).Seconds()
	}
	data := synth.Doc{"calories": math.Round(w.EnergyKcal), "hr_average": w.AvgHR, "hr_min": math.Round(g.tl.P.RestingHR + 20),
		"hr_max": w.MaxHR, "hr_zone_0": 300, "hr_zone_1": 600, "hr_zone_2": 900, "hr_zone_3": 300, "intensity": 50,
		"pause_duration": pause, "algo_pause_duration": pause, "spo2_average": nil, "manual_distance": 0, "manual_calories": 0}
	if w.Sport == synth.SportSwim {
		data["pool_laps"] = math.Round(w.DistanceM / 25)
		data["pool_length"] = 25
		data["strokes"] = math.Round(w.DistanceM / 1.6)
	} else {
		data["distance"] = math.Round(w.DistanceM)
		data["elevation"] = math.Round(w.AscentM / 3)
		data["steps"] = math.Round(w.Steps)
	}
	g.e.Add("workout", w.Start, synth.Doc{
		"id": 12345 + w.Day*10 + int(w.Start.Unix()%10), "category": categories[w.Sport], "timezone": g.zone(w.Start), "model": 94,
		"attrib": 0, "startdate": w.Start.Unix(), "enddate": w.End.Unix(), "date": g.tl.Days[w.Day].Date,
		"modified": w.End.Add(time.Hour).Unix(), "deviceid": g.r.Hex(40), "data": data,
	}, workoutShape)
}

func (g *renderer) ecg(x synth.ECG) {
	afib := 0
	if x.Classification == "atrialFibrillation" {
		afib = 1
	} else if x.Classification != "sinusRhythm" {
		afib = 2
	}
	// The Move ECG records at 300 Hz; the trace is resampled from the
	// generator's 512 Hz.
	const hz = 300
	n := len(x.MicroVolts) * hz / x.HZ
	if g.tl.Opt.Volume < 60 {
		n /= 4
	}
	sig := make([]any, n)
	for i := range sig {
		sig[i] = math.Round(x.MicroVolts[i*x.HZ/hz])
	}
	g.e.Add("ecg", x.Start, synth.Doc{
		"deviceid": g.r.Hex(40), "model": 91, "ecg": synth.Doc{"signalid": 48 + int(x.Start.Unix()%1000), "afib": afib},
		"heart_rate": x.AvgHR, "modified": x.Start.Add(time.Minute).Unix(), "timestamp": x.Start.Unix(),
		"signal": synth.Doc{"signal": sig, "sampling_frequency": hz, "wearposition": 1},
	}, heartShape)
}
