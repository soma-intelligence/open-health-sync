// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package settings_test

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
)

type hbEnv struct {
	pool  *pgxpool.Pool
	store *settings.PG
	url   string
}

func newHBEnv(t *testing.T) hbEnv {
	t.Helper()
	url := db.TestDB(t)
	pool, err := db.Open(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	key, err := base64.StdEncoding.DecodeString(goodKey)
	require.NoError(t, err)
	box, err := crypto.NewBox(key)
	require.NoError(t, err)
	return hbEnv{pool: pool, store: settings.NewPG(pool, box), url: url}
}

func (e hbEnv) live(t *testing.T, start settings.Start) *settings.Live {
	t.Helper()
	cfg, env, environ := bootEnv(t, e.url)
	return settings.NewLive(settings.LiveOptions{Store: e.store, Env: env, Environ: environ, Config: cfg, Start: start})
}

func (e hbEnv) writer(t *testing.T, live *settings.Live) (*settings.HeartbeatWriter, settings.Heartbeat) {
	t.Helper()
	hb := settings.Heartbeat{
		ID: uuid.New(), Host: "ohs-test", Roles: []string{"etl", "api"}, Version: "0.0.0-test",
		StartedAt: time.Now().Add(-time.Minute).UTC().Truncate(time.Second),
	}
	return settings.NewHeartbeatWriter(e.pool, live, hb), hb
}

func (e hbEnv) count(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_core.process_heartbeats`).Scan(&n))
	return n
}

func TestHeartbeatUpsert(t *testing.T) {
	e := newHBEnv(t)
	ctx := context.Background()
	live := e.live(t, settings.Start{
		Revision: 3,
		Invalid:  []settings.Invalid{{Name: "OHS_BACKFILL_DAYS", Message: "bad"}},
		Unknown:  []string{"OHS_OLD"},
	})
	w, hb := e.writer(t, live)
	require.NoError(t, w.Beat(ctx))
	require.NoError(t, w.Beat(ctx))
	require.Equal(t, 1, e.count(t))

	var skipped, roles []string
	var reloadErr *string
	var startRev, liveRev int64
	scan := func() {
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT skipped, roles, reload_error, start_revision, live_revision
			FROM ohs_core.process_heartbeats WHERE process_id = $1`, hb.ID).Scan(&skipped, &roles, &reloadErr, &startRev, &liveRev))
	}
	scan()
	require.Equal(t, []string{"OHS_BACKFILL_DAYS", "OHS_OLD"}, skipped)
	require.Equal(t, []string{"api", "etl"}, roles, "roles are sorted")
	require.Nil(t, reloadErr)
	require.EqualValues(t, 3, startRev)
	require.EqualValues(t, 3, liveRev)
}

func TestHeartbeatReloadErrorFollowsStatus(t *testing.T) {
	e := newHBEnv(t)
	ctx := context.Background()
	// A store that fails: the Live reads a pool that is closed.
	other := newHBEnv(t)
	live := other.live(t, settings.Start{Revision: 1})
	w, hb := e.writer(t, live)
	other.pool.Close()
	require.Error(t, live.Reload(ctx))
	msg := live.Status().ReloadError
	require.NotEmpty(t, msg)
	require.NoError(t, w.Beat(ctx))
	var got *string
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT reload_error FROM ohs_core.process_heartbeats WHERE process_id = $1`, hb.ID).Scan(&got))
	require.NotNil(t, got)
	require.Equal(t, msg, *got)
}

func TestProcessesBehindAndStale(t *testing.T) {
	e := newHBEnv(t)
	ctx := context.Background()
	by := uuid.New()
	rev1, err := e.store.Apply(ctx, 0, settings.Change{By: by, Put: map[string]string{"OHS_WEBHOOK_PAUSE_AFTER": "6"}})
	require.NoError(t, err)
	live := e.live(t, settings.Start{Revision: rev1})
	w, hb := e.writer(t, live)
	require.NoError(t, w.Beat(ctx))

	one := func() (settings.Process, int64) {
		ps, rev, err := settings.ListProcesses(ctx, e.pool)
		require.NoError(t, err)
		require.Len(t, ps, 1)
		return ps[0], rev
	}
	p, rev := one()
	require.Equal(t, rev1, rev)
	require.Equal(t, hb.ID, p.ID)
	require.Equal(t, "ohs-test", p.Host)
	require.Equal(t, []string{"api", "etl"}, p.Roles)
	require.False(t, p.Behind)
	require.False(t, p.Stale)

	// A change to a restart setting: the process started before it.
	rev2, err := e.store.Apply(ctx, rev1, settings.Change{By: by, Restart: true, Put: map[string]string{"OHS_INGEST_TENANT_CONCURRENCY": "4"}})
	require.NoError(t, err)
	p, rev = one()
	require.Equal(t, rev2, rev)
	require.True(t, p.Behind)

	// A live change this process has not loaded: stale only after 60 s.
	rev3, err := e.store.Apply(ctx, rev2, settings.Change{By: by, Put: map[string]string{"OHS_WEBHOOK_PAUSE_AFTER": "7"}})
	require.NoError(t, err)
	setAge := func(sec int) {
		_, err := e.pool.Exec(ctx, `UPDATE ohs_core.instance_settings_revision SET changed_at = now() - make_interval(secs => $1)`, sec)
		require.NoError(t, err)
	}
	setAge(59)
	p, rev = one()
	require.Equal(t, rev3, rev)
	require.False(t, p.Stale, "59 s is not stale yet")
	setAge(61)
	p, _ = one()
	require.True(t, p.Stale)

	// Loading the change clears stale; the restart change keeps it behind.
	require.NoError(t, live.Reload(ctx))
	require.NoError(t, w.Beat(ctx))
	p, _ = one()
	require.False(t, p.Stale)
	require.True(t, p.Behind)
	require.Equal(t, rev3, p.LiveRevision)
	require.Equal(t, rev1, p.StartRevision)
}

func TestProcessesHideAndPrune(t *testing.T) {
	e := newHBEnv(t)
	ctx := context.Background()
	live := e.live(t, settings.Start{})
	ids := map[string]uuid.UUID{}
	for name, age := range map[string]int{"fresh": 0, "hidden": 180, "old": 360} {
		w, hb := e.writer(t, live)
		require.NoError(t, w.Beat(ctx))
		_, err := e.pool.Exec(ctx, `UPDATE ohs_core.process_heartbeats SET seen_at = now() - make_interval(secs => $1) WHERE process_id = $2`, age, hb.ID)
		require.NoError(t, err)
		ids[name] = hb.ID
	}
	ps, _, err := settings.ListProcesses(ctx, e.pool)
	require.NoError(t, err)
	require.Len(t, ps, 1)
	require.Equal(t, ids["fresh"], ps[0].ID)

	n, err := settings.PruneHeartbeats(ctx, e.pool)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, 2, e.count(t), "the row 3 min back is kept, hidden only")
	var gone bool
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM ohs_core.process_heartbeats WHERE process_id = $1)`, ids["old"]).Scan(&gone))
	require.True(t, gone)
}

func TestHeartbeatGoneOnStop(t *testing.T) {
	e := newHBEnv(t)
	live := e.live(t, settings.Start{})
	w, hb := e.writer(t, live)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, 50*time.Millisecond) }()
	require.Eventually(t, func() bool { return e.count(t) == 1 }, 5*time.Second, 20*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_core.process_heartbeats WHERE process_id = $1`, hb.ID).Scan(&n))
	require.Zero(t, n)
}

func TestHeartbeatBeatsOnSwap(t *testing.T) {
	e := newHBEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live := e.live(t, settings.Start{})
	w, hb := e.writer(t, live)
	go func() { _ = w.Run(ctx, time.Hour) }()
	require.Eventually(t, func() bool { return e.count(t) == 1 }, 5*time.Second, 20*time.Millisecond)

	rev, err := e.store.Apply(ctx, 0, settings.Change{By: uuid.New(), Put: map[string]string{"OHS_WEBHOOK_PAUSE_AFTER": "8"}})
	require.NoError(t, err)
	require.NoError(t, live.Reload(ctx))
	require.Eventually(t, func() bool {
		var got int64
		err := e.pool.QueryRow(ctx, `SELECT live_revision FROM ohs_core.process_heartbeats WHERE process_id = $1`, hb.ID).Scan(&got)
		return err == nil && got == rev
	}, 5*time.Second, 20*time.Millisecond)
}
