// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package tenant_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// Rotation: a second key works beside the first, then the first is revoked
// by its prefix (or the whole key) and stops working.
func TestKeyRotation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, first, err := s.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	other, _, err := s.CreateTenant(ctx, "Other")
	require.NoError(t, err)

	second, err := s.CreateKey(ctx, ten.ID)
	require.NoError(t, err)
	require.Regexp(t, `^ohs_sk_[0-9A-Za-z]{43}$`, second)
	_, err = s.CreateKey(ctx, uuid.New())
	require.ErrorIs(t, err, tenant.ErrNotFound)
	_, err = s.ListKeys(ctx, uuid.New())
	require.ErrorIs(t, err, tenant.ErrNotFound)

	keys, err := s.ListKeys(ctx, ten.ID)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	require.Equal(t, first[:12], keys[0].Prefix, "oldest first")
	require.Nil(t, keys[0].RevokedAt)

	// Another tenant cannot revoke it; an unknown prefix is not found.
	require.ErrorIs(t, s.RevokeKeyByPrefix(ctx, other.ID, first[:12]), tenant.ErrNotFound)
	require.ErrorIs(t, s.RevokeKeyByPrefix(ctx, ten.ID, "ohs_sk_zzzzz"), tenant.ErrNotFound)

	require.NoError(t, s.RevokeKeyByPrefix(ctx, ten.ID, first[:12]))
	_, err = s.LookupKey(ctx, auth.Hash(first))
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
	p, err := s.LookupKey(ctx, auth.Hash(second))
	require.NoError(t, err)
	require.Equal(t, ten.ID, p.TenantID)

	// The whole key works too, and revoking twice is fine.
	require.NoError(t, s.RevokeKeyByPrefix(ctx, ten.ID, second))
	require.NoError(t, s.RevokeKeyByPrefix(ctx, ten.ID, second))
	_, err = s.LookupKey(ctx, auth.Hash(second))
	require.ErrorIs(t, err, auth.ErrUnauthenticated)

	keys, err = s.ListKeys(ctx, ten.ID)
	require.NoError(t, err)
	require.NotNil(t, keys[0].RevokedAt)
	require.NotNil(t, keys[1].RevokedAt)

	tenants, err := s.ListTenants(ctx)
	require.NoError(t, err)
	require.Len(t, tenants, 2)
	require.Equal(t, "Acme", tenants[0].Name)
}

// A key holds the scopes it was created with, "*" without any; the lookup
// hands them to the middleware, for the key and for the tokens it minted.
func TestKeyScopes(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, first, err := s.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	p, err := s.LookupKey(ctx, auth.Hash(first))
	require.NoError(t, err)
	require.Equal(t, []string{"*"}, p.KeyScopes, "a tenant's first key does everything")

	_, plain, err := s.CreateKeyInfo(ctx, ten.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"*"}, plain.Scopes)

	full, k, err := s.CreateKeyWith(ctx, ten.ID, tenant.KeyOptions{Scopes: []string{"data:read", "users:read", "data:read"}})
	require.NoError(t, err)
	require.Equal(t, []string{"users:read", "data:read"}, k.Scopes, "no duplicate, in the order of the list of scopes")
	p, err = s.LookupKey(ctx, auth.Hash(full))
	require.NoError(t, err)
	require.Equal(t, []string{"users:read", "data:read"}, p.KeyScopes)
	require.True(t, p.Can("data:read"))
	require.False(t, p.Can("data:write"))

	_, star, err := s.CreateKeyWith(ctx, ten.ID, tenant.KeyOptions{Scopes: []string{"data:read", "*"}})
	require.NoError(t, err)
	require.Equal(t, []string{"*"}, star.Scopes)

	_, _, err = s.CreateKeyWith(ctx, ten.ID, tenant.KeyOptions{Scopes: []string{"data:read", "everything"}})
	require.ErrorIs(t, err, tenant.ErrInvalidKeyScopes)

	keys, err := s.ListKeys(ctx, ten.ID)
	require.NoError(t, err)
	require.Len(t, keys, 4)
	require.Equal(t, []string{"*"}, keys[0].Scopes)
	require.Equal(t, []string{"users:read", "data:read"}, keys[2].Scopes)

	// A token is held to the key that minted it; one tied to no key is not.
	u, err := s.CreateUser(ctx, ten.ID, "u-1")
	require.NoError(t, err)
	tok, _, err := s.MintUserTokenBy(ctx, ten.ID, u.ID, []string{"read"}, 0, &k.ID)
	require.NoError(t, err)
	p, err = s.LookupKey(ctx, auth.Hash(tok))
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, p.Scopes)
	require.Equal(t, []string{"users:read", "data:read"}, p.KeyScopes)
	free, _, err := s.MintUserToken(ctx, ten.ID, u.ID, []string{"read"}, 0)
	require.NoError(t, err)
	p, err = s.LookupKey(ctx, auth.Hash(free))
	require.NoError(t, err)
	require.Nil(t, p.KeyScopes)
	require.True(t, p.Can("data:write"))

	// The scopes of the key are read on each lookup.
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.api_keys SET scopes = '{users:read}' WHERE id = $1`, k.ID)
	require.NoError(t, err)
	p, err = s.LookupKey(ctx, auth.Hash(tok))
	require.NoError(t, err)
	require.Equal(t, []string{"users:read"}, p.KeyScopes)

	// A key always holds a scope.
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.api_keys SET scopes = '{}' WHERE id = $1`, k.ID)
	require.Error(t, err)
}
