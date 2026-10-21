// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package db

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgxpool"
)

var wantTables = map[string][]string{
	"ohs_core": {"tenants", "api_keys", "end_users", "user_tokens", "idempotency_keys", "deletion_jobs"},
	"ohs_sync": {"connections", "oauth_states", "pull_jobs", "pull_coverage", "sync_log",
		"inbound_subscriptions", "raw_objects", "etl_jobs", "consents", "consent_events", "consent_purges",
		"shares", "share_accesses"},
	"ohs_data": {"quantity_samples", "category_samples", "sleep_sessions", "workouts", "route_points",
		"nutrition_logs", "ecg_recordings", "ecg_samples", "heartbeat_samples", "daily_summaries",
		"observations", "sleep_phases", "workout_tracks", "quantity_codes", "device_aliases", "failed_records",
		"deleted_records", "time_tags"},
}

var wantHypertables = []string{
	"category_samples", "daily_summaries", "ecg_recordings", "ecg_samples", "heartbeat_samples",
	"nutrition_logs", "observations", "quantity_samples", "route_points", "sleep_sessions", "workouts",
}

func TestBaselineUpDown(t *testing.T) {
	ctx := context.Background()
	url := TestDB(t) // migrated up

	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	assertBaseline(t, ctx, pool)

	// Data in hypertables creates chunks; down must remove them too.
	if _, err := pool.Exec(ctx, `INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, quantity_type, value, start_date, content_hash)
		VALUES (gen_random_uuid(), gen_random_uuid(), 'test', 'heart_rate', 60, now(), 'h1')`); err != nil {
		t.Fatalf("insert quantity sample: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ohs_data.daily_summaries
		(tenant_id, user_id, provider, summary_date, content_hash)
		VALUES (gen_random_uuid(), gen_random_uuid(), 'test', DATE '2026-03-08', 'd1')`); err != nil {
		t.Fatalf("insert daily summary: %v", err)
	}
	var chunks int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'ohs_data'`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks < 2 {
		t.Fatalf("expected chunks after inserts, got %d", chunks)
	}

	if err := Migrate(url, "down"); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM timescaledb_information.chunks`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks != 0 {
		t.Fatalf("after down: %d chunks remain", chunks)
	}
	var leftover int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = '_timescaledb_internal' AND c.relkind = 'r' AND c.relname LIKE '\_hyper\_%'`).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatalf("after down: %d chunk tables remain in _timescaledb_internal", leftover)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM _timescaledb_catalog.chunk`).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatalf("after down: %d rows remain in the chunk catalog", leftover)
	}
	var schemas int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.schemata
		WHERE schema_name IN ('ohs_core','ohs_sync','ohs_data')`).Scan(&schemas); err != nil {
		t.Fatal(err)
	}
	if schemas != 0 {
		t.Fatalf("after down: %d ohs schemas remain", schemas)
	}

	if err := Migrate(url, "up"); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	assertBaseline(t, ctx, pool)

	v, dirty, err := Version(url)
	if err != nil || dirty || v < 2 {
		t.Fatalf("version = %d dirty=%v err=%v, want >= 2 clean", v, dirty, err)
	}
}

func assertBaseline(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for schema, tables := range wantTables {
		for _, table := range tables {
			var n int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
				WHERE table_schema = $1 AND table_name = $2`, schema, table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Errorf("table %s.%s missing", schema, table)
			}
		}
	}

	rows, err := pool.Query(ctx, `SELECT hypertable_name FROM timescaledb_information.hypertables
		WHERE hypertable_schema = 'ohs_data'`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if len(got) != len(wantHypertables) {
		t.Errorf("hypertables = %v, want %v", got, wantHypertables)
	} else {
		for i := range got {
			if got[i] != wantHypertables[i] {
				t.Errorf("hypertables = %v, want %v", got, wantHypertables)
				break
			}
		}
	}

	old := regexp.MustCompile(`^(th|ah)_`)
	trows, err := pool.Query(ctx, `SELECT table_schema, table_name FROM information_schema.tables`)
	if err != nil {
		t.Fatal(err)
	}
	for trows.Next() {
		var s, n string
		if err := trows.Scan(&s, &n); err != nil {
			t.Fatal(err)
		}
		if old.MatchString(n) {
			t.Errorf("old prefix table %s.%s", s, n)
		}
	}
	if err := trows.Err(); err != nil {
		t.Fatal(err)
	}

	// Every row of ohs_sync and ohs_data carries tenant_id and user_id, NOT NULL,
	// except the two reference tables of ohs_data.
	reference := map[string]bool{"quantity_codes": true, "device_aliases": true}
	for _, schema := range []string{"ohs_sync", "ohs_data"} {
		for _, table := range wantTables[schema] {
			if reference[table] {
				continue
			}
			for _, col := range []string{"tenant_id", "user_id"} {
				var typ, nullable string
				err := pool.QueryRow(ctx, `SELECT data_type, is_nullable FROM information_schema.columns
					WHERE table_schema=$1 AND table_name=$2 AND column_name=$3`, schema, table, col).Scan(&typ, &nullable)
				if err != nil {
					t.Errorf("%s.%s.%s: %v", schema, table, col, err)
					continue
				}
				if typ != "uuid" || nullable != "NO" {
					t.Errorf("%s.%s.%s is %s nullable=%s, want uuid NOT NULL", schema, table, col, typ, nullable)
				}
			}
		}
	}

	var summaryType string
	if err := pool.QueryRow(ctx, `SELECT data_type FROM information_schema.columns
		WHERE table_schema='ohs_data' AND table_name='daily_summaries' AND column_name='summary_date'`).Scan(&summaryType); err != nil {
		t.Fatal(err)
	}
	if summaryType != "date" {
		t.Errorf("daily_summaries.summary_date is %s, want date", summaryType)
	}

	var codes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ohs_data.quantity_codes`).Scan(&codes); err != nil {
		t.Fatal(err)
	}
	if codes == 0 {
		t.Error("quantity_codes not seeded")
	}
}

// Migration 0006 turns the active flag into a status: a row that was live at
// the provider stays subscribed (not pending, which would subscribe it
// again), an inactive one is removed. Its down migration maps back.
func TestSubscriptionStateMigrationKeepsLiveRows(t *testing.T) {
	ctx := context.Background()
	url := TestDB(t)
	m, err := newMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Migrate(5); err != nil {
		t.Fatalf("down to 5: %v", err)
	}
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `
		INSERT INTO ohs_sync.inbound_subscriptions (tenant_id, user_id, provider, topic, callback_url, active)
		VALUES (gen_random_uuid(), gen_random_uuid(), 'withings', 'all', 'https://x/live', true),
		       (gen_random_uuid(), gen_random_uuid(), 'withings', 'all', 'https://x/off', false)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := m.Migrate(6); err != nil {
		t.Fatalf("up to 6: %v", err)
	}
	status := func(cb string) string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT status FROM ohs_sync.inbound_subscriptions WHERE callback_url = $1`, cb).Scan(&s); err != nil {
			t.Fatalf("status %s: %v", cb, err)
		}
		return s
	}
	if got := status("https://x/live"); got != "subscribed" {
		t.Errorf("live row = %q, want subscribed", got)
	}
	if got := status("https://x/off"); got != "removed" {
		t.Errorf("inactive row = %q, want removed", got)
	}
	if err := m.Migrate(5); err != nil {
		t.Fatalf("down to 5 again: %v", err)
	}
	var active bool
	if err := pool.QueryRow(ctx, `SELECT active FROM ohs_sync.inbound_subscriptions WHERE callback_url = 'https://x/live'`).Scan(&active); err != nil || !active {
		t.Errorf("live row after down: active=%v err=%v", active, err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}
}

// Migration 0038 makes the only tenant of a single-tenant instance its admin,
// so the console keeps working on upgrade, and names no admin when the
// instance has several tenants.
func TestAdminMigrationPromotesOnlyTenant(t *testing.T) {
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
	admins := func() (n int) {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.tenants WHERE admin`).Scan(&n); err != nil {
			t.Fatalf("count admins: %v", err)
		}
		return n
	}

	if err := m.Migrate(37); err != nil {
		t.Fatalf("down to 37: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ('only')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := m.Migrate(38); err != nil {
		t.Fatalf("up to 38: %v", err)
	}
	if got := admins(); got != 1 {
		t.Errorf("one tenant: %d admins, want 1", got)
	}

	if err := m.Migrate(37); err != nil {
		t.Fatalf("down to 37 again: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ('second')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := m.Migrate(38); err != nil {
		t.Fatalf("up to 38 again: %v", err)
	}
	if got := admins(); got != 0 {
		t.Errorf("two tenants: %d admins, want 0", got)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("up: %v", err)
	}
}

// The pool size setting the docs recommend (pool_max_conns in the URL) is a
// pgxpool parameter: the migrator must not send it to the server.
func TestMigrateAcceptsPoolParams(t *testing.T) {
	url := withParam(TestDB(t), "pool_max_conns=20")
	if err := Migrate(url, "up"); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if _, _, err := Version(url); err != nil {
		t.Fatalf("version: %v", err)
	}
	pool, err := Open(context.Background(), url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()
	if got := pool.Config().MaxConns; got != 20 {
		t.Fatalf("pool MaxConns = %d, want 20", got)
	}
}

func withParam(url, kv string) string {
	if regexp.MustCompile(`\?`).MatchString(url) {
		return url + "&" + kv
	}
	return url + "?" + kv
}
