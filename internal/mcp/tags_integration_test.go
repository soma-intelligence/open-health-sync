// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package mcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/timetag"
)

func TestGetTags(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pa, err := e.store.LookupKey(ctx, auth.Hash(e.keyA))
	require.NoError(t, err)
	pb, err := e.store.LookupKey(ctx, auth.Hash(e.keyB))
	require.NoError(t, err)
	day := func(d int) time.Time { return time.Date(2026, 3, d, 0, 0, 0, 0, time.UTC) }
	end := day(5)
	for _, f := range []timetag.Fields{
		{Kind: "illness", StartAt: day(1), EndAt: &end, Note: ptr("invented note")},
		{Kind: "travel", StartAt: day(8), Timezone: ptr("Asia/Tokyo")},
		{Kind: "alcohol", StartAt: day(10), EndAt: ptr(day(11))},
	} {
		_, err := timetag.Create(ctx, e.pool, pa.TenantID, e.userA, f)
		require.NoError(t, err)
	}
	_, err = timetag.Create(ctx, e.pool, pb.TenantID, e.userB, timetag.Fields{Kind: "injury", StartAt: day(2)})
	require.NoError(t, err)

	cs := Connect(t, e.srv.URL+"/mcp", e.keyA)
	u := e.userA.String()
	r := get(t, cs, "get_tags", map[string]any{"user_id": u})
	assert.JSONEq(t, string(e.rest("/v1/users/"+u+"/tags", e.keyA)), string(r.Records))
	assert.Equal(t, 3, count(t, r))

	// The window keeps the overlapping tags, the ongoing one included.
	r = get(t, cs, "get_tags", map[string]any{"user_id": u, "start": "2026-03-05", "end": "2026-03-20"})
	assert.JSONEq(t, string(e.rest("/v1/users/"+u+"/tags?start=2026-03-05&end=2026-03-20", e.keyA)), string(r.Records))
	assert.Equal(t, 2, count(t, r))
	r = get(t, cs, "get_tags", map[string]any{"user_id": u, "kind": "travel", "start": "2026-04-01"})
	assert.Equal(t, 1, count(t, r))

	// Paging.
	r = get(t, cs, "get_tags", map[string]any{"user_id": u, "limit": 2})
	assert.True(t, r.Truncated)
	require.NotNil(t, r.NextCursor)
	r = get(t, cs, "get_tags", map[string]any{"user_id": u, "limit": 2, "cursor": *r.NextCursor})
	assert.Equal(t, 1, count(t, r))
	assert.False(t, r.Truncated)

	for want, args := range map[string]map[string]any{
		"kind must be one of":      {"user_id": u, "kind": "flu"},
		"the cursor is not valid":  {"user_id": u, "cursor": "zzz"},
		"limit must be between":    {"user_id": u, "limit": 5000},
		"start is not an RFC 3339": {"user_id": u, "start": "soon"},
	} {
		msg, isErr := call(t, cs, "get_tags", args)
		assert.True(t, isErr, want)
		assert.Contains(t, msg, want)
	}

	// Another tenant's user is not found; a user token reads its own tags.
	msg, isErr := call(t, Connect(t, e.srv.URL+"/mcp", e.keyB), "get_tags", map[string]any{"user_id": u})
	assert.True(t, isErr)
	assert.Equal(t, "user not found", msg)
	r = get(t, Connect(t, e.srv.URL+"/mcp", e.token(e.keyB, e.userB, "read")), "get_tags", nil)
	assert.Equal(t, 1, count(t, r))
	assert.Contains(t, string(r.Records), "injury")
}

func ptr[T any](v T) *T { return &v }
