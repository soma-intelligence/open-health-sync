// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeKeyScopes(t *testing.T) {
	for _, tc := range []struct {
		in, want []string
	}{
		{nil, []string{"*"}},
		{[]string{}, []string{"*"}},
		{[]string{"*"}, []string{"*"}},
		{[]string{"data:read", "*"}, []string{"*"}},
		{[]string{"data:read", "users:read", "data:read"}, []string{"users:read", "data:read"}},
		{[]string{"keys:write"}, []string{"keys:write"}},
	} {
		got, err := NormalizeKeyScopes(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, "%v", tc.in)
	}
	for _, in := range [][]string{{"data"}, {"data:read", ""}, {"DATA:READ"}, {"data:*"}, {"users:delete"}} {
		_, err := NormalizeKeyScopes(in)
		assert.ErrorIs(t, err, ErrInvalidKeyScopes, "%v", in)
	}
	// The list is its own normal form.
	all, err := NormalizeKeyScopes(KeyScopes[1:])
	require.NoError(t, err)
	assert.Equal(t, KeyScopes[1:], all)
}

func TestGrants(t *testing.T) {
	assert.True(t, Grants(nil, ScopeAdminWrite), "no scoped key behind the credential")
	assert.True(t, Grants([]string{ScopeAll}, ScopeAdminWrite))
	assert.True(t, Grants([]string{ScopeDataRead}, ScopeDataRead))
	assert.True(t, Grants([]string{ScopeUsersWrite}, ScopeUsersRead), "write grants the read of the same resource")
	assert.False(t, Grants([]string{ScopeUsersRead}, ScopeUsersWrite))
	assert.False(t, Grants([]string{ScopeDataWrite}, ScopeDataDelete))
	assert.False(t, Grants([]string{ScopeDataWrite}, ScopeUsersRead))
	assert.False(t, Grants([]string{}, ScopeTenantRead), "an empty list grants nothing")
	assert.False(t, Grants([]string{ScopeDataRead, ScopeUsersWrite, ScopeKeysWrite}, ScopeAll),
		"only * grants what has no scope of its own")

	assert.Equal(t, ScopeDataWrite, TokenNeeds(UserUpload))
	assert.Equal(t, ScopeConnectionsWrite, TokenNeeds(UserConnect))
	assert.Equal(t, ScopeDataRead, TokenNeeds(UserRead))
	assert.Empty(t, TokenNeeds("nope"))
}

// The middleware asks the request's Slot which key scope the route needs
// and refuses a credential without it: 403 naming the scope, the caller
// still known to the Slot (the audit log). A user token is held to the
// scopes of the key that minted it.
func TestMiddlewareRequiresKeyScope(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	full, fullHash, _ := NewSecret(PrefixSecretKey)
	narrow, narrowHash, _ := NewSecret(PrefixSecretKey)
	writer, writerHash, _ := NewSecret(PrefixSecretKey)
	token, tokenHash, _ := NewSecret(PrefixUserToken)
	free, freeHash, _ := NewSecret(PrefixUserToken)
	lookup := lookupMap(map[string]Principal{
		fullHash:   {TenantID: tenant, KeyID: "full", KeyScopes: []string{ScopeAll}},
		narrowHash: {TenantID: tenant, KeyID: "narrow", KeyScopes: []string{ScopeUsersRead}},
		writerHash: {TenantID: tenant, KeyID: "writer", KeyScopes: []string{ScopeDataWrite}},
		tokenHash:  {TenantID: tenant, UserID: &user, Scopes: []string{"read"}, KeyID: "tok", KeyScopes: []string{ScopeUsersRead}},
		freeHash:   {TenantID: tenant, UserID: &user, Scopes: []string{"read"}, KeyID: "free"},
	})
	h := MiddlewareAny(lookup, TenantKey, UserRead)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func(secret, scope string) (*httptest.ResponseRecorder, *Slot) {
		r := httptest.NewRequest("GET", "/x", nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		ctx, slot := WithSlot(r.Context())
		if scope != "" {
			slot.RequireScope(func(*http.Request) string { return scope })
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r.WithContext(ctx))
		return rec, slot
	}

	for name, secret := range map[string]string{"full key": full, "narrow key": narrow, "token": token} {
		rec, slot := call(secret, "")
		assert.Equal(t, http.StatusNoContent, rec.Code, "%s: no scope asked for", name)
		assert.Empty(t, slot.MissingScope())
	}
	for name, secret := range map[string]string{"full key": full, "data:write key": writer, "token tied to no key": free} {
		rec, slot := call(secret, ScopeDataRead)
		assert.Equal(t, http.StatusNoContent, rec.Code, name)
		assert.Empty(t, slot.MissingScope())
	}

	rec, slot := call(narrow, ScopeDataRead)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	var body struct{ Detail string }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "this endpoint needs the scope data:read, which this API key does not hold", body.Detail)
	assert.Equal(t, ScopeDataRead, slot.MissingScope())
	p, ok := slot.Principal()
	require.True(t, ok, "the refused caller is known, for the audit log")
	assert.Equal(t, "narrow", p.KeyID)

	rec, slot = call(token, ScopeDataRead)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body.Detail, "the API key that minted this user token does not hold")
	assert.Equal(t, ScopeDataRead, slot.MissingScope())

	// A route with no scope of its own needs every scope.
	rec, _ = call(narrow, ScopeAll)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec, _ = call(full, ScopeAll)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}
