// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/tracing"
)

// Worker claims ETL jobs of one tier and processes them with bounded
// concurrency.
type Worker struct {
	pool        *pgxpool.Pool
	rs          rawstore.Store
	q           *queue.ETL
	tier        string
	concurrency int
	opts        Options
	idleMin     time.Duration
	idleMax     time.Duration
	jobTimeout  time.Duration
	grace       time.Duration
}

// WorkerOption configures a Worker.
type WorkerOption func(*Worker)

// WithLookup makes the worker resolve connectors with l instead of the
// default registry.
func WithLookup(l Lookup) WorkerOption { return func(w *Worker) { w.opts.Lookup = l } }

// WithJobTimeout bounds one job (default queue.VisibilityTimeout minus one
// minute, so a job ends before another worker may reclaim it).
func WithJobTimeout(d time.Duration) WorkerOption { return func(w *Worker) { w.jobTimeout = d } }

// WithShutdownGrace bounds how long Run waits for in-flight jobs once its
// context is done. Past it the jobs' contexts are cancelled: they stop at
// their next read or write, and a claim they could not settle is reclaimed
// after the visibility timeout. Zero (the default) waits for every job up to
// its job timeout.
func WithShutdownGrace(d time.Duration) WorkerOption { return func(w *Worker) { w.grace = d } }

// WithWriterConfig sets the writer batch size and COPY threshold.
func WithWriterConfig(c WriterConfig) WorkerOption { return func(w *Worker) { w.opts.Writer = c } }

// NewWorker returns a worker for tier running up to concurrency jobs at once.
func NewWorker(pool *pgxpool.Pool, rs rawstore.Store, q *queue.ETL, tier string, concurrency int) *Worker {
	return NewWorkerWith(pool, rs, q, tier, concurrency)
}

// NewWorkerWith is NewWorker with options.
func NewWorkerWith(pool *pgxpool.Pool, rs rawstore.Store, q *queue.ETL, tier string, concurrency int, opts ...WorkerOption) *Worker {
	if concurrency <= 0 {
		concurrency = 1
	}
	w := &Worker{
		pool: pool, rs: rs, q: q, tier: tier, concurrency: concurrency,
		idleMin: time.Second, idleMax: 2 * time.Second,
		jobTimeout: queue.VisibilityTimeout - time.Minute,
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Run claims and processes jobs until ctx is cancelled, then waits for the
// jobs in flight and returns nil.
//
// Jobs run under a context that outlives ctx: cancelling a job at the moment
// shutdown starts would abort its read or its write and leave it claimed until
// the visibility timeout, burning one of its attempts. Each job is bounded by
// the job timeout instead, and by the shutdown grace when one is set
// (WithShutdownGrace), which bounds how long shutdown waits. No new job is
// claimed after ctx is done.
func (w *Worker) Run(ctx context.Context) error {
	jobCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()
	slots := make(chan struct{}, w.concurrency)
	var wg sync.WaitGroup
	defer w.drain(&wg, cancelJobs)

	for {
		// Wait for one free slot, then take every other free one.
		select {
		case <-ctx.Done():
			return nil
		case slots <- struct{}{}:
		}
		n := 1
	more:
		for n < w.concurrency {
			select {
			case slots <- struct{}{}:
				n++
			default:
				break more
			}
		}

		jobs, err := w.q.Claim(ctx, w.tier, n)
		if err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "claim etl jobs", "tier", w.tier, "err", err)
		}
		for range n - len(jobs) {
			<-slots
		}
		for _, job := range jobs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				w.handle(jobCtx, job)
			}()
		}
		// Fewer jobs than asked for (even zero) does not mean the queue is
		// empty, a parked row can take a claim slot: just poll again later.
		if len(jobs) == 0 {
			if !w.idle(ctx) {
				return nil
			}
		}
	}
}

// drain waits for the jobs in flight, cancelling them once the shutdown grace
// has passed.
func (w *Worker) drain(wg *sync.WaitGroup, cancelJobs context.CancelFunc) {
	if w.grace <= 0 {
		wg.Wait()
		return
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	t := time.NewTimer(w.grace)
	defer t.Stop()
	select {
	case <-done:
		return
	case <-t.C:
	}
	slog.Warn("etl shutdown grace over, cancelling jobs in flight", "tier", w.tier, "grace", w.grace)
	cancelJobs()
	<-done
}

// idle sleeps 1 to 2 seconds. It returns false when ctx ends first.
func (w *Worker) idle(ctx context.Context) bool {
	d := w.idleMin
	if span := w.idleMax - w.idleMin; span > 0 {
		d += time.Duration(rand.Int64N(int64(span)))
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// handle processes one job and settles it. A lost claim (ErrNotRunning) means
// another worker owns the job now: the result is dropped. With tracing on,
// the job is a span carrying what its log lines carry, and its outcome.
func (w *Worker) handle(ctx context.Context, job queue.ETLJob) {
	log := slog.With("job_id", job.ID, "tier", job.Tier, "provider", job.Provider, "attempts", job.Attempts)
	ctx, span := tracing.Tracer().Start(ctx, "etl job", trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.Int64(tracing.AttrJobID, job.ID),
			attribute.String(tracing.AttrTier, job.Tier),
			attribute.String(tracing.AttrProvider, job.Provider),
			attribute.Int(tracing.AttrAttempts, job.Attempts)))
	outcome := ""
	defer func() {
		if r := recover(); r != nil {
			log.ErrorContext(ctx, "etl job panicked", "panic", r)
			outcome = w.settle(ctx, log, job, errors.New("etl job panicked"))
		}
		span.SetAttributes(attribute.String(tracing.AttrOutcome, outcome))
		if outcome != "completed" && outcome != "claim_lost" {
			span.SetStatus(codes.Error, "")
		}
		span.End()
	}()
	jobCtx, cancel := context.WithTimeout(ctx, w.jobTimeout)
	defer cancel()
	_, err := ProcessWith(jobCtx, w.pool, w.rs, job, w.opts)
	// Settle on the base context: the job's own may have expired.
	outcome = w.settle(ctx, log, job, err)
}

// settle records the outcome on the job and returns it: completed, failed,
// claim_lost or settle_error.
func (w *Worker) settle(ctx context.Context, log *slog.Logger, job queue.ETLJob, cause error) string {
	var err error
	outcome := "completed"
	if cause == nil {
		err = w.q.Complete(ctx, job)
	} else {
		outcome = "failed"
		log.WarnContext(ctx, "etl job failed", "err", cause)
		err = w.q.Fail(ctx, job, cause)
	}
	switch {
	case errors.Is(err, queue.ErrNotRunning):
		log.InfoContext(ctx, "etl job claim lost, result dropped")
		return "claim_lost"
	case err != nil:
		log.ErrorContext(ctx, "settle etl job", "err", err)
		return "settle_error"
	}
	return outcome
}
