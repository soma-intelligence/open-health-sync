package logging

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/rs/zerolog"
	zerologlog "github.com/rs/zerolog/log"
)

func TestInitLevelFiltersBelowThreshold(t *testing.T) {
	var buf bytes.Buffer
	l := Init(Options{Level: "warn", Format: FormatJSON, Out: &buf})

	l.Info().Msg("dropped")
	l.Warn().Msg("kept")

	if got := buf.String(); !contains(got, "kept") || contains(got, "dropped") {
		t.Fatalf("level not applied, got %q", got)
	}
}

func TestInitEmptyLevelDefaultsToInfo(t *testing.T) {
	var buf bytes.Buffer
	l := Init(Options{Level: "", Format: FormatJSON, Out: &buf})

	l.Debug().Msg("dropped")
	l.Info().Msg("kept")

	if got := buf.String(); !contains(got, "kept") || contains(got, "dropped") {
		t.Fatalf("empty level should mean info, got %q", got)
	}
}

func TestInitGarbageLevelDefaultsToInfo(t *testing.T) {
	var buf bytes.Buffer
	l := Init(Options{Level: "loud", Format: FormatJSON, Out: &buf})

	l.Debug().Msg("dropped")
	l.Info().Msg("kept")

	if got := buf.String(); !contains(got, "kept") || contains(got, "dropped") {
		t.Fatalf("garbage level should mean info, got %q", got)
	}
}

func TestInitWritesParseableJSONWithTimestamp(t *testing.T) {
	var buf bytes.Buffer
	l := Init(Options{Level: "info", Format: FormatJSON, Out: &buf})
	l.Info().Str("k", "v").Msg("hello")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("output is not JSON: %v (%q)", err, buf.String())
	}
	if line["message"] != "hello" || line["level"] != "info" || line["k"] != "v" {
		t.Fatalf("unexpected fields: %v", line)
	}
	if _, ok := line["time"]; !ok {
		t.Fatalf("no timestamp: %v", line)
	}
}

func TestModuleTagsEveryLine(t *testing.T) {
	var buf bytes.Buffer
	Init(Options{Level: "info", Format: FormatJSON, Out: &buf})

	moduleLogger := Module("app")
	moduleLogger.Info().Msg("hello")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if line["module"] != "app" {
		t.Fatalf("module tag missing: %v", line)
	}
}

func TestResolveFormatExplicitWins(t *testing.T) {
	if got := resolveFormat("console", &bytes.Buffer{}); got != FormatConsole {
		t.Fatalf("explicit console ignored, got %q", got)
	}
	if got := resolveFormat("JSON", os.Stdout); got != FormatJSON {
		t.Fatalf("explicit json ignored, got %q", got)
	}
}

func TestResolveFormatAutoIsJSONForNonTerminal(t *testing.T) {
	if got := resolveFormat("", &bytes.Buffer{}); got != FormatJSON {
		t.Fatalf("a buffer is not a terminal, got %q", got)
	}
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := resolveFormat("", f); got != FormatJSON {
		t.Fatalf("a regular file is not a terminal, got %q", got)
	}
}

func TestInitSetsTheZerologGlobal(t *testing.T) {
	var buf bytes.Buffer
	Init(Options{Level: "info", Format: FormatJSON, Out: &buf})

	// This is the whole reason existing call sites need no edits: they call the
	// package-level helpers of rs/zerolog/log, which delegate to this global.
	zerologlog.Info().Msg("from the global")

	if !contains(buf.String(), "from the global") {
		t.Fatalf("Init did not install the zerolog global, got %q", buf.String())
	}
}

func TestInitBaseFields(t *testing.T) {
	var buf bytes.Buffer
	l := Init(Options{Service: "svc", Format: FormatJSON, Out: &buf})
	l.Info().Msg("hello")

	line := decode(t, buf.Bytes())
	for k, want := range map[string]string{"service": "svc", "env": "dev", "version": "dev"} {
		if line[k] != want {
			t.Fatalf("%s = %v, want %q (line %v)", k, line[k], want, line)
		}
	}

	buf.Reset()
	l = Init(Options{Service: "svc", Env: "prod", Version: "abc123", Format: FormatJSON, Out: &buf})
	l.Info().Msg("hello")
	line = decode(t, buf.Bytes())
	if line["env"] != "prod" || line["version"] != "abc123" {
		t.Fatalf("explicit env/version lost: %v", line)
	}
}

func TestInitExtraGetsSameLine(t *testing.T) {
	var out, extra bytes.Buffer
	l := Init(Options{Service: "svc", Format: FormatJSON, Out: &out, Extra: &extra})
	l.Info().Str("k", "v").Msg("hello")

	if out.Len() == 0 || !bytes.Equal(out.Bytes(), extra.Bytes()) {
		t.Fatalf("Out and Extra differ:\nout   %q\nextra %q", out.String(), extra.String())
	}
}

func TestInitCLIWritesStderrConsole(t *testing.T) {
	var buf bytes.Buffer
	l := Init(Options{Service: "tool", CLI: true, Out: &buf})
	l.Warn().Msg("careful")

	got := buf.String()
	if !contains(got, "careful") {
		t.Fatalf("message missing: %q", got)
	}
	if contains(got, `"time"`) || json.Valid(buf.Bytes()) {
		t.Fatalf("CLI output must be console text without a time key, got %q", got)
	}
}

func TestParseLevelUnknownIsInfo(t *testing.T) {
	for _, s := range []string{"WARNING", "", "verbose"} {
		if got := parseLevel(s); got != zerolog.InfoLevel {
			t.Fatalf("parseLevel(%q) = %v, want info", s, got)
		}
	}
}

func TestInitReadsEnvWhenOptionsEmpty(t *testing.T) {
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("LOG_FORMAT", "json")
	var buf bytes.Buffer
	l := Init(Options{Service: "svc", Out: &buf})
	l.Info().Msg("dropped")
	l.Warn().Msg("kept")

	if contains(buf.String(), "dropped") {
		t.Fatalf("LOG_LEVEL=warn ignored: %q", buf.String())
	}
	if decode(t, buf.Bytes())["level"] != "warn" {
		t.Fatalf("LOG_FORMAT=json ignored: %q", buf.String())
	}
}

func TestInitOptionsBeatEnv(t *testing.T) {
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("LOG_FORMAT", "console")
	var buf bytes.Buffer
	l := Init(Options{Service: "svc", Level: "debug", Format: FormatJSON, Out: &buf})
	l.Debug().Msg("kept")

	if decode(t, buf.Bytes())["message"] != "kept" {
		t.Fatalf("options did not beat env: %q", buf.String())
	}
}

func contains(hay, needle string) bool {
	return bytes.Contains([]byte(hay), []byte(needle))
}
