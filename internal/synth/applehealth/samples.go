// SPDX-License-Identifier: AGPL-3.0-only

// Package applehealth renders a synthetic timeline as Apple Health data: the
// "Export All Health Data" archive of the iPhone Health app (export.zip) and
// the NDJSON lines an iOS client sends to POST /v1/ingest/apple-health.
//
// Both are written from the same HealthKit samples, built one type at a time
// and one day at a time, so a year at one heart rate a second streams to disk
// without being held in memory.
package applehealth

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Kind is the HealthKit class of a sample.
type Kind int

// Sample kinds.
const (
	KindQuantity Kind = iota
	KindCategory
	KindCorrelation
	KindWorkout
)

// Source is the app and device that wrote a sample.
type Source struct {
	Name, Bundle, Version string
	// Device fields, empty for a sample without a device.
	DeviceName, Manufacturer, Model, Hardware, Software string
	// ProductType is the model the writing app ran on (HKSourceRevision).
	ProductType string
}

// Sample is one HealthKit object.
type Sample struct {
	Kind       Kind
	Type       string // full identifier, HKQuantityTypeIdentifierHeartRate
	Start, End time.Time
	Created    time.Time
	Value      float64
	Unit       string
	// Category value: raw integer and export name.
	CatRaw  int
	CatName string
	Src     Source
	Meta    []MetaEntry
	UUID    string
	// UserEntered is HKWasUserEntered.
	UserEntered bool
	// TZ is the HKTimeZone metadata, empty when the writer did not set it.
	TZ string
	// HRV beats, for an SDNN sample from the watch.
	Beats []Beat
	// Children of a correlation.
	Children []Sample
	// Workout fields.
	Workout *WorkoutData
}

// MetaEntry is one metadata key and its value as the export prints it.
type MetaEntry struct{ Key, Value string }

// Beat is one InstantaneousBeatsPerMinute of an HRV sample.
type Beat struct {
	At  time.Time
	BPM float64
}

// WorkoutData is what a workout holds besides its sample fields.
type WorkoutData struct {
	Activity             string // HKWorkoutActivityTypeRunning
	Duration             time.Duration
	TotalDistanceKM      float64 // old style attributes, zero when absent
	TotalEnergyKcal      float64
	Events               []Event
	Stats                []Stat
	Zones                []Zone
	RoutePath            string // /workout-routes/route_...gpx, empty without a route
	Activities           []Activity
	W                    synth.Workout
	IndoorFlag           bool
	OldStyle             bool // attributes instead of statistics, as third-party apps write
	ElevationCM, AvgMETs float64
	WeatherTempF         float64
	WeatherHumidity      float64
}

// Event is a WorkoutEvent.
type Event struct {
	Type     string
	At       time.Time
	Duration time.Duration
}

// Stat is a WorkoutStatistics element.
type Stat struct {
	Type                   string
	Start, End             time.Time
	Sum, Avg, Min, Max     float64
	HasSum, HasAvg, HasMin bool
	Unit                   string
}

// Zone is a WorkoutZone of the heart rate zone group.
type Zone struct {
	Min, Max float64 // zero Min: open below
	Duration time.Duration
}

// Activity is a WorkoutActivity (a leg of a multisport workout).
type Activity struct {
	UUID       string
	Start, End time.Time
}

// Generator builds the HealthKit samples of a timeline.
type Generator struct {
	TL *synth.Timeline
	// Units picks the unit of each quantity type: metric when nil.
	Units func(q quantity) altUnit
	// Watch, Phone and the app sources.
	Watch, Phone, Health, Scale, BP, CGM, Ring, Food, RunApp, OldWatch Source
	first                                                              string
}

// NewGenerator returns a generator with the persona's devices.
func NewGenerator(tl *synth.Timeline) *Generator {
	first := strings.Fields(tl.P.Name)[0]
	r := synth.NewRand(tl.Opt.Seed, "apple/sources")
	g := &Generator{TL: tl, first: first}
	// Device names are what a person calls their devices; the typographic
	// apostrophe is the one iOS writes.
	g.Watch = Source{Name: first + "’s Apple Watch", Bundle: "com.apple.health." + r.UUID(), Version: "11.0",
		DeviceName: "Apple Watch", Manufacturer: "Apple Inc.", Model: "Watch", Hardware: "Watch7,1", Software: "11.0", ProductType: "Watch7,1"}
	g.OldWatch = Source{Name: first + "’s Apple Watch", Bundle: "com.apple.health." + r.UUID(), Version: "10.6",
		DeviceName: "Apple Watch", Manufacturer: "Apple Inc.", Model: "Watch", Hardware: "Watch6,1", Software: "10.6", ProductType: "Watch6,1"}
	g.Phone = Source{Name: first + "’s iPhone", Bundle: "com.apple.health." + r.UUID(), Version: "26.0",
		DeviceName: "iPhone", Manufacturer: "Apple Inc.", Model: "iPhone", Hardware: "iPhone17,1", Software: "26.0", ProductType: "iPhone17,1"}
	g.Health = Source{Name: "Health", Bundle: "com.apple.Health", Version: "26.0", ProductType: "iPhone17,1"}
	g.Scale = Source{Name: "Smart Scale", Bundle: "com.example.scale", Version: "7.2.1", DeviceName: "Body Scale",
		Manufacturer: "Example Scales", Model: "Body Scale", Hardware: "2", Software: "1.4.0", ProductType: "iPhone17,1"}
	g.BP = Source{Name: "BP Monitor", Bundle: "com.example.bp", Version: "3.1", DeviceName: "Cuff", Manufacturer: "Example Medical",
		Model: "Upper Arm Cuff", ProductType: "iPhone17,1"}
	g.CGM = Source{Name: "CGM App", Bundle: "com.example.cgm", Version: "5.0", DeviceName: "Sensor", Manufacturer: "Example Diabetes",
		Model: "CGM", Software: "1.0", ProductType: "iPhone17,1"}
	g.Ring = Source{Name: "Ring App", Bundle: "com.example.ring", Version: "6.4", DeviceName: "Ring", Manufacturer: "Example Rings",
		Model: "Ring Gen 4", Hardware: "4", ProductType: "iPhone17,1"}
	g.Food = Source{Name: "Food Log", Bundle: "com.example.foodlog", Version: "12.3", ProductType: "iPhone17,1"}
	g.RunApp = Source{Name: "Run App", Bundle: "com.example.run", Version: "400.1", ProductType: "iPhone17,1"}
	return g
}

// unit returns the unit and the value converted for a quantity type, value
// given in the type's metric unit.
func (g *Generator) unit(q quantity, v float64) (string, float64) {
	if g.Units != nil {
		if a := g.Units(q); a.Unit != "" && a.Unit != q.Unit {
			return a.Unit, v*a.Factor + a.Offset
		}
	}
	return q.Unit, v
}

func quantityOf(id string) quantity {
	for _, q := range quantities {
		if q.ID == id {
			return q
		}
	}
	panic("applehealth: unknown quantity " + id)
}

// Types returns every type the generator writes, quantity types first, in
// the order an export lists them.
func Types() []string {
	var out []string
	for _, q := range quantities {
		out = append(out, prefixQuantity+q.ID)
	}
	out = append(out, prefixCategory+"SleepAnalysis", prefixCategory+"AppleStandHour", prefixCategory+"MindfulSession",
		prefixCategory+"HandwashingEvent", prefixCategory+"ToothbrushingEvent", prefixCategory+"HighHeartRateEvent",
		prefixCategory+"LowHeartRateEvent", prefixCategory+"IrregularHeartRhythmEvent", prefixCategory+"EnvironmentalAudioExposureEvent",
		prefixCategory+"HeadphoneAudioExposureEvent", prefixCategory+"LowCardioFitnessEvent", prefixCategory+"MenstrualFlow",
		prefixCategory+"IntermenstrualBleeding", prefixCategory+"OvulationTestResult", prefixCategory+"CervicalMucusQuality",
		prefixCategory+"SexualActivity", prefixCategory+"AppleWalkingSteadinessEvent")
	for _, s := range symptomTypes {
		out = append(out, prefixCategory+s)
	}
	out = append(out, prefixCorrelation+"BloodPressure", prefixCorrelation+"Food", "HKWorkoutTypeIdentifier")
	return out
}

// Each calls fn with every sample of one type, day by day.
func (g *Generator) Each(typ string, fn func(Sample)) {
	for _, d := range g.TL.Days {
		if d.Missing {
			continue
		}
		g.day(typ, d, fn)
	}
}

func (g *Generator) rand(typ string, d synth.Day) *synth.Rand {
	return synth.NewRand(g.TL.Opt.Seed, "apple/"+typ+"/"+d.Date+fmt.Sprint(d.Index))
}

// mk returns a sample of type typ written by src, its creation date a few
// seconds to hours after its end, as HealthKit stamps them.
func (g *Generator) mk(r *synth.Rand, kind Kind, typ string, s, e time.Time, src Source) Sample {
	return Sample{Kind: kind, Type: typ, Start: s, End: e, Created: e.Add(time.Duration(r.Range(1, 600)) * time.Second), Src: src, UUID: r.UUID()}
}

func (g *Generator) quantity(r *synth.Rand, id string, s, e time.Time, v float64, src Source, fn func(Sample)) {
	q := quantityOf(id)
	x := g.mk(r, KindQuantity, prefixQuantity+id, s, e, src)
	x.Unit, x.Value = g.unit(q, v)
	x.Value = synth.Round(x.Value, 6)
	if src.Model == "Watch" || src.Model == "iPhone" {
		// The watch stamps the zone it was in on what it measures overnight
		// or in workouts; elsewhere the field is absent.
		if _, ok := g.TL.SleepAt(s); ok {
			x.TZ = g.TL.ZoneAt(s).String()
		}
	}
	fn(x)
}

func (g *Generator) day(typ string, d synth.Day, fn func(Sample)) {
	tl := g.TL
	r := g.rand(typ, d)
	vol := tl.Opt.Volume
	every := func(base time.Duration) time.Duration {
		step := time.Duration(float64(base) / vol)
		if step < time.Second {
			step = time.Second
		}
		return step
	}
	worn := func(t time.Time) bool { return tl.Worn(t) }
	id := strings.TrimPrefix(typ, prefixQuantity)

	switch typ {
	case prefixQuantity + "HeartRate":
		for t := d.Start; t.Before(d.End); {
			step := every(5 * time.Minute)
			_, inW := tl.WorkoutAt(t)
			if inW {
				step = every(5 * time.Second)
			}
			if worn(t) {
				x := g.mk(r, KindQuantity, typ, t, t, g.Watch)
				x.Unit, x.Value = "count/min", tl.HeartRate(t)
				ctx := "1"
				if inW {
					ctx = "2"
				}
				x.Meta = []MetaEntry{{"HKMetadataKeyHeartRateMotionContext", ctx}}
				if _, ok := tl.SleepAt(t); ok {
					x.TZ = tl.ZoneAt(t).String()
				}
				fn(x)
				if tl.Opt.Has(synth.MultiDevice) && !inW && r.Chance(0.5) {
					// The ring's reading of the same minute.
					y := g.mk(r, KindQuantity, typ, t.Add(-30*time.Second), t.Add(30*time.Second), g.Ring)
					y.Unit, y.Value = "count/min", tl.HeartRate(t)+synth.Round(r.Norm(0, 1.5), 0)
					fn(y)
				}
			}
			t = t.Add(step + time.Duration(r.Intn(3))*time.Second)
		}
	case prefixQuantity + "StepCount", prefixQuantity + "DistanceWalkingRunning":
		// The watch and the phone each count, over different intervals; the
		// Health app picks one per minute, an importer must not add them.
		for _, src := range []Source{g.Watch, g.Phone} {
			chunk := every(10 * time.Minute)
			if src.Model == "iPhone" {
				chunk = every(7 * time.Minute)
			}
			for t := d.Start; t.Before(d.End); t = t.Add(chunk) {
				e := t.Add(chunk)
				if e.After(d.End) {
					e = d.End
				}
				steps := tl.Steps(t, e)
				if steps == 0 || (src.Model == "iPhone" && r.Chance(0.35)) {
					continue
				}
				if src.Model == "iPhone" {
					steps = math.Round(steps * r.Range(0.85, 1))
				}
				if id == "StepCount" {
					g.quantity(r, id, t, e, steps, src, fn)
				} else {
					g.quantity(r, id, t, e, synth.Round(tl.Distance(t, e)/1000*steps/math.Max(tl.Steps(t, e), 1), 5), src, fn)
				}
			}
		}
	case prefixQuantity + "ActiveEnergyBurned":
		for t := d.Start; t.Before(d.End); t = t.Add(every(5 * time.Minute)) {
			e := t.Add(every(5 * time.Minute))
			if v := tl.ActiveEnergy(t, e); v > 0 && worn(t) {
				g.quantity(r, id, t, e, v, g.Watch, fn)
			}
		}
	case prefixQuantity + "BasalEnergyBurned":
		for t := d.Start; t.Before(d.End); t = t.Add(every(30 * time.Minute)) {
			e := t.Add(every(30 * time.Minute))
			if e.After(d.End) {
				e = d.End
			}
			if worn(t) {
				g.quantity(r, id, t, e, tl.BasalEnergy(t, e), g.Watch, fn)
			}
		}
	case prefixQuantity + "FlightsClimbed":
		for k := 0; k < 3+r.Intn(5); k++ {
			s := synth.AtLocal(d, r.Range(8, 21)).Truncate(time.Second)
			g.quantity(r, id, s, s.Add(time.Duration(r.Range(10, 60))*time.Second), float64(1+r.Intn(3)), g.Phone, fn)
		}
	case prefixQuantity + "AppleExerciseTime":
		for t := d.Start; t.Before(d.End); t = t.Add(time.Minute) {
			if _, ok := tl.WorkoutAt(t); ok && worn(t) {
				g.quantity(r, id, t, t.Add(time.Minute), 1, g.Watch, fn)
			}
		}
	case prefixQuantity + "AppleStandTime", prefixQuantity + "TimeInDaylight":
		for h := 7.0; h < 22; h += 1 {
			s := synth.AtLocal(d, h)
			if _, ok := tl.SleepAt(s); ok || !worn(s) || r.Chance(0.3) {
				continue
			}
			for k := 0; k < 1+r.Intn(3); k++ {
				ss := s.Add(time.Duration(k*5) * time.Minute)
				g.quantity(r, id, ss, ss.Add(5*time.Minute), float64(1+r.Intn(5)), g.Watch, fn)
			}
		}
	case prefixQuantity + "RestingHeartRate", prefixQuantity + "WalkingHeartRateAverage":
		v := tl.P.RestingHR + synth.Round(r.Norm(0, 2), 0)
		if id == "WalkingHeartRateAverage" {
			v = tl.P.RestingHR + 35 + synth.Round(r.Norm(0, 4), 0)
		}
		g.quantity(r, id, d.Start, d.End.Add(-time.Second), v, g.Watch, fn)
	case prefixQuantity + "HeartRateVariabilitySDNN":
		for k := 0; k < 4; k++ {
			s := synth.AtLocal(d, 2+float64(k)*5.5+r.Range(0, 1)).Truncate(time.Second)
			if !worn(s) {
				continue
			}
			x := g.mk(r, KindQuantity, typ, s, s.Add(time.Minute), g.Watch)
			x.Unit, x.Value = g.unit(quantityOf(id), tl.HRVSDNN(s))
			x.Value = synth.Round(x.Value, 4)
			for b := s; b.Before(x.End); b = b.Add(time.Duration(60/tl.HeartRate(b)*1000) * time.Millisecond) {
				x.Beats = append(x.Beats, Beat{At: b, BPM: tl.HeartRate(b)})
			}
			fn(x)
		}
	case prefixQuantity + "HeartRateVariabilityRMSSD", prefixQuantity + "AppleSleepingBreathingDisturbances", prefixQuantity + "AppleSleepingWristTemperature":
		for _, s := range tl.Sleeps {
			if s.Day != d.Index || s.Nap {
				continue
			}
			var v float64
			switch id {
			case "HeartRateVariabilityRMSSD":
				v = s.HRV
			case "AppleSleepingBreathingDisturbances":
				v = synth.Round(r.Range(0, 4), 2)
			default:
				v = synth.Round(35.6+s.TempDelta, 3)
			}
			g.quantity(r, id, s.Start, s.End, v, g.Watch, fn)
		}
	case prefixQuantity + "OxygenSaturation", prefixQuantity + "RespiratoryRate":
		for _, s := range tl.Sleeps {
			if s.Day != d.Index {
				continue
			}
			step := every(45 * time.Minute)
			if id == "RespiratoryRate" {
				step = every(5 * time.Minute)
			}
			for t := s.Start.Add(10 * time.Minute); t.Before(s.End); t = t.Add(step) {
				if !worn(t) {
					continue
				}
				v := tl.SpO2(t)
				if id == "RespiratoryRate" {
					v = tl.Respiration(t)
				}
				x := g.mk(r, KindQuantity, typ, t, t, g.Watch)
				x.Unit, x.Value = quantityOf(id).Unit, v
				x.TZ = tl.ZoneAt(t).String()
				fn(x)
			}
		}
	case prefixQuantity + "VO2Max":
		for _, w := range tl.Workouts {
			if w.Day == d.Index && (w.Sport == synth.SportRun || w.Sport == synth.SportWalk || w.Sport == synth.SportHike) && !w.Indoor {
				x := g.mk(r, KindQuantity, typ, w.End, w.End, g.Watch)
				x.Unit, x.Value = "mL/min·kg", synth.Round(tl.P.VO2Max+r.Norm(0, 0.8), 2)
				x.Meta = []MetaEntry{{"HKVO2MaxTestType", "2"}}
				fn(x)
			}
		}
	case prefixQuantity + "WorkoutEffortScore", prefixQuantity + "EstimatedWorkoutEffortScore", prefixQuantity + "PhysicalEffort",
		prefixQuantity + "HeartRateRecoveryOneMinute":
		for _, w := range tl.Workouts {
			if w.Day != d.Index {
				continue
			}
			switch id {
			case "PhysicalEffort":
				for t := w.Start; t.Before(w.End); t = t.Add(every(time.Minute)) {
					g.quantity(r, id, t, t.Add(every(time.Minute)), synth.Round(2+10*(tl.HeartRate(t)-tl.P.RestingHR)/(tl.P.MaxHR-tl.P.RestingHR), 1), g.Watch, fn)
				}
			case "HeartRateRecoveryOneMinute":
				if w.Sport == synth.SportRun || w.Sport == synth.SportHIIT {
					g.quantity(r, id, w.End, w.End, synth.Round(r.Range(20, 42), 3), g.Watch, fn)
				}
			default:
				g.quantity(r, id, w.Start, w.End, float64(1+r.Intn(10)), g.Watch, fn)
			}
		}
	case prefixQuantity + "CyclingCadence", prefixQuantity + "CyclingPower", prefixQuantity + "CyclingSpeed", prefixQuantity + "DistanceCycling",
		prefixQuantity + "RunningSpeed", prefixQuantity + "RunningPower", prefixQuantity + "RunningStrideLength",
		prefixQuantity + "RunningVerticalOscillation", prefixQuantity + "RunningGroundContactTime",
		prefixQuantity + "DistanceSwimming", prefixQuantity + "SwimmingStrokeCount", prefixQuantity + "DistanceRowing":
		g.workoutSeries(r, id, d, fn)
	case prefixQuantity + "WalkingSpeed", prefixQuantity + "WalkingStepLength", prefixQuantity + "WalkingDoubleSupportPercentage",
		prefixQuantity + "WalkingAsymmetryPercentage":
		for _, b := range tl.Bouts {
			if b.Start.Before(d.Start) || !b.Start.Before(d.End) || b.End.Sub(b.Start) < 5*time.Minute {
				continue
			}
			q := quantityOf(id)
			var v float64
			switch id {
			case "WalkingSpeed":
				v = synth.Round(b.Cadence*tl.P.HeightCM*0.415/100*60/1000, 3)
			case "WalkingStepLength":
				v = synth.Round(tl.P.HeightCM*0.415, 0)
			default:
				v = synth.Round(r.Range(q.Lo, q.Hi), 3)
			}
			g.quantity(r, id, b.Start, b.End, v, g.Phone, fn)
		}
	case prefixQuantity + "AppleWalkingSteadiness", prefixQuantity + "SixMinuteWalkTestDistance":
		if d.Index%7 == 0 {
			q := quantityOf(id)
			g.quantity(r, id, d.Start.Add(-7*24*time.Hour), d.Start, synth.Round(r.Range(q.Lo, q.Hi), 3), g.Phone, fn)
		}
	case prefixQuantity + "StairAscentSpeed", prefixQuantity + "StairDescentSpeed":
		for k := 0; k < 2; k++ {
			s := synth.AtLocal(d, r.Range(8, 20)).Truncate(time.Second)
			q := quantityOf(id)
			g.quantity(r, id, s, s.Add(time.Duration(r.Range(4, 20))*time.Second), synth.Round(r.Range(q.Lo, q.Hi), 3), g.Phone, fn)
		}
	case prefixQuantity + "EnvironmentalAudioExposure":
		for t := synth.AtLocal(d, 8); t.Before(synth.AtLocal(d, 22)); t = t.Add(every(30 * time.Minute)) {
			if worn(t) {
				g.quantity(r, id, t, t.Add(every(30*time.Minute)), synth.Round(r.Range(38, 80), 3), g.Watch, fn)
			}
		}
	case prefixQuantity + "HeadphoneAudioExposure":
		for k := 0; k < 3; k++ {
			s := synth.AtLocal(d, r.Range(8, 21)).Truncate(time.Second)
			g.quantity(r, id, s, s.Add(time.Duration(r.Range(60, 1800))*time.Second), synth.Round(r.Range(55, 85), 3), g.Phone, fn)
		}
	case prefixQuantity + "BloodPressureSystolic", prefixQuantity + "BloodPressureDiastolic":
		// Correlation children also appear on their own (the DTD says so).
		for _, b := range tl.BPs {
			if b.At.Before(d.Start) || !b.At.Before(d.End) {
				continue
			}
			v := b.Systolic
			if id == "BloodPressureDiastolic" {
				v = b.Diastolic
			}
			g.quantity(synth.NewRand(tl.Opt.Seed, "apple/bp/"+b.At.String()+id), id, b.At, b.At, v, g.BP, fn)
		}
	case prefixQuantity + "BloodGlucose":
		if tl.P.CGM {
			for t := d.Start; t.Before(d.End); t = t.Add(every(5 * time.Minute)) {
				x := g.mk(r, KindQuantity, typ, t, t, g.CGM)
				x.Unit, x.Value = g.unit(quantityOf(id), tl.Glucose(t))
				x.Value = synth.Round(x.Value, 4)
				x.Meta = []MetaEntry{{"HKBloodGlucoseMealTime", "1"}}
				fn(x)
			}
		} else if d.Index%3 == 0 {
			s := synth.AtLocal(d, 7.2)
			x := g.mk(r, KindQuantity, typ, s, s, g.Health)
			x.Unit, x.Value = g.unit(quantityOf(id), tl.Glucose(s))
			x.Value = synth.Round(x.Value, 4)
			x.UserEntered = true
			fn(x)
		}
	case prefixQuantity + "BodyMass", prefixQuantity + "BodyMassIndex", prefixQuantity + "BodyFatPercentage", prefixQuantity + "LeanBodyMass":
		for _, w := range tl.Weighs {
			if w.At.Before(d.Start) || !w.At.Before(d.End) {
				continue
			}
			var v float64
			switch id {
			case "BodyMass":
				v = w.WeightKG
			case "BodyMassIndex":
				v = w.BMI
			case "BodyFatPercentage":
				v = w.BodyFat
			default:
				v = w.LeanKG
			}
			x := g.mk(synth.NewRand(tl.Opt.Seed, "apple/w/"+w.At.String()+id), KindQuantity, typ, w.At, w.At, g.Scale)
			x.Unit, x.Value = g.unit(quantityOf(id), v)
			x.Value = synth.Round(x.Value, 4)
			x.Meta = []MetaEntry{{"HKMetadataKeySyncIdentifier", "scale-" + w.At.UTC().Format("20060102T150405")}, {"HKMetadataKeySyncVersion", "1"}}
			fn(x)
		}
	case prefixQuantity + "Height":
		if d.Index == 0 {
			x := g.mk(r, KindQuantity, typ, d.Start.Add(9*time.Hour), d.Start.Add(9*time.Hour), g.Health)
			x.Unit, x.Value = g.unit(quantityOf(id), tl.P.HeightCM)
			x.UserEntered = true
			fn(x)
		}
	case prefixQuantity + "DietaryWater":
		for _, dr := range tl.Drinks {
			if dr.At.Before(d.Start) || !dr.At.Before(d.End) {
				continue
			}
			x := g.mk(r, KindQuantity, typ, dr.At, dr.At, g.Food)
			x.Unit, x.Value = g.unit(quantityOf(id), dr.ML)
			x.Value = synth.Round(x.Value, 4)
			fn(x)
		}
	case prefixQuantity + "DietaryEnergyConsumed", prefixQuantity + "DietaryProtein", prefixQuantity + "DietaryCarbohydrates",
		prefixQuantity + "DietaryFatTotal", prefixQuantity + "DietaryFatSaturated", prefixQuantity + "DietaryFiber",
		prefixQuantity + "DietarySugar", prefixQuantity + "DietarySodium", prefixQuantity + "DietaryCaffeine":
		for _, m := range tl.Meals {
			if m.At.Before(d.Start) || !m.At.Before(d.End) {
				continue
			}
			for _, c := range g.mealSamples(m) {
				if c.Type == typ {
					fn(c)
				}
			}
		}
	case prefixCategory + "SleepAnalysis":
		g.sleep(r, d, fn)
	case prefixCategory + "AppleStandHour":
		for h := 7.0; h < 22; h++ {
			s := synth.AtLocal(d, h)
			if !worn(s) {
				continue
			}
			x := g.mk(r, KindCategory, typ, s, s.Add(time.Hour), g.Watch)
			if r.Chance(0.7) {
				x.CatRaw, x.CatName = 0, "HKCategoryValueAppleStandHourStood"
			} else {
				x.CatRaw, x.CatName = 1, "HKCategoryValueAppleStandHourIdle"
			}
			fn(x)
		}
	case prefixCategory + "MindfulSession":
		for _, m := range tl.Mindful {
			if m.Start.Before(d.Start) || !m.Start.Before(d.End) {
				continue
			}
			x := g.mk(r, KindCategory, typ, m.Start, m.End, g.Watch)
			x.CatRaw, x.CatName = 0, "HKCategoryValueNotApplicable"
			fn(x)
		}
	case prefixCategory + "HandwashingEvent", prefixCategory + "ToothbrushingEvent":
		n, dur := 4, 25.0
		if strings.HasSuffix(typ, "ToothbrushingEvent") {
			n, dur = 2, 120
		}
		for k := 0; k < n; k++ {
			s := synth.AtLocal(d, 7+float64(k)*14/float64(n)+r.Range(0, 1)).Truncate(time.Second)
			x := g.mk(r, KindCategory, typ, s, s.Add(time.Duration(dur+r.Range(-10, 30))*time.Second), g.Watch)
			x.CatRaw, x.CatName = 0, "HKCategoryValueNotApplicable"
			fn(x)
		}
	case prefixCategory + "HighHeartRateEvent", prefixCategory + "LowHeartRateEvent", prefixCategory + "IrregularHeartRhythmEvent",
		prefixCategory + "LowCardioFitnessEvent", prefixCategory + "AppleWalkingSteadinessEvent":
		if d.Index != tl.ExtremeDay {
			return
		}
		s := synth.AtLocal(d, 3)
		x := g.mk(r, KindCategory, typ, s, s.Add(10*time.Minute), g.Watch)
		x.CatRaw, x.CatName = 0, "HKCategoryValueNotApplicable"
		switch typ {
		case prefixCategory + "HighHeartRateEvent":
			x.Meta = []MetaEntry{{"HKHeartRateEventThreshold", "120 count/min"}}
		case prefixCategory + "LowHeartRateEvent":
			x.Meta = []MetaEntry{{"HKHeartRateEventThreshold", "40 count/min"}}
		case prefixCategory + "LowCardioFitnessEvent":
			x.CatRaw, x.CatName = 1, "HKCategoryValueLowCardioFitnessEventLowFitness"
			x.Meta = []MetaEntry{{"HKVO2MaxValue", "29.1 mL/min·kg"}, {"HKLowCardioFitnessEventThreshold", "31.2 mL/min·kg"}}
		case prefixCategory + "AppleWalkingSteadinessEvent":
			x.CatRaw, x.CatName = 1, "HKCategoryValueAppleWalkingSteadinessEventInitialLow"
		}
		fn(x)
	case prefixCategory + "EnvironmentalAudioExposureEvent", prefixCategory + "HeadphoneAudioExposureEvent":
		if d.Index%5 != 2 {
			return
		}
		s := synth.AtLocal(d, r.Range(10, 20)).Truncate(time.Second)
		x := g.mk(r, KindCategory, typ, s, s.Add(3*time.Minute), g.Watch)
		x.CatRaw, x.CatName = 1, "HKCategoryValueEnvironmentalAudioExposureEventMomentaryLimit"
		if strings.HasSuffix(typ, "HeadphoneAudioExposureEvent") {
			x.CatName = "HKCategoryValueHeadphoneAudioExposureEventSevenDayLimit"
		}
		x.Meta = []MetaEntry{{"HKMetadataKeyAudioExposureLevel", fmt.Sprintf("%.1f dBASPL", r.Range(90, 100))}}
		fn(x)
	case prefixCategory + "MenstrualFlow":
		for _, f := range tl.Flows {
			if f.Day != d.Index {
				continue
			}
			x := g.mk(r, KindCategory, typ, f.Start, f.Start, g.Health)
			v := menstrualFlow[f.Level]
			x.CatRaw, x.CatName = v.Raw, v.Name
			start := "0"
			if f.First {
				start = "1"
			}
			x.Meta = []MetaEntry{{"HKMenstrualCycleStart", start}}
			x.UserEntered = true
			fn(x)
		}
	case prefixCategory + "IntermenstrualBleeding", prefixCategory + "OvulationTestResult", prefixCategory + "CervicalMucusQuality",
		prefixCategory + "SexualActivity":
		if tl.P.CycleDays == 0 || d.Index%tl.P.CycleDays != 13 {
			return
		}
		s := synth.AtLocal(d, 8)
		x := g.mk(r, KindCategory, typ, s, s, g.Health)
		x.UserEntered = true
		switch typ {
		case prefixCategory + "OvulationTestResult":
			x.CatRaw, x.CatName = 2, "HKCategoryValueOvulationTestResultLuteinizingHormoneSurge"
		case prefixCategory + "CervicalMucusQuality":
			x.CatRaw, x.CatName = 5, "HKCategoryValueCervicalMucusQualityEggWhite"
		case prefixCategory + "SexualActivity":
			x.CatRaw, x.CatName = 0, "HKCategoryValueNotApplicable"
			x.Meta = []MetaEntry{{"HKSexualActivityProtectionUsed", "1"}}
		default:
			x.CatRaw, x.CatName = 0, "HKCategoryValueNotApplicable"
		}
		fn(x)
	case prefixCorrelation + "BloodPressure":
		for _, b := range tl.BPs {
			if b.At.Before(d.Start) || !b.At.Before(d.End) {
				continue
			}
			x := g.mk(r, KindCorrelation, typ, b.At, b.At, g.BP)
			g.Each2(prefixQuantity+"BloodPressureSystolic", d, b.At, func(c Sample) { x.Children = append(x.Children, c) })
			g.Each2(prefixQuantity+"BloodPressureDiastolic", d, b.At, func(c Sample) { x.Children = append(x.Children, c) })
			x.Meta = []MetaEntry{{"HKWasUserEntered", "0"}}
			fn(x)
		}
	case prefixCorrelation + "Food":
		for _, m := range tl.Meals {
			if m.At.Before(d.Start) || !m.At.Before(d.End) {
				continue
			}
			x := g.mk(r, KindCorrelation, typ, m.At, m.At, g.Food)
			x.Children = g.mealSamples(m)
			x.Meta = []MetaEntry{{"HKFoodType", m.Name}, {"HKFoodMeal", m.Kind}}
			fn(x)
		}
	case "HKWorkoutTypeIdentifier":
		g.workouts(r, d, fn)
	default:
		if strings.HasPrefix(typ, prefixCategory) {
			// Symptoms, logged now and then in the Health app.
			name := strings.TrimPrefix(typ, prefixCategory)
			for i, s := range symptomTypes {
				if s == name && d.Index%7 == i%7 {
					st := synth.AtLocal(d, 9)
					x := g.mk(r, KindCategory, typ, st, st.Add(4*time.Hour), g.Health)
					v := severity[2+r.Intn(3)]
					x.CatRaw, x.CatName = v.Raw, v.Name
					x.UserEntered = true
					fn(x)
				}
			}
			return
		}
		q := quantityOf(id)
		// A type the timeline does not drive: an entry on one day in seven,
		// rotating so a fortnight holds every type.
		idx := 0
		for i, qq := range quantities {
			if qq.ID == id {
				idx = i
			}
		}
		if d.Index%7 != idx%7 {
			return
		}
		s := synth.AtLocal(d, r.Range(8, 20)).Truncate(time.Minute)
		e := s
		if q.Cumulative {
			e = s.Add(30 * time.Minute)
		}
		v := synth.Round(q.Lo+(q.Hi-q.Lo)*r.Float(), 3)
		if q.Cumulative && q.Unit == "count" {
			v = math.Max(1, math.Round(v))
		}
		x := g.mk(r, KindQuantity, typ, s, e, g.Health)
		x.Unit, x.Value = g.unit(q, v)
		x.Value = synth.Round(x.Value, 6)
		x.UserEntered = true
		fn(x)
	}
}

// Each2 calls fn with the samples of typ of day d at instant at only.
func (g *Generator) Each2(typ string, d synth.Day, at time.Time, fn func(Sample)) {
	g.day(typ, d, func(s Sample) {
		if s.Start.Equal(at) {
			fn(s)
		}
	})
}

func (g *Generator) mealSamples(m synth.Meal) []Sample {
	r := synth.NewRand(g.TL.Opt.Seed, "apple/meal/"+m.ID)
	var out []Sample
	add := func(id string, v float64) {
		if v <= 0 {
			return
		}
		q := quantityOf(id)
		x := g.mk(r, KindQuantity, prefixQuantity+id, m.At, m.At, g.Food)
		x.Unit, x.Value = g.unit(q, v)
		x.Value = synth.Round(x.Value, 4)
		x.Meta = []MetaEntry{{"HKFoodType", m.Name}, {"HKFoodMeal", m.Kind}}
		out = append(out, x)
	}
	add("DietaryEnergyConsumed", m.EnergyKc)
	add("DietaryProtein", m.ProteinG)
	add("DietaryCarbohydrates", m.CarbsG)
	add("DietaryFatTotal", m.FatG)
	add("DietaryFatSaturated", m.SatFatG)
	add("DietaryFiber", m.FiberG)
	add("DietarySugar", m.SugarG)
	add("DietarySodium", m.SodiumMg)
	add("DietaryCaffeine", m.CaffMg)
	return out
}

func (g *Generator) workoutSeries(r *synth.Rand, id string, d synth.Day, fn func(Sample)) {
	tl := g.TL
	for _, w := range tl.Workouts {
		if w.Day != d.Index {
			continue
		}
		var want synth.Sport
		switch {
		case strings.HasPrefix(id, "Cycling"), id == "DistanceCycling":
			want = synth.SportCycle
		case strings.HasPrefix(id, "Running"):
			want = synth.SportRun
		case id == "DistanceSwimming", id == "SwimmingStrokeCount":
			want = synth.SportSwim
		case id == "DistanceRowing":
			want = synth.SportRow
		}
		if w.Sport != want {
			continue
		}
		step := time.Duration(float64(10*time.Second) / tl.Opt.Volume)
		if step < time.Second {
			step = time.Second
		}
		if id == "DistanceSwimming" || id == "SwimmingStrokeCount" {
			step = 50 * time.Second // one length
		}
		for t := w.Start; t.Before(w.End); t = t.Add(step) {
			if w.Paused(t) {
				continue
			}
			_, _, _, speed := tl.Route(w, t)
			var v float64
			switch id {
			case "CyclingCadence":
				v = synth.Round(w.AvgCadence+r.Norm(0, 3), 0)
			case "CyclingPower", "RunningPower":
				base := w.AvgPowerW
				if base == 0 {
					base = tl.P.WeightKG * 3.3
				}
				v = synth.Round(base+r.Norm(0, 12), 0)
			case "CyclingSpeed", "RunningSpeed":
				v = synth.Round(speed*3.6, 3)
			case "DistanceCycling", "DistanceRowing":
				v = speed * step.Seconds() / 1000
				if id == "DistanceRowing" {
					v *= 1000
				}
			case "RunningStrideLength":
				v = synth.Round(speed*60/w.AvgCadence, 2)
			case "RunningVerticalOscillation":
				v = synth.Round(r.Range(8, 11), 1)
			case "RunningGroundContactTime":
				v = synth.Round(r.Range(220, 290), 0)
			case "DistanceSwimming":
				v = 25
			case "SwimmingStrokeCount":
				v = float64(14 + r.Intn(6))
			}
			s, e := t, t.Add(step)
			if !quantityOf(id).Cumulative {
				e = s
			}
			x := g.mk(r, KindQuantity, prefixQuantity+id, s, e, g.Watch)
			x.Unit, x.Value = g.unit(quantityOf(id), v)
			x.Value = synth.Round(x.Value, 5)
			if id == "SwimmingStrokeCount" {
				x.Meta = []MetaEntry{{"HKSwimmingStrokeStyle", "2"}}
			}
			fn(x)
		}
	}
}

// sleep writes a night as HealthKit does: the phone's in bed sample from the
// sleep schedule and the watch's stages, each filed under its own start.
func (g *Generator) sleep(r *synth.Rand, d synth.Day, fn func(Sample)) {
	tl := g.TL
	typ := prefixCategory + "SleepAnalysis"
	for _, s := range tl.Sleeps {
		if s.Day != d.Index {
			continue
		}
		tz := tl.ZoneAt(s.Start).String()
		if !s.Nap {
			x := g.mk(r, KindCategory, typ, s.Start.Add(-5*time.Minute), s.End.Add(4*time.Minute), g.Phone)
			v := sleepValues["in_bed"]
			x.CatRaw, x.CatName, x.TZ = v.Raw, v.Name, tz
			fn(x)
		}
		for _, st := range s.Stages {
			if !tl.Worn(st.Start) {
				continue
			}
			x := g.mk(r, KindCategory, typ, st.Start, st.End, g.Watch)
			v := sleepValues[string(st.Stage)]
			x.CatRaw, x.CatName, x.TZ = v.Raw, v.Name, tz
			fn(x)
		}
		if tl.Opt.Has(synth.MultiDevice) && !s.Nap {
			// The ring app's night: unstaged, with its own bounds.
			x := g.mk(r, KindCategory, typ, s.Start.Add(7*time.Minute), s.End.Add(-3*time.Minute), g.Ring)
			v := sleepValues["unspecified"]
			x.CatRaw, x.CatName = v.Raw, v.Name
			fn(x)
		}
	}
}

func (g *Generator) workouts(r *synth.Rand, d synth.Day, fn func(Sample)) {
	tl := g.TL
	for _, w := range tl.Workouts {
		if w.Day != d.Index {
			continue
		}
		x := g.mk(r, KindWorkout, "HKWorkoutTypeIdentifier", w.Start, w.End, g.Watch)
		wd := &WorkoutData{Activity: prefixWorkout + workoutTypes[string(w.Sport)], Duration: w.Moving(), W: w, IndoorFlag: w.Indoor}
		x.Workout = wd
		x.TZ = tl.ZoneAt(w.Start).String()
		for _, p := range w.Pauses {
			wd.Events = append(wd.Events, Event{Type: "HKWorkoutEventTypePause", At: p.Start}, Event{Type: "HKWorkoutEventTypeResume", At: p.End})
		}
		// One segment per kilometre or ten minutes.
		seg := 10 * time.Minute
		if w.DistanceM > 0 {
			seg = time.Duration(float64(w.Moving()) * 1000 / w.DistanceM)
		}
		if seg < time.Minute {
			seg = time.Minute
		}
		for t := w.Start; t.Add(seg).Before(w.End) && len(wd.Events) < 400; t = t.Add(seg) {
			wd.Events = append(wd.Events, Event{Type: "HKWorkoutEventTypeSegment", At: t, Duration: seg})
		}
		wd.Stats = append(wd.Stats, Stat{Type: prefixQuantity + "ActiveEnergyBurned", Start: w.Start, End: w.End, Sum: w.EnergyKcal, HasSum: true, Unit: "kcal"},
			Stat{Type: prefixQuantity + "BasalEnergyBurned", Start: w.Start, End: w.End, Sum: tl.BasalEnergy(w.Start, w.End), HasSum: true, Unit: "kcal"},
			Stat{Type: prefixQuantity + "HeartRate", Start: w.Start, End: w.End, Avg: w.AvgHR, Min: tl.P.RestingHR + 20, Max: w.MaxHR, HasAvg: true, HasMin: true, Unit: "count/min"})
		if w.DistanceM > 0 {
			dt := prefixQuantity + "DistanceWalkingRunning"
			unit, v := "km", w.DistanceM/1000
			switch w.Sport {
			case synth.SportCycle:
				dt = prefixQuantity + "DistanceCycling"
			case synth.SportSwim:
				dt, unit, v = prefixQuantity+"DistanceSwimming", "m", w.DistanceM
			case synth.SportRow:
				dt, unit, v = prefixQuantity+"DistanceRowing", "m", w.DistanceM
			}
			wd.Stats = append(wd.Stats, Stat{Type: dt, Start: w.Start, End: w.End, Sum: synth.Round(v, 5), HasSum: true, Unit: unit})
		}
		if w.Steps > 0 {
			wd.Stats = append(wd.Stats, Stat{Type: prefixQuantity + "StepCount", Start: w.Start, End: w.End, Sum: w.Steps, HasSum: true, Unit: "count"})
		}
		// Five heart rate zones from the maximum.
		edges := []float64{0, 0.6, 0.7, 0.8, 0.9, 1.2}
		for i := 0; i < 5; i++ {
			z := Zone{Min: synth.Round(tl.P.MaxHR*edges[i], 0), Max: synth.Round(tl.P.MaxHR*edges[i+1], 0)}
			z.Duration = w.Moving() / 5
			if i == 0 {
				z.Min = 0
			}
			wd.Zones = append(wd.Zones, z)
		}
		if !w.Indoor && w.Sport.Outdoor() {
			lt := w.Start.In(tl.ZoneAt(w.Start))
			wd.RoutePath = "/workout-routes/route_" + lt.Format("2006-01-02_3.04pm") + ".gpx"
		}
		wd.ElevationCM = w.AscentM * 100
		wd.AvgMETs = synth.Round(3+9*(w.AvgHR-tl.P.RestingHR)/(tl.P.MaxHR-tl.P.RestingHR), 5)
		wd.WeatherTempF = synth.Round(r.Range(40, 80), 1)
		wd.WeatherHumidity = float64(30 + r.Intn(60))
		fn(x)

		// The same session as a third-party running app wrote it: old style
		// attributes, no device, times a few seconds off.
		if tl.Opt.Has(synth.MultiDevice) && w.Sport == synth.SportRun {
			y := g.mk(r, KindWorkout, "HKWorkoutTypeIdentifier", w.Start.Add(3*time.Second), w.End.Add(-2*time.Second), g.RunApp)
			y.Workout = &WorkoutData{Activity: prefixWorkout + "Running", Duration: w.Moving(), W: w, OldStyle: true,
				TotalDistanceKM: synth.Round(w.DistanceM/1000, 4), TotalEnergyKcal: w.EnergyKcal}
			fn(y)
		}
	}
	if tl.Opt.Has(synth.Unknown) && d.Index == 0 {
		// Every activity type the timeline does not drive, five minutes each,
		// from the Health app.
		s := synth.AtLocal(d, 6)
		for i, a := range rareWorkoutTypes {
			st := s.Add(time.Duration(i*6) * time.Minute)
			x := g.mk(r, KindWorkout, "HKWorkoutTypeIdentifier", st, st.Add(5*time.Minute), g.Health)
			x.UserEntered = true
			x.Workout = &WorkoutData{Activity: prefixWorkout + a, Duration: 5 * time.Minute, OldStyle: true, TotalEnergyKcal: 30}
			fn(x)
		}
	}
}
