// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package settings_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
)

const token = "0123456789abcdefXYZ"

func newStore(t *testing.T) (*settings.PG, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	box, err := crypto.NewBox(key)
	require.NoError(t, err)
	return settings.NewPG(pool, box), pool
}

func counters(t *testing.T, pool *pgxpool.Pool) (rev, restart int64) {
	t.Helper()
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT revision, restart_revision FROM ohs_core.instance_settings_revision`).Scan(&rev, &restart))
	return
}

func TestPGStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, pool := newStore(t)
	by := uuid.New()

	rev, err := s.Apply(ctx, 0, settings.Change{By: by, Put: map[string]string{
		"OHS_LOG_LEVEL": "debug", "OHS_METRICS_TOKEN": token}})
	require.NoError(t, err)
	require.EqualValues(t, 1, rev)

	st, err := s.Load(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, st.Revision)
	require.Len(t, st.Rows, 2)
	require.False(t, st.ChangedAt.IsZero())
	require.Equal(t, settings.Row{}.OpenErr, st.Rows["OHS_LOG_LEVEL"].OpenErr)
	require.Equal(t, "debug", st.Rows["OHS_LOG_LEVEL"].Value)
	require.False(t, st.Rows["OHS_LOG_LEVEL"].Sealed)
	m := st.Rows["OHS_METRICS_TOKEN"]
	require.True(t, m.Sealed)
	require.NoError(t, m.OpenErr)
	require.Equal(t, token, m.Value)
	require.Equal(t, by, m.UpdatedBy)

	var raw []byte
	var clear *string
	require.NoError(t, pool.QueryRow(ctx, `SELECT sealed, value FROM ohs_core.instance_settings WHERE name = 'OHS_METRICS_TOKEN'`).Scan(&raw, &clear))
	require.NotEmpty(t, raw)
	require.False(t, bytes.Contains(raw, []byte(token)))
	require.Nil(t, clear)

	// Switching a value replaces it; a delete removes the row.
	rev, err = s.Apply(ctx, 1, settings.Change{By: by, Put: map[string]string{"OHS_LOG_LEVEL": "info"}, Delete: []string{"OHS_METRICS_TOKEN"}})
	require.NoError(t, err)
	require.EqualValues(t, 2, rev)
	st, err = s.Load(ctx)
	require.NoError(t, err)
	require.Len(t, st.Rows, 1)
	require.Equal(t, "info", st.Rows["OHS_LOG_LEVEL"].Value)
}

func TestPGStoreRevision(t *testing.T) {
	ctx := context.Background()
	s, pool := newStore(t)
	by := uuid.New()

	rev, err := s.Apply(ctx, 0, settings.Change{By: by, Put: map[string]string{"OHS_LOG_LEVEL": "debug"}})
	require.NoError(t, err)
	require.EqualValues(t, 1, rev)

	_, err = s.Apply(ctx, 0, settings.Change{By: by, Put: map[string]string{"OHS_LOG_LEVEL": "warn"}})
	require.ErrorIs(t, err, settings.ErrRevisionMoved)
	st, err := s.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, "debug", st.Rows["OHS_LOG_LEVEL"].Value)
	r, _ := counters(t, pool)
	require.EqualValues(t, 1, r)

	// Deleting a name that is not there changes nothing.
	rev, err = s.Apply(ctx, 1, settings.Change{By: by, Delete: []string{"OHS_NOT_SAVED"}})
	require.NoError(t, err)
	require.EqualValues(t, 1, rev)
	r, rr := counters(t, pool)
	require.EqualValues(t, 1, r)
	require.EqualValues(t, 0, rr)

	got, err := s.Revision(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, got)

	// Restart false leaves restart_revision; true sets it to the new revision.
	rev, err = s.Apply(ctx, 1, settings.Change{By: by, Put: map[string]string{"OHS_LOG_LEVEL": "info"}})
	require.NoError(t, err)
	require.EqualValues(t, 2, rev)
	_, rr = counters(t, pool)
	require.EqualValues(t, 0, rr)
	rev, err = s.Apply(ctx, 2, settings.Change{By: by, Restart: true, Put: map[string]string{"OHS_PUBLIC_URL": "https://example.org"}})
	require.NoError(t, err)
	require.EqualValues(t, 3, rev)
	r, rr = counters(t, pool)
	require.EqualValues(t, 3, r)
	require.EqualValues(t, 3, rr)
	rev, err = s.Apply(ctx, 3, settings.Change{By: by, Put: map[string]string{"OHS_LOG_LEVEL": "warn"}})
	require.NoError(t, err)
	require.EqualValues(t, 4, rev)
	_, rr = counters(t, pool)
	require.EqualValues(t, 3, rr)
}

func TestPGStoreConcurrentApply(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	by := uuid.New()
	_, err := s.Apply(ctx, 0, settings.Change{By: by, Put: map[string]string{"OHS_LOG_LEVEL": "debug"}})
	require.NoError(t, err)

	var wg sync.WaitGroup
	revs := make([]int64, 2)
	errs := make([]error, 2)
	for i, v := range []string{"info", "warn"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			revs[i], errs[i] = s.Apply(ctx, 1, settings.Change{By: by, Put: map[string]string{"OHS_LOG_LEVEL": v}})
		}()
	}
	wg.Wait()
	won, lost := 0, 0
	for i := range errs {
		switch {
		case errs[i] == nil:
			won++
			require.EqualValues(t, 2, revs[i])
		default:
			lost++
			require.ErrorIs(t, errs[i], settings.ErrRevisionMoved)
		}
	}
	require.Equal(t, 1, won)
	require.Equal(t, 1, lost)
}

func TestPGStoreBoundToName(t *testing.T) {
	ctx := context.Background()
	s, pool := newStore(t)
	by := uuid.New()
	_, err := s.Apply(ctx, 0, settings.Change{By: by, Put: map[string]string{
		"OHS_METRICS_TOKEN": token, "OHS_VAULT_ROOT_KEY_PREVIOUS": "placeholder-previous-key"}})
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `UPDATE ohs_core.instance_settings
		SET sealed = (SELECT sealed FROM ohs_core.instance_settings WHERE name = 'OHS_METRICS_TOKEN')
		WHERE name = 'OHS_VAULT_ROOT_KEY_PREVIOUS'`)
	require.NoError(t, err)

	st, err := s.Load(ctx)
	require.NoError(t, err)
	moved := st.Rows["OHS_VAULT_ROOT_KEY_PREVIOUS"]
	require.Error(t, moved.OpenErr)
	require.Empty(t, moved.Value)
	require.NotContains(t, moved.OpenErr.Error(), token)
	require.NoError(t, st.Rows["OHS_METRICS_TOKEN"].OpenErr)
}
