// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package queue

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The hooks see every round of a worker loop (a claim, even an empty one)
// and every job from its claim to its settle.
func TestETLHooks(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var events []string
	q := NewETL(pool)
	q.Hooks = ETLHooks{
		Poll:    func(tier string) { events = append(events, "poll "+tier) },
		Claimed: func(j ETLJob) { events = append(events, fmt.Sprint("claimed ", j.Tier, " ", j.Key)) },
		Settled: func(j ETLJob, outcome string) { events = append(events, fmt.Sprint("settled ", j.Key, " ", outcome)) },
	}

	got, err := q.Claim(ctx, TierHighFreq, 4)
	require.NoError(t, err)
	require.Empty(t, got)
	require.Equal(t, []string{"poll high_freq"}, events, "an empty claim is a heartbeat too")

	events = nil
	enqueueETL(t, pool, etlJob(1, TierStandard))
	enqueueETL(t, pool, etlJob(2, TierStandard))
	enqueueETL(t, pool, etlJob(3, TierStandard))
	jobs, err := q.Claim(ctx, TierStandard, 4)
	require.NoError(t, err)
	require.Len(t, jobs, 3)
	require.Equal(t, []string{"poll standard", "claimed standard raw/k1", "claimed standard raw/k2", "claimed standard raw/k3"}, events)

	events = nil
	require.NoError(t, q.Complete(ctx, jobs[0]))
	require.NoError(t, q.Fail(ctx, jobs[1], errors.New("boom")))
	// The claim of the third is gone: its settle is a lost claim.
	mustExec(t, pool, `UPDATE ohs_sync.etl_jobs SET claimed_at = claimed_at + interval '1 second' WHERE id = $1`, jobs[2].ID)
	require.ErrorIs(t, q.Complete(ctx, jobs[2]), ErrNotRunning)
	require.Equal(t, []string{"settled raw/k1 completed", "settled raw/k2 failed", "settled raw/k3 claim_lost"}, events)
}

func TestBacklog(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Empty queues: every tier is there, at zero.
	tiers, err := ETLBacklog(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, map[string]Backlog{TierStandard: {}, TierHighFreq: {}}, tiers)
	pulls, err := PullBacklog(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, Backlog{}, pulls)

	for i := range 5 {
		enqueueETL(t, pool, etlJob(i, TierStandard))
	}
	enqueueETL(t, pool, etlJob(10, TierHighFreq))
	// Due for five minutes, due for one, waiting for a retry, claimed, done.
	mustExec(t, pool, `UPDATE ohs_sync.etl_jobs SET not_before = now() - interval '5 minutes' WHERE key = 'raw/k0'`)
	mustExec(t, pool, `UPDATE ohs_sync.etl_jobs SET not_before = now() - interval '1 minute' WHERE key = 'raw/k1'`)
	mustExec(t, pool, `UPDATE ohs_sync.etl_jobs SET not_before = now() + interval '1 hour' WHERE key = 'raw/k2'`)
	mustExec(t, pool, `UPDATE ohs_sync.etl_jobs SET status = 'running', claimed_at = now() WHERE key = 'raw/k3'`)
	mustExec(t, pool, `UPDATE ohs_sync.etl_jobs SET status = 'done' WHERE key = 'raw/k4'`)

	tiers, err = ETLBacklog(ctx, pool)
	require.NoError(t, err)
	std := tiers[TierStandard]
	require.EqualValues(t, 2, std.Depth, "only the jobs due and not claimed")
	require.InDelta(t, (5 * time.Minute).Seconds(), std.OldestAge.Seconds(), 30)
	require.EqualValues(t, 1, tiers[TierHighFreq].Depth)
	require.Less(t, tiers[TierHighFreq].OldestAge, time.Minute)

	enqueuePull(t, pool, pullJob(1))
	enqueuePull(t, pool, pullJob(2))
	mustExec(t, pool, `UPDATE ohs_sync.pull_jobs SET not_before = now() - interval '10 minutes'
		WHERE id = (SELECT min(id) FROM ohs_sync.pull_jobs)`)
	mustExec(t, pool, `UPDATE ohs_sync.pull_jobs SET not_before = now() + interval '1 hour'
		WHERE id = (SELECT max(id) FROM ohs_sync.pull_jobs)`)
	pulls, err = PullBacklog(ctx, pool)
	require.NoError(t, err)
	require.EqualValues(t, 1, pulls.Depth, "a pull deferred by a rate limit is not due")
	require.InDelta(t, (10 * time.Minute).Seconds(), pulls.OldestAge.Seconds(), 30)
}
