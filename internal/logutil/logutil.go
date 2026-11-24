// SPDX-License-Identifier: AGPL-3.0-only

// Package logutil holds the small logging helpers specific to Open Health Sync, on top of the
// shared internal/logging package (which is never edited here).
package logutil

import (
	"context"
	"errors"
	"strings"

	"github.com/rs/zerolog"
)

// Service is the "service" field of every log line of this binary.
const Service = "ohs"

// Shutdown reports whether err comes from a shutdown or a client that left: a cancelled context
// or a database pool already closed. The logging spec (section 4) puts these at debug, never at
// error. A deadline is not included: see ShutdownCtx.
func Shutdown(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	return strings.Contains(err.Error(), "database is closed")
}

// ShutdownCtx is Shutdown plus context.DeadlineExceeded, but only while ctx, the context of the
// request, loop or job doing the logging, is itself done: then the deadline is the one that
// ended it (or a client that left). A deadline while ctx is alive is a provider or a query that
// timed out, which is worth its normal level.
func ShutdownCtx(ctx context.Context, err error) bool {
	if Shutdown(err) {
		return true
	}
	return err != nil && ctx.Err() != nil && errors.Is(err, context.DeadlineExceeded)
}

// Level returns def, or debug when err comes from a shutdown.
func Level(err error, def zerolog.Level) zerolog.Level {
	if Shutdown(err) {
		return zerolog.DebugLevel
	}
	return def
}

// LevelCtx is Level for a caller that has the context of the work, see ShutdownCtx.
func LevelCtx(ctx context.Context, err error, def zerolog.Level) zerolog.Level {
	if ShutdownCtx(ctx, err) {
		return zerolog.DebugLevel
	}
	return def
}

// Err opens an event at the wanted level (debug for a shutdown) with the error attached.
func Err(log zerolog.Logger, err error, def zerolog.Level) *zerolog.Event {
	return log.WithLevel(Level(err, def)).Err(err)
}

// ErrCtx is Err for a caller that has the context of the work, see ShutdownCtx.
func ErrCtx(ctx context.Context, log zerolog.Logger, err error, def zerolog.Level) *zerolog.Event {
	return log.WithLevel(LevelCtx(ctx, err, def)).Err(err)
}
