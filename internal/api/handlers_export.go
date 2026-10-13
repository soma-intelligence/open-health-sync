// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/export"
)

// exportDatasets are the lists an export can hold, by the name in its route.
var exportDatasets = map[string]func(s *server, w http.ResponseWriter, r *http.Request, tenantID, user uuid.UUID, q read.Query, f export.Format){
	"samples":          exportRoute(read.AllSamples),
	"category-samples": exportRoute(read.CategorySamples),
	"sleep":            exportRoute(read.Sleep),
	"workouts":         exportRoute(read.Workouts),
	"nutrition":        exportRoute(read.Nutrition),
	"daily":            exportRoute(read.Daily),
}

func (s *server) mountExports(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/users/{id}/exports/{dataset}", s.exportFile)
}

// exportFile streams every record of one list of the user in [start, end)
// as one CSV or Parquet file, reading the list page by page.
func (s *server) exportFile(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	dataset := r.PathValue("dataset")
	write, known := exportDatasets[dataset]
	if !known {
		names := make([]string, 0, len(exportDatasets))
		for n := range exportDatasets {
			names = append(names, n)
		}
		slices.Sort(names)
		problem(w, http.StatusNotFound, "no export named "+dataset+": use one of "+strings.Join(names, ", "))
		return
	}
	qs := r.URL.Query()
	f, okFormat := export.ParseFormat(qs.Get("format"))
	if !qs.Has("format") {
		f, okFormat = export.CSV, true
	}
	if !okFormat {
		badRequest(w, "format must be csv or parquet")
		return
	}
	if qs.Has("cursor") || qs.Has("limit") {
		badRequest(w, "an export holds every record in [start, end) and takes no cursor or limit")
		return
	}
	q, err := parseReadQuery(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	s.mergePolicy(r, p.TenantID, user, &q)
	write(s, w, r, p.TenantID, user, q, f)
}

func exportRoute[T any](fn listFunc[T]) func(s *server, w http.ResponseWriter, r *http.Request, tenantID, user uuid.UUID, q read.Query, f export.Format) {
	return func(s *server, w http.ResponseWriter, r *http.Request, tenantID, user uuid.UUID, q read.Query, f export.Format) {
		q.Limit = read.MaxLimit
		// The first page is read before anything is written, so a bad
		// query or an unknown user is still a problem response.
		pg, err := fn(r.Context(), s.d.Pool, tenantID, user, q)
		if err != nil {
			readError(w, r, err, "user")
			return
		}
		w.Header().Set("Content-Type", f.ContentType())
		w.Header().Set("Content-Disposition", `attachment; filename="`+r.PathValue("dataset")+"-"+user.String()+"."+string(f)+`"`)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		out, err := export.NewWriter[T](w, f)
		if err == nil {
			for err = out.Write(pg.Records); err == nil && pg.NextCursor != ""; {
				q.Cursor = pg.NextCursor
				if pg, err = fn(r.Context(), s.d.Pool, tenantID, user, q); err == nil {
					err = out.Write(pg.Records)
				}
			}
		}
		if err == nil {
			err = out.Close()
		}
		if err != nil {
			// The status is sent: cut the response so the client sees a
			// broken transfer, never a file that looks complete.
			if r.Context().Err() == nil { // not the client going away
				slog.ErrorContext(r.Context(), "api: export", "error", err, "route", r.Pattern)
			}
			panic(http.ErrAbortHandler)
		}
	}
}
