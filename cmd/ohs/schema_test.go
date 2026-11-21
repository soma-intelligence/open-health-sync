// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/soma-intelligence/open-health-sync/internal/db"
)

// serve refuses a database that is not at the binary's schema version, before
// any role starts, and says which command to run.
func TestServeRefusesSchemaMismatch(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", unreachableDB)
	t.Setenv("OHS_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	old := checkSchema
	t.Cleanup(func() { checkSchema = old })

	for _, tc := range []struct {
		name  string
		args  []string
		state db.SchemaState
		is    error
		want  []string
	}{
		{"behind", []string{"serve"}, db.SchemaState{Database: 44, Binary: 49}, db.ErrSchemaBehind,
			[]string{"version 44", "version 49", "`ohs migrate up`"}},
		{"ahead", []string{"serve"}, db.SchemaState{Database: 52, Binary: 49}, db.ErrSchemaAhead,
			[]string{"version 52", "version 49", "ohs migrate"}},
		{"dirty", []string{"serve"}, db.SchemaState{Database: 49, Binary: 49, Dirty: true}, db.ErrSchemaDirty,
			[]string{"ohs migrate force"}},
		// The flag lets a newer schema through, nothing else.
		{"behind with the flag", []string{"serve", "--allow-newer-schema"}, db.SchemaState{Database: 44, Binary: 49},
			db.ErrSchemaBehind, []string{"`ohs migrate up`"}},
		{"dirty with the flag", []string{"serve", "--allow-newer-schema"}, db.SchemaState{Database: 52, Binary: 49, Dirty: true},
			db.ErrSchemaDirty, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkSchema = func(context.Context, string) error { return tc.state.Err() }
			err := run(tc.args, &bytes.Buffer{})
			if !errors.Is(err, tc.is) {
				t.Fatalf("run %v = %v, want %v", tc.args, err, tc.is)
			}
			if exitCode(err) != 1 {
				t.Errorf("exit code %d, want 1", exitCode(err))
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("message %q lacks %q", err, w)
				}
			}
		})
	}
}

// A database that cannot be read fails serve too: it never starts unchecked.
func TestServeSchemaCheckError(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", unreachableDB)
	t.Setenv("OHS_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	err := run([]string{"serve", "--allow-newer-schema"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("got %v, want the schema check to fail on an unreachable database", err)
	}
}

func TestMigrateForceUsage(t *testing.T) {
	t.Setenv("OHS_DATABASE_URL", unreachableDB)
	t.Setenv("OHS_ENCRYPTION_KEY", "")
	var buf bytes.Buffer
	for _, args := range [][]string{
		{"migrate", "force"}, {"migrate", "force", "--yes"}, {"migrate", "force", "x", "--yes"},
		{"migrate", "force", "-1", "--yes"}, {"migrate", "force", "44", "45", "--yes"},
	} {
		err := run(args, &buf)
		if err == nil || exitCode(err) != 2 || !strings.Contains(err.Error(), "ohs migrate") {
			t.Errorf("run %v = %v, want a usage error", args, err)
		}
	}
	// Without --yes it refuses before it reaches the database, wherever the
	// flag would go.
	err := run([]string{"migrate", "force", "44"}, &buf)
	if err == nil || exitCode(err) != 2 || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("force without --yes = %v, want a refusal naming --yes", err)
	}
	// With it, in either position, it goes on to the database.
	for _, args := range [][]string{{"migrate", "force", "44", "--yes"}, {"migrate", "--yes", "force", "44"}} {
		err := run(args, &buf)
		if err == nil || exitCode(err) != 1 {
			t.Errorf("run %v = %v, want a connection error", args, err)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("failed commands print nothing: %q", buf.String())
	}
}
