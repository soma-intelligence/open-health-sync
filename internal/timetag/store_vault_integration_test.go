// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package timetag_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/timetag"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// sealedSetup is setup on an instance with a vault: the user has keys and
// ctx carries the vault, as a request of the API does.
func sealedSetup(t *testing.T) (context.Context, *pgxpool.Pool, *vault.Vault, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	p, err := vault.OpenProvider(ctx, config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{2}, 32)})
	require.NoError(t, err)
	v := vault.New(pool, p, vault.Options{CanOpen: true})
	ts := tenant.New(pool).WithVault(v)
	ten, _, err := ts.CreateTenant(ctx, "tags")
	require.NoError(t, err)
	u, err := ts.CreateUser(ctx, ten.ID, "tag-user")
	require.NoError(t, err)
	return vault.NewContext(ctx, v), pool, v, ten.ID, u.ID
}

// stored is what the row of a tag holds in the database.
type stored struct {
	kind, label, timezone, note, source *string
	sealed                              []byte
}

func storedTag(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) stored {
	t.Helper()
	var s stored
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT kind, label, timezone, note, source, sealed
		FROM ohs_data.time_tags WHERE id = $1`, id).Scan(&s.kind, &s.label, &s.timezone, &s.note, &s.source, &s.sealed))
	return s
}

// Under a vault a tag is stored sealed: its kind, label, timezone, note and
// source are in the sealed value and their columns are NULL, and every read
// and write gives the tag back as a clear one does.
func TestSealedTagsCreateGetUpdateDelete(t *testing.T) {
	ctx, pool, _, ten, user := sealedSetup(t)

	tag, err := timetag.Create(ctx, pool, ten, user, timetag.Fields{
		Kind: "illness", Label: ptr("flu"), StartAt: day(3, 8), Timezone: ptr("Europe/Paris"), Note: ptr("fever at night"),
	})
	require.NoError(t, err)
	assert.Equal(t, "illness", tag.Kind)
	assert.Equal(t, "flu", *tag.Label)
	assert.Equal(t, "fever at night", *tag.Note)
	assert.Equal(t, timetag.DefaultSource, tag.Source)

	row := storedTag(t, pool, tag.ID)
	assert.Equal(t, stored{sealed: row.sealed}, row, "every sensitive column is NULL")
	require.NotEmpty(t, row.sealed)
	for _, text := range []string{"illness", "flu", "fever at night", "Europe/Paris"} {
		assert.NotContains(t, string(row.sealed), text, "the sealed value does not show its content")
	}

	got, err := timetag.Get(ctx, pool, ten, user, tag.ID)
	require.NoError(t, err)
	assert.Equal(t, tag, got)

	// A patch keeps what it does not name, and the row stays sealed.
	end := day(6, 8)
	upd, err := timetag.Update(ctx, pool, ten, user, tag.ID, timetag.Patch{
		EndAt: timetag.Change[time.Time]{Set: true, Value: &end},
		Label: timetag.Change[string]{Set: true},
		Kind:  timetag.Change[string]{Set: true, Value: ptr("injury")},
	})
	require.NoError(t, err)
	assert.Equal(t, "injury", upd.Kind)
	assert.Nil(t, upd.Label)
	assert.Equal(t, "fever at night", *upd.Note)
	assert.Equal(t, "Europe/Paris", *upd.Timezone)
	assert.Equal(t, timetag.DefaultSource, upd.Source)
	require.NotNil(t, upd.EndAt)
	assert.True(t, upd.EndAt.Equal(end))
	after := storedTag(t, pool, tag.ID)
	assert.Equal(t, stored{sealed: after.sealed}, after)
	assert.NotEqual(t, row.sealed, after.sealed)
	got, err = timetag.Get(ctx, pool, ten, user, tag.ID)
	require.NoError(t, err)
	assert.Equal(t, upd, got)

	// A sealed value moved to another tag does not open: the read fails, it
	// does not return the other tag's content.
	other, err := timetag.Create(ctx, pool, ten, user, timetag.Fields{Kind: "travel", StartAt: day(9, 8)})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE ohs_data.time_tags SET sealed = $2 WHERE id = $1`, other.ID, after.sealed)
	require.NoError(t, err)
	_, err = timetag.Get(ctx, pool, ten, user, other.ID)
	assert.ErrorIs(t, err, vault.ErrOpen)
	_, err = timetag.List(ctx, pool, ten, user, timetag.Query{})
	assert.ErrorIs(t, err, vault.ErrOpen, "a list never leaves the tag out")
	require.NoError(t, timetag.Delete(ctx, pool, ten, user, other.ID))

	// Without the vault the sealed tag is an error, never an empty tag.
	_, err = timetag.Get(context.Background(), pool, ten, user, tag.ID)
	assert.ErrorIs(t, err, vault.ErrNoVault)

	require.NoError(t, timetag.Delete(ctx, pool, ten, user, tag.ID))
	_, err = timetag.Get(ctx, pool, ten, user, tag.ID)
	assert.ErrorIs(t, err, timetag.ErrNotFound)
}

// The kind of a sealed tag is sealed, so the kind filter runs on the opened
// tags: a page is still full, in order, and followed by its cursor, with
// sealed tags of other kinds between the matches and a tag written before
// the vault (clear) among them.
func TestSealedTagsListByKind(t *testing.T) {
	ctx, pool, _, ten, user := sealedSetup(t)
	var want []uuid.UUID
	for i := range 23 {
		kind := "travel"
		if i%4 == 0 {
			kind = "illness"
		}
		c := ctx
		if i == 8 {
			// Written by a release without the vault.
			c = context.Background()
		}
		tag, err := timetag.Create(c, pool, ten, user, timetag.Fields{Kind: kind, Label: ptr(fmt.Sprint(i)),
			StartAt: day(1, 0).Add(time.Duration(i) * time.Hour)})
		require.NoError(t, err)
		if kind == "illness" {
			want = append(want, tag.ID)
		}
	}
	require.Len(t, want, 6)
	clear := storedTag(t, pool, want[2])
	require.Nil(t, clear.sealed)
	require.Equal(t, "illness", *clear.kind)

	var got []uuid.UUID
	q := timetag.Query{Kind: "illness", Limit: 2}
	for pages := 0; ; pages++ {
		require.Less(t, pages, 10)
		pg, err := timetag.List(ctx, pool, ten, user, q)
		require.NoError(t, err)
		require.LessOrEqual(t, len(pg.Records), 2)
		for _, r := range pg.Records {
			assert.Equal(t, "illness", r.Kind)
			got = append(got, r.ID)
		}
		if pg.NextCursor == "" {
			break
		}
		require.Len(t, pg.Records, 2, "a page followed by another is full")
		q.Cursor = pg.NextCursor
	}
	assert.Equal(t, want, got)

	all, err := timetag.List(ctx, pool, ten, user, timetag.Query{Limit: 1000})
	require.NoError(t, err)
	assert.Len(t, all.Records, 23)
	none, err := timetag.List(ctx, pool, ten, user, timetag.Query{Kind: "alcohol"})
	require.NoError(t, err)
	assert.Empty(t, none.Records)
	assert.Empty(t, none.NextCursor)

	// The first update of the clear tag seals it.
	_, err = timetag.Update(ctx, pool, ten, user, want[2], timetag.Patch{Note: timetag.Change[string]{Set: true, Value: ptr("worse")}})
	require.NoError(t, err)
	converted := storedTag(t, pool, want[2])
	assert.Equal(t, stored{sealed: converted.sealed}, converted)
	require.NotEmpty(t, converted.sealed)
	tag, err := timetag.Get(ctx, pool, ten, user, want[2])
	require.NoError(t, err)
	assert.Equal(t, "illness", tag.Kind)
	assert.Equal(t, "8", *tag.Label)
	assert.Equal(t, "worse", *tag.Note)
}

// A user whose keys are gone reads and writes as an absent user.
func TestSealedTagsOfAUserWithoutKeys(t *testing.T) {
	ctx, pool, _, ten, user := sealedSetup(t)
	tag, err := timetag.Create(ctx, pool, ten, user, timetag.Fields{Kind: "travel", StartAt: day(2, 0)})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id = $2`, ten, user)
	require.NoError(t, err)

	// A vault of its own: nothing cached.
	p, err := vault.OpenProvider(ctx, config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{2}, 32)})
	require.NoError(t, err)
	cold := vault.NewContext(context.Background(), vault.New(pool, p, vault.Options{CanOpen: true}))
	_, err = timetag.Get(cold, pool, ten, user, tag.ID)
	assert.ErrorIs(t, err, timetag.ErrUserNotFound)
	_, err = timetag.List(cold, pool, ten, user, timetag.Query{})
	assert.ErrorIs(t, err, timetag.ErrUserNotFound)
	_, err = timetag.Create(cold, pool, ten, user, timetag.Fields{Kind: "travel", StartAt: day(3, 0)})
	assert.ErrorIs(t, err, timetag.ErrUserNotFound, "nothing is written clear for a user who had keys")
}
