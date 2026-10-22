// SPDX-License-Identifier: AGPL-3.0-only

// Package healthconnect renders a synthetic timeline as the Health Connect
// upload lines: one Android Health Connect record per line, as the phone app
// sends them to POST /v1/ingest/health-connect.
//
// Source: the androidx.health.connect.client reference
// (https://developer.android.com/reference/kotlin/androidx/health/connect/client/records/package-summary),
// its record classes for the value fields and their units, the STAGE_TYPE_,
// EXERCISE_TYPE_, SPECIMEN_SOURCE_, RELATION_TO_MEAL_, MEAL_TYPE_,
// BODY_POSITION_, MEASUREMENT_LOCATION_ and FLOW_ constants, and the
// metadata classes (Metadata recording methods, Device types). Times are
// written as Instant.toString and ZoneOffset.toString write them, so a zero
// offset is "Z". The line envelope (recordType without "Record", metadata,
// enum constants lower case without their prefix, Deletion lines) is the
// upload contract of docs/connectors/health-connect.md.
//
// Apps write to Health Connect under their package name: the lines use
// Samsung Health, Mi Fitness, Zepp, RingConn, Oura, Withings, OMRON connect
// and FreeStyle Libre, so the Xiaomi, Zepp and RingConn sources are covered
// here.
package healthconnect

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "health_connect"

// Package names of the apps that write the records.
const (
	samsung  = "com.sec.android.app.shealth"
	miFit    = "com.xiaomi.wearable"
	zepp     = "com.huami.watch.hmwatchmanager"
	ringconn = "com.gdjztech.ringconn"
	oura     = "com.ouraring.oura"
	withings = "com.withings.wiscale2"
	omron    = "jp.co.omron.healthcare.omron_connect"
	libre    = "com.freestylelibre.app.de"
	mfp      = "com.myfitnesspal.android"
)

// instant writes t as java.time.Instant.toString does: UTC, a Z, and the
// fraction in groups of three digits only when there is one.
func instant(t time.Time) string {
	t = t.UTC()
	s := t.Format("2006-01-02T15:04:05")
	switch ns := t.Nanosecond(); {
	case ns == 0:
	case ns%1e6 == 0:
		s += fmt.Sprintf(".%03d", ns/1e6)
	case ns%1e3 == 0:
		s += fmt.Sprintf(".%06d", ns/1e3)
	default:
		s += fmt.Sprintf(".%09d", ns)
	}
	return s + "Z"
}

// zoneOffset writes the offset at t as java.time.ZoneOffset.toString does.
func zoneOffset(tl *synth.Timeline, t time.Time) string {
	_, off := tl.Local(t).Zone()
	if off == 0 {
		return "Z"
	}
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	return fmt.Sprintf("%s%02d:%02d", sign, off/3600, off%3600/60)
}

// shape builds a record's shape: the envelope fields plus its value fields.
func shape(interval bool, numbers map[string][2]float64, enums ...string) synth.Shape {
	sh := synth.Shape{
		ID:       "metadata.id",
		Required: []string{"recordType", "metadata.id"},
		Optional: []string{"metadata.dataOrigin", "metadata.lastModifiedTime", "metadata.recordingMethod", "metadata.device"},
		Numbers:  numbers,
		Enums:    append([]string{"metadata.recordingMethod", "metadata.device.type"}, enums...),
		Strings:  []string{"metadata.device.model"},
	}
	if interval {
		sh.Required = append(sh.Required, "startTime", "endTime")
		sh.Optional = append(sh.Optional, "startZoneOffset", "endZoneOffset")
		sh.Times, sh.Start, sh.End = []string{"startTime", "endTime"}, "startTime", "endTime"
	} else {
		sh.Required = append(sh.Required, "time")
		sh.Optional = append(sh.Optional, "zoneOffset")
		sh.Times = []string{"time"}
	}
	// In key order, so the field list is the same on every run.
	for _, k := range slices.Sorted(maps.Keys(numbers)) {
		if !strings.Contains(k, ".") {
			sh.Required = append(sh.Required, k)
		}
	}
	sh.TimeLayout = "2006-01-02T15:04:05Z07:00"
	return sh
}

var shapes = map[string]synth.Shape{
	"Steps":                     shape(true, map[string][2]float64{"count": {0, 1e6}}),
	"Distance":                  shape(true, map[string][2]float64{"distanceMeters": {0, 1e6}}),
	"ActiveCaloriesBurned":      shape(true, map[string][2]float64{"energyKilocalories": {0, 1e5}}),
	"TotalCaloriesBurned":       shape(true, map[string][2]float64{"energyKilocalories": {0, 1e5}}),
	"HeartRate":                 shape(true, map[string][2]float64{"samples.0.beatsPerMinute": {1, 300}}),
	"RestingHeartRate":          shape(false, map[string][2]float64{"beatsPerMinute": {1, 300}}),
	"HeartRateVariabilityRmssd": shape(false, map[string][2]float64{"heartRateVariabilityMillis": {1, 200}}),
	"OxygenSaturation":          shape(false, map[string][2]float64{"percentage": {0, 100}}),
	"RespiratoryRate":           shape(false, map[string][2]float64{"rate": {0, 1000}}),
	"BodyTemperature":           shape(false, map[string][2]float64{"temperatureCelsius": {0, 100}}, "measurementLocation"),
	"BloodPressure": shape(false, map[string][2]float64{"systolicMillimetersOfMercury": {20, 200},
		"diastolicMillimetersOfMercury": {10, 180}}, "bodyPosition", "measurementLocation"),
	"BloodGlucose": shape(false, map[string][2]float64{"levelMillimolesPerLiter": {0, 50}},
		"specimenSource", "mealType", "relationToMeal"),
	"Weight":             shape(false, map[string][2]float64{"weightKilograms": {0, 1000}}),
	"Height":             shape(false, map[string][2]float64{"heightMeters": {0, 3}}),
	"BodyFat":            shape(false, map[string][2]float64{"percentage": {0, 100}}),
	"BasalMetabolicRate": shape(false, map[string][2]float64{"basalMetabolicRateKilocaloriesPerDay": {0, 10000}}),
	"SleepSession":       shape(true, nil, "stages.0.stage"),
	"ExerciseSession":    shape(true, nil, "exerciseType"),
	"MenstruationFlow":   shape(false, nil, "flow"),
	"Nutrition": shape(true, map[string][2]float64{"energyKilocalories": {0, 100000}, "proteinGrams": {0, 100000},
		"totalCarbohydrateGrams": {0, 100000}, "sodiumGrams": {0, 100000}}, "mealType"),
	"Hydration": shape(true, map[string][2]float64{"volumeLiters": {0, 100}}),
}

// The ranges above are the ones the record classes document (HeartRate
// 1 to 300 bpm, OxygenSaturation 0 to 100 %, Weight 0 to 1000 kg, ...).

// sports maps the generator's sports to EXERCISE_TYPE_ constants.
var sports = map[synth.Sport]string{
	synth.SportRun: "running", synth.SportWalk: "walking", synth.SportCycle: "biking", synth.SportSwim: "swimming_pool",
	synth.SportStrength: "strength_training", synth.SportYoga: "yoga", synth.SportHIIT: "high_intensity_interval_training",
	synth.SportRow: "rowing_machine", synth.SportHike: "hiking", synth.SportOther: "other_workout",
}

// stages maps the generator's stages to STAGE_TYPE_ constants.
var stages = map[synth.SleepStage]string{
	synth.StageAwake: "awake", synth.StageLight: "light", synth.StageDeep: "deep", synth.StageREM: "rem",
}

type renderer struct {
	tl *synth.Timeline
	e  *synth.Emitter
	r  *synth.Rand
	n  int
}

// meta builds a record's metadata.
func (g *renderer) meta(origin, method, devType, maker, model string, modified time.Time) synth.Doc {
	g.n++
	m := synth.Doc{"id": g.r.UUID(), "dataOrigin": origin, "lastModifiedTime": instant(modified), "recordingMethod": method}
	if devType != "" {
		m["device"] = synth.Doc{"type": devType, "manufacturer": maker, "model": model}
	}
	return m
}

func (g *renderer) instantRec(kind string, t time.Time, m synth.Doc, vals synth.Doc) synth.Doc {
	d := synth.Doc{"recordType": kind, "metadata": m, "time": instant(t), "zoneOffset": zoneOffset(g.tl, t)}
	for k, v := range vals {
		d[k] = v
	}
	g.e.Add(kind, t, d, shapes[kind])
	return d
}

func (g *renderer) intervalRec(kind string, a, b time.Time, m synth.Doc, vals synth.Doc) synth.Doc {
	d := synth.Doc{"recordType": kind, "metadata": m, "startTime": instant(a), "endTime": instant(b),
		"startZoneOffset": zoneOffset(g.tl, a), "endZoneOffset": zoneOffset(g.tl, b)}
	for k, v := range vals {
		d[k] = v
	}
	g.e.Add(kind, a, d, shapes[kind])
	return d
}

// watch is the wrist device writing the activity records.
type watch struct{ origin, devType, maker, model string }

// Lines renders tl as Health Connect upload lines.
func Lines(tl *synth.Timeline) []synth.Line {
	g := &renderer{tl: tl, e: synth.NewEmitter(tl, Slug), r: synth.NewRand(tl.Opt.Seed, "health_connect")}
	g.e.UnknownType = "MindfulnessSession"
	now := tl.Opt.Now
	watches := []watch{{samsung, "watch", "Samsung", "Galaxy Watch7"}}
	if tl.Opt.Has(synth.MultiDevice) {
		// A Xiaomi band and a Zepp watch writing the same minutes, and a
		// ring for the heart rate.
		watches = append(watches, watch{miFit, "fitness_band", "Xiaomi", "Smart Band 9"}, watch{zepp, "watch", "Amazfit", "Balance"})
	}
	win := tl.Window()
	hours := tl.Every(win.Start, win.End, time.Hour)
	for _, w := range watches {
		for _, a := range hours {
			b := a.Add(time.Hour)
			if b.After(now) || !tl.Worn(a) {
				continue
			}
			m := func() synth.Doc {
				return g.meta(w.origin, "automatic", w.devType, w.maker, w.model, b.Add(10*time.Minute))
			}
			if s := tl.Steps(a, b); s > 0 {
				g.intervalRec("Steps", a, b, m(), synth.Doc{"count": int64(s)})
				g.intervalRec("Distance", a, b, m(), synth.Doc{"distanceMeters": synth.Round(tl.Distance(a, b), 1)})
			}
			if kc := tl.ActiveEnergy(a, b); kc > 0 {
				g.intervalRec("ActiveCaloriesBurned", a, b, m(), synth.Doc{"energyKilocalories": synth.Round(kc, 2)})
			}
			step := time.Duration(float64(time.Minute) / tl.Opt.Volume)
			var samples []any
			for _, t := range tl.Every(a, b, step) {
				samples = append(samples, synth.Doc{"time": instant(t), "beatsPerMinute": int64(tl.HeartRate(t))})
			}
			g.intervalRec("HeartRate", a, b, m(), synth.Doc{"samples": samples})
		}
	}
	for i, d := range tl.Days {
		if d.Missing || d.End.After(now) {
			continue
		}
		tot := tl.Totals(d)
		m := g.meta(samsung, "automatic", "", "", "", d.End)
		g.intervalRec("TotalCaloriesBurned", d.Start, d.End, m, synth.Doc{"energyKilocalories": synth.Round(tot.ActiveKcal+tot.BasalKcal, 1)})
		g.instantRec("RestingHeartRate", synth.AtLocal(d, 7), g.meta(samsung, "automatic", "watch", "Samsung", "Galaxy Watch7", d.End),
			synth.Doc{"beatsPerMinute": int64(tot.RestingHR)})
		g.instantRec("BasalMetabolicRate", synth.AtLocal(d, 0), g.meta(samsung, "unknown", "", "", "", d.End),
			synth.Doc{"basalMetabolicRateKilocaloriesPerDay": synth.Round(tot.BasalKcal, 1)})
		if s, ok := tl.NightOf(i); ok {
			g.night(s)
		}
	}
	for _, s := range tl.Sleeps {
		if s.Nap && !s.End.After(now) {
			g.sleep(s, samsung)
		}
	}
	for _, w := range tl.Recorded() {
		m := g.meta(samsung, "active", "watch", "Samsung", "Galaxy Watch7", w.End)
		g.intervalRec("ExerciseSession", w.Start, w.End, m, synth.Doc{"exerciseType": sports[w.Sport], "title": nil})
	}
	g.body(now)
	g.food(now)
	return g.e.Lines()
}

// night writes the sleep and the figures a ring measures over it.
func (g *renderer) night(s synth.Sleep) {
	tl := g.tl
	g.sleep(s, oura)
	origin := oura
	if tl.Opt.Has(synth.MultiDevice) {
		g.sleep(s, ringconn)
		origin = ringconn
	}
	mid := s.Start.Add(s.End.Sub(s.Start) / 2)
	ring := func() synth.Doc { return g.meta(origin, "automatic", "ring", "Oura", "Ring 4", s.End) }
	g.instantRec("HeartRateVariabilityRmssd", mid, ring(), synth.Doc{"heartRateVariabilityMillis": synth.Round(s.HRV, 1)})
	g.instantRec("OxygenSaturation", mid, ring(), synth.Doc{"percentage": synth.Round(s.SpO2, 1)})
	g.instantRec("RespiratoryRate", mid, ring(), synth.Doc{"rate": synth.Round(s.Resp, 1)})
	g.instantRec("BodyTemperature", s.End, ring(), synth.Doc{"temperatureCelsius": synth.Round(36.4+s.TempDelta, 2), "measurementLocation": "finger"})
}

func (g *renderer) sleep(s synth.Sleep, origin string) {
	var st []any
	for _, x := range s.Stages {
		name := stages[x.Stage]
		if origin == samsung && x.Stage == synth.StageLight {
			// Apps without staging write STAGE_TYPE_SLEEPING.
			name = "sleeping"
		}
		st = append(st, synth.Doc{"startTime": instant(x.Start), "endTime": instant(x.End), "stage": name})
	}
	m := g.meta(origin, "automatic", "ring", "Oura", "Ring 4", s.End.Add(20*time.Minute))
	g.intervalRec("SleepSession", s.Start, s.End, m, synth.Doc{"title": nil, "notes": nil, "stages": st})
}

func (g *renderer) body(now time.Time) {
	tl := g.tl
	first := true
	for _, w := range tl.Weighs {
		if w.At.After(now) {
			continue
		}
		m := func() synth.Doc {
			return g.meta(withings, "automatic", "scale", "Withings", "Body Smart", w.At.Add(time.Minute))
		}
		g.instantRec("Weight", w.At, m(), synth.Doc{"weightKilograms": synth.Round(w.WeightKG, 2)})
		g.instantRec("BodyFat", w.At, m(), synth.Doc{"percentage": synth.Round(w.BodyFat*100, 1)})
		if first {
			g.instantRec("Height", w.At, g.meta(samsung, "manual", "", "", "", w.At), synth.Doc{"heightMeters": tl.P.HeightCM / 100})
			first = false
		}
	}
	positions := []string{"sitting_down", "standing_up", "lying_down", "reclining", "unknown"}
	for i, b := range tl.BPs {
		if b.At.After(now) {
			continue
		}
		g.instantRec("BloodPressure", b.At, g.meta(omron, "automatic", "consumer_medical_device", "OMRON", "M7 Intelli IT", b.At.Add(time.Minute)),
			synth.Doc{"systolicMillimetersOfMercury": b.Systolic, "diastolicMillimetersOfMercury": b.Diastolic,
				"bodyPosition": positions[i%len(positions)], "measurementLocation": []string{"left_upper_arm", "right_wrist"}[i%2]})
	}
	if tl.P.CGM {
		win := tl.Window()
		for i, t := range tl.Every(win.Start, win.End, 15*time.Minute) {
			if t.After(now) || !tl.Worn(t) {
				continue
			}
			rel := "general"
			if i%96 == 28 {
				rel = "fasting"
			}
			g.instantRec("BloodGlucose", t, g.meta(libre, "automatic", "", "", "", t.Add(time.Minute)),
				synth.Doc{"levelMillimolesPerLiter": synth.Round(tl.Glucose(t)/18.0182, 2), "specimenSource": "interstitial_fluid",
					"relationToMeal": rel, "mealType": "unknown"})
		}
	}
	for _, f := range tl.Flows {
		if f.Start.After(now) {
			continue
		}
		g.instantRec("MenstruationFlow", f.Start, g.meta(samsung, "manual", "", "", "", f.Start), synth.Doc{"flow": f.Level})
	}
}

func (g *renderer) food(now time.Time) {
	tl := g.tl
	for i, m := range tl.Meals {
		if m.At.After(now) {
			continue
		}
		d := g.intervalRec("Nutrition", m.At, m.At.Add(20*time.Minute), g.meta(mfp, "manual", "", "", "", m.At.Add(30*time.Minute)), synth.Doc{
			"name": m.Name, "mealType": m.Kind, "energyKilocalories": synth.Round(m.EnergyKc, 1),
			"proteinGrams": synth.Round(m.ProteinG, 1), "totalCarbohydrateGrams": synth.Round(m.CarbsG, 1),
			"totalFatGrams": synth.Round(m.FatG, 1), "saturatedFatGrams": synth.Round(m.SatFatG, 1),
			"dietaryFiberGrams": synth.Round(m.FiberG, 1), "sugarGrams": synth.Round(m.SugarG, 1),
			"sodiumGrams": synth.Round(m.SodiumMg/1000, 3), "caffeineGrams": synth.Round(m.CaffMg/1000, 3),
		})
		if tl.Opt.Has(synth.Duplicates) && i%5 == 0 {
			// The meal deleted on the phone afterwards.
			id := d["metadata"].(synth.Doc)["id"]
			g.e.Add("Deletion", m.At, synth.Doc{"recordType": "Deletion", "recordId": id}, synth.Shape{Required: []string{"recordType", "recordId"}})
		}
	}
	for _, d := range tl.Drinks {
		if d.At.After(now) {
			continue
		}
		g.intervalRec("Hydration", d.At, d.At.Add(time.Minute), g.meta(samsung, "manual", "", "", "", d.At),
			synth.Doc{"volumeLiters": synth.Round(d.ML/1000, 3)})
	}
}
