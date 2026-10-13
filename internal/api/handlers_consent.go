// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/consent"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// maxEvidence bounds the evidence a consent change carries (a consent form
// version, the screen it was given on).
const maxEvidence = 500

func (s *server) mountConsent(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/users/{id}/consents", s.listConsents)
	s.tenantRoute(mux, "GET /v1/users/{id}/consents/history", s.consentHistory)
	s.tenantRoute(mux, "PUT /v1/users/{id}/consents/{provider}", s.putConsent)
	s.tenantRoute(mux, "DELETE /v1/users/{id}/consents/{provider}", s.deleteConsent)
}

type consentList struct {
	Data       []consent.Source `json:"data"`
	Categories []string         `json:"categories"`
}

// listConsents serves every source the user has recorded consent for, and
// the categories there are.
func (s *server) listConsents(w http.ResponseWriter, r *http.Request) {
	p, user, ok := s.activeUser(w, r)
	if !ok {
		return
	}
	srcs, err := consent.List(r.Context(), s.d.Pool, p.TenantID, user)
	if err != nil {
		internalError(w, r, "api: list consents", err)
		return
	}
	if srcs == nil {
		srcs = []consent.Source{}
	}
	writeJSON(w, http.StatusOK, consentList{Data: srcs, Categories: consent.Categories})
}

func (s *server) consentHistory(w http.ResponseWriter, r *http.Request) {
	p, user, ok := s.activeUser(w, r)
	if !ok {
		return
	}
	limit, err := parseLimit(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	var before int64
	if v := r.URL.Query().Get("cursor"); v != "" {
		if before, err = strconv.ParseInt(v, 10, 64); err != nil || before < 1 {
			badRequest(w, "cursor is not one this endpoint returned")
			return
		}
	}
	events, next, err := consent.History(r.Context(), s.d.Pool, p.TenantID, user, before, limit)
	if err != nil {
		internalError(w, r, "api: consent history", err)
		return
	}
	cursor := ""
	if next > 0 {
		cursor = strconv.FormatInt(next, 10)
	}
	writeJSON(w, http.StatusOK, newPage(events, cursor))
}

type putConsentRequest struct {
	Granted  []string `json:"granted"`
	Evidence string   `json:"evidence"`
}

// putConsent makes the request's granted list the user's whole consent for
// the source: a category left out is revoked.
func (s *server) putConsent(w http.ResponseWriter, r *http.Request) {
	var req putConsentRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.Granted == nil {
		badRequest(w, "granted is required: the categories the user allows from this source ([] for none)")
		return
	}
	s.setConsent(w, r, req)
}

// deleteConsent revokes every category of the source.
func (s *server) deleteConsent(w http.ResponseWriter, r *http.Request) {
	s.setConsent(w, r, putConsentRequest{Granted: []string{}, Evidence: r.URL.Query().Get("evidence")})
}

func (s *server) setConsent(w http.ResponseWriter, r *http.Request, req putConsentRequest) {
	provider := r.PathValue("provider")
	if _, known := connector.Get(provider); !known {
		notFound(w, "provider")
		return
	}
	for _, c := range req.Granted {
		if !consent.Known(c) {
			badRequest(w, "unknown category "+strconv.Quote(c)+": use "+strings.Join(consent.Categories, ", "))
			return
		}
	}
	if utf8.RuneCountInString(req.Evidence) > maxEvidence {
		badRequest(w, "evidence is at most 500 characters")
		return
	}
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		internalError(w, r, "api: set consent", err)
		return
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// The share lock keeps a deletion from starting until the change commits.
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR SHARE`,
		p.TenantID, user).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "active") {
		notFound(w, "user")
		return
	}
	if err != nil {
		internalError(w, r, "api: set consent", err)
		return
	}
	changes, err := consent.Set(ctx, tx, p.TenantID, user, provider, req.Granted, p.KeyID, req.Evidence)
	if err != nil {
		internalError(w, r, "api: set consent", err)
		return
	}
	var granted, revoked []string
	for _, c := range changes {
		if c.Granted {
			granted = append(granted, c.Category)
		} else {
			revoked = append(revoked, c.Category)
		}
	}
	for typ, cats := range map[string][]string{webhook.ConsentGranted: granted, webhook.ConsentRevoked: revoked} {
		if len(cats) == 0 {
			continue
		}
		if _, err := webhook.Enqueue(ctx, tx, webhook.Event{Tenant: p.TenantID, User: &user, Type: typ,
			Data: map[string]any{"provider": provider, "categories": cats}}); err != nil {
			internalError(w, r, "api: set consent", err)
			return
		}
	}
	srcs, err := consent.List(ctx, tx, p.TenantID, user)
	if err != nil {
		internalError(w, r, "api: set consent", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		internalError(w, r, "api: set consent", err)
		return
	}
	i := slices.IndexFunc(srcs, func(src consent.Source) bool { return src.Provider == provider })
	writeJSON(w, http.StatusOK, srcs[i])
}

// activeUser is pathUser that also answers 404 for a user who is gone or
// being deleted.
func (s *server) activeUser(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, bool) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return p, user, false
	}
	if _, err := s.d.Store.GetActiveUser(r.Context(), p.TenantID, user); err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			notFound(w, "user")
		} else {
			internalError(w, r, "api: look up user", err)
		}
		return p, user, false
	}
	return p, user, true
}
