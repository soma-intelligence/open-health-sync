// SPDX-License-Identifier: AGPL-3.0-only

package reqid

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	if got := Sanitize("abc-123_x.y"); got != "abc-123_x.y" {
		t.Fatalf("a valid id is kept: %q", got)
	}
	for _, in := range []string{"", "has space", "new\nline", strings.Repeat("a", 129), "é"} {
		got := Sanitize(in)
		if got == in || len(got) != 32 {
			t.Errorf("%q: got %q, want a fresh 32 character id", in, got)
		}
	}
	if Sanitize("") == Sanitize("") {
		t.Fatal("fresh ids differ")
	}
}

func TestHandlerAddsTheID(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(Handler(slog.NewJSONHandler(&buf, nil)))
	log.InfoContext(With(context.Background(), "rid1"), "hello")
	log.Info("no context")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var first, second map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &first)
	_ = json.Unmarshal([]byte(lines[1]), &second)
	if first["request_id"] != "rid1" {
		t.Fatalf("first line: %v", first)
	}
	if _, ok := second["request_id"]; ok {
		t.Fatalf("second line: %v", second)
	}
	// WithAttrs and WithGroup keep the wrapper.
	buf.Reset()
	log.With("a", 1).WithGroup("g").InfoContext(With(context.Background(), "rid2"), "x", "b", 2)
	if !strings.Contains(buf.String(), `"request_id":"rid2"`) {
		t.Fatalf("derived logger lost the id: %s", buf.String())
	}
}
