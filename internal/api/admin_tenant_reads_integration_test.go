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
)

func newReadsEnv(t *testing.T) *env {
	t.Helper()
	box, err := crypto.NewBox(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	return newEnv(t, func(d *api.Deps) {
		d.Collect = collect.NewHandlers(collect.Deps{Pool: d.Pool, Box: box, Tenants: d.Store, Lookup: d.Providers.Lookup})
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
	r = e.do("POST", base+"/users", e.keyA, `{}`)
	assert.Equal(t, http.StatusMethodNotAllowed, r.code, "read only: %s", r.body)
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
