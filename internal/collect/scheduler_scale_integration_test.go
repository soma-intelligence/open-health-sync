// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// addConnections stores n backfilled connections of tenant A, each with a
// user of its own.
func (e *connEnv) addConnections(t *testing.T, n int, backfilledAt time.Time) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), `
		WITH u AS (
			INSERT INTO ohs_core.end_users (tenant_id, external_ref)
			SELECT $1, 'bulk-' || g FROM generate_series(1, $2::int) g
			RETURNING tenant_id, id
		)
		INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, status, initial_backfill_at)
		SELECT tenant_id, id, $3, 'active', $4 FROM u`, e.tenantA, n, fakeSlug, backfilledAt)
	require.NoError(t, err)
}

// A pass enqueues at most Batch regular pulls, the rest follow on the next
// ticks, and each connection gets one job per slot.
func TestScheduleBatchBoundsAPassAndCatchesUp(t *testing.T) {
	e := newConnEnvWith(t, collect.SchedulerConfig{Batch: 40})
	ctx := context.Background()
	slot := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	e.addConnections(t, 100, slot.Add(-48*time.Hour))
	e.setNow(slot.Add(5 * time.Minute))

	var passes []int
	for range 4 {
		n, err := e.sched.Schedule(ctx)
		require.NoError(t, err)
		passes = append(passes, n)
	}
	assert.Equal(t, []int{40, 40, 20, 0}, passes)
	assert.Equal(t, 100, e.scalar(t, `SELECT count(DISTINCT user_id) FROM ohs_sync.pull_jobs WHERE to_time = $1`, slot))
	assert.Equal(t, 100, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs`))

	// The next slot: everyone again, once.
	e.setNow(slot.Add(time.Hour + time.Minute))
	total := 0
	for range 4 {
		n, err := e.sched.Schedule(ctx)
		require.NoError(t, err)
		total += n
	}
	assert.Equal(t, 100, total)
	assert.Equal(t, 200, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs`))
}

// Once the scheduler looked at a connection, it does not read it again
// before its slot ends: schedule_after is past now for all of them, so the
// due query finds no row to check.
func TestScheduleSkipsConnectionsUntilTheirSlotEnds(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	slot := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	e.addConnections(t, 30, slot.Add(-48*time.Hour))
	now := slot.Add(5 * time.Minute)
	e.setNow(now)
	n, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	require.Equal(t, 30, n)

	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.connections WHERE schedule_after <= $1`, now))
	assert.Equal(t, 30, e.scalar(t, `SELECT count(*) FROM ohs_sync.connections WHERE schedule_after = $1`, slot.Add(time.Hour)),
		"the hourly slot of a provider without notifications")

	// A connection made in this slot is not due, and is not read again
	// either until the next one.
	e.connect(t, e.keyA, e.userA, "pu-1")
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.connections WHERE schedule_after <= $1`, now))
}

// A connection whose notifications arrive is looked at again when they
// will count as silent, not only at the end of its six hour slot.
func TestScheduleLooksAgainWhenNotificationsGoSilent(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	connected := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	e.setNow(connected)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	lastEvent := time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC)
	_, err := e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET last_event_at = $1`, lastEvent)
	require.NoError(t, err)

	// 12:00 slot of the six hour interval, on the next day: the
	// notifications go silent at 14:00, before the slot ends at 18:00.
	now := time.Date(2026, 10, 11, 12, 10, 0, 0, time.UTC)
	e.setNow(now)
	n, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	var after time.Time
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT schedule_after FROM ohs_sync.connections`).Scan(&after))
	assert.Equal(t, lastEvent.Add(collect.PushSilence), after.UTC())

	// Silent: the hourly slot applies at once.
	e.setNow(time.Date(2026, 10, 11, 14, 1, 0, 0, time.UTC))
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE to_time = $1`,
		time.Date(2026, 10, 11, 14, 0, 0, 0, time.UTC)))
}

// A provider that is down fails the pull on the long schedule: the job is
// not parked after five attempts, and it completes once the provider is back.
func TestProviderOutageDoesNotParkThePull(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	code, run, _ := e.syncNow(t, "")
	require.Equal(t, http.StatusAccepted, code)

	e.fake.fetchMode.Store("503")
	for i := range queue.MaxAttempts + 2 {
		e.retryDue(t)
		n, err := e.sched.PullOnce(ctx, 1)
		require.NoError(t, err)
		require.Equal(t, 1, n, "attempt %d", i+1)
	}
	got := e.runs(t, "").Data[0]
	require.Equal(t, run.ID, got.ID)
	assert.Equal(t, collect.RunRetrying, got.Status, "still retried after %d failures", queue.MaxAttempts+2)
	assert.Equal(t, queue.MaxAttempts+2, got.Attempts)
	var wait float64
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT extract(epoch FROM not_before - now()) FROM ohs_sync.pull_jobs WHERE id = $1`,
		run.ID).Scan(&wait))
	assert.InDelta(t, (64 * time.Minute).Seconds(), wait, 60, "the seventh failure waits about an hour")

	e.fake.fetchMode.Store("ok")
	e.retryDue(t)
	_, err := e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, collect.RunSucceeded, e.runs(t, "").Data[0].Status)
}

// A provider that cannot be reached at all is transient too.
func TestUnreachableProviderIsTransient(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	code, run, _ := e.syncNow(t, "")
	require.Equal(t, http.StatusAccepted, code)
	e.fake.srv.Close()
	n, err := e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	var wait float64
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT extract(epoch FROM not_before - now()) FROM ohs_sync.pull_jobs
		WHERE id = $1 AND status = 'pending'`, run.ID).Scan(&wait))
	assert.InDelta(t, queue.TransientBackoffBase.Seconds(), wait, 10)
}

func (e *connEnv) retryRun(t *testing.T, id int64) (int, collect.SyncRun) {
	t.Helper()
	rec := e.do(t, http.MethodPost, fmt.Sprintf("/v1/users/%s/sync-runs/%d/retry", e.userA, id), e.keyA, nil)
	var run collect.SyncRun
	if rec.Code < 300 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &run))
	}
	return rec.Code, run
}

// A run that gave up is queued again by id, or with every failed run of
// its connection.
func TestRetryFailedSyncRuns(t *testing.T) {
	e := newConnEnv(t)
	e.connect(t, e.keyA, e.userA, "pu-1")
	backfills := e.jobs(t, e.userA, "pending")
	require.Positive(t, backfills)

	// The provider answers 500 until every backfill chunk gave up.
	e.fake.fetchMode.Store("500")
	for range queue.MaxAttempts {
		e.retryDue(t)
		e.drain(t)
	}
	require.Equal(t, backfills, e.jobs(t, e.userA, "parked"))
	failed := e.runs(t, "").Data
	require.Equal(t, collect.RunFailed, failed[0].Status)
	e.fake.fetchMode.Store("ok")

	code, _ := e.retryRun(t, failed[0].ID+1000)
	assert.Equal(t, http.StatusNotFound, code)
	code, run := e.retryRun(t, failed[0].ID)
	require.Equal(t, http.StatusAccepted, code)
	assert.Equal(t, collect.RunQueued, run.Status)
	assert.Zero(t, run.Attempts)
	code, _ = e.retryRun(t, failed[0].ID)
	assert.Equal(t, http.StatusConflict, code, "already queued again")

	rec := e.do(t, http.MethodPost, "/v1/users/"+e.userA.String()+"/connections/"+fakeSlug+"/sync-runs/retry", e.keyA, nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var out collect.RetriedRuns
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.EqualValues(t, backfills-1, out.Retried)

	e.drain(t)
	assert.Equal(t, backfills, e.jobs(t, e.userA, "done"))
	assert.Equal(t, 0, e.jobs(t, e.userA, "parked"))

	// Another tenant's key reaches nothing.
	rec = e.do(t, http.MethodPost, fmt.Sprintf("/v1/users/%s/sync-runs/%d/retry", e.userA, failed[0].ID), e.keyB, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// A two year backfill is one job per chunk, the most recent first: a chunk
// that fails is retried alone, and a restart (a new scheduler over the same
// database) goes on with the chunks that are left.
func TestLongBackfillIsWalkedInChunksThatSurviveARestart(t *testing.T) {
	day := 24 * time.Hour
	e := newConnEnvTweaked(t, collect.SchedulerConfig{Concurrency: 8, BackfillPerProvider: -1, PerProvider: -1},
		func(c fakeConnector) connector.Connector { return c },
		func(d *collect.Deps) { d.BackfillWindow = 730 * day })
	ctx := context.Background()
	connected := time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC)
	e.setNow(connected)
	e.connect(t, e.keyA, e.userA, "pu-1")

	var n int
	var oldest, newest, firstTo time.Time
	var span float64
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*), min(from_time), max(to_time),
			extract(epoch FROM sum(to_time - from_time)),
			(SELECT to_time FROM ohs_sync.pull_jobs ORDER BY id LIMIT 1)
		FROM ohs_sync.pull_jobs WHERE user_id = $1 AND trigger = 'backfill'`, e.userA).Scan(&n, &oldest, &newest, &span, &firstTo))
	assert.Equal(t, 105, n, "730 days in 7 day chunks")
	assert.Equal(t, connected.Add(-730*day), oldest.UTC())
	assert.Equal(t, connected, newest.UTC())
	assert.Equal(t, (730 * day).Seconds(), span, "the chunks cover the window once")
	assert.Equal(t, connected, firstTo.UTC(), "the most recent chunk is queued first")

	// Ten chunks are pulled, then the provider goes down for one, and the
	// process stops.
	done, err := e.sched.PullOnce(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 10, done)
	e.fake.fetchMode.Store("503")
	_, err = e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	e.fake.fetchMode.Store("ok")
	fetched := e.fake.fetchN.Load()

	// A new scheduler: nothing is enqueued again, the ten done chunks are
	// not pulled again, and the other 95 are.
	restarted := collect.NewScheduler(e.deps, collect.SchedulerConfig{Concurrency: 8, BackfillPerProvider: -1, PerProvider: -1})
	_, err = restarted.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 105, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE trigger = 'backfill'`))
	e.retryDue(t)
	for range 20 {
		n, err := restarted.PullOnce(ctx, 16)
		require.NoError(t, err)
		if n == 0 {
			break
		}
	}
	assert.Equal(t, 105, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE trigger = 'backfill' AND status = 'done'`))
	assert.EqualValues(t, 95, e.fake.fetchN.Load()-fetched, "only the chunks left are pulled")
}
