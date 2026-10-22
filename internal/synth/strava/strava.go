// SPDX-License-Identifier: AGPL-3.0-only

// Package strava renders a synthetic timeline as Strava API v3 summary
// activities.
//
// Source: the Strava API v3 Swagger document
// (https://developers.strava.com/swagger/swagger.json) and its models
// activity.json (SummaryActivity), sport_type.json, activity_type.json,
// latlng.json and map.json, with the example answer of GET
// /athlete/activities for the fields the model leaves out (utc_offset,
// has_heartrate, average_heartrate). Each line is one activity of a page.
package strava

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "strava"

var activityShape = synth.Shape{
	ID:       "id",
	Required: []string{"id", "name", "distance", "moving_time", "elapsed_time", "sport_type", "start_date", "start_date_local", "timezone"},
	Optional: []string{"resource_state", "athlete", "total_elevation_gain", "elev_high", "elev_low", "type", "utc_offset", "start_latlng",
		"end_latlng", "achievement_count", "kudos_count", "map", "device_name", "trainer", "commute", "manual", "private",
		"average_speed", "max_speed", "average_cadence", "average_watts", "kilojoules", "has_heartrate", "average_heartrate",
		"max_heartrate", "external_id", "upload_id", "workout_type", "gear_id", "calories"},
	Numbers: map[string][2]float64{"distance": {0, 3e5}, "moving_time": {0, 86400}, "elapsed_time": {0, 86400},
		"average_heartrate": {25, 250}, "average_speed": {0, 50}, "kilojoules": {0, 2e4}},
	Counts: []string{"distance", "moving_time", "kudos_count"},
	Times:  []string{"start_date", "start_date_local"}, Enums: []string{"sport_type", "type"}, Strings: []string{"name", "device_name"},
	TimeLayout: time.RFC3339,
}

// sports maps the generator's sports to SportType and the deprecated
// ActivityType.
var sports = map[synth.Sport][2]string{
	synth.SportRun: {"Run", "Run"}, synth.SportWalk: {"Walk", "Walk"}, synth.SportCycle: {"Ride", "Ride"},
	synth.SportSwim: {"Swim", "Swim"}, synth.SportStrength: {"WeightTraining", "WeightTraining"}, synth.SportYoga: {"Yoga", "Yoga"},
	synth.SportHIIT: {"HighIntensityIntervalTraining", "Workout"}, synth.SportRow: {"Rowing", "Rowing"}, synth.SportHike: {"Hike", "Hike"},
	synth.SportOther: {"Workout", "Workout"},
}

// Lines renders tl as the activities the connector stores, one per line.
func Lines(tl *synth.Timeline) []synth.Line {
	e := synth.NewEmitter(tl, Slug)
	r := synth.NewRand(tl.Opt.Seed, "strava")
	e.UnknownType = "segment_effort"
	var id int64 = 154504250376823
	for _, w := range tl.Recorded() {
		id += 1 + int64(r.Intn(1000))
		e.Add("activity", w.Start, activity(tl, w, id, r), activityShape)
		if tl.Opt.Has(synth.MultiDevice) && w.Sport.Outdoor() && !w.Indoor {
			// The same run uploaded again from a second device.
			id++
			d := activity(tl, w, id, r)
			d["device_name"] = "Apple Watch Ultra"
			d["external_id"] = fmt.Sprintf("apple_%d", id)
			e.Add("activity", w.Start, d, activityShape)
		}
	}
	return e.Lines()
}

func activity(tl *synth.Timeline, w synth.Workout, id int64, r *synth.Rand) synth.Doc {
	sp := sports[w.Sport]
	loc := tl.ZoneAt(w.Start)
	lt := w.Start.In(loc)
	_, off := lt.Zone()
	moving := math.Round(w.Moving().Seconds())
	name := map[int]string{0: "Morning", 1: "Lunch", 2: "Afternoon", 3: "Evening", 4: "Night"}[min(lt.Hour()/5, 4)] + " " + sp[0]
	doc := synth.Doc{
		"resource_state": 2, "athlete": synth.Doc{"id": 134815, "resource_state": 1}, "name": name,
		"distance": synth.Round(w.DistanceM, 1), "moving_time": moving, "elapsed_time": math.Round(w.End.Sub(w.Start).Seconds()),
		"total_elevation_gain": synth.Round(w.AscentM, 1), "type": sp[1], "sport_type": sp[0], "workout_type": nil, "id": id,
		"external_id": fmt.Sprintf("garmin_push_%d", id), "upload_id": id * 7,
		"start_date": w.Start.UTC().Format(time.RFC3339),
		// Strava writes the local wall time with a Z.
		"start_date_local": lt.Format("2006-01-02T15:04:05") + "Z",
		"timezone":         fmt.Sprintf("(GMT%s) %s", lt.Format("-07:00"), loc.String()), "utc_offset": off,
		"achievement_count": 0, "kudos_count": r.Intn(12), "comment_count": 0, "athlete_count": 1, "photo_count": 0,
		"device_name": "Garmin Forerunner 965", "trainer": w.Indoor, "commute": false, "manual": false, "private": false,
		"flagged": false, "gear_id": nil, "has_heartrate": true, "average_heartrate": w.AvgHR, "max_heartrate": w.MaxHR,
		"total_photo_count": 0, "has_kudoed": false,
	}
	if moving > 0 && w.DistanceM > 0 {
		doc["average_speed"] = synth.Round(w.DistanceM/moving, 3)
		doc["max_speed"] = synth.Round(w.DistanceM/moving*1.6, 3)
	}
	if w.AvgCadence > 0 {
		doc["average_cadence"] = synth.Round(w.AvgCadence/2, 1)
	}
	if w.Sport == synth.SportCycle {
		doc["average_watts"] = synth.Round(w.AvgPowerW, 1)
		doc["kilojoules"] = synth.Round(w.AvgPowerW*moving/1000, 1)
		doc["device_watts"] = true
	}
	if w.Sport.Outdoor() && !w.Indoor {
		var pts [][2]float64
		for t := w.Start; t.Before(w.End); t = t.Add(30 * time.Second) {
			lat, lon, _, _ := tl.Route(w, t)
			pts = append(pts, [2]float64{lat, lon})
		}
		doc["start_latlng"] = []any{pts[0][0], pts[0][1]}
		doc["end_latlng"] = []any{pts[len(pts)-1][0], pts[len(pts)-1][1]}
		doc["map"] = synth.Doc{"id": fmt.Sprintf("a%d", id), "summary_polyline": polyline(pts), "resource_state": 2}
		doc["elev_high"] = 60.0
		doc["elev_low"] = 32.0
	} else {
		doc["start_latlng"] = []any{}
		doc["end_latlng"] = []any{}
		doc["map"] = synth.Doc{"id": fmt.Sprintf("a%d", id), "summary_polyline": "", "resource_state": 2}
	}
	return doc
}

// polyline encodes points with the Encoded Polyline Algorithm Format that
// PolylineMap documents.
func polyline(pts [][2]float64) string {
	var b strings.Builder
	var plat, plon int64
	enc := func(v int64) {
		v <<= 1
		if v < 0 {
			v = ^v
		}
		for v >= 0x20 {
			b.WriteByte(byte((0x20 | (v & 0x1f)) + 63))
			v >>= 5
		}
		b.WriteByte(byte(v + 63))
	}
	for _, p := range pts {
		lat, lon := int64(math.Round(p[0]*1e5)), int64(math.Round(p[1]*1e5))
		enc(lat - plat)
		enc(lon - plon)
		plat, plon = lat, lon
	}
	return b.String()
}
