// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
)

type adminTenant struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	Admin      bool      `json:"admin"`
	CreatedAt  time.Time `json:"created_at"`
	ActiveKeys int       `json:"active_keys"`
}

type createdTenant struct {
	adminTenant
	Key keyInfo `json:"key"`
}

// newTenant creates a tenant with the admin key A and returns it with its
// first key.
func (e *env) newTenant(name string) createdTenant {
	e.t.Helper()
	r := e.do("POST", "/v1/admin/tenants", e.keyA, map[string]any{"name": name})
	require.Equal(e.t, http.StatusCreated, r.code, string(r.body))
	var out createdTenant
	r.json(e.t, &out)
	return out
}

// Only a key of an instance admin tenant reaches the tenant routes: another
// tenant's key and a user token get the same 403 as /v1/admin/providers.
func TestAdminTenantsNeedAnAdminKey(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantB, "b-1")
	tok, _, err := e.store.MintUserToken(context.Background(), e.tenantB, u, []string{"read", "upload"}, time.Hour)
	require.NoError(t, err)
	b := e.tenantB.String()
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/v1/admin/tenants", ""},
		{"POST", "/v1/admin/tenants", `{"name":"x"}`},
		{"GET", "/v1/admin/tenants/" + b, ""},
		{"GET", "/v1/admin/tenants/" + b + "/pipeline", ""},
		{"GET", "/v1/admin/tenants/" + b + "/keys", ""},
		{"POST", "/v1/admin/tenants/" + b + "/keys", ""},
		{"DELETE", "/v1/admin/tenants/" + b + "/keys/" + uuid.NewString(), ""},
		{"POST", "/v1/admin/tenants/" + b + "/demo-user", `{"days":1}`},
		{"GET", "/v1/admin/tenants/" + b + "/providers", ""},
		{"PATCH", "/v1/admin/tenants/" + b + "/providers/apple_health", `{"enabled":false}`},
		{"GET", "/v1/admin/tenants/" + b + "/users", ""},
		{"GET", "/v1/admin/tenants/" + b + "/users/" + u.String(), ""},
		{"GET", "/v1/admin/tenants/" + b + "/users/" + u.String() + "/daily", ""},
		{"GET", "/v1/admin/tenants/" + b + "/users/" + u.String() + "/coverage", ""},
	} {
		var body any
		if c.body != "" {
			body = c.body
		}
		r := e.do(c.method, c.path, e.keyB, body)
		assert.Equal(t, http.StatusForbidden, r.code, "%s %s: %s", c.method, c.path, r.body)
		assert.Contains(t, string(r.body), "instance admin tenant")
		assert.Equal(t, http.StatusForbidden, e.do(c.method, c.path, tok, body).code, "%s %s with a user token", c.method, c.path)
		assert.Equal(t, http.StatusUnauthorized, e.do(c.method, c.path, "", body).code, "%s %s without a key", c.method, c.path)
	}
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_core.tenants`).Scan(&n))
	assert.Equal(t, 2, n, "nothing created")
}

func TestAdminTenantsCreateListGet(t *testing.T) {
	e := newEnv(t)

	r := e.do("POST", "/v1/admin/tenants", e.keyA, `{"name":"  Team Blue  "}`)
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	var blue createdTenant
	r.json(t, &blue)
	assert.Equal(t, "/v1/admin/tenants/"+blue.ID.String(), r.header.Get("Location"))
	assert.Equal(t, "Team Blue", blue.Name, "trimmed")
	assert.False(t, blue.Admin)
	assert.Equal(t, 1, blue.ActiveKeys)
	assert.WithinDuration(t, time.Now(), blue.CreatedAt, time.Minute)
	require.True(t, strings.HasPrefix(blue.Key.Key, "ohs_sk_"), blue.Key.Key)
	assert.True(t, strings.HasPrefix(blue.Key.Key, blue.Key.Prefix))
	assert.Len(t, blue.Key.Prefix, 12)
	assert.NotEqual(t, uuid.Nil, blue.Key.ID)
	assert.False(t, blue.Key.Current)
	assert.Nil(t, blue.Key.RevokedAt)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(r.body, &raw))
	assert.ElementsMatch(t, []string{"id", "name", "admin", "created_at", "active_keys", "key"}, keysOfAny(r.body))
	assert.ElementsMatch(t, []string{"id", "prefix", "created_at", "last_used_at", "revoked_at", "current", "key"},
		keysOfAny(raw["key"]), "the key object of POST /v1/keys")

	// The new key works, for its own tenant only.
	var own tenantInfo
	r = e.do("GET", "/v1/tenant", blue.Key.Key, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &own)
	assert.Equal(t, blue.ID, own.ID)
	assert.False(t, own.Admin)
	assert.Equal(t, blue.Key.ID, own.Key.ID)
	assert.Equal(t, http.StatusForbidden, e.do("GET", "/v1/admin/tenants", blue.Key.Key, nil).code, "not an admin key")
	assert.Equal(t, http.StatusForbidden, e.do("GET", "/v1/admin/providers", blue.Key.Key, nil).code)
	ua := e.user(e.tenantA, "a-1")
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/users/"+ua.String(), blue.Key.Key, nil).code, "A's user")
	var users page[userJSON]
	e.do("GET", "/v1/users", blue.Key.Key, nil).json(t, &users)
	assert.Empty(t, users.Data)

	// One tenant.
	var got adminTenant
	r = e.do("GET", "/v1/admin/tenants/"+blue.ID.String(), e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &got)
	assert.Equal(t, blue.adminTenant, got)
	assert.ElementsMatch(t, []string{"id", "name", "admin", "created_at", "active_keys"}, keysOfAny(r.body))
	for _, p := range []string{uuid.NewString(), "not-a-tenant"} {
		r = e.do("GET", "/v1/admin/tenants/"+p, e.keyA, nil)
		assert.Equal(t, http.StatusNotFound, r.code, p)
		assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"))
	}

	// Names: trimmed, 1 to 100 characters, no control character; nothing else
	// in the body, so no way to ask for an admin tenant.
	for _, body := range []string{
		``, `{}`, `{"name":""}`, `{"name":"   "}`, `{"name":"` + strings.Repeat("x", 101) + `"}`,
		`{"name":"a\u0007b"}`, `{"name":"a\nb"}`, `{"name":"x","admin":true}`, `{"name":1}`, `[`,
	} {
		r = e.do("POST", "/v1/admin/tenants", e.keyA, body)
		assert.Equal(t, http.StatusBadRequest, r.code, "%s: %s", body, r.body)
	}
	long := e.newTenant(strings.Repeat("é", 100))
	assert.Equal(t, strings.Repeat("é", 100), long.Name, "100 characters, 200 bytes")
	assert.False(t, long.Admin)

	// The list: every tenant, oldest first, paged like the users.
	var list page[adminTenant]
	r = e.do("GET", "/v1/admin/tenants", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &list)
	require.Len(t, list.Data, 4)
	assert.Nil(t, list.NextCursor)
	assert.Equal(t, []uuid.UUID{e.tenantA, e.tenantB, blue.ID, long.ID},
		[]uuid.UUID{list.Data[0].ID, list.Data[1].ID, list.Data[2].ID, list.Data[3].ID})
	assert.True(t, list.Data[0].Admin)
	assert.Equal(t, "Acme", list.Data[0].Name)
	assert.Equal(t, 1, list.Data[1].ActiveKeys)
	assert.Equal(t, blue.adminTenant, list.Data[2])

	var seen []uuid.UUID
	cursor := ""
	for range 4 {
		path := "/v1/admin/tenants?limit=3"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var p page[adminTenant]
		r = e.do("GET", path, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		r.json(t, &p)
		for _, ten := range p.Data {
			seen = append(seen, ten.ID)
		}
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	assert.Equal(t, []uuid.UUID{e.tenantA, e.tenantB, blue.ID, long.ID}, seen)
	for _, q := range []string{"limit=0", "limit=1001", "limit=x", "cursor=not!a!cursor"} {
		assert.Equal(t, http.StatusBadRequest, e.do("GET", "/v1/admin/tenants?"+q, e.keyA, nil).code, q)
	}
}

func TestAdminTenantKeys(t *testing.T) {
	e := newEnv(t)
	blue := e.newTenant("Team Blue")
	keys := "/v1/admin/tenants/" + blue.ID.String() + "/keys"

	r := e.do("POST", keys, e.keyA, nil)
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	var second keyInfo
	r.json(t, &second)
	require.True(t, strings.HasPrefix(second.Key, second.Prefix), second.Key)
	assert.False(t, second.Current)
	var own tenantInfo
	r = e.do("GET", "/v1/tenant", second.Key, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &own)
	assert.Equal(t, blue.ID, own.ID)
	assert.Equal(t, http.StatusCreated, e.do("POST", keys, e.keyA, `{}`).code)
	assert.Equal(t, http.StatusBadRequest, e.do("POST", keys, e.keyA, `{"name":"x"}`).code)

	var list struct{ Data []keyInfo }
	r = e.do("GET", keys, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &list)
	require.Len(t, list.Data, 3)
	assert.Equal(t, blue.Key.ID, list.Data[0].ID, "oldest first")
	assert.Equal(t, second.ID, list.Data[1].ID)
	for _, k := range list.Data {
		assert.False(t, k.Current, "the admin key is not one of them")
		assert.Empty(t, k.Key)
	}
	assert.NotContains(t, string(r.body), second.Key, "a listing never holds a secret")
	assert.NotContains(t, string(r.body), blue.Key.Key)

	// Revoked: the key stops working, the tenant counts one active key less.
	r = e.do("DELETE", keys+"/"+second.ID.String(), e.keyA, nil)
	require.Equal(t, http.StatusNoContent, r.code, string(r.body))
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/tenant", second.Key, nil).code)
	assert.Equal(t, http.StatusNoContent, e.do("DELETE", keys+"/"+second.ID.String(), e.keyA, nil).code,
		"revoking a revoked key succeeds")
	var got adminTenant
	e.do("GET", "/v1/admin/tenants/"+blue.ID.String(), e.keyA, nil).json(t, &got)
	assert.Equal(t, 2, got.ActiveKeys)
	require.Equal(t, http.StatusOK, e.do("GET", "/v1/tenant", blue.Key.Key, nil).code, "the first key still works")

	// A key of another tenant, an unknown or malformed id, an unknown tenant.
	var a tenantInfo
	e.do("GET", "/v1/tenant", e.keyA, nil).json(t, &a)
	for _, path := range []string{
		keys + "/" + a.Key.ID.String(),
		keys + "/" + uuid.NewString(),
		keys + "/not-a-key",
		"/v1/admin/tenants/" + uuid.NewString() + "/keys/" + blue.Key.ID.String(),
		"/v1/admin/tenants/nope/keys/" + blue.Key.ID.String(),
	} {
		assert.Equal(t, http.StatusNotFound, e.do("DELETE", path, e.keyA, nil).code, path)
	}
	for _, path := range []string{"/v1/admin/tenants/" + uuid.NewString() + "/keys", "/v1/admin/tenants/nope/keys"} {
		assert.Equal(t, http.StatusNotFound, e.do("GET", path, e.keyA, nil).code, path)
		assert.Equal(t, http.StatusNotFound, e.do("POST", path, e.keyA, nil).code, path)
	}
	require.Equal(t, http.StatusOK, e.do("GET", "/v1/tenant", e.keyA, nil).code, "the admin key still works")

	// The admin's own tenant: the calling key is marked, and cannot be revoked.
	mine := "/v1/admin/tenants/" + e.tenantA.String() + "/keys"
	list.Data = nil
	e.do("GET", mine, e.keyA, nil).json(t, &list)
	require.Len(t, list.Data, 1)
	assert.True(t, list.Data[0].Current)
	r = e.do("DELETE", mine+"/"+a.Key.ID.String(), e.keyA, nil)
	assert.Equal(t, http.StatusConflict, r.code, string(r.body))
	require.Equal(t, http.StatusOK, e.do("GET", "/v1/tenant", e.keyA, nil).code)
	var spare keyInfo
	e.do("POST", mine, e.keyA, nil).json(t, &spare)
	assert.Equal(t, http.StatusNoContent, e.do("DELETE", mine+"/"+spare.ID.String(), e.keyA, nil).code,
		"another key of the admin's tenant")
}

func TestAdminTenantPipeline(t *testing.T) {
	e := newEnv(t)
	blue := e.newTenant("Team Blue")
	u := e.user(blue.ID, "blue-1")
	e.user(blue.ID, "blue-2")
	e.user(e.tenantA, "a-1")
	e.exec(`INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, status) VALUES ($1, $2, 'oura', 'active')`, blue.ID, u)
	e.exec(`INSERT INTO ohs_sync.etl_jobs (tenant_id, user_id, provider, key, status, attempts, last_error)
		VALUES ($1, $2, 'apple_health', 'k1', 'parked', 5, 'boom')`, blue.ID, u)

	r := e.do("GET", "/v1/admin/tenants/"+blue.ID.String()+"/pipeline", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var viaAdmin, viaOwn map[string]any
	r.json(t, &viaAdmin)
	r = e.do("GET", "/v1/pipeline", blue.Key.Key, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &viaOwn)
	delete(viaAdmin, "generated_at")
	delete(viaOwn, "generated_at")
	assert.Equal(t, viaOwn, viaAdmin, "the answer the tenant's own key gets")
	assert.Equal(t, map[string]any{"active": float64(2), "deleting": float64(0)}, viaAdmin["users"])
	assert.Equal(t, float64(1), viaAdmin["processing"].(map[string]any)["parked"])

	var mine map[string]any
	e.do("GET", "/v1/pipeline", e.keyA, nil).json(t, &mine)
	assert.Equal(t, float64(1), mine["users"].(map[string]any)["active"], "A's own pipeline is A's")

	for _, id := range []string{uuid.NewString(), "nope"} {
		assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/admin/tenants/"+id+"/pipeline", e.keyA, nil).code, id)
	}
}

type demoUser struct {
	User        userJSON `json:"user"`
	Days        int      `json:"days"`
	RowsWritten int      `json:"rows_written"`
	Persona     string   `json:"persona"`
}

func TestAdminTenantDemoUser(t *testing.T) {
	e := newEnv(t)
	blue := e.newTenant("Team Blue")
	demo := "/v1/admin/tenants/" + blue.ID.String() + "/demo-user"

	r := e.do("POST", demo, e.keyA, `{"days":3}`)
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var first demoUser
	r.json(t, &first)
	require.NotNil(t, first.User.ExternalRef)
	assert.Equal(t, "demo-user", *first.User.ExternalRef)
	assert.Equal(t, "active", first.User.Status)
	assert.Equal(t, 3, first.Days)
	assert.Positive(t, first.RowsWritten)
	assert.Equal(t, "mix", first.Persona)
	assert.ElementsMatch(t, []string{"user", "days", "rows_written", "persona"}, keysOfAny(r.body))
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(r.body, &raw))
	assert.ElementsMatch(t, []string{"id", "external_ref", "status", "created_at"}, keysOfAny(raw["user"]),
		"the user object of POST /v1/users")

	// Readable with the tenant's own key, not with the admin's.
	r = e.do("GET", "/v1/users/"+first.User.ID.String()+"/daily", blue.Key.Key, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var daily page[map[string]any]
	r.json(t, &daily)
	assert.NotEmpty(t, daily.Data)
	r = e.do("GET", "/v1/users/"+first.User.ID.String()+"/samples?type=heart_rate&limit=1", blue.Key.Key, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Contains(t, string(r.body), `"synthetic":true`)
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/users/"+first.User.ID.String()+"/daily", e.keyA, nil).code)

	// Again: the same user, nothing new.
	r = e.do("POST", demo, e.keyA, `{"days":3,"external_ref":"demo-user"}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var again demoUser
	r.json(t, &again)
	assert.Equal(t, first.User.ID, again.User.ID)
	assert.Zero(t, again.RowsWritten)

	// Another ref, the default length.
	r = e.do("POST", demo, e.keyA, `{"external_ref":"demo-2"}`)
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var other demoUser
	r.json(t, &other)
	assert.Equal(t, 30, other.Days)
	assert.NotEqual(t, first.User.ID, other.User.ID)
	assert.Greater(t, other.RowsWritten, first.RowsWritten)
	r = e.do("POST", "/v1/admin/tenants/"+e.tenantB.String()+"/demo-user", e.keyA, nil)
	require.Equal(t, http.StatusCreated, r.code, "no body: every default: %s", r.body)

	for _, body := range []string{
		`{"days":0}`, `{"days":91}`, `{"days":-1}`, `{"days":"30"}`, `{"days":1.5}`,
		`{"external_ref":""}`, `{"external_ref":"` + strings.Repeat("x", 256) + `"}`, `{"seed":2}`, `[`,
		`{"persona":""}`, `{"persona":"athlete"}`, `{"persona":"Runner"}`, `{"persona":1}`,
	} {
		r = e.do("POST", demo, e.keyA, body)
		assert.Equal(t, http.StatusBadRequest, r.code, "%s: %s", body, r.body)
	}
	r = e.do("POST", demo, e.keyA, `{"persona":"athlete"}`)
	assert.Contains(t, string(r.body), "persona is one of mix, runner, sleep, weight")
	for _, id := range []string{uuid.NewString(), "nope"} {
		assert.Equal(t, http.StatusNotFound, e.do("POST", "/v1/admin/tenants/"+id+"/demo-user", e.keyA, `{"days":1}`).code, id)
	}
	e.exec(`UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, other.User.ID)
	r = e.do("POST", demo, e.keyA, `{"external_ref":"demo-2","days":1}`)
	assert.Equal(t, http.StatusConflict, r.code, string(r.body))
}

// Each change is recorded twice: on the admin's tenant like any request, and
// on the tenant it changed, naming the admin key and never a secret.
func TestAdminTenantsAudit(t *testing.T) {
	e := newEnv(t)
	var a tenantInfo
	e.do("GET", "/v1/tenant", e.keyA, nil).json(t, &a)
	blue := e.newTenant("Team Blue")
	base := "/v1/admin/tenants/" + blue.ID.String()
	var second keyInfo
	e.do("POST", base+"/keys", e.keyA, nil).json(t, &second)
	require.Equal(t, http.StatusNoContent, e.do("DELETE", base+"/keys/"+second.ID.String(), e.keyA, nil).code)
	var demo demoUser
	r := e.do("POST", base+"/demo-user", e.keyA, `{"days":1}`, "X-Request-Id", "req-demo-1")
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	r.json(t, &demo)
	require.Equal(t, http.StatusOK, e.do("GET", base+"/pipeline", e.keyA, nil).code)

	// The target tenant's log, read with its own key.
	var target auditPage
	r = e.do("GET", "/v1/audit?limit=100", blue.Key.Key, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &target)
	assert.NotContains(t, string(r.body), blue.Key.Key)
	assert.NotContains(t, string(r.body), second.Key)
	byAction := map[string]audit.Entry{}
	for _, ev := range target.Data {
		if ev.Actor.Type == audit.Admin {
			_, dup := byAction[ev.Action]
			assert.False(t, dup, "one event per change: %+v", ev)
			byAction[ev.Action] = ev
		}
	}
	require.Len(t, byAction, 4, "%+v", target.Data)
	adminActor := audit.Actor{Type: audit.Admin, ID: "api_key:" + a.Key.ID.String()}
	for action, key := range map[string]keyInfo{audit.TenantCreate: blue.Key, audit.KeyCreate: second, audit.KeyRevoke: second} {
		ev := byAction[action]
		assert.Equal(t, adminActor, ev.Actor, action)
		assert.Equal(t, e.tenantA.String(), ev.Detail["admin_tenant_id"], action)
		assert.Equal(t, key.ID.String(), ev.Detail["key_id"], action)
		assert.Equal(t, key.Prefix+"...", ev.Detail["key_prefix"], action)
		assert.Nil(t, ev.UserID, action)
	}
	assert.Equal(t, "POST /v1/admin/tenants", byAction[audit.TenantCreate].Route)
	assert.Equal(t, http.StatusCreated, *byAction[audit.TenantCreate].Status)
	assert.Equal(t, "DELETE /v1/admin/tenants/{tenant_id}/keys/{key_id}", byAction[audit.KeyRevoke].Route)
	assert.Equal(t, http.StatusNoContent, *byAction[audit.KeyRevoke].Status)
	written := byAction[audit.DataWrite]
	assert.Equal(t, adminActor, written.Actor)
	require.NotNil(t, written.UserID)
	assert.Equal(t, demo.User.ID, *written.UserID)
	assert.Equal(t, "req-demo-1", written.RequestID)
	assert.Equal(t, map[string]any{"admin_tenant_id": e.tenantA.String(), "synthetic": true, "days": float64(1),
		"persona": "mix", "rows_written": float64(demo.RowsWritten), "user_created": true}, written.Detail)

	// The admin's own log: one event per request, its action by route, the
	// target tenant in the detail, never the target's user.
	var own auditPage
	e.do("GET", "/v1/audit?limit=100", e.keyA, nil).json(t, &own)
	actions := map[string]audit.Entry{}
	for _, ev := range own.Data {
		if strings.HasPrefix(ev.Route, "POST /v1/admin/tenants") || strings.HasPrefix(ev.Route, "DELETE /v1/admin/tenants") ||
			strings.HasPrefix(ev.Route, "GET /v1/admin/tenants") {
			assert.Equal(t, audit.APIKey, ev.Actor.Type)
			assert.Equal(t, a.Key.ID.String(), ev.Actor.ID)
			assert.Nil(t, ev.UserID)
			actions[ev.Route] = ev
		}
	}
	assert.Equal(t, audit.TenantCreate, actions["POST /v1/admin/tenants"].Action)
	assert.Equal(t, audit.KeyCreate, actions["POST /v1/admin/tenants/{tenant_id}/keys"].Action)
	assert.Equal(t, blue.ID.String(), actions["POST /v1/admin/tenants/{tenant_id}/keys"].Detail["tenant_id"])
	revoke := actions["DELETE /v1/admin/tenants/{tenant_id}/keys/{key_id}"]
	assert.Equal(t, audit.KeyRevoke, revoke.Action)
	assert.Equal(t, map[string]any{"tenant_id": blue.ID.String(), "key_id": second.ID.String()}, revoke.Detail)
	assert.Equal(t, audit.DataWrite, actions["POST /v1/admin/tenants/{tenant_id}/demo-user"].Action)
	assert.Equal(t, audit.ConfigRead, actions["GET /v1/admin/tenants/{tenant_id}/pipeline"].Action)

	// A refused request changes nothing and records nothing on the target.
	before := len(target.Data)
	require.Equal(t, http.StatusConflict, e.do("DELETE", "/v1/admin/tenants/"+e.tenantA.String()+"/keys/"+a.Key.ID.String(), e.keyA, nil).code)
	var mine auditPage
	e.do("GET", "/v1/audit?action=key.revoke", e.keyA, nil).json(t, &mine)
	for _, ev := range mine.Data {
		assert.NotEqual(t, audit.Admin, ev.Actor.Type, "the refused revocation is no change: %+v", ev)
	}
	target = auditPage{}
	e.do("GET", "/v1/audit?limit=100", blue.Key.Key, nil).json(t, &target)
	assert.Len(t, target.Data, before+1, "only the read of the log itself")
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOfAny(raw []byte) []string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return keysOf(m)
}

// Each persona fills a user of its own, from its own providers.
func TestAdminTenantDemoUserPersonas(t *testing.T) {
	e := newEnv(t)
	blue := e.newTenant("Team Blue")
	path := "/v1/admin/tenants/" + blue.ID.String() + "/demo-user"
	users := map[uuid.UUID]bool{}
	for name, providers := range map[string][]string{
		"mix":    {"apple_health", "fitbit", "oura", "polar", "whoop", "withings"},
		"runner": {"apple_health", "polar", "whoop"},
		"sleep":  {"apple_health", "oura"},
		"weight": {"fitbit", "withings"},
	} {
		r := e.do("POST", path, e.keyA, `{"days":3,"persona":"`+name+`"}`)
		require.Equal(t, http.StatusCreated, r.code, "%s: %s", name, r.body)
		var d demoUser
		r.json(t, &d)
		assert.Equal(t, name, d.Persona)
		want := "demo-" + name
		if name == "mix" {
			want = "demo-user"
		}
		require.NotNil(t, d.User.ExternalRef)
		assert.Equal(t, want, *d.User.ExternalRef, "each persona its own user by default")
		assert.Positive(t, d.RowsWritten)
		users[d.User.ID] = true

		var cov struct{ Days []struct{ Provider string } }
		r = e.do("GET", "/v1/users/"+d.User.ID.String()+"/coverage", blue.Key.Key, nil)
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		r.json(t, &cov)
		got := map[string]bool{}
		for _, day := range cov.Days {
			assert.Contains(t, providers, day.Provider, "%s: only its providers", name)
			got[day.Provider] = true
		}
		for _, p := range providers {
			// The watch writes on workout days only, maybe none of three.
			assert.True(t, got[p] || p == "polar", "%s: nothing from %s", name, p)
		}

		r = e.do("POST", path, e.keyA, `{"days":3,"persona":"`+name+`"}`)
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		r.json(t, &d)
		assert.Zero(t, d.RowsWritten, "%s: again, nothing new", name)
	}
	assert.Len(t, users, 4)
}
