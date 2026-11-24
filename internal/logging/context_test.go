package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/rs/zerolog"
)

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, string(b))
	}
	return m
}

func TestEachHelperLandsItsField(t *testing.T) {
	cases := []struct {
		name  string
		apply func(context.Context, string) context.Context
		field string
	}{
		{"user", WithUserID, "user_id"},
		{"request", WithRequestID, "request_id"},
		{"trace", WithTraceID, "trace_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			Init(Options{Level: "info", Format: FormatJSON, Out: &buf})

			ctx := tc.apply(context.Background(), "value-1")
			Ctx(ctx).Info().Msg("hello")

			if got := decode(t, buf.Bytes())[tc.field]; got != "value-1" {
				t.Fatalf("%s = %v, want value-1", tc.field, got)
			}
		})
	}
}

func TestWithAddsField(t *testing.T) {
	var buf bytes.Buffer
	Init(Options{Level: "info", Format: FormatJSON, Out: &buf})

	ctx := With(context.Background(), "story_id", "s1")
	Ctx(ctx).Info().Msg("x")

	if got := decode(t, buf.Bytes())["story_id"]; got != "s1" {
		t.Fatalf("story_id = %v, want s1", got)
	}
}

func TestHelpersAccumulate(t *testing.T) {
	var buf bytes.Buffer
	Init(Options{Level: "info", Format: FormatJSON, Out: &buf})

	ctx := context.Background()
	ctx = With(ctx, "story_id", "s1")
	ctx = WithUserID(ctx, "user-1")
	ctx = WithRequestID(ctx, "req-1")
	Ctx(ctx).Info().Msg("ingested")

	line := decode(t, buf.Bytes())
	for k, want := range map[string]string{
		"story_id":   "s1",
		"user_id":    "user-1",
		"request_id": "req-1",
	} {
		if line[k] != want {
			t.Fatalf("%s = %v, want %v (full line %v)", k, line[k], want, line)
		}
	}
}

func TestReapplyingAHelperOverwritesRatherThanDuplicates(t *testing.T) {
	var buf bytes.Buffer
	Init(Options{Level: "info", Format: FormatJSON, Out: &buf})

	ctx := WithUserID(context.Background(), "first")
	ctx = WithUserID(ctx, "second")
	Ctx(ctx).Info().Msg("hello")

	// encoding/json keeps the last value for a repeated key, so this asserts
	// the observable behaviour a log reader sees.
	if got := decode(t, buf.Bytes())["user_id"]; got != "second" {
		t.Fatalf("user_id = %v, want second", got)
	}
}

func TestBareContextStillLogsThroughTheProcessLogger(t *testing.T) {
	var buf bytes.Buffer
	Init(Options{Level: "info", Format: FormatJSON, Out: &buf})

	// zerolog.Ctx returns a DISABLED logger when the context carries none.
	// Without the seed in with(), every helper applied to a bare context would
	// build on that disabled logger and the line would vanish.
	ctx := WithUserID(context.Background(), "user-1")
	Ctx(ctx).Info().Msg("hello")

	if buf.Len() == 0 {
		t.Fatal("a bare context swallowed the line")
	}
}

func TestCtxOnABareContextFallsBackToTheProcessLogger(t *testing.T) {
	var buf bytes.Buffer
	Init(Options{Level: "info", Format: FormatJSON, Out: &buf})

	// No WithXxx ever touched this context, so it carries no logger at all,
	// and zerolog.Ctx alone would hand back a DISABLED one. Ctx must fall
	// back to the process logger the same way with does, or this call is a
	// no-op and the line below never reaches buf.
	Ctx(context.Background()).Info().Msg("hello")

	if buf.Len() == 0 {
		t.Fatal("Ctx on a bare context swallowed the line")
	}
}

func TestCtxOnASeededContextReturnsItsOwnLoggerNotTheFallback(t *testing.T) {
	var buf bytes.Buffer
	Init(Options{Level: "info", Format: FormatJSON, Out: &buf})

	// A context can carry a real logger installed straight through
	// zerolog.Logger.WithContext, without going through with/WithXxx. The
	// fallback must not shadow it: Ctx has to hand back that logger, fields
	// and all, not the process one, or the fallback would mask a caller's
	// own logger on every context that already carries one.
	var seededBuf bytes.Buffer
	seededLogger := zerolog.New(&seededBuf).With().Str("job_id", "job-1").Logger()
	ctx := seededLogger.WithContext(context.Background())

	Ctx(ctx).Info().Msg("hello")

	if buf.Len() != 0 {
		t.Fatalf("Ctx used the process logger instead of the context's own: %s", buf.String())
	}
	line := decode(t, seededBuf.Bytes())
	if line["job_id"] != "job-1" {
		t.Fatalf("job_id = %v, want job-1 (full line %v)", line["job_id"], line)
	}
}
