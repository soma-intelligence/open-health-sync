// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package providerset_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/providerset"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	_, err := rand.Read(k)
	require.NoError(t, err)
	return k
}

func TestPGStore(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	box, err := crypto.NewBox(newKey(t))
	require.NoError(t, err)
	s := providerset.NewPG(pool, box)

	list, err := s.List(ctx)
	require.NoError(t, err)
	require.Empty(t, list)

	keys := config.ProviderKeys{"CLIENT_ID": "id-1", "CLIENT_SECRET": "a-secret-value"}
	require.NoError(t, s.Put(ctx, providerset.Settings{Slug: "oura", Keys: keys, Enabled: true, UpdatedBy: "k1"}))
	require.NoError(t, s.Put(ctx, providerset.Settings{Slug: "whoop", Enabled: false, UpdatedBy: "k1"}))

	// The keys are sealed: no value is readable in the table.
	var sealed []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT keys_sealed FROM ohs_core.provider_settings WHERE slug = 'oura'`).Scan(&sealed))
	require.False(t, bytes.Contains(sealed, []byte("a-secret-value")))
	require.False(t, bytes.Contains(sealed, []byte("id-1")))
	var none []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT keys_sealed FROM ohs_core.provider_settings WHERE slug = 'whoop'`).Scan(&none))
	require.Nil(t, none, "no keys saved, none stored")

	list, err = s.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "oura", list[0].Slug)
	require.Equal(t, keys, list[0].Keys)
	require.True(t, list[0].Enabled)
	require.Equal(t, "k1", list[0].UpdatedBy)
	require.False(t, list[0].UpdatedAt.IsZero())
	require.Nil(t, list[1].Keys)
	require.False(t, list[1].Enabled)

	// Put replaces the row.
	before := list[0].UpdatedAt
	require.NoError(t, s.Put(ctx, providerset.Settings{Slug: "oura", Keys: config.ProviderKeys{"CLIENT_ID": "id-2"}, Enabled: false, UpdatedBy: "k2"}))
	list, err = s.List(ctx)
	require.NoError(t, err)
	require.Equal(t, config.ProviderKeys{"CLIENT_ID": "id-2"}, list[0].Keys)
	require.False(t, list[0].Enabled)
	require.Equal(t, "k2", list[0].UpdatedBy)
	require.True(t, list[0].UpdatedAt.After(before))

	// Sealed keys are bound to their slug: moved to another row, they do
	// not open, and only that row says so.
	_, err = pool.Exec(ctx, `UPDATE ohs_core.provider_settings SET keys_sealed =
		(SELECT keys_sealed FROM ohs_core.provider_settings WHERE slug = 'oura') WHERE slug = 'whoop'`)
	require.NoError(t, err)
	list, err = s.List(ctx)
	require.NoError(t, err)
	require.NoError(t, list[0].OpenErr)
	require.Error(t, list[1].OpenErr)
	require.Nil(t, list[1].Keys)

	// Under another key they do not open either.
	other, err := crypto.NewBox(newKey(t))
	require.NoError(t, err)
	list, err = providerset.NewPG(pool, other).List(ctx)
	require.NoError(t, err)
	require.ErrorContains(t, list[0].OpenErr, "OHS_PREVIOUS_ENCRYPTION_KEYS")

	found, err := s.Delete(ctx, "oura")
	require.NoError(t, err)
	require.True(t, found)
	found, err = s.Delete(ctx, "oura")
	require.NoError(t, err)
	require.False(t, found)

	// The slug check of the table.
	require.Error(t, s.Put(ctx, providerset.Settings{Slug: "Not A Slug"}))
}
