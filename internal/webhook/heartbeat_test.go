// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/ops"
)

// A claim that never returns (a database call that hangs) stops the loop:
// its heartbeat goes silent and the registry names it. An empty queue polled
// every few milliseconds is alive.
func TestPollingDeliveryLoopIsAlive(t *testing.T) {
	loops := ops.NewRegistry().WithMinStall(0)
	w := pumpWorker(&fakeQueue{}, 2, 5*time.Millisecond, func(job) {})
	w.loop = loops.Loop("webhooks", 50*time.Millisecond)
	stop := runPump(t, w)
	assert.Never(t, func() bool { return len(loops.Stalled()) > 0 }, 300*time.Millisecond, 10*time.Millisecond)
	stop()
}

func TestHungClaimStallsTheDeliveryLoop(t *testing.T) {
	loops := ops.NewRegistry().WithMinStall(0)
	w := NewWorker(nil, nil, WorkerConfig{Concurrency: 2, Idle: 5 * time.Millisecond,
		Loop: loops.Loop("webhooks", 50*time.Millisecond)})
	release := make(chan struct{})
	w.claimFn = func(ctx context.Context, n int) ([]job, error) {
		<-release // ignores its context, like a stuck driver call
		return nil, nil
	}
	w.deliverFn = func(context.Context, job) error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		assert.NoError(t, w.pump(ctx, func() {}))
	}()

	require.Eventually(t, func() bool { return len(loops.Stalled()) == 1 }, 5*time.Second, 10*time.Millisecond)
	s := loops.Stalled()[0]
	assert.Equal(t, "webhooks", s.Loop)
	assert.False(t, s.Overdue, "no delivery in flight: the loop itself is stuck")

	// The call comes back: the loop beats again.
	close(release)
	require.Eventually(t, func() bool { return len(loops.Stalled()) == 0 }, 5*time.Second, 10*time.Millisecond)
	cancel()
	<-done
}

// Every slot held by a slow receiver: the loop waits for a slot instead of
// polling. It is busy, not hung, and each attempt is observed.
func TestSlowDeliveriesDoNotStallTheLoop(t *testing.T) {
	loops := ops.NewRegistry().WithMinStall(0)
	q := &fakeQueue{ready: jobs(1, 2)}
	release := make(chan struct{})
	w := pumpWorker(q, 2, 5*time.Millisecond, func(job) { <-release })
	w.loop = loops.Loop("webhooks", 50*time.Millisecond)
	stop := runPump(t, w)

	assert.Never(t, func() bool { return len(loops.Stalled()) > 0 }, 400*time.Millisecond, 10*time.Millisecond,
		"two deliveries in flight on two slots")
	close(release)
	assert.Never(t, func() bool { return len(loops.Stalled()) > 0 }, 300*time.Millisecond, 10*time.Millisecond)
	stop()
}
