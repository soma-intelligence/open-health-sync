// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package tenantkeys_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/tenantkeys"
)

func TestPGStore(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := tenant.New(pool)
	a, _, err := store.CreateTenant(ctx, "A")
	require.NoError(t, err)
	b, _, err := store.CreateTenant(ctx, "B")
	require.NoError(t, err)
	box, err := crypto.NewBox(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	var retired []uuid.UUID
	retire := func(_ context.Context, _ pgx.Tx, tenant uuid.UUID, provider string, id uuid.UUID) (int, error) {
		require.Equal(t, a.ID, tenant)
		require.Equal(t, "oura", provider)
		retired = append(retired, id)
		return 2, nil
	}
	pg := tenantkeys.NewPG(pool, box, retire)
	same := func(k config.ProviderKeys) func(config.ProviderKeys) bool {
		return func(old config.ProviderKeys) bool { return tenantkeys.SameApp(old, k) }
	}

	k1 := config.ProviderKeys{"CLIENT_ID": "a-app", "CLIENT_SECRET": "a-secret"}
	ev := audit.Event{ActorType: audit.Admin, ActorID: "api_key:k1", Action: audit.TenantProviderKeys,
		Detail: map[string]any{"provider": "oura", "set": true}}
	put, n, err := pg.Put(ctx, tenantkeys.Keys{Tenant: a.ID, Provider: "oura", Keys: k1, UpdatedBy: "k1"}, same(k1), &ev)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, k1, put.Keys)
	assert.Equal(t, "k1", put.CreatedBy)
	assert.Equal(t, "k1", put.UpdatedBy)
	assert.WithinDuration(t, time.Now(), put.UpdatedAt, time.Minute)
	assert.Equal(t, time.UTC, put.UpdatedAt.Location())
	assert.NotEqual(t, uuid.Nil, put.ID)

	got, found, err := pg.Get(ctx, a.ID, "oura")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, put, got)
	_, found, err = pg.Get(ctx, b.ID, "oura")
	require.NoError(t, err)
	assert.False(t, found)

	// Sealed, and bound to its row.
	var sealed []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT keys_sealed FROM ohs_core.tenant_provider_keys WHERE tenant_id = $1`, a.ID).Scan(&sealed))
	assert.NotContains(t, string(sealed), "a-secret")
	_, _, err = pg.Put(ctx, tenantkeys.Keys{Tenant: b.ID, Provider: "oura", Keys: k1}, same(k1), nil)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE ohs_core.tenant_provider_keys SET keys_sealed = $1 WHERE tenant_id = $2`, sealed, b.ID)
	require.NoError(t, err)
	moved, _, err := pg.Get(ctx, b.ID, "oura")
	require.NoError(t, err)
	assert.Error(t, moved.OpenErr, "a value moved to another row does not open")
	assert.Nil(t, moved.Keys)

	// A secret rotated keeps the id; another client id retires it.
	k2 := config.ProviderKeys{"CLIENT_ID": "a-app", "CLIENT_SECRET": "a-secret-2"}
	rot, n, err := pg.Put(ctx, tenantkeys.Keys{Tenant: a.ID, Provider: "oura", Keys: k2, UpdatedBy: "k2"}, same(k2), nil)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, put.ID, rot.ID)
	assert.Equal(t, "k1", rot.CreatedBy)
	assert.Equal(t, "k2", rot.UpdatedBy)
	assert.Empty(t, retired)
	k3 := config.ProviderKeys{"CLIENT_ID": "a-app-2", "CLIENT_SECRET": "a-secret-2"}
	other, n, err := pg.Put(ctx, tenantkeys.Keys{Tenant: a.ID, Provider: "oura", Keys: k3}, same(k3), nil)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.NotEqual(t, put.ID, other.ID)
	assert.Equal(t, []uuid.UUID{put.ID}, retired)

	list, err := pg.List(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 2)

	_, _, err = pg.Put(ctx, tenantkeys.Keys{Tenant: uuid.New(), Provider: "oura", Keys: k1}, same(k1), nil)
	assert.ErrorIs(t, err, tenantkeys.ErrUnknownTenant)

	// Removed, with its connections retired and the event in the same
	// transaction.
	del := audit.Event{ActorType: audit.Admin, ActorID: "api_key:k1", Action: audit.TenantProviderKeys,
		Detail: map[string]any{"provider": "oura", "set": false}}
	prev, n, found, err := pg.Delete(ctx, a.ID, "oura", &del)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 2, n)
	assert.Equal(t, other.ID, prev.ID)
	assert.Equal(t, []uuid.UUID{put.ID, other.ID}, retired)
	_, _, found, err = pg.Delete(ctx, a.ID, "oura", &del)
	require.NoError(t, err)
	assert.False(t, found)

	var details []map[string]any
	rows, err := pool.Query(ctx, `SELECT detail FROM ohs_core.audit_events WHERE tenant_id = $1 AND action = $2 ORDER BY id`,
		a.ID, audit.TenantProviderKeys)
	require.NoError(t, err)
	for rows.Next() {
		var d map[string]any
		require.NoError(t, rows.Scan(&d))
		details = append(details, d)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []map[string]any{
		{"provider": "oura", "set": true, "reauth_required": float64(0)},
		{"provider": "oura", "set": false, "reauth_required": float64(2)},
	}, details)

	// A failed retire changes nothing.
	failing := tenantkeys.NewPG(pool, box, func(context.Context, pgx.Tx, uuid.UUID, string, uuid.UUID) (int, error) {
		return 0, errors.New("boom")
	})
	_, _, _, err = failing.Delete(ctx, b.ID, "oura", nil)
	assert.Error(t, err)
	_, found, err = pg.Get(ctx, b.ID, "oura")
	require.NoError(t, err)
	assert.True(t, found)
}
