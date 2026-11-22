// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package db

import (
	"context"
	"strings"
	"testing"
)

// Migration 0059 gives API keys a nullable name. A key that exists keeps
// working without one, a name is 1 to 100 characters, and down then up again
// leaves the keys in place.
func TestKeyNameMigration(t *testing.T) {
	ctx := context.Background()
	url := TestDB(t)
	m, err := newMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := m.Migrate(58); err != nil {
		t.Fatalf("down to 58: %v", err)
	}
	const column = `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'ohs_core' AND table_name = 'api_keys' AND column_name = 'name')`
	var hasName bool
	if err := pool.QueryRow(ctx, column).Scan(&hasName); err != nil || hasName {
		t.Fatalf("after down: name column present = %v, %v", hasName, err)
	}
	if _, err := pool.Exec(ctx, `WITH t AS (INSERT INTO ohs_core.tenants (name) VALUES ('Acme') RETURNING id)
		INSERT INTO ohs_core.api_keys (tenant_id, key_prefix, key_hash) SELECT id, 'ohs_sk_old00', 'hash-of-an-old-key' FROM t`); err != nil {
		t.Fatalf("a key of before the migration: %v", err)
	}
	if err := m.Migrate(59); err != nil {
		t.Fatalf("up to 59: %v", err)
	}
	var name *string
	if err := pool.QueryRow(ctx, `SELECT name FROM ohs_core.api_keys WHERE key_hash = 'hash-of-an-old-key'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != nil {
		t.Fatalf("an existing key is named %q, want no name", *name)
	}
	if _, err := pool.Exec(ctx, `UPDATE ohs_core.api_keys SET name = 'Production backend'`); err != nil {
		t.Fatalf("a name of 18 characters: %v", err)
	}
	for _, bad := range []string{"", strings.Repeat("k", 101)} {
		if _, err := pool.Exec(ctx, `UPDATE ohs_core.api_keys SET name = $1`, bad); err == nil {
			t.Fatalf("a name of %d characters must be refused", len(bad))
		}
	}
	if err := m.Migrate(58); err != nil {
		t.Fatalf("down to 58 again: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.api_keys`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("after down: %d keys, %v; want the key kept", n, err)
	}
}
