// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package tenant_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// auditedEvent is what the admin API passes: the target tenant and the key
// details are added by the store.
func auditedEvent(action string) audit.Event {
	return audit.Event{ActorType: audit.Admin, ActorID: "api_key:admin-key", Action: action,
		Method: "POST", Route: "POST /v1/admin/tenants", Status: 201, RequestID: "req-1",
		Detail: map[string]any{"admin_tenant_id": "admin-tenant"}}
}

type recorded struct {
	actor, keyID, keyPrefix, admin string
}

func events(t *testing.T, s *tenant.Store, tenantID uuid.UUID, action string) []recorded {
	t.Helper()
	rows, err := s.Pool().Query(context.Background(),
		`SELECT actor_type || ':' || actor_id, coalesce(detail->>'key_id', ''), coalesce(detail->>'key_prefix', ''),
		        coalesce(detail->>'admin_tenant_id', '')
		 FROM ohs_core.audit_events WHERE tenant_id = $1 AND action = $2 ORDER BY id`, tenantID, action)
	require.NoError(t, err)
	defer rows.Close()
	var out []recorded
	for rows.Next() {
		var r recorded
		require.NoError(t, rows.Scan(&r.actor, &r.keyID, &r.keyPrefix, &r.admin))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// A tenant, a key and a revocation made for the admin API are recorded on the
// tenant they change, in the same transaction: an event that cannot be
// written leaves nothing changed.
func TestAuditedTenantAndKeys(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	_, _, err := s.CreateTenant(ctx, "Admin")
	require.NoError(t, err)

	ten, full, first, err := s.CreateTenantAudited(ctx, "Team", auditedEvent(audit.TenantCreate))
	require.NoError(t, err)
	assert.False(t, ten.Admin, "only the first tenant of an instance is an admin")
	assert.Equal(t, full[:12], first.Prefix)
	assert.NotEqual(t, uuid.Nil, first.ID)
	assert.False(t, first.CreatedAt.IsZero())
	p, err := s.LookupKey(ctx, auth.Hash(full))
	require.NoError(t, err)
	assert.Equal(t, ten.ID, p.TenantID)
	assert.Equal(t, first.ID.String(), p.KeyID)
	assert.Equal(t, []recorded{{"admin:api_key:admin-key", first.ID.String(), full[:12] + "...", "admin-tenant"}},
		events(t, s, ten.ID, audit.TenantCreate))

	second, k, err := s.CreateKeyAudited(ctx, ten.ID, auditedEvent(audit.KeyCreate))
	require.NoError(t, err)
	assert.Equal(t, second[:12], k.Prefix)
	assert.Equal(t, []recorded{{"admin:api_key:admin-key", k.ID.String(), second[:12] + "...", "admin-tenant"}},
		events(t, s, ten.ID, audit.KeyCreate))
	_, _, err = s.CreateKeyAudited(ctx, uuid.New(), auditedEvent(audit.KeyCreate))
	require.ErrorIs(t, err, tenant.ErrNotFound)

	revoked, err := s.RevokeKeyAudited(ctx, ten.ID, k.ID, auditedEvent(audit.KeyRevoke))
	require.NoError(t, err)
	assert.Equal(t, k.ID, revoked.ID)
	assert.NotNil(t, revoked.RevokedAt)
	_, err = s.LookupKey(ctx, auth.Hash(second))
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
	_, err = s.RevokeKeyAudited(ctx, ten.ID, k.ID, auditedEvent(audit.KeyRevoke))
	require.NoError(t, err, "revoking a revoked key succeeds")
	assert.Len(t, events(t, s, ten.ID, audit.KeyRevoke), 2)
	_, err = s.RevokeKeyAudited(ctx, uuid.New(), first.ID, auditedEvent(audit.KeyRevoke))
	require.ErrorIs(t, err, tenant.ErrNotFound, "a key of another tenant")
	_, err = s.RevokeKeyAudited(ctx, ten.ID, uuid.New(), auditedEvent(audit.KeyRevoke))
	require.ErrorIs(t, err, tenant.ErrNotFound)

	// An event the log refuses (no action) rolls the change back.
	var n int
	_, _, _, err = s.CreateTenantAudited(ctx, "Lost", audit.Event{ActorType: audit.Admin})
	require.Error(t, err)
	require.NoError(t, s.Pool().QueryRow(ctx, `SELECT count(*) FROM ohs_core.tenants WHERE name = 'Lost'`).Scan(&n))
	assert.Zero(t, n)
	_, _, err = s.CreateKeyAudited(ctx, ten.ID, audit.Event{ActorType: audit.Admin})
	require.Error(t, err)
	keys, err := s.ListKeys(ctx, ten.ID)
	require.NoError(t, err)
	assert.Len(t, keys, 2)
	_, err = s.RevokeKeyAudited(ctx, ten.ID, first.ID, audit.Event{ActorType: audit.Admin})
	require.Error(t, err)
	_, err = s.LookupKey(ctx, auth.Hash(full))
	require.NoError(t, err, "still active")
}

// Tenants are paged oldest first with their count of active keys, like users.
func TestListTenantsPage(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	var ids []uuid.UUID
	for i := range 5 {
		ten, _, err := s.CreateTenant(ctx, fmt.Sprintf("t-%d", i))
		require.NoError(t, err)
		ids = append(ids, ten.ID)
	}
	_, err := s.CreateKey(ctx, ids[1])
	require.NoError(t, err)
	keys, err := s.ListKeys(ctx, ids[2])
	require.NoError(t, err)
	require.NoError(t, s.RevokeKeyByID(ctx, ids[2], keys[0].ID))

	var got []tenant.TenantSummary
	cursor := ""
	for range 5 {
		page, next, err := s.ListTenantsPage(ctx, cursor, 2)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page), 2)
		got = append(got, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	require.Len(t, got, 5)
	for i, ten := range got {
		assert.Equal(t, ids[i], ten.ID, "oldest first")
	}
	assert.True(t, got[0].Admin)
	assert.Equal(t, []int{1, 2, 0, 1, 1}, []int{got[0].ActiveKeys, got[1].ActiveKeys, got[2].ActiveKeys,
		got[3].ActiveKeys, got[4].ActiveKeys})

	all, next, err := s.ListTenantsPage(ctx, "", 0)
	require.NoError(t, err)
	assert.Len(t, all, 5, "0 is the default limit")
	assert.Empty(t, next)
	_, _, err = s.ListTenantsPage(ctx, "not a cursor", 2)
	require.ErrorIs(t, err, tenant.ErrInvalidCursor)

	one, err := s.GetTenantSummary(ctx, ids[1])
	require.NoError(t, err)
	assert.Equal(t, "t-1", one.Name)
	assert.Equal(t, 2, one.ActiveKeys)
	_, err = s.GetTenantSummary(ctx, uuid.New())
	require.ErrorIs(t, err, tenant.ErrNotFound)
}
