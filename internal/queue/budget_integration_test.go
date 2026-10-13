// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package queue

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func providerJob(i int, provider, trigger string) PullJob {
	j := pullJob(i)
	j.Provider, j.Trigger = provider, trigger
	return j
}

func countBy(jobs []PullJob) map[string]int {
	out := map[string]int{}
	for _, j := range jobs {
		out[j.Provider+"/"+j.Trigger]++
	}
	return out
}

func TestPullLimitsCapEachProvider(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	for i := range 6 {
		enqueuePull(t, pool, providerJob(i, "withings", TriggerBackfill))
	}
	enqueuePull(t, pool, providerJob(10, "withings", TriggerScheduled))
	enqueuePull(t, pool, providerJob(11, "withings", TriggerScheduled))
	enqueuePull(t, pool, providerJob(12, "oura", TriggerBackfill))
	enqueuePull(t, pool, providerJob(13, "oura", TriggerScheduled))

	q := NewPull(pool)
	q.Limits = PullLimits{PerProvider: 3, BackfillPerProvider: 1}
	got, err := q.Claim(ctx, 20)
	require.NoError(t, err)
	// Regular pulls first, then one backfill each; withings stops at 3.
	assert.Equal(t, map[string]int{
		"withings/scheduled": 2, "withings/backfill": 1,
		"oura/scheduled": 1, "oura/backfill": 1,
	}, countBy(got))

	// Running pulls count: nothing more until one finishes, across claims.
	again, err := q.Claim(ctx, 20)
	require.NoError(t, err)
	assert.Empty(t, again)
	for _, j := range got {
		if j.Provider == "withings" && j.Trigger == TriggerBackfill {
			require.NoError(t, q.Complete(ctx, j))
		}
	}
	again, err = q.Claim(ctx, 20)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"withings/backfill": 1}, countBy(again))
}

func TestPullLimitsHoldAcrossConcurrentClaims(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	for i := range 40 {
		enqueuePull(t, pool, providerJob(i, "withings", TriggerScheduled))
	}
	var mu sync.Mutex
	total := 0
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := NewPull(pool)
			q.Limits = PullLimits{PerProvider: 4}
			got, err := q.Claim(ctx, 3)
			assert.NoError(t, err)
			mu.Lock()
			total += len(got)
			mu.Unlock()
		}()
	}
	wg.Wait()
	assert.Equal(t, 4, total, "eight instances claiming at once stay within the cap")
}

func TestBackfillsGoLast(t *testing.T) {
	pool := testPool(t)
	q := NewPull(pool)
	enqueuePull(t, pool, providerJob(1, "withings", TriggerBackfill))
	enqueuePull(t, pool, providerJob(2, "withings", TriggerScheduled))
	j := providerJob(3, "withings", TriggerNotification)
	j.Inbound = true
	enqueuePull(t, pool, j)
	var order []string
	for range 3 {
		order = append(order, claimPull(t, q).Trigger)
	}
	assert.Equal(t, []string{TriggerNotification, TriggerScheduled, TriggerBackfill}, order)
}

func TestPausedProviderIsSkipped(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := NewPull(pool)
	enqueuePull(t, pool, providerJob(1, "withings", TriggerScheduled))
	enqueuePull(t, pool, providerJob(2, "oura", TriggerScheduled))

	until, err := q.PauseProvider(ctx, "withings", 2*time.Minute)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(2*time.Minute), until, 5*time.Second)
	got, err := q.Claim(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "oura", got[0].Provider)
	paused, err := q.PausedProviders(ctx)
	require.NoError(t, err)
	assert.Contains(t, paused, "withings")

	// Limits in a row double the pause from the base; the cap holds.
	mustExec(t, pool, `DELETE FROM ohs_core.provider_limits`)
	var pauses []time.Duration
	for range 7 {
		start := time.Now()
		until, err := q.PauseProvider(ctx, "fitbit", time.Second)
		require.NoError(t, err)
		pauses = append(pauses, until.Sub(start).Round(time.Second))
		mustExec(t, pool, `UPDATE ohs_core.provider_limits SET paused_until = now()`)
	}
	assert.Equal(t, []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute,
		8 * time.Minute, ProviderPauseMax, ProviderPauseMax}, pauses)
	// A day's Retry-After pauses the provider for the cap only.
	until, err = q.PauseProvider(ctx, "strava", 24*time.Hour)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(ProviderPauseMax), until, 5*time.Second)

	// A success resets the doubling.
	require.NoError(t, q.ProviderOK(ctx, "fitbit"))
	start := time.Now()
	until, err = q.PauseProvider(ctx, "fitbit", time.Second)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, until.Sub(start).Round(time.Second))

	// Once the pause ends, its jobs are claimed again.
	mustExec(t, pool, `UPDATE ohs_core.provider_limits SET paused_until = now() - interval '1 second'`)
	got, err = q.Claim(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "withings", got[0].Provider)
}
