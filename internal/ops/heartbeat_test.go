// SPDX-License-Identifier: AGPL-3.0-only

package ops

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a registry clock the test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func testRegistry() (*Registry, *clock) {
	c := &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	r := NewRegistry()
	r.now = c.now
	return r, c
}

func names(stalls []Stall) string {
	var out []string
	for _, s := range stalls {
		out = append(out, s.Loop)
	}
	return strings.Join(out, ",")
}

func TestLoopStallsAfterThreeMissedIntervals(t *testing.T) {
	r, c := testRegistry()
	slow := r.Loop("scheduler", 5*time.Minute)
	fast := r.Loop("workers", 2*time.Second)
	if got := r.Stalled(); len(got) != 0 {
		t.Fatalf("just registered: %v", got)
	}

	// A fast loop is held to MinStall, not to three of its 2 s intervals.
	c.add(30 * time.Second)
	if got := r.Stalled(); len(got) != 0 {
		t.Fatalf("30 s of silence: %v", got)
	}
	c.add(31 * time.Second)
	if got := names(r.Stalled()); got != "workers" {
		t.Fatalf("61 s of silence: %q", got)
	}
	fast.Tick()

	// The slow loop: three intervals, to the second.
	c.add(13*time.Minute + 59*time.Second)
	fast.Tick()
	if got := r.Stalled(); len(got) != 0 {
		t.Fatalf("silent for exactly three intervals: %v", got)
	}
	c.add(2 * time.Second)
	fast.Tick()
	stalls := r.Stalled()
	if names(stalls) != "scheduler" {
		t.Fatalf("past three intervals: %v", stalls)
	}
	if s := stalls[0]; s.Limit != 15*time.Minute || s.Silent != 15*time.Minute+2*time.Second || s.Overdue {
		t.Fatalf("stall: %+v", s)
	}
	if msg := stalls[0].String(); !strings.HasPrefix(msg, "scheduler (no heartbeat for 15m2s") {
		t.Fatalf("message: %q", msg)
	}

	// Both silent: sorted by name.
	c.add(2 * time.Minute)
	if got := names(r.Stalled()); got != "scheduler,workers" {
		t.Fatalf("both: %q", got)
	}
	_ = slow
}

func TestLoopRecoversOnTick(t *testing.T) {
	r, c := testRegistry()
	l := r.Loop("webhooks", time.Second)
	c.add(5 * time.Minute)
	if got := names(r.Stalled()); got != "webhooks" {
		t.Fatalf("silent: %q", got)
	}
	l.Tick()
	if got := r.Stalled(); len(got) != 0 {
		t.Fatalf("after a tick: %v", got)
	}
	states := r.States()
	if len(states) != 1 || states[0].Name != "webhooks" || !states[0].LastTick.Equal(c.now()) || states[0].Stalled {
		t.Fatalf("states: %+v", states)
	}
}

// A worker whose only slot is taken by one long job does not poll: it is
// busy, not hung, until the job goes past the time it was given.
func TestLoopBusyOnLongJobIsNotStalled(t *testing.T) {
	r, c := testRegistry()
	l := r.Loop("etl_standard", 2*time.Second)
	l.Tick()
	done := l.Busy(15 * time.Minute)

	c.add(9 * time.Minute)
	if got := r.Stalled(); len(got) != 0 {
		t.Fatalf("9 minutes into a job allowed 15: %v", got)
	}

	// The job ends: that is a sign of life, and the loop has its usual
	// limit to come round again.
	done()
	done() // twice is harmless
	c.add(59 * time.Second)
	if got := r.Stalled(); len(got) != 0 {
		t.Fatalf("59 s after the job ended: %v", got)
	}
	c.add(2 * time.Second)
	if got := names(r.Stalled()); got != "etl_standard" {
		t.Fatalf("the loop never came round after its job: %q", got)
	}
}

func TestLoopJobPastItsLimitStalls(t *testing.T) {
	r, c := testRegistry()
	l := r.Loop("etl_standard", 2*time.Second)
	done := l.Busy(15 * time.Minute)
	defer done()
	c.add(15*time.Minute + time.Second)
	stalls := r.Stalled()
	if names(stalls) != "etl_standard" || !stalls[0].Overdue {
		t.Fatalf("a job past its limit excuses nothing: %+v", stalls)
	}
	if msg := stalls[0].String(); !strings.Contains(msg, "a job ran past its limit") {
		t.Fatalf("message: %q", msg)
	}

	// A second job inside its limit excuses the loop again: the other
	// slots still work.
	other := l.Busy(time.Minute)
	if got := r.Stalled(); len(got) != 0 {
		t.Fatalf("another job in flight: %v", got)
	}
	other()
}

// A loop that beats keeps its jobs out of the picture: a hung job in one
// slot is not a hung loop.
func TestLoopTickingWithAnOverdueJobIsAlive(t *testing.T) {
	r, c := testRegistry()
	l := r.Loop("pull_workers", 2*time.Second)
	done := l.Busy(time.Minute)
	defer done()
	for range 10 {
		c.add(30 * time.Second)
		l.Tick()
	}
	if got := r.Stalled(); len(got) != 0 {
		t.Fatalf("the loop beats: %v", got)
	}
}

func TestNilLoopRecordsNothing(t *testing.T) {
	var l *Loop
	l.Tick()
	l.Busy(time.Minute)()
	NewJobs(nil, time.Minute).Start(1)
}

func TestWithMinStallLowersTheFloor(t *testing.T) {
	r, c := testRegistry()
	r.WithMinStall(0)
	r.Loop("workers", 100*time.Millisecond)
	r.Loop("retention", 24*time.Hour)
	c.add(300 * time.Millisecond)
	if got := r.Stalled(); len(got) != 0 {
		t.Fatalf("three intervals exactly: %v", got)
	}
	c.add(time.Millisecond)
	if got := names(r.Stalled()); got != "workers" {
		t.Fatalf("stalled: %q", got)
	}
}

func TestTaskRegistersThenBeats(t *testing.T) {
	r, c := testRegistry()
	r.Task("reconciler", 5*time.Minute)
	c.add(16 * time.Minute)
	if got := names(r.Stalled()); got != "reconciler" {
		t.Fatalf("stalled: %q", got)
	}
	r.Task("reconciler", 5*time.Minute)
	if got := r.Stalled(); len(got) != 0 {
		t.Fatalf("after a beat: %v", got)
	}
}

func TestJobsFollowClaimToSettle(t *testing.T) {
	r, c := testRegistry()
	l := r.Loop("etl_high_freq", 2*time.Second)
	j := NewJobs(l, 15*time.Minute)
	j.now = c.now

	j.Start(7)
	c.add(8 * time.Minute)
	if got := r.Stalled(); len(got) != 0 {
		t.Fatalf("a job in flight: %v", got)
	}
	ran, ok := j.End(7)
	if !ok || ran != 8*time.Minute {
		t.Fatalf("End: %v %v", ran, ok)
	}
	if _, ok := j.End(7); ok {
		t.Fatal("a job ends once")
	}
	if _, ok := j.End(99); ok {
		t.Fatal("a job never started has no duration")
	}

	// A job never settled is forgotten at the next Start past its limit.
	j.Start(8)
	c.add(16 * time.Minute)
	j.Start(9)
	if _, ok := j.End(8); ok {
		t.Fatal("job 8 was past its limit: forgotten")
	}
	if ran, ok := j.End(9); !ok || ran != 0 {
		t.Fatalf("job 9: %v %v", ran, ok)
	}
	if len(l.busy) != 0 {
		t.Fatalf("open jobs left on the loop: %d", len(l.busy))
	}
}

func TestRegistryIsSafeForConcurrentUse(t *testing.T) {
	r := NewRegistry()
	l := r.Loop("workers", time.Second)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				l.Tick()
				done := l.Busy(time.Minute)
				_ = r.Stalled()
				_ = r.States()
				done()
			}
		}()
	}
	wg.Wait()
}
