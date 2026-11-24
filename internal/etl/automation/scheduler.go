// SPDX-License-Identifier: AGPL-3.0-only

package automation

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/soma-intelligence/open-health-sync/internal/logging"
	"github.com/soma-intelligence/open-health-sync/internal/logutil"
)

type task struct {
	name     string
	interval time.Duration
	timeout  time.Duration
	fn       func(ctx context.Context) error
}

// Scheduler runs named periodic tasks, each on its own goroutine. A task never
// overlaps itself, every run is bounded by its timeout, and an error or panic
// is logged and never stops the loop.
type Scheduler struct {
	tasks  []task
	jitter float64
	// onTick, when set, is told at the top of every iteration of a task's
	// loop, before the wait: the task's name and interval (Deps.OnTick).
	onTick func(task string, interval time.Duration)
}

// NewScheduler returns an empty scheduler. jitter is the fraction of the
// interval by which each wait varies (0.1 means plus or minus 10 percent), so
// several instances do not hit the database at the same moment.
func NewScheduler(jitter float64) *Scheduler {
	return &Scheduler{jitter: max(0, min(jitter, 0.5))}
}

// Add registers a task. All tasks must be added before Run.
func (s *Scheduler) Add(name string, interval, timeout time.Duration, fn func(ctx context.Context) error) {
	s.tasks = append(s.tasks, task{name: name, interval: interval, timeout: timeout, fn: fn})
}

// Run starts every task and blocks until ctx is done and the runs in flight
// have returned. The first run of a task happens after a random delay of up to
// the jitter fraction of its interval.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, t := range s.tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.loop(ctx, t)
		}()
	}
	wg.Wait()
}

func (s *Scheduler) loop(ctx context.Context, t task) {
	logging.Ctx(ctx).Info().Str("task", t.name).Int64("interval_ms", (t.interval).Milliseconds()).Msg("automation_task_starting")
	wait := time.Duration(rand.Float64() * s.jitter * float64(t.interval))
	for {
		if s.onTick != nil {
			s.onTick(t.name, t.interval)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			logging.Ctx(ctx).Info().Str("task", t.name).Msg("automation_task_stopping")
			return
		case <-timer.C:
		}
		runOnce(ctx, t)
		wait = jittered(t.interval, s.jitter)
	}
}

// runOnce runs t under its timeout and logs any failure.
func runOnce(ctx context.Context, t task) {
	runCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			logging.Ctx(ctx).Error().Str("task", t.name).Str("panic", fmt.Sprint(r)).Msg("automation_task_panicked")
		}
	}()
	if err := t.fn(runCtx); err != nil && ctx.Err() == nil {
		logutil.Err(*logging.Ctx(ctx), err, zerolog.ErrorLevel).Str("task", t.name).Msg("automation_task_failed")
	}
}

// jittered returns d varied by up to plus or minus frac of itself.
func jittered(d time.Duration, frac float64) time.Duration {
	if frac <= 0 {
		return d
	}
	return max(time.Millisecond, d+time.Duration((rand.Float64()*2-1)*frac*float64(d)))
}
