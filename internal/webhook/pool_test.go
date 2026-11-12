// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeQueue hands out queued jobs, at most n per claim, and can hold some
// back until release is closed (an endpoint at its in-flight cap).
type fakeQueue struct {
	mu      sync.Mutex
	ready   []job
	held    []job
	release chan struct{}
	asked   []int
}

func (q *fakeQueue) claim(_ context.Context, n int) ([]job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.asked = append(q.asked, n)
	if q.release != nil {
		select {
		case <-q.release:
			q.ready, q.held, q.release = append(q.ready, q.held...), nil, nil
		default:
		}
	}
	k := min(n, len(q.ready))
	out := q.ready[:k:k]
	q.ready = q.ready[k:]
	return out, nil
}

func jobs(ids ...int64) []job {
	var out []job
	for _, id := range ids {
		out = append(out, job{id: id})
	}
	return out
}

// pumpWorker is a Worker without a database: claims come from q and
// deliver records what it posts.
func pumpWorker(q *fakeQueue, concurrency int, idle time.Duration, deliver func(job)) *Worker {
	w := NewWorker(nil, nil, WorkerConfig{Concurrency: concurrency, Idle: idle})
	w.claimFn = q.claim
	w.deliverFn = func(_ context.Context, j job) error {
		deliver(j)
		return nil
	}
	return w
}

func runPump(t *testing.T, w *Worker) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		assert.NoError(t, w.pump(ctx, func() {}))
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("pump did not return")
		}
	}
}

// One delivery that hangs keeps one slot: the others keep flowing through
// the free slots without waiting for it.
func TestPumpFreesSlotsAtOnce(t *testing.T) {
	q := &fakeQueue{ready: jobs(1, 2, 3, 4, 5, 6, 7)}
	hang := make(chan struct{})
	got := make(chan int64, 16)
	w := pumpWorker(q, 2, 5*time.Millisecond, func(j job) {
		if j.id == 1 {
			<-hang
		}
		got <- j.id
	})
	stop := runPump(t, w)
	var fast []int64
	for range 6 {
		select {
		case id := <-got:
			fast = append(fast, id)
		case <-time.After(5 * time.Second):
			t.Fatalf("only %v delivered while delivery 1 hangs", fast)
		}
	}
	assert.ElementsMatch(t, []int64{2, 3, 4, 5, 6, 7}, fast)
	close(hang)
	assert.Equal(t, int64(1), <-got)
	stop()
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, n := range q.asked {
		assert.LessOrEqual(t, n, 2, "never asks for more than the free slots")
		assert.Positive(t, n)
	}
}

// A finished delivery triggers a claim at once: a delivery that was held
// back while another was in flight does not wait for the idle poll.
func TestPumpClaimsWhenADeliveryFinishes(t *testing.T) {
	q := &fakeQueue{ready: jobs(1), held: jobs(2), release: make(chan struct{})}
	got := make(chan int64, 4)
	w := pumpWorker(q, 4, time.Hour, func(j job) {
		if j.id == 1 {
			close(q.release)
		}
		got <- j.id
	})
	stop := runPump(t, w)
	defer stop()
	require.Equal(t, int64(1), <-got)
	select {
	case id := <-got:
		assert.Equal(t, int64(2), id)
	case <-time.After(5 * time.Second):
		t.Fatal("the held delivery waited for the idle poll")
	}
}

// Shutdown waits for the attempts in flight.
func TestPumpWaitsForInFlight(t *testing.T) {
	q := &fakeQueue{ready: jobs(1)}
	started, finish := make(chan struct{}), make(chan struct{})
	var finished bool
	var mu sync.Mutex
	w := pumpWorker(q, 2, 5*time.Millisecond, func(job) {
		close(started)
		<-finish
		mu.Lock()
		finished = true
		mu.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		assert.NoError(t, w.pump(ctx, func() {}))
	}()
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("returned with an attempt in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(finish)
	<-done
	mu.Lock()
	defer mu.Unlock()
	assert.True(t, finished)
}

func TestWorkerDefaults(t *testing.T) {
	w := NewWorker(nil, nil, WorkerConfig{})
	assert.Equal(t, 8, DefaultConcurrency)
	assert.Equal(t, DefaultConcurrency, w.concurrency)
	assert.Equal(t, DefaultPerEndpoint, w.perEndpoint)
	assert.Equal(t, DefaultConcurrency/2, w.perTenant, "half the slots for one tenant")
	assert.Equal(t, DefaultPauseAfter, w.pauseAfter)
	assert.Equal(t, time.Second, w.idle)
	assert.Equal(t, pauseSchedule(DefaultPauseBase, DefaultPauseMax), w.pauses)

	w = NewWorker(nil, nil, WorkerConfig{Concurrency: 3, PerEndpoint: 9, PauseAfter: -1})
	assert.Equal(t, 3, w.concurrency)
	assert.Equal(t, 3, w.perEndpoint, "never more per endpoint than the worker posts at once")
	assert.Equal(t, 1, w.perTenant)
	assert.Zero(t, w.pauseAfter, "a negative PauseAfter turns the pause off")
	assert.Equal(t, 1, NewWorker(nil, nil, WorkerConfig{Concurrency: 1}).perTenant, "at least one")
	assert.Equal(t, 5, NewWorker(nil, nil, WorkerConfig{Concurrency: 6, PerTenant: 5}).perTenant)
	assert.Equal(t, 6, NewWorker(nil, nil, WorkerConfig{Concurrency: 6, PerTenant: 50}).perTenant,
		"never more per tenant than the worker posts at once")
}

func TestPauseSchedule(t *testing.T) {
	assert.Equal(t, []float64{30, 60, 120, 240, 480, 600}, pauseSchedule(30*time.Second, 10*time.Minute))
	assert.Equal(t, []float64{5}, pauseSchedule(5*time.Second, 5*time.Second))
	assert.Equal(t, []float64{1}, pauseSchedule(10*time.Second, time.Second), "the maximum wins")
	assert.Equal(t, []float64{0}, pauseSchedule(0, 0))
}
