// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/ops"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// The scheduler's hooks see every pull with what started it and how it
// ended, and the three loops of Run beat.
func TestSchedulerHooks(t *testing.T) {
	type pulled struct{ trigger, provider, outcome string }
	var (
		mu  sync.Mutex
		got []pulled
	)
	loops := ops.NewRegistry()
	started := time.Now()
	hooks := collect.SchedulerHooks{
		Schedule:  loops.Loop("pull_scheduler", collect.DefaultTick),
		Subscribe: loops.Loop("pull_subscriber", collect.DefaultTick),
		Work:      loops.Loop("pull_workers", 2*time.Second),
		Pulled: func(job queue.PullJob, outcome string, took time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			assert.Positive(t, took)
			got = append(got, pulled{job.TriggerName(), job.Provider, outcome})
		},
	}
	e := newConnEnvWith(t, collect.SchedulerConfig{Concurrency: 8, Hooks: hooks,
		IdleMin: 10 * time.Millisecond, IdleMax: 20 * time.Millisecond})
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	mu.Lock()
	require.NotEmpty(t, got, "the backfill of a new connection is pulled")
	for _, p := range got {
		assert.Equal(t, pulled{queue.TriggerBackfill, fakeSlug, "completed"}, p)
	}
	got = nil
	mu.Unlock()

	e.fake.fetchMode.Store("500")
	code, _, _ := e.syncNow(t, "")
	require.Equal(t, http.StatusAccepted, code)
	n, err := e.sched.PullOnce(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	mu.Lock()
	assert.Equal(t, []pulled{{queue.TriggerManual, fakeSlug, "failed"}}, got)
	mu.Unlock()
	assert.Empty(t, loops.Stalled(), "a pull that ended leaves no work open on the loop")

	// Run: each loop beats at its first round and the workers keep beating.
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.sched.Run(ctx) }()
	beat := func(after time.Time) bool {
		for _, s := range loops.States() {
			if !s.LastTick.After(after) {
				return false
			}
		}
		return true
	}
	require.Eventually(t, func() bool { return beat(started.Add(10 * time.Millisecond)) }, 10*time.Second, 10*time.Millisecond)
	var workers time.Time
	for _, s := range loops.States() {
		if s.Name == "pull_workers" {
			workers = s.LastTick
		}
	}
	require.Eventually(t, func() bool {
		for _, s := range loops.States() {
			if s.Name == "pull_workers" {
				return s.LastTick.After(workers)
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "an idle worker loop beats at every poll")
	cancel()
	require.NoError(t, <-done)
}
