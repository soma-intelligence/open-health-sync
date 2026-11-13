// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

func (e *env) enqueue(j queue.ETLJob) {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	require.NoError(e.t, err)
	require.NoError(e.t, queue.EnqueueETL(ctx, tx, j))
	require.NoError(e.t, tx.Commit(ctx))
}

func (e *env) jobStates() map[string]int {
	e.t.Helper()
	rows, err := e.read().Query(context.Background(), `SELECT status, attempts FROM ohs_sync.etl_jobs`)
	require.NoError(e.t, err)
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (struct {
		s string
		a int
	}, error) {
		var v struct {
			s string
			a int
		}
		return v, r.Scan(&v.s, &v.a)
	})
	require.NoError(e.t, err)
	m := map[string]int{}
	for _, v := range out {
		m[v.s]++
		if v.a > 0 {
			m["attempted"]++
		}
	}
	return m
}

func TestWorker_ProcessesCompletesAndFails(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	at := recent()
	for i := range 4 {
		e.enqueue(e.upload(e.tenant, e.user, "apple_health", "heart_rate", q("heart_rate", 60+float64(i), "bpm", at.Add(time.Duration(i)*time.Minute))))
	}
	// A job for a provider nobody registered fails and is retried later.
	e.enqueue(e.upload(e.tenant, e.user, "nobody", "x", q("heart_rate", 60, "bpm", at)))

	var mu sync.Mutex
	rows := map[string]int{}
	w := NewWorkerWith(e.pool, e.rs, queue.NewETL(e.pool), queue.TierStandard, 2, WithLookup(e.reg.Get),
		WithRowsWritten(func(table string, n int) {
			mu.Lock()
			defer mu.Unlock()
			rows[table] += n
		}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	require.Eventually(t, func() bool {
		s := e.jobStates()
		return s["done"] == 4 && s["attempted"] == 1
	}, 30*time.Second, 100*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop")
	}
	assert.Equal(t, 4, e.count("quantity_samples"))
	assert.Equal(t, 1, e.jobStates()["pending"], "the failed job waits for its backoff")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 4, rows["quantity_samples"], "the rows each job wrote are reported; the failed job reports none")
}

// One user's backlog runs one job at a time (DefaultETLPerUser), so the
// next job becomes claimable only when the previous one ends. The worker
// claims it at once instead of sleeping its idle interval: 20 small jobs
// of one user drain in far less than 20 idle intervals (20 to 40 s).
func TestWorker_OneUsersBacklogDoesNotWaitIdle(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	at := recent()
	const n = 20
	for i := range n {
		e.enqueue(e.upload(e.tenant, e.user, "apple_health", "heart_rate", q("heart_rate", 60+float64(i), "bpm", at.Add(time.Duration(i)*time.Minute))))
	}
	w := NewWorkerWith(e.pool, e.rs, queue.NewETL(e.pool), queue.TierStandard, 4, WithLookup(e.reg.Get))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool { return e.jobStates()["done"] == n }, 60*time.Second, 50*time.Millisecond)
	took := time.Since(start)
	cancel()
	require.NoError(t, <-done)
	assert.Less(t, took, 10*time.Second, "one user's jobs follow each other without the idle sleep")
	assert.Equal(t, n, e.count("quantity_samples"))
}

func TestWorker_WaitsForInFlightJobsOnCancel(t *testing.T) {
	e := newEnv(t)
	var started, finished atomic.Int32
	release := make(chan struct{})
	e.register("slow", func(r connector.RawRecord) (model.MapResult, error) {
		started.Add(1)
		<-release
		finished.Add(1)
		return model.MapResult{}, nil
	})
	e.enqueue(e.upload(e.tenant, e.user, "slow", "x", `{"a":1}`))

	w := NewWorkerWith(e.pool, e.rs, queue.NewETL(e.pool), queue.TierStandard, 1, WithLookup(e.reg.Get))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool { return started.Load() == 1 }, 30*time.Second, 50*time.Millisecond)

	cancel()
	select {
	case <-done:
		t.Fatal("Run returned before its in-flight job finished")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop")
	}
	assert.EqualValues(t, 1, finished.Load())
	assert.Equal(t, 1, e.jobStates()["done"], "the drained job is still settled")
}

// hangingStore serves every object as a stream that blocks until the reading
// context ends.
type hangingStore struct{ rawstore.Store }

func (h hangingStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(ctxReader{ctx}), nil
}

type ctxReader struct{ ctx context.Context }

func (r ctxReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

// A job that hangs is cut off by the job timeout and failed, not left running,
// and Run returns after cancel.
func TestWorker_JobTimeoutFailsTheJob(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	e.enqueue(e.upload(e.tenant, e.user, "apple_health", "heart_rate", q("heart_rate", 60, "bpm", recent())))

	w := NewWorkerWith(e.pool, hangingStore{e.rs}, queue.NewETL(e.pool), queue.TierStandard, 1,
		WithLookup(e.reg.Get), WithJobTimeout(300*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	require.Eventually(t, func() bool {
		s := e.jobStates()
		return s["pending"] == 1 && s["attempted"] == 1
	}, 30*time.Second, 50*time.Millisecond, "the timed-out job is failed back to pending")
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop")
	}
	assert.Zero(t, e.jobStates()["running"])
}

// After shutdown starts, in-flight jobs get the shutdown grace and are then
// cancelled: Run returns within the grace even though the job would hang for
// its whole job timeout. The claim is left for a later reclaim.
func TestWorker_ShutdownGraceCancelsHungJobs(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	e.enqueue(e.upload(e.tenant, e.user, "apple_health", "heart_rate", q("heart_rate", 60, "bpm", recent())))

	w := NewWorkerWith(e.pool, hangingStore{e.rs}, queue.NewETL(e.pool), queue.TierStandard, 1,
		WithLookup(e.reg.Get), WithShutdownGrace(300*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool { return e.jobStates()["running"] == 1 }, 30*time.Second, 20*time.Millisecond)

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("a hung job kept Run from returning past the shutdown grace")
	}
	took := time.Since(start)
	assert.GreaterOrEqual(t, took, 250*time.Millisecond, "in-flight jobs get the grace first")
	assert.Less(t, took, 2*time.Second)
	assert.Equal(t, 1, e.jobStates()["running"], "the claim is left for the visibility timeout reclaim")
}
