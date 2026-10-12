// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

func TestScheduleHourlyWithoutNotifications(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.setNow(time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC))
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)

	// No notification ever arrived: pulled every hour.
	e.setNow(time.Date(2026, 10, 10, 14, 10, 0, 0, time.UTC))
	n, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	var from, to time.Time
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT from_time, to_time FROM ohs_sync.pull_jobs WHERE status = 'pending'`).Scan(&from, &to))
	assert.Equal(t, time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC), to.UTC())
	assert.Equal(t, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), from.UTC(), "one hour plus the overlap")
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "once per slot")
	e.drain(t)

	// Notifications stopped a day ago: back to hourly after a 6 hour stretch.
	_, err = e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET last_event_at = $1`, time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	e.setNow(time.Date(2026, 10, 10, 15, 10, 0, 0, time.UTC))
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "notifications arrive: next pull at the 18:00 slot")
	e.setNow(time.Date(2026, 10, 11, 15, 10, 0, 0, time.UTC))
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT to_time FROM ohs_sync.pull_jobs WHERE status = 'pending'`).Scan(&to))
	assert.Equal(t, time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC), to.UTC(), "silent for a day: hourly slot")
}

func TestInboundRecordsLastEvent(t *testing.T) {
	e := newConnEnv(t)
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	e.setNow(at)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.notify(t, "pu-1", at.Add(-time.Hour).Unix(), at.Unix())
	var got time.Time
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT last_event_at FROM ohs_sync.connections WHERE user_id = $1`, e.userA).Scan(&got))
	assert.Equal(t, at, got.UTC())
}

func TestPullOutcomeUpdatesFreshness(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	connectedAt := time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC)
	e.setNow(connectedAt)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)

	type state struct {
		through  *time.Time
		failures int
		lastErr  *string
	}
	read := func() state {
		var s state
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT synced_through, consecutive_failures, last_error
			FROM ohs_sync.connections WHERE user_id = $1`, e.userA).Scan(&s.through, &s.failures, &s.lastErr))
		return s
	}
	s := read()
	require.NotNil(t, s.through)
	assert.Equal(t, connectedAt, s.through.UTC(), "the backfill ends at the connect")
	assert.Equal(t, 0, s.failures)

	// A pull of an older window does not move synced_through back.
	e.notify(t, "pu-1", connectedAt.Add(-48*time.Hour).Unix(), connectedAt.Add(-47*time.Hour).Unix())
	e.drain(t)
	assert.Equal(t, connectedAt, read().through.UTC())
	// Forget that notification: the connection is pulled hourly from here.
	_, err := e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET last_event_at = NULL`)
	require.NoError(t, err)

	// Failures are counted and kept until a success clears them.
	e.fake.fetchMode.Store("500")
	e.setNow(connectedAt.Add(2 * time.Hour))
	_, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	for range collect.FailingAfter {
		_, err = e.pool.Exec(ctx, `UPDATE ohs_sync.pull_jobs SET not_before = now() WHERE status = 'pending'`)
		require.NoError(t, err)
		n, err := e.sched.PullOnce(ctx, 1)
		require.NoError(t, err)
		require.Equal(t, 1, n)
	}
	s = read()
	assert.Equal(t, collect.FailingAfter, s.failures)
	require.NotNil(t, s.lastErr)

	list := e.freshness(t)
	require.Len(t, list.Data, 1)
	assert.Equal(t, collect.FreshnessFailing, list.Data[0].State)
	assert.Equal(t, collect.KindOAuth, list.Data[0].Kind)

	e.fake.fetchMode.Store("ok")
	_, err = e.pool.Exec(ctx, `UPDATE ohs_sync.pull_jobs SET not_before = now() WHERE status = 'pending'`)
	require.NoError(t, err)
	e.drain(t)
	s = read()
	assert.Equal(t, 0, s.failures)
	assert.Nil(t, s.lastErr)
	assert.Equal(t, time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC), s.through.UTC())

	list = e.freshness(t)
	f := list.Data[0]
	assert.Equal(t, collect.FreshnessFresh, f.State)
	require.NotNil(t, f.LagSeconds)
	assert.Equal(t, int64(5*60), *f.LagSeconds)
	assert.False(t, f.PushHealthy)
	assert.Equal(t, int64(3600), f.PullEverySeconds)
	require.NotNil(t, f.NextPullAt)
	assert.Equal(t, time.Date(2026, 10, 10, 16, 0, 0, 0, time.UTC), *f.NextPullAt)

	// Two hours and more without a pull: stale.
	e.setNow(connectedAt.Add(6 * time.Hour))
	assert.Equal(t, collect.FreshnessStale, e.freshness(t).Data[0].State)
}

func (e *connEnv) freshness(t *testing.T) collect.FreshnessList {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/v1/users/"+e.userA.String()+"/freshness", e.keyA, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out collect.FreshnessList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestFreshnessListsDeviceSources(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	e.setNow(now)
	_, err := e.pool.Exec(ctx, `INSERT INTO ohs_sync.raw_objects
		(tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, created_at)
		VALUES ($1, $2, 'apple_health', 'k1', 'h1', 1, 'b', 0, $3), ($1, $2, 'apple_health', 'k2', 'h2', 1, 'b', 1, $4)`,
		e.tenantA, e.userA, now.Add(-30*time.Hour), now.Add(-2*time.Hour))
	require.NoError(t, err)

	d := e.deps
	d.Lookup = func(slug string) (connector.Connector, bool) {
		if slug == "apple_health" {
			return applehealth.New(), true
		}
		return e.deps.Lookup(slug)
	}
	list, err := d.Freshness(ctx, e.tenantA, e.userA)
	require.NoError(t, err)
	require.Len(t, list.Data, 1)
	f := list.Data[0]
	assert.Equal(t, "apple_health", f.Provider)
	assert.Equal(t, collect.KindDevice, f.Kind)
	assert.Equal(t, collect.FreshnessFresh, f.State)
	assert.Equal(t, int64(2*3600), *f.LagSeconds)

	e.setNow(now.Add(25 * time.Hour))
	list, err = d.Freshness(ctx, e.tenantA, e.userA)
	require.NoError(t, err)
	assert.Equal(t, collect.FreshnessStale, list.Data[0].State)

	// Another tenant's key does not see the user.
	rec := e.do(t, http.MethodGet, "/v1/users/"+e.userA.String()+"/freshness", e.keyB, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRenewSilentSubscriptions(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	// Rows default to the database clock: start from the real time.
	start := time.Now().UTC().Add(time.Second)
	e.setNow(start)
	e.connect(t, e.keyA, e.userA, "pu-1")
	_, err := e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, "subscribed", e.subStatus(t))

	// Recent subscription: left alone even though silent.
	e.setNow(start.Add(3 * 24 * time.Hour))
	n, err := e.sched.RenewSilent(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// Old but notifications arrive: left alone.
	e.setNow(start.Add(10 * 24 * time.Hour))
	e.notify(t, "pu-1", start.Unix(), start.Add(time.Hour).Unix())
	n, err = e.sched.RenewSilent(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// Old and silent for more than PushSilence: renewed at the provider.
	e.setNow(start.Add(12 * 24 * time.Hour))
	n, err = e.sched.RenewSilent(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, "pending", e.subStatus(t))
	_, err = e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, "subscribed", e.subStatus(t))
	calls := e.fake.subscriptionCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, "subscribe", calls[1].action)

	// Renewed: not again before ResubscribeAfter.
	e.setNow(start.Add(14 * 24 * time.Hour))
	n, err = e.sched.RenewSilent(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestRenewedSubscriptionIsUnsubscribedOnDisconnect(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	// Rows default to the database clock: start from the real time.
	start := time.Now().UTC().Add(time.Second)
	e.setNow(start)
	e.connect(t, e.keyA, e.userA, "pu-1")
	_, err := e.sched.SubscribePending(ctx, 10)
	require.NoError(t, err)
	e.setNow(start.Add(12 * 24 * time.Hour))
	n, err := e.sched.RenewSilent(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// Disconnect before the renewal ran: the live subscription is removed.
	rec := e.do(t, http.MethodDelete, "/v1/users/"+e.userA.String()+"/connections/"+fakeSlug, e.keyA, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	calls := e.fake.subscriptionCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, "unsubscribe", calls[1].action)
}

func TestSyncMetricsAggregatesByProvider(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC)
	e.setNow(now)
	e.connect(t, e.keyA, e.userA, "pu-1")
	ub, err := e.store.CreateUser(ctx, e.tenantA, "a-2")
	require.NoError(t, err)
	e.connect(t, e.keyA, ub.ID, "pu-2")
	e.drain(t)
	_, err = e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET consecutive_failures = 3 WHERE user_id = $1`, ub.ID)
	require.NoError(t, err)

	got, err := collect.NewHandlers(e.deps).SyncMetrics(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, fakeSlug, got[0].Provider)
	assert.Equal(t, int64(1), got[0].States[collect.FreshnessFresh])
	assert.Equal(t, int64(1), got[0].States[collect.FreshnessFailing])
	assert.Equal(t, int64(2), got[0].PushSilent, "the fake provider notifies, none arrived")
	assert.Equal(t, int64(0), got[0].MaxLagSeconds)
}
