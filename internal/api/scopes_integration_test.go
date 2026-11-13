// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scopedKey struct {
	ID     uuid.UUID `json:"id"`
	Scopes []string  `json:"scopes"`
	Key    string    `json:"key"`
}

// newScopedKey creates a key at path with the calling key and the scopes.
func (e *env) newScopedKey(path, key string, scopes ...string) scopedKey {
	e.t.Helper()
	r := e.do("POST", path, key, map[string]any{"scopes": scopes})
	require.Equal(e.t, http.StatusCreated, r.code, string(r.body))
	var k scopedKey
	r.json(e.t, &k)
	return k
}

func problemDetail(t *testing.T, r resp) string {
	t.Helper()
	var p struct {
		Detail string `json:"detail"`
	}
	r.json(t, &p)
	return p.Detail
}

// A key created with scopes does what they allow and nothing else: the
// refusal is a 403 naming the scope, and it is in the audit log.
func TestScopedKeyThroughTheAPI(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")

	// The keys that exist do everything, and say so.
	var own struct {
		Key struct {
			Scopes []string `json:"scopes"`
		} `json:"key"`
	}
	r := e.do("GET", "/v1/tenant", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &own)
	assert.Equal(t, []string{"*"}, own.Key.Scopes)

	k := e.newScopedKey("/v1/keys", e.keyA, "data:read", "users:read", "users:read")
	assert.Equal(t, []string{"users:read", "data:read"}, k.Scopes)

	assert.Equal(t, http.StatusOK, e.do("GET", "/v1/users", k.Key, nil).code)
	assert.Equal(t, http.StatusOK, e.do("GET", "/v1/users/"+u.String()+"/sleep", k.Key, nil).code)

	r = e.do("POST", "/v1/users", k.Key, map[string]any{"external_ref": "a-2"})
	require.Equal(t, http.StatusForbidden, r.code, string(r.body))
	assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"))
	assert.Equal(t, "this endpoint needs the scope users:write, which this API key does not hold", problemDetail(t, r))
	for path, scope := range map[string]string{
		"/v1/tenant":                           "tenant:read",
		"/v1/keys":                             "keys:read",
		"/v1/audit":                            "audit:read",
		"/v1/pipeline":                         "connections:read",
		"/v1/users/" + u.String() + "/archive": "exports:read",
		"/v1/admin/tenants":                    "admin:read",
	} {
		r = e.do("GET", path, k.Key, nil)
		require.Equal(t, http.StatusForbidden, r.code, path+": "+string(r.body))
		assert.Contains(t, problemDetail(t, r), "needs the scope "+scope+",", path)
	}
	r = e.do("DELETE", "/v1/users/"+u.String(), k.Key, nil)
	require.Equal(t, http.StatusForbidden, r.code)
	assert.Contains(t, problemDetail(t, r), "data:delete")

	// The refusals are on the tenant's log, with the scope that lacked.
	var n int
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM ohs_core.audit_events
		 WHERE tenant_id = $1 AND actor_id = $2 AND status = 403 AND action = 'user.create'
		   AND detail->>'missing_scope' = 'users:write'`, e.tenantA, k.ID.String()).Scan(&n))
	assert.Equal(t, 1, n)

	// Listings show the scopes of each key.
	var list struct {
		Data []scopedKey `json:"data"`
	}
	r = e.do("GET", "/v1/keys", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	r.json(t, &list)
	require.Len(t, list.Data, 2)
	assert.Equal(t, []string{"*"}, list.Data[0].Scopes)
	assert.Equal(t, []string{"users:read", "data:read"}, list.Data[1].Scopes)

	// A scope that does not exist is a 400 that lists the scopes.
	for _, path := range []string{"/v1/keys", "/v1/admin/tenants/" + e.tenantB.String() + "/keys"} {
		r = e.do("POST", path, e.keyA, map[string]any{"scopes": []string{"data:read", "everything"}})
		require.Equal(t, http.StatusBadRequest, r.code, path+": "+string(r.body))
		assert.Contains(t, problemDetail(t, r), "webhooks:write", path)
	}
}

// A key hands on only what it holds: to a new key, and to a user token.
func TestScopedKeyGrantsNoMoreThanItHolds(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	tokens := "/v1/users/" + u.String() + "/tokens"

	k := e.newScopedKey("/v1/keys", e.keyA, "keys:write", "users:write", "tokens:write", "data:read")

	// Without scopes the new key holds what the calling key holds.
	r := e.do("POST", "/v1/keys", k.Key, nil)
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var child scopedKey
	r.json(t, &child)
	assert.Equal(t, []string{"users:write", "tokens:write", "data:read", "keys:write"}, child.Scopes)

	// users:write grants users:read, so it can be handed on.
	assert.Equal(t, []string{"users:read", "keys:read"}, e.newScopedKey("/v1/keys", k.Key, "users:read", "keys:read").Scopes)
	for _, scope := range []string{"*", "data:write", "admin:write", "webhooks:read"} {
		r = e.do("POST", "/v1/keys", k.Key, map[string]any{"scopes": []string{"users:read", scope}})
		require.Equal(t, http.StatusForbidden, r.code, scope+": "+string(r.body))
		assert.Equal(t, "this API key does not hold the scope "+scope+" and cannot grant it", problemDetail(t, r), scope)
	}

	// A token: read needs data:read, upload data:write, connect connections:write.
	r = e.do("POST", tokens, k.Key, map[string]any{"scopes": []string{"read"}})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var tok mintedToken
	r.json(t, &tok)
	for scope, need := range map[string]string{"upload": "data:write", "connect": "connections:write"} {
		r = e.do("POST", tokens, k.Key, map[string]any{"scopes": []string{"read", scope}})
		require.Equal(t, http.StatusForbidden, r.code, scope+": "+string(r.body))
		assert.Contains(t, problemDetail(t, r), "does not hold the scope "+need, scope)
	}
	assert.Equal(t, http.StatusBadRequest, e.do("POST", tokens, k.Key, map[string]any{"scopes": []string{"nope"}}).code)
	// A key without tokens:write mints nothing.
	reader := e.newScopedKey("/v1/keys", e.keyA, "data:read")
	r = e.do("POST", tokens, reader.Key, map[string]any{"scopes": []string{"read"}})
	require.Equal(t, http.StatusForbidden, r.code)
	assert.Contains(t, problemDetail(t, r), "needs the scope tokens:write")

	// The token is held to its key on each request: the archive needs
	// exports:read, which the key lacks; a token of a full key reads it.
	r = e.do("GET", "/v1/users/"+u.String()+"/archive", tok.Token, nil)
	require.Equal(t, http.StatusForbidden, r.code, string(r.body))
	assert.Equal(t, "this endpoint needs the scope exports:read, which the API key that minted this user token does not hold",
		problemDetail(t, r))
	fullTok := e.mint(u, e.keyA)
	require.True(t, e.tokenWorks(u, fullTok.Token))
	// If the key loses a scope, so do its tokens, at once.
	e.exec(`UPDATE ohs_core.api_keys SET scopes = '{users:read}' WHERE tenant_id = $1 AND key_prefix = $2`, e.tenantA, e.keyA[:12])
	r = e.do("GET", "/v1/users/"+u.String()+"/archive", fullTok.Token, nil)
	assert.Equal(t, http.StatusForbidden, r.code, string(r.body))
}

// An admin creates keys with any scopes for any tenant, and an admin key
// can itself be reduced: to reads of the instance, or to its own tenant.
func TestScopedAdminKeys(t *testing.T) {
	e := newEnv(t)
	tenants := "/v1/admin/tenants"
	keysB := tenants + "/" + e.tenantB.String() + "/keys"

	// A key for another tenant, with scopes; without, everything.
	kb := e.newScopedKey(keysB, e.keyA, "data:read")
	assert.Equal(t, []string{"data:read"}, kb.Scopes)
	assert.Equal(t, http.StatusForbidden, e.do("GET", "/v1/users", kb.Key, nil).code)
	r := e.do("POST", keysB, e.keyA, nil)
	require.Equal(t, http.StatusCreated, r.code)
	var plain scopedKey
	r.json(t, &plain)
	assert.Equal(t, []string{"*"}, plain.Scopes)
	var n int
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM ohs_core.audit_events
		 WHERE tenant_id = $1 AND action = 'key.create' AND detail->'scopes' = '["data:read"]'`, e.tenantB).Scan(&n))
	assert.Equal(t, 1, n, "the tenant's log names the scopes of the key an admin made")

	// admin:read reads every tenant and changes nothing.
	ro := e.newScopedKey("/v1/keys", e.keyA, "admin:read")
	assert.Equal(t, http.StatusOK, e.do("GET", tenants, ro.Key, nil).code)
	assert.Equal(t, http.StatusOK, e.do("GET", tenants+"/"+e.tenantB.String()+"/users", ro.Key, nil).code)
	assert.Equal(t, http.StatusOK, e.do("GET", keysB, ro.Key, nil).code)
	for _, call := range [][2]string{{"POST", tenants}, {"POST", keysB}, {"POST", tenants + "/" + e.tenantB.String() + "/users"}} {
		r = e.do(call[0], call[1], ro.Key, map[string]any{"name": "x"})
		require.Equal(t, http.StatusForbidden, r.code, call[1]+": "+string(r.body))
		assert.Contains(t, problemDetail(t, r), "needs the scope admin:write", call[1])
	}
	// It is no key of its own tenant's data either.
	assert.Equal(t, http.StatusForbidden, e.do("GET", "/v1/users", ro.Key, nil).code)

	// A key of the admin tenant without an admin scope is a plain tenant key.
	own := e.newScopedKey("/v1/keys", e.keyA, "users:read")
	assert.Equal(t, http.StatusOK, e.do("GET", "/v1/users", own.Key, nil).code)
	r = e.do("GET", tenants, own.Key, nil)
	require.Equal(t, http.StatusForbidden, r.code)
	assert.Contains(t, problemDetail(t, r), "needs the scope admin:read")

	// The admin scopes on a key of a tenant that is no admin open nothing.
	notAdmin := e.newScopedKey(keysB, e.keyA, "admin:write")
	r = e.do("GET", tenants, notAdmin.Key, nil)
	require.Equal(t, http.StatusForbidden, r.code)
	assert.Contains(t, problemDetail(t, r), "an API key of the instance admin tenant")
}
