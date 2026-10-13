// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/soma-intelligence/open-health-sync/internal/reqid"
)

// recordSpans installs an in-memory tracer provider and the W3C propagator
// as the globals for the test, as tracing.Setup does with an exporter.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		_ = tp.Shutdown(t.Context())
	})
	return rec
}

func serverSpans(rec *tracetest.SpanRecorder) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.SpanKind() == trace.SpanKindServer {
			out = append(out, s)
		}
	}
	return out
}

func TestRequestSpanNamedByRoutePattern(t *testing.T) {
	rec := recordSpans(t)
	h := offlineRouter(t)

	const (
		userID     = "3f1d5c1e-0b7a-4c1e-9d55-1d2b8f9e0a11"
		parentTID  = "4bf92f3577b34da6a3ce929d0e0e4736"
		parentSID  = "00f067aa0ba902b7"
		secretWord = "sekrit-value"
	)
	req := httptest.NewRequest("GET", "/v1/users/"+userID+"/samples?type=heart_rate&token="+secretWord, nil)
	req.Header.Set("traceparent", "00-"+parentTID+"-"+parentSID+"-01")
	req.Header.Set("Authorization", "Bearer "+secretWord)
	req.Header.Set(reqid.Header, "rid-trace-1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	spans := serverSpans(rec)
	require.Len(t, spans, 1)
	s := spans[0]
	assert.Equal(t, "GET /v1/users/{id}/samples", s.Name())
	assert.Equal(t, parentTID, s.SpanContext().TraceID().String(), "joins the caller's trace")
	assert.Equal(t, parentSID, s.Parent().SpanID().String())
	assert.True(t, s.Parent().IsRemote())

	attrs := map[string]string{}
	for _, kv := range s.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	assert.Equal(t, "GET", attrs["http.request.method"])
	assert.Equal(t, "/v1/users/{id}/samples", attrs["http.route"])
	assert.Equal(t, "401", attrs["http.response.status_code"])
	assert.Equal(t, "rid-trace-1", attrs["ohs.request_id"])
	for k, v := range attrs {
		for _, leak := range []string{userID, secretWord, "heart_rate", "?"} {
			assert.NotContains(t, v, leak, "attribute %s leaks %q", k, leak)
		}
	}
	assert.NotContains(t, s.Name(), userID)
	assert.Empty(t, s.Events(), "no events (an event could carry an error text)")
}

func TestRequestSpanWithoutParentAndUnmatchedRoute(t *testing.T) {
	rec := recordSpans(t)
	h := offlineRouter(t)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/nothing-here/12345", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("BREW", "/v1/users", nil))

	spans := serverSpans(rec)
	require.Len(t, spans, 2)
	assert.Equal(t, "GET /", spans[0].Name(), "the catch-all pattern, not the path")
	assert.False(t, spans[0].Parent().IsValid(), "a new trace without traceparent")
	assert.Equal(t, "HTTP /", spans[1].Name(), "an unknown method is not recorded as sent")
	for _, s := range spans {
		for _, kv := range s.Attributes() {
			assert.False(t, strings.Contains(kv.Value.Emit(), "12345") || strings.Contains(kv.Value.Emit(), "BREW"),
				"attribute %s = %q", kv.Key, kv.Value.Emit())
		}
	}
}

func TestNoSpanWhenTracingIsOff(t *testing.T) {
	// OpenTelemetry's defaults, which stay in place when tracing.Setup is
	// not enabled: a no-op provider and a propagator that extracts nothing.
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(noop.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	req := httptest.NewRequest("GET", "/v1/users", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	var span trace.Span
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span = trace.SpanFromContext(r.Context())
	})
	s := &server{metrics: newHTTPMetrics()}
	s.instrument(probe).ServeHTTP(httptest.NewRecorder(), req)
	require.NotNil(t, span)
	assert.False(t, span.IsRecording())
	assert.False(t, span.SpanContext().IsValid(), "the incoming traceparent is ignored")
}
