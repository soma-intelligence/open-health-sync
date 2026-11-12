// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
)

// storageLayoutVersion is the migration that widens the chunks of the sparse
// hypertables.
const storageLayoutVersion = 49

var uncompressedByLayout = []string{"heartbeat_samples", "ecg_samples", "route_points"}

var widenedByLayout = []string{"sleep_sessions", "workouts", "nutrition_logs", "ecg_recordings", "daily_summaries"}

func compressionPolicies(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT hypertable_name::text FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_compression' AND hypertable_schema = 'ohs_data'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// chunkDays is the chunk interval of a hypertable for new chunks, in days.
func chunkDays(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var days int
	if err := pool.QueryRow(ctx, `SELECT (extract(epoch FROM time_interval) / 86400)::int
		FROM timescaledb_information.dimensions
		WHERE hypertable_schema = 'ohs_data' AND hypertable_name = $1`, table).Scan(&days); err != nil {
		t.Fatalf("chunk interval of %s: %v", table, err)
	}
	return days
}

// The storage layout migration: chunk intervals in place, compression
// untouched, and the down migration undoes it.
func TestStorageLayoutMigration(t *testing.T) {
	ctx := context.Background()
	url := TestDB(t)
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	policies := compressionPolicies(t, ctx, pool)
	if !policies["quantity_samples"] {
		t.Errorf("quantity_samples has no compression policy")
	}
	// These keep a dedup unique the writer relies on, which a compressed
	// chunk cannot check past the decompression limit: not compressed.
	for _, table := range uncompressedByLayout {
		if policies[table] {
			t.Errorf("%s has a compression policy", table)
		}
	}
	for _, table := range widenedByLayout {
		if got := chunkDays(t, ctx, pool, table); got != 30 {
			t.Errorf("%s: chunk interval %d days, want 30", table, got)
		}
	}
	for _, table := range []string{"quantity_samples", "heartbeat_samples", "ecg_samples", "route_points"} {
		if got := chunkDays(t, ctx, pool, table); got != 7 {
			t.Errorf("%s: chunk interval %d days, want the 7 day default", table, got)
		}
	}

	// Down to the migration before: the 7 day default back.
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	prev, err := src.Prev(storageLayoutVersion)
	_ = src.Close()
	if err != nil {
		t.Fatal(err)
	}
	m, err := newMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Migrate(prev); err != nil {
		t.Fatalf("down to %d: %v", prev, err)
	}
	if !compressionPolicies(t, ctx, pool)["quantity_samples"] {
		t.Error("after down: the quantity_samples policy of migration 0015 is gone")
	}
	for _, table := range widenedByLayout {
		if got := chunkDays(t, ctx, pool, table); got != 7 {
			t.Errorf("after down: %s chunk interval %d days, want 7", table, got)
		}
	}
	// And up again.
	if err := m.Up(); err != nil {
		t.Fatalf("up again: %v", err)
	}
	for _, table := range widenedByLayout {
		if got := chunkDays(t, ctx, pool, table); got != 30 {
			t.Errorf("after up again: %s chunk interval %d days, want 30", table, got)
		}
	}
}

// A database behind, ahead of or dirty against the binary is told apart by
// CheckSchema; Force clears the dirty mark.
func TestCheckSchemaAndForce(t *testing.T) {
	ctx := context.Background()
	url := TestDB(t)
	if err := CheckSchema(ctx, url); err != nil {
		t.Fatalf("migrated database: %v", err)
	}
	latest, err := LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	set := func(version uint, dirty bool) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET version = $1, dirty = $2`, int64(version), dirty); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name    string
		version uint
		dirty   bool
		want    error
	}{
		{"ahead", latest + 3, false, ErrSchemaAhead},
		{"behind", latest - 1, false, ErrSchemaBehind},
		{"dirty", latest, true, ErrSchemaDirty},
	} {
		set(tc.version, tc.dirty)
		if err := CheckSchema(ctx, url); !errors.Is(err, tc.want) {
			t.Errorf("%s: CheckSchema = %v, want %v", tc.name, err, tc.want)
		}
	}
	// Still dirty at latest: up refuses, force clears, up is then a no-op.
	if err := Migrate(url, "up"); err == nil {
		t.Fatal("migrate up on a dirty schema must fail")
	}
	versions, err := embeddedVersions()
	if err != nil {
		t.Fatal(err)
	}
	before := versions[len(versions)-2]
	// Only the dirty version and its neighbours may be recorded.
	for _, v := range []uint{0, versions[0], versions[len(versions)-3]} {
		if err := Force(url, v); err == nil || !strings.Contains(err.Error(), "dirty at version") {
			t.Fatalf("force %d on a schema dirty at %d = %v, want a refusal", v, latest, err)
		}
	}
	if st, err := ReadSchemaState(ctx, url); err != nil || st.Database != latest || !st.Dirty {
		t.Fatalf("after refused forces: %+v err %v", st, err)
	}
	if err := Force(url, latest); err != nil {
		t.Fatalf("force: %v", err)
	}
	if v, dirty, err := Version(url); err != nil || v != latest || dirty {
		t.Fatalf("after force: version %d dirty %v err %v", v, dirty, err)
	}
	if err := Migrate(url, "up"); err != nil {
		t.Fatalf("migrate up after force: %v", err)
	}
	if err := CheckSchema(ctx, url); err != nil {
		t.Fatalf("after force: %v", err)
	}
	// A clean schema is not forced, to any version: its own, a lower one
	// (the next up would run migrations again) or none.
	for _, v := range []uint{latest, before, 0} {
		if err := Force(url, v); err == nil || !strings.Contains(err.Error(), "not dirty") {
			t.Fatalf("force %d on a clean schema = %v, want a refusal", v, err)
		}
	}
	if err := CheckSchema(ctx, url); err != nil {
		t.Fatalf("after refused forces: %v", err)
	}
	// A failed up to latest, undone by hand: the version before it.
	set(latest, true)
	if err := Force(url, before); err != nil {
		t.Fatalf("force to the version before the dirty one: %v", err)
	}
	if st, err := ReadSchemaState(ctx, url); err != nil || st.Database != before || st.Dirty {
		t.Fatalf("after force: %+v err %v", st, err)
	}
	// A failed down of latest leaves the version before it dirty: undone by
	// hand, the schema is back at latest.
	set(before, true)
	if err := Force(url, latest); err != nil {
		t.Fatalf("force to the version after the dirty one: %v", err)
	}
	// A failed down of the first migration: no version, dirty.
	if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET version = -1, dirty = true`); err != nil {
		t.Fatal(err)
	}
	if v, dirty, err := Version(url); err != nil || v != 0 || !dirty {
		t.Fatalf("no version, dirty: Version = %d, %v, %v", v, dirty, err)
	}
	if err := Force(url, versions[1]); err == nil {
		t.Fatal("force two migrations above a dirty empty schema must fail")
	}
	if err := Force(url, 0); err != nil {
		t.Fatalf("force 0: %v", err)
	}
	st, err := ReadSchemaState(ctx, url)
	if err != nil || st.Database != 0 || st.Dirty {
		t.Fatalf("after force 0: %+v err %v", st, err)
	}
}
