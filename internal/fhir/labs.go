// SPDX-License-Identifier: AGPL-3.0-only

package fhir

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/labs"
)

// Lab results (docs/labs.md): each result is an Observation of category
// laboratory, each report a DiagnosticReport that references its results.
const (
	// LabIDPrefix starts the id of the Observation of a lab result; the rest
	// is the stored row id.
	LabIDPrefix = "lab-"
	// LabReportIDPrefix starts the id of a DiagnosticReport; the rest is
	// the first 40 hex digits of the SHA-256 of the source's report id, so
	// the id holds no source id and stays within the 64 characters of a
	// FHIR id.
	LabReportIDPrefix = "labreport-"
	// DiagnosticServiceSystem is the HL7 v2 table 0074 of diagnostic
	// service sections; LAB is the laboratory.
	DiagnosticServiceSystem = "http://terminology.hl7.org/CodeSystem/v2-0074"
	// InterpretationSystem codes the interpretation flags (H, L, N, A,
	// HH, LL, AA).
	InterpretationSystem = "http://terminology.hl7.org/CodeSystem/v3-ObservationInterpretation"
	// LabReportIDSystem is the system of a DiagnosticReport's identifier:
	// the source's report id.
	LabReportIDSystem = Namespace + "/provider/" + labs.Slug + "/report-id"
)

var interpretationDisplay = map[string]string{
	"H": "High", "L": "Low", "N": "Normal", "A": "Abnormal",
	"HH": "Critical high", "LL": "Critical low", "AA": "Critical abnormal",
}

// absentDisplay names the data absent reasons a lab result may carry.
var absentDisplay = map[string]string{
	"not-performed": "Not Performed", "unknown": "Unknown", "asked-unknown": "Asked But Unknown",
	"temp-unknown": "Temporarily Unknown", "not-asked": "Not Asked", "masked": "Masked",
	"not-applicable": "Not Applicable", "unsupported": "Unsupported", "as-text": "As Text",
	"error": "Error", "not-a-number": "Not a Number", "negative-infinity": "Negative Infinity",
	"positive-infinity": "Positive Infinity",
}

// ReferenceRange is a FHIR Observation.referenceRange.
type ReferenceRange struct {
	Low  *Quantity `json:"low,omitempty"`
	High *Quantity `json:"high,omitempty"`
	Text string    `json:"text,omitempty"`
}

// LabReportID is the id of the DiagnosticReport of the source's report id.
func LabReportID(reportID string) string {
	sum := sha256.Sum256([]byte(reportID))
	return LabReportIDPrefix + hex.EncodeToString(sum[:])[:40]
}

// ParseLabReportID returns the hash part of a DiagnosticReport id.
func ParseLabReportID(s string) (string, bool) {
	rest, ok := strings.CutPrefix(s, LabReportIDPrefix)
	if !ok || len(rest) != 40 {
		return "", false
	}
	if _, err := hex.DecodeString(rest); err != nil || strings.ToLower(rest) != rest {
		return "", false
	}
	return rest, true
}

func labCode(loinc, code, system, display *string) CodeableConcept {
	var c CodeableConcept
	if l := deref(loinc); l != "" {
		c.Coding = append(c.Coding, Coding{System: LOINC, Code: l})
	}
	if cd := deref(code); cd != "" {
		c.Coding = append(c.Coding, Coding{System: deref(system), Code: cd})
	}
	c.Text = deref(display)
	return c
}

func labQuantity(v *float64, unit *string) *Quantity {
	if v == nil {
		return nil
	}
	x := *v
	q := &Quantity{Value: &x}
	if u := deref(unit); u != "" {
		q.Unit, q.System, q.Code = u, UCUM, u
	}
	return q
}

// FromLabResult maps one lab result to an Observation of category
// laboratory: its LOINC code and the source's own code, the value as sent
// (a quantity, a string or a code), the reference range, the interpretation
// flag, the specimen, the collection time and the issued time. The
// normalized value has no element of its own and is left out.
func FromLabResult(user uuid.UUID, x read.LabResult) Observation {
	ext := x.ExternalID
	o := newObservation(user, LabIDPrefix+strconv.FormatInt(x.ID, 10), x.Provenance, &ext)
	o.Status = x.Status
	if o.Status == "" {
		o.Status = "final"
	}
	o.Category = categories(CategoryLaboratory)
	o.Code = labCode(x.LOINC, x.SourceCode, x.SourceSystem, x.Display)
	if len(o.Code.Coding) == 0 && o.Code.Text == "" {
		o.Code.Text = x.Code
	}
	o.effective(x.CollectedAt, x.CollectedEnd)
	if x.IssuedAt != nil {
		o.Issued = instant(*x.IssuedAt)
	}
	if p := deref(x.Performer); p != "" {
		o.Performer = []Reference{{Display: p}}
	}
	switch {
	case x.Value != nil:
		o.ValueQuantity = labQuantity(x.Value, x.Unit)
		o.ValueQuantity.Comparator = deref(x.Comparator)
	case deref(x.ValueCode) != "":
		o.ValueCodeableConcept = &CodeableConcept{Coding: []Coding{{System: deref(x.ValueSystem), Code: *x.ValueCode}}}
	case deref(x.ValueString) != "":
		o.ValueString = *x.ValueString
	default:
		code := deref(x.Absent)
		switch {
		case code == "" && x.Status == labs.StatusCancelled:
			code = "not-performed"
		case code == "":
			code = "unknown"
		}
		o.DataAbsentReason = absent(code, absentDisplay[code])
	}
	if f := deref(x.Interpretation); f != "" {
		o.Interpretation = []CodeableConcept{{Coding: []Coding{{System: InterpretationSystem, Code: f, Display: interpretationDisplay[f]}}}}
	} else if t := deref(x.InterpretationText); t != "" {
		o.Interpretation = []CodeableConcept{{Text: t}}
	}
	if s := deref(x.Specimen); s != "" {
		o.Specimen = &Reference{Display: s}
	}
	if x.ReferenceLow != nil || x.ReferenceHigh != nil || deref(x.ReferenceText) != "" {
		o.ReferenceRange = []ReferenceRange{{
			Low:  labQuantity(x.ReferenceLow, x.Unit),
			High: labQuantity(x.ReferenceHigh, x.Unit),
			Text: deref(x.ReferenceText),
		}}
	}
	return o
}

// DiagnosticReport is a FHIR R4 DiagnosticReport, limited to the elements
// the mapping fills. Members are in the order of the specification.
type DiagnosticReport struct {
	ResourceType      string            `json:"resourceType"`
	ID                string            `json:"id"`
	Meta              *Meta             `json:"meta,omitempty"`
	Identifier        []Identifier      `json:"identifier,omitempty"`
	Status            string            `json:"status"`
	Category          []CodeableConcept `json:"category"`
	Code              CodeableConcept   `json:"code"`
	Subject           Reference         `json:"subject"`
	EffectiveDateTime string            `json:"effectiveDateTime,omitempty"`
	Issued            string            `json:"issued,omitempty"`
	Performer         []Reference       `json:"performer,omitempty"`
	Result            []Reference       `json:"result,omitempty"`
}

// FromLabReport maps one lab report to a DiagnosticReport that references
// the Observation of each of its results.
func FromLabReport(user uuid.UUID, rep read.LabReport) DiagnosticReport {
	d := DiagnosticReport{
		ResourceType: "DiagnosticReport",
		ID:           LabReportID(rep.ID),
		Meta:         &Meta{Source: Namespace + "/provider/" + labs.Slug},
		Identifier:   []Identifier{{System: LabReportIDSystem, Value: rep.ID}},
		Status:       deref(rep.Status),
		Category: []CodeableConcept{{
			Coding: []Coding{{System: DiagnosticServiceSystem, Code: "LAB", Display: "Laboratory"}},
			Text:   "Laboratory",
		}},
		Code:              labCode(rep.LOINC, rep.Code, rep.System, rep.Display),
		Subject:           Reference{Reference: "Patient/" + user.String()},
		EffectiveDateTime: instant(rep.CollectedAt),
	}
	if d.Status == "" {
		d.Status = "unknown"
	}
	if len(d.Code.Coding) == 0 && d.Code.Text == "" {
		d.Code.Text = "Laboratory report"
	}
	if rep.IssuedAt != nil {
		d.Issued = instant(*rep.IssuedAt)
	}
	if p := deref(rep.Performer); p != "" {
		d.Performer = []Reference{{Display: p}}
	}
	for _, r := range rep.Results {
		d.Result = append(d.Result, Reference{Reference: "Observation/" + LabIDPrefix + strconv.FormatInt(r.ID, 10)})
	}
	return d
}

// DiagnosticReportSet is the searchset Bundle of one page of reports.
func DiagnosticReportSet(user uuid.UUID, base, self, next string, reps []read.LabReport) SearchBundle[DiagnosticReport] {
	b := newSearchSet[DiagnosticReport](self, next)
	for _, rep := range reps {
		d := FromLabReport(user, rep)
		b.add(base+"/DiagnosticReport/"+d.ID, d, ModeMatch)
	}
	return b
}
