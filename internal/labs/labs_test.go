// SPDX-License-Identifier: AGPL-3.0-only

package labs

import (
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestDecodeLipidPanel reads the HL7 R4 example "diagnosticreport-example-lipids"
// (Bundle "lipids"): one report, four results that take the report's
// collection time and performer, and the cholesterols normalized to mg/dL.
func TestDecodeLipidPanel(t *testing.T) {
	reports, err := Decode(load(t, "hl7-r4-diagnosticreport-example-lipids.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	rep := reports[0]
	if rep.ID != "lipids" || rep.LOINC != "24331-1" || rep.Display != "Lipid Panel" || rep.Status != StatusFinal {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Performer != "Acme Laboratory, Inc" {
		t.Fatalf("performer = %q", rep.Performer)
	}
	if len(rep.Results) != 4 {
		t.Fatalf("results = %d, want 4", len(rep.Results))
	}
	collected := time.Date(2011, 3, 3, 21, 30, 0, 0, time.UTC)
	for _, r := range rep.Results {
		if r.CollectedAt == nil || !r.CollectedAt.Equal(collected) {
			t.Fatalf("%s collected = %v, want %v", r.ID, r.CollectedAt, collected)
		}
		if r.Unit != "mmol/L" || r.Status != StatusFinal || r.Performer != "Acme Laboratory, Inc" {
			t.Fatalf("result %+v", r)
		}
	}
	hdl := rep.Results[2]
	if hdl.ID != "hdlcholesterol" || hdl.LOINC != "2085-9" || hdl.Display != "Cholesterol in HDL" ||
		hdl.ReferenceLow == nil || *hdl.ReferenceLow != 1.5 || hdl.ReferenceHigh != nil {
		t.Fatalf("hdl = %+v", hdl)
	}

	obs := Observations("u1", reports)
	if len(obs) != 4 {
		t.Fatalf("observations = %d", len(obs))
	}
	byCode := map[string]int{}
	for i, o := range obs {
		byCode[o.CanonicalCode] = i
		if o.Provider != Slug || o.TypeConcept != "ehr" || o.ReportID != "lipids" || !o.Replaceable {
			t.Fatalf("observation %+v", o)
		}
		if o.ContentHash != ContentHash("u1", o.ExternalID) {
			t.Fatal("content hash is not the identity of the result")
		}
	}
	chol := obs[byCode["lab:loinc:35200-5"]]
	if chol.ValueCanonical == nil || !near(*chol.ValueCanonical, 6.3*38.665) || chol.Metadata[MetaNormalizedUnit] != "mg/dL" {
		t.Fatalf("cholesterol normalized = %v %v", chol.ValueCanonical, chol.Metadata[MetaNormalizedUnit])
	}
	// Triglycerides have no exact conversion: no normalized value.
	tg := obs[byCode["lab:loinc:35217-9"]]
	if tg.ValueCanonical != nil {
		t.Fatalf("triglyceride normalized = %v, want none", *tg.ValueCanonical)
	}
	if tg.Metadata[MetaReportDisplay] != "Lipid Panel" || tg.Metadata[MetaReferenceHigh] != 2.0 {
		t.Fatalf("triglyceride metadata = %v", tg.Metadata)
	}
}

// TestDecodeBloodExam reads the HL7 R4 example
// "diagnosticreport-example-f001-bloodexam" with the results of
// "observation-example-f001-glucose" and "observation-example-f005-hemoglobin":
// the report has no collection time, each result has its own period, the
// flags H and L, and the orderer comes from the ServiceRequest.
func TestDecodeBloodExam(t *testing.T) {
	reports, err := Decode(load(t, "hl7-r4-diagnosticreport-example-f001-bloodexam.json"))
	if err != nil {
		t.Fatal(err)
	}
	rep := reports[0]
	if rep.ID != "f001" || rep.LOINC != "58410-2" || rep.Orderer != "Dr. E. van den Broek" ||
		rep.Performer != "Burgers University Medical Centre" || rep.CollectedAt != nil {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Results) != 2 {
		t.Fatalf("results = %d", len(rep.Results))
	}
	glu, hb := rep.Results[0], rep.Results[1]
	if glu.ID != "f001" || glu.LOINC != "15074-8" || glu.Interpretation != "H" || glu.Unit != "mmol/L" ||
		*glu.Value != 6.3 || *glu.ReferenceLow != 3.1 || *glu.ReferenceHigh != 6.2 || glu.Performer != "A. Langeveld" {
		t.Fatalf("glucose = %+v", glu)
	}
	if !glu.CollectedAt.Equal(time.Date(2013, 4, 2, 8, 30, 10, 0, time.UTC)) || glu.CollectedEnd != nil {
		t.Fatalf("glucose collected = %v %v", glu.CollectedAt, glu.CollectedEnd)
	}
	if hb.Interpretation != "L" || hb.Unit != "g/dL" || hb.CollectedEnd != nil {
		t.Fatalf("hemoglobin = %+v", hb)
	}
	obs := Observations("u1", reports)
	if obs[0].ValueCanonical == nil || !near(*obs[0].ValueCanonical, 6.3*18.0156) {
		t.Fatalf("glucose normalized = %v", obs[0].ValueCanonical)
	}
	if obs[1].ValueCanonical != nil {
		t.Fatal("hemoglobin is not in the table, yet normalized")
	}
	if obs[0].Metadata[MetaOrderer] != "Dr. E. van den Broek" || obs[0].Metadata[MetaInterpretation] != "H" {
		t.Fatalf("metadata = %v", obs[0].Metadata)
	}
}

func TestDecodeSimpleForm(t *testing.T) {
	body := `{"reports":[{"id":"r1","display":"Basic panel","collected_at":"2026-01-02T08:00:00Z","performer":"Lab A",
	  "results":[
	    {"id":"a","loinc":"2345-7","value":99,"unit":"mg/dL","reference_low":70,"reference_high":99,"interpretation":"N"},
	    {"id":"b","code":"COVID","system":"urn:lab","value_string":"positive","interpretation":"critical"},
	    {"id":"c","loinc":"4548-4","status":"cancelled"}
	  ]}]}`
	reports, err := Decode([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	obs := Observations("u1", reports)
	if len(obs) != 3 {
		t.Fatalf("observations = %d", len(obs))
	}
	if obs[0].ValueCanonical == nil || *obs[0].ValueCanonical != 99 {
		t.Fatalf("identity normalization = %v", obs[0].ValueCanonical)
	}
	if obs[1].CanonicalCode != "lab:local:COVID" || obs[1].ValueText != "positive" ||
		obs[1].Metadata[MetaInterpretation] != "AA" || obs[1].Metadata[MetaCritical] != true {
		t.Fatalf("string result = %+v", obs[1])
	}
	if obs[2].ValueNum != nil || obs[2].Metadata[MetaStatus] != StatusCancelled {
		t.Fatalf("cancelled = %+v", obs[2])
	}
	if obs[0].SourceName != "Lab A" || !obs[0].TS.Equal(time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("defaults = %+v", obs[0])
	}
}

func TestDecodeRejects(t *testing.T) {
	vital := `{"resourceType":"Observation","id":"bp","status":"final",
	  "category":[{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/observation-category","code":"vital-signs"}]}],
	  "code":{"coding":[{"system":"http://loinc.org","code":"8867-4"}]},"effectiveDateTime":"2026-01-01T00:00:00Z","valueQuantity":{"value":60}}`
	imaging := `{"resourceType":"DiagnosticReport","id":"x","status":"final",
	  "category":[{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v2-0074","code":"RAD"}]}],
	  "code":{"text":"CT"},"result":[{"reference":"Observation/o"}]}`
	cases := []struct{ name, body, want string }{
		{"not json", `[`, "not a JSON object"},
		{"other resource", `{"resourceType":"Patient"}`, "resourceType must be"},
		{"vital signs", vital, "category must be laboratory"},
		{"no category", strings.Replace(vital, `"category"`, `"x_category"`, 1), "category laboratory is required"},
		{"imaging report", imaging, "laboratory section"},
		{"dangling result", strings.Replace(imaging, `"RAD"`, `"LAB"`, 1), "not in the upload"},
		{"unknown field", `{"reports":[],"x":1}`, "unknown field"},
		{"empty", `{"reports":[]}`, "at least one report"},
		{"no value", `{"reports":[{"id":"r","collected_at":"2026-01-01T00:00:00Z","results":[{"id":"a","loinc":"2345-7"}]}]}`, "value, value_string or value_code is required"},
		{"two values", `{"reports":[{"id":"r","collected_at":"2026-01-01T00:00:00Z","results":[{"id":"a","loinc":"2345-7","value":1,"value_string":"x"}]}]}`, "more than one"},
		{"bad loinc", `{"reports":[{"id":"r","collected_at":"2026-01-01T00:00:00Z","results":[{"id":"a","loinc":"glucose","value":1}]}]}`, "LOINC code"},
		{"bad status", `{"reports":[{"id":"r","status":"done","collected_at":"2026-01-01T00:00:00Z","results":[{"id":"a","loinc":"2345-7","value":1}]}]}`, "status must be"},
		{"bad flag", `{"reports":[{"id":"r","collected_at":"2026-01-01T00:00:00Z","results":[{"id":"a","loinc":"2345-7","value":1,"interpretation":"X"}]}]}`, "interpretation must be"},
		{"no time", `{"reports":[{"id":"r","results":[{"id":"a","loinc":"2345-7","value":1}]}]}`, "collected_at is required"},
		{"duplicate id", `{"reports":[{"id":"r","collected_at":"2026-01-01T00:00:00Z","results":[{"id":"a","loinc":"2345-7","value":1},{"id":"a","loinc":"2345-7","value":2}]}]}`, "used by another result"},
		{"range order", `{"reports":[{"id":"r","collected_at":"2026-01-01T00:00:00Z","results":[{"id":"a","loinc":"2345-7","value":1,"reference_low":5,"reference_high":1}]}]}`, "reference_low"},
		{"entered in error", strings.Replace(strings.Replace(vital, `"vital-signs"`, `"laboratory"`, 1), `"status":"final"`, `"status":"entered-in-error"`, 1), "status must be one of"},
		{"component", strings.Replace(strings.Replace(vital, `"vital-signs"`, `"laboratory"`, 1), `"valueQuantity":{"value":60}`, `"component":[{}]`, 1), "component is not supported"},
		{"value range", strings.Replace(strings.Replace(vital, `"vital-signs"`, `"laboratory"`, 1), `"valueQuantity"`, `"valueRange"`, 1), "valueRange is not supported"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode([]byte(c.body))
			msg, ok := IsInvalid(err)
			if !ok {
				t.Fatalf("err = %v, want an InvalidError", err)
			}
			if !strings.Contains(msg, c.want) {
				t.Fatalf("message %q does not contain %q", msg, c.want)
			}
		})
	}
}

// TestStandaloneObservation reads a single laboratory Observation: a result
// without a report, stored with no report id.
func TestStandaloneObservation(t *testing.T) {
	body := `{"resourceType":"Observation","id":"o1","status":"amended",
	  "category":[{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/observation-category","code":"laboratory"}]}],
	  "code":{"coding":[{"system":"urn:acme","code":"K","display":"Potassium"},{"system":"http://loinc.org","code":"2823-3"}]},
	  "issued":"2026-02-01T10:00:00Z","valueQuantity":{"value":4.1,"comparator":"<","unit":"mmol/L"}}`
	reports, err := Decode([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	r := reports[0].Results[0]
	if reports[0].ID != "" || r.LOINC != "2823-3" || r.Code != "K" || r.System != "urn:acme" || r.Display != "Potassium" ||
		r.Comparator != "<" || r.Status != StatusAmended || !r.IssuedAsCollected {
		t.Fatalf("result = %+v", r)
	}
	o := Observations("u", reports)[0]
	if o.ReportID != "" || o.Metadata[MetaIssuedAsCollected] != true || o.SourceValue != "K" {
		t.Fatalf("observation = %+v", o)
	}
}

// TestNormalizeTable pins every row of the table, and the cases that must
// get no normalized value.
func TestNormalizeTable(t *testing.T) {
	cases := []struct {
		loinc, unit string
		in, want    float64
		wantUnit    string
		ok          bool
	}{
		{"2345-7", "mmol/L", 5.5, 5.5 * 18.0156, "mg/dL", true},
		{"15074-8", "mmol/L", 6.3, 113.49828, "mg/dL", true},
		{"2345-7", "mg/dL", 99, 99, "mg/dL", true},
		{"2093-3", "mmol/L", 5.0, 193.325, "mg/dL", true},
		{"2085-9", "mmol/L", 1.3, 1.3 * 38.665, "mg/dL", true},
		{"13457-7", "mg/dL", 120, 120, "mg/dL", true},
		{"59261-8", "mmol/mol", 48, 48/10.929 + 2.15, "%", true},
		{"4548-4", "%", 6.5, 6.5, "%", true},
		{"14682-9", "umol/L", 88.4, 1, "mg/dL", true},
		{"2160-0", "mg/dL", 0.9, 0.9, "mg/dL", true},
		{"14635-7", "nmol/L", 74.88, 30, "ng/mL", true},
		{"1989-3", "ng/mL", 30, 30, "ng/mL", true},
		// Never guessed: an unknown unit spelling, an analyte outside the
		// table, a known unit on an analyte with no exact conversion.
		{"2345-7", "mmol/l", 5.5, 0, "", false},
		{"2345-7", "g/L", 1, 0, "", false},
		{"35217-9", "mmol/L", 1.3, 0, "", false},
		{"718-7", "g/dL", 7.2, 0, "", false},
		{"", "mmol/L", 5, 0, "", false},
	}
	for _, c := range cases {
		got, unit, ok := Normalize(c.loinc, c.in, c.unit)
		if ok != c.ok || unit != c.wantUnit || (ok && !near(got, c.want)) {
			t.Errorf("Normalize(%s, %v, %s) = %v %q %v, want %v %q %v", c.loinc, c.in, c.unit, got, unit, ok, c.want, c.wantUnit, c.ok)
		}
	}
	seen := map[string]bool{}
	for _, a := range Analytes {
		if _, ok := a.From[a.Unit]; !ok {
			t.Errorf("%s: canonical unit %s does not convert as the identity", a.Name, a.Unit)
		}
		for _, c := range a.LOINC {
			if seen[c] {
				t.Errorf("LOINC %s is listed twice", c)
			}
			seen[c] = true
			if !loincCode.MatchString(c) {
				t.Errorf("LOINC %s is malformed", c)
			}
		}
	}
}

// TestContentHashIgnoresAmendments: an amended copy (other value, other
// collection time) has the identity of the first.
func TestContentHashIgnoresAmendments(t *testing.T) {
	mk := func(v float64, at string) []Report {
		body := `{"reports":[{"id":"r","collected_at":"` + at + `","results":[{"id":"a","loinc":"2345-7","value":` +
			strconv.FormatFloat(v, 'g', -1, 64) + `}]}]}`
		reports, err := Decode([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return reports
	}
	a := Observations("u", mk(90, "2026-01-01T00:00:00Z"))[0]
	b := Observations("u", mk(95, "2026-01-01T01:00:00Z"))[0]
	if a.ContentHash != b.ContentHash {
		t.Fatal("an amended copy changed the identity")
	}
	if Observations("v", mk(90, "2026-01-01T00:00:00Z"))[0].ContentHash == a.ContentHash {
		t.Fatal("two users share an identity")
	}
}
