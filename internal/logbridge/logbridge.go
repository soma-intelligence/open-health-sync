// SPDX-License-Identifier: AGPL-3.0-only

// Package logbridge adapts the process logger to the few standard library and third party APIs
// that still want a *log.Logger (http.Server.ErrorLog). It lives outside internal/logging so that
// package stays byte-identical to the shared copy. It is the only place allowed to import "log".
package logbridge

import (
	stdlog "log"
	"strings"

	"github.com/rs/zerolog"
)

type writer struct {
	log   zerolog.Logger
	level zerolog.Level
	event string
}

func (w writer) Write(p []byte) (int, error) {
	w.log.WithLevel(w.level).Str("detail", strings.TrimSpace(string(p))).Msg(w.event)
	return len(p), nil
}

// New returns a *log.Logger that writes each line to log at level, as event with the text in the
// "detail" field. net/http writes one line per call.
func New(log zerolog.Logger, level zerolog.Level, event string) *stdlog.Logger {
	return stdlog.New(writer{log: log, level: level, event: event}, "", 0)
}
