// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package timetag_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/timetag"
)

func ptr[T any](v T) *T { return &v }

func day(d int, h int) time.Time { return time.Date(2026, 4, d, h, 0, 0, 0, time.UTC) }

// setup returns a pool on a fresh database, a tenant and one of its users.
func setup(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	ts := tenant.New(pool)
	ten, _, err := ts.CreateTenant(ctx, "tags")
	require.NoError(t, err)
	u, err := ts.CreateUser(ctx, ten.ID, "tag-user")
	require.NoError(t, err)
	return pool, ten.ID, u.ID
}

func TestStoreCreateGetUpdateDelete(t *testing.T) {
	ctx := context.Background()
	pool, ten, user := setup(t)

	tag, err := timetag.Create(ctx, pool, ten, user, timetag.Fields{
		Kind: "illness", Label: ptr("flu"), StartAt: day(3, 8).Add(1500 * time.Nanosecond),
		Timezone: ptr("Europe/Paris"),
	})
	require.NoError(t, err)
	assert.Equal(t, "illness", tag.Kind)
	assert.Equal(t, timetag.DefaultSource, tag.Source)
	assert.Nil(t, tag.EndAt, "ongoing")
	assert.True(t, tag.StartAt.Equal(day(3, 8).Add(time.Microsecond)), "kept to the microsecond: %v", tag.StartAt)
	assert.Equal(t, time.UTC, tag.StartAt.Location())

	got, err := timetag.Get(ctx, pool, ten, user, tag.ID)
	require.NoError(t, err)
	assert.Equal(t, tag, got)

	// End it and clear its label.
	upd, err := timetag.Update(ctx, pool, ten, user, tag.ID, timetag.Patch{
		EndAt: timetag.Change[time.Time]{Set: true, Value: ptr(day(6, 20))},
		Label: timetag.Change[string]{Set: true},
	})
	require.NoError(t, err)
	require.NotNil(t, upd.EndAt)
	assert.True(t, upd.EndAt.Equal(day(6, 20)))
	assert.Nil(t, upd.Label)
	assert.Equal(t, "Europe/Paris", *upd.Timezone, "untouched fields kept")
	assert.False(t, upd.UpdatedAt.Before(tag.UpdatedAt))

	// A patch that changes nothing returns the tag as it is.
	same, err := timetag.Update(ctx, pool, ten, user, tag.ID, timetag.Patch{})
	require.NoError(t, err)
	assert.Equal(t, upd, same)

	// The result is checked as a whole: a start after the end is refused.
	_, err = timetag.Update(ctx, pool, ten, user, tag.ID, timetag.Patch{
		StartAt: timetag.Change[time.Time]{Set: true, Value: ptr(day(7, 0))},
	})
	var inv *timetag.InvalidError
	assert.ErrorAs(t, err, &inv)
	_, err = timetag.Update(ctx, pool, ten, user, tag.ID, timetag.Patch{Kind: timetag.Change[string]{Set: true}})
	assert.ErrorAs(t, err, &inv, "kind cannot be cleared")

	require.NoError(t, timetag.Delete(ctx, pool, ten, user, tag.ID))
	_, err = timetag.Get(ctx, pool, ten, user, tag.ID)
	assert.ErrorIs(t, err, timetag.ErrNotFound)
	assert.ErrorIs(t, timetag.Delete(ctx, pool, ten, user, tag.ID), timetag.ErrNotFound)
	_, err = timetag.Update(ctx, pool, ten, user, tag.ID, timetag.Patch{Note: timetag.Change[string]{Set: true, Value: ptr("x")}})
	assert.ErrorIs(t, err, timetag.ErrNotFound)
}

func TestStoreScopesToTheUser(t *testing.T) {
	ctx := context.Background()
	pool, ten, user := setup(t)
	other, err := tenant.New(pool).CreateUser(ctx, ten, "other-user")
	require.NoError(t, err)
	tag, err := timetag.Create(ctx, pool, ten, user, timetag.Fields{Kind: "travel", StartAt: day(1, 0)})
	require.NoError(t, err)

	_, err = timetag.Get(ctx, pool, ten, other.ID, tag.ID)
	assert.ErrorIs(t, err, timetag.ErrNotFound, "another user's tag is not found")
	assert.ErrorIs(t, timetag.Delete(ctx, pool, ten, other.ID, tag.ID), timetag.ErrNotFound)

	stranger := uuid.New()
	_, err = timetag.Get(ctx, pool, ten, stranger, tag.ID)
	assert.ErrorIs(t, err, timetag.ErrUserNotFound)
	_, err = timetag.List(ctx, pool, ten, stranger, timetag.Query{})
	assert.ErrorIs(t, err, timetag.ErrUserNotFound)
	_, err = timetag.Create(ctx, pool, ten, stranger, timetag.Fields{Kind: "travel", StartAt: day(1, 0)})
	assert.ErrorIs(t, err, timetag.ErrUserNotFound)
	_, err = timetag.Get(ctx, pool, uuid.New(), user, tag.ID)
	assert.ErrorIs(t, err, timetag.ErrUserNotFound, "the user under another tenant")

	// A user being deleted takes no new tag.
	_, err = pool.Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, other.ID)
	require.NoError(t, err)
	_, err = timetag.Create(ctx, pool, ten, other.ID, timetag.Fields{Kind: "travel", StartAt: day(1, 0)})
	assert.ErrorIs(t, err, timetag.ErrUserNotFound)
}

func TestStoreListWindowKindAndPages(t *testing.T) {
	ctx := context.Background()
	pool, ten, user := setup(t)
	mk := func(kind string, start time.Time, end *time.Time) uuid.UUID {
		tag, err := timetag.Create(ctx, pool, ten, user, timetag.Fields{Kind: kind, StartAt: start, EndAt: end})
		require.NoError(t, err)
		return tag.ID
	}
	a := mk("travel", day(1, 0), ptr(day(3, 0)))
	b := mk("illness", day(2, 0), ptr(day(2, 12)))
	c := mk("travel", day(5, 0), nil) // ongoing
	d := mk("alcohol", day(10, 20), ptr(day(10, 23)))

	ids := func(q timetag.Query) []uuid.UUID {
		t.Helper()
		p, err := timetag.List(ctx, pool, ten, user, q)
		require.NoError(t, err)
		out := []uuid.UUID{}
		for _, tag := range p.Records {
			out = append(out, tag.ID)
		}
		return out
	}
	assert.Equal(t, []uuid.UUID{a, b, c, d}, ids(timetag.Query{}), "by start time")
	assert.Equal(t, []uuid.UUID{a, c}, ids(timetag.Query{Kind: "travel"}))
	// [day 2 12:00, day 4): a overlaps, b ends exactly at the start (excluded).
	assert.Equal(t, []uuid.UUID{a}, ids(timetag.Query{Start: day(2, 12), End: day(4, 0)}))
	// An ongoing tag has no end: it overlaps any later window.
	assert.Equal(t, []uuid.UUID{c, d}, ids(timetag.Query{Start: day(9, 0)}))
	assert.Equal(t, []uuid.UUID{a, b}, ids(timetag.Query{End: day(5, 0)}), "a tag starting at End is out")
	assert.Equal(t, []uuid.UUID{}, ids(timetag.Query{Start: day(2, 0), End: day(2, 0)}), "an empty window")

	// Pages of one, following the cursor.
	var walked []uuid.UUID
	q := timetag.Query{Limit: 1}
	for {
		p, err := timetag.List(ctx, pool, ten, user, q)
		require.NoError(t, err)
		require.LessOrEqual(t, len(p.Records), 1)
		for _, tag := range p.Records {
			walked = append(walked, tag.ID)
		}
		if p.NextCursor == "" {
			break
		}
		q.Cursor = p.NextCursor
	}
	assert.Equal(t, []uuid.UUID{a, b, c, d}, walked)

	for _, bad := range []timetag.Query{
		{Kind: "holiday"}, {Start: day(3, 0), End: day(2, 0)}, {Limit: -1}, {Limit: timetag.MaxLimit + 1},
	} {
		_, err := timetag.List(ctx, pool, ten, user, bad)
		var inv *timetag.InvalidError
		assert.ErrorAs(t, err, &inv, "%+v", bad)
	}
	_, err := timetag.List(ctx, pool, ten, user, timetag.Query{Cursor: "%%"})
	assert.ErrorIs(t, err, timetag.ErrInvalidCursor)
}

func TestStoreQueuesWebhookEvents(t *testing.T) {
	ctx := context.Background()
	pool, ten, user := setup(t)
	_, err := pool.Exec(ctx, `INSERT INTO ohs_core.webhook_endpoints (id, tenant_id, url, secret_sealed, event_types)
		VALUES ($1, $2, 'https://hooks.example.com/tags', '\x00', '{}')`, uuid.New(), ten)
	require.NoError(t, err)

	tag, err := timetag.Create(ctx, pool, ten, user, timetag.Fields{Kind: "vaccination", StartAt: day(1, 9)})
	require.NoError(t, err)
	_, err = timetag.Update(ctx, pool, ten, user, tag.ID, timetag.Patch{Note: timetag.Change[string]{Set: true, Value: ptr("dose 2")}})
	require.NoError(t, err)
	_, err = timetag.Update(ctx, pool, ten, user, tag.ID, timetag.Patch{})
	require.NoError(t, err)
	require.NoError(t, timetag.Delete(ctx, pool, ten, user, tag.ID))

	rows, err := pool.Query(ctx, `SELECT event_type, payload->'data'->>'tag_id', payload->'data' ? 'note'
		FROM ohs_core.webhook_deliveries WHERE user_id = $1 ORDER BY id`, user)
	require.NoError(t, err)
	defer rows.Close()
	var types []string
	for rows.Next() {
		var typ, id string
		var leaks bool
		require.NoError(t, rows.Scan(&typ, &id, &leaks))
		types = append(types, typ)
		assert.Equal(t, tag.ID.String(), id)
		assert.False(t, leaks, "the event names the tag, never its text")
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"tag.created", "tag.updated", "tag.deleted"}, types, "a patch that changes nothing announces nothing")
}
