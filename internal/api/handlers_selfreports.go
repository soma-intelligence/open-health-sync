// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/selfreport"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// maxSelfReports is the most reports one request may carry.
const maxSelfReports = 100

type selfReportIn struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Value    *float64 `json:"value"`
	StartAt  string   `json:"start_at"`
	EndAt    *string  `json:"end_at"`
	Timezone *string  `json:"timezone"`
}

type selfReportsRequest struct {
	Reports []selfReportIn `json:"reports"`
}

// selfReportOut is a stored report, as the response names it.
type selfReportOut struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	Type     string     `json:"type"`
	Value    float64    `json:"value"`
	StartAt  time.Time  `json:"start_at"`
	EndAt    *time.Time `json:"end_at"`
	Timezone *string    `json:"timezone"`
}

type selfReportsResponse struct {
	Reports []selfReportOut `json:"reports"`
	// Duplicate is true when these exact reports were already received.
	Duplicate bool `json:"duplicate"`
}

func (s *server) mountSelfReports(mux *http.ServeMux) {
	if s.d.SelfReport == nil {
		return
	}
	// A tenant key records reports for any of its users; a user token with
	// the upload scope, for its own user (the person's app).
	mux.Handle("POST /v1/users/{id}/self-reports",
		auth.MiddlewareAny(s.lookup, auth.TenantKey, auth.UserUpload)(s.idempotent(http.HandlerFunc(s.createSelfReports))))
}

// createSelfReports checks the reports, stores them as one upload part of the
// self report connector and answers 202: the ETL writes them like any upload,
// so consent, the merged view, erasure and re-processing apply unchanged.
func (s *server) createSelfReports(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	if p.UserID != nil && *p.UserID != user {
		notFound(w, "user")
		return
	}
	var req selfReportsRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	reports, msg := checkSelfReports(req.Reports)
	if msg != "" {
		badRequest(w, msg)
		return
	}
	if _, err := s.d.Store.GetActiveUser(r.Context(), p.TenantID, user); errors.Is(err, tenant.ErrNotFound) {
		notFound(w, "user")
		return
	} else if err != nil {
		internalError(w, r, "api: self reports: user", err)
		return
	}

	part, err := selfReportPart(p.TenantID, user, reports)
	if err != nil {
		internalError(w, r, "api: self reports: part", err)
		return
	}
	resp, err := collect.StorePart(r.Context(), collect.UploadDeps{Pool: s.d.Pool, Raw: s.d.Raw, Device: s.d.SelfReport,
		Allowed: s.providerAllowed}, part)
	switch {
	case errors.Is(err, collect.ErrProviderOff):
		problem(w, http.StatusForbidden, collect.ProviderOffDetail)
		return
	case errors.Is(err, collect.ErrUserGone):
		notFound(w, "user")
		return
	case errors.Is(err, collect.ErrErasing):
		problem(w, http.StatusConflict,
			"this user's self reports are being deleted; nothing was kept, send them again once the deletion job is done")
		return
	case err != nil:
		internalError(w, r, "api: self reports: store", err)
		return
	}
	tenant.SetIdempotencyUser(r.Context(), user)
	out := selfReportsResponse{Reports: make([]selfReportOut, 0, len(reports)), Duplicate: resp.Duplicate}
	for _, rep := range reports {
		qt, _ := selfreport.QuantityType(rep.Kind)
		o := selfReportOut{ID: rep.ID, Kind: rep.Kind, Type: qt, Value: rep.Value, StartAt: rep.StartAt, EndAt: rep.EndAt}
		if rep.Timezone != "" {
			o.Timezone = &rep.Timezone
		}
		out.Reports = append(out.Reports, o)
	}
	writeJSON(w, http.StatusAccepted, out)
}

// checkSelfReports turns the request's reports into checked reports, giving
// an id to those without one. The message names the report and the field,
// never a value.
func checkSelfReports(in []selfReportIn) ([]selfreport.Report, string) {
	switch {
	case len(in) == 0:
		return nil, "reports must hold at least one report"
	case len(in) > maxSelfReports:
		return nil, "reports holds at most " + strconv.Itoa(maxSelfReports) + " reports"
	}
	out := make([]selfreport.Report, 0, len(in))
	seen := make(map[string]bool, len(in))
	for i, rr := range in {
		at := "reports[" + strconv.Itoa(i) + "]."
		rep := selfreport.Report{ID: rr.ID, Kind: rr.Kind}
		if rep.ID == "" {
			id, err := uuid.NewV7()
			if err != nil {
				return nil, "an id could not be generated, retry"
			}
			rep.ID = id.String()
		}
		if rr.Value == nil {
			return nil, at + "value is required"
		}
		rep.Value = *rr.Value
		if rr.StartAt == "" {
			return nil, at + "start_at is required"
		}
		var err error
		if rep.StartAt, err = time.Parse(time.RFC3339Nano, rr.StartAt); err != nil {
			return nil, at + "start_at must be an RFC 3339 time"
		}
		if rr.EndAt != nil {
			end, err := time.Parse(time.RFC3339Nano, *rr.EndAt)
			if err != nil {
				return nil, at + "end_at must be an RFC 3339 time"
			}
			rep.EndAt = &end
		}
		if rr.Timezone != nil {
			rep.Timezone = *rr.Timezone
		}
		var ie *selfreport.InvalidError
		if err := rep.Check(); errors.As(err, &ie) {
			return nil, at + ie.Msg
		} else if err != nil {
			return nil, at + "is not valid"
		}
		if seen[rep.ID] {
			return nil, at + "id is used by another report of this request"
		}
		seen[rep.ID] = true
		out = append(out, rep)
	}
	return out, ""
}

// selfReportPart is the upload part of checked reports: one NDJSON line per
// report, gzip compressed with no timestamp, under a batch derived from the
// lines, so the same reports sent again are the same part (a duplicate). The
// day is the earliest report's UTC day.
func selfReportPart(tenantID, user uuid.UUID, reports []selfreport.Report) (collect.Part, error) {
	var ndjson bytes.Buffer
	day := reports[0].StartAt
	for _, rep := range reports {
		line, err := selfreport.Line(rep)
		if err != nil {
			return collect.Part{}, err
		}
		ndjson.Write(line)
		if rep.StartAt.Before(day) {
			day = rep.StartAt
		}
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(ndjson.Bytes()); err != nil {
		return collect.Part{}, err
	}
	if err := zw.Close(); err != nil {
		return collect.Part{}, err
	}
	sum := sha256.Sum256(ndjson.Bytes())
	return collect.Part{
		Tenant: tenantID, User: user, Type: selfreport.DataType,
		Day:   time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC),
		Batch: "sr-" + hex.EncodeToString(sum[:20]), Part: 0, Body: gz.Bytes(),
	}, nil
}
