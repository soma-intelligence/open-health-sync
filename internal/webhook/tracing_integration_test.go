// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package webhook_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/soma-intelligence/open-health-sync/internal/tracing"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

func TestDeliveryAttemptSpans(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rcv := newReceiver(t, 503)
	ep, secret, err := e.store.Create(ctx, e.tenant, rcv.srv.URL+"/hook?key=query-secret", "app", []string{webhook.DataWritten})
	require.NoError(t, err)

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})

	e.enqueue(webhook.DataWritten)
	w := e.worker()
	assert.Equal(t, 1, pass(t, w)) // 503
	assert.Equal(t, 1, pass(t, w)) // 204
	require.Equal(t, 2, rcv.count())
	assert.Empty(t, rcv.got[0].Header.Get("traceparent"), "no trace context goes to the receiver")

	var spans []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.InstrumentationScope().Name == tracing.ScopeName && s.Name() == "webhook delivery" {
			spans = append(spans, s)
		}
	}
	require.Len(t, spans, 2)
	for i, want := range []struct {
		code, attempt string
		status        codes.Code
	}{{"503", "1", codes.Error}, {"204", "2", codes.Unset}} {
		s := spans[i]
		attrs := map[string]string{}
		for _, kv := range s.Attributes() {
			attrs[string(kv.Key)] = kv.Value.Emit()
			for _, leak := range []string{"query-secret", rcv.srv.URL, secret, "oura", "u-1"} {
				assert.NotContains(t, kv.Value.Emit(), leak, "attribute %s", kv.Key)
			}
		}
		assert.Equal(t, want.code, attrs["http.response.status_code"])
		assert.Equal(t, want.attempt, attrs["ohs.webhook.attempt"])
		assert.Equal(t, ep.ID.String(), attrs["ohs.webhook.endpoint_id"])
		assert.Equal(t, webhook.DataWritten, attrs["ohs.webhook.event_type"])
		assert.Contains(t, attrs, "ohs.webhook.delivery_id")
		assert.Equal(t, want.status, s.Status().Code)
		assert.Empty(t, s.Status().Description)
		assert.Empty(t, s.Events())
	}
}
