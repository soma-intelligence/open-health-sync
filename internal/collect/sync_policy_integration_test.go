// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// policiedFake is the fake provider with a sync policy of its own.
type policiedFake struct {
	fakeConnector
	p connector.SyncPolicy
}

func (c policiedFake) SyncPolicy() connector.SyncPolicy { return c.p }

// latePolicy is a provider whose data arrives hours late: pulled every 3
// hours, each pull re-reading 6 hours, 14 days of backfill, and a 2 day
// reconciliation at midnight.
var latePolicy = connector.SyncPolicy{
	PullEveryWithoutPush: 3 * time.Hour,
	Lookback:             6 * time.Hour,
	Backfill:             14 * 24 * time.Hour,
	BackfillChunk:        7 * 24 * time.Hour,
	Reconcile:            2 * 24 * time.Hour,
}

func newPolicyEnv(t *testing.T, p connector.SyncPolicy) *connEnv {
	t.Helper()
	return newConnEnvWrapped(t, collect.SchedulerConfig{Concurrency: 8},
		func(c fakeConnector) connector.Connector { return policiedFake{fakeConnector: c, p: p} })
}

// slotJob is the from_time of the regular job whose window ends at slot.
func (e *connEnv) slotJob(t *testing.T, slot time.Time) time.Time {
	t.Helper()
	var from time.Time
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT from_time FROM ohs_sync.pull_jobs WHERE to_time = $1 AND trigger = 'scheduled'`, slot).Scan(&from))
	return from.UTC()
}

func TestProviderPolicyShapesBackfillAndRegularPulls(t *testing.T) {
	e := newPolicyEnv(t, latePolicy)
	ctx := context.Background()
	connected := time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC)
	e.setNow(connected)
	e.connect(t, e.keyA, e.userA, "pu-1")

	var n int
	var oldest time.Time
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*), min(from_time) FROM ohs_sync.pull_jobs
		WHERE user_id = $1 AND trigger = 'backfill'`, e.userA).Scan(&n, &oldest))
	assert.Equal(t, 2, n, "14 days in 7 day chunks")
	assert.Equal(t, connected.Add(-14*24*time.Hour), oldest.UTC())
	e.drain(t)

	// The provider sends no notifications yet: its 3 hour slot, starting
	// its 6 hour lookback before the interval.
	slot := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	e.setNow(slot.Add(20 * time.Minute))
	got, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, got)
	assert.Equal(t, slot.Add(-9*time.Hour), e.slotJob(t, slot))

	got, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, got, "one job per slot")
}

func TestProviderPolicyReconcilesAtMidnight(t *testing.T) {
	e := newPolicyEnv(t, latePolicy)
	ctx := context.Background()
	e.setNow(time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC))
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)

	midnight := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	_, err := e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET synced_through = $1, last_sync_at = $1`, midnight.Add(-3*time.Hour))
	require.NoError(t, err)
	e.setNow(midnight.Add(10 * time.Minute))
	n, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, midnight.Add(-48*time.Hour), e.slotJob(t, midnight), "the first slot of the day reaches back Reconcile")

	// A notification job that covers the slot's usual window, but not the
	// reconciliation, does not stand in for it.
	_, err = e.pool.Exec(ctx, `DELETE FROM ohs_sync.pull_jobs WHERE to_time = $1`, midnight)
	require.NoError(t, err)
	e.notify(t, "pu-1", midnight.Add(-12*time.Hour).Unix(), midnight.Add(time.Hour).Unix())
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, midnight.Add(-48*time.Hour), e.slotJob(t, midnight))
}

// A pull of one data type moves last_sync_at but not synced_through: the
// next regular pull still reaches back to the last complete pull, so a
// window whose pull failed is fetched again.
func TestRegularPullReachesBackPastSingleTypePulls(t *testing.T) {
	e := newPolicyEnv(t, latePolicy)
	ctx := context.Background()
	e.setNow(time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC))
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)

	slot := time.Date(2026, 10, 12, 15, 0, 0, 0, time.UTC)
	complete := slot.Add(-30 * time.Hour)
	_, err := e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET synced_through = $1, last_sync_at = $2`,
		complete, slot.Add(-10*time.Minute))
	require.NoError(t, err)
	e.setNow(slot.Add(5 * time.Minute))
	n, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, complete.Add(-6*time.Hour), e.slotJob(t, slot))
}

// The freshness view reports the provider's own interval.
func TestFreshnessReportsProviderInterval(t *testing.T) {
	e := newPolicyEnv(t, latePolicy)
	e.setNow(time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC))
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	f := e.freshness(t)
	require.Len(t, f.Data, 1)
	assert.Equal(t, int64(3*3600), f.Data[0].PullEverySeconds)
	require.NotNil(t, f.Data[0].NextPullAt)
	assert.Equal(t, time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC), *f.Data[0].NextPullAt)
}

// A short pull that starts after a gap does not move synced_through over
// the gap; one that starts inside the synced span does.
func TestShortPullAfterGapKeepsSyncedThrough(t *testing.T) {
	e := newPolicyEnv(t, latePolicy)
	ctx := context.Background()
	e.setNow(time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC))
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)

	now := time.Date(2026, 10, 12, 19, 0, 0, 0, time.UTC)
	gap := now.Add(-14 * time.Hour)
	_, err := e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET synced_through = $1`, gap)
	require.NoError(t, err)
	e.setNow(now)
	syncedThrough := func() time.Time {
		var at time.Time
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT synced_through FROM ohs_sync.connections WHERE user_id = $1`, e.userA).Scan(&at))
		return at.UTC()
	}

	e.notify(t, "pu-1", now.Add(-30*time.Minute).Unix(), now.Unix())
	e.drain(t)
	assert.Equal(t, gap, syncedThrough(), "the gap before the notified window stays to be pulled")

	e.notify(t, "pu-1", gap.Add(-time.Hour).Unix(), now.Unix())
	e.drain(t)
	assert.Equal(t, now, syncedThrough())
}
