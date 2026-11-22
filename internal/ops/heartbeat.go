// SPDX-License-Identifier: AGPL-3.0-only

// Package ops holds what a process tells its operator about its background
// work: the heartbeats of its loops, which make a hung worker visible on
// /healthz, and the worker metrics served on /metrics. It depends on nothing
// else in the repository, so every package with a loop can report to it.
package ops

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	// Misses is how many of its intervals a loop may let pass without a
	// heartbeat before it counts as stalled.
	Misses = 3
	// MinStall is the shortest silence that counts as a stall, whatever the
	// loop's interval: a loop that polls every second or two is not hung
	// because one claim waited a few seconds for the database.
	MinStall = time.Minute
)

// Registry holds the loops of one process. The zero value is not usable: see
// NewRegistry. Every method is safe for concurrent use.
type Registry struct {
	mu    sync.Mutex
	loops map[string]*Loop
	now   func() time.Time
	floor time.Duration
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{loops: map[string]*Loop{}, now: time.Now, floor: MinStall}
}

// WithMinStall replaces MinStall as the shortest silence that counts as a
// stall, zero included: every loop is then held to Misses of its own
// interval. It is for tests, which cannot wait a minute.
func (r *Registry) WithMinStall(d time.Duration) *Registry {
	r.mu.Lock()
	r.floor = d
	r.mu.Unlock()
	return r
}

// limit is how long a loop of this interval may stay silent. Under r.mu.
func (r *Registry) limit(interval time.Duration) time.Duration {
	return max(Misses*interval, r.floor)
}

// Loop registers the loop name, which is expected to beat once per interval
// or more often, and returns its handle. The registration is its first
// heartbeat. Registering a name again returns the same loop.
func (r *Registry) Loop(name string, interval time.Duration) *Loop {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.loops[name]; ok {
		return l
	}
	l := &Loop{name: name, interval: interval, r: r, last: r.now(), busy: map[uint64]time.Time{}}
	r.loops[name] = l
	return l
}

// Task is the heartbeat of a named periodic task: it registers the task the
// first time and beats every time. It has the shape of an OnTick hook, for a
// scheduler that runs several tasks and tells them apart by name.
func (r *Registry) Task(name string, interval time.Duration) {
	r.Loop(name, interval).Tick()
}

// Stall is one loop that missed its heartbeats.
type Stall struct {
	Loop string
	// Silent is how long ago the loop last beat; Limit is how long it may
	// stay silent.
	Silent, Limit time.Duration
	// Overdue is true when the loop is silent because a job it started went
	// past the time it was given.
	Overdue bool
}

func (s Stall) String() string {
	if s.Overdue {
		return fmt.Sprintf("%s (a job ran past its limit, no heartbeat for %s)", s.Loop, s.Silent.Round(time.Second))
	}
	return fmt.Sprintf("%s (no heartbeat for %s, limit %s)", s.Loop, s.Silent.Round(time.Second), s.Limit)
}

// Stalled returns the loops that are hung, sorted by name: silent for Misses
// of their intervals (MinStall at least) while no job they started is still
// inside its limit.
func (r *Registry) Stalled() []Stall {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	var out []Stall
	for _, l := range r.loops {
		limit := r.limit(l.interval)
		silent := now.Sub(l.last)
		if silent <= limit {
			continue
		}
		working := false
		for _, deadline := range l.busy {
			if now.Before(deadline) {
				working = true
				break
			}
		}
		if working {
			continue
		}
		out = append(out, Stall{Loop: l.name, Silent: silent, Limit: limit, Overdue: len(l.busy) > 0})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Loop < out[j].Loop })
	return out
}

// LoopState is one loop as the metrics report it.
type LoopState struct {
	Name     string
	LastTick time.Time
	Stalled  bool
}

// States returns every loop, sorted by name.
func (r *Registry) States() []LoopState {
	stalled := map[string]bool{}
	for _, s := range r.Stalled() {
		stalled[s.Loop] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LoopState, 0, len(r.loops))
	for _, l := range r.loops {
		out = append(out, LoopState{Name: l.name, LastTick: l.last, Stalled: stalled[l.name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Loop is the heartbeat of one background loop. A nil Loop is valid and
// records nothing, so a package takes one as an optional setting and calls
// it without a check.
type Loop struct {
	name     string
	interval time.Duration
	r        *Registry

	// Under r.mu.
	last time.Time
	busy map[uint64]time.Time // open jobs: when each stops excusing the loop
	seq  uint64
}

// Tick records a heartbeat. Call it from the loop itself, once per
// iteration, never from the end of a job: a loop is alive when it keeps
// coming round, whatever its jobs do.
func (l *Loop) Tick() {
	if l == nil {
		return
	}
	l.r.mu.Lock()
	l.last = l.r.now()
	l.r.mu.Unlock()
}

// Busy says the loop started a job that may take up to limit, and returns
// what to call when the job ends. While such a job is inside its limit the
// loop is not stalled even when it does not beat: a worker whose slots are
// all taken by long jobs waits for one of them instead of polling. A job
// past its limit excuses nothing. The end of a job counts as a heartbeat.
// done may be called more than once.
func (l *Loop) Busy(limit time.Duration) (done func()) {
	if l == nil {
		return func() {}
	}
	l.r.mu.Lock()
	l.seq++
	id := l.seq
	l.busy[id] = l.r.now().Add(limit)
	l.r.mu.Unlock()
	return func() {
		l.r.mu.Lock()
		if _, open := l.busy[id]; open {
			delete(l.busy, id)
			l.last = l.r.now()
		}
		l.r.mu.Unlock()
	}
}

// Jobs follows the jobs of one loop that are claimed in one place and
// settled in another, by id: Start when a job is claimed, End when it is
// settled. A job never ended stops excusing the loop at its limit, and is
// forgotten at the next Start after that.
type Jobs struct {
	loop  *Loop
	limit time.Duration
	now   func() time.Time

	mu   sync.Mutex
	open map[int64]openJob
}

type openJob struct {
	start time.Time
	done  func()
}

// NewJobs follows jobs of loop that may each take up to limit. loop may be nil.
func NewJobs(loop *Loop, limit time.Duration) *Jobs {
	return &Jobs{loop: loop, limit: limit, now: time.Now, open: map[int64]openJob{}}
}

// Start records that job id was claimed.
func (j *Jobs) Start(id int64) {
	now := j.now()
	done := j.loop.Busy(j.limit)
	j.mu.Lock()
	defer j.mu.Unlock()
	for old, o := range j.open {
		if now.Sub(o.start) > j.limit {
			o.done()
			delete(j.open, old)
		}
	}
	if o, again := j.open[id]; again {
		o.done()
	}
	j.open[id] = openJob{start: now, done: done}
}

// End records that job id was settled and returns how long it ran. ok is
// false for a job Start never saw.
func (j *Jobs) End(id int64) (ran time.Duration, ok bool) {
	j.mu.Lock()
	o, ok := j.open[id]
	delete(j.open, id)
	j.mu.Unlock()
	if !ok {
		return 0, false
	}
	o.done()
	return j.now().Sub(o.start), true
}
