// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A transient failure waits minutes then hours, capped, and the job is not
// parked at the fifth: a provider outage of a day leaves no hole.
func TestPullFailTransientBacksOffForHours(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := NewPull(pool)
	enqueuePull(t, pool, pullJob(1))

	var waits []time.Duration
	for i := 1; i < TransientMaxAttempts; i++ {
		j := claimPull(t, q)
		require.Equal(t, i-1, j.Attempts)
		require.NoError(t, q.FailTransient(ctx, j, errors.New("provider: HTTP 503")))
		var status, outcome string
		var wait float64
		require.NoError(t, pool.QueryRow(ctx, `SELECT status, outcome, extract(epoch FROM not_before - now())
			FROM ohs_sync.pull_jobs WHERE id = $1`, j.ID).Scan(&status, &outcome, &wait))
		require.Equal(t, "pending", status, "failure %d", i)
		require.Equal(t, OutcomeFailed, outcome)
		waits = append(waits, time.Duration(wait*float64(time.Second)).Round(time.Minute))
		mustExec(t, pool, `UPDATE ohs_sync.pull_jobs SET not_before = now()`)
	}
	assert.Equal(t, []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute,
		32 * time.Minute, 64 * time.Minute, 128 * time.Minute, 256 * time.Minute}, waits[:9])
	for _, w := range waits[9:] {
		assert.Equal(t, TransientBackoffMax, w, "capped")
	}

	// The last attempt parks it.
	j := claimPull(t, q)
	require.NoError(t, q.FailTransient(ctx, j, errors.New("provider: HTTP 503")))
	var status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM ohs_sync.pull_jobs WHERE id = $1`, j.ID).Scan(&status))
	assert.Equal(t, "parked", status)
}

func parkPull(t *testing.T, q *Pull, j PullJob) PullJob {
	t.Helper()
	enqueuePull(t, q.pool, j)
	mustExec(t, q.pool, `UPDATE ohs_sync.pull_jobs SET attempts = $1 WHERE status = 'pending' AND user_id = $2 AND from_time = $3`,
		MaxAttempts-1, j.UserID, j.From)
	got, err := q.Claim(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NoError(t, q.Fail(context.Background(), got[0], errors.New("provider: HTTP 500")))
	return got[0]
}

func pullStatus(t *testing.T, q *Pull, id int64) (status string, attempts int, outcome *string, finished *time.Time) {
	t.Helper()
	require.NoError(t, q.pool.QueryRow(context.Background(),
		`SELECT status, attempts, outcome, finished_at FROM ohs_sync.pull_jobs WHERE id = $1`, id).
		Scan(&status, &attempts, &outcome, &finished))
	return
}

func TestRetryPullQueuesAParkedJobAgain(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := NewPull(pool)
	j := parkPull(t, q, pullJob(1))
	status, _, _, _ := pullStatus(t, q, j.ID)
	require.Equal(t, "parked", status)

	assert.ErrorIs(t, RetryPull(ctx, pool, uuid.New(), j.ID), ErrNoJob, "another tenant's job")
	assert.ErrorIs(t, RetryPull(ctx, pool, j.TenantID, j.ID+1000), ErrNoJob)
	require.NoError(t, RetryPull(ctx, pool, j.TenantID, j.ID))
	status, attempts, outcome, finished := pullStatus(t, q, j.ID)
	assert.Equal(t, "pending", status)
	assert.Zero(t, attempts)
	assert.Nil(t, outcome)
	assert.Nil(t, finished)
	assert.ErrorIs(t, RetryPull(ctx, pool, j.TenantID, j.ID), ErrNotParked)

	got := claimPull(t, q)
	assert.Equal(t, j.ID, got.ID)
	require.NoError(t, q.Complete(ctx, got))
	assert.ErrorIs(t, RetryPull(ctx, pool, j.TenantID, j.ID), ErrNotParked, "a done job")
}

// A parked job whose window another job already covers cannot be queued
// twice (the idempotency index).
func TestRetryPullRefusesAWindowAlreadyQueued(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := NewPull(pool)
	job := pullJob(1)
	parked := parkPull(t, q, job)
	enqueuePull(t, pool, job)
	assert.ErrorIs(t, RetryPull(ctx, pool, job.TenantID, parked.ID), ErrPullQueued)
	n, err := RetryParkedPulls(ctx, pool, job.TenantID, job.UserID, job.Provider)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestRetryParkedPullsOfOneConnection(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := NewPull(pool)
	tenant, user := uuid.New(), uuid.New()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	window := func(i int, provider string, u uuid.UUID) PullJob {
		return PullJob{TenantID: tenant, UserID: u, Provider: provider,
			From: base.Add(time.Duration(i) * time.Hour), To: base.Add(time.Duration(i+1) * time.Hour)}
	}
	a := parkPull(t, q, window(1, "withings", user))
	b := parkPull(t, q, window(2, "withings", user))
	// The same window parked twice: only the latest is queued.
	b2 := parkPull(t, q, window(2, "withings", user))
	// Cancelled because the connection stopped: stays parked.
	cancelled := parkPull(t, q, window(3, "withings", user))
	mustExec(t, pool, `UPDATE ohs_sync.pull_jobs SET outcome = 'inactive' WHERE id = $1`, cancelled.ID)
	otherProvider := parkPull(t, q, window(1, "oura", user))
	otherUser := parkPull(t, q, window(1, "withings", uuid.New()))

	n, err := RetryParkedPulls(ctx, pool, tenant, user, "withings")
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	want := map[int64]string{a.ID: "pending", b.ID: "parked", b2.ID: "pending", cancelled.ID: "parked",
		otherProvider.ID: "parked", otherUser.ID: "parked"}
	for id, status := range want {
		got, _, _, _ := pullStatus(t, q, id)
		assert.Equal(t, status, got, "job %d", id)
	}
	n, err = RetryParkedPulls(ctx, pool, tenant, user, "withings")
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left to retry")
}

// EnqueuePulls inserts in one statement with the rules of EnqueuePull: the
// ids follow the order given, and a window already queued is skipped.
func TestEnqueuePullsInBulk(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	jobs := make([]PullJob, 0, 500)
	for i := range 500 {
		jobs = append(jobs, pullJob(i))
	}
	jobs[1].Trigger = TriggerBackfill
	jobs[2].Inbound = true
	jobs[2].DataType = "sleep"
	later := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	jobs[3].NotBefore = later
	enqueuePull(t, pool, jobs[0])

	var n int64
	inTx(t, pool, func(tx pgx.Tx) error {
		var err error
		n, err = EnqueuePulls(ctx, tx, jobs)
		return err
	})
	assert.EqualValues(t, 499, n, "the window already queued is skipped")
	inTx(t, pool, func(tx pgx.Tx) error {
		var err error
		n, err = EnqueuePulls(ctx, tx, append(jobs[:3:3], jobs[2]))
		return err
	})
	assert.Zero(t, n)
	inTx(t, pool, func(tx pgx.Tx) error {
		_, err := EnqueuePulls(ctx, tx, nil)
		return err
	})

	rows, err := pool.Query(ctx, `SELECT user_id, priority, trigger, coalesce(data_type, ''), not_before
		FROM ohs_sync.pull_jobs ORDER BY id`)
	require.NoError(t, err)
	type row struct {
		user      uuid.UUID
		priority  int
		trigger   string
		dataType  string
		notBefore time.Time
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.user, &x.priority, &x.trigger, &x.dataType, &x.notBefore)
		return x, err
	})
	require.NoError(t, err)
	require.Len(t, got, 500)
	for i, x := range got {
		require.Equal(t, jobs[i].UserID, x.user, "row %d follows the order given", i)
	}
	assert.Equal(t, row{jobs[1].UserID, priorityBackfill, TriggerBackfill, "", got[1].notBefore}, got[1])
	assert.Equal(t, row{jobs[2].UserID, priorityInbound, TriggerNotification, "sleep", got[2].notBefore}, got[2])
	assert.Equal(t, later, got[3].notBefore.UTC())
	assert.WithinDuration(t, time.Now(), got[4].notBefore, time.Minute)
	assert.Equal(t, priorityRoutine, got[4].priority)
}

// A pull on the long transient schedule whose instance dies (the claim
// expires) stays on it: the reclaim parks a pull at the transient ceiling,
// not at the fifth attempt.
func TestPullReclaimKeepsTheTransientSchedule(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := NewPull(pool)
	enqueuePull(t, pool, pullJob(1))
	expire := func() {
		mustExec(t, pool, `UPDATE ohs_sync.pull_jobs SET claimed_at = now() - interval '11 minutes' WHERE status = 'running'`)
	}

	// Four transient failures, then the instance is killed mid-pull.
	mustExec(t, pool, `UPDATE ohs_sync.pull_jobs SET attempts = $1`, MaxAttempts-1)
	j := claimPull(t, q)
	expire()
	again := claimPull(t, q)
	assert.Equal(t, j.ID, again.ID, "reclaimed, not parked")
	assert.Equal(t, MaxAttempts, again.Attempts, "the reclaim burns an attempt")
	require.NoError(t, q.FailTransient(ctx, again, errors.New("provider: HTTP 503")))
	status, _, _, _ := pullStatus(t, q, j.ID)
	assert.Equal(t, "pending", status)

	// Out of transient attempts: the reclaim parks it as failed.
	mustExec(t, pool, `UPDATE ohs_sync.pull_jobs SET attempts = $1, not_before = now()`, TransientMaxAttempts-1)
	claimPull(t, q)
	expire()
	got, err := q.Claim(ctx, 1)
	require.NoError(t, err)
	assert.Empty(t, got)
	status, attempts, outcome, finished := pullStatus(t, q, j.ID)
	assert.Equal(t, "parked", status)
	assert.Equal(t, TransientMaxAttempts, attempts)
	require.NotNil(t, outcome)
	assert.Equal(t, OutcomeFailed, *outcome)
	assert.NotNil(t, finished)
}

func pullWait(t *testing.T, q *Pull, id int64) (wait time.Duration, attempts int) {
	t.Helper()
	var secs float64
	require.NoError(t, q.pool.QueryRow(context.Background(),
		`SELECT extract(epoch FROM not_before - now()), attempts FROM ohs_sync.pull_jobs WHERE id = $1`, id).Scan(&secs, &attempts))
	return time.Duration(secs * float64(time.Second)), attempts
}

// A manual sync that meets a job waiting out a failure brings it forward: it
// runs now, and that attempt is not counted against the job's retries. A job
// waiting for a rate limit keeps waiting, and a routine enqueue moves nothing.
func TestEnqueuePullIDBringsABackedOffJobForward(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := NewPull(pool)
	manual := pullJob(1)
	manual.Inbound, manual.Trigger = true, TriggerManual
	routine := manual
	routine.Inbound, routine.Trigger = false, ""

	id, created := enqueuePullID(t, q, manual)
	require.True(t, created)
	for range 3 {
		j := claimPull(t, q)
		require.NoError(t, q.FailTransient(ctx, j, errors.New("provider: HTTP 503")))
		mustExec(t, pool, `UPDATE ohs_sync.pull_jobs SET not_before = now() WHERE id = $1`, id)
	}
	j := claimPull(t, q)
	require.NoError(t, q.FailTransient(ctx, j, errors.New("provider: HTTP 503")))
	wait, attempts := pullWait(t, q, id)
	require.Greater(t, wait, 7*time.Minute)
	require.Equal(t, 4, attempts)

	// The regular pull of the same window does not hurry it.
	again, created := enqueuePullID(t, q, routine)
	assert.False(t, created)
	assert.Equal(t, id, again)
	wait, attempts = pullWait(t, q, id)
	assert.Greater(t, wait, 7*time.Minute)
	assert.Equal(t, 4, attempts)

	// Sync now does.
	again, created = enqueuePullID(t, q, manual)
	assert.False(t, created)
	assert.Equal(t, id, again)
	wait, attempts = pullWait(t, q, id)
	assert.LessOrEqual(t, wait, time.Duration(0))
	assert.Equal(t, 3, attempts, "the attempt it brings forward is not counted")
	j = claimPull(t, q)
	assert.Equal(t, id, j.ID)

	// Deferred for the provider's rate limit: it waits as asked.
	until := time.Now().Add(time.Hour)
	require.NoError(t, q.Defer(ctx, j, until))
	_, created = enqueuePullID(t, q, manual)
	assert.False(t, created)
	wait, attempts = pullWait(t, q, id)
	assert.Greater(t, wait, 50*time.Minute)
	assert.Equal(t, 3, attempts)
}

// A notification for a window a backed off job covers brings that job
// forward (ExpediteCoveringPulls); jobs of another type or window stay.
func TestExpediteCoveringPulls(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := NewPull(pool)
	all := pullJob(1)
	sleep := all
	sleep.DataType = "sleep"
	enqueuePull(t, pool, all)
	enqueuePull(t, pool, sleep)
	for range 2 {
		j := claimPull(t, q)
		require.NoError(t, q.FailTransient(ctx, j, errors.New("provider: HTTP 503")))
	}
	var allID, sleepID int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM ohs_sync.pull_jobs WHERE data_type IS NULL`).Scan(&allID))
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM ohs_sync.pull_jobs WHERE data_type = 'sleep'`).Scan(&sleepID))

	expedite := func(j PullJob) (n int64) {
		inTx(t, pool, func(tx pgx.Tx) error {
			var err error
			n, err = ExpediteCoveringPulls(ctx, tx, j)
			return err
		})
		return n
	}
	outside := all
	outside.To = all.To.Add(time.Hour)
	assert.Zero(t, expedite(outside), "no job covers this window")

	steps := all
	steps.DataType = "steps"
	steps.From = all.From.Add(time.Minute)
	assert.EqualValues(t, 1, expedite(steps), "the job of every type covers it")
	wait, attempts := pullWait(t, q, allID)
	assert.LessOrEqual(t, wait, time.Duration(0))
	assert.Zero(t, attempts)
	wait, attempts = pullWait(t, q, sleepID)
	assert.Greater(t, wait, 30*time.Second, "the sleep job does not cover steps")
	assert.Equal(t, 1, attempts)
	assert.Zero(t, expedite(steps), "already due")
}
