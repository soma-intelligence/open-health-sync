// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
)

type tenantKeys struct {
	Provider string  `json:"provider"`
	Set      bool    `json:"set"`
	Status   string  `json:"status"`
	Error    *string `json:"error"`
	ClientID *string `json:"client_id"`
	Keys     []struct {
		Name     string `json:"name"`
		Required bool   `json:"required"`
		Secret   bool   `json:"secret"`
		Set      bool   `json:"set"`
	} `json:"keys"`
	URLs struct {
		Redirect *string `json:"redirect"`
		Inbound  *string `json:"inbound"`
	} `json:"urls"`
	CreatedAt      *time.Time `json:"created_at"`
	UpdatedAt      *time.Time `json:"updated_at"`
	UpdatedBy      *string    `json:"updated_by"`
	ReauthRequired *int       `json:"reauth_required"`
}

// An admin sets a tenant's own keys for a provider, rotates a secret,
// switches to another application and removes them; no secret is ever
// served back, each change is audited on the tenant, and the connections
// made with keys that went away need a new consent.
func TestAdminTenantProviderKeys(t *testing.T) {
	e, _ := adminEnv(t, nil)
	ctx := context.Background()
	var a tenantInfo
	e.do("GET", "/v1/tenant", e.keyA, nil).json(t, &a)
	blue := e.newTenant("Team Blue")
	route := "/v1/admin/tenants/" + blue.ID.String() + "/providers/dexcom/keys"

	// Nothing set yet.
	r := e.do("GET", route, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var st tenantKeys
	r.json(t, &st)
	assert.Equal(t, "dexcom", st.Provider)
	assert.False(t, st.Set)
	assert.Equal(t, "not_set", st.Status)
	assert.Nil(t, st.ClientID)
	assert.Nil(t, st.UpdatedAt)
	assert.Nil(t, st.ReauthRequired)
	require.Len(t, st.Keys, 3)
	assert.Equal(t, "CLIENT_ID", st.Keys[0].Name)
	assert.True(t, st.Keys[0].Required)
	assert.False(t, st.Keys[0].Secret)
	assert.Equal(t, "CLIENT_SECRET", st.Keys[1].Name)
	assert.True(t, st.Keys[1].Secret)
	assert.Equal(t, "REGION", st.Keys[2].Name)
	assert.False(t, st.Keys[2].Required)
	require.NotNil(t, st.URLs.Redirect)
	assert.True(t, strings.HasSuffix(*st.URLs.Redirect, "/oauth/dexcom/callback"))
	assert.Nil(t, st.URLs.Inbound, "dexcom sends no notifications")
	assert.ElementsMatch(t, []string{"provider", "set", "status", "error", "client_id", "keys", "urls",
		"created_at", "updated_at", "updated_by"}, keysOfAny(r.body))

	// Refused.
	for body, code := range map[string]int{
		`{}`: http.StatusBadRequest,
		`{"keys":{"CLIENT_ID":"blue-client-0001"}}`:                      http.StatusBadRequest,
		`{"keys":{"CLIENT_ID":"a","CLIENT_SECRET":"b","NOPE":"c"}}`:      http.StatusBadRequest,
		`{"keys":{"CLIENT_ID":"a","CLIENT_SECRET":"b"},"x":1}`:           http.StatusBadRequest,
		`{"keys":{"CLIENT_ID":"a","CLIENT_SECRET":"b\u0000"}}`:           http.StatusBadRequest,
		`{"keys":{"CLIENT_ID":"a","CLIENT_SECRET":"b","REGION":"mars"}}`: http.StatusUnprocessableEntity,
		`[`: http.StatusBadRequest,
	} {
		r = e.do("PUT", route, e.keyA, body)
		assert.Equal(t, code, r.code, "%s: %s", body, r.body)
	}
	assert.Equal(t, http.StatusNotFound, e.do("PUT", "/v1/admin/tenants/"+blue.ID.String()+"/providers/nope/keys",
		e.keyA, `{"keys":{}}`).code)
	assert.Equal(t, http.StatusConflict, e.do("GET", "/v1/admin/tenants/"+blue.ID.String()+"/providers/apple_health/keys",
		e.keyA, nil).code, "a device source takes no keys")
	for _, id := range []string{uuid.NewString(), "nope"} {
		assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/admin/tenants/"+id+"/providers/dexcom/keys", e.keyA, nil).code)
	}
	assert.Equal(t, http.StatusForbidden, e.do("GET", route, blue.Key.Key, nil).code, "not an admin key")
	assert.Equal(t, http.StatusForbidden, e.do("PUT", route, blue.Key.Key,
		`{"keys":{"CLIENT_ID":"a","CLIENT_SECRET":"b"}}`).code)
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", route, e.keyA, nil).code, "nothing to remove")

	// Set.
	r = e.do("PUT", route, e.keyA, `{"keys":{"CLIENT_ID":" blue-client-0001 ","CLIENT_SECRET":"blue-secret-one"}}`,
		"X-Request-Id", "req-keys-1")
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.NotContains(t, string(r.body), "blue-secret-one")
	assert.NotContains(t, string(r.body), "blue-client-0001", "the client id is shown in part only")
	r.json(t, &st)
	assert.True(t, st.Set)
	assert.Equal(t, "active", st.Status)
	require.NotNil(t, st.ClientID)
	assert.Equal(t, "blue...0001", *st.ClientID)
	assert.True(t, st.Keys[0].Set)
	assert.True(t, st.Keys[1].Set)
	assert.False(t, st.Keys[2].Set)
	require.NotNil(t, st.ReauthRequired)
	assert.Zero(t, *st.ReauthRequired)
	require.NotNil(t, st.UpdatedBy)
	assert.Equal(t, a.Key.ID.String(), *st.UpdatedBy)
	require.NotNil(t, st.UpdatedAt)
	assert.WithinDuration(t, time.Now(), *st.UpdatedAt, time.Minute)

	r = e.do("GET", route, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	assert.NotContains(t, string(r.body), "blue-secret-one")
	var got tenantKeys
	r.json(t, &got)
	assert.Equal(t, "active", got.Status)
	assert.Nil(t, got.ReauthRequired)

	var sealed []byte
	var keysID uuid.UUID
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT keys_sealed, keys_id FROM ohs_core.tenant_provider_keys
		WHERE tenant_id = $1 AND provider = 'dexcom'`, blue.ID).Scan(&sealed, &keysID))
	assert.NotContains(t, string(sealed), "blue-secret-one", "sealed at rest")

	// The tenant now has the provider, though the instance has no keys for
	// it; other tenants do not.
	listed := e.tenantProviders(blue.ID)
	require.Len(t, listed, 2)
	assert.True(t, listed[1].OwnKeys)
	assert.True(t, listed[1].Available)
	assert.Equal(t, "not_configured", listed[1].InstanceStatus)
	assert.Equal(t, []string{"apple_health", "dexcom"}, providerSlugs(t, e, blue.Key.Key))
	assert.Equal(t, []string{"apple_health"}, providerSlugs(t, e, e.keyA))

	// A connection made with these keys.
	u := e.user(blue.ID, "b-1")
	e.exec(`INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, status, tenant_keys_id)
		VALUES ($1, $2, 'dexcom', 'active', $3)`, blue.ID, u, keysID)
	status := func() string {
		var s string
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT status FROM ohs_sync.connections WHERE user_id = $1`, u).Scan(&s))
		return s
	}

	// A secret rotated: the same application, the connection is kept.
	r = e.do("PUT", route, e.keyA, `{"keys":{"CLIENT_ID":"blue-client-0001","CLIENT_SECRET":"blue-secret-two"}}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &st)
	assert.Zero(t, *st.ReauthRequired)
	assert.Equal(t, "active", status())

	// Another application (another client id, or another region of the
	// provider): the connection needs a new consent.
	r = e.do("PUT", route, e.keyA, `{"keys":{"CLIENT_ID":"blue-client-0001","CLIENT_SECRET":"blue-secret-two","REGION":"eu"}}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &st)
	assert.Equal(t, 1, *st.ReauthRequired)
	assert.Equal(t, "needs_reauth", status())

	// Removed.
	e.exec(`UPDATE ohs_sync.connections SET status = 'active', tenant_keys_id =
		(SELECT keys_id FROM ohs_core.tenant_provider_keys WHERE tenant_id = $1) WHERE user_id = $2`, blue.ID, u)
	r = e.do("DELETE", route, e.keyA, nil, "X-Request-Id", "req-keys-del")
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &st)
	assert.False(t, st.Set)
	assert.Equal(t, "not_set", st.Status)
	assert.Equal(t, 1, *st.ReauthRequired)
	assert.Equal(t, "needs_reauth", status())
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", route, e.keyA, nil).code)
	assert.Equal(t, []string{"apple_health"}, providerSlugs(t, e, blue.Key.Key))
	assert.False(t, e.tenantProviders(blue.ID)[1].OwnKeys)

	// Audited on the tenant, once per change, never with a key.
	var target auditPage
	e.do("GET", "/v1/audit?action=tenant.provider_keys", blue.Key.Key, nil).json(t, &target)
	require.Len(t, target.Data, 4, "%+v", target.Data)
	first, last := target.Data[3], target.Data[0]
	assert.Equal(t, audit.Actor{Type: audit.Admin, ID: "api_key:" + a.Key.ID.String()}, first.Actor)
	assert.Equal(t, "PUT /v1/admin/tenants/{tenant_id}/providers/{provider}/keys", first.Route)
	assert.Equal(t, "req-keys-1", first.RequestID)
	assert.Equal(t, map[string]any{"admin_tenant_id": e.tenantA.String(), "provider": "dexcom", "set": true,
		"reauth_required": float64(0)}, first.Detail)
	assert.Equal(t, "req-keys-del", last.RequestID)
	assert.Equal(t, map[string]any{"admin_tenant_id": e.tenantA.String(), "provider": "dexcom", "set": false,
		"reauth_required": float64(1)}, last.Detail)
	for _, ev := range target.Data {
		for _, v := range ev.Detail {
			s, _ := v.(string)
			assert.NotContains(t, s, "blue-")
		}
	}
	var own auditPage
	e.do("GET", "/v1/audit?action=tenant.provider_keys&limit=100", e.keyA, nil).json(t, &own)
	assert.NotEmpty(t, own.Data, "the admin's own tenant records the requests too")
}
