// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
)

// auditTimeout bounds the write of one audit event.
const auditTimeout = 2 * time.Second

// auditPathValues are the path values an audit event keeps in its detail:
// identifiers, never a value read or written.
var auditPathValues = []string{"tenant_id", "provider", "key_id", "job_id"}

func (s *server) mountAudit(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/audit", s.listAudit)
}

// audit records one authenticated request: the credential, the action its
// route stands for, the user it concerns and its status. A user token names
// its own user; a tenant key route names the user of its {id}, unless its
// {tenant_id} names another tenant (an admin route): that user is not one of
// the caller's, and is named on that tenant's log instead (asTenant). A write
// that fails is logged and counted, never turned into an error for the
// caller, who already has the response.
func (s *server) audit(r *http.Request, p auth.Principal, code int) {
	ev := audit.Event{
		Tenant:    p.TenantID,
		ActorType: audit.APIKey,
		ActorID:   p.KeyID,
		Action:    audit.Classify(r.Method, r.Pattern),
		Method:    r.Method,
		Route:     r.Pattern,
		Status:    code,
		RequestID: reqid.From(r.Context()),
	}
	if p.UserID != nil {
		ev.ActorType = audit.UserToken
		ev.User = p.UserID
	} else if u, err := uuid.Parse(r.PathValue("id")); err == nil && ownTenant(r, p) {
		ev.User = &u
	}
	// The route pattern hides which provider, key or job a request named.
	for _, name := range auditPathValues {
		if v := r.PathValue(name); v != "" && len(v) <= 64 {
			if ev.Detail == nil {
				ev.Detail = map[string]any{}
			}
			ev.Detail[name] = v
		}
	}
	s.record(r, ev)
}

// record writes ev once the response is decided. A write that fails is
// logged and counted.
func (s *server) record(r *http.Request, ev audit.Event) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
	defer cancel()
	if err := audit.Record(ctx, s.d.Pool, ev); err != nil {
		s.metrics.auditFailures.Add(1)
		slog.ErrorContext(r.Context(), "api: audit event not written", "error", err, "route", r.Pattern)
	}
}

// ownTenant reports whether the request is about the caller's own tenant:
// true unless the route's {tenant_id} names another one.
func ownTenant(r *http.Request, p auth.Principal) bool {
	v := r.PathValue("tenant_id")
	if v == "" {
		return true
	}
	id, err := uuid.Parse(v)
	return err == nil && id == p.TenantID
}

// listAudit serves the tenant's audit log, newest first, filtered by
// user_id, action (exact, or a prefix ending in "."), since and until.
func (s *server) listAudit(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	qs := r.URL.Query()
	var f audit.Filter
	var err error
	if f.Limit, err = parseLimit(r); err != nil {
		badRequest(w, err.Error())
		return
	}
	if v := qs.Get("user_id"); v != "" {
		u, err := uuid.Parse(v)
		if err != nil {
			badRequest(w, "user_id must be a user id")
			return
		}
		f.User = &u
	}
	f.Action = qs.Get("action")
	for name, dst := range map[string]*time.Time{"since": &f.Since, "until": &f.Until} {
		if v := qs.Get(name); v != "" {
			if *dst, err = parseTime(v); err != nil {
				badRequest(w, name+": "+err.Error())
				return
			}
		}
	}
	if v := qs.Get("cursor"); v != "" {
		if f.Before, err = strconv.ParseInt(v, 10, 64); err != nil || f.Before < 1 {
			badRequest(w, "cursor is not one this endpoint returned")
			return
		}
	}
	entries, next, err := audit.List(r.Context(), s.d.Pool, p.TenantID, f)
	if err != nil {
		internalError(w, r, "list audit events", err)
		return
	}
	cursor := ""
	if next > 0 {
		cursor = strconv.FormatInt(next, 10)
	}
	writeJSON(w, http.StatusOK, newPage(entries, cursor))
}
