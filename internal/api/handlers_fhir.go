// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/fhir"
)

// fhirJSON is the FHIR JSON media type.
const fhirJSON = "application/fhir+json"

func (s *server) mountFHIR(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/users/{id}/fhir/Observation", s.fhirObservations)
	s.tenantRoute(mux, "GET /v1/users/{id}/fhir/Observation/{observation_id}", s.fhirObservation)
}

// fhirObservations serves the user's records as a searchset Bundle of
// Observations: quantity samples, blood pressure panels, sleep sessions,
// workouts and daily summaries in one list, paged like every list.
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
	pg, err := read.Observations(r.Context(), s.d.Pool, p.TenantID, user, q, f)
	if err != nil {
		readError(w, r, err, "user")
		return
	}
	obs := make([]fhir.Observation, 0, len(pg.Records))
	for _, rec := range pg.Records {
		obs = append(obs, fhir.FromRecord(user, rec))
	}
	base := s.fhirBase(r, user)
	self := s.publicBase(r) + r.URL.Path
	if r.URL.RawQuery != "" {
		self += "?" + r.URL.RawQuery
	}
	next := ""
	if pg.NextCursor != "" {
		qs.Set("cursor", pg.NextCursor)
		next = s.publicBase(r) + r.URL.Path + "?" + qs.Encode()
	}
	writeFHIR(w, fhir.SearchSet(base, self, next, obs))
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
	rec, err := read.ObservationByID(r.Context(), s.d.Pool, p.TenantID, user, kind, panel, id)
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

// parseFHIRSearch reads the code and category search parameters: at most
// one value each, a single token (no comma list). It returns a message for a
// 400.
func parseFHIRSearch(qs url.Values) (fhirSearch, string) {
	var fs fhirSearch
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
			Sleep: code == fhir.LOINCSleepDuration, Workouts: code == fhir.LOINCExerciseDuration}, nil
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
