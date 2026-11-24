// Package logging owns the one logger of a process. Init is called once, first
// thing in main. The process then takes its own tagged copy with Module.
//
// This package is the canonical copy (see VERSION). Other repos hold identical
// copies written by scripts/sync-logging.sh, so the shared files import only
// the standard library and github.com/rs/zerolog. Anything specific to the
// canonical repo lives in files that are never copied.
package logging

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Format values accepted in Options.Format and in the LOG_FORMAT variable.
const (
	FormatJSON    = "json"
	FormatConsole = "console"
)

// Options configures the process logger.
type Options struct {
	// Service is the "service" field of every line and the Loki label.
	Service string
	// Version is the build version or git sha. Empty means "dev".
	Version string
	// Env is prod, staging or dev. Empty means "dev".
	Env string
	// Level overrides LOG_LEVEL when set: trace, debug, info, warn, error, fatal, panic.
	// Empty or unrecognised means info.
	Level string
	// Format overrides LOG_FORMAT when set: "json", "console", or empty for auto, which
	// resolves to console when Out is a terminal and JSON otherwise.
	Format string
	// Out defaults to os.Stdout, or os.Stderr in CLI mode. Never stderr for a
	// service: two streams of different nature in one container is what one
	// service used to do.
	Out io.Writer
	// Extra also receives every JSON line (an OTLP or Loki hook). Nil by
	// default.
	Extra io.Writer
	// CLI is for command-line tools: console output on stderr, no timestamp.
	CLI bool
}

const defaultTag = "dev"

// Init installs the process-wide logger and returns it.
//
// It assigns log.Logger, the global of github.com/rs/zerolog/log. That single
// assignment is what lets existing call sites emit the new format without one
// source edit: they all go through that global.
func Init(o Options) zerolog.Logger {
	// RFC3339Nano rather than a Unix timestamp, which Azure Log Analytics
	// cannot read as a time.
	zerolog.TimeFieldFormat = time.RFC3339Nano

	// Empty Level and Format fall back to LOG_LEVEL and LOG_FORMAT, so every
	// service honours them without wiring; explicit Options values win.
	if o.Level == "" {
		o.Level = os.Getenv("LOG_LEVEL")
	}
	if o.Format == "" {
		o.Format = os.Getenv("LOG_FORMAT")
	}

	var out io.Writer = os.Stdout
	switch {
	case o.Out != nil:
		out = o.Out
	case o.CLI:
		out = os.Stderr
	}

	var main io.Writer = out
	if o.CLI || resolveFormat(o.Format, out) == FormatConsole {
		cw := zerolog.ConsoleWriter{Out: out, TimeFormat: time.RFC3339, NoColor: !isTerminal(out)}
		if o.CLI {
			cw.PartsExclude = []string{zerolog.TimestampFieldName}
		}
		main = cw
	}
	if o.Extra != nil {
		main = zerolog.MultiLevelWriter(main, o.Extra)
	}

	zerolog.SetGlobalLevel(parseLevel(o.Level))
	c := zerolog.New(main).With()
	if !o.CLI {
		c = c.Timestamp()
	}
	if o.Service != "" {
		c = c.Str("service", o.Service)
	}
	c = c.Str("env", orDefault(o.Env)).Str("version", orDefault(o.Version))
	log.Logger = c.Logger()
	return log.Logger
}

func orDefault(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return defaultTag
	}
	return s
}

// Module returns the process logger tagged with the module that owns it, so a
// line says who wrote it. Before this, only feed tagged itself.
func Module(name string) zerolog.Logger {
	return log.Logger.With().Str("module", name).Logger()
}

// parseLevel maps LOG_LEVEL onto a zerolog level. Both an empty string and an
// unrecognised one mean info: a typo in a deployment variable must not silence
// the process, which is what returning NoLevel would do.
func parseLevel(s string) zerolog.Level {
	lvl, err := zerolog.ParseLevel(strings.ToLower(strings.TrimSpace(s)))
	if err != nil || lvl == zerolog.NoLevel {
		return zerolog.InfoLevel
	}
	return lvl
}

// resolveFormat decides between JSON and console. An explicit value always
// wins. Empty means auto: console when the destination is a character device,
// which is a developer's terminal, JSON otherwise, which is every container.
func resolveFormat(format string, out io.Writer) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatConsole:
		return FormatConsole
	case FormatJSON:
		return FormatJSON
	}
	if isTerminal(out) {
		return FormatConsole
	}
	return FormatJSON
}

// isTerminal reports whether out is a character device, a developer's
// terminal, as opposed to a pipe, a file or a buffer.
func isTerminal(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
