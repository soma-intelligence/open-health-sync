// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package demo_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/demo"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// A demo seed through a store with a vault gives the user keys and stores
// sealed rows only; seeding the same user again, over its sealed rows, adds
// nothing.
func TestSeedWithVaultSeals(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	p, err := vault.OpenProvider(ctx, config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{9}, 32)})
	require.NoError(t, err)
	store := tenant.New(pool).WithVault(vault.New(pool, p, vault.Options{CanOpen: true}))
	tn, _, err := store.CreateTenant(ctx, "Demo")
	require.NoError(t, err)

	o := demo.SeedOptions{Days: 2, Seed: 1, ExternalRef: "demo-1"}
	res, err := demo.Seed(ctx, store, tn.ID, o)
	require.NoError(t, err)
	require.Positive(t, res.Written)
	for name := range vault.Schema {
		var clear int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM ohs_data.`+name+`
			WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NULL`, tn.ID, res.User.ID).Scan(&clear))
		require.Zero(t, clear, name)
	}

	again, err := demo.Seed(ctx, store, tn.ID, o)
	require.NoError(t, err)
	require.Zero(t, again.Written)
}
