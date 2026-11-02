// SPDX-License-Identifier: AGPL-3.0-only

package smartfhir

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sourceName is the source_name of every record.
const sourceName = "EHR (SMART on FHIR)"

// typeConceptEHR is the provenance of the generic observations it writes.
const typeConceptEHR = "ehr"

// loincSystem is the code system URI of LOINC.
const loincSystem = "http://loinc.org"

// loincQuantities maps the LOINC codes of the US Core vital signs to the
// canonical quantity they measure (the codes of ohs_data.quantity_codes, and
// the alternates US Core accepts for the same measure).
var loincQuantities = map[string]string{
	"8867-4":  "heart_rate",
	"40443-4": "resting_heart_rate",
	"59408-5": "oxygen_saturation", // by pulse oximetry
	"2708-6":  "oxygen_saturation", // arterial, the US Core pulse oximetry profile's other code
	"9279-1":  "respiratory_rate",
	"8310-5":  "body_temperature",
	"8480-6":  "blood_pressure_systolic",
	"8462-4":  "blood_pressure_diastolic",
	"2339-0":  "blood_glucose",
	"29463-7": "body_mass",
	"3141-9":  "body_mass", // measured body weight
	"39156-5": "body_mass_index",
	"8302-2":  "height",
	"41982-0": "body_fat_percentage",
}

// ucumSpellings renames the UCUM codes FHIR servers send to the spellings the
// unit table converts from.
var ucumSpellings = map[string]string{
	"[lb_av]":       "lb",
	"[in_i]":        "in",
	"[ft_i]":        "ft",
	"{beats}/min":   "/min",
	"{breaths}/min": "/min",
	"beats/minute":  "/min",
}

// observation is the part of a FHIR R4 Observation the mapper reads.
type observation struct {
	ResourceType      string            `json:"resourceType"`
	ID                string            `json:"id"`
	Status            string            `json:"status"`
	Category          []codeableConcept `json:"category"`
	Code              codeableConcept   `json:"code"`
	EffectiveDateTime string            `json:"effectiveDateTime"`
	EffectiveInstant  string            `json:"effectiveInstant"`
	EffectivePeriod   *struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"effectivePeriod"`
	Issued string `json:"issued"`
	value
	Component      []component       `json:"component"`
	Interpretation []codeableConcept `json:"interpretation"`
	ReferenceRange []struct {
		Low  *quantity `json:"low"`
		High *quantity `json:"high"`
		Text string    `json:"text"`
	} `json:"referenceRange"`
}

// value is the value[x] slots the mapper reads.
type value struct {
	ValueQuantity        *quantity        `json:"valueQuantity"`
	ValueCodeableConcept *codeableConcept `json:"valueCodeableConcept"`
	ValueString          *string          `json:"valueString"`
	ValueBoolean         *bool            `json:"valueBoolean"`
	ValueInteger         *int64           `json:"valueInteger"`
}

type component struct {
	Code codeableConcept `json:"code"`
	value
}

type quantity struct {
	Value      *float64 `json:"value"`
	Unit       string   `json:"unit"`
	System     string   `json:"system"`
	Code       string   `json:"code"`
	Comparator string   `json:"comparator"`
}

type coding struct {
	System  string `json:"system"`
	Code    string `json:"code"`
	Display string `json:"display"`
}

type codeableConcept struct {
	Coding []coding `json:"coding"`
	Text   string   `json:"text"`
}

// loinc returns the LOINC code of a concept, empty when it has none.
func (cc codeableConcept) loinc() string {
	for _, c := range cc.Coding {
		if isLOINC(c.System) && c.Code != "" {
			return c.Code
		}
	}
	return ""
}

// isLOINC reports whether a coding system is LOINC. Some servers write the
// https form or a trailing slash; the canonical URI is http://loinc.org.
func isLOINC(system string) bool {
	s := strings.TrimSuffix(system, "/")
	return s == loincSystem || s == "https://loinc.org"
}

// label is the text of a concept, else the display of its first coding.
func (cc codeableConcept) label() string {
	if cc.Text != "" {
		return cc.Text
	}
	for _, c := range cc.Coding {
		if c.Display != "" {
			return c.Display
		}
	}
	return ""
}

// canonicalCode is the code an observation is stored under: "loinc:<code>"
// when LOINC coded, else "<system>|<code>" of the first coding, else empty.
func (cc codeableConcept) canonicalCode() string {
	if l := cc.loinc(); l != "" {
		return "loinc:" + l
	}
	for _, c := range cc.Coding {
		if c.Code != "" {
			return c.System + "|" + c.Code
		}
	}
	return ""
}

// when is the instant an Observation describes: its effective time (the
// start of a period), else the time it was issued.
func (o observation) when() (time.Time, bool) {
	for _, s := range []string{o.EffectiveDateTime, o.EffectiveInstant, periodStart(o), o.Issued} {
		if s == "" {
			continue
		}
		if t, ok := parseFHIRTime(s); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

func periodStart(o observation) string {
	if o.EffectivePeriod == nil {
		return ""
	}
	return o.EffectivePeriod.Start
}

// parseFHIRTime reads a FHIR dateTime or instant. A partial date (year,
// year-month, date) is read as its first instant in UTC.
func parseFHIRTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// mapObservation turns one Observation line into records. A vital sign
// with a known LOINC code and a convertible unit gives a quantity sample (a
// blood pressure gives one per component); anything else with a value gives
// a generic observation. An Observation entered in error gives a deletion of
// what it may have stored before; a cancelled one, or one without a time or
// a value, gives nothing.
func mapObservation(fields map[string]any, userID string) (model.MapResult, error) {
	line, err := json.Marshal(fields)
	if err != nil {
		return model.MapResult{}, fmt.Errorf("smart_fhir: Observation: re-encode line: %w", err)
	}
	var o observation
	if err := json.Unmarshal(line, &o); err != nil {
		return model.MapResult{}, fmt.Errorf("smart_fhir: Observation: %w", err)
	}
	if o.ResourceType != "Observation" || o.ID == "" {
		return model.MapResult{}, fmt.Errorf("smart_fhir: Observation: line is not an Observation with an id")
	}
	switch o.Status {
	case "entered-in-error":
		return model.MapResult{Deletions: []model.Deletion{{ExternalID: o.ID}}}, nil
	case "cancelled":
		return model.MapResult{}, nil
	}
	at, ok := o.when()
	if !ok {
		return model.MapResult{}, nil
	}
	at = at.UTC()
	md := baseMetadata(o)

	var res model.MapResult
	if len(o.Component) > 0 && o.ValueQuantity == nil {
		for _, c := range o.Component {
			addValue(&res, userID, o.ID, at, c.Code, c.value, md)
		}
		return res, nil
	}
	addValue(&res, userID, o.ID, at, o.Code, o.value, md)
	return res, nil
}

// addValue appends the record of one coded value to res.
func addValue(res *model.MapResult, userID, id string, at time.Time, code codeableConcept, v value, md map[string]any) {
	if v.ValueQuantity != nil && v.ValueQuantity.Value != nil && v.ValueQuantity.Comparator == "" {
		if s, ok := quantitySample(userID, id, at, code, *v.ValueQuantity, md); ok {
			res.QuantitySamples = append(res.QuantitySamples, s)
			return
		}
	}
	if o, ok := genericObservation(userID, id, at, code, v, md); ok {
		res.Observations = append(res.Observations, o)
	}
}

// quantitySample builds the sample of a known vital sign, false when the
// code is not one or its unit does not convert.
func quantitySample(userID, id string, at time.Time, code codeableConcept, q quantity, md map[string]any) (model.QuantitySample, bool) {
	qt, ok := loincQuantities[code.loinc()]
	if !ok {
		return model.QuantitySample{}, false
	}
	unit := ucumUnit(q)
	val, storage, err := model.ToStorageUnit(qt, *q.Value, unit)
	if err != nil {
		return model.QuantitySample{}, false
	}
	m := copyMetadata(md)
	m["loinc"] = code.loinc()
	m["source_unit"] = unit
	s := model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      sourceName,
		ExternalID:      id,
		QuantityType:    qt,
		Value:           val,
		Unit:            storage,
		IsPrimarySource: true,
		StartDate:       at,
		EndDate:         at,
		Metadata:        m,
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      Slug,
		"external_id":   id,
		"quantity_type": qt,
		"start_date":    at.Format(time.RFC3339),
		"value":         model.CanonicalFloat(val),
		"unit":          storage,
	})
	return s, true
}

// genericObservation builds the observation of any coded value, false when
// the code or the value is missing.
func genericObservation(userID, id string, at time.Time, code codeableConcept, v value, md map[string]any) (model.Observation, bool) {
	cc := code.canonicalCode()
	if cc == "" {
		return model.Observation{}, false
	}
	o := model.Observation{
		UserID:          userID,
		TS:              at,
		CanonicalCode:   cc,
		Provider:        Slug,
		SourceName:      sourceName,
		IsPrimarySource: true,
		TypeConcept:     typeConceptEHR,
		SourceValue:     code.label(),
		Metadata:        copyMetadata(md),
	}
	o.Metadata["fhir_id"] = id
	switch {
	case v.ValueQuantity != nil && v.ValueQuantity.Value != nil:
		n := *v.ValueQuantity.Value
		o.ValueNum = &n
		o.UnitUCUM = ucumUnit(*v.ValueQuantity)
		if c := v.ValueQuantity.Comparator; c != "" {
			o.Metadata["comparator"] = c
		}
	case v.ValueCodeableConcept != nil:
		for _, c := range v.ValueCodeableConcept.Coding {
			if c.Code != "" {
				o.ValueCode = c.Code
				break
			}
		}
		o.ValueText = v.ValueCodeableConcept.label()
	case v.ValueString != nil:
		o.ValueText = *v.ValueString
	case v.ValueBoolean != nil:
		o.ValueCode = fmt.Sprint(*v.ValueBoolean)
	case v.ValueInteger != nil:
		n := float64(*v.ValueInteger)
		o.ValueNum = &n
	default:
		return model.Observation{}, false
	}
	o.ContentHash = model.ObservationContentHash(o)
	return o, true
}

// ucumUnit is the unit of a quantity: its UCUM code when coded in UCUM,
// else its human unit, renamed to the spelling the unit table reads.
func ucumUnit(q quantity) string {
	u := q.Unit
	if q.Code != "" && (q.System == "" || q.System == "http://unitsofmeasure.org") {
		u = q.Code
	}
	if r, ok := ucumSpellings[strings.ToLower(u)]; ok {
		return r
	}
	return u
}

// baseMetadata is the metadata every record of o carries: the categories,
// the interpretation (H, L, N...), the reference range and the status.
func baseMetadata(o observation) map[string]any {
	md := map[string]any{"fhir_status": o.Status}
	var cats []string
	for _, c := range o.Category {
		for _, cd := range c.Coding {
			if cd.Code != "" {
				cats = append(cats, cd.Code)
			}
		}
	}
	if len(cats) > 0 {
		md["category"] = strings.Join(cats, ",")
	}
	for _, in := range o.Interpretation {
		for _, cd := range in.Coding {
			if cd.Code != "" {
				md["interpretation"] = cd.Code
				break
			}
		}
	}
	if len(o.ReferenceRange) > 0 {
		r := o.ReferenceRange[0]
		if r.Low != nil && r.Low.Value != nil {
			md["reference_low"] = *r.Low.Value
		}
		if r.High != nil && r.High.Value != nil {
			md["reference_high"] = *r.High.Value
		}
		if r.Text != "" {
			md["reference_text"] = r.Text
		}
	}
	return md
}

func copyMetadata(md map[string]any) map[string]any {
	out := make(map[string]any, len(md)+3)
	for k, v := range md {
		out[k] = v
	}
	return out
}
