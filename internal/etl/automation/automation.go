// SPDX-License-Identifier: AGPL-3.0-only

// Package automation runs the ETL's background upkeep: the reconciler that
// reports what the pipeline lost or stalled on, the orphan scanner that queues
// raw objects nobody queued, the drainer that replays raw objects whose records
// failed, the retention job that prunes old data when the operator sets
// OHS_RETENTION_DAYS, the audit job that prunes audit events older than
// OHS_AUDIT_RETENTION_DAYS, the tracking job that prunes old sync_log rows and
// finished etl_jobs, and the shares job that removes the encrypted file of
// every expired share link.
//
// Sleep phase canonicalisation is not ported. In the source it was a one-off
// boot-time rewrite of rows written before the mappers normalised labels. Here
// the labels are normalised by the model package before anything is written and
// no database ever held the old labels, so there is nothing to rewrite.
package automation

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/shl"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// Default job intervals.
const (
	DefaultReconcileEvery   = 5 * time.Minute
	DefaultOrphansEvery     = 10 * time.Minute
	DefaultDrainEvery       = 15 * time.Minute
	DefaultFilesEvery       = 24 * time.Hour
	DefaultRetentionEvery   = 24 * time.Hour
	DefaultIdempotencyEvery = time.Hour
	DefaultAuditPruneEvery  = 24 * time.Hour
	DefaultSharesEvery      = time.Hour
	DefaultTrackingEvery    = 24 * time.Hour
	DefaultHeartbeatsEvery  = 24 * time.Hour

	defaultRunTimeout  = 5 * time.Minute
	defaultLongTimeout = time.Hour
	defaultJitter      = 0.1
)

// Intervals sets how often each job runs. A zero value means the default.
type Intervals struct {
	Reconcile, Orphans, Drain, Files, Retention, Idempotency, AuditPrune, Shares, Tracking, Heartbeats time.Duration
}

// Deps is everything Start needs.
type Deps struct {
	Pool  *pgxpool.Pool
	Store rawstore.Store
	// RetentionDays is OHS_RETENTION_DAYS. Zero or less keeps everything and
	// the retention job is not scheduled at all.
	RetentionDays int
	// AuditRetentionDays is OHS_AUDIT_RETENTION_DAYS. Zero or less keeps
	// every audit event and the audit job is not scheduled.
	AuditRetentionDays int
	// TrackingRetentionDays is how long sync_log rows and finished etl_jobs
	// are kept. Zero means DefaultTrackingRetentionDays, a negative value
	// keeps them for ever (the tracking job is not scheduled), and a value
	// under MinTrackingRetentionDays is raised to it.
	TrackingRetentionDays int
	Intervals             Intervals
	// PruneHeartbeats deletes the process heartbeats not seen for a while
	// and returns how many. Nil leaves the heartbeats task unscheduled.
	PruneHeartbeats func(context.Context) (int64, error)
	// RunTimeout bounds one run of any job except files and retention (default 5 minutes;
	// retention gets an hour unless this is set).
	RunTimeout time.Duration
	// NoJitter turns off the random spread of start times and intervals.
	NoJitter bool
	// Metrics receives counters and gauges. Nil means a private set.
	Metrics *Metrics
	// Now is the clock, injectable for tests. Nil means time.Now.
	Now func() time.Time
	// OnTick, when set, is called at the top of every iteration of every
	// task's loop with the task's name and interval: the heartbeat that
	// tells a stalled task from a healthy one.
	OnTick func(task string, interval time.Duration)
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Start runs the jobs until ctx is done, then waits for the runs in flight and
// returns nil. It returns an error only for unusable dependencies. A failing
// run is logged and counted, and the loop goes on.
func Start(ctx context.Context, deps Deps) error {
	s, err := newScheduler(deps)
	if err != nil {
		return err
	}
	s.Run(ctx)
	return nil
}

// newScheduler registers the jobs. Retention is registered only when
// RetentionDays is positive.
func newScheduler(deps Deps) (*Scheduler, error) {
	if deps.Pool == nil {
		return nil, errors.New("automation: Pool is required")
	}
	if deps.Store == nil {
		return nil, errors.New("automation: Store is required")
	}
	if deps.Metrics == nil {
		deps.Metrics = &Metrics{}
	}
	jitter := defaultJitter
	if deps.NoJitter {
		jitter = 0
	}
	timeout := or(deps.RunTimeout, defaultRunTimeout)

	m := deps.Metrics
	counted := func(fn func(context.Context) error) func(context.Context) error {
		return countFailures(m, fn)
	}

	rec := &Reconciler{Pool: deps.Pool, Metrics: m, Now: deps.now}
	orph := &OrphanScanner{Pool: deps.Pool, Metrics: m, Now: deps.now}
	files := &FileScanner{Pool: deps.Pool, Store: deps.Store, Metrics: m}
	drain := &Drainer{Pool: deps.Pool, Metrics: m, Since: deps.now()}

	s := NewScheduler(jitter)
	s.onTick = deps.OnTick
	s.Add("reconciler", or(deps.Intervals.Reconcile, DefaultReconcileEvery), timeout, counted(func(ctx context.Context) error {
		_, err := rec.Run(ctx)
		return err
	}))
	s.Add("orphans", or(deps.Intervals.Orphans, DefaultOrphansEvery), timeout, counted(func(ctx context.Context) error {
		_, err := orph.Run(ctx)
		return err
	}))
	s.Add("drainer", or(deps.Intervals.Drain, DefaultDrainEvery), timeout, counted(func(ctx context.Context) error {
		_, err := drain.Run(ctx)
		return err
	}))
	idem := tenant.New(deps.Pool)
	s.Add("idempotency", or(deps.Intervals.Idempotency, DefaultIdempotencyEvery), timeout, counted(func(ctx context.Context) error {
		n, err := idem.PurgeIdempotency(ctx)
		if err == nil && n > 0 {
			slog.InfoContext(ctx, "idempotency keys purged", "rows", n)
		}
		return err
	}))
	s.Add("shares", or(deps.Intervals.Shares, DefaultSharesEvery), timeout, counted(func(ctx context.Context) error {
		n, err := shl.PurgeExpired(ctx, deps.Pool)
		if n > 0 {
			slog.InfoContext(ctx, "expired share link files removed", "shares", n)
		}
		return err
	}))
	longTimeout := defaultLongTimeout
	if deps.RunTimeout > 0 {
		longTimeout = deps.RunTimeout
	}
	s.Add("files", or(deps.Intervals.Files, DefaultFilesEvery), longTimeout, counted(func(ctx context.Context) error {
		_, err := files.Run(ctx)
		return err
	}))
	if days := deps.AuditRetentionDays; days > 0 {
		s.Add("audit", or(deps.Intervals.AuditPrune, DefaultAuditPruneEvery), longTimeout, counted(func(ctx context.Context) error {
			n, err := audit.Prune(ctx, deps.Pool, deps.now().AddDate(0, 0, -days))
			if n > 0 {
				slog.InfoContext(ctx, "audit events pruned", "rows", n, "retention_days", days)
			}
			return err
		}))
	}
	if days := deps.TrackingRetentionDays; days >= 0 {
		if days == 0 {
			days = DefaultTrackingRetentionDays
		}
		track := &TrackingPruner{Pool: deps.Pool, Days: days, Metrics: m, Now: deps.now}
		s.Add("tracking", or(deps.Intervals.Tracking, DefaultTrackingEvery), longTimeout, counted(func(ctx context.Context) error {
			_, err := track.Run(ctx)
			return err
		}))
	}
	if prune := deps.PruneHeartbeats; prune != nil {
		s.Add("heartbeats", or(deps.Intervals.Heartbeats, DefaultHeartbeatsEvery), timeout, counted(func(ctx context.Context) error {
			n, err := prune(ctx)
			if n > 0 {
				slog.InfoContext(ctx, "stale process heartbeats removed", "rows", n)
			}
			return err
		}))
	}
	if deps.RetentionDays > 0 {
		ret := &Retention{Pool: deps.Pool, Store: deps.Store, Days: deps.RetentionDays, Metrics: m, Now: deps.now}
		s.Add("retention", or(deps.Intervals.Retention, DefaultRetentionEvery), longTimeout, counted(func(ctx context.Context) error {
			_, err := ret.Run(ctx)
			return err
		}))
	}
	return s, nil
}

// countFailures wraps fn so a failed run, a timed-out run and a panic add to
// RunFailures. A run cut short by cancellation (shutdown) is not a failure.
func countFailures(m *Metrics, fn func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		defer func() {
			if r := recover(); r != nil {
				m.RunFailures.Add(1)
				panic(r) // the scheduler logs it
			}
		}()
		err := fn(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			m.RunFailures.Add(1)
		}
		return err
	}
}
