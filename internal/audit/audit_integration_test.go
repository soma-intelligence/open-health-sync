// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/db"
)

func TestRecordListAppendOnlyPrune(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	defer pool.Close()

	tenantA, tenantB, user := uuid.New(), uuid.New(), uuid.New()
	ev := func(tenant uuid.UUID, u *uuid.UUID, action string) audit.Event {
		return audit.Event{Tenant: tenant, User: u, ActorType: audit.APIKey, ActorID: "k1", Action: action,
			Method: "GET", Route: "GET /v1/users/{id}/samples", Status: 200, RequestID: "r1"}
	}
	require.NoError(t, audit.Record(ctx, pool, ev(tenantA, &user, audit.DataRead)))
	require.NoError(t, audit.Record(ctx, pool, ev(tenantA, nil, audit.ConfigRead)))
	require.NoError(t, audit.Record(ctx, pool, ev(tenantA, &user, audit.DataExport)))
	require.NoError(t, audit.Record(ctx, pool, audit.Event{Tenant: tenantA, User: &user, ActorType: audit.System,
		ActorID: "deletion worker", Action: audit.UserErased, Detail: map[string]any{"job_id": "j"}}))
	require.NoError(t, audit.Record(ctx, pool, ev(tenantB, &user, audit.DataRead)))
	require.Error(t, audit.Record(ctx, pool, audit.Event{Tenant: tenantA}), "an event needs an action")

	all, next, err := audit.List(ctx, pool, tenantA, audit.Filter{})
	require.NoError(t, err)
	require.Len(t, all, 4, "the other tenant's event stays out")
	assert.Zero(t, next)
	assert.Equal(t, audit.UserErased, all[0].Action, "newest first")
	assert.Equal(t, map[string]any{"job_id": "j"}, all[0].Detail)
	assert.Nil(t, all[0].Status)
	assert.Equal(t, 200, *all[1].Status)
	assert.Equal(t, audit.Actor{Type: audit.APIKey, ID: "k1"}, all[1].Actor)

	page, next, err := audit.List(ctx, pool, tenantA, audit.Filter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.NotZero(t, next)
	rest, next2, err := audit.List(ctx, pool, tenantA, audit.Filter{Limit: 2, Before: next})
	require.NoError(t, err)
	assert.Len(t, rest, 2)
	assert.Zero(t, next2)

	data, _, err := audit.List(ctx, pool, tenantA, audit.Filter{Action: "data."})
	require.NoError(t, err)
	assert.Len(t, data, 2)
	exp, _, err := audit.List(ctx, pool, tenantA, audit.Filter{Action: audit.DataExport, User: &user})
	require.NoError(t, err)
	assert.Len(t, exp, 1)
	none, _, err := audit.List(ctx, pool, tenantA, audit.Filter{Until: time.Now().Add(-time.Hour)})
	require.NoError(t, err)
	assert.Empty(t, none)

	// Append only: no update, delete or truncate, even for the owner.
	for _, q := range []string{
		`UPDATE ohs_core.audit_events SET action = 'x'`,
		`DELETE FROM ohs_core.audit_events`,
		`TRUNCATE ohs_core.audit_events`,
	} {
		_, err := pool.Exec(ctx, q)
		require.Error(t, err, q)
		assert.Contains(t, err.Error(), "append only", q)
	}

	// Prune removes only what is older than the cutoff.
	n, err := audit.Prune(ctx, pool, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = audit.Prune(ctx, pool, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(5), n)
	// The prune setting does not leak out of its transaction.
	require.NoError(t, audit.Record(ctx, pool, ev(tenantA, &user, audit.DataRead)))
	_, err = pool.Exec(ctx, `DELETE FROM ohs_core.audit_events`)
	require.Error(t, err)
}

// A NUL or invalid UTF-8 in a value the event names (a path value a caller
// chose) is cleaned, never a reason to drop the event: Postgres refuses
// \u0000 in jsonb.
func TestRecordCleansText(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	defer pool.Close()

	tenant := uuid.New()
	require.NoError(t, audit.Record(ctx, pool, audit.Event{Tenant: tenant, ActorType: audit.APIKey, ActorID: "k\x001",
		Action: audit.ConnectionSync, Method: "POST", Route: "POST /v1/users/{id}/connections/{provider}/sync",
		Status: 404, RequestID: "r\xff", Detail: map[string]any{"provider": "oura\x00", "bad\xffname": []string{"a\x00"}}}))
	got, _, err := audit.List(ctx, pool, tenant, audit.Filter{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, map[string]any{"provider": "oura", "bad�name": []any{"a"}}, got[0].Detail)
	assert.Equal(t, "k1", got[0].Actor.ID)
}
