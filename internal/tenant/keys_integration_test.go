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
