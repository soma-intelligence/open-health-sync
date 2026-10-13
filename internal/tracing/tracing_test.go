// SPDX-License-Identifier: AGPL-3.0-only

package tracing

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// This test runs first in the package (tests run in file order): the
// globals are still OpenTelemetry's defaults.
func TestSetupDisabledInstallsNothing(t *testing.T) {
	tp, prop := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	shutdown, err := Setup(t.Context(), Options{Enabled: false, Version: "v1"})
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	require.NoError(t, shutdown(t.Context()))
	assert.Same(t, tp, otel.GetTracerProvider(), "the global provider is left alone")
	assert.Equal(t, prop, otel.GetTextMapPropagator())
	_, isSDK := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	assert.False(t, isSDK)

	_, span := Tracer().Start(t.Context(), "x")
	assert.False(t, span.IsRecording())
	assert.False(t, span.SpanContext().IsValid())
	span.End()
	assert.Empty(t, otel.GetTextMapPropagator().Fields(), "no traceparent is read or written")
}

func TestRouteAndSpanName(t *testing.T) {
	assert.Equal(t, "/v1/users/{id}", Route("GET /v1/users/{id}"))
	assert.Equal(t, "/v1/users", Route("/v1/users"))
	assert.Equal(t, "/x", Route("POST example.org/x"))
	assert.Equal(t, "", Route(""))
	assert.Equal(t, "GET /v1/users/{id}", SpanName("GET", "/v1/users/{id}"))
	assert.Equal(t, "POST", SpanName("POST", ""))
	assert.Equal(t, "HTTP /", SpanName("_OTHER", "/"))
	assert.Equal(t, "GET", Method("GET"))
	assert.Equal(t, "_OTHER", Method("get"))
	assert.Equal(t, "_OTHER", Method("BREW"))
}

func TestOperation(t *testing.T) {
	for sql, want := range map[string]string{
		"SELECT 1":                                  "SELECT",
		"\n\t\tinsert into t (a) values ($1)":       "INSERT",
		"-- a comment\nUPDATE t SET a = $1":         "UPDATE",
		"/* note */ delete from t where id = $1":    "DELETE",
		"WITH picked AS (SELECT 1) SELECT * FROM p": "WITH",
		"(SELECT 1) UNION (SELECT 2)":               "SELECT",
		"begin":                                     "BEGIN",
		"stmtcache_1234abcd":                        "QUERY",
		"":                                          "QUERY",
		"-- only a comment":                         "QUERY",
	} {
		assert.Equal(t, want, Operation(sql), "%q", sql)
	}
}

func attrKV(k, v string) attribute.KeyValue { return attribute.String(k, v) }

func recorder(t *testing.T) (*tracetest.SpanRecorder, trace.Tracer) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return rec, tp.Tracer("test")
}

// withProvider makes tp the global provider for the test.
func withProvider(t *testing.T, tp trace.TracerProvider) {
	t.Helper()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
}

func TestPGXTracesOnlyInsideASpanAndNeverArguments(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	withProvider(t, tp)
	var p PGX

	// Outside a span: no span, the context is returned as is.
	ctx := context.Background()
	got := p.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	assert.Equal(t, ctx, got)
	p.TraceQueryEnd(got, nil, pgx.TraceQueryEndData{})
	assert.Empty(t, rec.Ended())

	// Inside a job span: a child span named by the operation.
	ctx, job := Tracer().Start(context.Background(), "pull job")
	const secret = "token-abc-123"
	qctx := p.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL:  "UPDATE ohs_sync.connections SET last_error = $1 WHERE user_id = $2",
		Args: []any{secret, "user-1"},
	})
	p.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505", Message: "duplicate " + secret}})
	cctx := p.TraceCopyFromStart(ctx, nil, pgx.TraceCopyFromStartData{TableName: pgx.Identifier{"ohs_data", "samples"}})
	p.TraceCopyFromEnd(cctx, nil, pgx.TraceCopyFromEndData{})
	bctx := p.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{Batch: &pgx.Batch{}})
	p.TraceBatchEnd(bctx, nil, pgx.TraceBatchEndData{})
	assert.True(t, job.IsRecording(), "the end hooks never end the caller's span")
	job.End()

	ended := rec.Ended()
	require.Len(t, ended, 4)
	upd := ended[0]
	assert.Equal(t, "UPDATE", upd.Name())
	assert.Equal(t, job.SpanContext().SpanID(), upd.Parent().SpanID())
	assert.Equal(t, trace.SpanKindClient, upd.SpanKind())
	assert.Equal(t, codes.Error, upd.Status().Code)
	assert.Empty(t, upd.Status().Description)
	attrs := map[string]string{}
	for _, kv := range upd.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	assert.Equal(t, map[string]string{
		"db.system.name": "postgresql", "db.operation.name": "UPDATE", "db.response.status_code": "23505",
	}, attrs)
	assert.Equal(t, "COPY", ended[1].Name())
	assert.Contains(t, ended[1].Attributes(), attrKV("db.collection.name", "ohs_data.samples"))
	assert.Equal(t, "BATCH", ended[2].Name())
	for _, s := range ended {
		for _, kv := range s.Attributes() {
			assert.NotContains(t, kv.Value.Emit(), secret)
			assert.NotContains(t, kv.Value.Emit(), "$1", "no statement text")
		}
		assert.Empty(t, s.Events())
	}
}

func TestLogHandlerAddsTheTraceID(t *testing.T) {
	_, tr := recorder(t)
	var buf bytes.Buffer
	log := slog.New(LogHandler(slog.NewJSONHandler(&buf, nil)))
	ctx, span := tr.Start(context.Background(), "job")
	log.With("a", 1).InfoContext(ctx, "inside")
	span.End()
	log.InfoContext(context.Background(), "outside")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	var in, out map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &in))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &out))
	assert.Equal(t, span.SpanContext().TraceID().String(), in["trace_id"])
	assert.Equal(t, span.SpanContext().SpanID().String(), in["span_id"])
	assert.NotContains(t, out, "trace_id")
}

// TestSetupExportsOverOTLPHTTP runs last: it replaces the globals.
func TestSetupExportsOverOTLPHTTP(t *testing.T) {
	var (
		mu    sync.Mutex
		paths []string
		body  []byte
	)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		body = append(body, b...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=test")
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	shutdown, err := Setup(t.Context(), Options{Enabled: true, Protocol: "http/protobuf", Version: "v9.9.9"})
	require.NoError(t, err)
	_, isSDK := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	require.True(t, isSDK)
	assert.Equal(t, []string{"traceparent", "tracestate"}, otel.GetTextMapPropagator().Fields())

	_, span := Tracer().Start(t.Context(), "pull job")
	span.End()
	require.NoError(t, shutdown(t.Context()), "shutdown flushes the buffered span")

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"/v1/traces"}, paths)
	// The protobuf body holds the strings as they are.
	for _, want := range []string{"pull job", "service.name", "ohs", "v9.9.9", "deployment.environment.name", ScopeName} {
		assert.True(t, bytes.Contains(body, []byte(want)), "export carries %q", want)
	}
}
