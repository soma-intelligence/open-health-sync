// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/soma-intelligence/open-health-sync/internal/db"
)

func TestMigrateCLIDownAll(t *testing.T) {
	url := db.TestDB(t)
	t.Setenv("OHS_DATABASE_URL", url)
	t.Setenv("OHS_ENCRYPTION_KEY", "")

	var buf bytes.Buffer
	if err := run([]string{"migrate", "down"}, &buf); err == nil {
		t.Fatal("down without --all must refuse")
	}
	if v, _, err := db.Version(url); err != nil || v == 0 {
		t.Fatalf("refused down still changed the schema: version %d err %v", v, err)
	}
	if err := run([]string{"migrate", "down", "--all"}, &buf); err != nil {
		t.Fatalf("down --all: %v", err)
	}
	buf.Reset()
	if err := run([]string{"migrate", "version"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "schema version 0") {
		t.Fatalf("after down --all: %q", buf.String())
	}
}

// A migration killed half way leaves the schema dirty: serve and migrate up
// refuse, migrate force clears the mark.
func TestMigrateCLIForce(t *testing.T) {
	url := db.TestDB(t)
	t.Setenv("OHS_DATABASE_URL", url)
	t.Setenv("OHS_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	latest, err := db.LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(context.Background(), `UPDATE schema_migrations SET dirty = true`); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := run([]string{"migrate", "version"}, &buf); err != nil || !strings.Contains(buf.String(), "(dirty)") {
		t.Fatalf("migrate version: %q, %v", buf.String(), err)
	}
	if err := run([]string{"migrate", "up"}, &buf); err == nil {
		t.Fatal("migrate up on a dirty schema must fail")
	}
	if err := run([]string{"serve"}, &buf); !errors.Is(err, db.ErrSchemaDirty) {
		t.Fatalf("serve on a dirty schema = %v, want the dirty refusal", err)
	}
	if err := run([]string{"migrate", "force", fmt.Sprint(latest + 1000), "--yes"}, &buf); err == nil {
		t.Fatal("force to a version the binary does not have must fail")
	}
	if err := run([]string{"migrate", "force", "1", "--yes"}, &buf); err == nil || !strings.Contains(err.Error(), "dirty at version") {
		t.Fatalf("force far below the dirty version = %v, want a refusal", err)
	}
	buf.Reset()
	if err := run([]string{"migrate", "force", fmt.Sprint(latest), "--yes"}, &buf); err != nil {
		t.Fatalf("migrate force: %v", err)
	}
	if !strings.Contains(buf.String(), fmt.Sprintf("schema version set to %d, clean (was %d, dirty)", latest, latest)) {
		t.Fatalf("force output %q", buf.String())
	}
	if v, dirty, err := db.Version(url); err != nil || v != latest || dirty {
		t.Fatalf("after force: version %d dirty %v err %v", v, dirty, err)
	}
	if err := run([]string{"migrate", "up"}, &buf); err != nil {
		t.Fatalf("migrate up after force: %v", err)
	}
	// A clean schema is left alone, whatever the version asked for.
	for _, v := range []uint{latest, 1, 0} {
		err := run([]string{"migrate", "force", fmt.Sprint(v), "--yes"}, &buf)
		if err == nil || !strings.Contains(err.Error(), "not dirty") {
			t.Fatalf("migrate force %d on a clean schema = %v, want a refusal", v, err)
		}
	}
	if v, dirty, err := db.Version(url); err != nil || v != latest || dirty {
		t.Fatalf("after refused forces: version %d dirty %v err %v", v, dirty, err)
	}
}
