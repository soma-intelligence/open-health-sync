// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"net/url"
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

// fhirObservations serves the user's quantity samples as a searchset Bundle
// of Observations, paged like every list.
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
	loinc, empty, msg := parseFHIRCode(r.URL.Query()["code"], &q)
	if msg != "" {
		badRequest(w, msg)
		return
	}
	var pg read.Page[read.CodedSample]
	if !empty {
		pg, err = read.CodedSamples(r.Context(), s.d.Pool, p.TenantID, user, q, loinc)
	} else {
		// A code no Observation carries: an empty page, once the query and
		// the user check out like any other.
		if err = q.Check(); err == nil {
			err = read.CheckUser(r.Context(), s.d.Pool, p.TenantID, user)
		}
	}
	if err != nil {
		readError(w, r, err, "user")
		return
	}
	obs := make([]fhir.Observation, 0, len(pg.Records))
	for _, rec := range pg.Records {
		obs = append(obs, fhir.FromQuantitySample(user, rec))
	}
	base := s.fhirBase(r, user)
	self := s.publicBase(r) + r.URL.Path
	if r.URL.RawQuery != "" {
		self += "?" + r.URL.RawQuery
	}
	next := ""
	if pg.NextCursor != "" {
		qs := r.URL.Query()
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
	id, ok := fhir.ParseObservationID(r.PathValue("observation_id"))
	if !ok {
		notFound(w, "observation")
		return
	}
	rec, err := read.CodedSampleByID(r.Context(), s.d.Pool, p.TenantID, user, id)
	if err != nil {
		readError(w, r, err, "observation")
		return
	}
	writeFHIR(w, fhir.FromQuantitySample(user, rec))
}

// parseFHIRCode reads the FHIR code token: system|code, with system LOINC or
// the Open Health Sync quantity type system (which sets q.Type). It returns
// the LOINC code to filter on, empty true when no Observation can match, or
// a message for a 400.
func parseFHIRCode(values []string, q *read.Query) (loinc string, empty bool, msg string) {
	switch {
	case len(values) == 0:
		return "", false, ""
	case len(values) > 1 || strings.Contains(values[0], ","):
		return "", false, "code takes a single system|code value"
	}
	system, code, ok := strings.Cut(values[0], "|")
	if !ok || system == "" || code == "" {
		return "", false, "code must be system|code, for example " + fhir.LOINC + "|8867-4"
	}
	switch system {
	case fhir.LOINC:
		// R4 allows these only as panel components: no Observation has them.
		return code, fhir.ComponentOnlyLOINC(code), ""
	case fhir.QuantityTypeSystem:
		if q.Type != "" && q.Type != code {
			return "", true, ""
		}
		q.Type = code
		return "", false, ""
	}
	return "", false, "code system not supported: use " + fhir.LOINC + " or " + fhir.QuantityTypeSystem
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
