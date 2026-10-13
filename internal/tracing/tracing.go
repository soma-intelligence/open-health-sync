// SPDX-License-Identifier: AGPL-3.0-only

// Package tracing exports OpenTelemetry traces over OTLP (HTTP or gRPC)
// when the standard OTEL_ environment variables ask for it. Tracing is off by
// default: until Setup installs a provider, the global tracer is the
// OpenTelemetry no-op one, so the spans the other packages start cost next to
// nothing and go nowhere.
//
// Spans carry routes, job kinds, provider slugs, pseudonymous ids and status
// codes only: never a raw request path, a query string, a header, an SQL
// argument, a token or a health value.
package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// Attribute keys of the spans of this program, beyond the semantic
// conventions. Each value is one the logs already carry.
const (
	AttrProvider   = "ohs.provider"
	AttrTenantID   = "ohs.tenant_id"
	AttrUserID     = "ohs.user_id"
	AttrRequestID  = "ohs.request_id"
	AttrJobID      = "ohs.job.id"
	AttrJobKind    = "ohs.job.kind"
	AttrAttempts   = "ohs.job.attempts"
	AttrOutcome    = "ohs.job.outcome"
	AttrTier       = "ohs.etl.tier"
	AttrDeliveryID = "ohs.webhook.delivery_id"
	AttrEndpointID = "ohs.webhook.endpoint_id"
	AttrEventType  = "ohs.webhook.event_type"
	AttrAttempt    = "ohs.webhook.attempt"
)

// ScopeName names the instrumentation scope of every span of this program.
const ScopeName = "github.com/soma-intelligence/open-health-sync"

// DefaultServiceName is service.name unless OTEL_SERVICE_NAME or
// OTEL_RESOURCE_ATTRIBUTES set another.
const DefaultServiceName = "ohs"

// Tracer is the tracer every package starts its spans with. It resolves the
// global provider on each call, so spans started before Setup (or without
// it) are no-op spans.
func Tracer() trace.Tracer { return otel.Tracer(ScopeName) }

// Options select what Setup installs.
type Options struct {
	// Enabled turns tracing on; when false Setup installs nothing.
	Enabled bool
	// Protocol is "grpc" for OTLP over gRPC; anything else is OTLP over
	// HTTP/protobuf.
	Protocol string
	// Version becomes service.version.
	Version string
}

// Setup installs the OTLP exporter, the tracer provider and the W3C trace
// context propagator as the OpenTelemetry globals, when o.Enabled. The
// exporter, the sampler and the resource read the standard OTEL_ variables
// themselves (endpoint, headers, timeout, OTEL_TRACES_SAMPLER,
// OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES).
//
// The returned shutdown flushes the spans still buffered and stops the
// exporter; it is never nil.
func Setup(ctx context.Context, o Options) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }
	if !o.Enabled {
		return noop, nil
	}
	var exp sdktrace.SpanExporter
	if o.Protocol == "grpc" {
		exp, err = otlptracegrpc.New(ctx)
	} else {
		exp, err = otlptracehttp.New(ctx)
	}
	if err != nil {
		return noop, fmt.Errorf("tracing: OTLP exporter: %w", err)
	}
	// Later options win: the defaults first, then what the environment says.
	res, err := resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(semconv.ServiceName(DefaultServiceName), semconv.ServiceVersion(o.Version)),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
	)
	if err != nil {
		_ = exp.Shutdown(ctx)
		return noop, fmt.Errorf("tracing: resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Warn("tracing: export failed", "error", err)
	}))
	return tp.Shutdown, nil
}

// Extract returns ctx carrying the remote span context of an incoming
// traceparent header, when tracing is on (the default propagator extracts
// nothing).
func Extract(ctx context.Context, h http.Header) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(h))
}

// knownMethods are the request methods kept as they are in
// http.request.method; any other is "_OTHER", so a client cannot fill the
// tracing backend with made-up values.
var knownMethods = map[string]bool{
	http.MethodConnect: true, http.MethodDelete: true, http.MethodGet: true, http.MethodHead: true,
	http.MethodOptions: true, http.MethodPatch: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodTrace: true,
}

// Method is the method as http.request.method records it.
func Method(m string) string {
	if knownMethods[m] {
		return m
	}
	return "_OTHER"
}

// Route is the path part of a ServeMux pattern ("GET /v1/users/{id}" gives
// "/v1/users/{id}"): the template, never the path a client sent.
func Route(pattern string) string {
	if i := strings.Index(pattern, "/"); i >= 0 {
		return pattern[i:]
	}
	return ""
}

// SpanName is the name of a server span: the method and the route, or the
// method alone when no route matched ("HTTP" for an unknown method).
func SpanName(method, route string) string {
	if method == "_OTHER" {
		method = "HTTP"
	}
	if route == "" {
		return method
	}
	return method + " " + route
}
