// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/selfreport"
	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
)

type tenantProvider struct {
	Slug           string     `json:"slug"`
	Name           string     `json:"name"`
	Kinds          []string   `json:"kinds"`
	InstanceStatus string     `json:"instance_status"`
	Enabled        bool       `json:"enabled"`
	OwnKeys        bool       `json:"own_keys"`
	Available      bool       `json:"available"`
	UpdatedAt      *time.Time `json:"updated_at"`
}

func (e *env) tenantProviders(tenantID uuid.UUID) []tenantProvider {
	e.t.Helper()
	r := e.do("GET", "/v1/admin/tenants/"+tenantID.String()+"/providers", e.keyA, nil)
	require.Equal(e.t, http.StatusOK, r.code, string(r.body))
	var out struct{ Data []tenantProvider }
	r.json(e.t, &out)
	return out.Data
}

func providerSlugs(t *testing.T, e *env, key string) []string {
	t.Helper()
	var body struct{ Data []struct{ Slug string } }
	r := e.do("GET", "/v1/providers", key, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &body)
	out := []string{}
	for _, p := range body.Data {
		out = append(out, p.Slug)
	}
	return out
}

// An admin turns a provider off for one tenant: the tenant's listing and
// uploads lose it, other tenants keep it, and the change is audited on both
// sides. Turned back on, all of it comes back.
func TestAdminTenantProviders(t *testing.T) {
	e, _ := adminEnv(t, nil)
	var a tenantInfo
	e.do("GET", "/v1/tenant", e.keyA, nil).json(t, &a)
	blue := e.newTenant("Team Blue")
	base := "/v1/admin/tenants/" + blue.ID.String() + "/providers"
	u := e.user(blue.ID, "b-1")
	tok, _, err := e.store.MintUserToken(context.Background(), blue.ID, u, []string{"upload"}, time.Hour)
	require.NoError(t, err)

	// Every provider the instance knows, on by default.
	r := e.do("GET", base, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.JSONEq(t, `{"data":[
		{"slug":"apple_health","name":"Apple Health","kinds":["device"],"instance_status":"built_in","enabled":true,"own_keys":false,"available":true,"updated_at":null},
		{"slug":"dexcom","name":"Dexcom","kinds":["oauth"],"instance_status":"not_configured","enabled":true,"own_keys":false,"available":false,"updated_at":null}]}`,
		string(r.body))
	assert.Equal(t, []string{"apple_health"}, providerSlugs(t, e, blue.Key.Key))
	assert.Equal(t, http.StatusBadRequest, e.do("POST", "/v1/ingest/apple-health", tok, []byte("x")).code,
		"reaches the upload (and fails on its headers)")

	// Off.
	r = e.do("PATCH", base+"/apple_health", e.keyA, `{"enabled":false}`, "X-Request-Id", "req-switch-1")
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var off tenantProvider
	r.json(t, &off)
	assert.ElementsMatch(t, []string{"slug", "name", "kinds", "instance_status", "enabled", "own_keys", "available", "updated_at"},
		keysOfAny(r.body))
	assert.Equal(t, "apple_health", off.Slug)
	assert.Equal(t, "built_in", off.InstanceStatus)
	assert.False(t, off.Enabled)
	assert.False(t, off.Available)
	require.NotNil(t, off.UpdatedAt)
	assert.WithinDuration(t, time.Now(), *off.UpdatedAt, time.Minute)
	listed := e.tenantProviders(blue.ID)
	require.Len(t, listed, 2)
	assert.Equal(t, off, listed[0])
	assert.True(t, listed[1].Enabled, "dexcom untouched")

	assert.Empty(t, providerSlugs(t, e, blue.Key.Key), "left out of the tenant's listing")
	assert.Equal(t, []string{"apple_health"}, providerSlugs(t, e, e.keyA), "another tenant keeps it")
	r = e.do("POST", "/v1/ingest/apple-health", tok, []byte("x"))
	require.Equal(t, http.StatusForbidden, r.code, string(r.body))
	assert.Contains(t, string(r.body), collect.ProviderOffDetail)
	assert.Equal(t, http.StatusBadRequest, e.do("POST", "/v1/ingest/health-connect", tok, []byte("x")).code,
		"another provider of the tenant is not affected")
	assert.True(t, e.switches.Allowed(e.tenantA, "apple_health"))

	// Turning off what is off changes nothing but the time.
	r = e.do("PATCH", base+"/apple_health", e.keyA, `{"enabled":false}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))

	// Refused.
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":"no"}`, `{"enabled":true,"x":1}`, `[`, ``} {
		r = e.do("PATCH", base+"/apple_health", e.keyA, body)
		assert.Equal(t, http.StatusBadRequest, r.code, "%q: %s", body, r.body)
	}
	assert.Equal(t, http.StatusNotFound, e.do("PATCH", base+"/nope", e.keyA, `{"enabled":false}`).code, "unknown provider")
	for _, id := range []string{uuid.NewString(), "nope"} {
		assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/admin/tenants/"+id+"/providers", e.keyA, nil).code, id)
		assert.Equal(t, http.StatusNotFound,
			e.do("PATCH", "/v1/admin/tenants/"+id+"/providers/apple_health", e.keyA, `{"enabled":false}`).code, id)
	}
	assert.Equal(t, http.StatusForbidden, e.do("GET", base, blue.Key.Key, nil).code, "not an admin key")
	assert.Equal(t, http.StatusForbidden, e.do("PATCH", base+"/apple_health", blue.Key.Key, `{"enabled":true}`).code)

	// Audited on the tenant, once per change, naming the admin key.
	var target auditPage
	e.do("GET", "/v1/audit?action=tenant.provider", blue.Key.Key, nil).json(t, &target)
	require.Len(t, target.Data, 2, "%+v", target.Data)
	ev := target.Data[1]
	assert.Equal(t, audit.Actor{Type: audit.Admin, ID: "api_key:" + a.Key.ID.String()}, ev.Actor)
	assert.Equal(t, "PATCH /v1/admin/tenants/{tenant_id}/providers/{provider}", ev.Route)
	assert.Equal(t, http.StatusOK, *ev.Status)
	assert.Equal(t, "req-switch-1", ev.RequestID)
	assert.Equal(t, map[string]any{"admin_tenant_id": e.tenantA.String(), "provider": "apple_health", "enabled": false},
		ev.Detail)
	assert.Nil(t, ev.UserID)

	// And on the admin's own tenant, like any request (refused ones too),
	// by route, with the target in the detail.
	var own auditPage
	e.do("GET", "/v1/audit?action=tenant.provider&limit=100", e.keyA, nil).json(t, &own)
	var first *audit.Entry
	for i, ev := range own.Data {
		assert.Equal(t, audit.Actor{Type: audit.APIKey, ID: a.Key.ID.String()}, ev.Actor)
		if ev.RequestID == "req-switch-1" {
			first = &own.Data[i]
		}
	}
	require.NotNil(t, first, "%+v", own.Data)
	assert.Equal(t, http.StatusOK, *first.Status)
	assert.Equal(t, map[string]any{"tenant_id": blue.ID.String(), "provider": "apple_health"}, first.Detail)
	var reads auditPage
	e.do("GET", "/v1/audit?action=config.read", e.keyA, nil).json(t, &reads)
	found := false
	for _, ev := range reads.Data {
		found = found || ev.Route == "GET /v1/admin/tenants/{tenant_id}/providers"
	}
	assert.True(t, found, "the listing is a config read")

	// Back on.
	r = e.do("PATCH", base+"/apple_health", e.keyA, `{"enabled":true}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var on tenantProvider
	r.json(t, &on)
	assert.True(t, on.Enabled)
	assert.True(t, on.Available)
	assert.True(t, on.UpdatedAt.After(*off.UpdatedAt))
	assert.Equal(t, []string{"apple_health"}, providerSlugs(t, e, blue.Key.Key))
	assert.Equal(t, http.StatusBadRequest, e.do("POST", "/v1/ingest/apple-health", tok, []byte("x")).code)

	// Off for a provider the instance does not run: saved, not available.
	r = e.do("PATCH", base+"/dexcom", e.keyA, `{"enabled":false}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &off)
	assert.Equal(t, "not_configured", off.InstanceStatus)
	assert.False(t, off.Available)
}

// A self report to a tenant whose self_report provider is off is refused
// before anything is stored.
func TestSelfReportsProviderOff(t *testing.T) {
	e := newSelfReportEnv(t)
	u := e.user(e.tenantA, "rater")
	body := `{"reports":[{"kind":"feeling","value":4,"start_at":"2026-03-01T08:30:00Z"}]}`
	_, err := e.switches.Update(context.Background(), e.tenantA, selfreport.Slug, false, "test", nil)
	require.NoError(t, err)

	r, _ := e.report(u, e.keyA, body)
	require.Equal(t, http.StatusForbidden, r.code, string(r.body))
	assert.Contains(t, string(r.body), collect.ProviderOffDetail)
	assert.Zero(t, e.processAll())
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_sync.raw_objects`).Scan(&n))
	assert.Zero(t, n)

	_, err = e.switches.Update(context.Background(), e.tenantA, selfreport.Slug, true, "test", nil)
	require.NoError(t, err)
	r, _ = e.report(u, e.keyA, body)
	assert.Equal(t, http.StatusAccepted, r.code, string(r.body))
}

// Without the switches the routes are left out and every provider is on.
func TestAdminTenantProvidersNeedTheSwitches(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.TenantProviders = nil })
	r := e.do("GET", "/v1/admin/tenants/"+e.tenantB.String()+"/providers", e.keyA, nil)
	assert.Equal(t, http.StatusNotFound, r.code, string(r.body))
	assert.Equal(t, []string{"apple_health"}, providerSlugs(t, e, e.keyB))
}
