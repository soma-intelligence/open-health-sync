// SPDX-License-Identifier: AGPL-3.0-only

// Package omron renders a synthetic timeline as Omron Wellness API blood
// pressure and weight measurements.
//
// Source: Omron's public partner API pages (Omron Partner API for Europe
// and for US, omronhealthcare-ohi.atlassian.net, space API), the
// measurement tables and example responses. The pages disagree with
// themselves, and the lines follow every variant they show: the tables type
// systolic, diastolic and pulse as strings while the examples write numbers;
// dateTimeUtcOffset is "+00:00" in one example, "-21600" (seconds) in
// another and "hh:mm:ss" in the table; dateTime is UTC and dateTimeLocal the
// wall time, both without offset; weight is always in kg and "-1.0" stands
// for a value the scale did not measure. Each line is one entry of
// result.bloodPressure or result.weight.
package omron

import (
	"fmt"
	"strconv"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "omron"

const layout = "2006-01-02T15:04:05"

var bpShape = synth.Shape{
	ID:       "id",
	Required: []string{"id", "dateTime", "dateTimeLocal", "dateTimeUtcOffset", "systolic", "diastolic", "bloodPressureUnits", "pulse", "pulseUnits", "deviceType"},
	Optional: []string{"possibleAfib"},
	Numbers:  map[string][2]float64{"systolic": {60, 260}, "diastolic": {30, 160}, "pulse": {30, 200}},
	Times:    []string{"dateTime", "dateTimeLocal"},
	Units:    []string{"bloodPressureUnits", "pulseUnits"},
	Strings:  []string{"deviceType"},
	// UTC without an offset, as dateTime is written.
	TimeLayout: layout,
}

var weightShape = synth.Shape{
	ID:         "id",
	Required:   []string{"id", "dateTime", "dateTimeLocal", "dateTimeUtcOffset", "weight", "weightPref", "deviceType"},
	Optional:   []string{"idExt", "restingMetabolism", "visceralFatLevel", "skeletalMusclePercentage", "bmiValue", "bodyFatPercentage"},
	Numbers:    map[string][2]float64{"weight": {2, 300}, "bodyFatPercentage": {3, 70}, "bmiValue": {10, 70}},
	Times:      []string{"dateTime", "dateTimeLocal"},
	Units:      []string{"weightPref"},
	Strings:    []string{"deviceType"},
	TimeLayout: layout,
}

// offset writes a UTC offset in one of the three documented spellings.
func offset(sec, variant int) string {
	sign := "+"
	if sec < 0 {
		sign, sec = "-", -sec
	}
	switch variant % 3 {
	case 0:
		return fmt.Sprintf("%s%02d:%02d", sign, sec/3600, sec%3600/60)
	case 1:
		if sign == "-" {
			return strconv.Itoa(-sec)
		}
		return strconv.Itoa(sec)
	default:
		return fmt.Sprintf("%s%02d:%02d:00", sign, sec/3600, sec%3600/60)
	}
}

// Lines renders tl as the measurements the connector stores, one per line.
func Lines(tl *synth.Timeline) []synth.Line {
	e := synth.NewEmitter(tl, Slug)
	e.UnknownType = "temperature"
	for i, b := range tl.BPs {
		if b.At.After(tl.Opt.Now) {
			continue
		}
		_, off := tl.Local(b.At).Zone()
		doc := synth.Doc{
			"id": b.At.UnixMilli(), "dateTime": b.At.UTC().Format(layout), "dateTimeLocal": tl.Local(b.At).Format(layout),
			"dateTimeUtcOffset": offset(off, i), "bloodPressureUnits": "mmHg", "pulseUnits": "bpm",
			"deviceType": synth.Pick(synth.NewRand(tl.Opt.Seed, fmt.Sprint("omron", i)), "M700 Intelli IT", "7 Series Upper Arm"),
		}
		// Numbers as the examples write them, strings as the tables type them.
		vals := map[string]float64{"systolic": b.Systolic, "diastolic": b.Diastolic, "pulse": b.Pulse}
		for k, v := range vals {
			if i%2 == 0 {
				doc[k] = int(v)
			} else {
				doc[k] = strconv.Itoa(int(v))
			}
		}
		if b.Irregular {
			doc["possibleAfib"] = true
		} else if i%3 == 0 {
			doc["possibleAfib"] = false
		}
		e.Add("bloodpressure", b.At, doc, bpShape)
	}
	for i, w := range tl.Weighs {
		if w.At.After(tl.Opt.Now) {
			continue
		}
		_, off := tl.Local(w.At).Zone()
		pref := "kg"
		if tl.Opt.Has(synth.UnitVariants) && i%2 == 1 {
			// The weight stays in kg whatever the preference.
			pref = "lbs"
		}
		doc := synth.Doc{
			"id": w.At.UnixMilli(), "idExt": w.At.UnixMilli(), "dateTime": w.At.UTC().Format(layout),
			"dateTimeLocal": tl.Local(w.At).Format(layout), "dateTimeUtcOffset": offset(off, i+1),
			"weight": strconv.FormatFloat(synth.Round(w.WeightKG, 1), 'f', 1, 64), "weightPref": pref,
			"bmiValue": strconv.FormatFloat(synth.Round(w.BMI, 2), 'f', 2, 64), "deviceType": "HN-300T2 Intelli IT",
			"restingMetabolism": "-1.0", "visceralFatLevel": "-1.0", "skeletalMusclePercentage": "-1.0", "bodyFatPercentage": "-1.0",
		}
		if i%2 == 0 {
			// A body composition scale fills what the plain scale leaves at -1.0.
			doc["deviceType"] = "HBF-702T"
			doc["bodyFatPercentage"] = strconv.FormatFloat(synth.Round(w.BodyFat*100, 1), 'f', 1, 64)
			doc["visceralFatLevel"] = strconv.FormatFloat(synth.Round(w.VisceralF, 1), 'f', 1, 64)
			doc["skeletalMusclePercentage"] = strconv.FormatFloat(synth.Round(w.MuscleKG/w.WeightKG*100*0.6, 1), 'f', 1, 64)
			doc["restingMetabolism"] = strconv.FormatFloat(synth.Round(370+21.6*w.LeanKG, 0), 'f', 1, 64)
		}
		e.Add("weight", w.At, doc, weightShape)
	}
	return e.Lines()
}
