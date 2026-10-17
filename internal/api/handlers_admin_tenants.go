// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/demo"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// The tenants of the instance, for a key of an instance admin tenant: what
// `ohs admin tenant` and `ohs admin key` do from a shell, and the demo user of
// `ohs seed --demo`, so an operator's own tooling can run an instance for
// several teams. A change is recorded twice in the audit log: on the admin's
// tenant like any request, and on the tenant it changed, by the code that
// changes it.

const (
	// maxAdminTenantName is the longest tenant name, in characters.
	maxAdminTenantName = 100
	// defaultDemoDays and maxDemoDays bound the demo user route: 90 days are
	// about 44,000 rows, written in a few seconds within the request.
	defaultDemoDays = 30
	maxDemoDays     = 90
)

// adminTenantJSON is a tenant as an instance admin sees it.
type adminTenantJSON struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Admin     bool      `json:"admin"`
	CreatedAt time.Time `json:"created_at"`
	// ActiveKeys counts the tenant's keys that are not revoked.
	ActiveKeys int `json:"active_keys"`
}

func toAdminTenantJSON(t tenant.TenantSummary) adminTenantJSON {
	return adminTenantJSON{ID: t.ID, Name: t.Name, Admin: t.Admin, CreatedAt: t.CreatedAt.UTC(), ActiveKeys: t.ActiveKeys}
}

// createdTenantJSON is a new tenant with its first key, whose secret is in
// this answer only.
type createdTenantJSON struct {
	adminTenantJSON
	Key createdKeyJSON `json:"key"`
}

type createTenantRequest struct {
	Name string `json:"name"`
}

type demoUserRequest struct {
	ExternalRef *string `json:"external_ref"`
	Days        *int    `json:"days"`
	Persona     *string `json:"persona"`
}

type demoUserJSON struct {
	User userJSON `json:"user"`
	Days int      `json:"days"`
	// RowsWritten counts the rows this call stored; the days already there
	// add nothing.
	RowsWritten int `json:"rows_written"`
	// Persona is the kind of person written (demo.Personas).
	Persona string `json:"persona"`
}

// mountAdminTenants serves the tenant routes with admin, the admin key
// middleware of mountAdmin.
func (s *server) mountAdminTenants(admin func(string, http.HandlerFunc)) {
	admin("GET /v1/admin/tenants", s.listAdminTenants)
	// The two creations are not wrapped in the idempotency middleware, like
	// POST /v1/keys and token minting: a replay would mean storing the
	// plaintext key to send it again.
	admin("POST /v1/admin/tenants", s.createAdminTenant)
	admin("GET /v1/admin/tenants/{tenant_id}", s.getAdminTenant)
	admin("GET /v1/admin/tenants/{tenant_id}/pipeline", s.getAdminTenantPipeline)
	admin("GET /v1/admin/tenants/{tenant_id}/keys", s.listAdminTenantKeys)
	admin("POST /v1/admin/tenants/{tenant_id}/keys", s.createAdminTenantKey)
	admin("DELETE /v1/admin/tenants/{tenant_id}/keys/{key_id}", s.revokeAdminTenantKey)
	admin("POST /v1/admin/tenants/{tenant_id}/demo-user", s.createDemoUser)
	s.mountAdminTenantProviders(admin)
	s.mountAdminTenantReads(admin)
}

// pathTenantID parses {tenant_id}. A malformed id is a tenant that does not
// exist.
func pathTenantID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("tenant_id"))
	if err != nil {
		notFound(w, "tenant")
		return uuid.Nil, false
	}
	return id, true
}

// pathTenant is pathTenantID for a route whose tenant must exist.
func (s *server) pathTenant(w http.ResponseWriter, r *http.Request) (tenant.TenantSummary, bool) {
	id, ok := pathTenantID(w, r)
	if !ok {
		return tenant.TenantSummary{}, false
	}
	t, err := s.d.Store.GetTenantSummary(r.Context(), id)
	switch {
	case errors.Is(err, tenant.ErrNotFound):
		notFound(w, "tenant")
		return tenant.TenantSummary{}, false
	case err != nil:
		internalError(w, r, "api: get tenant", err)
		return tenant.TenantSummary{}, false
	}
	return t, true
}

// adminEvent is the event recorded on the tenant an admin key changed: actor
// admin, named by its key, the admin's tenant in the detail. The store adds
// the target tenant and, for a key, its id and prefix.
func adminEvent(r *http.Request, p auth.Principal, action string, status int, detail map[string]any) audit.Event {
	d := map[string]any{"admin_tenant_id": p.TenantID.String()}
	maps.Copy(d, detail)
	return audit.Event{ActorType: audit.Admin, ActorID: "api_key:" + p.KeyID, Action: action,
		Method: r.Method, Route: r.Pattern, Status: status, RequestID: reqid.From(r.Context()), Detail: d}
}

// checkAdminTenantName returns why a trimmed tenant name is refused, or "".
func checkAdminTenantName(name string) string {
	switch {
	case name == "":
		return "name is required"
	case utf8.RuneCountInString(name) > maxAdminTenantName:
		return "name is at most 100 characters"
	case strings.ContainsFunc(name, unicode.IsControl):
		return "name must not contain control characters"
	}
	return ""
}

func (s *server) listAdminTenants(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	ts, next, err := s.d.Store.ListTenantsPage(r.Context(), r.URL.Query().Get("cursor"), limit)
	switch {
	case errors.Is(err, tenant.ErrInvalidCursor):
		badRequest(w, "the cursor is not valid")
		return
	case err != nil:
		internalError(w, r, "api: list tenants", err)
		return
	}
	out := make([]adminTenantJSON, 0, len(ts))
	for _, t := range ts {
		out = append(out, toAdminTenantJSON(t))
	}
	writeJSON(w, http.StatusOK, newPage(out, next))
}

// createAdminTenant creates a tenant and its first key. The body takes a
// name only, so the route never makes an instance admin: that stays
// `ohs admin tenant`, run on the server.
func (s *server) createAdminTenant(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	var req createTenantRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if detail := checkAdminTenantName(name); detail != "" {
		badRequest(w, detail)
		return
	}
	t, full, k, err := s.d.Store.CreateTenantAudited(r.Context(), name,
		adminEvent(r, p, audit.TenantCreate, http.StatusCreated, nil))
	if err != nil {
		internalError(w, r, "api: create tenant", err)
		return
	}
	// The secret is in this answer only: nothing on the way may keep it.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/v1/admin/tenants/"+t.ID.String())
	writeJSON(w, http.StatusCreated, createdTenantJSON{
		adminTenantJSON: toAdminTenantJSON(tenant.TenantSummary{Tenant: t, ActiveKeys: 1}),
		Key:             createdKeyJSON{apiKeyJSON: toAPIKeyJSON(k, p.KeyID), Key: full},
	})
}

func (s *server) getAdminTenant(w http.ResponseWriter, r *http.Request) {
	t, ok := s.pathTenant(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toAdminTenantJSON(t))
}

func (s *server) getAdminTenantPipeline(w http.ResponseWriter, r *http.Request) {
	t, ok := s.pathTenant(w, r)
	if !ok {
		return
	}
	out, err := s.pipeline(r.Context(), t.ID)
	if err != nil {
		internalError(w, r, "api: pipeline", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) listAdminTenantKeys(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	id, ok := pathTenantID(w, r)
	if !ok {
		return
	}
	keys, err := s.d.Store.ListKeys(r.Context(), id)
	switch {
	case errors.Is(err, tenant.ErrNotFound):
		notFound(w, "tenant")
		return
	case err != nil:
		internalError(w, r, "api: list keys", err)
		return
	}
	out := make([]apiKeyJSON, 0, len(keys))
	for _, k := range keys {
		out = append(out, toAPIKeyJSON(k, p.KeyID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *server) createAdminTenantKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	id, ok := pathTenantID(w, r)
	if !ok {
		return
	}
	var req struct{}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	full, k, err := s.d.Store.CreateKeyAudited(r.Context(), id, adminEvent(r, p, audit.KeyCreate, http.StatusCreated, nil))
	switch {
	case errors.Is(err, tenant.ErrNotFound):
		notFound(w, "tenant")
		return
	case err != nil:
		internalError(w, r, "api: create key", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, createdKeyJSON{apiKeyJSON: toAPIKeyJSON(k, p.KeyID), Key: full})
}

func (s *server) revokeAdminTenantKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	tenantID, ok := pathTenantID(w, r)
	if !ok {
		return
	}
	keyID, err := uuid.Parse(r.PathValue("key_id"))
	if err != nil {
		notFound(w, "API key")
		return
	}
	if tenantID == p.TenantID && keyID.String() == p.KeyID {
		problem(w, http.StatusConflict, "this is the key making the call: revoke it with another key of the tenant")
		return
	}
	_, err = s.d.Store.RevokeKeyAudited(r.Context(), tenantID, keyID, adminEvent(r, p, audit.KeyRevoke, http.StatusNoContent, nil))
	switch {
	case errors.Is(err, tenant.ErrNotFound):
		notFound(w, "API key")
		return
	case err != nil:
		internalError(w, r, "api: revoke key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createDemoUser writes synthetic data for one user of the tenant, as
// `ohs seed --demo --tenant` does, and answers 201 when it created the user.
// The write and its audit event run to the end once started, even if the
// caller goes away: data is never left written without its event.
func (s *server) createDemoUser(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	t, ok := s.pathTenant(w, r)
	if !ok {
		return
	}
	var req demoUserRequest
	if !decodeJSON(w, r, &req, true) {
		return
	}
	persona, _ := demo.LookupPersona(demo.PersonaMix)
	if req.Persona != nil {
		var ok bool
		if persona, ok = demo.LookupPersona(*req.Persona); !ok || *req.Persona == "" {
			badRequest(w, "persona is one of "+demo.PersonaNames())
			return
		}
	}
	ref, days := demo.DefaultExternalRef(persona.Name), defaultDemoDays
	if req.ExternalRef != nil {
		if ref = *req.ExternalRef; ref == "" || len(ref) > maxExternalRef {
			badRequest(w, "external_ref is 1 to 255 characters")
			return
		}
	}
	if req.Days != nil {
		if days = *req.Days; days < 1 || days > maxDemoDays {
			badRequest(w, "days must be between 1 and 90")
			return
		}
	}
	ctx := context.WithoutCancel(r.Context())
	res, err := demo.Seed(ctx, s.d.Store, t.ID, demo.SeedOptions{Days: days, Seed: persona.Seed, Persona: persona.Name,
		ExternalRef: ref})
	switch {
	case errors.Is(err, tenant.ErrNotFound):
		notFound(w, "tenant")
		return
	case errors.Is(err, tenant.ErrConflict), errors.Is(err, etl.ErrUserNotActive):
		problem(w, http.StatusConflict, "the user with this external_ref is being deleted: pass another external_ref")
		return
	case err != nil:
		internalError(w, r, "api: demo user", err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	ev := adminEvent(r, p, audit.DataWrite, status, map[string]any{
		"synthetic": true, "days": days, "persona": persona.Name, "rows_written": res.Written, "user_created": res.Created})
	ev.Tenant, ev.User = t.ID, &res.User.ID
	actx, cancel := context.WithTimeout(ctx, auditTimeout)
	defer cancel()
	if err := audit.Record(actx, s.d.Pool, ev); err != nil {
		internalError(w, r, "api: demo user", err)
		return
	}
	writeJSON(w, status, demoUserJSON{User: toUserJSON(res.User), Days: days, RowsWritten: res.Written, Persona: persona.Name})
}
