// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package queue

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// userJob is the i-th ETL job of one user.
func userJob(tenant, user uuid.UUID, i int) ETLJob {
	return ETLJob{TenantID: tenant, UserID: user, Provider: "apple_health",
		Key: fmt.Sprintf("raw/%s/%d", user, i), Tier: TierStandard}
}

// One user's import of thousands of parts must not take every slot: the
// claim hands out one job per user, and the users queued behind the import
// get theirs at once.
func TestETLClaimIsFairAcrossUsers(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := NewETL(pool)
	tenant, big := uuid.New(), uuid.New()
	for i := range 600 {
		enqueueETL(t, pool, userJob(tenant, big, i))
	}
	others := map[uuid.UUID]bool{}
	for i := range 3 {
		u := uuid.New()
		others[u] = true
		enqueueETL(t, pool, userJob(tenant, u, i))
	}

	// The import is older than everything else, and still takes one slot.
	first, err := q.Claim(ctx, "standard", 8)
	require.NoError(t, err)
	require.Len(t, first, 1)
	assert.Equal(t, big, first[0].UserID)

	// While that job runs, the other users are served, and the import
	// gets nothing more.
	got, err := q.Claim(ctx, "standard", 8)
	require.NoError(t, err)
	require.Len(t, got, 3, "the users behind the import are not starved")
	for _, j := range got {
		assert.True(t, others[j.UserID], "job of user %s", j.UserID)
	}
	again, err := q.Claim(ctx, "standard", 8)
	require.NoError(t, err)
	assert.Empty(t, again, "one job per user at a time")

	// Its next job follows once the first is settled, in queue order.
	require.NoError(t, q.Complete(ctx, first[0]))
	next, err := q.Claim(ctx, "standard", 8)
	require.NoError(t, err)
	require.Len(t, next, 1)
	assert.Equal(t, big, next[0].UserID)
	assert.Greater(t, next[0].ID, first[0].ID)
}

// A user's jobs run one at a time in queue order (not_before, then id).
func TestETLClaimKeepsAUsersOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := NewETL(pool)
	tenant, user := uuid.New(), uuid.New()
	for i := range 5 {
		enqueueETL(t, pool, userJob(tenant, user, i))
	}
	var ids []int64
	for range 5 {
		j := claimOne(t, q)
		ids = append(ids, j.ID)
		require.NoError(t, q.Complete(ctx, j))
	}
	assert.IsIncreasing(t, ids)
}

// PerUser raises the cap; a negative value removes it.
func TestETLClaimPerUserCap(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user := uuid.New(), uuid.New()
	for i := range 10 {
		enqueueETL(t, pool, userJob(tenant, user, i))
	}
	q := NewETL(pool)
	q.PerUser = 3
	got, err := q.Claim(ctx, "standard", 8)
	require.NoError(t, err)
	assert.Len(t, got, 3)
	got, err = q.Claim(ctx, "standard", 8)
	require.NoError(t, err)
	assert.Empty(t, got)

	q.PerUser = -1
	got, err = q.Claim(ctx, "standard", 8)
	require.NoError(t, err)
	assert.Len(t, got, 7, "no cap")
}

// The cap holds when several instances claim at once.
func TestETLClaimCapHoldsAcrossConcurrentClaims(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user := uuid.New(), uuid.New()
	for i := range 40 {
		enqueueETL(t, pool, userJob(tenant, user, i))
	}
	var mu sync.Mutex
	total := 0
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := NewETL(pool).Claim(ctx, "standard", 4)
			assert.NoError(t, err)
			mu.Lock()
			total += len(got)
			mu.Unlock()
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, total, "eight instances claiming at once run one job of the user")
}

// A job whose claim expired is taken again before the user's waiting jobs,
// and a live claim of the user holds it back like any other.
func TestETLReclaimStaysWithinTheUserCap(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := NewETL(pool)
	tenant, user := uuid.New(), uuid.New()
	for i := range 3 {
		enqueueETL(t, pool, userJob(tenant, user, i))
	}
	first := claimOne(t, q)
	mustExec(t, pool, `UPDATE ohs_sync.etl_jobs SET claimed_at = now() - interval '11 minutes' WHERE id = $1`, first.ID)
	got, err := q.Claim(ctx, "standard", 8)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, first.ID, got[0].ID, "the expired claim goes first")
	assert.Equal(t, 1, got[0].Attempts)
	assert.ErrorIs(t, q.Complete(ctx, first), ErrNotRunning, "the stale claim is fenced")
}

// A provider whose backlog is far past its cap must not hide the other
// providers' jobs.
func TestPullClaimIsNotHiddenByOneProvidersBacklog(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	// 12,000 withings jobs, all older than the oura job.
	mustExec(t, pool, `
		INSERT INTO ohs_sync.pull_jobs (tenant_id, user_id, provider, from_time, to_time, not_before, priority, trigger)
		SELECT gen_random_uuid(), gen_random_uuid(), 'withings', now() - interval '2 hours', now() - interval '1 hour',
			now() - interval '1 hour', 1, 'scheduled'
		FROM generate_series(1, 12000)`)
	enqueuePull(t, pool, providerJob(1, "oura", TriggerScheduled))

	q := NewPull(pool)
	q.Limits = PullLimits{PerProvider: 2}
	got, err := q.Claim(ctx, 8)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"withings/scheduled": 2, "oura/scheduled": 1}, countBy(got))

	// With withings at its cap, a new oura job is still found.
	enqueuePull(t, pool, providerJob(2, "oura", TriggerScheduled))
	got, err = q.Claim(ctx, 8)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"oura/scheduled": 1}, countBy(got))
}

// A pull whose claim expired is queued again with an attempt burned, and
// claimed like any due job; one out of attempts is parked.
func TestPullReclaimOfExpiredClaims(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := NewPull(pool)
	enqueuePull(t, pool, providerJob(1, "withings", TriggerScheduled))
	enqueuePull(t, pool, providerJob(2, "oura", TriggerScheduled))
	first, err := q.Claim(ctx, 8)
	require.NoError(t, err)
	require.Len(t, first, 2)
	var live, poison PullJob
	for _, j := range first {
		if j.Provider == "withings" {
			live = j
		} else {
			poison = j
		}
	}
	mustExec(t, pool, `UPDATE ohs_sync.pull_jobs SET claimed_at = now() - interval '11 minutes' WHERE id = $1`, live.ID)
	mustExec(t, pool, `UPDATE ohs_sync.pull_jobs SET claimed_at = now() - interval '11 minutes', attempts = $2 WHERE id = $1`,
		poison.ID, TransientMaxAttempts-1)

	got, err := q.Claim(ctx, 8)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, live.ID, got[0].ID)
	assert.Equal(t, 1, got[0].Attempts, "the reclaim burns an attempt")
	assert.ErrorIs(t, q.Complete(ctx, live), ErrNotRunning, "the stale claim is fenced")
	require.NoError(t, q.Complete(ctx, got[0]))

	var status, outcome, lastErr string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status, outcome, last_error FROM ohs_sync.pull_jobs WHERE id = $1`,
		poison.ID).Scan(&status, &outcome, &lastErr))
	assert.Equal(t, "parked", status)
	assert.Equal(t, OutcomeFailed, outcome)
	assert.Equal(t, "visibility timeout expired", lastErr)
}
