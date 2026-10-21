// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/fhir"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// fhirJSON is the FHIR JSON media type.
const fhirJSON = "application/fhir+json"

func (s *server) mountFHIR(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/users/{id}/fhir/Observation", s.fhirObservations)
	s.tenantRoute(mux, "GET /v1/users/{id}/fhir/Observation/{observation_id}", s.fhirObservation)
	s.tenantRoute(mux, "GET /v1/users/{id}/fhir/Patient/{patient_id}", s.fhirPatient)
	s.tenantRoute(mux, "GET /v1/users/{id}/fhir/Patient/{patient_id}/$everything", s.fhirEverything)
	s.tenantRoute(mux, "GET /v1/users/{id}/fhir/Device", s.fhirDevices)
	s.tenantRoute(mux, "GET /v1/users/{id}/fhir/Device/{device_id}", s.fhirDevice)
	s.tenantRoute(mux, "GET /v1/users/{id}/fhir/Provenance", s.fhirProvenances)
	s.tenantRoute(mux, "GET /v1/users/{id}/fhir/Provenance/{provenance_id}", s.fhirProvenance)
	s.tenantRoute(mux, "GET /v1/users/{id}/fhir/metadata", s.fhirMetadata)
}

// fhirObservations serves the user's records as a searchset Bundle of
// Observations: quantity samples, blood pressure panels, sleep sessions,
// workouts, daily summaries, ECG recordings and nutrition logs in one list,
// paged like every list.
func (s *server) fhirObservations(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	q, err := parseReadQuery(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	qs := r.URL.Query()
	fs, msg := parseFHIRSearch(qs)
	if msg != "" {
		badRequest(w, msg)
		return
	}
	if err := read.CheckObservationQuery(q); err != nil {
		readError(w, r, err, "user")
		return
	}
	if err := read.CheckUser(r.Context(), s.d.Pool, p.TenantID, user); err != nil {
		readError(w, r, err, "user")
		return
	}
	f, err := s.observationFilter(r, p.TenantID, user, q.Type, fs)
	if err != nil {
		readError(w, r, err, "user")
		return
	}
	s.mergePolicy(r, p.TenantID, user, &q)
	pg, err := read.Observations(r.Context(), s.d.Pool, p.TenantID, user, q, f)
	if err != nil {
		readError(w, r, err, "user")
		return
	}
	obs := make([]fhir.Observation, 0, len(pg.Records))
	for _, rec := range pg.Records {
		obs = append(obs, fhir.FromRecord(user, rec))
	}
	self, next := s.pageLinks(r, pg.NextCursor)
	writeFHIR(w, fhir.SearchSet(s.fhirBase(r, user), self, next, obs))
}

// pageLinks are the self link of r and, when cursor is set, its next link:
// the same query with the cursor of the next page.
func (s *server) pageLinks(r *http.Request, cursor string) (self, next string) {
	self = s.publicBase(r) + r.URL.Path
	if r.URL.RawQuery != "" {
		self += "?" + r.URL.RawQuery
	}
	if cursor != "" {
		qs := r.URL.Query()
		qs.Set("cursor", cursor)
		next = s.publicBase(r) + r.URL.Path + "?" + qs.Encode()
	}
	return self, next
}

// fhirUser is the Patient of the request's user, 404 when the user is not
// the tenant's or {patient_id} is not the user's id.
func (s *server) fhirUser(w http.ResponseWriter, r *http.Request) (fhir.Patient, uuid.UUID, bool) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return fhir.Patient{}, user, false
	}
	if r.PathValue("patient_id") != user.String() {
		notFound(w, "patient")
		return fhir.Patient{}, user, false
	}
	u, err := s.d.Store.GetActiveUser(r.Context(), p.TenantID, user)
	if errors.Is(err, tenant.ErrNotFound) {
		notFound(w, "patient")
		return fhir.Patient{}, user, false
	}
	if err != nil {
		internalError(w, r, "api: fhir patient", err)
		return fhir.Patient{}, user, false
	}
	return fhir.FromUser(p.TenantID, user, u.ExternalRef), user, true
}

// fhirPatient reads the user as a Patient.
func (s *server) fhirPatient(w http.ResponseWriter, r *http.Request) {
	if pt, _, ok := s.fhirUser(w, r); ok {
		writeFHIR(w, pt)
	}
}

// fhirEverything serves one page of the Patient's $everything: the Patient
// on the first page, then the page of Observations of the Observation search
// with their Devices and Provenance.
func (s *server) fhirEverything(w http.ResponseWriter, r *http.Request) {
	q, err := parseReadQuery(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if q.Type != "" {
		badRequest(w, "$everything takes start, end, limit and cursor only")
		return
	}
	if err := read.CheckObservationQuery(q); err != nil {
		readError(w, r, err, "patient")
		return
	}
	pt, user, ok := s.fhirUser(w, r)
	if !ok {
		return
	}
	p, _ := auth.From(r.Context())
	s.mergePolicy(r, p.TenantID, user, &q)
	pg, err := read.Observations(r.Context(), s.d.Pool, p.TenantID, user, q, read.AllObservations())
	if err != nil {
		readError(w, r, err, "patient")
		return
	}
	var patient *fhir.Patient
	if q.Cursor == "" {
		patient = &pt
	}
	self, next := s.pageLinks(r, pg.NextCursor)
	writeFHIR(w, fhir.Everything(user, s.fhirBase(r, user), self, next, patient, pg.Records))
}

// fhirDevices serves the devices of the user's records in a time window as
// a searchset Bundle (one page).
func (s *server) fhirDevices(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	q, err := parseReadQuery(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if q.Type != "" || q.Cursor != "" || q.Limit != 0 {
		badRequest(w, "the Device search takes start and end only")
		return
	}
	devices, err := read.Devices(r.Context(), s.d.Pool, p.TenantID, user, q)
	if err != nil {
		readError(w, r, err, "user")
		return
	}
	self, _ := s.pageLinks(r, "")
	writeFHIR(w, fhir.DeviceSet(user, s.fhirBase(r, user), self, devices))
}

// fhirDevice reads one Device by its id.
func (s *server) fhirDevice(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("device_id")
	if !strings.HasPrefix(id, fhir.DeviceIDPrefix) {
		notFound(w, "device")
		return
	}
	devices, err := read.Devices(r.Context(), s.d.Pool, p.TenantID, user, read.Query{})
	if err != nil {
		readError(w, r, err, "device")
		return
	}
	for _, d := range devices {
		if fhir.DeviceID(user, d) == id {
			writeFHIR(w, fhir.FromDevice(user, d))
			return
		}
	}
	notFound(w, "device")
}

// fhirProvenances serves the Provenance of one Observation, named by the
// target parameter, as a searchset Bundle.
func (s *server) fhirProvenances(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	if v := qs["target"]; len(v) != 1 || strings.Contains(v[0], ",") {
		badRequest(w, "target is required, one value: Observation/{id}")
		return
	}
	obsID, ok := strings.CutPrefix(qs.Get("target"), "Observation/")
	if !ok {
		badRequest(w, "target must be Observation/{id}")
		return
	}
	if err := read.CheckUser(r.Context(), s.d.Pool, p.TenantID, user); err != nil {
		readError(w, r, err, "user")
		return
	}
	var provs []fhir.Provenance
	if kind, panel, id, ok := fhir.ParseID(obsID); ok {
		rec, err := read.ObservationByID(r.Context(), s.d.Pool, p.TenantID, user, kind, panel, id, read.ViewAll)
		switch {
		case err == nil:
			provs = append(provs, fhir.FromRecordProvenance(user, rec))
		case !errors.Is(err, read.ErrNotFound):
			readError(w, r, err, "user")
			return
		}
	}
	// An Observation that does not exist has no Provenance: an empty Bundle.
	self, _ := s.pageLinks(r, "")
	writeFHIR(w, fhir.ProvenanceSet(s.fhirBase(r, user), self, provs))
}

// fhirProvenance reads the Provenance of one Observation by its id.
func (s *server) fhirProvenance(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	obsID, ok := fhir.ParseProvenanceID(r.PathValue("provenance_id"))
	if !ok {
		notFound(w, "provenance")
		return
	}
	kind, panel, id, _ := fhir.ParseID(obsID)
	rec, err := read.ObservationByID(r.Context(), s.d.Pool, p.TenantID, user, kind, panel, id, read.ViewAll)
	if err != nil {
		readError(w, r, err, "provenance")
		return
	}
	writeFHIR(w, fhir.FromRecordProvenance(user, rec))
}

// fhirMetadata serves the CapabilityStatement of the user's FHIR base.
func (s *server) fhirMetadata(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	if err := read.CheckUser(r.Context(), s.d.Pool, p.TenantID, user); err != nil {
		readError(w, r, err, "user")
		return
	}
	writeFHIR(w, fhir.Capability(s.fhirBase(r, user)))
}

// fhirObservation reads one Observation by the id the Bundle gave it.
func (s *server) fhirObservation(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	kind, panel, id, ok := fhir.ParseID(r.PathValue("observation_id"))
	if !ok {
		notFound(w, "observation")
		return
	}
	view, err := read.ParseView(r.URL.Query().Get("view"))
	if err != nil {
		readError(w, r, err, "observation")
		return
	}
	rec, err := read.ObservationByID(r.Context(), s.d.Pool, p.TenantID, user, kind, panel, id, view)
	if err != nil {
		readError(w, r, err, "observation")
		return
	}
	writeFHIR(w, fhir.FromRecord(user, rec))
}

// fhirSearch is the code and category of a search, checked.
type fhirSearch struct {
	// codeSystem and code are the code token, when there is one.
	codeSystem, code string
	// category is an HL7 observation category code, when there is one.
	category string
}

// hl7Categories are the codes of the HL7 observation category system: a
// search for one the mapping never sets finds nothing, any other is a 400.
var hl7Categories = []string{"social-history", "vital-signs", "imaging", "laboratory", "procedure",
	"survey", "exam", "therapy", "activity"}

// fhirParams are the search parameters the Observation search takes.
var fhirParams = []string{"type", "code", "category", "start", "end", "limit", "cursor", "view"}

// fhirHints name the supported parameter that does what a standard FHIR
// search parameter would.
var fhirHints = map[string]string{
	"date":   "use start and end (RFC 3339 or YYYY-MM-DD)",
	"_count": "use limit",
}

// parseFHIRSearch reads the code and category search parameters: at most
// one value each, a single token (no comma list). Any other parameter than
// fhirParams is refused rather than ignored, so a client never takes an
// unfiltered list for a filtered one (_format may ask for JSON, the only
// format served). It returns a message for a 400.
func parseFHIRSearch(qs url.Values) (fhirSearch, string) {
	var fs fhirSearch
	names := make([]string, 0, len(qs))
	for name := range qs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		switch {
		case slices.Contains(fhirParams, name):
		case name == "_format" && slices.Contains([]string{"json", "application/json", fhirJSON}, qs.Get(name)):
		case fhirHints[name] != "":
			return fs, "search parameter " + name + " is not supported: " + fhirHints[name]
		default:
			return fs, "search parameter " + name + " is not supported: use " + strings.Join(fhirParams, ", ")
		}
	}
	for _, name := range []string{"code", "category"} {
		switch v := qs[name]; {
		case len(v) == 0:
			continue
		case len(v) > 1 || strings.Contains(v[0], ","):
			return fs, name + " takes a single value"
		}
	}
	if v := qs.Get("code"); qs.Has("code") {
		system, c, ok := strings.Cut(v, "|")
		if !ok || system == "" || c == "" {
			return fs, "code must be system|code, for example " + fhir.LOINC + "|8867-4"
		}
		switch system {
		case fhir.LOINC, fhir.QuantityTypeSystem, fhir.RecordTypeSystem:
		default:
			return fs, "code system not supported: use " + fhir.LOINC + ", " +
				fhir.QuantityTypeSystem + " or " + fhir.RecordTypeSystem
		}
		fs.codeSystem, fs.code = system, c
	}
	if v := qs.Get("category"); qs.Has("category") {
		c := v
		if system, rest, ok := strings.Cut(v, "|"); ok {
			if system != "" && system != fhir.ObservationCategory {
				return fs, "category system not supported: use " + fhir.ObservationCategory
			}
			c = rest
		}
		if !slices.Contains(hl7Categories, c) {
			return fs, "category must be an observation category code, for example " + fhir.CategoryVitalSigns
		}
		fs.category = c
	}
	return fs, ""
}

// observationFilter turns the type parameter and the code and category of
// fs into the records the search keeps: those all of them keep. Its error is
// a read error (an unknown quantity type is a QueryError).
func (s *server) observationFilter(r *http.Request, tenantID, user uuid.UUID, typ string, fs fhirSearch) (read.ObservationFilter, error) {
	f := read.AllObservations()
	quantityType := func(qt string) (read.ObservationFilter, error) {
		if err := read.CheckQuantityType(r.Context(), s.d.Pool, tenantID, user, qt); err != nil {
			return read.ObservationFilter{}, err
		}
		return read.ObservationFilter{Quantities: true, Types: []string{qt}}, nil
	}
	if typ != "" {
		g, err := quantityType(typ)
		if err != nil {
			return f, err
		}
		f = f.And(g)
	}
	if fs.code != "" {
		g, err := s.codeFilter(r, fs.codeSystem, fs.code, quantityType)
		if err != nil {
			return f, err
		}
		f = f.And(g)
	}
	if fs.category != "" {
		g := read.ObservationFilter{Quantities: true, Types: fhir.TypesWithCategory(fs.category)}
		if g.Types == nil {
			g.Types = []string{}
		}
		if fs.category == fhir.CategoryActivity {
			g.Workouts, g.Daily = true, true
		}
		f = f.And(g)
	}
	return f, nil
}

// codeFilter keeps the records whose Observation.code carries system|code.
func (s *server) codeFilter(r *http.Request, system, code string, quantityType func(string) (read.ObservationFilter, error)) (read.ObservationFilter, error) {
	switch system {
	case fhir.LOINC:
		switch code {
		case fhir.LOINCBloodPressurePanel, fhir.LOINCSystolic, fhir.LOINCDiastolic:
			// The panels: R4 allows the codes of the two halves only as the
			// panel's components, so a search for either finds the panel.
			return read.ObservationFilter{Quantities: true, PanelsOnly: true}, nil
		}
		types, err := read.TypesForLOINC(r.Context(), s.d.Pool, code)
		if err != nil {
			return read.ObservationFilter{}, err
		}
		for _, t := range fhir.TypesWithExtraLOINC(code) {
			if !slices.Contains(types, t) {
				types = append(types, t)
			}
		}
		if types == nil {
			types = []string{}
		}
		return read.ObservationFilter{Quantities: true, Types: types,
			Sleep: code == fhir.LOINCSleepDuration, Workouts: code == fhir.LOINCExerciseDuration,
			ECG: code == fhir.LOINCECGStudy}, nil
	case fhir.QuantityTypeSystem:
		return quantityType(code)
	}
	switch code { // fhir.RecordTypeSystem
	case fhir.RecordBloodPressure:
		return read.ObservationFilter{Quantities: true, PanelsOnly: true}, nil
	case fhir.RecordSleepSession:
		return read.ObservationFilter{Sleep: true}, nil
	case fhir.RecordWorkout:
		return read.ObservationFilter{Workouts: true}, nil
	case fhir.RecordDailySummary:
		return read.ObservationFilter{Daily: true}, nil
	case fhir.RecordECG:
		return read.ObservationFilter{ECG: true}, nil
	case fhir.RecordNutritionLog:
		return read.ObservationFilter{Nutrition: true}, nil
	}
	return read.ObservationFilter{}, nil
}

// publicBase is the scheme and host links start with: OHS_PUBLIC_URL, else
// the request's own host.
func (s *server) publicBase(r *http.Request) string {
	if s.d.PublicURL != "" {
		return strings.TrimRight(s.d.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: r.Host}).String()
}

// fhirBase is the FHIR base URL of one user's resources.
func (s *server) fhirBase(r *http.Request, user uuid.UUID) string {
	return s.publicBase(r) + "/v1/users/" + user.String() + "/fhir"
}

func writeFHIR(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", fhirJSON)
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // links keep their & as is
	_ = enc.Encode(v)
}
