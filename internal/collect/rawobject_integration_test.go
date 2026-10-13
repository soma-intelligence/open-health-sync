// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

func TestRegisterRawRefusesUserBeingDeleted(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	st := tenant.New(pool)
	ten, _, err := st.CreateTenant(ctx, "A")
	require.NoError(t, err)
	u, err := st.CreateUser(ctx, ten.ID, "u")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, u.ID)
	require.NoError(t, err)

	ok, err := registerRaw(ctx, pool, rawObject{
		tenant: ten.ID, user: u.ID, provider: "p", key: "raw/k", hash: "h", size: 1,
		batch: "b", part: 0, dataType: "steps", tier: queue.TierStandard,
	})
	assert.False(t, ok)
	assert.ErrorIs(t, err, errInactive)
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM ohs_sync.raw_objects) + (SELECT count(*) FROM ohs_sync.etl_jobs)`).Scan(&n))
	assert.Equal(t, 0, n)
}

// While the user's data from a provider is being erased nothing of that
// provider is registered; other providers are not affected.
func TestRegisterRawRefusesProviderBeingErased(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	st := tenant.New(pool)
	ten, _, err := st.CreateTenant(ctx, "A")
	require.NoError(t, err)
	u, err := st.CreateUser(ctx, ten.ID, "u")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO ohs_core.deletion_jobs (tenant_id, user_id, provider) VALUES ($1, $2, 'p')`, ten.ID, u.ID)
	require.NoError(t, err)

	obj := func(provider, key string) rawObject {
		return rawObject{tenant: ten.ID, user: u.ID, provider: provider, key: key, hash: key, size: 1,
			batch: "b", part: 0, dataType: "steps", tier: queue.TierStandard}
	}
	ok, err := registerRaw(ctx, pool, obj("p", "raw/k1"))
	assert.False(t, ok)
	assert.ErrorIs(t, err, errErasing)
	ok, err = registerRaw(ctx, pool, obj("q", "raw/k2"))
	require.NoError(t, err)
	assert.True(t, ok)

	_, err = pool.Exec(ctx, `UPDATE ohs_core.deletion_jobs SET status = 'done'`)
	require.NoError(t, err)
	ok, err = registerRaw(ctx, pool, obj("p", "raw/k3"))
	require.NoError(t, err)
	assert.True(t, ok, "once the job is done, new data of the provider is stored again")
}

// afterPut runs hook once a Put succeeded: it lets a test land between the
// unlocked user check and the registration.
type afterPut struct {
	rawstore.Store
	hook func()
}

func (a afterPut) Put(ctx context.Context, key string, r io.Reader) error {
	if err := a.Store.Put(ctx, key, r); err != nil {
		return err
	}
	a.hook()
	return nil
}

// A user whose deletion starts while a pull writes its file: the file is
// removed again, so it cannot outlive the deletion's raw sweep.
func TestPullSinkRemovesFileOfUserBeingDeleted(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	st := tenant.New(pool)
	ten, _, err := st.CreateTenant(ctx, "A")
	require.NoError(t, err)
	u, err := st.CreateUser(ctx, ten.ID, "u")
	require.NoError(t, err)
	fs, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)
	raw := afterPut{Store: fs, hook: func() {
		_, err := pool.Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, u.ID)
		require.NoError(t, err)
	}}

	sink := newPullSink(Deps{Pool: pool, Raw: raw}, queue.PullJob{
		ID: 1, TenantID: ten.ID, UserID: u.ID, Provider: "p", ClaimedAt: time.Now(),
	})
	err = sink.Emit(ctx, "steps", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), [][]byte{[]byte(`{"v":1}`)})
	assert.ErrorIs(t, err, errInactive)
	keys, err := fs.List(ctx, "raw/")
	require.NoError(t, err)
	assert.Empty(t, keys, "the file written for a user being deleted is removed")
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM ohs_sync.raw_objects`).Scan(&n))
	assert.Zero(t, n)
}

// A pull that finished just as the user's deletion started writes no
// sync_log row the deletion could miss.
func TestLogSyncSkipsUserBeingDeleted(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	st := tenant.New(pool)
	ten, _, err := st.CreateTenant(ctx, "A")
	require.NoError(t, err)
	active, err := st.CreateUser(ctx, ten.ID, "active")
	require.NoError(t, err)
	gone, err := st.CreateUser(ctx, ten.ID, "gone")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, gone.ID)
	require.NoError(t, err)

	s := &Scheduler{d: Deps{Pool: pool}}
	now := time.Now()
	for _, u := range []uuid.UUID{active.ID, gone.ID} {
		s.logSync(ctx, slog.Default(), queue.PullJob{TenantID: ten.ID, UserID: u, Provider: "p", From: now.Add(-time.Hour), To: now}, 3, time.Second, nil)
	}
	var a, g int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE user_id = $1), count(*) FILTER (WHERE user_id = $2) FROM ohs_sync.sync_log`,
		active.ID, gone.ID).Scan(&a, &g))
	assert.Equal(t, 1, a)
	assert.Equal(t, 0, g)
}
