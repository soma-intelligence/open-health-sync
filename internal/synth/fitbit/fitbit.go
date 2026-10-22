// SPDX-License-Identifier: AGPL-3.0-only

// Package fitbit renders a synthetic timeline as Google Health API v4 data
// points, the API the fitbit connector reads.
//
// Source: the Google Health API v4 discovery document
// (https://health.googleapis.com/$discovery/rest?version=v4, revision
// 20261020), schemas DataPoint, DataSource, ObservationTimeInterval,
// ObservationSampleTime, SessionTimeInterval, CivilDateTime and the per data
// type schemas (Steps, Distance, Floors, ActiveEnergyBurned, HeartRate,
// DailyRestingHeartRate, DailyHeartRateVariability, HeartRateVariability,
// OxygenSaturation, DailyOxygenSaturation, DailyRespiratoryRate, Weight,
// BodyFat, Height, BloodGlucose, CoreBodyTemperature,
// DailySleepTemperatureDerivations, Exercise, Sleep). Each line is one data
// point of a dataPoints.list page. int64 values are JSON strings and
// durations are "3600s", as the discovery format writes them.
package fitbit

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "fitbit"

type renderer struct {
	tl *synth.Timeline
	e  *synth.Emitter
	r  *synth.Rand
}

func shape(field string, kind string, value string, lo, hi float64) synth.Shape {
	sh := synth.Shape{Required: []string{field}, Optional: []string{"dataSource", "name"}, TimeLayout: time.RFC3339}
	switch kind {
	case "interval", "session":
		sh.Required = append(sh.Required, field+".interval", field+".interval.startTime", field+".interval.endTime")
		sh.Times = []string{field + ".interval.startTime", field + ".interval.endTime"}
		sh.Start, sh.End = field+".interval.startTime", field+".interval.endTime"
	case "sample":
		sh.Required = append(sh.Required, field+".sampleTime", field+".sampleTime.physicalTime")
		sh.Times = []string{field + ".sampleTime.physicalTime"}
	case "daily":
		sh.Required = append(sh.Required, field+".date")
	}
	if value != "" {
		sh.Required = append(sh.Required, field+"."+value)
		sh.Numbers = map[string][2]float64{field + "." + value: {lo, hi}}
		if lo == 0 {
			sh.Counts = []string{field + "." + value}
		}
	}
	return sh
}

var (
	stepsShape     = shape("steps", "interval", "count", 0, 1e6)
	distanceShape  = shape("distance", "interval", "millimeters", 0, 1e9)
	floorsShape    = shape("floors", "interval", "count", 0, 1e6)
	energyShape    = shape("activeEnergyBurned", "interval", "kcal", 0, 1e6)
	hrShape        = shape("heartRate", "sample", "beatsPerMinute", 1, 300)
	rhrShape       = shape("dailyRestingHeartRate", "daily", "beatsPerMinute", 1, 300)
	dhrvShape      = shape("dailyHeartRateVariability", "daily", "averageHeartRateVariabilityMilliseconds", 1, 300)
	hrvShape       = shape("heartRateVariability", "sample", "rootMeanSquareOfSuccessiveDifferencesMilliseconds", 1, 300)
	spo2Shape      = shape("oxygenSaturation", "sample", "percentage", 50, 100)
	dspo2Shape     = shape("dailyOxygenSaturation", "daily", "averagePercentage", 50, 100)
	respShape      = shape("dailyRespiratoryRate", "daily", "breathsPerMinute", 4, 40)
	weightShape    = shape("weight", "sample", "weightGrams", 0, 1e6)
	fatShape       = shape("bodyFat", "sample", "percentage", 0, 100)
	heightShape    = shape("height", "sample", "heightMillimeters", 0, 3000)
	glucoseShape   = shape("bloodGlucose", "sample", "bloodGlucoseMilligramsPerDeciliter", 0, 900)
	coreTempShape  = shape("coreBodyTemperature", "sample", "temperatureCelsius", 30, 45)
	sleepTempShape = shape("dailySleepTemperatureDerivations", "daily", "nightlyTemperatureCelsius", 25, 40)
	exerciseShape  = func() synth.Shape {
		s := shape("exercise", "session", "", 0, 0)
		s.Required = append(s.Required, "exercise.exerciseType", "exercise.displayName", "exercise.metricsSummary")
		s.Numbers = map[string][2]float64{"exercise.metricsSummary.caloriesKcal": {0, 2e4}, "exercise.metricsSummary.distanceMillimeters": {0, 3e8}}
		s.Counts = []string{"exercise.metricsSummary.caloriesKcal"}
		s.Enums = []string{"exercise.exerciseType"}
		s.Strings = []string{"exercise.displayName", "exercise.notes"}
		return s
	}()
	sleepShape = func() synth.Shape {
		s := shape("sleep", "session", "", 0, 0)
		s.Optional = append(s.Optional, "sleep.stages", "sleep.summary", "sleep.metadata", "sleep.type")
		s.Enums = []string{"sleep.type"}
		return s
	}()
)

var exerciseTypes = map[synth.Sport][2]string{
	synth.SportRun: {"RUNNING", "Run"}, synth.SportWalk: {"WALKING", "Walk"}, synth.SportCycle: {"BIKING", "Bike"},
	synth.SportSwim: {"SWIMMING_POOL", "Swim"}, synth.SportStrength: {"WEIGHTLIFTING", "Weights"}, synth.SportYoga: {"YOGA", "Yoga"},
	synth.SportHIIT: {"HIIT", "HIIT"}, synth.SportRow: {"ROWING_MACHINE", "Rowing machine"}, synth.SportHike: {"HIKING", "Hike"},
	synth.SportOther: {"OTHER", "Workout"},
}

// Lines renders tl as the data points the connector stores, one per line.
func Lines(tl *synth.Timeline) []synth.Line {
	g := &renderer{tl: tl, e: synth.NewEmitter(tl, Slug), r: synth.NewRand(tl.Opt.Seed, "fitbit")}
	g.e.UnknownType = "swim-lengths-data"
	for _, d := range tl.Days {
		if d.Missing {
			continue
		}
		g.intervals(d)
		g.samples(d)
		g.daily(d)
	}
	for _, w := range tl.Weighs {
		g.point("weight", w.At, "weight", g.sample(w.At, "weightGrams", synth.Round(w.WeightKG*1000, 1)), weightShape, "SCALE")
		g.point("body-fat", w.At, "bodyFat", g.sample(w.At, "percentage", synth.Round(w.BodyFat*100, 1)), fatShape, "SCALE")
	}
	if len(tl.Days) > 0 {
		at := synth.AtLocal(tl.Days[0], 9)
		g.point("height", at, "height", g.sample(at, "heightMillimeters", str(tl.P.HeightCM*10)), heightShape, "")
	}
	for _, w := range tl.Recorded() {
		g.exercise(w)
	}
	for _, s := range tl.Sleeps {
		if tl.Worn(s.Start) {
			g.sleep(s)
		}
	}
	return g.e.Lines()
}

// str writes an int64 as the discovery format does: a JSON string.
func str(v float64) string { return strconv.FormatInt(int64(math.Round(v)), 10) }

func dur(d time.Duration) string {
	return strconv.FormatInt(int64(d.Round(time.Second).Seconds()), 10) + "s"
}

func (g *renderer) offset(t time.Time) string {
	_, off := g.tl.Local(t).Zone()
	return strconv.Itoa(off) + "s"
}

func (g *renderer) civil(t time.Time) synth.Doc {
	l := g.tl.Local(t)
	return synth.Doc{"date": synth.Doc{"year": l.Year(), "month": int(l.Month()), "day": l.Day()},
		"time": synth.Doc{"hours": l.Hour(), "minutes": l.Minute(), "seconds": l.Second(), "nanos": 0}}
}

func (g *renderer) interval(a, b time.Time) synth.Doc {
	return synth.Doc{"startTime": a.UTC().Format(time.RFC3339), "endTime": b.UTC().Format(time.RFC3339),
		"startUtcOffset": g.offset(a), "endUtcOffset": g.offset(b), "civilStartTime": g.civil(a), "civilEndTime": g.civil(b)}
}

func (g *renderer) sample(t time.Time, field string, v any) synth.Doc {
	return synth.Doc{"sampleTime": synth.Doc{"physicalTime": t.UTC().Format(time.RFC3339), "utcOffset": g.offset(t),
		"civilTime": g.civil(t)}, field: v}
}

func date(d synth.Day) synth.Doc {
	t, _ := time.Parse(time.DateOnly, d.Date)
	return synth.Doc{"year": t.Year(), "month": int(t.Month()), "day": t.Day()}
}

func (g *renderer) source(form string) synth.Doc {
	if form == "" {
		return synth.Doc{"recordingMethod": "MANUAL", "platform": "FITBIT"}
	}
	name := map[string]string{"WATCH": "Pixel Watch 3", "SCALE": "Aria Air", "FITNESS_BAND": "Charge 6"}[form]
	return synth.Doc{"recordingMethod": "PASSIVELY_MEASURED", "platform": "FITBIT",
		"device": synth.Doc{"formFactor": form, "displayName": name, "manufacturer": "Google"}}
}

func (g *renderer) point(dataType string, at time.Time, field string, body synth.Doc, sh synth.Shape, form string) {
	doc := synth.Doc{field: body, "dataSource": g.source(form)}
	if dataType == "sleep" || dataType == "exercise" {
		doc["name"] = fmt.Sprintf("users/me/dataTypes/%s/dataPoints/%s", dataType, strings.ToLower(g.r.UUID()))
	}
	g.e.Add(dataType, at, doc, sh)
}

// intervals writes steps, distance, floors and active energy a minute at a
// time, the granularity the API rolls up from.
func (g *renderer) intervals(d synth.Day) {
	tl := g.tl
	step := time.Duration(float64(15*time.Minute) / tl.Opt.Volume)
	if step < time.Minute {
		step = time.Minute
	}
	for _, a := range tl.Every(d.Start, d.End, step) {
		b := a.Add(step)
		steps := tl.Steps(a, b)
		if steps == 0 {
			continue
		}
		iv := g.interval(a, b)
		g.point("steps", a, "steps", synth.Doc{"interval": iv, "count": str(steps)}, stepsShape, "WATCH")
		g.point("distance", a, "distance", synth.Doc{"interval": iv, "millimeters": str(tl.Distance(a, b) * 1000)}, distanceShape, "WATCH")
		g.point("active-energy-burned", a, "activeEnergyBurned", synth.Doc{"interval": iv, "kcal": synth.Round(tl.ActiveEnergy(a, b), 2)},
			energyShape, "WATCH")
	}
	t := tl.Totals(d)
	if t.FloorsUp > 0 {
		at := synth.AtLocal(d, 17)
		g.point("floors", at, "floors", synth.Doc{"interval": g.interval(at, at.Add(time.Hour)), "count": str(t.FloorsUp)}, floorsShape, "WATCH")
	}
}

func (g *renderer) samples(d synth.Day) {
	tl := g.tl
	step := time.Duration(float64(5*time.Minute) / tl.Opt.Volume)
	if step < time.Second {
		step = time.Second
	}
	for _, t := range tl.Every(d.Start, d.End, step) {
		body := g.sample(t, "beatsPerMinute", str(tl.HeartRate(t)))
		motion := "SEDENTARY"
		if _, ok := tl.WorkoutAt(t); ok {
			motion = "ACTIVE"
		}
		body["metadata"] = synth.Doc{"sensorLocation": "WRIST", "motionContext": motion}
		g.point("heart-rate", t, "heartRate", body, hrShape, "WATCH")
	}
	if s, ok := tl.NightOf(d.Index); ok && tl.Worn(s.Start) {
		for t := s.Start; t.Before(s.End); t = t.Add(5 * time.Minute) {
			if s.StageAt(t) == synth.StageAwake {
				continue
			}
			h := g.sample(t, "rootMeanSquareOfSuccessiveDifferencesMilliseconds", synth.Round(s.HRV+6*math.Sin(float64(t.Unix())/700), 3))
			h["standardDeviationMilliseconds"] = synth.Round(tl.HRVSDNN(t), 3)
			g.point("heart-rate-variability", t, "heartRateVariability", h, hrvShape, "WATCH")
			g.point("oxygen-saturation", t, "oxygenSaturation", g.sample(t, "percentage", synth.Round(tl.SpO2(t)*100, 1)), spo2Shape, "WATCH")
		}
	}
	if tl.P.CGM {
		for t := d.Start; t.Before(d.End); t = t.Add(15 * time.Minute) {
			b := g.sample(t, "bloodGlucoseMilligramsPerDeciliter", tl.Glucose(t))
			b["specimen"] = "INTERSTITIAL_FLUID"
			b["measurementSource"] = "CONTINUOUS_GLUCOSE_MONITORING"
			b["measurementTiming"] = "GENERAL"
			g.point("blood-glucose", t, "bloodGlucose", b, glucoseShape, "")
		}
	} else if d.Index%3 == 0 {
		t := synth.AtLocal(d, 7.3)
		b := g.sample(t, "bloodGlucoseMilligramsPerDeciliter", tl.Glucose(t))
		b["specimen"] = "CAPILLARY_BLOOD"
		b["measurementSource"] = "SELF_MONITORING_BLOOD_GLUCOSE"
		b["measurementTiming"] = "FASTING"
		b["mealType"] = "BREAKFAST"
		g.point("blood-glucose", t, "bloodGlucose", b, glucoseShape, "")
	}
	if d.Index%4 == 2 {
		t := synth.AtLocal(d, 21)
		c := g.sample(t, "temperatureCelsius", 36.8)
		c["measurementLocation"] = "MOUTH"
		g.point("core-body-temperature", t, "coreBodyTemperature", c, coreTempShape, "")
	}
}

func (g *renderer) daily(d synth.Day) {
	tl := g.tl
	t := tl.Totals(d)
	at := d.Start
	g.point("daily-resting-heart-rate", at, "dailyRestingHeartRate", synth.Doc{"date": date(d), "beatsPerMinute": str(t.RestingHR),
		"dailyRestingHeartRateMetadata": synth.Doc{"calculationMethod": "WITH_SLEEP"}}, rhrShape, "WATCH")
	s, ok := tl.NightOf(d.Index)
	if !ok || !tl.Worn(s.Start) {
		return
	}
	g.point("daily-heart-rate-variability", at, "dailyHeartRateVariability", synth.Doc{"date": date(d),
		"averageHeartRateVariabilityMilliseconds": synth.Round(s.HRV, 3), "nonRemHeartRateBeatsPerMinute": str(s.LowestHR + 4),
		"deepSleepRootMeanSquareOfSuccessiveDifferencesMilliseconds": synth.Round(s.HRV*1.15, 3), "entropy": 2.6}, dhrvShape, "WATCH")
	g.point("daily-oxygen-saturation", at, "dailyOxygenSaturation", synth.Doc{"date": date(d), "averagePercentage": s.SpO2,
		"lowerBoundPercentage": s.SpO2 - 1.5, "upperBoundPercentage": math.Min(100, s.SpO2+1.2), "standardDeviationPercentage": 0.6},
		dspo2Shape, "WATCH")
	g.point("daily-respiratory-rate", at, "dailyRespiratoryRate", synth.Doc{"date": date(d), "breathsPerMinute": synth.Round(s.Resp, 1)},
		respShape, "WATCH")
	g.point("daily-sleep-temperature-derivations", at, "dailySleepTemperatureDerivations", synth.Doc{"date": date(d),
		"nightlyTemperatureCelsius": synth.Round(33.9+s.TempDelta, 2), "baselineTemperatureCelsius": 33.9,
		"relativeNightlyStddev30dCelsius": 0.3}, sleepTempShape, "WATCH")
}

func (g *renderer) exercise(w synth.Workout) {
	et := exerciseTypes[w.Sport]
	events := []any{synth.Doc{"exerciseEventType": "START", "eventTime": w.Start.UTC().Format(time.RFC3339), "eventUtcOffset": g.offset(w.Start)}}
	for _, p := range w.Pauses {
		events = append(events,
			synth.Doc{"exerciseEventType": "AUTO_PAUSE", "eventTime": p.Start.UTC().Format(time.RFC3339), "eventUtcOffset": g.offset(p.Start)},
			synth.Doc{"exerciseEventType": "AUTO_RESUME", "eventTime": p.End.UTC().Format(time.RFC3339), "eventUtcOffset": g.offset(p.End)})
	}
	events = append(events, synth.Doc{"exerciseEventType": "STOP", "eventTime": w.End.UTC().Format(time.RFC3339), "eventUtcOffset": g.offset(w.End)})
	ms := synth.Doc{"caloriesKcal": synth.Round(w.EnergyKcal, 1), "averageHeartRateBeatsPerMinute": str(w.AvgHR),
		"activeZoneMinutes": str(w.Moving().Minutes() * w.Sport.Intensity()),
		"heartRateZoneDurations": synth.Doc{"lightTime": dur(w.Moving() / 4), "moderateTime": dur(w.Moving() / 3),
			"vigorousTime": dur(w.Moving() / 3), "peakTime": dur(w.Moving() / 12)}}
	if w.DistanceM > 0 {
		ms["distanceMillimeters"] = synth.Round(w.DistanceM*1000, 0)
		ms["averageSpeedMillimetersPerSecond"] = synth.Round(w.DistanceM*1000/w.Moving().Seconds(), 1)
		ms["averagePaceSecondsPerMeter"] = synth.Round(w.Moving().Seconds()/w.DistanceM, 4)
		ms["elevationGainMillimeters"] = synth.Round(w.AscentM*1000, 0)
	}
	if w.Steps > 0 {
		ms["steps"] = str(w.Steps)
	}
	meta := synth.Doc{"hasGps": w.Sport.Outdoor() && !w.Indoor}
	if w.Sport == synth.SportSwim {
		meta["poolLengthMillimeters"] = "25000"
		ms["totalSwimLengths"] = math.Floor(w.DistanceM / 25)
	}
	g.point("exercise", w.Start, "exercise", synth.Doc{
		"interval": g.interval(w.Start, w.End), "exerciseType": et[0], "displayName": et[1], "metricsSummary": ms,
		"activeDuration": dur(w.Moving()), "exerciseEvents": events, "exerciseMetadata": meta,
		"createTime": w.End.Add(time.Minute).UTC().Format(time.RFC3339), "updateTime": w.End.Add(2 * time.Minute).UTC().Format(time.RFC3339),
	}, exerciseShape, "WATCH")
}

func stageType(s synth.SleepStage) string {
	switch s {
	case synth.StageLight:
		return "LIGHT"
	case synth.StageDeep:
		return "DEEP"
	case synth.StageREM:
		return "REM"
	}
	return "AWAKE"
}

func (g *renderer) sleep(s synth.Sleep) {
	var stages []any
	counts := map[string][2]float64{}
	for _, st := range s.Stages {
		typ := stageType(st.Stage)
		if s.Nap {
			typ = "ASLEEP"
			if st.Stage == synth.StageAwake {
				typ = "AWAKE"
			}
		}
		stages = append(stages, synth.Doc{"type": typ, "startTime": st.Start.UTC().Format(time.RFC3339),
			"endTime": st.End.UTC().Format(time.RFC3339), "startUtcOffset": g.offset(st.Start), "endUtcOffset": g.offset(st.End)})
		c := counts[typ]
		counts[typ] = [2]float64{c[0] + st.End.Sub(st.Start).Minutes(), c[1] + 1}
	}
	var sum []any
	for _, typ := range []string{"AWAKE", "LIGHT", "DEEP", "REM", "ASLEEP"} {
		if c, ok := counts[typ]; ok {
			sum = append(sum, synth.Doc{"type": typ, "minutes": str(c[0]), "count": str(c[1])})
		}
	}
	typ := "STAGES"
	if s.Nap {
		typ = "CLASSIC"
	}
	g.point("sleep", s.Start, "sleep", synth.Doc{
		"interval": g.interval(s.Start, s.End), "type": typ, "stages": stages,
		"metadata": synth.Doc{"processed": true, "manuallyEdited": false, "mainSleep": !s.Nap, "nap": s.Nap, "stagesStatus": "SUCCEEDED"},
		"summary": synth.Doc{"minutesAsleep": str(s.Asleep().Minutes()), "minutesAwake": str(s.In(synth.StageAwake).Minutes()),
			"minutesInSleepPeriod": str(s.End.Sub(s.Start).Minutes()), "minutesToFallAsleep": "8", "minutesAfterWakeUp": "2",
			"stagesSummary": sum},
		"createTime": s.End.Add(time.Hour).UTC().Format(time.RFC3339), "updateTime": s.End.Add(time.Hour).UTC().Format(time.RFC3339),
	}, sleepShape, "WATCH")
}
