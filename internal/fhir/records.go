// SPDX-License-Identifier: AGPL-3.0-only

package fhir

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // a daily summary's zone must resolve on a host without a zone database

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
)

// Code systems of the Observations made from records other than a single
// quantity sample.
const (
	// RecordTypeSystem codes the kind of record an Observation was made
	// from: blood_pressure, sleep_session, workout, daily_summary,
	// ecg_recording, nutrition_log.
	RecordTypeSystem = Namespace + "/CodeSystem/record-type"
	// ComponentSystem codes the components: the REST field each one comes
	// from (deep_sleep_seconds, distance_m...).
	ComponentSystem = Namespace + "/CodeSystem/observation-component"
	// WorkoutActivitySystem codes a workout's canonical activity type
	// (running, cycling...).
	WorkoutActivitySystem = Namespace + "/CodeSystem/workout-activity"
)

// Record type codes.
const (
	RecordBloodPressure = "blood_pressure"
	RecordSleepSession  = "sleep_session"
	RecordWorkout       = "workout"
	RecordDailySummary  = "daily_summary"
)

// LOINC codes of the record mappings.
const (
	LOINCBloodPressurePanel = "85354-9" // Blood pressure panel with all children optional
	LOINCSystolic           = "8480-6"  // Systolic blood pressure
	LOINCDiastolic          = "8462-4"  // Diastolic blood pressure
	LOINCSleepDuration      = "93832-4" // Sleep duration
	LOINCExerciseDuration   = "55411-3" // Exercise duration
	LOINCExerciseActivity   = "73985-4" // Exercise activity
	loincCaloriesBurned     = "41981-2" // Calories burned
	loincCalories24h        = "41979-6" // Calories burned in 24 hour Calculated
	loincActivityMeanHR     = "55425-3" // Heart rate unspecified time mean by Pedometer
	loincActivityMaxHR      = "55426-1" // Heart rate unspecified time maximum by Pedometer
	loincSteps24h           = "41950-7" // Number of steps in 24 hour Measured
	loincHRV                = "80404-7" // R-R interval.standard deviation (Heart rate variability)
	loincRestingHR          = "40443-4" // Heart rate --resting
	loincRespiratoryRate    = "9279-1"  // Respiratory rate
	loincSpO2               = "59408-5" // Oxygen saturation in Arterial blood by Pulse oximetry
)

// Id prefixes: an Observation id is one of them and a stored row id.
const (
	PanelIDPrefix   = "bp-"
	SleepIDPrefix   = "sleep-"
	WorkoutIDPrefix = "workout-"
	DailyIDPrefix   = "daily-"
)

var idPrefixes = []struct {
	prefix string
	kind   read.Kind
	panel  bool
}{
	{ObservationIDPrefix, read.KindQuantity, false},
	{PanelIDPrefix, read.KindQuantity, true},
	{SleepIDPrefix, read.KindSleep, false},
	{WorkoutIDPrefix, read.KindWorkout, false},
	{DailyIDPrefix, read.KindDaily, false},
	{ECGIDPrefix, read.KindECG, false},
	{NutritionIDPrefix, read.KindNutrition, false},
}

// RecordID is the stable id of the Observation made from r.
func RecordID(r read.ObservationRecord) string {
	switch r.Kind {
	case read.KindQuantity:
		if r.Partner != nil {
			return PanelIDPrefix + strconv.FormatInt(r.Sample.ID, 10)
		}
		return ObservationID(r.Sample.ID)
	case read.KindSleep:
		return SleepIDPrefix + strconv.FormatInt(r.Sleep.ID, 10)
	case read.KindWorkout:
		return WorkoutIDPrefix + strconv.FormatInt(r.Workout.ID, 10)
	case read.KindDaily:
		return DailyIDPrefix + strconv.FormatInt(r.Daily.ID, 10)
	case read.KindECG:
		return ECGIDPrefix + strconv.FormatInt(r.ECG.ID, 10)
	case read.KindNutrition:
		return NutritionIDPrefix + strconv.FormatInt(r.Nutrition.ID, 10)
	}
	return ""
}

// ParseID reverses RecordID: the kind of record, whether it is a blood
// pressure panel (whose row id is the systolic sample's), and the row id.
func ParseID(s string) (kind read.Kind, panel bool, id int64, ok bool) {
	for _, p := range idPrefixes {
		if rest, found := strings.CutPrefix(s, p.prefix); found {
			id, ok := parseRowID(rest)
			return p.kind, p.panel, id, ok
		}
	}
	return 0, false, 0, false
}

func parseRowID(s string) (int64, bool) {
	if s == "" || s[0] == '+' || s[0] == '-' {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}

// Component is a FHIR Observation.component.
type Component struct {
	Code                 CodeableConcept  `json:"code"`
	ValueQuantity        *Quantity        `json:"valueQuantity,omitempty"`
	ValueCodeableConcept *CodeableConcept `json:"valueCodeableConcept,omitempty"`
	ValueSampledData     *SampledData     `json:"valueSampledData,omitempty"`
	DataAbsentReason     *CodeableConcept `json:"dataAbsentReason,omitempty"`
}

// FromRecord maps one record of a merged observation list to an Observation.
func FromRecord(user uuid.UUID, r read.ObservationRecord) Observation {
	switch r.Kind {
	case read.KindSleep:
		return FromSleepSession(user, *r.Sleep)
	case read.KindWorkout:
		return FromWorkout(user, *r.Workout)
	case read.KindDaily:
		return FromDailySummary(user, *r.Daily)
	case read.KindECG:
		return FromECG(user, *r.ECG)
	case read.KindNutrition:
		return FromNutritionLog(user, *r.Nutrition)
	}
	if r.Partner != nil {
		return FromBloodPressure(user, *r.Sample, *r.Partner)
	}
	return FromQuantitySample(user, *r.Sample)
}

// newObservation fills what every Observation has from a record's
// provenance.
func newObservation(user uuid.UUID, id string, p read.Provenance, externalID *string) Observation {
	o := Observation{
		ResourceType: "Observation",
		ID:           id,
		Status:       "final",
		Subject:      Reference{Reference: "Patient/" + user.String()},
		Issued:       instant(p.IngestedAt),
		Meta:         &Meta{Source: Namespace + "/provider/" + p.Provider},
	}
	if src := deref(p.Source); src != "" {
		o.Meta.Tag = []Coding{{System: SourceSystem, Code: src}}
	}
	if ext := deref(externalID); ext != "" {
		o.Identifier = []Identifier{{System: externalIDSystem(p.Provider), Value: ext}}
	}
	o.Device = deviceRef(user, read.ProvenanceDevice(p))
	return o
}

func externalIDSystem(provider string) string {
	return Namespace + "/provider/" + provider + "/external-id"
}

func categories(cat string) []CodeableConcept {
	if cat == "" {
		return nil
	}
	return []CodeableConcept{{
		Coding: []Coding{{System: ObservationCategory, Code: cat, Display: categoryDisplay[cat]}},
		Text:   categoryDisplay[cat],
	}}
}

// effective sets effectivePeriod when end is after start, else
// effectiveDateTime.
func (o *Observation) effective(start time.Time, end *time.Time) {
	if end != nil && end.After(start) {
		o.EffectivePeriod = &Period{Start: instant(start), End: instant(*end)}
		return
	}
	o.EffectiveDateTime = instant(start)
}

// value is a quantity in a UCUM unit, or why there is none: a NaN or
// infinite value has a data absent reason instead.
func value(v float64, unit, ucum string) (*Quantity, *CodeableConcept) {
	switch {
	case math.IsNaN(v):
		return nil, absent("not-a-number", "Not a Number (NaN)")
	case math.IsInf(v, 1):
		return nil, absent("positive-infinity", "Positive Infinity (PINF)")
	case math.IsInf(v, -1):
		return nil, absent("negative-infinity", "Negative Infinity (NINF)")
	}
	q := &Quantity{Value: &v, Unit: unit}
	if ucum != "" {
		q.System, q.Code = UCUM, ucum
	}
	return q, nil
}

// field is one numeric REST field mapped to a component: its name in the
// component code system, an optional LOINC code, a text, and its unit.
type field struct {
	name, loinc, text string
	unit, ucum        string
	v                 *float64
}

func intField(name, loinc, text, unit, ucum string, v *int) field {
	f := field{name: name, loinc: loinc, text: text, unit: unit, ucum: ucum}
	if v != nil {
		x := float64(*v)
		f.v = &x
	}
	return f
}

// components maps the fields that have a value; a field without one is left
// out.
func components(fields ...field) []Component {
	var out []Component
	for _, f := range fields {
		if f.v == nil {
			continue
		}
		var codings []Coding
		if f.loinc != "" {
			codings = append(codings, Coding{System: LOINC, Code: f.loinc})
		}
		codings = append(codings, Coding{System: ComponentSystem, Code: f.name})
		c := Component{Code: CodeableConcept{Coding: codings, Text: f.text}}
		c.ValueQuantity, c.DataAbsentReason = value(*f.v, f.unit, f.ucum)
		out = append(out, c)
	}
	return out
}

func recordCode(loinc, record, text string) CodeableConcept {
	var codings []Coding
	if loinc != "" {
		codings = append(codings, Coding{System: LOINC, Code: loinc})
	}
	return CodeableConcept{Coding: append(codings, Coding{System: RecordTypeSystem, Code: record}), Text: text}
}

// FromBloodPressure maps a systolic sample and the diastolic sample paired
// with it (same user, provider, start, end, source and device) to one blood
// pressure panel Observation, with the two values as its components, as the
// R4 bp profile requires.
func FromBloodPressure(user uuid.UUID, sys, dia read.CodedSample) Observation {
	o := newObservation(user, PanelIDPrefix+strconv.FormatInt(sys.ID, 10), sys.Provenance, sys.ExternalID)
	o.Device = deviceRef(user, read.SampleDevice(sys.Sample))
	if ext := deref(dia.ExternalID); ext != "" {
		o.Identifier = append(o.Identifier, Identifier{System: externalIDSystem(dia.Provider), Value: ext})
	}
	if dia.IngestedAt.After(sys.IngestedAt) {
		o.Issued = instant(dia.IngestedAt)
	}
	o.Category = categories(CategoryVitalSigns)
	o.effective(sys.StartDate, sys.EndDate)

	// The profile wants both values in mm[Hg]; a half in another unit (or
	// none) keeps its value but the panel claims no LOINC code or profile.
	inMMHg := true
	half := func(s read.CodedSample, loinc, text string) Component {
		c := Component{Code: CodeableConcept{
			Coding: []Coding{{System: LOINC, Code: loinc}, {System: QuantityTypeSystem, Code: s.Type}},
			Text:   text,
		}}
		ucum := ucumCode(s)
		c.ValueQuantity, c.DataAbsentReason = value(s.Value, deref(s.Unit), ucum)
		if c.ValueQuantity != nil && ucum != "mm[Hg]" {
			inMMHg = false
		}
		return c
	}
	o.Component = []Component{
		half(sys, LOINCSystolic, "systolic blood pressure"),
		half(dia, LOINCDiastolic, "diastolic blood pressure"),
	}
	if inMMHg {
		o.Code = recordCode(LOINCBloodPressurePanel, RecordBloodPressure, "blood pressure")
		o.Meta.Profile = []string{profileBase + "vitalsigns", profileBase + "bp"}
	} else {
		o.Code = recordCode("", RecordBloodPressure, "blood pressure")
	}
	return o
}

// FromSleepSession maps one sleep session to an Observation of its total
// sleep duration, with the stage durations and the night's averages as
// components.
func FromSleepSession(user uuid.UUID, s read.SleepSession) Observation {
	o := newObservation(user, SleepIDPrefix+strconv.FormatInt(s.ID, 10), s.Provenance, s.ExternalID)
	o.Code = recordCode(LOINCSleepDuration, RecordSleepSession, "sleep session")
	o.effective(s.BedtimeStart, s.BedtimeEnd)
	if s.TotalSleepSeconds != nil {
		o.ValueQuantity, _ = value(float64(*s.TotalSleepSeconds), "s", "s")
	} else {
		o.DataAbsentReason = absent("unknown", "Unknown")
	}
	o.Component = components(
		intField("deep_sleep_seconds", "", "deep sleep duration", "s", "s", s.DeepSleepSeconds),
		intField("rem_sleep_seconds", "", "REM sleep duration", "s", "s", s.REMSleepSeconds),
		intField("light_sleep_seconds", "", "light sleep duration", "s", "s", s.LightSleepSeconds),
		intField("awake_seconds", "", "awake duration", "s", "s", s.AwakeSeconds),
		field{name: "sleep_score", text: "sleep score", unit: "score", ucum: "{score}", v: s.SleepScore},
		field{name: "hrv_avg_ms", loinc: loincHRV, text: "average heart rate variability", unit: "ms", ucum: "ms", v: s.HRVAvgMs},
		field{name: "resting_hr", loinc: loincRestingHR, text: "resting heart rate", unit: "bpm", ucum: "/min", v: s.RestingHR},
		field{name: "respiratory_rate", loinc: loincRespiratoryRate, text: "average respiratory rate", unit: "bpm", ucum: "/min", v: s.RespiratoryRate},
		field{name: "spo2_avg", loinc: loincSpO2, text: "average oxygen saturation", unit: "percent", ucum: "%", v: s.SpO2Avg},
	)
	return o
}

// FromWorkout maps one workout to an Observation of its duration, with its
// activity type and its measurements as components.
func FromWorkout(user uuid.UUID, w read.Workout) Observation {
	o := newObservation(user, WorkoutIDPrefix+strconv.FormatInt(w.ID, 10), w.Provenance, w.ExternalID)
	text := "workout"
	if w.ActivityType != "" {
		text = strings.ReplaceAll(w.ActivityType, "_", " ") + " workout"
	}
	o.Code = recordCode(LOINCExerciseDuration, RecordWorkout, text)
	// A workout is an activity measure of the Physical Activity IG (its
	// duration, 55411-3), so it carries the IG's category too.
	o.Category = append(categories(CategoryActivity), physicalActivityCategory())
	o.effective(w.StartDate, w.EndDate)
	if w.DurationS != nil {
		o.ValueQuantity, _ = value(float64(*w.DurationS), "s", "s")
	} else {
		o.DataAbsentReason = absent("unknown", "Unknown")
	}
	if w.ActivityType != "" {
		o.Component = append(o.Component, Component{
			Code: CodeableConcept{
				Coding: []Coding{{System: LOINC, Code: LOINCExerciseActivity}, {System: ComponentSystem, Code: "activity_type"}},
				Text:   "activity type",
			},
			ValueCodeableConcept: &CodeableConcept{
				Coding: []Coding{{System: WorkoutActivitySystem, Code: w.ActivityType}},
				Text:   strings.ReplaceAll(w.ActivityType, "_", " "),
			},
		})
	}
	o.Component = append(o.Component, components(
		field{name: "active_energy_kcal", loinc: loincCaloriesBurned, text: "active energy", unit: "kcal", ucum: "kcal", v: w.ActiveEnergyKcal},
		field{name: "distance_m", text: "distance", unit: "m", ucum: "m", v: w.DistanceM},
		field{name: "avg_heart_rate", loinc: loincActivityMeanHR, text: "average heart rate", unit: "bpm", ucum: "/min", v: w.AvgHeartRate},
		field{name: "max_heart_rate", loinc: loincActivityMaxHR, text: "maximum heart rate", unit: "bpm", ucum: "/min", v: w.MaxHeartRate},
		field{name: "avg_speed_mps", text: "average speed", unit: "m/s", ucum: "m/s", v: w.AvgSpeedMps},
		field{name: "elevation_gain_m", text: "elevation gain", unit: "m", ucum: "m", v: w.ElevationGainM},
		field{name: "vo2_max", text: "VO2 max", unit: "mL/kg/min", ucum: "mL/kg/min", v: w.VO2Max},
	)...)
	return o
}

// FromDailySummary maps one daily summary to an Observation over the user's
// local day, with each metric the provider gave as a component.
func FromDailySummary(user uuid.UUID, d read.DailySummary) Observation {
	o := newObservation(user, DailyIDPrefix+strconv.FormatInt(d.ID, 10), d.Provenance, nil)
	o.Code = recordCode("", RecordDailySummary, "daily summary")
	o.Category = categories(CategoryActivity)
	if start, end, ok := localDay(d.Date, d.Metadata); ok {
		o.EffectivePeriod = &Period{Start: instant(start), End: instant(end)}
	}
	o.Component = components(
		intField("step_count", loincSteps24h, "steps", "steps", "{steps}", d.StepCount),
		field{name: "active_kcal", loinc: loincCaloriesBurned, text: "active energy", unit: "kcal", ucum: "kcal", v: d.ActiveKcal},
		field{name: "total_kcal", loinc: loincCalories24h, text: "total energy", unit: "kcal", ucum: "kcal", v: d.TotalKcal},
		field{name: "distance_m", text: "distance", unit: "m", ucum: "m", v: d.DistanceM},
		intField("floors_climbed", "", "floors climbed", "floors", "{floors}", d.FloorsClimbed),
		field{name: "resting_hr", loinc: loincRestingHR, text: "resting heart rate", unit: "bpm", ucum: "/min", v: d.RestingHR},
		field{name: "hrv_avg_ms", loinc: loincHRV, text: "average heart rate variability", unit: "ms", ucum: "ms", v: d.HRVAvgMs},
		field{name: "readiness_score", text: "readiness score", unit: "score", ucum: "{score}", v: d.ReadinessScore},
		field{name: "activity_score", text: "activity score", unit: "score", ucum: "{score}", v: d.ActivityScore},
		field{name: "sleep_score", text: "sleep score", unit: "score", ucum: "{score}", v: d.SleepScore},
		field{name: "recovery_score", text: "recovery score", unit: "score", ucum: "{score}", v: d.RecoveryScore},
		field{name: "strain_score", text: "strain score", unit: "score", ucum: "{score}", v: d.StrainScore},
	)
	if len(o.Component) == 0 {
		o.DataAbsentReason = absent("unknown", "Unknown")
	}
	return o
}

// localDay is the interval from midnight to the next midnight of date
// (YYYY-MM-DD) in the zone named by the record's "timezone" metadata, else
// UTC: 23 or 25 hours on a daylight saving change.
func localDay(date string, metadata json.RawMessage) (time.Time, time.Time, bool) {
	day, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	loc := time.UTC
	var md struct {
		Timezone string `json:"timezone"`
	}
	if json.Unmarshal(metadata, &md) == nil && md.Timezone != "" {
		if l, err := time.LoadLocation(md.Timezone); err == nil {
			loc = l
		}
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	return start, time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, loc), true
}
