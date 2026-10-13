// SPDX-License-Identifier: AGPL-3.0-only

// Package automation runs the ETL's background upkeep: the reconciler that
// reports what the pipeline lost or stalled on, the orphan scanner that queues
// raw objects nobody queued, the drainer that replays raw objects whose records
// failed, the retention job that prunes old data when the operator sets
// OHS_RETENTION_DAYS, and the audit job that prunes audit events older than
// OHS_AUDIT_RETENTION_DAYS.
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

	defaultRunTimeout  = 5 * time.Minute
	defaultLongTimeout = time.Hour
	defaultJitter      = 0.1
)

// Intervals sets how often each job runs. A zero value means the default.
type Intervals struct {
	Reconcile, Orphans, Drain, Files, Retention, Idempotency, AuditPrune time.Duration
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
	Intervals          Intervals
	// RunTimeout bounds one run of any job except files and retention (default 5 minutes;
	// retention gets an hour unless this is set).
	RunTimeout time.Duration
	// NoJitter turns off the random spread of start times and intervals.
	NoJitter bool
	// Metrics receives counters and gauges. Nil means a private set.
	Metrics *Metrics
	// Now is the clock, injectable for tests. Nil means time.Now.
	Now func() time.Time
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
