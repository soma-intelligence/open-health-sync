package logging

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// With attaches one string field to the logger carried by ctx and returns a
// context carrying the result. Every helper below is one line of this, and it
// is the way to add any other correlation field (story_id, job_id, ...).
//
// The seed matters. zerolog.Ctx returns a DISABLED logger when the context
// carries none, so building on it directly would silently swallow every line
// logged from a context that no middleware ever touched, which is most
// background work. Detecting that case by its level is a heuristic, because a
// caller could legitimately install a disabled logger; the cost of getting it
// wrong is a line that gets printed when someone asked for silence, which is
// the safe direction for a logging change.
func With(ctx context.Context, key, val string) context.Context {
	return seeded(ctx).With().Str(key, val).Logger().WithContext(ctx)
}

// seeded is the fallback With and Ctx share: the logger ctx already carries,
// or the process logger installed by Init when it carries none. See the note
// on With above for why the fallback exists and what it can get wrong.
func seeded(ctx context.Context) *zerolog.Logger {
	base := zerolog.Ctx(ctx)
	if base.GetLevel() == zerolog.Disabled {
		seed := log.Logger
		base = &seed
	}
	return base
}

// WithUserID tags every downstream record with the member the work belongs to.
func WithUserID(ctx context.Context, id string) context.Context {
	return With(ctx, "user_id", id)
}

// WithRequestID tags every downstream record with the HTTP request it serves.
func WithRequestID(ctx context.Context, id string) context.Context {
	return With(ctx, "request_id", id)
}

// WithTraceID tags every downstream record with the trace id, for a future
// OpenTelemetry tracer.
func WithTraceID(ctx context.Context, id string) context.Context {
	return With(ctx, "trace_id", id)
}

// Ctx returns the logger carried by ctx. Use it instead of the package-level
// helpers anywhere a context is in hand, or the correlation fields are lost.
//
// It used to be a bare zerolog.Ctx(ctx), which hands back a DISABLED logger
// for a context nothing has seeded. Every call built on that logger is a
// no-op, so any code path reached through a context that no WithXxx ever
// touched, which is most background work, printed nothing and no error
// marked the loss. Ctx now falls back the same way With does, sharing the
// same tradeoff: the disabled-level check cannot tell an unseeded context
// from one where a caller deliberately installed a disabled logger, and this
// file picks printing over swallowing either way.
func Ctx(ctx context.Context) *zerolog.Logger {
	return seeded(ctx)
}
