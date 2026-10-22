// SPDX-License-Identifier: AGPL-3.0-only

// Package suunto renders a synthetic timeline as Suunto API Zone answers:
// workout summaries and the 24/7 sleep, recovery and daily activity data.
//
// Source: the Suunto API Zone developer portal (https://apizone.suunto.com),
// operations "List workouts" and "Get workout" of the Suunto Workout API and
// "Sleep data", "Recovery data" and "Daily activity statistics" of the 24/7
// API, with their examples, and the FAQ for the time fields: startTime and
// stopTime are epoch milliseconds, timeOffsetInMinutes the local offset when
// the exercise stopped, positions have x the longitude and y the latitude,
// avgTemperature is in kelvin, energy of the 24/7 data in joules. The
// activityId values are Suunto App ids, which the portal does not list; the
// lines use the ids of its examples (3 and 11) and a few more. Each line is
// one item of an answer.
package suunto

import (
	"math"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "suunto"

var workoutShape = synth.Shape{
	ID:       "workoutKey",
	Required: []string{"workoutKey", "activityId", "startTime", "totalTime"},
	Optional: []string{"stopTime", "timeOffsetInMinutes", "totalDistance", "totalAscent", "totalDescent", "startPosition",
		"stopPosition", "centerPosition", "maxSpeed", "avgSpeed", "avgPace", "energyConsumption", "stepCount", "hrdata",
		"hrdata.workoutAvgHR", "hrdata.workoutMaxHR", "cadence", "extensions", "minAltitude", "maxAltitude", "workoutName",
		"recoveryTime", "workoutId", "created"},
	Numbers: map[string][2]float64{"totalDistance": {0, 3e5}, "hrdata.workoutAvgHR": {25, 250}, "hrdata.workoutMaxHR": {25, 250},
		"energyConsumption": {0, 2e4}, "avgSpeed": {0, 50}, "totalTime": {0, 86400}},
	Counts:  []string{"totalDistance", "totalTime", "stepCount", "energyConsumption"},
	Times:   []string{"startTime", "stopTime"},
	Start:   "startTime",
	End:     "stopTime",
	Enums:   []string{"activityId"},
	Strings: []string{"workoutName", "description"},
	// Epoch milliseconds, as the FAQ documents.
	EpochMillis: true,
}

var sleepShape = synth.Shape{
	Required: []string{"timestamp", "entryData"},
	Optional: []string{"entryData.DeepSleepDuration", "entryData.LightSleepDuration", "entryData.REMSleepDuration",
		"entryData.HRAvg", "entryData.HRMin", "entryData.MaxSpo2", "entryData.AvgHRV", "entryData.SleepQualityScore",
		"entryData.BedtimeStart", "entryData.BedtimeEnd", "entryData.IsNap", "entryData.SleepId"},
	Numbers: map[string][2]float64{"entryData.Duration": {0, 86400}, "entryData.HRAvg": {25, 200}, "entryData.MaxSpo2": {0.7, 1}},
	Counts:  []string{"entryData.Duration", "entryData.DeepSleepDuration"},
	Times:   []string{"timestamp", "entryData.BedtimeStart", "entryData.BedtimeEnd"},
	Start:   "entryData.BedtimeStart",
	End:     "entryData.BedtimeEnd",
}

var recoveryShape = synth.Shape{
	Required: []string{"timestamp", "entryData"},
	Optional: []string{"entryData.Balance", "entryData.StressState"},
	Numbers:  map[string][2]float64{"entryData.Balance": {0, 1}},
	Times:    []string{"timestamp"},
	Enums:    []string{"entryData.StressState"},
}

var dailyShape = synth.Shape{
	Required:   []string{"Name", "Aggregation", "Sources"},
	Numbers:    map[string][2]float64{"Sources.0.Samples.0.Value": {0, 1e8}},
	Counts:     []string{"Sources.0.Samples.0.Value"},
	Times:      []string{"Sources.0.Samples.0.TimeISO8601"},
	Enums:      []string{"Name", "Aggregation"},
	TimeLayout: "2006-01-02T15:04:05.000",
}

// activities gives each sport a Suunto App id: 3 and 11 come from the
// portal's examples, the others stand for ids the portal does not list.
var activities = map[synth.Sport]int{
	synth.SportRun: 3, synth.SportWalk: 0, synth.SportCycle: 2, synth.SportSwim: 6, synth.SportStrength: 17,
	synth.SportYoga: 69, synth.SportHIIT: 63, synth.SportRow: 14, synth.SportHike: 11, synth.SportOther: 9,
}

// Lines renders tl as the items the connector stores.
func Lines(tl *synth.Timeline) []synth.Line {
	e := synth.NewEmitter(tl, Slug)
	r := synth.NewRand(tl.Opt.Seed, "suunto")
	e.UnknownType = "routes"
	for _, w := range tl.Recorded() {
		e.Add("workout", w.Start, workout(tl, w, r), workoutShape)
	}
	for _, s := range tl.Sleeps {
		if s.End.After(tl.Opt.Now) {
			continue
		}
		e.Add("sleep", s.Start, sleep(s), sleepShape)
	}
	win := tl.Window()
	for _, t := range tl.Every(win.Start, win.End, time.Hour) {
		if !tl.Worn(t) || t.After(tl.Opt.Now) {
			continue
		}
		hr := tl.HeartRate(t)
		balance := synth.Round(math.Max(0, math.Min(1, 1.2-hr/100)), 2)
		state := 0
		switch {
		case hr > 100:
			state = 3
		case hr > 80:
			state = 2
		case hr > 65:
			state = 1
		}
		e.Add("recovery", t, synth.Doc{"timestamp": t.UTC().Format(time.RFC3339),
			"entryData": synth.Doc{"Balance": balance, "StressState": state}}, recoveryShape)
	}
	source := "suunto-247-" + r.UUID()
	for _, d := range tl.Days {
		if d.Missing || d.End.After(tl.Opt.Now) {
			continue
		}
		tot := tl.Totals(d)
		// Samples are stamped at the local midnight written in UTC without
		// an offset, as the example does.
		at := d.Start.UTC().Format("2006-01-02T15:04:05.000")
		e.Add("daily_activity", d.Start, daily("energyconsumption", source, at, synth.Round((tot.ActiveKcal+tot.BasalKcal)*4184, 4)), dailyShape)
		e.Add("daily_activity", d.Start, daily("stepcount", source, at, math.Round(tot.Steps)), dailyShape)
	}
	return e.Lines()
}

func workout(tl *synth.Timeline, w synth.Workout, r *synth.Rand) synth.Doc {
	_, off := w.End.In(tl.ZoneAt(w.End)).Zone()
	moving := w.Moving().Seconds()
	doc := synth.Doc{
		"workoutKey": r.Hex(24), "workoutId": 100000 + r.Intn(900000), "activityId": activities[w.Sport],
		"startTime": w.Start.UnixMilli(), "stopTime": w.End.UnixMilli(), "timeOffsetInMinutes": off / 60,
		"totalTime": synth.Round(moving, 2), "totalDistance": synth.Round(w.DistanceM, 1),
		"totalAscent": synth.Round(w.AscentM, 2), "totalDescent": synth.Round(w.AscentM*0.95, 2),
		"energyConsumption": math.Round(w.EnergyKcal), "stepCount": int(w.Steps),
		"hrdata":       synth.Doc{"userMaxHR": 0, "workoutMaxHR": int(w.MaxHR), "workoutAvgHR": int(w.AvgHR), "hrmax": int(w.MaxHR), "max": 0, "avg": int(w.AvgHR)},
		"cadence":      synth.Doc{"max": 0, "avg": 0},
		"recoveryTime": int(moving * w.Sport.Intensity() * 4), "cumulativeRecoveryTime": 0,
		"commentCount": 0, "pictureCount": 0, "viewCount": 0,
		"extensions": []any{synth.Doc{"type": "SummaryExtension", "pte": synth.Round(1+3*w.Sport.Intensity(), 1),
			"avgTemperature": synth.Round(273.15+r.Range(8, 30), 2), "peakEpoc": synth.Round(r.Range(5, 120), 1),
			"avgPower": nil, "avgCadence": synth.Round(w.AvgCadence/120, 4)}},
		"extensionTypes": []any{"SUMMARY", "HEARTRATESTREAM", "INTENSITY"},
		"created":        w.End.Add(5 * time.Minute).UnixMilli(),
	}
	if moving > 0 && w.DistanceM > 0 {
		v := w.DistanceM / moving
		doc["avgSpeed"], doc["maxSpeed"] = synth.Round(v, 2), synth.Round(v*1.6, 2)
		doc["avgPace"] = synth.Round(1000/v/60, 2)
	}
	if w.Sport == synth.SportCycle {
		doc["extensions"].([]any)[0].(synth.Doc)["avgPower"] = synth.Round(w.AvgPowerW, 1)
	}
	if w.Sport.Outdoor() && !w.Indoor {
		pos := func(t time.Time) synth.Doc {
			lat, lon, _, _ := tl.Route(w, t)
			return synth.Doc{"x": lon, "y": lat}
		}
		doc["startPosition"], doc["stopPosition"] = pos(w.Start), pos(w.End.Add(-time.Second))
		doc["centerPosition"] = synth.Doc{"x": w.CenterLon, "y": w.CenterLat}
		doc["minAltitude"], doc["maxAltitude"] = 32.0, 32.0+w.AscentM/3
		doc["extensionTypes"] = append(doc["extensionTypes"].([]any), "LOCATIONSTREAM", "ALTITUDESTREAM", "SPEEDSTREAM")
	}
	return doc
}

func sleep(s synth.Sleep) synth.Doc {
	iso := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }
	return synth.Doc{
		"timestamp": iso(s.Start),
		"entryData": synth.Doc{
			"DeepSleepDuration": int(s.In(synth.StageDeep).Seconds()), "LightSleepDuration": int(s.In(synth.StageLight).Seconds()),
			"REMSleepDuration": int(s.In(synth.StageREM).Seconds()), "Duration": int(s.Asleep().Seconds()),
			"Feeling": nil, "HRAvg": int(s.AvgHR), "HRMin": int(s.LowestHR), "SleepQualityScore": s.Score,
			"BodyResourcesInsightId": nil, "SleepId": nil, "BedtimeStart": iso(s.Start), "BedtimeEnd": iso(s.End),
			"MaxSpo2": synth.Round(math.Min(1, s.SpO2/100+0.015), 3), "Altitude": 50, "AvgHRV": synth.Round(s.HRV, 1),
			"AvgHRVSampleCount": int(s.Asleep().Minutes() / 5), "IsNap": s.Nap, "SleepOnsetLatencyDuration": 600,
			"WakeAfterSleepOnsetDuration": int(s.In(synth.StageAwake).Seconds()), "WakeBeforeOffBedDuration": 120,
			"DateTime": iso(s.Start),
		},
	}
}

func daily(name, source, at string, v float64) synth.Doc {
	return synth.Doc{"Name": name, "Aggregation": "sum",
		"Sources": []any{synth.Doc{"Name": source, "Samples": []any{synth.Doc{"TimeISO8601": at, "Value": v}}}}}
}
