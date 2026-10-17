// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/deletion"
)

// An instance admin runs one tenant's API: its users and their data, their
// connections, its webhooks, its pipeline and its audit log. It serves a
// portal that runs a team's projects behind its own sign in, and support,
// without asking the team for its key. Each route is the tenant route of
// the same name served by the same handler: same parameters, same answer.
// It gives an admin no power it lacks, since an admin can already mint a key
// for any tenant; it saves minting one, and each request is logged on the
// tenant with the admin's name.

// adminTenantPrefix is where the tenant routes are served for an admin.
const adminTenantPrefix = "/v1/admin/tenants/{tenant_id}"

// mountAdminTenantRoutes serves the tenant routes with admin, the admin key
// middleware of mountAdmin. GET /v1/pipeline is not among them: the admin
// route of that path, mounted before, answers the same.
func (s *server) mountAdminTenantRoutes(admin func(string, http.HandlerFunc)) {
	type route struct {
		pattern string // method and the path after adminTenantPrefix
		h       http.Handler
	}
	deps := deletion.Deps{Pool: s.d.Pool, KnownProvider: knownProvider}
	routes := []route{
		{"GET /users", http.HandlerFunc(s.listUsers)},
		{"POST /users", s.idempotent(http.HandlerFunc(s.createUser))},
		{"GET /users/{id}", http.HandlerFunc(s.getUser)},
		{"DELETE /users/{id}", deletion.NewDeleteHandler(deps)},
		{"GET /deletions/{job_id}", http.HandlerFunc(s.getDeletion)},
		{"GET /users/{id}/daily", listRoute(s, read.Daily)},
		{"GET /users/{id}/samples", listRoute(s, read.Samples)},
		{"GET /users/{id}/samples/stats", http.HandlerFunc(s.sampleStats)},
		{"GET /users/{id}/sleep", listRoute(s, read.Sleep)},
		{"GET /users/{id}/workouts", listRoute(s, read.Workouts)},
		{"GET /users/{id}/nutrition", listRoute(s, read.Nutrition)},
		{"GET /users/{id}/coverage", http.HandlerFunc(s.coverage)},
		{"GET /pipeline/sync-runs", http.HandlerFunc(s.listTenantSyncRuns)},
		{"GET /pipeline/etl-jobs", http.HandlerFunc(s.listETLJobs)},
		{"POST /pipeline/etl-jobs/retry", http.HandlerFunc(s.retryParkedETLJobs)},
		{"POST /pipeline/etl-jobs/{job_id}/retry", http.HandlerFunc(s.retryETLJob)},
		{"GET /audit", http.HandlerFunc(s.listAudit)},
	}
	if h := s.d.Collect; h != nil {
		routes = append(routes,
			route{"GET /users/{id}/connections", h.ListConnections()},
			route{"GET /users/{id}/freshness", h.Freshness()},
			route{"DELETE /users/{id}/connections/{provider}", h.DeleteConnection()},
			route{"POST /users/{id}/connections/{provider}/sync", h.SyncNow()},
			route{"GET /users/{id}/sync-runs", h.ListSyncRuns()},
			route{"GET /users/{id}/sync-runs/{run}", h.GetSyncRun()},
			route{"POST /users/{id}/connect-sessions", h.CreateConnectSession()})
	}
	if s.d.Webhooks != nil {
		routes = append(routes,
			route{"POST /webhooks", http.HandlerFunc(s.createWebhook)},
			route{"GET /webhooks", http.HandlerFunc(s.listWebhooks)},
			route{"GET /webhooks/{webhook_id}", http.HandlerFunc(s.getWebhook)},
			route{"PATCH /webhooks/{webhook_id}", http.HandlerFunc(s.updateWebhook)},
			route{"DELETE /webhooks/{webhook_id}", http.HandlerFunc(s.deleteWebhook)},
			route{"POST /webhooks/{webhook_id}/rotate-secret", http.HandlerFunc(s.rotateWebhookSecret)},
			route{"POST /webhooks/{webhook_id}/test", http.HandlerFunc(s.testWebhook)},
			route{"GET /webhooks/{webhook_id}/deliveries", http.HandlerFunc(s.listWebhookDeliveries)},
			route{"GET /webhooks/{webhook_id}/deliveries/{delivery_id}", http.HandlerFunc(s.getWebhookDelivery)},
			route{"POST /webhooks/{webhook_id}/deliveries/{delivery_id}/retry", http.HandlerFunc(s.retryWebhookDelivery)},
			route{"POST /webhooks/{webhook_id}/deliveries/{delivery_id}/replay", http.HandlerFunc(s.replayWebhookDelivery)})
	}
	for _, rt := range routes {
		method, path, _ := strings.Cut(rt.pattern, " ")
		admin(method+" "+adminTenantPrefix+path, s.asTenant(rt.h))
	}
}

// asTenant serves h for the tenant of {tenant_id} (404 when there is none),
// as if that tenant's key had called: the handler sees a Principal of the
// tenant, named by the admin key's id, never an admin. A Location it sets
// points at the admin route of the same resource.
//
// The request is recorded twice, as an admin's change is. On the admin's
// tenant, like any request (instrument): the action of the tenant route, the
// tenant in the detail, never the user, who is not the admin's. On the
// tenant served: actor admin, named by its key, the user of {id}, the
// admin's tenant in the detail, so the tenant's log shows who read or
// changed what. When the admin serves its own tenant, the first event
// already says all of it, with the user: the second would count each
// request twice in one log, so it is not written.
func (s *server) asTenant(h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admin, _ := auth.From(r.Context())
		t, ok := s.pathTenant(w, r)
		if !ok {
			return
		}
		aw := &adminWriter{statusWriter: statusWriter{ResponseWriter: w}, prefix: "/v1/admin/tenants/" + t.ID.String()}
		h.ServeHTTP(aw, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{TenantID: t.ID, KeyID: admin.KeyID})))
		if t.ID == admin.TenantID {
			return
		}
		code := aw.code
		if code == 0 {
			code = http.StatusOK
		}
		var detail map[string]any
		for _, name := range auditPathValues {
			if v := r.PathValue(name); name != "tenant_id" && v != "" && len(v) <= 64 {
				if detail == nil {
					detail = map[string]any{}
				}
				detail[name] = v
			}
		}
		ev := adminEvent(r, admin, audit.Classify(r.Method, r.Pattern), code, detail)
		ev.Tenant = t.ID
		if u, err := uuid.Parse(r.PathValue("id")); err == nil {
			ev.User = &u
		}
		s.record(r, ev)
	}
}

// adminWriter turns a Location a tenant handler set ("/v1/webhooks/{id}")
// into the admin route of the same resource, under prefix.
type adminWriter struct {
	statusWriter
	prefix string
	fixed  bool
}

func (w *adminWriter) fix() {
	if w.fixed {
		return
	}
	w.fixed = true
	if l := w.Header().Get("Location"); strings.HasPrefix(l, "/v1/") {
		w.Header().Set("Location", w.prefix+strings.TrimPrefix(l, "/v1"))
	}
}

func (w *adminWriter) WriteHeader(code int) {
	w.fix()
	w.statusWriter.WriteHeader(code)
}

func (w *adminWriter) Write(b []byte) (int, error) {
	w.fix()
	return w.statusWriter.Write(b)
}
