// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "apple_health"

// iso is how an iOS client writes dates: ISO8601DateFormatter with fractional
// seconds, in UTC.
const iso = "2006-01-02T15:04:05.000Z07:00"

var (
	quantityShape = synth.Shape{
		Required: []string{"quantityType", "startDate", "value", "unit"},
		Optional: []string{"endDate", "sourceName", "sourceBundleId", "device", "deviceModel", "productType", "wasUserEntered", "metadata", "uuid"},
		Numbers:  map[string][2]float64{"value": {0, 1e5}},
		Times:    []string{"startDate", "endDate"}, Start: "startDate", End: "endDate",
		Units: []string{"unit"}, Strings: []string{"sourceName", "device"}, Enums: []string{"quantityType"}, ID: "uuid",
		TimeLayout: iso,
	}
	categoryShape = synth.Shape{
		Required: []string{"categoryType", "startDate", "value"},
		Optional: []string{"endDate", "sourceName", "metadata", "uuid"},
		Times:    []string{"startDate", "endDate"}, Start: "startDate", End: "endDate",
		Enums: []string{"categoryType"}, Strings: []string{"sourceName"}, ID: "uuid", TimeLayout: iso,
	}
	workoutShape = synth.Shape{
		Required: []string{"workoutActivityType", "startDate", "endDate"},
		Optional: []string{"durationS", "totalEnergyBurned", "totalDistance", "averageHeartRate", "sourceName", "metadata", "uuid"},
		Numbers:  map[string][2]float64{"durationS": {0, 86400}, "totalEnergyBurned": {0, 20000}, "totalDistance": {0, 300000}, "averageHeartRate": {30, 230}},
		Counts:   []string{"totalDistance"},
		Times:    []string{"startDate", "endDate"}, Start: "startDate", End: "endDate",
		Enums: []string{"workoutActivityType"}, ID: "uuid", TimeLayout: iso,
	}
	correlationShape = synth.Shape{
		Required: []string{"correlationType", "startDate", "objects"},
		Optional: []string{"endDate", "sourceName", "metadata", "uuid"},
		Times:    []string{"startDate"}, Enums: []string{"correlationType"}, ID: "uuid", TimeLayout: iso,
	}
	routeShape = synth.Shape{
		Required: []string{"latitude", "longitude", "timestamp"},
		Optional: []string{"altitude", "speed", "course", "horizontalAccuracy", "verticalAccuracy", "workoutUUID"},
		Numbers:  map[string][2]float64{"latitude": {-90, 90}, "longitude": {-180, 180}, "speed": {0, 100}},
		Times:    []string{"timestamp"}, TimeLayout: iso,
	}
	ecgShape = synth.Shape{
		Required: []string{"classification", "startDate", "samplingFrequency", "numberOfVoltageMeasurements"},
		Optional: []string{"averageHeartRate", "symptomsStatus", "uuid"},
		Numbers:  map[string][2]float64{"averageHeartRate": {30, 250}, "samplingFrequency": {1, 1000}},
		Times:    []string{"startDate"}, ID: "uuid", TimeLayout: iso,
	}
	heartbeatShape = synth.Shape{
		Required: []string{"startDate", "heartbeats"},
		Optional: []string{"uuid", "sourceName"},
		Times:    []string{"startDate"}, ID: "uuid", TimeLayout: iso,
	}
)

// Lines renders tl as the NDJSON lines an iOS client uploads, one data type
// per X-OHS-Type, with the document scenarios of tl's options.
func Lines(tl *synth.Timeline) []synth.Line {
	g := NewGenerator(tl)
	if tl.Opt.Has(synth.UnitVariants) {
		g.Units = ImperialUnits
	}
	e := synth.NewEmitter(tl, Slug)
	e.UnknownType = "HKQuantityTypeIdentifierFutureMetric"
	for _, typ := range Types() {
		g.Each(typ, func(s Sample) {
			addSample(e, tl, s)
		})
	}
	routes(e, tl)
	ecgs(e, tl)
	heartbeats(e, tl)
	return e.Lines()
}

func device(s Source, d synth.Doc) {
	d["sourceName"] = s.Name
	d["sourceBundleId"] = s.Bundle
	if s.DeviceName != "" {
		d["device"] = s.DeviceName
		d["deviceModel"] = s.Model
	}
	if s.ProductType != "" {
		d["productType"] = s.ProductType
	}
}

func meta(s Sample) map[string]any {
	md := map[string]any{}
	if s.TZ != "" {
		md["HKTimeZone"] = s.TZ
	}
	for _, m := range s.Meta {
		md[m.Key] = m.Value
	}
	return md
}

func addSample(e *synth.Emitter, tl *synth.Timeline, s Sample) {
	d := synth.Doc{"uuid": s.UUID, "startDate": s.Start.UTC().Format(iso), "endDate": s.End.UTC().Format(iso)}
	device(s.Src, d)
	if md := meta(s); len(md) > 0 {
		d["metadata"] = md
	}
	d["wasUserEntered"] = s.UserEntered
	day := s.Start
	switch s.Kind {
	case KindQuantity:
		d["quantityType"] = s.Type
		d["value"] = s.Value
		d["unit"] = s.Unit
		e.Add(s.Type, day, d, quantityShape)
	case KindCategory:
		d["categoryType"] = s.Type
		d["value"] = s.CatRaw
		e.Add(s.Type, day, d, categoryShape)
	case KindCorrelation:
		d["correlationType"] = s.Type
		var objs []any
		for _, c := range s.Children {
			cd := synth.Doc{"uuid": c.UUID, "sampleType": c.Type, "startDate": c.Start.UTC().Format(iso), "endDate": c.End.UTC().Format(iso),
				"value": c.Value, "unit": c.Unit}
			objs = append(objs, cd)
		}
		d["objects"] = objs
		e.Add(s.Type, day, d, correlationShape)
	case KindWorkout:
		wd := s.Workout
		d["workoutActivityType"] = workoutRaw[strings.TrimPrefix(wd.Activity, prefixWorkout)]
		d["durationS"] = synth.Round(wd.Duration.Seconds(), 3)
		energy, dist := wd.TotalEnergyKcal, wd.TotalDistanceKM*1000
		if !wd.OldStyle {
			energy, dist = wd.W.EnergyKcal, wd.W.DistanceM
		}
		if energy > 0 {
			d["totalEnergyBurned"] = energy
			d["totalEnergyBurnedUnit"] = "kcal"
		}
		if dist > 0 {
			d["totalDistance"] = dist
			d["totalDistanceUnit"] = "m"
		}
		if !wd.OldStyle && wd.W.AvgHR > 0 {
			d["averageHeartRate"] = wd.W.AvgHR
			d["maxHeartRate"] = wd.W.MaxHR
		}
		if !wd.OldStyle {
			md, _ := d["metadata"].(map[string]any)
			if md == nil {
				md = map[string]any{}
			}
			md["HKIndoorWorkout"] = "0"
			if wd.IndoorFlag {
				md["HKIndoorWorkout"] = "1"
			}
			if wd.ElevationCM > 0 {
				md["HKElevationAscended"] = strconv.FormatFloat(wd.ElevationCM/100, 'f', -1, 64) + " m"
			}
			d["metadata"] = md
		}
		e.Add("HKWorkoutTypeIdentifier", day, d, workoutShape)
	}
}

// routes writes one line per GPS point of every outdoor workout.
func routes(e *synth.Emitter, tl *synth.Timeline) {
	step := time.Duration(float64(5*time.Second) / tl.Opt.Volume)
	if step < time.Second {
		step = time.Second
	}
	for _, w := range tl.Workouts {
		if w.Indoor || !w.Sport.Outdoor() || !tl.Worn(w.Start) {
			continue
		}
		for t := w.Start; t.Before(w.End); t = t.Add(step) {
			if w.Paused(t) {
				continue
			}
			lat, lon, ele, spd := tl.Route(w, t)
			e.Add("HKWorkoutRouteTypeIdentifier", t, synth.Doc{"workoutUUID": w.ID, "timestamp": t.UTC().Format(iso), "latitude": lat, "longitude": lon,
				"altitude": ele, "speed": spd, "course": float64(t.Unix() % 360), "horizontalAccuracy": 4.0, "verticalAccuracy": 3.0}, routeShape)
		}
	}
}

// ecgs writes each recording's metadata line and its voltage lines.
func ecgs(e *synth.Emitter, tl *synth.Timeline) {
	for _, x := range tl.ECGs {
		if !tl.Worn(x.Start) {
			continue
		}
		e.Add("HKElectrocardiogramType", x.Start, synth.Doc{"uuid": x.ID, "startDate": x.Start.UTC().Format(iso),
			"endDate": x.Start.Add(time.Duration(len(x.MicroVolts)/x.HZ) * time.Second).UTC().Format(iso), "classification": ecgRaw[x.Classification],
			"averageHeartRate": x.AvgHR, "samplingFrequency": float64(x.HZ), "numberOfVoltageMeasurements": len(x.MicroVolts),
			"symptomsStatus": 1, "sourceName": "ECG"}, ecgShape)
		for i, v := range x.MicroVolts {
			if i%8 != 0 && tl.Opt.Volume < 60 {
				// Every eighth sample keeps the default dataset small; the
				// Huge scenario writes all of them.
				continue
			}
			e.Raw(synth.Line{DataType: "HKElectrocardiogramType", Day: x.Start.UTC().Truncate(24 * time.Hour),
				JSON: synth.Marshal(map[string]any{"uuid": x.ID, "timeSinceSampleStart": float64(i) / float64(x.HZ), "value": v / 1e6})})
		}
	}
}

// heartbeats writes a beat to beat series for each night, as the watch takes
// during HRV readings.
func heartbeats(e *synth.Emitter, tl *synth.Timeline) {
	for _, s := range tl.Sleeps {
		if s.Nap || !tl.Worn(s.Start) {
			continue
		}
		start := s.Start.Add(time.Hour)
		var beats []any
		var t float64
		for k := 0; k < 70; k++ {
			bpm := tl.HeartRate(start.Add(time.Duration(t * float64(time.Second))))
			beats = append(beats, map[string]any{"timeSinceSeriesStart": synth.Round(t, 3), "precededByGap": k == 40})
			t += 60/bpm + 0.02*float64(k%3-1)
		}
		e.Add("HKDataTypeIdentifierHeartbeatSeries", start, synth.Doc{"uuid": s.ID, "startDate": start.UTC().Format(iso), "heartbeats": beats, "sourceName": "Apple Watch"}, heartbeatShape)
	}
}
