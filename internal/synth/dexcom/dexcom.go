// SPDX-License-Identifier: AGPL-3.0-only

// Package dexcom renders a synthetic timeline as Dexcom API v3 estimated
// glucose values.
//
// Source: the Dexcom Developer API OpenAPI document (swaggerV3.json, EGV
// schema and the egvs_v3 example, https://developer.dexcom.com/docs/dexcomv3/endpoint-overview).
// Values are in mg/dL whatever the unit field says, 39 and 401 stand for
// below 40 and above 400 with a status, and records from a receiver carry
// no UTC offset. Each line is one record of the egvs answer.
package dexcom

import (
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "dexcom"

var egvShape = synth.Shape{
	ID: "recordId",
	Required: []string{"recordId", "systemTime", "displayTime", "transmitterTicks", "unit", "rateUnit", "displayDevice",
		"transmitterGeneration", "transmitterGenerationVariant"},
	Optional:   []string{"transmitterId", "value", "status", "trend", "trendRate", "displayApp"},
	Numbers:    map[string][2]float64{"value": {39, 401}, "trendRate": {-8, 8}},
	Counts:     []string{"transmitterTicks"},
	Times:      []string{"systemTime", "displayTime"},
	Start:      "systemTime",
	Enums:      []string{"status", "trend", "displayDevice", "transmitterGeneration"},
	Units:      []string{"unit", "rateUnit"},
	TimeLayout: time.RFC3339,
}

// device is one display that uploads the sensor's readings.
type device struct {
	display, app string
	offset       bool // mobile apps write UTC offsets, receivers do not
}

// Lines renders tl as the EGV records the connector stores, one per line.
func Lines(tl *synth.Timeline) []synth.Line {
	e := synth.NewEmitter(tl, Slug)
	r := synth.NewRand(tl.Opt.Seed, "dexcom")
	e.UnknownType = "calibrations"
	devices := []device{{"iOS", "G7", true}}
	if tl.Opt.Has(synth.MultiDevice) {
		// The same sensor read by the phone app and a receiver.
		devices = append(devices, device{"receiver", "", false})
	}
	transmitter := r.Hex(64)
	win := tl.Window()
	// A G7 sensor lasts ten days plus a grace period; the API reads every
	// five minutes whatever the volume.
	session := win.Start
	prev := 0.0
	for _, t := range tl.Every(win.Start, win.End, 5*time.Minute) {
		if !tl.Worn(t) || !t.Before(tl.Opt.Now) {
			continue
		}
		if t.Sub(session) > 10*24*time.Hour {
			session = t
			transmitter = r.Hex(64)
		}
		g := tl.Glucose(t)
		rate := 0.0
		if prev > 0 {
			rate = synth.Round((g-prev)/5, 1)
		}
		prev = g
		for _, dv := range devices {
			e.Add("egvs", t, egv(tl, r, t, g, rate, session, transmitter, dv), egvShape)
		}
	}
	return e.Lines()
}

func egv(tl *synth.Timeline, r *synth.Rand, t time.Time, g, rate float64, session time.Time, transmitter string, dv device) synth.Doc {
	local := tl.Local(t)
	doc := synth.Doc{
		"recordId": r.UUID(), "transmitterId": transmitter, "transmitterTicks": int64(t.Sub(session).Seconds()),
		"value": int(g), "status": nil, "trend": trend(rate), "trendRate": rate,
		"unit": "mg/dL", "rateUnit": "mg/dL/min", "displayDevice": dv.display,
		"transmitterGeneration": "g7", "transmitterGenerationVariant": "g715day", "displayApp": dv.app,
	}
	if dv.offset {
		doc["systemTime"] = t.UTC().Format(time.RFC3339)
		doc["displayTime"] = local.Format(time.RFC3339)
	} else {
		doc["systemTime"] = t.UTC().Format("2006-01-02T15:04:05")
		doc["displayTime"] = local.Format("2006-01-02T15:04:05")
		doc["displayApp"] = nil
		doc["transmitterGeneration"], doc["transmitterGenerationVariant"] = "g6", "g6"
	}
	// The documented sentinels for readings outside 40 to 400.
	switch {
	case g <= 40 && tl.Opt.Has(synth.Extremes):
		doc["value"], doc["status"] = 39, "low"
	case g >= 400:
		doc["value"], doc["status"] = 401, "high"
	}
	if rate > 8 || rate < -8 {
		doc["trend"], doc["trendRate"] = "rateOutOfRange", nil
	}
	return doc
}

// trend maps a rate in mg/dL/min to the documented arrows.
func trend(rate float64) string {
	switch {
	case rate >= 3:
		return "doubleUp"
	case rate >= 2:
		return "singleUp"
	case rate >= 1:
		return "fortyFiveUp"
	case rate > -1:
		return "flat"
	case rate > -2:
		return "fortyFiveDown"
	case rate > -3:
		return "singleDown"
	default:
		return "doubleDown"
	}
}
