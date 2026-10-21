// SPDX-License-Identifier: AGPL-3.0-only

// Package fhir maps normalized records to FHIR R4 (4.0.1) JSON resources. It
// holds only the few resource shapes the API serves, written with the
// standard library: a quantity sample, a blood pressure pair, a sleep
// session, a workout, a daily summary or an ECG recording becomes an
// Observation, and a page of them a searchset Bundle; the user is a Patient, the devices the records
// came from Devices, and who produced each Observation its Provenance. The
// mapping is described in docs/fhir.md; the
// golden files under testdata are what the HL7 validator checks
// (scripts/fhir-validate.sh).
package fhir

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
)

// Code systems, profiles and the identifiers Open Health Sync defines.
const (
	LOINC = "http://loinc.org"
	UCUM  = "http://unitsofmeasure.org"
	// ObservationCategory is the HL7 observation category code system.
	ObservationCategory = "http://terminology.hl7.org/CodeSystem/observation-category"
	dataAbsentReason    = "http://terminology.hl7.org/CodeSystem/data-absent-reason"
	profileBase         = "http://hl7.org/fhir/StructureDefinition/"

	// Namespace prefixes every URI Open Health Sync defines (docs/fhir.md).
	Namespace = "https://github.com/soma-intelligence/open-health-sync/fhir"
	// QuantityTypeSystem is the code system of the canonical quantity names
	// (heart_rate, step_count...).
	QuantityTypeSystem = Namespace + "/CodeSystem/quantity-type"
	// SourceSystem is the code system of the meta.tag that carries the
	// vendor that produced a record (the API's provenance source).
	SourceSystem = Namespace + "/CodeSystem/source"

	// ObservationIDPrefix starts the id of an Observation made from a
	// quantity sample; the rest is the stored row id.
	ObservationIDPrefix = "qs-"
)

// Observation category codes.
const (
	CategoryVitalSigns = "vital-signs"
	CategoryActivity   = "activity"
	CategoryLaboratory = "laboratory"
	CategorySurvey     = "survey"
)

// PhysicalActivitySystem is the code system of the HL7 Physical Activity
// IG's own codes (hl7.fhir.us.physical-activity 1.0.0); PhysicalActivity is
// the category its measures carry next to the HL7 activity category.
const (
	PhysicalActivitySystem = "http://hl7.org/fhir/us/physical-activity/CodeSystem/pa-temporary-codes"
	PhysicalActivity       = "PhysicalActivity"
)

// physicalActivityCategory is the Physical Activity IG's category, a second
// category element next to activity.
func physicalActivityCategory() CodeableConcept {
	return CodeableConcept{Coding: []Coding{{System: PhysicalActivitySystem, Code: PhysicalActivity}}}
}

var categoryDisplay = map[string]string{
	CategoryVitalSigns: "Vital Signs",
	CategoryActivity:   "Activity",
	CategoryLaboratory: "Laboratory",
	CategorySurvey:     "Survey",
}

// Coding is a FHIR Coding.
type Coding struct {
	System  string `json:"system,omitempty"`
	Code    string `json:"code,omitempty"`
	Display string `json:"display,omitempty"`
}

// CodeableConcept is a FHIR CodeableConcept.
type CodeableConcept struct {
	Coding []Coding `json:"coding,omitempty"`
	Text   string   `json:"text,omitempty"`
}

// Reference is a FHIR Reference: a literal reference (Type/id), or a logical
// one (an identifier).
type Reference struct {
	Reference  string      `json:"reference,omitempty"`
	Identifier *Identifier `json:"identifier,omitempty"`
	Display    string      `json:"display,omitempty"`
}

// Period is a FHIR Period.
type Period struct {
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

// Quantity is a FHIR Quantity.
type Quantity struct {
	Value  *float64 `json:"value,omitempty"`
	Unit   string   `json:"unit,omitempty"`
	System string   `json:"system,omitempty"`
	Code   string   `json:"code,omitempty"`
}

// Meta is a FHIR Meta.
type Meta struct {
	Source  string   `json:"source,omitempty"`
	Profile []string `json:"profile,omitempty"`
	Tag     []Coding `json:"tag,omitempty"`
}

// Identifier is a FHIR Identifier.
type Identifier struct {
	System string `json:"system,omitempty"`
	Value  string `json:"value,omitempty"`
}

// Observation is a FHIR R4 Observation, limited to the elements the mapping
// fills. Members are in the order of the specification.
type Observation struct {
	ResourceType      string            `json:"resourceType"`
	ID                string            `json:"id"`
	Meta              *Meta             `json:"meta,omitempty"`
	Identifier        []Identifier      `json:"identifier,omitempty"`
	Status            string            `json:"status"`
	Category          []CodeableConcept `json:"category,omitempty"`
	Code              CodeableConcept   `json:"code"`
	Subject           Reference         `json:"subject"`
	EffectiveDateTime string            `json:"effectiveDateTime,omitempty"`
	EffectivePeriod   *Period           `json:"effectivePeriod,omitempty"`
	Issued            string            `json:"issued,omitempty"`
	// Performer is the person, on an answer they gave about themselves.
	Performer     []Reference `json:"performer,omitempty"`
	ValueQuantity *Quantity   `json:"valueQuantity,omitempty"`
	// ValueCodeableConcept is the value of a classification.
	ValueCodeableConcept *CodeableConcept `json:"valueCodeableConcept,omitempty"`
	DataAbsentReason     *CodeableConcept `json:"dataAbsentReason,omitempty"`
	Device               *Reference       `json:"device,omitempty"`
	Component            []Component      `json:"component,omitempty"`
}

// ObservationID is the stable id of the Observation made from the quantity
// sample stored with row id id.
func ObservationID(id int64) string {
	return ObservationIDPrefix + strconv.FormatInt(id, 10)
}

// ParseObservationID reverses ObservationID.
func ParseObservationID(s string) (int64, bool) {
	rest, ok := strings.CutPrefix(s, ObservationIDPrefix)
	if !ok {
		return 0, false
	}
	return parseRowID(rest)
}

// FromQuantitySample maps one quantity sample of user to an Observation.
func FromQuantitySample(user uuid.UUID, s read.CodedSample) Observation {
	o := Observation{
		ResourceType: "Observation",
		ID:           ObservationID(s.ID),
		Status:       "final",
		Subject:      Reference{Reference: "Patient/" + user.String()},
		Issued:       instant(s.IngestedAt),
		Meta:         &Meta{Source: Namespace + "/provider/" + s.Provider},
	}
	if src := deref(s.Source); src != "" {
		o.Meta.Tag = []Coding{{System: SourceSystem, Code: src}}
	}
	if ext := deref(s.ExternalID); ext != "" {
		o.Identifier = []Identifier{{System: Namespace + "/provider/" + s.Provider + "/external-id", Value: ext}}
	}

	loinc := sampleLOINC(s)
	vs, isVital := vitalSigns[s.Type]
	if isVital && vs.loinc != loinc {
		// The catalog codes this type differently from what the profile
		// expects: keep the category, claim no profile.
		vs = vitalSign{}
	}
	var codings []Coding
	if vs.extra != "" {
		codings = append(codings, Coding{System: LOINC, Code: vs.extra})
	}
	if loinc != "" && !vs.componentOnly {
		codings = append(codings, Coding{System: LOINC, Code: loinc})
	}
	codings = append(codings, Coding{System: QuantityTypeSystem, Code: s.Type})
	o.Code = CodeableConcept{Coding: codings, Text: strings.ReplaceAll(s.Type, "_", " ")}

	if cat := category(s.Type); cat != "" {
		o.Category = []CodeableConcept{{
			Coding: []Coding{{System: ObservationCategory, Code: cat, Display: categoryDisplay[cat]}},
			Text:   categoryDisplay[cat],
		}}
		if cat == CategorySurvey {
			// A self report: the person gave the answer.
			o.Performer = []Reference{o.Subject}
		}
		if physicalActivityMeasures[s.Type] {
			o.Category = append(o.Category, physicalActivityCategory())
		}
	}

	start := instant(s.StartDate)
	if s.EndDate != nil && s.EndDate.After(s.StartDate) {
		o.EffectivePeriod = &Period{Start: start, End: instant(*s.EndDate)}
	} else {
		o.EffectiveDateTime = start
	}

	if c, ok := afibClasses[s.Value]; ok && s.Type == afibClassification {
		// A classification is a code, not an amount.
		o.ValueCodeableConcept = &CodeableConcept{Coding: []Coding{{System: AFibClassificationSystem, Code: c}}, Text: c}
		o.Device = deviceRef(user, read.SampleDevice(s.Sample))
		return o
	}
	switch v := s.Value; {
	case math.IsNaN(v):
		o.DataAbsentReason = absent("not-a-number", "Not a Number (NaN)")
	case math.IsInf(v, 1):
		o.DataAbsentReason = absent("positive-infinity", "Positive Infinity (PINF)")
	case math.IsInf(v, -1):
		o.DataAbsentReason = absent("negative-infinity", "Negative Infinity (NINF)")
	default:
		q := &Quantity{Value: &v, Unit: deref(s.Unit)}
		if code := ucumCode(s); code != "" {
			q.System, q.Code = UCUM, code
			// A UCUM annotation ({steps}) means 1; its words are the unit
			// a reader sees.
			if a, ok := strings.CutPrefix(code, "{"); ok && strings.HasSuffix(a, "}") {
				q.Unit = strings.TrimSuffix(a, "}")
			}
		}
		o.ValueQuantity = q
		if vs.profile != "" && vs.allows(q.Code) {
			o.Meta.Profile = []string{profileBase + "vitalsigns"}
			if vs.profile != "vitalsigns" {
				o.Meta.Profile = append(o.Meta.Profile, profileBase+vs.profile)
			}
		}
	}

	o.Device = deviceRef(user, read.SampleDevice(s.Sample))
	return o
}

// afibClassification is the quantity type of an ECG's atrial fibrillation
// classification (Withings), stored as 0 negative, 1 positive, 2
// inconclusive. Its Observation carries the class as a code.
const afibClassification = "ecg_afib_classification"

// AFibClassificationSystem is the code system of those classes.
const AFibClassificationSystem = Namespace + "/CodeSystem/ecg-afib-classification"

var afibClasses = map[float64]string{0: "negative", 1: "positive", 2: "inconclusive"}

// LOINCInterstitialGlucose is glucose measured in interstitial fluid, what a
// continuous glucose monitor reads (the code the HL7 CGM implementation guide
// uses). The catalog codes blood_glucose with 2339-0, glucose in blood.
const LOINCInterstitialGlucose = "99504-3"

// sampleLOINC is the LOINC code of s: the catalog's, except for a glucose
// value whose metadata says it was measured in interstitial fluid (a CGM
// reading from Dexcom, or from Health Connect with that specimen source).
func sampleLOINC(s read.CodedSample) string {
	loinc := deref(s.LOINCCode)
	if s.Type != "blood_glucose" || len(s.Metadata) == 0 {
		return loinc
	}
	var md struct {
		Specimen string `json:"specimen"`
	}
	if json.Unmarshal(s.Metadata, &md) == nil && md.Specimen == "interstitial_fluid" {
		return LOINCInterstitialGlucose
	}
	return loinc
}

func absent(code, display string) *CodeableConcept {
	return &CodeableConcept{Coding: []Coding{{System: dataAbsentReason, Code: code, Display: display}}}
}

// ucumCode is the UCUM code of the sample's unit: the catalog's when the
// sample is in its type's storage unit, else the spelling table's. Empty
// when the unit has no known UCUM code.
func ucumCode(s read.CodedSample) string {
	unit := deref(s.Unit)
	if unit == "" {
		return ""
	}
	if c := deref(s.UCUMUnit); c != "" && unit == deref(s.StorageUnit) {
		return c
	}
	return ucumUnits[unit]
}

// instant formats t as a FHIR instant (and dateTime): RFC 3339 in UTC.
func instant(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// BundleLink is one link of a Bundle.
type BundleLink struct {
	Relation string `json:"relation"`
	URL      string `json:"url"`
}

// BundleSearch says why an entry is in a searchset: ModeMatch for a resource
// the search asked for, ModeInclude for one it brings along.
type BundleSearch struct {
	Mode string `json:"mode"`
}

// Search modes.
const (
	ModeMatch   = "match"
	ModeInclude = "include"
)

// Entry is one entry of a searchset Bundle of resources of type T.
type Entry[T any] struct {
	FullURL  string        `json:"fullUrl"`
	Resource T             `json:"resource"`
	Search   *BundleSearch `json:"search,omitempty"`
}

// SearchBundle is a FHIR R4 searchset Bundle of resources of type T (any,
// or json.RawMessage to read one, for a Bundle of several resource types).
type SearchBundle[T any] struct {
	ResourceType string       `json:"resourceType"`
	Type         string       `json:"type"`
	Link         []BundleLink `json:"link,omitempty"`
	Entry        []Entry[T]   `json:"entry,omitempty"`
}

// BundleEntry is one entry of a searchset Bundle of Observations.
type BundleEntry = Entry[Observation]

// Bundle is a FHIR R4 searchset Bundle of Observations.
type Bundle = SearchBundle[Observation]

// newSearchSet is a searchset with its self link and, unless this is the
// last page, its next link. total is left out: the API pages with a cursor
// and never counts.
func newSearchSet[T any](self, next string) SearchBundle[T] {
	b := SearchBundle[T]{ResourceType: "Bundle", Type: "searchset", Link: []BundleLink{{Relation: "self", URL: self}}}
	if next != "" {
		b.Link = append(b.Link, BundleLink{Relation: "next", URL: next})
	}
	return b
}

func (b *SearchBundle[T]) add(fullURL string, r T, mode string) {
	b.Entry = append(b.Entry, Entry[T]{FullURL: fullURL, Resource: r, Search: &BundleSearch{Mode: mode}})
}

// SearchSet is the searchset Bundle of one page of observations. base is the
// absolute FHIR base URL the entries' fullUrl hang from (it ends before
// "/Observation"); next is empty on the last page.
func SearchSet(base, self, next string, obs []Observation) Bundle {
	b := newSearchSet[Observation](self, next)
	for _, o := range obs {
		b.add(base+"/Observation/"+o.ID, o, ModeMatch)
	}
	return b
}
