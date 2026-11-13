// SPDX-License-Identifier: AGPL-3.0-only

// Package labs defines a lab result and the report (panel) it belongs to,
// checks them, reads them from the two forms POST /v1/users/{id}/labs
// accepts (a FHIR R4 Bundle or resource of DiagnosticReport and Observation
// with category laboratory, or a simple JSON form), and maps each result to
// one model.Observation with canonical code lab:loinc:<code> or
// lab:local:<code>. The lab connector (connectors/labs) maps every stored
// upload line through Decode and Observations; docs/labs.md documents both
// forms and the unit normalization (units.go).
//
// A result carries the source's id. Sending it again (an amended or
// corrected result) replaces the stored one whole, also when its collection
// time moved.
package labs

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Slug is the provider and the source slug of every lab result.
const Slug = "labs"

// DataType is the X-OHS-Type of a lab upload part.
const DataType = "lab_results"

// Canonical code prefixes: a result is stored under the LOINC code when the
// source gives one, else under the source's own code.
const (
	CodePrefix  = "lab:"
	LOINCPrefix = "lab:loinc:"
	LocalPrefix = "lab:local:"
)

// Result statuses.
const (
	StatusPreliminary = "preliminary"
	StatusFinal       = "final"
	StatusAmended     = "amended"
	StatusCorrected   = "corrected"
	StatusCancelled   = "cancelled"
)

// Statuses lists the statuses a result or a report may have.
var Statuses = []string{StatusPreliminary, StatusFinal, StatusAmended, StatusCorrected, StatusCancelled}

// Interpretations are the flags a result may carry, the codes of the HL7 v3
// ObservationInterpretation system: high, low, normal, abnormal, and the
// critical forms of the three (critically high, critically low, critically
// abnormal).
var Interpretations = []string{"H", "L", "N", "A", "HH", "LL", "AA"}

// Critical reports whether an interpretation flag is a critical one.
func Critical(flag string) bool { return flag == "HH" || flag == "LL" || flag == "AA" }

// Comparators are the comparators a numeric value may carry ("<5").
var Comparators = []string{"<", "<=", ">=", ">"}

// Limits of one upload.
const (
	MaxReports          = 100
	MaxResultsPerReport = 500
	MaxResults          = 2000
	MaxID               = 200
	MaxText             = 500
	MaxUnit             = 64
)

// The range a result's times must fall in.
var (
	minTime = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	maxTime = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
)

var loincCode = regexp.MustCompile(`^[0-9]{1,7}-[0-9]$`)

// Report is one lab report (a panel, an order) with its results.
type Report struct {
	// ID is the source's id of the report. Empty only for results a FHIR
	// upload sent without a DiagnosticReport.
	ID      string `json:"id,omitempty"`
	LOINC   string `json:"loinc,omitempty"`
	Code    string `json:"code,omitempty"`
	System  string `json:"system,omitempty"`
	Display string `json:"display,omitempty"`
	// Status is the report's status, the default of its results' statuses.
	Status string `json:"status,omitempty"`
	// CollectedAt is the default of its results' collection times.
	CollectedAt *time.Time `json:"collected_at,omitempty"`
	IssuedAt    *time.Time `json:"issued_at,omitempty"`
	// Performer is the lab that ran the tests, Orderer who ordered them.
	Performer string   `json:"performer,omitempty"`
	Orderer   string   `json:"orderer,omitempty"`
	Results   []Result `json:"results"`
}

// Result is one test result.
type Result struct {
	// ID is the source's id of the result: sending it again replaces it.
	ID string `json:"id"`
	// LOINC is the LOINC code when the source gives one; Code and System
	// the source's own code. One of LOINC and Code is required.
	LOINC   string `json:"loinc,omitempty"`
	Code    string `json:"code,omitempty"`
	System  string `json:"system,omitempty"`
	Display string `json:"display,omitempty"`
	// The value: a number with its UCUM unit, a string, or a code (with
	// its system). A cancelled result may have none.
	Value       *float64 `json:"value,omitempty"`
	Comparator  string   `json:"comparator,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	ValueString string   `json:"value_string,omitempty"`
	ValueCode   string   `json:"value_code,omitempty"`
	ValueSystem string   `json:"value_system,omitempty"`
	// The reference range, in the result's unit.
	ReferenceLow  *float64 `json:"reference_low,omitempty"`
	ReferenceHigh *float64 `json:"reference_high,omitempty"`
	ReferenceText string   `json:"reference_text,omitempty"`
	// Interpretation is one of Interpretations.
	Interpretation string `json:"interpretation,omitempty"`
	// InterpretationText keeps an interpretation the source coded with a
	// flag outside Interpretations, as text (FHIR only).
	InterpretationText string `json:"interpretation_text,omitempty"`
	// Status defaults to the report's, then to final.
	Status   string `json:"status,omitempty"`
	Specimen string `json:"specimen,omitempty"`
	// CollectedAt is when the specimen was taken; it defaults to the
	// report's. CollectedEnd ends a collection period.
	CollectedAt  *time.Time `json:"collected_at,omitempty"`
	CollectedEnd *time.Time `json:"collected_end,omitempty"`
	IssuedAt     *time.Time `json:"issued_at,omitempty"`
	Performer    string     `json:"performer,omitempty"`
	// Absent is why a result has no value (FHIR dataAbsentReason).
	Absent string `json:"absent,omitempty"`
	// IssuedAsCollected says the source gave no collection time and the
	// issued time stands in for it (FHIR only).
	IssuedAsCollected bool `json:"issued_as_collected,omitempty"`
}

// Upload is the simple JSON form of an upload.
type Upload struct {
	Reports []Report `json:"reports"`
}

// InvalidError is an upload that cannot be stored. Msg names the place and
// the field and never quotes a value or an id.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &InvalidError{Msg: fmt.Sprintf(format, args...)}
}

// IsInvalid reports whether err is an InvalidError, and its message.
func IsInvalid(err error) (string, bool) {
	var ie *InvalidError
	if errors.As(err, &ie) {
		return ie.Msg, true
	}
	return "", false
}

// text checks one line of text of at most max bytes.
func text(at, name, s string, max int) error {
	if s == "" {
		return nil
	}
	if !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\t' }) >= 0 {
		return invalid("%s%s must be one line of text", at, name)
	}
	if len(s) > max {
		return invalid("%s%s is at most %d bytes", at, name, max)
	}
	return nil
}

func id(at, name, s string) error {
	if strings.TrimSpace(s) == "" {
		return invalid("%s%s is required", at, name)
	}
	return text(at, name, s, MaxID)
}

func checkTime(at, name string, t *time.Time) error {
	if t == nil {
		return nil
	}
	*t = t.UTC().Truncate(time.Microsecond)
	if t.Before(minTime) || !t.Before(maxTime) {
		return invalid("%s%s must be between 1900 and 2200", at, name)
	}
	return nil
}

func finite(at, name string, v *float64) error {
	if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0)) {
		return invalid("%s%s must be a finite number", at, name)
	}
	return nil
}

// Check validates the reports of one upload and fills the defaults each
// result takes from its report: status, collection and issued time,
// performer. Every report but a FHIR one without DiagnosticReport needs an
// id; result ids are unique across the upload.
func Check(reports []Report) error {
	switch {
	case len(reports) == 0:
		return invalid("reports must hold at least one report")
	case len(reports) > MaxReports:
		return invalid("reports holds at most %d reports", MaxReports)
	}
	seenResult := map[string]bool{}
	seenReport := map[string]bool{}
	total := 0
	for i := range reports {
		r := &reports[i]
		at := fmt.Sprintf("reports[%d].", i)
		if r.ID != "" {
			if err := id(at, "id", r.ID); err != nil {
				return err
			}
			if seenReport[r.ID] {
				return invalid("%sid is used by another report of this upload", at)
			}
			seenReport[r.ID] = true
		}
		if err := checkCode(at, r.LOINC, r.Code, r.System, r.Display, false); err != nil {
			return err
		}
		if r.Status == "" {
			r.Status = StatusFinal
		}
		if !slices.Contains(Statuses, r.Status) {
			return invalid("%sstatus must be one of %s", at, strings.Join(Statuses, ", "))
		}
		for _, f := range []struct {
			name string
			t    *time.Time
		}{{"collected_at", r.CollectedAt}, {"issued_at", r.IssuedAt}} {
			if err := checkTime(at, f.name, f.t); err != nil {
				return err
			}
		}
		if err := text(at, "performer", r.Performer, MaxText); err != nil {
			return err
		}
		if err := text(at, "orderer", r.Orderer, MaxText); err != nil {
			return err
		}
		switch {
		case len(r.Results) == 0:
			return invalid("%sresults must hold at least one result", at)
		case len(r.Results) > MaxResultsPerReport:
			return invalid("%sresults holds at most %d results", at, MaxResultsPerReport)
		}
		total += len(r.Results)
		if total > MaxResults {
			return invalid("an upload holds at most %d results", MaxResults)
		}
		for j := range r.Results {
			res := &r.Results[j]
			rat := fmt.Sprintf("%sresults[%d].", at, j)
			if res.Status == "" {
				res.Status = r.Status
			}
			if res.CollectedAt == nil && r.CollectedAt != nil {
				t := *r.CollectedAt
				res.CollectedAt = &t
			}
			if res.IssuedAt == nil && r.IssuedAt != nil {
				t := *r.IssuedAt
				res.IssuedAt = &t
			}
			if res.Performer == "" {
				res.Performer = r.Performer
			}
			if err := checkResult(rat, res); err != nil {
				return err
			}
			if seenResult[res.ID] {
				return invalid("%sid is used by another result of this upload", rat)
			}
			seenResult[res.ID] = true
		}
	}
	return nil
}

func checkCode(at, loinc, code, system, display string, required bool) error {
	if loinc != "" && !loincCode.MatchString(loinc) {
		return invalid("%sloinc must be a LOINC code such as 2345-7", at)
	}
	if required && loinc == "" && strings.TrimSpace(code) == "" {
		return invalid("%sloinc or code is required", at)
	}
	if err := text(at, "code", code, MaxID); err != nil {
		return err
	}
	if err := text(at, "system", system, MaxText); err != nil {
		return err
	}
	return text(at, "display", display, MaxText)
}

func checkResult(at string, r *Result) error {
	if err := id(at, "id", r.ID); err != nil {
		return err
	}
	if err := checkCode(at, r.LOINC, r.Code, r.System, r.Display, true); err != nil {
		return err
	}
	if !slices.Contains(Statuses, r.Status) {
		return invalid("%sstatus must be one of %s", at, strings.Join(Statuses, ", "))
	}
	values := 0
	if r.Value != nil {
		values++
	}
	if r.ValueString != "" {
		values++
	}
	if r.ValueCode != "" {
		values++
	}
	switch {
	case values > 1:
		return invalid("%sholds more than one of value, value_string and value_code", at)
	case values == 0 && r.Status != StatusCancelled && r.Absent == "":
		return invalid("%svalue, value_string or value_code is required unless the status is cancelled", at)
	}
	for _, f := range []struct {
		name string
		v    *float64
	}{{"value", r.Value}, {"reference_low", r.ReferenceLow}, {"reference_high", r.ReferenceHigh}} {
		if err := finite(at, f.name, f.v); err != nil {
			return err
		}
	}
	if r.Comparator != "" && (r.Value == nil || !slices.Contains(Comparators, r.Comparator)) {
		return invalid("%scomparator must be one of %s, with a numeric value", at, strings.Join(Comparators, ", "))
	}
	if r.Unit != "" && r.Value == nil {
		return invalid("%sunit goes with a numeric value", at)
	}
	if err := text(at, "unit", r.Unit, MaxUnit); err != nil {
		return err
	}
	if r.ReferenceLow != nil && r.ReferenceHigh != nil && *r.ReferenceLow > *r.ReferenceHigh {
		return invalid("%sreference_low must not be above reference_high", at)
	}
	if r.Interpretation == "critical" {
		r.Interpretation = "AA"
	}
	if r.Interpretation != "" && !slices.Contains(Interpretations, r.Interpretation) {
		return invalid("%sinterpretation must be one of %s or critical", at, strings.Join(Interpretations, ", "))
	}
	for _, f := range []struct{ name, v string }{
		{"value_string", r.ValueString}, {"value_code", r.ValueCode}, {"value_system", r.ValueSystem},
		{"reference_text", r.ReferenceText}, {"interpretation_text", r.InterpretationText},
		{"specimen", r.Specimen}, {"performer", r.Performer}, {"absent", r.Absent},
	} {
		if err := text(at, f.name, f.v, MaxText); err != nil {
			return err
		}
	}
	if r.ValueSystem != "" && r.ValueCode == "" {
		return invalid("%svalue_system goes with value_code", at)
	}
	if r.CollectedAt == nil {
		return invalid("%scollected_at is required, on the result or on its report", at)
	}
	for _, f := range []struct {
		name string
		t    *time.Time
	}{{"collected_at", r.CollectedAt}, {"collected_end", r.CollectedEnd}, {"issued_at", r.IssuedAt}} {
		if err := checkTime(at, f.name, f.t); err != nil {
			return err
		}
	}
	if r.CollectedEnd != nil && !r.CollectedEnd.After(*r.CollectedAt) {
		r.CollectedEnd = nil
	}
	return nil
}

// CanonicalCode is the stored code of a result: lab:loinc:<code> when it has
// a LOINC code, else lab:local:<code>.
func CanonicalCode(r Result) string {
	if r.LOINC != "" {
		return LOINCPrefix + r.LOINC
	}
	return LocalPrefix + r.Code
}

// IsLab reports whether a canonical code is a lab result's.
func IsLab(canonical string) bool { return strings.HasPrefix(canonical, CodePrefix) }

// CodesFor are the canonical codes a code filter matches: the LOINC code and
// the source's own code of that spelling.
func CodesFor(code string) []string { return []string{LOINCPrefix + code, LocalPrefix + code} }
