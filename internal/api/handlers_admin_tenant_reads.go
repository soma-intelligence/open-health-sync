// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
)

// An instance admin reads one tenant's users and their data, for support
// and for a portal that shows a team what it collected, without asking the
// team for its key. Each route is the tenant route of the same name served
// by the same handler: same parameters, same answer, read only.

// adminTenantReadPrefix is where the tenant routes are served for an admin.
const adminTenantReadPrefix = "/v1/admin/tenants/{tenant_id}"

// mountAdminTenantReads serves the read routes with admin, the admin key
// middleware of mountAdmin.
func (s *server) mountAdminTenantReads(admin func(string, http.HandlerFunc)) {
	type route struct {
		path string
		h    http.Handler
	}
	routes := []route{
		{"/users", http.HandlerFunc(s.listUsers)},
		{"/users/{id}", http.HandlerFunc(s.getUser)},
		{"/users/{id}/daily", listRoute(s, read.Daily)},
		{"/users/{id}/samples", listRoute(s, read.Samples)},
		{"/users/{id}/samples/stats", http.HandlerFunc(s.sampleStats)},
		{"/users/{id}/sleep", listRoute(s, read.Sleep)},
		{"/users/{id}/workouts", listRoute(s, read.Workouts)},
		{"/users/{id}/nutrition", listRoute(s, read.Nutrition)},
		{"/users/{id}/coverage", http.HandlerFunc(s.coverage)},
	}
	if h := s.d.Collect; h != nil {
		routes = append(routes, route{"/users/{id}/connections", h.ListConnections()},
			route{"/users/{id}/freshness", h.Freshness()})
	}
	for _, rt := range routes {
		admin("GET "+adminTenantReadPrefix+rt.path, s.asTenant(rt.h))
	}
}

// asTenant serves h for the tenant of {tenant_id} (404 when there is none),
// as if that tenant's key had called: the handler sees a Principal of the
// tenant, named by the admin key's id, never an admin.
//
// The request is recorded twice, as an admin's change is. On the admin's
// tenant, like any request (instrument): the action of the tenant route, the
// tenant in the detail, never the user, who is not the admin's. On the
// tenant read: actor admin, named by its key, the user of {id}, the admin's
// tenant in the detail, so the tenant's log shows who read its data. When
// the admin reads its own tenant, the first event already says all of it,
// with the user: the second would count each read twice in one log, so it
// is not written.
func (s *server) asTenant(h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admin, _ := auth.From(r.Context())
		t, ok := s.pathTenant(w, r)
		if !ok {
			return
		}
		sw := &statusWriter{ResponseWriter: w}
		h.ServeHTTP(sw, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{TenantID: t.ID, KeyID: admin.KeyID})))
		if t.ID == admin.TenantID {
			return
		}
		code := sw.code
		if code == 0 {
			code = http.StatusOK
		}
		ev := adminEvent(r, admin, audit.Classify(r.Method, r.Pattern), code, nil)
		ev.Tenant = t.ID
		if u, err := uuid.Parse(r.PathValue("id")); err == nil {
			ev.User = &u
		}
		s.record(r, ev)
	}
}
