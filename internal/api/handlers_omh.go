// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/omh"
)

func (s *server) mountOMH(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/users/{id}/omh/data-points", s.omhDataPoints)
}

// omhDataPoints serves the user's records as Open mHealth data points, in
// the order and with the cursor of the FHIR search. A record without an Open
// mHealth schema is left out, so a page can hold fewer than limit data
// points and still have a next cursor.
func (s *server) omhDataPoints(w http.ResponseWriter, r *http.Request) {
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
	if qs.Has("type") {
		badRequest(w, "type is not a parameter of this list: use schema, for example schema=omh:heart-rate")
		return
	}
	var only *omh.Schema
	if v := qs["schema"]; len(v) > 0 {
		sc, found := omh.SchemaByName(v[0])
		if len(v) > 1 || !found {
			names := make([]string, 0, len(omh.Schemas()))
			for _, sc := range omh.Schemas() {
				names = append(names, sc.Name)
			}
			badRequest(w, "schema must be one of "+strings.Join(names, ", ")+" (bare or as omh:name)")
			return
		}
		only = &sc
	}
	if err := read.CheckObservationQuery(q); err != nil {
		readError(w, r, err, "user")
		return
	}
	s.mergePolicy(r, p.TenantID, user, &q)
	pg, err := read.Observations(r.Context(), s.d.Pool, p.TenantID, user, q, omh.Filter(only))
	if err != nil {
		readError(w, r, err, "user")
		return
	}
	out := make([]omh.DataPoint, 0, len(pg.Records))
	for _, rec := range pg.Records {
		if d, ok := omh.FromRecord(user, rec); ok {
			out = append(out, d)
		}
	}
	writeJSON(w, http.StatusOK, newPage(out, pg.NextCursor))
}
