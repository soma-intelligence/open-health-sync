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

// The first tenant of an instance is its admin, whichever path creates it;
// later ones are not until SetAdmin says so. LookupKey carries the flag.
func TestFirstTenantIsAdmin(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	first, firstKey, err := s.EnsureTenant(ctx, "personal")
	require.NoError(t, err)
	require.True(t, first.Admin)
	second, secondKey, err := s.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	require.False(t, second.Admin)

	p, err := s.LookupKey(ctx, auth.Hash(firstKey))
	require.NoError(t, err)
	require.True(t, p.Admin)
	p, err = s.LookupKey(ctx, auth.Hash(secondKey))
	require.NoError(t, err)
	require.False(t, p.Admin)

	// EnsureTenant reports the flag of the tenant it finds.
	again, key, err := s.EnsureTenant(ctx, "personal")
	require.NoError(t, err)
	require.Empty(t, key)
	require.True(t, again.Admin)

	require.NoError(t, s.SetAdmin(ctx, second.ID, true))
	require.NoError(t, s.SetAdmin(ctx, first.ID, false))
	p, err = s.LookupKey(ctx, auth.Hash(secondKey))
	require.NoError(t, err)
	require.True(t, p.Admin)
	got, err := s.GetTenant(ctx, first.ID)
	require.NoError(t, err)
	require.False(t, got.Admin)
	require.Equal(t, "personal", got.Name)
	require.ErrorIs(t, s.SetAdmin(ctx, uuid.New(), true), tenant.ErrNotFound)
	_, err = s.GetTenant(ctx, uuid.New())
	require.ErrorIs(t, err, tenant.ErrNotFound)

	list, err := s.ListTenants(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.False(t, list[0].Admin)
	require.True(t, list[1].Admin)
}

func TestFirstRunTenantIsAdmin(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, key, err := s.CreateFirstTenant(ctx, "Clinic", nil)
	require.NoError(t, err)
	require.True(t, ten.Admin)
	p, err := s.LookupKey(ctx, auth.Hash(key))
	require.NoError(t, err)
	require.True(t, p.Admin)
}

// Keys by id: created with their description, revoked by id within their
// tenant only.
func TestKeysByID(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, first, err := s.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	other, _, err := s.CreateTenant(ctx, "Other")
	require.NoError(t, err)

	full, k, err := s.CreateKeyInfo(ctx, ten.ID)
	require.NoError(t, err)
	require.Equal(t, full[:12], k.Prefix)
	require.NotEqual(t, uuid.Nil, k.ID)
	require.False(t, k.CreatedAt.IsZero())
	_, _, err = s.CreateKeyInfo(ctx, uuid.New())
	require.ErrorIs(t, err, tenant.ErrNotFound)

	keys, err := s.ListKeys(ctx, ten.ID)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	require.Equal(t, k.ID, keys[1].ID)
	p, err := s.LookupKey(ctx, auth.Hash(first))
	require.NoError(t, err)
	require.Equal(t, keys[0].ID.String(), p.KeyID, "the audit log and the key list name a key alike")

	require.ErrorIs(t, s.RevokeKeyByID(ctx, other.ID, k.ID), tenant.ErrNotFound)
	require.ErrorIs(t, s.RevokeKeyByID(ctx, ten.ID, uuid.New()), tenant.ErrNotFound)
	require.NoError(t, s.RevokeKeyByID(ctx, ten.ID, k.ID))
	require.NoError(t, s.RevokeKeyByID(ctx, ten.ID, k.ID))
	_, err = s.LookupKey(ctx, auth.Hash(full))
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
}
