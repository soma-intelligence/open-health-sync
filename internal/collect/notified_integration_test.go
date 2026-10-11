// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// notifiedFake is the fake provider able to pull only what a notification
// names. It records the data types it was asked for.
type notifiedFake struct {
	fakeConnector
	mu    *sync.Mutex
	asked *[]string
}

func (c notifiedFake) FetchNotified(ctx context.Context, conn connector.Connection, from, to time.Time, dataType string, emit connector.RawSink) error {
	c.mu.Lock()
	*c.asked = append(*c.asked, dataType)
	c.mu.Unlock()
	return c.Fetch(ctx, conn, from, to, emit)
}

func newNotifiedEnv(t *testing.T) (*connEnv, func() []string) {
	var mu sync.Mutex
	var asked []string
	e := newConnEnvWrapped(t, collect.SchedulerConfig{Concurrency: 8}, func(c fakeConnector) connector.Connector {
		return notifiedFake{fakeConnector: c, mu: &mu, asked: &asked}
	})
	return e, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), asked...) }
}

func TestInboundPullsOnlyTheNotifiedType(t *testing.T) {
	e, asked := newNotifiedEnv(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC)
	e.setNow(at)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	require.Empty(t, asked(), "the backfill pulls every type")
	var before time.Time
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT synced_through FROM ohs_sync.connections`).Scan(&before))

	e.notify(t, "pu-1", at.Add(-time.Hour).Unix(), at.Unix())
	e.notify(t, "pu-1", at.Add(-30*time.Minute).Unix(), at.Add(30*time.Minute).Unix()) // widened, same type
	var dataType string
	var prio int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT data_type, priority FROM ohs_sync.pull_jobs WHERE status = 'pending'`).Scan(&dataType, &prio))
	assert.Equal(t, "steps", dataType)
	assert.Equal(t, 0, prio, "notification pulls go first")

	e.drain(t)
	assert.Equal(t, []string{"steps"}, asked())
	var after time.Time
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT synced_through FROM ohs_sync.connections`).Scan(&after))
	assert.Equal(t, before, after, "one type does not make every type current")
}

func TestTypedJobDoesNotReplaceTheBackstop(t *testing.T) {
	e, _ := newNotifiedEnv(t)
	ctx := context.Background()
	e.setNow(time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC))
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	slot := time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC)
	// A pending notification pull covering the slot window.
	e.notify(t, "pu-1", slot.Add(-3*time.Hour).Unix(), slot.Add(time.Hour).Unix())
	// Keep the connection on hourly slots.
	_, err := e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET last_event_at = NULL`)
	require.NoError(t, err)
	e.setNow(slot.Add(5 * time.Minute))
	n, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE status = 'pending' AND data_type IS NULL AND to_time = $1`, slot))
}

func TestNotificationPullsAreClaimedFirst(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.setNow(time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC))
	e.connect(t, e.keyA, e.userA, "pu-1")
	// The six backfill jobs are pending and older; a notification arrives.
	require.Equal(t, 5, e.jobs(t, e.userA, "pending"))
	e.notify(t, "pu-1", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC).Unix())

	n, err := e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE status = 'done' AND priority = 0`))
	assert.Equal(t, 5, e.jobs(t, e.userA, "pending"), "the backfill waits")
}
