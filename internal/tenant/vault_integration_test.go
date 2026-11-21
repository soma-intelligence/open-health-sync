// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package tenant_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

func newVault(t *testing.T, s *tenant.Store, canOpen bool) *vault.Vault {
	t.Helper()
	p, err := vault.OpenProvider(context.Background(), config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{7}, 32)})
	require.NoError(t, err)
	return vault.New(s.Pool(), p, vault.Options{CanOpen: canOpen})
}

// A user is created with its keys, in the same transaction.
func TestCreateUserGivesKeys(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	v := newVault(t, s, true)
	s = s.WithVault(v)
	ten, _, err := s.CreateTenant(ctx, "Acme")
	require.NoError(t, err)

	u, err := s.CreateUser(ctx, ten.ID, "a")
	require.NoError(t, err)
	_, err = v.ForUser(ctx, ten.ID, u.ID)
	require.NoError(t, err)
	_, version, err := v.PublicKey(ctx, s.Pool(), ten.ID, u.ID)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), version)
	var created int
	require.NoError(t, s.Pool().QueryRow(ctx, `SELECT count(*) FROM ohs_core.audit_events
		WHERE tenant_id = $1 AND user_id = $2 AND action = 'vault.key_created'`, ten.ID, u.ID).Scan(&created))
	assert.Equal(t, 1, created)

	// EnsureUser creates through CreateUser: the same holds.
	e, made, err := s.EnsureUser(ctx, ten.ID, "b")
	require.NoError(t, err)
	require.True(t, made)
	_, err = v.ForUser(ctx, ten.ID, e.ID)
	require.NoError(t, err)

	// A duplicate ref is still a conflict, and leaves no keys behind.
	_, err = s.CreateUser(ctx, ten.ID, "a")
	require.ErrorIs(t, err, tenant.ErrConflict)
	var keys int
	require.NoError(t, s.Pool().QueryRow(ctx, `SELECT count(*) FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id IS NOT NULL`,
		ten.ID).Scan(&keys))
	assert.Equal(t, 2, keys)
}

// A user whose keys cannot be made is not created: no user is ever stored
// without keys by a store that has a vault.
func TestCreateUserWithoutKeysIsRefused(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	s = s.WithVault(newVault(t, s, false))
	ten, _, err := s.CreateTenant(ctx, "Acme")
	require.NoError(t, err)

	_, err = s.CreateUser(ctx, ten.ID, "a")
	require.ErrorIs(t, err, vault.ErrCannotOpen)
	var n int
	require.NoError(t, s.Pool().QueryRow(ctx, `SELECT count(*) FROM ohs_core.end_users WHERE tenant_id = $1`, ten.ID).Scan(&n))
	assert.Zero(t, n)
}
