// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
)

func (s *server) mountReads(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/users/{id}/samples", listRoute(s, read.Samples))
	s.tenantRoute(mux, "GET /v1/users/{id}/category-samples", listRoute(s, read.CategorySamples))
	s.tenantRoute(mux, "GET /v1/users/{id}/sleep", listRoute(s, read.Sleep))
	s.tenantRoute(mux, "GET /v1/users/{id}/sleep/{session_id}/phases", childRoute(s, "session_id", "sleep session", read.SleepPhases))
	s.tenantRoute(mux, "GET /v1/users/{id}/workouts", listRoute(s, read.Workouts))
	s.tenantRoute(mux, "GET /v1/users/{id}/workouts/{workout_id}/route", childRoute(s, "workout_id", "workout", read.WorkoutRoute))
	s.tenantRoute(mux, "GET /v1/users/{id}/nutrition", listRoute(s, read.Nutrition))
	s.tenantRoute(mux, "GET /v1/users/{id}/daily", listRoute(s, read.Daily))
	s.tenantRoute(mux, "GET /v1/users/{id}/rr-intervals", rrRoute(s, read.RRIntervals))
	s.tenantRoute(mux, "GET /v1/users/{id}/hrv", rrRoute(s, read.HRV))
	s.tenantRoute(mux, "GET /v1/users/{id}/menstrual-cycles", listRoute(s, read.MenstrualCycles))
	s.tenantRoute(mux, "GET /v1/users/{id}/coverage", s.coverage)
}

type listFunc[T any] func(ctx context.Context, db read.Querier, tenantID, userID uuid.UUID, q read.Query) (read.Page[T], error)

type childFunc[T any] func(ctx context.Context, db read.Querier, tenantID, userID uuid.UUID, parent int64, q read.Query) (read.Page[T], error)

func listRoute[T any](s *server, fn listFunc[T]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, user, ok := pathUser(w, r)
		if !ok {
			return
		}
		q, err := parseReadQuery(r)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		s.mergePolicy(r, p.TenantID, user, &q)
		pg, err := fn(r.Context(), s.d.Pool, p.TenantID, user, q)
		if err != nil {
			readError(w, r, err, "user")
			return
		}
		writeJSON(w, http.StatusOK, newPage(pg.Records, pg.NextCursor))
	}
}

func childRoute[T any](s *server, param, what string, fn childFunc[T]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, user, ok := pathUser(w, r)
		if !ok {
			return
		}
		parent, err := strconv.ParseInt(r.PathValue(param), 10, 64)
		if err != nil {
			notFound(w, what)
			return
		}
		q, err := parseReadQuery(r)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		pg, err := fn(r.Context(), s.d.Pool, p.TenantID, user, parent, q)
		if err != nil {
			readError(w, r, err, what)
			return
		}
		writeJSON(w, http.StatusOK, newPage(pg.Records, pg.NextCursor))
	}
}

// rrRoute serves a heartbeat list: series_id, start, end, cursor and limit.
func rrRoute[T any](s *server, fn func(context.Context, read.Querier, uuid.UUID, uuid.UUID, read.RRQuery) (read.Page[T], error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, user, ok := pathUser(w, r)
		if !ok {
			return
		}
		rq, err := parseReadQuery(r)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		q := read.RRQuery{SeriesID: r.URL.Query().Get("series_id"), Start: rq.Start, End: rq.End,
			Cursor: rq.Cursor, Limit: rq.Limit}
		pg, err := fn(r.Context(), s.d.Pool, p.TenantID, user, q)
		if err != nil {
			readError(w, r, err, "user")
			return
		}
		writeJSON(w, http.StatusOK, newPage(pg.Records, pg.NextCursor))
	}
}

func readError(w http.ResponseWriter, r *http.Request, err error, what string) {
	var qe *read.QueryError
	switch {
	case errors.As(err, &qe):
		badRequest(w, qe.Msg)
	case errors.Is(err, read.ErrInvalidCursor):
		badRequest(w, "the cursor is not valid")
	case errors.Is(err, read.ErrNotFound):
		notFound(w, what)
	default:
		internalError(w, r, "api: read", err)
	}
}

func (s *server) coverage(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	q := read.CoverageQuery{Provider: qs.Get("provider")}
	var err error
	if v := qs.Get("start"); v != "" {
		if q.Start, err = parseTime(v); err != nil {
			badRequest(w, "start: "+err.Error())
			return
		}
	}
	if v := qs.Get("end"); v != "" {
		if q.End, err = parseTime(v); err != nil {
			badRequest(w, "end: "+err.Error())
			return
		}
	}
	c, err := read.UserCoverage(r.Context(), s.d.Pool, p.TenantID, user, q)
	if err != nil {
		readError(w, r, err, "user")
		return
	}
	writeJSON(w, http.StatusOK, c)
}
