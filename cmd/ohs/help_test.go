// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestTopLevelHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}, {"-help"}} {
		var buf bytes.Buffer
		if err := run(args, &buf); err != nil {
			t.Fatalf("run %v: %v", args, err)
		}
		out := buf.String()
		for _, want := range []string{"Usage: ohs <command>", "up", "serve", "migrate", "doctor", "admin", "mcp", "import", "version", "Exit status"} {
			if !strings.Contains(out, want) {
				t.Errorf("run %v: help lacks %q:\n%s", args, want, out)
			}
		}
	}
}

func TestVersionFlag(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"-v"}} {
		var buf bytes.Buffer
		if err := run(args, &buf); err != nil || !strings.HasPrefix(buf.String(), "ohs ") {
			t.Fatalf("run %v: %v %q", args, err, buf.String())
		}
	}
}

func TestSubcommandHelp(t *testing.T) {
	cases := map[string][]string{
		"usage: ohs migrate": {"migrate", "--help"},
		"usage: ohs serve":   {"serve", "-h"},
		"usage: ohs up":      {"up", "-h"},
		"usage: ohs doctor":  {"doctor", "-h"},
		"usage: ohs admin":   {"admin", "tenant", "create", "-h"},
		"usage: ohs mcp":     {"mcp", "--help"},
		"usage: ohs import":  {"import", "apple-health", "-h"},
		"usage: ohs version": {"version", "-h"},
		"usage: ohs doctor ": {"help", "doctor"},
	}
	for want, args := range cases {
		var buf bytes.Buffer
		if err := run(args, &buf); err != nil {
			t.Fatalf("run %v: %v", args, err)
		}
		if !strings.Contains(buf.String(), want) {
			t.Errorf("run %v: got %q, want %q", args, buf.String(), want)
		}
	}
}

func TestUsageErrorsAreTyped(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"migrate"}, {"admin"}, {"doctor", "x"}, {"serve", "x"}, {"help", "nope"}} {
		err := run(args, &bytes.Buffer{})
		var ue usageError
		if !errors.As(err, &ue) {
			t.Errorf("run %v = %v, want a usageError", args, err)
		}
		if exitCode(err) != 2 {
			t.Errorf("run %v: exit code %d, want 2", args, exitCode(err))
		}
	}
	if exitCode(errors.New("boom")) != 1 {
		t.Fatal("a plain error must exit 1")
	}
	if !strings.Contains(run([]string{"nope"}, &bytes.Buffer{}).Error(), "ohs help") {
		t.Fatal("an unknown command should point to ohs help")
	}
}
