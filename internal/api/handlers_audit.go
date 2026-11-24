// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/logging"
	"github.com/soma-intelligence/open-health-sync/internal/logutil"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
)

// auditTimeout bounds the write of one audit event.
const auditTimeout = 2 * time.Second

// auditPathValues are the path values an audit event keeps in its detail:
// identifiers, never a value read or written.
var auditPathValues = []string{"tenant_id", "provider", "key_id", "job_id", "token_id"}

type auditNotesKey struct{}

// withAuditNotes gives the request an empty notes map: instrument calls it
// once, a handler fills it with auditNote, audit merges it into the event.
func withAuditNotes(ctx context.Context) context.Context {
	return context.WithValue(ctx, auditNotesKey{}, map[string]any{})
}

// auditNote adds key to the detail of the request's own audit event. Names
// and counts only, never a value read or written. Without a notes map in the
// context (a handler run outside instrument) it does nothing.
func auditNote(r *http.Request, key string, value any) {
	if m, ok := r.Context().Value(auditNotesKey{}).(map[string]any); ok {
		m[key] = value
	}
}

func auditNotesOf(r *http.Request) map[string]any {
	m, _ := r.Context().Value(auditNotesKey{}).(map[string]any)
	return m
}

func (s *server) mountAudit(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/audit", s.listAudit)
}

// audit records one authenticated request: the credential, the action its
// route stands for, the user it concerns and its status. A user token names
// its own user; a tenant key route names the user of its {id}, unless its
// {tenant_id} names another tenant (an admin route): that user is not one of
// the caller's, and is named on that tenant's log instead (asTenant). A write
// that fails is logged and counted, never turned into an error for the
// caller, who already has the response. A console session request carries
// the tenant of the session as ActorTenant; a session route is recorded on
// that tenant, and the request event of a session route whose handler
// recorded its own event is skipped when it succeeded.
func (s *server) audit(r *http.Request, p auth.Principal, code int, missingScope string) {
	// The handler of a session route writes its own event when it succeeds;
	// a request refused before that (a scope, a bad body, an unknown tenant)
	// is recorded here.
	if sessionOwnEvent[r.Pattern] && code < http.StatusBadRequest {
		return
	}
	// An instance admin route is the admin's own business: it is recorded on
	// the tenant that acts, never on a tenant the console session is viewing.
	// So is a session route.
	tenantID := p.TenantID
	if isAdminRoute(r.Pattern) || isSessionRoute(r.Pattern) {
		tenantID = p.Actor()
	}
	ev := audit.Event{
		Tenant:    tenantID,
		ActorType: audit.APIKey,
		ActorID:   p.KeyID,
		Action:    audit.Classify(r.Method, r.Pattern),
		Method:    r.Method,
		Route:     r.Pattern,
		Status:    code,
		RequestID: reqid.From(r.Context()),
	}
	if p.SessionID != uuid.Nil {
		at := p.ActorTenantID
		ev.ActorTenant = &at
	}
	if p.UserID != nil {
		ev.ActorType = audit.UserToken
		ev.User = p.UserID
	} else if u, err := uuid.Parse(r.PathValue("id")); err == nil && ownTenant(r, tenantID) {
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
	// What the handler chose to say about the request (auditNote).
	for k, v := range auditNotesOf(r) {
		if ev.Detail == nil {
			ev.Detail = map[string]any{}
		}
		ev.Detail[k] = v
	}
	// A request refused for a key scope (403) says which one.
	if missingScope != "" {
		if ev.Detail == nil {
			ev.Detail = map[string]any{}
		}
		ev.Detail["missing_scope"] = missingScope
	}
	s.record(r, ev)
}

// record writes ev once the response is decided. An event of a high volume
// action (audit.Buffered) goes to the buffered writer when there is one; any
// other event, every event without a writer, and an event the writer does
// not take (its queue is full, or the process is stopping) is written before
// the request ends, so a full queue slows requests down instead of losing
// events. A write that fails is logged and counted.
func (s *server) record(r *http.Request, ev audit.Event) {
	if s.sink != nil {
		s.sink(ev)
		return
	}
	if s.d.Audit != nil && audit.Buffered(ev.Action) {
		if err := s.d.Audit.Enqueue(ev); err == nil {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
	defer cancel()
	if err := audit.Record(ctx, s.d.Pool, ev); err != nil {
		s.metrics.auditFailures.Add(1)
		logutil.Err(*logging.Ctx(r.Context()), err, zerolog.ErrorLevel).Str("route", r.Pattern).Msg("audit_event_not_written")
	}
}

// ownTenant reports whether the request is about the caller's own tenant:
// true unless the route's {tenant_id} names another one.
func ownTenant(r *http.Request, tenantID uuid.UUID) bool {
	v := r.PathValue("tenant_id")
	if v == "" {
		return true
	}
	id, err := uuid.Parse(v)
	return err == nil && id == tenantID
}

// isAdminRoute reports whether a route pattern is an instance admin route.
func isAdminRoute(pattern string) bool {
	_, path, _ := strings.Cut(pattern, " ")
	return strings.HasPrefix(path, "/v1/admin/")
}

// isSessionRoute reports whether pattern is a console session route.
func isSessionRoute(pattern string) bool {
	_, path, _ := strings.Cut(pattern, " ")
	return strings.HasPrefix(path, "/console/session")
}

// listAudit serves the tenant's audit log, newest first, filtered by
// user_id, action (exact, or a prefix ending in "."), actor_type with
// actor_id, since and until.
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
	if v := qs.Get("actor_type"); v != "" {
		t := audit.ActorType(v)
		switch t {
		case audit.APIKey, audit.UserToken, audit.Admin, audit.System, audit.ShareLink:
			f.ActorType = t
		default:
			badRequest(w, "actor_type must be one of api_key, user_token, admin, system, share_link")
			return
		}
	}
	f.ActorID = qs.Get("actor_id")
	if f.ActorID != "" && f.ActorType == "" {
		badRequest(w, "actor_id needs actor_type")
		return
	}
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
		internalError(w, r, "list_audit_events_failed", err)
		return
	}
	cursor := ""
	if next > 0 {
		cursor = strconv.FormatInt(next, 10)
	}
	writeJSON(w, http.StatusOK, newPage(entries, cursor))
}
