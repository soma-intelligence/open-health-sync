// SPDX-License-Identifier: AGPL-3.0-only

package automation

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSchedulerRunsTaskRepeatedly(t *testing.T) {
	var n atomic.Int32
	s := NewScheduler(0)
	s.Add("tick", 10*time.Millisecond, time.Second, func(context.Context) error { n.Add(1); return nil })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	require.Eventually(t, func() bool { return n.Load() >= 3 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestSchedulerErrorAndPanicDoNotStopLoop(t *testing.T) {
	var errs, panics atomic.Int32
	s := NewScheduler(0)
	s.Add("err", 10*time.Millisecond, time.Second, func(context.Context) error { errs.Add(1); return errors.New("boom") })
	s.Add("panic", 10*time.Millisecond, time.Second, func(context.Context) error { panics.Add(1); panic("kaboom") })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	require.Eventually(t, func() bool { return errs.Load() >= 3 && panics.Load() >= 3 }, 2*time.Second, 5*time.Millisecond)
}

func TestSchedulerRunsAreBoundedByTimeout(t *testing.T) {
	var timedOut atomic.Int32
	s := NewScheduler(0)
	s.Add("slow", 10*time.Millisecond, 20*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		timedOut.Add(1)
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	require.Eventually(t, func() bool { return timedOut.Load() >= 2 }, 2*time.Second, 5*time.Millisecond)
}

func TestSchedulerDoesNotOverlapRuns(t *testing.T) {
	var running, maxRunning atomic.Int32
	s := NewScheduler(0)
	s.Add("t", time.Millisecond, time.Second, func(context.Context) error {
		c := running.Add(1)
		if c > maxRunning.Load() {
			maxRunning.Store(c)
		}
		time.Sleep(15 * time.Millisecond)
		running.Add(-1)
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	require.EqualValues(t, 1, maxRunning.Load())
}

func TestJitteredStaysInBand(t *testing.T) {
	for range 200 {
		d := jittered(time.Minute, 0.1)
		require.GreaterOrEqual(t, d, 54*time.Second)
		require.LessOrEqual(t, d, 66*time.Second)
	}
	require.Equal(t, time.Minute, jittered(time.Minute, 0))
}

// The tick hook is told at the top of every iteration, the one before the
// first run included, with the task's name and interval.
func TestSchedulerTicksEveryIteration(t *testing.T) {
	var runs, ticks atomic.Int32
	var wrong atomic.Bool
	s := NewScheduler(0)
	s.onTick = func(task string, interval time.Duration) {
		if task != "tick" || interval != 10*time.Millisecond {
			wrong.Store(true)
		}
		ticks.Add(1)
	}
	s.Add("tick", 10*time.Millisecond, time.Second, func(context.Context) error { runs.Add(1); return errors.New("boom") })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return runs.Load() >= 3 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	<-done
	require.False(t, wrong.Load())
	require.Greater(t, ticks.Load(), runs.Load()-1, "one tick before each run, failed or not")
}
