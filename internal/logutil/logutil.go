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

// Shutdown reports whether err comes from a shutdown or a client that left: a cancelled or
// expired context, or a database pool already closed. The logging spec (section 4) puts these
// at debug, never at error.
func Shutdown(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return strings.Contains(err.Error(), "database is closed")
}

// Level returns def, or debug when err comes from a shutdown.
func Level(err error, def zerolog.Level) zerolog.Level {
	if Shutdown(err) {
		return zerolog.DebugLevel
	}
	return def
}

// Err opens an event at the wanted level (debug for a shutdown) with the error attached.
func Err(log zerolog.Logger, err error, def zerolog.Level) *zerolog.Event {
	return log.WithLevel(Level(err, def)).Err(err)
}
