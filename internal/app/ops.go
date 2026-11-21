// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/consent"
	"github.com/soma-intelligence/open-health-sync/internal/ops"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// The background loops that report a heartbeat, by the name /healthz and the
// ohs_loop_ metrics give them. The etl role runs the ETL tiers, the webhook
// dispatcher, the deletion worker and the consent purger; the collect role
// runs the three pull loops.
const (
	loopETLPrefix      = "etl_" // + the tier: etl_standard, etl_high_freq
	loopWebhooks       = "webhooks"
	loopDeletion       = "deletion"
	loopConsentPurge   = "consent_purge"
	loopPullScheduler  = "pull_scheduler"
	loopPullSubscriber = "pull_subscriber"
	loopPullWorkers    = "pull_workers"
)

// How often each loop comes round when it has nothing to do: the interval
// its heartbeat is held to. The loops that poll every second or two are held
// to ops.MinStall in practice.
const (
	etlPollEvery      = 2 * time.Second
	pullPollEvery     = 2 * time.Second
	webhookPollEvery  = time.Second
	deletionPollEvery = 5 * time.Second
)

const (
	// jobBusyLimit is how long a claimed ETL job excuses a worker loop that
	// does not beat (every slot taken by a long job): the job timeout, then
	// a settle retried for up to half the visibility timeout.
	jobBusyLimit = queue.VisibilityTimeout + queue.VisibilityTimeout/2
	// purgeLimit is how long one consent purge may hold its loop.
	purgeLimit = time.Hour
	// queueSampleEvery is how often the queues' backlog is counted for the
	// metrics, and queueSampleTimeout bounds one count.
	queueSampleEvery   = 15 * time.Second
	queueSampleTimeout = 10 * time.Second
)

// etlHooks reports the ETL workers through their queue, the one place where
// each tier's loop comes round (a claim) and each job starts and ends: the
// worker package is not touched. A claimed job excuses its tier's loop until
// limit.
func etlHooks(loops *ops.Registry, m *ops.Metrics, limit time.Duration) queue.ETLHooks {
	type tier struct {
		loop *ops.Loop
		jobs *ops.Jobs
	}
	tiers := map[string]tier{}
	for _, t := range []string{queue.TierStandard, queue.TierHighFreq} {
		l := loops.Loop(loopETLPrefix+t, etlPollEvery)
		tiers[t] = tier{loop: l, jobs: ops.NewJobs(l, limit)}
	}
	return queue.ETLHooks{
		Poll:    func(t string) { tiers[t].loop.Tick() },
		Claimed: func(j queue.ETLJob) { tiers[j.Tier].jobs.Start(j.ID) },
		Settled: func(j queue.ETLJob, outcome string) {
			if ran, ok := tiers[j.Tier].jobs.End(j.ID); ok {
				m.JobDone("etl", j.Tier, outcome, ran)
			}
		},
	}
}

// pullHooks reports the three loops of the pull scheduler and its pulls.
func pullHooks(loops *ops.Registry, m *ops.Metrics) collect.SchedulerHooks {
	return collect.SchedulerHooks{
		Schedule:  loops.Loop(loopPullScheduler, collect.DefaultTick),
		Subscribe: loops.Loop(loopPullSubscriber, collect.DefaultTick),
		Work:      loops.Loop(loopPullWorkers, pullPollEvery),
		Pulled: func(job queue.PullJob, outcome string, took time.Duration) {
			m.JobDone("pull", job.TriggerName(), outcome, took)
			m.PullDone(job.Provider, took)
		},
	}
}

// runConsentPurge is consent.Purger.Run with a heartbeat: the same loop over
// Once, written here so the consent package needs no hook.
func runConsentPurge(ctx context.Context, p *consent.Purger, loop *ops.Loop) error {
	for {
		loop.Tick()
		done := loop.Busy(purgeLimit)
		did, err := p.Once(ctx)
		done()
		if err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "consent: purge failed", "error", err)
		}
		if did && err == nil {
			continue
		}
		t := time.NewTimer(consent.DefaultPurgeEvery)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

// sampleQueues counts the backlog of the queues this process works on, at
// start and then every interval, until ctx ends: the etl role's two tiers
// and the webhook deliveries, the collect role's pulls. The counts read an
// index each, but a long queue makes them too dear to run on every scrape,
// and a scrape must not wait for the database.
func sampleQueues(ctx context.Context, pool *pgxpool.Pool, roles map[string]bool, m *ops.Metrics, every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		sctx, cancel := context.WithTimeout(ctx, queueSampleTimeout)
		err := sampleQueuesOnce(sctx, pool, roles, m)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "ops: queue backlog not sampled", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func sampleQueuesOnce(ctx context.Context, pool *pgxpool.Pool, roles map[string]bool, m *ops.Metrics) error {
	var errs []error
	if roles["etl"] {
		tiers, err := queue.ETLBacklog(ctx, pool)
		for tier, b := range tiers {
			m.SetQueue("etl_"+tier, ops.QueueStat{Depth: b.Depth, OldestAge: b.OldestAge})
		}
		errs = append(errs, err)
		depth, oldest, err := webhook.Backlog(ctx, pool)
		if err == nil {
			m.SetQueue("webhook", ops.QueueStat{Depth: depth, OldestAge: oldest})
		}
		errs = append(errs, err)
	}
	if roles["collect"] {
		b, err := queue.PullBacklog(ctx, pool)
		if err == nil {
			m.SetQueue("pull", ops.QueueStat{Depth: b.Depth, OldestAge: b.OldestAge})
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// stalledLoops is /healthz's view of the registry: one phrase per hung loop.
func stalledLoops(loops *ops.Registry) func() []string {
	return func() []string {
		var out []string
		for _, s := range loops.Stalled() {
			out = append(out, s.String())
		}
		return out
	}
}

// serveOps serves the ops routes (/healthz, /readyz, /metrics) on the
// address of OHS_OPS_LISTEN, stopped with the process. A taken address is an
// error when the operator chose it. When it is the default of a process
// without the api role, the process starts without the listener and says so:
// two workers on one host must not stop each other at an upgrade.
func serveOps(ctx context.Context, cfg config.Config, d api.OpsDeps, grace time.Duration,
	logger *slog.Logger, goRole func(string, func() error)) error {
	ln, err := net.Listen("tcp", cfg.OpsListen)
	if err != nil {
		if cfg.OpsListenDefault {
			slog.Warn("ops: the default address is taken, this process serves no /healthz, /readyz or /metrics: "+
				"set OHS_OPS_LISTEN to an address of its own, or to off", "addr", cfg.OpsListen, "error", err)
			return nil
		}
		return fmt.Errorf("ops: %w (OHS_OPS_LISTEN)", err)
	}
	srv := newHTTPServer(cfg.OpsListen, api.NewOpsHandler(d), logger)
	slog.Info("ops listening", "addr", ln.Addr().String())
	goRole("ops", func() error {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	goRole("ops shutdown", func() error {
		<-ctx.Done()
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
		defer scancel()
		if err := srv.Shutdown(sctx); err != nil {
			_ = srv.Close()
		}
		return nil
	})
	return nil
}

// opsMetrics writes what the ops listener's /metrics serves: the worker
// metrics, then the writers of the roles this process runs.
func opsMetrics(m *ops.Metrics, more ...func(io.Writer)) func(io.Writer) {
	return func(w io.Writer) {
		m.Write(w)
		for _, write := range more {
			write(w)
		}
	}
}
