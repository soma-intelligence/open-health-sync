// SPDX-License-Identifier: AGPL-3.0-only

// Package intervalsicu renders a synthetic timeline as intervals.icu API
// activities and wellness records.
//
// Source: the intervals.icu OpenAPI document
// (https://intervals.icu/api/v1/docs), schemas Activity, Wellness and
// SportInfo (whose type enum lists the sports). The document marks no field
// required and leaves the time fields as plain strings: start_date is UTC
// with a Z, start_date_local the wall time without offset, timezone an IANA
// name, and a wellness id is its local date. Each line is one item of the
// list answers.
package intervalsicu

import (
	"fmt"
	"math"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "intervals_icu"

var activityShape = synth.Shape{
	ID: "id",
	// No field is required in the document; these are the ones every list
	// item carries.
	Required: []string{"id", "start_date_local", "type"},
	Optional: []string{"name", "start_date", "timezone", "distance", "moving_time", "elapsed_time", "total_elevation_gain",
		"average_heartrate", "max_heartrate", "calories", "average_speed", "average_cadence", "icu_average_watts", "device_name",
		"icu_training_load", "trainer", "source", "external_id"},
	Numbers: map[string][2]float64{"distance": {0, 3e5}, "average_heartrate": {25, 250}, "max_heartrate": {25, 250},
		"calories": {0, 2e4}, "moving_time": {0, 86400}},
	Counts:  []string{"distance", "moving_time", "elapsed_time", "calories"},
	Times:   []string{"start_date_local", "start_date"},
	Enums:   []string{"type", "source", "sub_type"},
	Strings: []string{"name", "device_name"},
	// The local layout without offset, as start_date_local is written.
	TimeLayout: "2006-01-02T15:04:05",
}

var wellnessShape = synth.Shape{
	ID:       "id",
	Required: []string{"id"},
	Optional: []string{"weight", "restingHR", "hrv", "hrvSDNN", "sleepSecs", "sleepScore", "sleepQuality", "avgSleepingHR",
		"spO2", "systolic", "diastolic", "steps", "respiration", "bodyFat", "kcalConsumed", "updated", "menstrualPhase",
		"bloodGlucose", "vo2max", "mood", "stress"},
	Numbers: map[string][2]float64{"weight": {20, 300}, "restingHR": {25, 120}, "hrv": {5, 250}, "sleepSecs": {0, 86400},
		"spO2": {70, 100}, "systolic": {70, 250}, "diastolic": {40, 150}, "respiration": {5, 40}, "bodyFat": {3, 60},
		"bloodGlucose": {2, 30}},
	Counts:     []string{"steps", "sleepSecs", "kcalConsumed"},
	Times:      []string{"updated"},
	Enums:      []string{"menstrualPhase"},
	Strings:    []string{"comments"},
	TimeLayout: "2006-01-02T15:04:05.000Z07:00",
}

// sports maps the generator's sports to the SportInfo type enum.
var sports = map[synth.Sport]string{
	synth.SportRun: "Run", synth.SportWalk: "Walk", synth.SportCycle: "Ride", synth.SportSwim: "Swim",
	synth.SportStrength: "WeightTraining", synth.SportYoga: "Yoga", synth.SportHIIT: "HighIntensityIntervalTraining",
	synth.SportRow: "Rowing", synth.SportHike: "Hike", synth.SportOther: "Workout",
}

// Lines renders tl as the activity and wellness items the connector stores.
func Lines(tl *synth.Timeline) []synth.Line {
	e := synth.NewEmitter(tl, Slug)
	r := synth.NewRand(tl.Opt.Seed, "intervals_icu")
	e.UnknownType = "events"
	for _, w := range tl.Recorded() {
		e.Add("activity", w.Start, activity(tl, w, "GARMIN_CONNECT", "Forerunner 965", r), activityShape)
		if tl.Opt.Has(synth.MultiDevice) && w.Sport.Moves() {
			// The same session synced again from a second service.
			e.Add("activity", w.Start, activity(tl, w, "STRAVA", "Apple Watch Ultra", r), activityShape)
		}
	}
	for i, d := range tl.Days {
		if d.Missing || !d.End.Before(tl.Opt.Now) {
			continue
		}
		e.Add("wellness", d.Start, wellness(tl, i, d, r), wellnessShape)
	}
	return e.Lines()
}

func activity(tl *synth.Timeline, w synth.Workout, source, device string, r *synth.Rand) synth.Doc {
	loc := tl.ZoneAt(w.Start)
	lt := w.Start.In(loc)
	moving := math.Round(w.Moving().Seconds())
	doc := synth.Doc{
		"id": fmt.Sprintf("i%d", 40000000+r.Intn(9000000)), "icu_athlete_id": "i123456",
		"start_date_local": lt.Format("2006-01-02T15:04:05"), "start_date": w.Start.UTC().Format("2006-01-02T15:04:05Z"),
		"timezone": loc.String(), "type": sports[w.Sport], "name": sports[w.Sport],
		"moving_time": int(moving), "elapsed_time": int(w.End.Sub(w.Start).Seconds()),
		"distance": synth.Round(w.DistanceM, 1), "icu_distance": synth.Round(w.DistanceM, 1),
		"total_elevation_gain": synth.Round(w.AscentM, 1), "trainer": w.Indoor, "commute": false, "race": false,
		"has_heartrate": true, "average_heartrate": int(w.AvgHR), "max_heartrate": int(w.MaxHR),
		"calories": int(w.EnergyKcal), "icu_training_load": int(moving / 60 * w.Sport.Intensity()),
		"device_name": device, "source": source, "external_id": fmt.Sprintf("%s_%s", source, w.ID),
		"sub_type": "NONE", "created": w.End.UTC().Add(10 * time.Minute).Format("2006-01-02T15:04:05.000Z07:00"),
	}
	if moving > 0 && w.DistanceM > 0 {
		doc["average_speed"] = synth.Round(w.DistanceM/moving, 3)
		doc["max_speed"] = synth.Round(w.DistanceM/moving*1.5, 3)
	}
	if w.AvgCadence > 0 {
		doc["average_cadence"] = synth.Round(w.AvgCadence, 1)
	}
	if w.Sport == synth.SportCycle {
		doc["icu_average_watts"] = int(w.AvgPowerW)
		doc["icu_joules"] = int(w.AvgPowerW * moving)
		doc["device_watts"] = true
	}
	return doc
}

func wellness(tl *synth.Timeline, i int, d synth.Day, r *synth.Rand) synth.Doc {
	tot := tl.Totals(d)
	doc := synth.Doc{
		"id": d.Date, "restingHR": int(tot.RestingHR), "steps": int(tot.Steps),
		"updated": d.End.UTC().Add(2 * time.Hour).Format("2006-01-02T15:04:05.000Z07:00"),
		"ctl":     synth.Round(r.Range(30, 60), 2), "atl": synth.Round(r.Range(25, 70), 2), "locked": false,
	}
	if s, ok := tl.NightOf(i); ok {
		doc["sleepSecs"] = int(s.Asleep().Seconds())
		doc["sleepScore"] = float64(s.Score)
		doc["sleepQuality"] = 1 + min(3, s.Score/25)
		doc["avgSleepingHR"] = synth.Round(s.AvgHR, 1)
		doc["hrv"] = synth.Round(s.HRV, 1)
		doc["respiration"] = synth.Round(s.Resp, 1)
		doc["spO2"] = synth.Round(s.SpO2, 1)
	}
	for _, w := range tl.Weighs {
		if d.Interval().Contains(w.At) {
			doc["weight"] = synth.Round(w.WeightKG, 2)
			doc["bodyFat"] = synth.Round(w.BodyFat*100, 1)
		}
	}
	for _, b := range tl.BPs {
		if d.Interval().Contains(b.At) {
			doc["systolic"], doc["diastolic"] = int(b.Systolic), int(b.Diastolic)
		}
	}
	kcal := 0.0
	for _, m := range tl.Meals {
		if d.Interval().Contains(m.At) {
			kcal += m.EnergyKc
		}
	}
	if kcal > 0 {
		doc["kcalConsumed"] = int(kcal)
	}
	for _, f := range tl.Flows {
		if f.Day == i {
			doc["menstrualPhase"] = "PERIOD"
		}
	}
	if tl.P.CGM {
		// intervals.icu takes glucose in mmol/L.
		doc["bloodGlucose"] = synth.Round(tl.Glucose(synth.AtLocal(d, 7))/18.0182, 1)
	}
	return doc
}
