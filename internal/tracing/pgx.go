// SPDX-License-Identifier: AGPL-3.0-only

package tracing

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// PGX traces the queries of a pgx connection as client spans named by the
// SQL operation ("SELECT", "INSERT", ...). A span records the operation, the
// table of a COPY and the SQLSTATE of a failure: never the statement text,
// an argument or an error message (one can quote a value).
//
// A query only gets a span inside a span already open (a request, a job):
// the polling of the job queues would otherwise start a trace every second.
type PGX struct{}

var (
	_ pgx.QueryTracer    = PGX{}
	_ pgx.BatchTracer    = PGX{}
	_ pgx.CopyFromTracer = PGX{}
)

// pgxSpanKey marks a context holding a span PGX started, so the end hook
// never ends the caller's span.
type pgxSpanKey struct{}

// operations are the SQL keywords used as span names; any other first word
// (a prepared statement name, say) is "QUERY".
var operations = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "WITH": true, "MERGE": true,
	"BEGIN": true, "COMMIT": true, "ROLLBACK": true, "SAVEPOINT": true, "RELEASE": true,
	"COPY": true, "CREATE": true, "ALTER": true, "DROP": true, "TRUNCATE": true, "LOCK": true,
	"SET": true, "SHOW": true, "VALUES": true, "CALL": true, "DO": true, "ANALYZE": true,
	"VACUUM": true, "REFRESH": true, "LISTEN": true, "NOTIFY": true, "UNLISTEN": true,
}

// Operation is the first keyword of sql, upper case, past white space and
// comments; "QUERY" when it is not a known SQL keyword.
func Operation(sql string) string {
	s := sql
	for {
		s = strings.TrimLeft(s, " \t\r\n(")
		switch {
		case strings.HasPrefix(s, "--"):
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				return "QUERY"
			}
			s = s[i+1:]
			continue
		case strings.HasPrefix(s, "/*"):
			i := strings.Index(s, "*/")
			if i < 0 {
				return "QUERY"
			}
			s = s[i+2:]
			continue
		}
		break
	}
	end := strings.IndexFunc(s, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') })
	if end < 0 {
		end = len(s)
	}
	word := strings.ToUpper(s[:end])
	if operations[word] {
		return word
	}
	return "QUERY"
}

func (PGX) start(ctx context.Context, op string, attrs ...attribute.KeyValue) context.Context {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	attrs = append(attrs, semconv.DBSystemNamePostgreSQL, semconv.DBOperationName(op))
	ctx, span := Tracer().Start(ctx, op, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	return context.WithValue(ctx, pgxSpanKey{}, span)
}

func (PGX) end(ctx context.Context, err error) {
	span, ok := ctx.Value(pgxSpanKey{}).(trace.Span)
	if !ok {
		return
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			span.SetAttributes(semconv.DBResponseStatusCode(pgErr.Code))
		}
		span.SetStatus(codes.Error, "")
	}
	span.End()
}

// TraceQueryStart implements pgx.QueryTracer.
func (t PGX) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return t.start(ctx, Operation(data.SQL))
}

// TraceQueryEnd implements pgx.QueryTracer.
func (t PGX) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	t.end(ctx, data.Err)
}

// TraceBatchStart implements pgx.BatchTracer.
func (t PGX) TraceBatchStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	n := 0
	if data.Batch != nil {
		n = data.Batch.Len()
	}
	return t.start(ctx, "BATCH", semconv.DBOperationBatchSize(n))
}

// TraceBatchQuery implements pgx.BatchTracer. The queries of a batch are not
// spans of their own.
func (PGX) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}

// TraceBatchEnd implements pgx.BatchTracer.
func (t PGX) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	t.end(ctx, data.Err)
}

// TraceCopyFromStart implements pgx.CopyFromTracer.
func (t PGX) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	return t.start(ctx, "COPY", semconv.DBCollectionName(strings.Join(data.TableName, ".")))
}

// TraceCopyFromEnd implements pgx.CopyFromTracer.
func (t PGX) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	t.end(ctx, data.Err)
}
