// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tracing"
)

func TestPullJobSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})

	ctx := context.Background()
	e := newConnEnv(t)
	traced, err := db.Open(ctx, e.pool.Config().ConnString(), db.WithTracer(tracing.PGX{}))
	require.NoError(t, err)
	t.Cleanup(traced.Close)
	deps := e.deps
	deps.Pool = traced
	sched := collect.NewScheduler(deps, collect.SchedulerConfig{Concurrency: 1})

	e.connect(t, e.keyA, e.userA, "pu-1")
	e.fake.fetchMode.Store("500")
	n, err := sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	e.fake.fetchMode.Store("ok")
	n, err = sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	var jobs []sdktrace.ReadOnlySpan
	jobIDs := map[trace.SpanID]bool{}
	for _, s := range rec.Ended() {
		if s.Name() == "pull job" {
			jobs = append(jobs, s)
			jobIDs[s.SpanContext().SpanID()] = true
		}
	}
	require.Len(t, jobs, 2)
	attrs := func(s sdktrace.ReadOnlySpan) map[string]string {
		out := map[string]string{}
		for _, kv := range s.Attributes() {
			out[string(kv.Key)] = kv.Value.Emit()
		}
		return out
	}
	failed, done := attrs(jobs[0]), attrs(jobs[1])
	assert.Equal(t, "failed", failed["ohs.job.outcome"])
	assert.Equal(t, codes.Error, jobs[0].Status().Code)
	assert.Empty(t, jobs[0].Status().Description, "the error text stays in the logs")
	assert.Equal(t, "completed", done["ohs.job.outcome"])
	assert.Equal(t, codes.Unset, jobs[1].Status().Code)
	for _, a := range []map[string]string{failed, done} {
		assert.Equal(t, fakeSlug, a["ohs.provider"])
		assert.Equal(t, "scheduled", a["ohs.job.kind"])
		assert.Equal(t, e.tenantA.String(), a["ohs.tenant_id"])
		assert.Equal(t, e.userA.String(), a["ohs.user_id"])
		assert.Contains(t, a, "ohs.job.attempts")
	}

	// The job's queries are its children; the claim, outside any job, is
	// not traced. (The test container's Docker client traces too: its
	// spans are not ours.)
	queries := 0
	for _, s := range rec.Ended() {
		if s.Name() == "pull job" || s.InstrumentationScope().Name != tracing.ScopeName {
			continue
		}
		queries++
		assert.True(t, jobIDs[s.Parent().SpanID()], "%s outside a job span", s.Name())
		for _, kv := range s.Attributes() {
			assert.NotContains(t, kv.Value.Emit(), "pu-1")
			assert.NotContains(t, kv.Value.Emit(), "secret")
		}
	}
	assert.Positive(t, queries)
}
