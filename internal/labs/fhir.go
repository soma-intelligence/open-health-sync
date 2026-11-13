// SPDX-License-Identifier: AGPL-3.0-only

package labs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MaxBody is the largest upload POST /v1/users/{id}/labs accepts.
const MaxBody = 1 << 20

// FHIR systems the reader knows.
const (
	SystemLOINC          = "http://loinc.org"
	SystemUCUM           = "http://unitsofmeasure.org"
	SystemInterpretation = "http://terminology.hl7.org/CodeSystem/v3-ObservationInterpretation"
	SystemObsCategory    = "http://terminology.hl7.org/CodeSystem/observation-category"
	SystemDiagService    = "http://terminology.hl7.org/CodeSystem/v2-0074"
)

// labSections are the v2-0074 diagnostic service sections of a laboratory.
var labSections = []string{
	"LAB", "BG", "BLB", "CG", "CH", "CP", "HM", "HLA", "IMM", "MB", "MCB", "MYC",
	"OSL", "PAR", "SP", "SR", "TX", "URN", "VR",
}

// Decode reads an upload in either form: a FHIR R4 resource (a Bundle, a
// DiagnosticReport or an Observation) when the object has a resourceType,
// else the simple form {"reports": [...]}. The reports it returns passed
// Check.
func Decode(body []byte) ([]Report, error) {
	var head struct {
		ResourceType *string `json:"resourceType"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return nil, invalid("the body is not a JSON object")
	}
	var reports []Report
	if head.ResourceType != nil {
		var err error
		if reports, err = decodeFHIR(body, *head.ResourceType); err != nil {
			return nil, err
		}
	} else {
		var u Upload
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&u); err != nil {
			return nil, invalid("the body is neither a FHIR resource nor a reports object: %s", jsonProblem(err))
		}
		if dec.More() {
			return nil, invalid("the body holds more than one JSON value")
		}
		reports = u.Reports
	}
	if err := Check(reports); err != nil {
		return nil, err
	}
	return reports, nil
}

// jsonProblem names a decoding problem without quoting the body.
func jsonProblem(err error) string {
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		return fmt.Sprintf("%s has the wrong type", ute.Field)
	}
	if strings.HasPrefix(err.Error(), "json: unknown field ") {
		return strings.TrimPrefix(err.Error(), "json: ")
	}
	return "it is not valid JSON"
}

// FHIR R4 shapes, only the elements the reader uses.
type (
	fCoding struct {
		System  string `json:"system"`
		Code    string `json:"code"`
		Display string `json:"display"`
	}
	fConcept struct {
		Coding []fCoding `json:"coding"`
		Text   string    `json:"text"`
	}
	fReference struct {
		Reference string `json:"reference"`
		Display   string `json:"display"`
	}
	fIdentifier struct {
		System string `json:"system"`
		Value  string `json:"value"`
	}
	fQuantity struct {
		Value      *float64 `json:"value"`
		Comparator string   `json:"comparator"`
		Unit       string   `json:"unit"`
		System     string   `json:"system"`
		Code       string   `json:"code"`
	}
	fPeriod struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	fRange struct {
		Low  *fQuantity `json:"low"`
		High *fQuantity `json:"high"`
		Text string     `json:"text"`
	}
	fResource struct {
		ResourceType string            `json:"resourceType"`
		ID           string            `json:"id"`
		Identifier   []fIdentifier     `json:"identifier"`
		Status       string            `json:"status"`
		Category     json.RawMessage   `json:"category"`
		Code         *fConcept         `json:"code"`
		Contained    []json.RawMessage `json:"contained"`

		// DiagnosticReport
		Result    []fReference `json:"result"`
		BasedOn   []fReference `json:"basedOn"`
		Performer []fReference `json:"performer"`
		Issued    string       `json:"issued"`

		// Observation
		EffectiveDateTime string          `json:"effectiveDateTime"`
		EffectiveInstant  string          `json:"effectiveInstant"`
		EffectivePeriod   *fPeriod        `json:"effectivePeriod"`
		ValueQuantity     *fQuantity      `json:"valueQuantity"`
		ValueString       *string         `json:"valueString"`
		ValueCodeable     *fConcept       `json:"valueCodeableConcept"`
		ValueBoolean      *bool           `json:"valueBoolean"`
		ValueInteger      *int64          `json:"valueInteger"`
		DataAbsentReason  *fConcept       `json:"dataAbsentReason"`
		Interpretation    []fConcept      `json:"interpretation"`
		ReferenceRange    []fRange        `json:"referenceRange"`
		Specimen          *fReference     `json:"specimen"`
		HasMember         []fReference    `json:"hasMember"`
		Component         json.RawMessage `json:"component"`

		// Organization, Practitioner
		Name json.RawMessage `json:"name"`
		// Specimen
		Type *fConcept `json:"type"`
		// ServiceRequest
		Requester *fReference `json:"requester"`

		raw map[string]json.RawMessage
	}
	fBundle struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
)

// supportedValues are the value[x] elements of an Observation the reader takes.
var supportedValues = []string{"valueQuantity", "valueString", "valueCodeableConcept", "valueBoolean", "valueInteger"}

func parseResource(raw json.RawMessage) (*fResource, error) {
	var r fResource
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &r.raw); err != nil {
		return nil, err
	}
	return &r, nil
}

// pool resolves references inside one upload.
type pool struct {
	byRef map[string]*fResource
}

func (p *pool) add(key string, r *fResource) {
	if key != "" {
		p.byRef[key] = r
	}
}

func (p *pool) addResource(fullURL string, r *fResource) {
	p.add(fullURL, r)
	if r.ID != "" {
		p.add(r.ResourceType+"/"+r.ID, r)
	}
}

// resolve finds a reference: a fullUrl, a relative Type/id, the tail of an
// absolute URL, or a contained #id of owner.
func (p *pool) resolve(owner *fResource, ref fReference) *fResource {
	s := ref.Reference
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "#") && owner != nil {
		for _, c := range owner.Contained {
			r, err := parseResource(c)
			if err == nil && "#"+r.ID == s {
				return r
			}
		}
		return nil
	}
	if r := p.byRef[s]; r != nil {
		return r
	}
	if parts := strings.Split(strings.TrimRight(s, "/"), "/"); len(parts) >= 2 {
		if r := p.byRef[parts[len(parts)-2]+"/"+parts[len(parts)-1]]; r != nil {
			return r
		}
	}
	return nil
}

func decodeFHIR(body []byte, kind string) ([]Report, error) {
	p := &pool{byRef: map[string]*fResource{}}
	var reports, observations []*fResource
	switch kind {
	case "Bundle":
		var b fBundle
		if err := json.Unmarshal(body, &b); err != nil {
			return nil, invalid("Bundle: %s", jsonProblem(err))
		}
		for i, e := range b.Entry {
			if len(e.Resource) == 0 {
				return nil, invalid("Bundle.entry[%d] has no resource", i)
			}
			r, err := parseResource(e.Resource)
			if err != nil {
				return nil, invalid("Bundle.entry[%d].resource: %s", i, jsonProblem(err))
			}
			p.addResource(e.FullURL, r)
			switch r.ResourceType {
			case "DiagnosticReport":
				reports = append(reports, r)
			case "Observation":
				observations = append(observations, r)
			}
		}
		if len(reports) == 0 && len(observations) == 0 {
			return nil, invalid("Bundle holds no DiagnosticReport and no Observation")
		}
	case "DiagnosticReport", "Observation":
		r, err := parseResource(body)
		if err != nil {
			return nil, invalid("%s: %s", kind, jsonProblem(err))
		}
		p.addResource("", r)
		if kind == "DiagnosticReport" {
			reports = append(reports, r)
		} else {
			observations = append(observations, r)
		}
	default:
		return nil, invalid("resourceType must be Bundle, DiagnosticReport or Observation")
	}

	var out []Report
	used := map[*fResource]bool{}
	for i, dr := range reports {
		at := fmt.Sprintf("DiagnosticReport[%d].", i)
		rep, err := p.report(at, dr, used)
		if err != nil {
			return nil, err
		}
		out = append(out, rep)
	}
	// Observations no lab report referenced must be laboratory ones.
	var alone Report
	for i, o := range observations {
		if used[o] {
			continue
		}
		at := fmt.Sprintf("Observation[%d].", i)
		lab, has, err := observationCategory(o)
		if err != nil {
			return nil, invalid("%scategory: %s", at, err.Error())
		}
		if !lab {
			if has {
				return nil, invalid("%scategory must be laboratory: only laboratory results are accepted here", at)
			}
			return nil, invalid("%scategory laboratory is required on an Observation sent without a DiagnosticReport", at)
		}
		res, err := p.results(at, o, nil, used)
		if err != nil {
			return nil, err
		}
		alone.Results = append(alone.Results, res...)
	}
	if len(alone.Results) > 0 {
		out = append(out, alone)
	}
	return out, nil
}

// categories reads a category element (a list of CodeableConcept).
func categories(raw json.RawMessage) ([]fConcept, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var cs []fConcept
	if err := json.Unmarshal(raw, &cs); err != nil {
		return nil, fmt.Errorf("must be a list of CodeableConcept")
	}
	return cs, nil
}

// observationCategory says whether an Observation is a laboratory one, and
// whether it has a category at all.
func observationCategory(o *fResource) (lab, has bool, err error) {
	cs, err := categories(o.Category)
	if err != nil {
		return false, false, err
	}
	for _, c := range cs {
		for _, cd := range c.Coding {
			has = true
			if cd.Code == "laboratory" && (cd.System == SystemObsCategory || cd.System == "") {
				lab = true
			}
		}
	}
	return lab, has, nil
}

// reportCategory says whether a DiagnosticReport is a laboratory one. A
// report with no category is refused: nothing says it is a lab report.
func reportCategory(dr *fResource) (bool, error) {
	cs, err := categories(dr.Category)
	if err != nil {
		return false, err
	}
	for _, c := range cs {
		for _, cd := range c.Coding {
			switch {
			case cd.System == SystemDiagService && slices.Contains(labSections, cd.Code):
				return true, nil
			case cd.Code == "laboratory" && (cd.System == SystemObsCategory || cd.System == ""):
				return true, nil
			}
		}
	}
	return false, nil
}

// sourceID is a resource's id, else its first identifier as system|value.
func sourceID(r *fResource) string {
	if r.ID != "" {
		return r.ID
	}
	for _, i := range r.Identifier {
		if i.Value != "" {
			if i.System != "" {
				return i.System + "|" + i.Value
			}
			return i.Value
		}
	}
	return ""
}

var reportStatus = map[string]string{
	"registered":  StatusPreliminary,
	"partial":     StatusPreliminary,
	"preliminary": StatusPreliminary,
	"final":       StatusFinal,
	"amended":     StatusAmended,
	"appended":    StatusAmended,
	"corrected":   StatusCorrected,
	"cancelled":   StatusCancelled,
}

func (p *pool) report(at string, dr *fResource, used map[*fResource]bool) (Report, error) {
	lab, err := reportCategory(dr)
	if err != nil {
		return Report{}, invalid("%scategory %s", at, err.Error())
	}
	if !lab {
		return Report{}, invalid("%scategory must be a laboratory section (v2-0074 LAB, CH, HM and the other lab sections, or laboratory): only laboratory reports are accepted here", at)
	}
	rep := Report{ID: sourceID(dr)}
	if rep.ID == "" {
		return Report{}, invalid("%sid or identifier is required", at)
	}
	if dr.Status != "" {
		st, ok := reportStatus[dr.Status]
		if !ok {
			return Report{}, invalid("%sstatus must be registered, partial, preliminary, final, amended, appended, corrected or cancelled", at)
		}
		rep.Status = st
	}
	if dr.Code != nil {
		rep.LOINC, rep.Code, rep.System, rep.Display = codeOf(dr.Code)
	}
	if rep.CollectedAt, _, err = effective(at, dr); err != nil {
		return Report{}, err
	}
	if rep.IssuedAt, err = fhirTime(at+"issued", dr.Issued); err != nil {
		return Report{}, err
	}
	rep.Performer = p.name(dr, dr.Performer)
	for _, b := range dr.BasedOn {
		if sr := p.resolve(dr, b); sr != nil && sr.ResourceType == "ServiceRequest" && sr.Requester != nil {
			rep.Orderer = p.name(sr, []fReference{*sr.Requester})
			break
		}
	}
	if len(dr.Result) == 0 {
		return Report{}, invalid("%sresult must reference at least one Observation", at)
	}
	for j, ref := range dr.Result {
		rat := fmt.Sprintf("%sresult[%d]", at, j)
		o := p.resolve(dr, ref)
		if o == nil {
			return Report{}, invalid("%s references an Observation that is not in the upload", rat)
		}
		if o.ResourceType != "Observation" {
			return Report{}, invalid("%s must reference an Observation", rat)
		}
		lab, has, err := observationCategory(o)
		if err != nil {
			return Report{}, invalid("%s category: %s", rat, err.Error())
		}
		if has && !lab {
			return Report{}, invalid("%s is an Observation whose category is not laboratory", rat)
		}
		res, err := p.results(rat+".", o, dr, used)
		if err != nil {
			return Report{}, err
		}
		rep.Results = append(rep.Results, res...)
	}
	if rep.CollectedAt == nil {
		// No collection time on the report: the results carry their own, or
		// fall back to the issued time.
		for i := range rep.Results {
			if rep.Results[i].CollectedAt == nil {
				return Report{}, invalid("%sresult[%d] has no effective time and no issued time", at, i)
			}
		}
	}
	return rep, nil
}

// name is the display of the first reference, else the name of the
// Organization or Practitioner it resolves to.
func (p *pool) name(owner *fResource, refs []fReference) string {
	for _, ref := range refs {
		if ref.Display != "" {
			return ref.Display
		}
		if r := p.resolve(owner, ref); r != nil {
			if n := resourceName(r); n != "" {
				return n
			}
		}
	}
	return ""
}

func resourceName(r *fResource) string {
	if len(r.Name) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(r.Name, &s) == nil {
		return s
	}
	var hn []struct {
		Text   string   `json:"text"`
		Family string   `json:"family"`
		Given  []string `json:"given"`
	}
	if json.Unmarshal(r.Name, &hn) == nil && len(hn) > 0 {
		if hn[0].Text != "" {
			return hn[0].Text
		}
		return strings.TrimSpace(strings.Join(append(slices.Clone(hn[0].Given), hn[0].Family), " "))
	}
	return ""
}

// codeOf splits a code into its LOINC code, the source's own code (the first
// coding that is not LOINC) and a display name.
func codeOf(c *fConcept) (loinc, code, system, display string) {
	var loincDisplay string
	for _, cd := range c.Coding {
		if cd.System == SystemLOINC {
			if loinc == "" {
				loinc, loincDisplay = cd.Code, cd.Display
			}
			continue
		}
		if code == "" && cd.Code != "" {
			code, system = cd.Code, cd.System
			if display == "" {
				display = cd.Display
			}
		}
	}
	switch {
	case c.Text != "":
		display = c.Text
	case display == "":
		display = loincDisplay
	}
	return loinc, code, system, display
}

// results reads one Observation into its results: one, or the results of
// its members when it only groups others (hasMember without a value).
func (p *pool) results(at string, o *fResource, dr *fResource, used map[*fResource]bool) ([]Result, error) {
	if used[o] {
		return nil, invalid("%s is referenced twice in the upload", strings.TrimSuffix(at, "."))
	}
	used[o] = true
	if len(o.HasMember) > 0 && !hasValue(o) {
		var out []Result
		for k, m := range o.HasMember {
			mat := fmt.Sprintf("%shasMember[%d]", at, k)
			mo := p.resolve(o, m)
			if mo == nil || mo.ResourceType != "Observation" {
				return nil, invalid("%s references an Observation that is not in the upload", mat)
			}
			res, err := p.results(mat+".", mo, dr, used)
			if err != nil {
				return nil, err
			}
			out = append(out, res...)
		}
		return out, nil
	}
	r, err := p.result(at, o, dr)
	if err != nil {
		return nil, err
	}
	return []Result{r}, nil
}

func hasValue(o *fResource) bool {
	for _, k := range supportedValues {
		if _, ok := o.raw[k]; ok {
			return true
		}
	}
	return false
}

func (p *pool) result(at string, o *fResource, dr *fResource) (Result, error) {
	r := Result{ID: sourceID(o)}
	if r.ID == "" {
		return Result{}, invalid("%sid or identifier is required", at)
	}
	switch o.Status {
	case StatusPreliminary, StatusFinal, StatusAmended, StatusCorrected, StatusCancelled:
		r.Status = o.Status
	case "":
		return Result{}, invalid("%sstatus is required", at)
	default:
		return Result{}, invalid("%sstatus must be one of %s", at, strings.Join(Statuses, ", "))
	}
	if o.Code == nil {
		return Result{}, invalid("%scode is required", at)
	}
	r.LOINC, r.Code, r.System, r.Display = codeOf(o.Code)
	if r.LOINC == "" && r.Code == "" {
		return Result{}, invalid("%scode needs a coding with a code", at)
	}
	if len(o.Component) > 0 && string(o.Component) != "null" {
		return Result{}, invalid("%scomponent is not supported: send each component as its own Observation", at)
	}
	for k := range o.raw {
		if strings.HasPrefix(k, "value") && !slices.Contains(supportedValues, k) {
			return Result{}, invalid("%s%s is not supported: use valueQuantity, valueString, valueCodeableConcept, valueBoolean or valueInteger", at, k)
		}
	}
	switch {
	case o.ValueQuantity != nil:
		q := o.ValueQuantity
		if q.Value == nil {
			return Result{}, invalid("%svalueQuantity.value is required", at)
		}
		v := *q.Value
		r.Value = &v
		r.Comparator = q.Comparator
		r.Unit = unitOf(q)
	case o.ValueString != nil:
		r.ValueString = *o.ValueString
	case o.ValueCodeable != nil:
		c := o.ValueCodeable
		if len(c.Coding) > 0 && c.Coding[0].Code != "" {
			r.ValueCode, r.ValueSystem = c.Coding[0].Code, c.Coding[0].System
		} else {
			r.ValueString = c.Text
		}
	case o.ValueBoolean != nil:
		r.ValueString = strconv.FormatBool(*o.ValueBoolean)
	case o.ValueInteger != nil:
		v := float64(*o.ValueInteger)
		r.Value = &v
	}
	if o.DataAbsentReason != nil {
		_, code, _, display := codeOf(o.DataAbsentReason)
		r.Absent = code
		if r.Absent == "" {
			r.Absent = display
		}
		if r.Absent == "" {
			r.Absent = "unknown"
		}
	}
	if len(o.ReferenceRange) > 0 {
		rr := o.ReferenceRange[0]
		if rr.Low != nil && rr.Low.Value != nil {
			v := *rr.Low.Value
			r.ReferenceLow = &v
		}
		if rr.High != nil && rr.High.Value != nil {
			v := *rr.High.Value
			r.ReferenceHigh = &v
		}
		r.ReferenceText = rr.Text
	}
	if len(o.Interpretation) > 0 {
		c := o.Interpretation[0]
		for _, cd := range c.Coding {
			if (cd.System == SystemInterpretation || cd.System == "") && slices.Contains(Interpretations, cd.Code) {
				r.Interpretation = cd.Code
				break
			}
		}
		if r.Interpretation == "" {
			_, code, _, display := codeOf(&c)
			r.InterpretationText = display
			if r.InterpretationText == "" {
				r.InterpretationText = code
			}
		}
	}
	if o.Specimen != nil {
		r.Specimen = o.Specimen.Display
		if r.Specimen == "" {
			if s := p.resolve(o, *o.Specimen); s != nil && s.Type != nil {
				_, code, _, display := codeOf(s.Type)
				r.Specimen = display
				if r.Specimen == "" {
					r.Specimen = code
				}
			}
		}
	}
	var err error
	if r.CollectedAt, r.CollectedEnd, err = effective(at, o); err != nil {
		return Result{}, err
	}
	if r.IssuedAt, err = fhirTime(at+"issued", o.Issued); err != nil {
		return Result{}, err
	}
	r.Performer = p.name(o, o.Performer)
	if r.CollectedAt == nil {
		// The report's collection time applies (Check copies it), else the
		// result's or the report's issued time stands in.
		var drEff *time.Time
		if dr != nil {
			drEff, _, _ = effective(at, dr)
		}
		if drEff == nil {
			issued := r.IssuedAt
			if issued == nil && dr != nil {
				issued, _ = fhirTime(at+"issued", dr.Issued)
			}
			if issued != nil {
				t := *issued
				r.CollectedAt = &t
				r.IssuedAsCollected = true
			}
		}
	}
	return r, nil
}

// unitOf is a quantity's UCUM code, else its unit text.
func unitOf(q *fQuantity) string {
	if q.Code != "" && (q.System == SystemUCUM || q.System == "") {
		return q.Code
	}
	return q.Unit
}

// effective reads effective[x]: a dateTime, an instant or a period.
func effective(at string, r *fResource) (*time.Time, *time.Time, error) {
	switch {
	case r.EffectiveDateTime != "":
		t, err := fhirTime(at+"effectiveDateTime", r.EffectiveDateTime)
		return t, nil, err
	case r.EffectiveInstant != "":
		t, err := fhirTime(at+"effectiveInstant", r.EffectiveInstant)
		return t, nil, err
	case r.EffectivePeriod != nil:
		start, err := fhirTime(at+"effectivePeriod.start", r.EffectivePeriod.Start)
		if err != nil {
			return nil, nil, err
		}
		end, err := fhirTime(at+"effectivePeriod.end", r.EffectivePeriod.End)
		if err != nil {
			return nil, nil, err
		}
		if start == nil {
			start, end = end, nil
		}
		return start, end, nil
	}
	return nil, nil, nil
}

// fhirTime reads a FHIR dateTime or instant: a full time with its zone, or a
// date (a year, a month or a day, taken at its start in UTC).
func fhirTime(at, s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t, nil
		}
	}
	return nil, invalid("%s must be a FHIR dateTime with a time zone, or a date", at)
}
