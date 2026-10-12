// SPDX-License-Identifier: AGPL-3.0-only

// Package omh maps normalized records to Open mHealth data points: a header
// (id, creation time, schema id, provenance) and a body that follows one
// schema of the Open mHealth catalog (heart-rate, blood-pressure,
// sleep-episode...). It reads the same merged observation list as the FHIR
// output, so the two describe the same records. The mapping is in
// docs/open-mhealth.md; the tests check every body and header against the
// official schemas vendored under testdata/schemas.
package omh

import (
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/fhir"
)

// Namespace is the schema namespace of every data point.
const Namespace = "omh"

// SchemaID names the schema a body follows.
type SchemaID struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

// Provenance is the header's acquisition_provenance.
type Provenance struct {
	// SourceName is the provider the record came from (apple_health, oura...).
	SourceName string `json:"source_name"`
	// SourceDataPointID is the provider's own id of the record, when it gave one.
	SourceDataPointID string `json:"source_data_point_id,omitempty"`
	// Modality is "self-reported" for a value the user typed, "sensed" for one
	// the source says it measured, and left out when the source does not say.
	Modality string `json:"modality,omitempty"`
}

// Header is the data point header (omh:header:1.2).
type Header struct {
	ID string `json:"id"`
	// CreationDateTime is when Open Health Sync stored the record.
	CreationDateTime      string     `json:"creation_date_time"`
	SchemaID              SchemaID   `json:"schema_id"`
	AcquisitionProvenance Provenance `json:"acquisition_provenance"`
	UserID                string     `json:"user_id"`
}

// DataPoint is one Open mHealth data point (omh:data-point:1.0).
type DataPoint struct {
	Header Header         `json:"header"`
	Body   map[string]any `json:"body"`
}

// UnitValue is the unit-value shape most bodies use.
type UnitValue struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// Schema is one schema of the catalog this package emits.
type Schema struct {
	Name    string
	Version string
}

// ID is the schema's id, namespace omh.
func (s Schema) ID() SchemaID {
	return SchemaID{Namespace: Namespace, Name: s.Name, Version: s.Version}
}

// The schemas the mapping emits. Each is the newest version whose
// definitions are all in the Open mHealth schema repository; the newer
// majors only swap in IEEE 1752.1 building blocks of the same shape.
var (
	HeartRate         = Schema{"heart-rate", "2.0"}
	StepCount         = Schema{"step-count", "3.0"}
	BloodPressure     = Schema{"blood-pressure", "3.1"}
	BodyWeight        = Schema{"body-weight", "2.0"}
	BodyHeight        = Schema{"body-height", "1.0"}
	BodyMassIndex     = Schema{"body-mass-index", "2.0"}
	BodyFatPercentage = Schema{"body-fat-percentage", "1.0"}
	BodyTemperature   = Schema{"body-temperature", "3.0"}
	OxygenSaturation  = Schema{"oxygen-saturation", "2.0"}
	RespiratoryRate   = Schema{"respiratory-rate", "2.0"}
	BloodGlucose      = Schema{"blood-glucose", "3.0"}
	CaloriesBurned    = Schema{"calories-burned", "2.0"}
	SleepEpisode      = Schema{"sleep-episode", "1.1"}
	PhysicalActivity  = Schema{"physical-activity", "1.2"}
)

// quantity maps one canonical quantity type to a schema: the body member
// that holds the value, the unit Open mHealth spells, and the storage unit a
// sample must be in (a sample in another unit has no data point).
type quantity struct {
	schema      Schema
	member      string
	unit        string
	storageUnit string
	// interval: the schema wants a time interval, never a single instant.
	interval bool
	// atRest sets temporal_relationship_to_physical_activity to "at rest".
	atRest bool
}

var quantities = map[string]quantity{
	"heart_rate":           {schema: HeartRate, member: "heart_rate", unit: "beats/min", storageUnit: "bpm"},
	"resting_heart_rate":   {schema: HeartRate, member: "heart_rate", unit: "beats/min", storageUnit: "bpm", atRest: true},
	"step_count":           {schema: StepCount, member: "step_count", unit: "steps", storageUnit: "count", interval: true},
	"body_mass":            {schema: BodyWeight, member: "body_weight", unit: "kg", storageUnit: "kg"},
	"height":               {schema: BodyHeight, member: "body_height", unit: "cm", storageUnit: "cm"},
	"body_mass_index":      {schema: BodyMassIndex, member: "body_mass_index", unit: "kg/m^2", storageUnit: "kg/m2"},
	"body_fat_percentage":  {schema: BodyFatPercentage, member: "body_fat_percentage", unit: "%", storageUnit: "percent"},
	"body_temperature":     {schema: BodyTemperature, member: "body_temperature", unit: "C", storageUnit: "celsius"},
	"oxygen_saturation":    {schema: OxygenSaturation, member: "oxygen_saturation", unit: "%", storageUnit: "percent"},
	"respiratory_rate":     {schema: RespiratoryRate, member: "respiratory_rate", unit: "breaths/min", storageUnit: "bpm"},
	"blood_glucose":        {schema: BloodGlucose, member: "blood_glucose", unit: "mg/dL", storageUnit: "mg/dL"},
	"active_energy_burned": {schema: CaloriesBurned, member: "kcal_burned", unit: "kcal", storageUnit: "kcal", interval: true},
}

// Schemas lists, by name, the schemas the mapping emits.
func Schemas() []Schema {
	out := []Schema{BloodPressure, SleepEpisode, PhysicalActivity}
	for _, q := range quantities {
		if !slices.Contains(out, q.schema) {
			out = append(out, q.schema)
		}
	}
	slices.SortFunc(out, func(a, b Schema) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// SchemaByName finds a schema of Schemas by name, bare (heart-rate) or with
// its namespace (omh:heart-rate), with or without a version that matches.
func SchemaByName(s string) (Schema, bool) {
	s = strings.TrimPrefix(s, Namespace+":")
	name, version, hasVersion := strings.Cut(s, ":")
	for _, sc := range Schemas() {
		if sc.Name == name && (!hasVersion || version == sc.Version) {
			return sc, true
		}
	}
	return Schema{}, false
}

// Filter is the part of the merged observation list a set of schemas reads:
// every record the mapping turns into a data point, or those of one schema.
// It reads the diastolic type so that the blood pressure panels are in.
func Filter(only *Schema) read.ObservationFilter {
	f := read.ObservationFilter{}
	want := func(s Schema) bool { return only == nil || *only == s }
	types := []string{}
	for t, q := range quantities {
		if want(q.schema) {
			types = append(types, t)
		}
	}
	slices.Sort(types)
	switch {
	case only != nil && *only == BloodPressure:
		f.Quantities, f.PanelsOnly = true, true
	case len(types) > 0:
		f.Quantities = true
		f.Types = types
		if only == nil {
			f.Types = append(f.Types, read.Diastolic)
		}
	}
	f.Sleep = want(SleepEpisode)
	f.Workouts = want(PhysicalActivity)
	return f
}

// FromRecord maps one record of a merged observation list to a data point.
// ok is false when the record has no Open mHealth schema here: a quantity
// type outside the table, a sample in another unit, a value that is not a
// finite number, a lone half of a blood pressure reading or a daily summary.
func FromRecord(user uuid.UUID, r read.ObservationRecord) (DataPoint, bool) {
	switch r.Kind {
	case read.KindQuantity:
		if r.Partner != nil {
			return FromBloodPressure(user, *r.Sample, *r.Partner)
		}
		return FromQuantitySample(user, *r.Sample)
	case read.KindSleep:
		return FromSleepSession(user, *r.Sleep), true
	case read.KindWorkout:
		return FromWorkout(user, *r.Workout), true
	}
	return DataPoint{}, false
}

func newDataPoint(user uuid.UUID, r read.ObservationRecord, s Schema, p read.Provenance, externalID *string) DataPoint {
	return DataPoint{
		Header: Header{
			ID:               fhir.RecordID(r),
			CreationDateTime: instant(p.IngestedAt),
			SchemaID:         s.ID(),
			AcquisitionProvenance: Provenance{
				SourceName:        p.Provider,
				SourceDataPointID: deref(externalID),
			},
			UserID: user.String(),
		},
		Body: map[string]any{},
	}
}

func modality(userEntered *bool) string {
	switch {
	case userEntered == nil:
		return ""
	case *userEntered:
		return "self-reported"
	}
	return "sensed"
}

// FromQuantitySample maps one quantity sample of a type of the table.
func FromQuantitySample(user uuid.UUID, s read.CodedSample) (DataPoint, bool) {
	q, ok := quantities[s.Type]
	if !ok || deref(s.Unit) != q.storageUnit || !finite(s.Value) {
		return DataPoint{}, false
	}
	d := newDataPoint(user, read.ObservationRecord{Kind: read.KindQuantity, Sample: &s}, q.schema, s.Provenance, s.ExternalID)
	d.Header.AcquisitionProvenance.Modality = modality(s.WasUserEntered)
	d.Body[q.member] = UnitValue{Value: s.Value, Unit: q.unit}
	d.Body["effective_time_frame"] = timeFrame(s.StartDate, s.EndDate, q.interval)
	if q.atRest {
		d.Body["temporal_relationship_to_physical_activity"] = "at rest"
	}
	return d, true
}

// FromBloodPressure maps a systolic sample and its paired diastolic sample,
// both in mmHg, to one blood-pressure data point.
func FromBloodPressure(user uuid.UUID, sys, dia read.CodedSample) (DataPoint, bool) {
	if deref(sys.Unit) != "mmHg" || deref(dia.Unit) != "mmHg" || !finite(sys.Value) || !finite(dia.Value) {
		return DataPoint{}, false
	}
	d := newDataPoint(user, read.ObservationRecord{Kind: read.KindQuantity, Sample: &sys, Partner: &dia},
		BloodPressure, sys.Provenance, sys.ExternalID)
	if dia.IngestedAt.After(sys.IngestedAt) {
		d.Header.CreationDateTime = instant(dia.IngestedAt)
	}
	d.Header.AcquisitionProvenance.Modality = modality(sys.WasUserEntered)
	d.Body["systolic_blood_pressure"] = UnitValue{Value: sys.Value, Unit: "mmHg"}
	d.Body["diastolic_blood_pressure"] = UnitValue{Value: dia.Value, Unit: "mmHg"}
	d.Body["effective_time_frame"] = timeFrame(sys.StartDate, sys.EndDate, false)
	return d, true
}

// FromSleepSession maps one sleep session to a sleep-episode over its
// bedtime, with its total sleep time when known.
func FromSleepSession(user uuid.UUID, s read.SleepSession) DataPoint {
	d := newDataPoint(user, read.ObservationRecord{Kind: read.KindSleep, Sleep: &s}, SleepEpisode, s.Provenance, s.ExternalID)
	d.Body["effective_time_frame"] = timeFrame(s.BedtimeStart, s.BedtimeEnd, true)
	if s.TotalSleepSeconds != nil {
		d.Body["total_sleep_time"] = UnitValue{Value: float64(*s.TotalSleepSeconds), Unit: "sec"}
	}
	return d
}

// FromWorkout maps one workout to a physical-activity named by its activity
// type, with its distance and active energy when known.
func FromWorkout(user uuid.UUID, w read.Workout) DataPoint {
	d := newDataPoint(user, read.ObservationRecord{Kind: read.KindWorkout, Workout: &w}, PhysicalActivity, w.Provenance, w.ExternalID)
	name := strings.ReplaceAll(w.ActivityType, "_", " ")
	if name == "" {
		name = "workout"
	}
	d.Body["activity_name"] = name
	d.Body["effective_time_frame"] = timeFrame(w.StartDate, w.EndDate, false)
	if w.DistanceM != nil && finite(*w.DistanceM) && *w.DistanceM >= 0 {
		d.Body["distance"] = UnitValue{Value: *w.DistanceM, Unit: "m"}
	}
	if w.ActiveEnergyKcal != nil && finite(*w.ActiveEnergyKcal) {
		d.Body["kcal_burned"] = UnitValue{Value: *w.ActiveEnergyKcal, Unit: "kcal"}
	}
	return d
}

// timeFrame is a time_interval from start to end when end is after start (or
// when the schema wants an interval, then start to start without an end),
// else a date_time.
func timeFrame(start time.Time, end *time.Time, interval bool) map[string]any {
	switch {
	case end != nil && end.After(start):
		return map[string]any{"time_interval": map[string]string{
			"start_date_time": instant(start), "end_date_time": instant(*end)}}
	case interval:
		return map[string]any{"time_interval": map[string]string{
			"start_date_time": instant(start), "end_date_time": instant(start)}}
	}
	return map[string]any{"date_time": instant(start)}
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// instant formats t as RFC 3339 in UTC.
func instant(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
