// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
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
	"samples":          exportRoute(read.EachSample),
	"category-samples": exportRoute(pages(read.CategorySamples)),
	"sleep":            exportRoute(pages(read.Sleep)),
	"workouts":         exportRoute(pages(read.Workouts)),
	"nutrition":        exportRoute(pages(read.Nutrition)),
	"daily":            exportRoute(pages(read.Daily)),
	"ecg":              exportRoute(pages(read.ECGRecordings)),
}

// eachFunc calls fn with every record of a list in [start, end), batch by
// batch.
type eachFunc[T any] func(ctx context.Context, db read.Querier, tenantID, userID uuid.UUID, q read.Query, fn func([]T) error) error

// pages reads a list page by page at the largest page size.
func pages[T any](list listFunc[T]) eachFunc[T] {
	return func(ctx context.Context, db read.Querier, tenantID, userID uuid.UUID, q read.Query, fn func([]T) error) error {
		q.Limit = read.MaxLimit
		for {
			pg, err := list(ctx, db, tenantID, userID, q)
			if err != nil {
				return err
			}
			if err := fn(pg.Records); err != nil || pg.NextCursor == "" {
				return err
			}
			q.Cursor = pg.NextCursor
		}
	}
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

func exportRoute[T any](each eachFunc[T]) func(s *server, w http.ResponseWriter, r *http.Request, tenantID, user uuid.UUID, q read.Query, f export.Format) {
	return func(s *server, w http.ResponseWriter, r *http.Request, tenantID, user uuid.UUID, q read.Query, f export.Format) {
		// The first batch is read before anything is written, so a bad
		// query or an unknown user is still a problem response.
		var out *export.Writer[T]
		begin := func() error {
			w.Header().Set("Content-Type", f.ContentType())
			w.Header().Set("Content-Disposition", `attachment; filename="`+r.PathValue("dataset")+"-"+user.String()+"."+string(f)+`"`)
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			var err error
			out, err = export.NewWriter[T](w, f)
			return err
		}
		started := false
		err := each(r.Context(), s.d.Pool, tenantID, user, q, func(recs []T) error {
			if !started {
				started = true
				if err := begin(); err != nil {
					return err
				}
			}
			return out.Write(recs)
		})
		if !started {
			if err != nil {
				readError(w, r, err, "user")
				return
			}
			started = true
			err = begin()
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
