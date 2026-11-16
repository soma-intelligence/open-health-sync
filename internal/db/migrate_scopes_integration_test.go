// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package db

import (
	"context"
	"testing"
)

// Migration 0051 gives API keys scopes. A key that exists keeps full access
// ('{*}'), and down then up again leaves it so.
func TestKeyScopesMigrationKeepsFullAccess(t *testing.T) {
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

	if err := m.Migrate(50); err != nil {
		t.Fatalf("down to 50: %v", err)
	}
	var hasScopes bool
	const column = `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'ohs_core' AND table_name = 'api_keys' AND column_name = 'scopes')`
	if err := pool.QueryRow(ctx, column).Scan(&hasScopes); err != nil || hasScopes {
		t.Fatalf("after down: scopes column present = %v, %v", hasScopes, err)
	}
	if _, err := pool.Exec(ctx, `WITH t AS (INSERT INTO ohs_core.tenants (name) VALUES ('Acme') RETURNING id)
		INSERT INTO ohs_core.api_keys (tenant_id, key_prefix, key_hash) SELECT id, 'ohs_sk_old00', 'hash-of-an-old-key' FROM t`); err != nil {
		t.Fatalf("a key of before the migration: %v", err)
	}
	if err := m.Migrate(51); err != nil {
		t.Fatalf("up to 51: %v", err)
	}
	var scopes []string
	if err := pool.QueryRow(ctx, `SELECT scopes FROM ohs_core.api_keys WHERE key_hash = 'hash-of-an-old-key'`).Scan(&scopes); err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 1 || scopes[0] != "*" {
		t.Fatalf("an existing key holds %v, want [*]", scopes)
	}
	if _, err := pool.Exec(ctx, `UPDATE ohs_core.api_keys SET scopes = '{}'`); err == nil {
		t.Fatal("a key with no scope at all must be refused")
	}
}
