// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/labs"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// labResultOut names one accepted result in the response.
type labResultOut struct {
	ID     string `json:"id"`
	Code   string `json:"code"`
	Status string `json:"status"`
}

// labReportOut names one accepted report in the response; ID is null for
// the results a FHIR upload sent without a DiagnosticReport.
type labReportOut struct {
	ID      *string        `json:"id"`
	Results []labResultOut `json:"results"`
}

type labsResponse struct {
	Reports []labReportOut `json:"reports"`
	// Duplicate is true when this exact body was already received.
	Duplicate bool `json:"duplicate"`
}

func (s *server) mountLabs(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/users/{id}/labs", s.listLabs)
	s.tenantRoute(mux, "GET /v1/users/{id}/labs/reports/{report_id}", s.labReport)
	if s.d.Labs == nil {
		return
	}
	// A tenant key uploads lab results for any of its users; a user token
	// with the upload scope, for its own user (the person's app).
	mux.Handle("POST /v1/users/{id}/labs",
		auth.MiddlewareAny(s.lookup, auth.TenantKey, auth.UserUpload)(s.idempotent(http.HandlerFunc(s.createLabs))))
}

// createLabs checks a lab upload (FHIR R4 or the simple form), stores the
// body as one upload part of the lab connector and answers 202: the ETL maps
// it like any upload, so consent, erasure and re-processing apply unchanged.
func (s *server) createLabs(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	if p.UserID != nil && *p.UserID != user {
		notFound(w, "user")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, labs.MaxBody))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		problem(w, http.StatusRequestEntityTooLarge, "the request body is too large")
		return
	case err != nil:
		badRequest(w, "the request body could not be read")
		return
	}
	reports, err := labs.Decode(body)
	if msg, ok := labs.IsInvalid(err); ok {
		badRequest(w, msg)
		return
	} else if err != nil {
		badRequest(w, "the request body is not a lab upload")
		return
	}
	if _, err := s.d.Store.GetActiveUser(r.Context(), p.TenantID, user); errors.Is(err, tenant.ErrNotFound) {
		notFound(w, "user")
		return
	} else if err != nil {
		internalError(w, r, "labs_user_failed", err)
		return
	}
	part, err := labPart(p.TenantID, user, body, reports)
	if err != nil {
		internalError(w, r, "labs_part_failed", err)
		return
	}
	resp, err := collect.StorePart(r.Context(), collect.UploadDeps{Pool: s.d.Pool, Raw: s.d.Raw, Device: s.d.Labs,
		Allowed: s.providerAllowed, Vault: s.d.Vault}, part)
	switch {
	case errors.Is(err, collect.ErrProviderOff):
		problem(w, http.StatusForbidden, collect.ProviderOffDetail)
		return
	case errors.Is(err, collect.ErrUserGone):
		notFound(w, "user")
		return
	case errors.Is(err, collect.ErrErasing):
		problem(w, http.StatusConflict,
			"this user's lab results are being deleted; nothing was kept, send them again once the deletion job is done")
		return
	case err != nil:
		internalError(w, r, "labs_store_failed", err)
		return
	}
	tenant.SetIdempotencyUser(r.Context(), user)
	out := labsResponse{Reports: make([]labReportOut, 0, len(reports)), Duplicate: resp.Duplicate}
	for _, rep := range reports {
		ro := labReportOut{Results: make([]labResultOut, 0, len(rep.Results))}
		if rep.ID != "" {
			id := rep.ID
			ro.ID = &id
		}
		for _, res := range rep.Results {
			ro.Results = append(ro.Results, labResultOut{ID: res.ID, Code: labs.CanonicalCode(res), Status: res.Status})
		}
		out.Reports = append(out.Reports, ro)
	}
	writeJSON(w, http.StatusAccepted, out)
}

// labPart is the upload part of a checked body: the body compacted to one
// NDJSON line, gzip compressed with no timestamp, under a batch derived from
// the line, so the same body sent again is the same part (a duplicate). The
// day is the earliest collection day in UTC.
func labPart(tenantID, user uuid.UUID, body []byte, reports []labs.Report) (collect.Part, error) {
	var line bytes.Buffer
	if err := json.Compact(&line, body); err != nil {
		return collect.Part{}, err
	}
	line.WriteByte('\n')
	var day time.Time
	for _, rep := range reports {
		for _, res := range rep.Results {
			if day.IsZero() || res.CollectedAt.Before(day) {
				day = *res.CollectedAt
			}
		}
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(line.Bytes()); err != nil {
		return collect.Part{}, err
	}
	if err := zw.Close(); err != nil {
		return collect.Part{}, err
	}
	sum := sha256.Sum256(line.Bytes())
	return collect.Part{
		Tenant: tenantID, User: user, Type: labs.DataType,
		Day:   time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC),
		Batch: "lab-" + hex.EncodeToString(sum[:20]), Part: 0, Body: gz.Bytes(),
	}, nil
}

// labParams are the query parameters GET /labs takes.
var labParams = []string{"code", "report", "start", "end", "limit", "cursor"}

// listLabs lists the user's lab results, filtered by code (a LOINC code or a
// source code), report id and collection time (start and end), in keyset
// pages.
func (s *server) listLabs(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	for name, v := range qs {
		if !slices.Contains(labParams, name) {
			badRequest(w, "query parameter "+name+" is not supported: use "+strings.Join(labParams, ", "))
			return
		}
		if len(v) > 1 {
			badRequest(w, name+" takes a single value")
			return
		}
	}
	q, err := parseReadQuery(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	f := read.LabFilter{Code: qs.Get("code"), Report: qs.Get("report")}
	pg, err := read.Labs(r.Context(), s.d.Pool, p.TenantID, user, q, f)
	if err != nil {
		readError(w, r, err, "user")
		return
	}
	writeJSON(w, http.StatusOK, newPage(pg.Records, pg.NextCursor))
}

// labReport reads one report of the user with its results.
func (s *server) labReport(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	rep, err := read.LabReportByID(r.Context(), s.d.Pool, p.TenantID, user, r.PathValue("report_id"))
	if err != nil {
		readError(w, r, err, "lab report")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
