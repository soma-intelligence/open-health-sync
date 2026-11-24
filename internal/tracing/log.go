// SPDX-License-Identifier: AGPL-3.0-only

package tracing

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"github.com/soma-intelligence/open-health-sync/internal/logging"
)

// LogContext returns ctx with trace_id and span_id on the logger it carries, so every line
// written with logging.Ctx inside a span links to its trace. Call it right after starting a span.
// Without a span (tracing off, or no span open) ctx is returned unchanged.
func LogContext(ctx context.Context) context.Context {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ctx
	}
	ctx = logging.WithTraceID(ctx, sc.TraceID().String())
	return logging.With(ctx, "span_id", sc.SpanID().String())
}
