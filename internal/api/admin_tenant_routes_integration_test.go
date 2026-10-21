// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

func newReadsEnv(t *testing.T) *env {
	t.Helper()
	box, err := crypto.NewBox(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	return newEnv(t, func(d *api.Deps) {
		d.Collect = collect.NewHandlers(collect.Deps{Pool: d.Pool, Box: box, Tenants: d.Store, Lookup: d.Providers.Lookup})
		d.Webhooks = webhook.NewStore(d.Pool, box)
	})
}

// An admin key reads a tenant's users and data through the tenant routes'
// handlers: the answer its own key gets, and nothing of another tenant.
func TestAdminTenantReads(t *testing.T) {
	e := newReadsEnv(t)
	var a tenantInfo
	e.do("GET", "/v1/tenant", e.keyA, nil).json(t, &a)
	blue := e.newTenant("Team Blue")
	var demo demoUser
	r := e.do("POST", "/v1/admin/tenants/"+blue.ID.String()+"/demo-user", e.keyA, `{"days":3}`)
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	r.json(t, &demo)
	uid := demo.User.ID.String()
	base := "/v1/admin/tenants/" + blue.ID.String()
	week := time.Now().UTC().AddDate(0, 0, -7).Format(time.DateOnly)
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format(time.DateOnly)

	paths := []string{
		"/users", "/users?limit=1", "/users/" + uid, "/users/" + uid + "/daily", "/users/" + uid + "/daily?limit=2",
		"/users/" + uid + "/samples?type=heart_rate&limit=5",
		"/users/" + uid + "/samples/stats?type=step_count&interval=day&tz=Europe/Paris&start=" + week + "&end=" + tomorrow,
		"/users/" + uid + "/sleep", "/users/" + uid + "/workouts", "/users/" + uid + "/nutrition",
		"/users/" + uid + "/coverage", "/users/" + uid + "/coverage?provider=oura", "/users/" + uid + "/connections",
	}
	for _, p := range paths {
		own := e.do("GET", "/v1"+p, blue.Key.Key, nil)
		require.Equal(t, http.StatusOK, own.code, "%s: %s", p, own.body)
		admin := e.do("GET", base+p, e.keyA, nil)
		require.Equal(t, http.StatusOK, admin.code, "%s: %s", p, admin.body)
		assert.JSONEq(t, string(own.body), string(admin.body), p)
	}
	var daily page[map[string]any]
	e.do("GET", base+"/users/"+uid+"/daily", e.keyA, nil).json(t, &daily)
	assert.NotEmpty(t, daily.Data)
	var cov struct{ Days []map[string]any }
	e.do("GET", base+"/users/"+uid+"/coverage", e.keyA, nil).json(t, &cov)
	assert.NotEmpty(t, cov.Days)
	var fresh struct {
		Data []struct{ Provider, Kind, State string }
	}
	r = e.do("GET", base+"/users/"+uid+"/freshness", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &fresh)
	var ownFresh struct {
		Data []struct{ Provider, Kind, State string }
	}
	e.do("GET", "/v1/users/"+uid+"/freshness", blue.Key.Key, nil).json(t, &ownFresh)
	assert.Equal(t, ownFresh.Data, fresh.Data)

	// The same refusals as the tenant routes.
	assert.Equal(t, http.StatusBadRequest, e.do("GET", base+"/users/"+uid+"/samples/stats?type=step_count&tz=Mars&start="+week+"&end="+tomorrow, e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/users/"+uuid.NewString()+"/daily", e.keyA, nil).code)
	ua := e.user(e.tenantA, "a-1")
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/users/"+ua.String(), e.keyA, nil).code,
		"a user of another tenant, the admin's own included")
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/users/"+ua.String()+"/daily", e.keyA, nil).code)

	// Only an admin key, for a tenant that exists.
	for _, key := range []string{blue.Key.Key, e.keyB} {
		r = e.do("GET", base+"/users/"+uid+"/daily", key, nil)
		assert.Equal(t, http.StatusForbidden, r.code, string(r.body))
	}
	for _, id := range []string{uuid.NewString(), "nope"} {
		r = e.do("GET", "/v1/admin/tenants/"+id+"/users/"+uid+"/daily", e.keyA, nil)
		assert.Equal(t, http.StatusNotFound, r.code, id)
		assert.Contains(t, string(r.body), "tenant not found")
	}
	r = e.do("DELETE", base+"/users/"+uid+"/daily", e.keyA, nil)
	assert.Equal(t, http.StatusMethodNotAllowed, r.code, "the methods of the tenant route: %s", r.body)
}

// Each read is recorded on the tenant read, naming the admin and the user,
// and on the admin's tenant without the user. Reading its own tenant is
// recorded once.
func TestAdminTenantReadsAudit(t *testing.T) {
	e := newReadsEnv(t)
	var a tenantInfo
	e.do("GET", "/v1/tenant", e.keyA, nil).json(t, &a)
	blue := e.newTenant("Team Blue")
	var demo demoUser
	e.do("POST", "/v1/admin/tenants/"+blue.ID.String()+"/demo-user", e.keyA, `{"days":1}`).json(t, &demo)
	base := "/v1/admin/tenants/" + blue.ID.String() + "/users"
	uid := demo.User.ID.String()

	require.Equal(t, http.StatusOK, e.do("GET", base+"/"+uid+"/daily", e.keyA, nil, "X-Request-Id", "req-read-1").code)
	require.Equal(t, http.StatusOK, e.do("GET", base+"/"+uid+"/coverage", e.keyA, nil).code)
	require.Equal(t, http.StatusOK, e.do("GET", base, e.keyA, nil).code)
	require.Equal(t, http.StatusOK, e.do("GET", base+"/"+uid+"/connections", e.keyA, nil).code)
	missing := uuid.NewString()
	require.Equal(t, http.StatusNotFound, e.do("GET", base+"/"+missing+"/daily", e.keyA, nil).code)
	require.Equal(t, http.StatusNotFound, e.do("GET", "/v1/admin/tenants/"+uuid.NewString()+"/users", e.keyA, nil).code)

	// The tenant's log, read with its own key.
	var target auditPage
	e.do("GET", "/v1/audit?limit=100", blue.Key.Key, nil).json(t, &target)
	adminActor := audit.Actor{Type: audit.Admin, ID: "api_key:" + a.Key.ID.String()}
	var reads []audit.Entry
	for _, ev := range target.Data {
		if ev.Actor == adminActor && strings.HasPrefix(ev.Route, "GET /v1/admin/tenants/{tenant_id}/users") {
			reads = append(reads, ev)
		}
	}
	require.Len(t, reads, 5, "one per read, the refused one included: %+v", target.Data)
	byRequest := map[string]audit.Entry{}
	for _, ev := range reads {
		byRequest[ev.RequestID] = ev
		assert.Equal(t, map[string]any{"admin_tenant_id": e.tenantA.String()}, ev.Detail)
	}
	first := byRequest["req-read-1"]
	assert.Equal(t, audit.DataRead, first.Action)
	assert.Equal(t, "GET /v1/admin/tenants/{tenant_id}/users/{id}/daily", first.Route)
	assert.Equal(t, http.StatusOK, *first.Status)
	require.NotNil(t, first.UserID)
	assert.Equal(t, demo.User.ID, *first.UserID)
	actions := map[string]string{}
	for _, ev := range reads {
		actions[strings.TrimPrefix(ev.Route, "GET /v1/admin/tenants/{tenant_id}")+" "+statusOf(ev)] = ev.Action
	}
	assert.Equal(t, map[string]string{
		"/users/{id}/daily 200":       audit.DataRead,
		"/users/{id}/coverage 200":    audit.DataRead,
		"/users 200":                  audit.UserRead,
		"/users/{id}/connections 200": audit.ConnectionRead,
		"/users/{id}/daily 404":       audit.DataRead,
	}, actions)
	var listed audit.Entry
	for _, ev := range reads {
		if ev.Route == "GET /v1/admin/tenants/{tenant_id}/users" {
			listed = ev
		}
	}
	assert.Nil(t, listed.UserID, "a list of users concerns no one user")

	// The tenant can filter its log for what the admin read about a user.
	var aboutUser auditPage
	e.do("GET", "/v1/audit?action=data.read&user_id="+uid, blue.Key.Key, nil).json(t, &aboutUser)
	assert.Len(t, aboutUser.Data, 2)

	// The admin's own log: one event per request, the tenant in the detail,
	// never the other tenant's user.
	var own auditPage
	e.do("GET", "/v1/audit?limit=100", e.keyA, nil).json(t, &own)
	n := 0
	for _, ev := range own.Data {
		if !strings.HasPrefix(ev.Route, "GET /v1/admin/tenants/{tenant_id}/users") {
			continue
		}
		n++
		assert.Equal(t, audit.Actor{Type: audit.APIKey, ID: a.Key.ID.String()}, ev.Actor)
		assert.Nil(t, ev.UserID, "%+v", ev)
		assert.NotEmpty(t, ev.Detail["tenant_id"])
		if ev.RequestID == "req-read-1" {
			assert.Equal(t, audit.DataRead, ev.Action)
			assert.Equal(t, map[string]any{"tenant_id": blue.ID.String()}, ev.Detail)
		}
	}
	assert.Equal(t, 6, n, "the unknown tenant's request too")

	// Its own tenant through the admin route: one event, with the user.
	ua := e.user(e.tenantA, "a-1")
	r := e.do("GET", "/v1/admin/tenants/"+e.tenantA.String()+"/users/"+ua.String()+"/daily", e.keyA, nil,
		"X-Request-Id", "req-own-1")
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	own = auditPage{}
	e.do("GET", "/v1/audit?limit=100&user_id="+ua.String(), e.keyA, nil).json(t, &own)
	var mine []audit.Entry
	for _, ev := range own.Data {
		if ev.RequestID == "req-own-1" {
			mine = append(mine, ev)
		}
	}
	require.Len(t, mine, 1, "%+v", own.Data)
	assert.Equal(t, audit.APIKey, mine[0].Actor.Type)
	assert.Equal(t, audit.DataRead, mine[0].Action)
	require.NotNil(t, mine[0].UserID)
	assert.Equal(t, ua, *mine[0].UserID)
}

func statusOf(ev audit.Entry) string {
	if ev.Status == nil {
		return "-"
	}
	return strconv.Itoa(*ev.Status)
}

// An admin key changes a tenant through the tenant routes' handlers: a user
// created, a webhook managed, the pipeline retried, the user deleted. Each
// lands on that tenant, as with its own key, and its log names the admin.
func TestAdminTenantWrites(t *testing.T) {
	e := newReadsEnv(t)
	var a tenantInfo
	e.do("GET", "/v1/tenant", e.keyA, nil).json(t, &a)
	blue := e.newTenant("Team Blue")
	base := "/v1/admin/tenants/" + blue.ID.String()

	// A user, created for the tenant: Location is the admin route.
	r := e.do("POST", base+"/users", e.keyA, `{"external_ref":"ada"}`, "X-Request-Id", "req-write-1")
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var u userJSON
	r.json(t, &u)
	uid := u.ID.String()
	assert.Equal(t, base+"/users/"+uid, r.header.Get("Location"))
	assert.Equal(t, http.StatusOK, e.do("GET", "/v1/users/"+uid, blue.Key.Key, nil).code, "the tenant's user")
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/users/"+uid, e.keyA, nil).code, "not the admin's")
	assert.Equal(t, http.StatusConflict, e.do("POST", base+"/users", e.keyA, `{"external_ref":"ada"}`).code)

	// A webhook, managed end to end.
	r = e.do("POST", base+"/webhooks", e.keyA, map[string]any{"url": "https://app.example.com/hooks"})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var hook webhookJSON
	r.json(t, &hook)
	assert.NotEmpty(t, hook.Secret)
	hookPath := "/webhooks/" + hook.ID.String()
	assert.Equal(t, base+hookPath, r.header.Get("Location"))
	var own page[webhookJSON]
	e.do("GET", "/v1/webhooks", blue.Key.Key, nil).json(t, &own)
	require.Len(t, own.Data, 1)
	assert.Equal(t, hook.ID, own.Data[0].ID)
	r = e.do("PATCH", base+hookPath, e.keyA, map[string]any{"description": "portal"})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, http.StatusOK, e.do("POST", base+hookPath+"/rotate-secret", e.keyA, nil).code)
	r = e.do("POST", base+hookPath+"/test", e.keyA, nil)
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	var sent deliveryJSON
	r.json(t, &sent)
	replay := hookPath + "/deliveries/" + strconv.FormatInt(sent.ID, 10) + "/replay"
	r = e.do("POST", base+replay, e.keyA, nil)
	assert.Equal(t, http.StatusConflict, r.code, "not sent yet, as for the tenant: %s", r.body)
	assert.Equal(t, r.code, e.do("POST", "/v1"+replay, blue.Key.Key, nil).code)
	for _, p := range []string{"/webhooks", hookPath, hookPath + "/deliveries"} {
		mine := e.do("GET", "/v1"+p, blue.Key.Key, nil)
		admin := e.do("GET", base+p, e.keyA, nil)
		require.Equal(t, http.StatusOK, admin.code, "%s: %s", p, admin.body)
		assert.JSONEq(t, string(mine.body), string(admin.body), p)
	}

	// The pipeline and the log: the tenant's own answers.
	for _, c := range []struct{ method, path string }{
		{"GET", "/pipeline/sync-runs"}, {"GET", "/pipeline/etl-jobs"}, {"POST", "/pipeline/etl-jobs/retry"},
		{"POST", "/pipeline/etl-jobs/" + uuid.NewString() + "/retry"}, {"GET", "/users/" + uid + "/sync-runs"},
	} {
		mine := e.do(c.method, "/v1"+c.path, blue.Key.Key, `{}`)
		admin := e.do(c.method, base+c.path, e.keyA, `{}`)
		assert.Equal(t, mine.code, admin.code, "%s %s: %s", c.method, c.path, admin.body)
	}
	r = e.do("GET", base+"/audit?limit=100", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var log auditPage
	r.json(t, &log)
	assert.NotEmpty(t, log.Data)

	// Disconnecting a provider the user never connected: the tenant's answer.
	mine := e.do("DELETE", "/v1/users/"+uid+"/connections/oura", blue.Key.Key, nil)
	admin := e.do("DELETE", base+"/users/"+uid+"/connections/oura", e.keyA, nil)
	assert.Equal(t, mine.code, admin.code, string(admin.body))

	assert.Equal(t, http.StatusNoContent, e.do("DELETE", base+hookPath, e.keyA, nil).code)
	own = page[webhookJSON]{}
	e.do("GET", "/v1/webhooks", blue.Key.Key, nil).json(t, &own)
	assert.Empty(t, own.Data)

	// The user, deleted: the job is read back through the admin route too.
	r = e.do("DELETE", base+"/users/"+uid, e.keyA, nil, "X-Request-Id", "req-delete-1")
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	var job struct {
		JobID  uuid.UUID `json:"job_id"`
		Status string    `json:"status"`
	}
	r.json(t, &job)
	r = e.do("GET", base+"/deletions/"+job.JobID.String(), e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.JSONEq(t, string(e.do("GET", "/v1/deletions/"+job.JobID.String(), blue.Key.Key, nil).body), string(r.body))
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/deletions/"+job.JobID.String(), e.keyA, nil).code)

	// Only an admin key, for a tenant that exists.
	for _, key := range []string{blue.Key.Key, e.keyB} {
		assert.Equal(t, http.StatusForbidden, e.do("POST", base+"/users", key, `{"external_ref":"x"}`).code)
		assert.Equal(t, http.StatusForbidden, e.do("POST", base+"/webhooks", key, map[string]any{"url": "https://app.example.com/x"}).code)
	}
	r = e.do("POST", "/v1/admin/tenants/"+uuid.NewString()+"/users", e.keyA, `{"external_ref":"x"}`)
	assert.Equal(t, http.StatusNotFound, r.code)
	assert.Contains(t, string(r.body), "tenant not found")

	// The tenant's log names the admin for each change, with the user.
	var target auditPage
	e.do("GET", "/v1/audit?limit=100", blue.Key.Key, nil).json(t, &target)
	adminActor := audit.Actor{Type: audit.Admin, ID: "api_key:" + a.Key.ID.String()}
	byRequest := map[string]audit.Entry{}
	actions := map[string]bool{}
	for _, ev := range target.Data {
		if ev.Actor != adminActor {
			continue
		}
		byRequest[ev.RequestID] = ev
		actions[ev.Action] = true
		assert.Equal(t, e.tenantA.String(), ev.Detail["admin_tenant_id"], "%+v", ev)
	}
	created := byRequest["req-write-1"]
	assert.Equal(t, audit.UserCreate, created.Action)
	assert.Equal(t, "POST /v1/admin/tenants/{tenant_id}/users", created.Route)
	deleted := byRequest["req-delete-1"]
	assert.Equal(t, audit.UserDelete, deleted.Action)
	require.NotNil(t, deleted.UserID)
	assert.Equal(t, u.ID, *deleted.UserID)
	for _, want := range []string{audit.UserCreate, audit.UserDelete, audit.WebhookWrite, audit.WebhookRead,
		audit.AuditRead, audit.ConnectionDelete} {
		assert.True(t, actions[want], "%s in %v", want, actions)
	}

	// The admin's own log: the changes, never the other tenant's user.
	var mineLog auditPage
	e.do("GET", "/v1/audit?limit=100", e.keyA, nil).json(t, &mineLog)
	for _, ev := range mineLog.Data {
		if strings.HasPrefix(ev.Route, "DELETE /v1/admin/tenants/{tenant_id}/users/{id}") {
			assert.Nil(t, ev.UserID, "%+v", ev)
			assert.Equal(t, blue.ID.String(), ev.Detail["tenant_id"])
		}
	}
}
