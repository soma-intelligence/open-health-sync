// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package tenantproviders_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/tenantproviders"
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
	pg := tenantproviders.NewPG(pool)

	off, err := pg.Off(ctx)
	require.NoError(t, err)
	assert.Empty(t, off)

	ev := audit.Event{ActorType: audit.Admin, ActorID: "api_key:k1", Action: audit.TenantProvider,
		Detail: map[string]any{"provider": "oura", "enabled": false}}
	st, err := pg.Put(ctx, tenantproviders.Setting{Tenant: a.ID, Provider: "oura", Enabled: false, UpdatedBy: "k1"}, &ev)
	require.NoError(t, err)
	assert.Equal(t, a.ID, st.Tenant)
	assert.False(t, st.Enabled)
	assert.Equal(t, "k1", st.UpdatedBy)
	assert.WithinDuration(t, time.Now(), st.UpdatedAt, time.Minute)
	assert.Equal(t, time.UTC, st.UpdatedAt.Location())
	_, err = pg.Put(ctx, tenantproviders.Setting{Tenant: b.ID, Provider: "whoop", Enabled: true}, nil)
	require.NoError(t, err)

	off, err = pg.Off(ctx)
	require.NoError(t, err)
	assert.Equal(t, []tenantproviders.Key{{Tenant: a.ID, Provider: "oura"}}, off)

	// Turned back on: the row stays, with its new time, and is no longer off.
	again, err := pg.Put(ctx, tenantproviders.Setting{Tenant: a.ID, Provider: "oura", Enabled: true, UpdatedBy: "k2"}, nil)
	require.NoError(t, err)
	assert.True(t, again.Enabled)
	assert.True(t, again.UpdatedAt.After(st.UpdatedAt))
	off, err = pg.Off(ctx)
	require.NoError(t, err)
	assert.Empty(t, off)
	rows, err := pg.List(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "k2", rows[0].UpdatedBy)

	// The event went to the tenant, with the change.
	entries, _, err := audit.List(ctx, pool, a.ID, audit.Filter{Action: audit.TenantProvider})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, audit.Actor{Type: audit.Admin, ID: "api_key:k1"}, entries[0].Actor)
	assert.Equal(t, map[string]any{"provider": "oura", "enabled": false}, entries[0].Detail)

	// An unknown tenant, a slug the table refuses.
	_, err = pg.Put(ctx, tenantproviders.Setting{Tenant: uuid.New(), Provider: "oura"}, nil)
	assert.ErrorIs(t, err, tenantproviders.ErrUnknownTenant)
	_, err = pg.Put(ctx, tenantproviders.Setting{Tenant: a.ID, Provider: "Not A Slug"}, nil)
	assert.Error(t, err)

	// A failed audit write changes nothing.
	_, err = pg.Put(ctx, tenantproviders.Setting{Tenant: b.ID, Provider: "polar"}, &audit.Event{})
	require.Error(t, err)
	rows, err = pg.List(ctx, b.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1, "no polar row")
}
