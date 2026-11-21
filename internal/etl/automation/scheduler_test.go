// SPDX-License-Identifier: AGPL-3.0-only

package automation

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// nopStore satisfies Deps.Store for tests that never touch it.
type nopStore struct{ rawstore.Store }

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

// taskFn returns the run function of the named task.
func taskFn(t *testing.T, s *Scheduler, name string) func(context.Context) error {
	t.Helper()
	for _, tk := range s.tasks {
		if tk.name == name {
			return tk.fn
		}
	}
	t.Fatalf("task %q not registered", name)
	return nil
}

// With a getter the retention, audit and tracking tasks are always
// registered, and each run reads the getter: off does nothing, then a value
// prunes with it.
func TestRetentionTasksReadTheGetter(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	var days, auditDays, trackDays atomic.Int64
	var retentionCalls, auditCalls, trackCalls []int
	var auditCutoffs []time.Time
	s, err := newScheduler(Deps{
		Pool: &pgxpool.Pool{}, Store: nopStore{}, Now: func() time.Time { return now },
		RetentionDaysFunc:         func() int { return int(days.Load()) },
		AuditRetentionDaysFunc:    func() int { return int(auditDays.Load()) },
		TrackingRetentionDaysFunc: func() int { return int(trackDays.Load()) },
		retentionRun:              func(_ context.Context, d int) error { retentionCalls = append(retentionCalls, d); return nil },
		auditPrune: func(_ context.Context, c time.Time) (int64, error) {
			auditCalls = append(auditCalls, 1)
			auditCutoffs = append(auditCutoffs, c)
			return 0, nil
		},
		trackingRun: func(_ context.Context, d int) error { trackCalls = append(trackCalls, d); return nil },
	})
	require.NoError(t, err)
	ctx := context.Background()
	ret, aud, trk := taskFn(t, s, "retention"), taskFn(t, s, "audit"), taskFn(t, s, "tracking")

	// First tick: retention and audit off, tracking off (negative keeps for ever).
	trackDays.Store(-1)
	require.NoError(t, ret(ctx))
	require.NoError(t, aud(ctx))
	require.NoError(t, trk(ctx))
	require.Empty(t, retentionCalls)
	require.Empty(t, auditCalls)
	require.Empty(t, trackCalls)

	// Second tick: values set.
	days.Store(30)
	auditDays.Store(7)
	trackDays.Store(0)
	require.NoError(t, ret(ctx))
	require.NoError(t, aud(ctx))
	require.NoError(t, trk(ctx))
	require.Equal(t, []int{30}, retentionCalls)
	require.Equal(t, []time.Time{now.AddDate(0, 0, -7)}, auditCutoffs)
	require.Equal(t, []int{DefaultTrackingRetentionDays}, trackCalls, "zero is the default for tracking")
}

// Without a getter a zero or negative static value registers nothing, as
// before.
func TestRetentionTasksStaticStillGate(t *testing.T) {
	s, err := newScheduler(Deps{Pool: &pgxpool.Pool{}, Store: nopStore{}, TrackingRetentionDays: -1})
	require.NoError(t, err)
	for _, tk := range s.tasks {
		require.NotContains(t, []string{"retention", "audit", "tracking"}, tk.name)
	}
}
