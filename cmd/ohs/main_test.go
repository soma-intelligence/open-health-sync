// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	var buf bytes.Buffer
	if err := run([]string{"version"}, &buf); err != nil {
		t.Fatalf("run version: %v", err)
	}
	if !regexp.MustCompile(`^ohs \S+ \(\S+\)\n$`).MatchString(buf.String()) {
		t.Fatalf("unexpected version output %q", buf.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var buf bytes.Buffer
	if err := run([]string{"nope"}, &buf); err == nil {
		t.Fatal("expected error for unknown command")
	}
	if err := run(nil, &buf); err == nil {
		t.Fatal("expected error for no command")
	}
}

func TestMigrateUsage(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", "postgres://nobody@127.0.0.1:1/none")
	var buf bytes.Buffer
	for _, args := range [][]string{{"migrate"}, {"migrate", "sideways"}, {"migrate", "up", "extra"}} {
		err := run(args, &buf)
		if err == nil || !strings.Contains(err.Error(), "usage: ohs migrate") {
			t.Errorf("run %v = %v, want usage error", args, err)
		}
	}
}

func TestMigrateNeedsDatabaseURL(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", "")
	t.Setenv("OHS_ENCRYPTION_KEY", "")
	var buf bytes.Buffer
	err := run([]string{"migrate", "version"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "OHS_DATABASE_URL") {
		t.Fatalf("got %v, want an error naming OHS_DATABASE_URL", err)
	}
}

// migrate latest reads the binary's own migrations: it needs no database.
func TestMigrateLatestNeedsNoDatabase(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", "")
	t.Setenv("OHS_ENCRYPTION_KEY", "")
	var buf bytes.Buffer
	if err := run([]string{"migrate", "latest"}, &buf); err != nil {
		t.Fatalf("migrate latest: %v", err)
	}
	if !regexp.MustCompile(`^latest schema version [1-9][0-9]*\n$`).MatchString(buf.String()) {
		t.Fatalf("output %q", buf.String())
	}
}

// migrate only needs the database URL: a missing encryption key must not stop it.
func TestMigrateWithoutEncryptionKey(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	t.Setenv("OHS_ENCRYPTION_KEY", "")
	var buf bytes.Buffer
	err := run([]string{"migrate", "version"}, &buf)
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), "OHS_ENCRYPTION_KEY") {
		t.Fatalf("migrate must not require OHS_ENCRYPTION_KEY: %v", err)
	}
}

const unreachableDB = "postgres://nobody@127.0.0.1:1/none?connect_timeout=1"

func TestMigrateDownRequiresAll(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", unreachableDB)
	var buf bytes.Buffer
	err := run([]string{"migrate", "down"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "--all") || !strings.Contains(err.Error(), "drop") {
		t.Fatalf("got %v, want a refusal naming --all and the data loss", err)
	}
}

func TestMigrateDownAllRuns(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", unreachableDB)
	var buf bytes.Buffer
	for _, args := range [][]string{{"migrate", "down", "--all"}, {"migrate", "--all", "down"}} {
		err := run(args, &buf)
		if err == nil {
			t.Fatalf("run %v: expected a connection error", args)
		}
		if strings.Contains(err.Error(), "--all") || strings.Contains(err.Error(), "usage") {
			t.Fatalf("run %v: --all was not honoured: %v", args, err)
		}
	}
}

func TestMigrateReturnsOtherConfigErrors(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", unreachableDB)
	t.Setenv("OHS_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("OHS_ROLES", "bogus")
	var buf bytes.Buffer
	err := run([]string{"migrate", "version"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "OHS_ROLES") {
		t.Fatalf("got %v, want the OHS_ROLES config error", err)
	}
}

func TestAdminUsage(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	var buf bytes.Buffer
	for _, args := range [][]string{
		{"admin"}, {"admin", "tenant"}, {"admin", "tenant", "delete"},
		{"admin", "tenant", "create"}, {"admin", "tenant", "create", "--name", ""},
		{"admin", "tenant", "create", "--name", "a", "extra"},
		{"admin", "key"}, {"admin", "key", "create"}, {"admin", "key", "revoke", "--tenant", "x"},
		{"admin", "tenant", "list", "extra"},
		{"admin", "tenant", "list", "--name", "x"}, {"admin", "tenant", "create", "--name", "a", "--tenant", "x"},
		{"admin", "key", "create", "--tenant", "x", "--key", "y"}, {"admin", "key", "list", "--tenant", "x", "--name", "n"},
		{"admin", "rekey", "extra"}, {"admin", "rekey", "--batch", "0"}, {"admin", "rekey", "--tenant", "x"},
	} {
		err := run(args, &buf)
		if err == nil || !strings.Contains(err.Error(), "usage: ohs admin") {
			t.Errorf("run %v = %v, want usage error", args, err)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("usage errors print nothing: %q", buf.String())
	}
}

// Unlike the other admin commands, rekey needs the encryption keys.
func TestAdminRekeyNeedsKeys(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", unreachableDB)
	t.Setenv("OHS_ENCRYPTION_KEY", "")
	err := run([]string{"admin", "rekey"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "OHS_ENCRYPTION_KEY") {
		t.Fatalf("got %v, want an OHS_ENCRYPTION_KEY error", err)
	}
	t.Setenv("OHS_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("OHS_PREVIOUS_ENCRYPTION_KEYS", "not-a-key")
	err = run([]string{"admin", "rekey"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "OHS_PREVIOUS_ENCRYPTION_KEYS") || strings.Contains(err.Error(), "not-a-key") {
		t.Fatalf("got %v, want an OHS_PREVIOUS_ENCRYPTION_KEYS error that does not echo the value", err)
	}
	// A bad previous key does not stop the commands that need no key.
	err = run([]string{"migrate", "version"}, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), "OHS_PREVIOUS_ENCRYPTION_KEYS") {
		t.Fatalf("got %v, want a connection error", err)
	}
}

func TestServeNeedsConfig(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", "")
	var buf bytes.Buffer
	err := run([]string{"serve"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "OHS_DATABASE_URL") {
		t.Fatalf("got %v, want an error naming OHS_DATABASE_URL", err)
	}
}

// migrate and admin need only the database URL: a bad public URL or log
// level (read by serve) must not stop them.
func TestMigrateIgnoresServeOnlySettings(t *testing.T) {
	for k, v := range map[string]string{"OHS_PUBLIC_URL": "localhost:8080", "OHS_LOG_LEVEL": "loud"} {
		t.Run(k, func(t *testing.T) {
			t.Setenv("OHS_DATABASE_URL", unreachableDB)
			t.Setenv("OHS_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
			t.Setenv(k, v)
			err := run([]string{"migrate", "version"}, &bytes.Buffer{})
			if err == nil || strings.Contains(err.Error(), k) {
				t.Fatalf("got %v, want a connection error, not %s", err, k)
			}
		})
	}
}
