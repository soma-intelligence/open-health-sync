// SPDX-License-Identifier: AGPL-3.0-only

package tracing

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// LogHandler wraps h so that a record logged with a context inside a span
// gets trace_id and span_id attributes, which link the log line to its trace.
// Without a span (tracing off, or no span open) records pass unchanged.
func LogHandler(h slog.Handler) slog.Handler { return logHandler{h} }

type logHandler struct{ slog.Handler }

func (h logHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r = r.Clone()
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h logHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return logHandler{h.Handler.WithAttrs(as)}
}

func (h logHandler) WithGroup(name string) slog.Handler {
	return logHandler{h.Handler.WithGroup(name)}
}
