// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package db

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgxpool"
)

var wantTables = map[string][]string{
	"ohs_core": {"tenants", "api_keys", "end_users", "user_tokens", "idempotency_keys", "deletion_jobs",
		"vault_root", "vault_keys"},
	"ohs_sync": {"connections", "oauth_states", "pull_jobs", "pull_coverage", "sync_log",
		"inbound_subscriptions", "raw_objects", "etl_jobs", "consents", "consent_events", "consent_purges",
		"shares", "share_accesses"},
	"ohs_data": {"quantity_samples", "category_samples", "sleep_sessions", "workouts", "route_points",
		"nutrition_logs", "ecg_recordings", "ecg_samples", "heartbeat_samples", "daily_summaries",
		"observations", "sleep_phases", "workout_tracks", "quantity_codes", "device_aliases", "failed_records",
		"deleted_records", "time_tags", "quantity_hourly_sealed"},
}

var wantHypertables = []string{
	"category_samples", "daily_summaries", "ecg_recordings", "ecg_samples", "heartbeat_samples",
	"nutrition_logs", "observations", "quantity_hourly_sealed", "quantity_samples", "route_points",
	"sleep_sessions", "workouts",
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

// schemaDump lists every column, index and constraint of the three ohs schemas,
// one line each, sorted, so two dumps of the same schema compare equal.
func schemaDump(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT 'column ' || table_schema || '.' || table_name || '.' || column_name || ' ' || data_type ||
		       ' null=' || is_nullable || ' default=' || coalesce(column_default, '')
		FROM information_schema.columns WHERE table_schema IN ('ohs_core', 'ohs_sync', 'ohs_data')
		UNION ALL
		SELECT 'index ' || schemaname || '.' || indexname || ' ' || indexdef
		FROM pg_indexes WHERE schemaname IN ('ohs_core', 'ohs_sync', 'ohs_data')
		UNION ALL
		SELECT 'constraint ' || n.nspname || '.' || c.relname || '.' || k.conname || ' ' || pg_get_constraintdef(k.oid)
		FROM pg_constraint k JOIN pg_class c ON c.oid = k.conrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IN ('ohs_core', 'ohs_sync', 'ohs_data')
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

// Migration 0050 adds the vault schema: up, down and up again leaves the schema
// identical, and down restores the one before it.
func TestVaultMigrationUpDownUp(t *testing.T) {
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

	withVault := schemaDump(t, ctx, pool)
	if err := m.Migrate(49); err != nil {
		t.Fatalf("down to 49: %v", err)
	}
	before := schemaDump(t, ctx, pool)
	if strings.Contains(before, "vault_") || strings.Contains(before, "content_mac") {
		t.Fatalf("migration 49 still holds vault objects")
	}
	if err := m.Migrate(50); err != nil {
		t.Fatalf("up to 50: %v", err)
	}
	if got := schemaDump(t, ctx, pool); got != withVault {
		t.Errorf("schema after 50 differs from the first migrate up")
	}
	if err := m.Migrate(49); err != nil {
		t.Fatalf("down to 49 again: %v", err)
	}
	if got := schemaDump(t, ctx, pool); got != before {
		t.Errorf("down from 50 did not restore the schema of 49")
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("up: %v", err)
	}
	if got := schemaDump(t, ctx, pool); got != withVault {
		t.Errorf("schema after up, down, up differs")
	}
}

// Migration 0050 fills worn, tz, derived and synthesized from the stored
// columns and metadata with the expressions the code applies today, leaves
// source_key and sealed empty, and lets a sealed row carry NULL values.
func TestVaultMigrationBackfill(t *testing.T) {
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
	if err := m.Migrate(49); err != nil {
		t.Fatalf("down to 49: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, quantity_type, value, start_date,
			content_hash, source_name, product_type, device_model, metadata)
		VALUES
		 (gen_random_uuid(), gen_random_uuid(), 'p', 'steps', 1, now(), 'watch-by-name', 'Alex''s Apple Watch', NULL, NULL,
		  '{"timezone":"Europe/Paris","derived":true}'),
		 (gen_random_uuid(), gen_random_uuid(), 'p', 'steps', 1, now(), 'watch-by-product', 'Health', 'Watch6,1', NULL,
		  '{"synthesized":true}'),
		 (gen_random_uuid(), gen_random_uuid(), 'p', 'steps', 1, now(), 'phone', 'iPhone', 'iPhone15,2', 'iPhone',
		  '{"timezone":"America/Los_Angeles","derived":false,"synthesized":"no"}'),
		 (gen_random_uuid(), gen_random_uuid(), 'p', 'steps', 1, now(), 'bare', NULL, NULL, NULL, '{}')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ohs_data.daily_summaries
		(tenant_id, user_id, provider, summary_date, content_hash, metadata)
		VALUES (gen_random_uuid(), gen_random_uuid(), 'p', DATE '2026-03-08', 'sum', '{"summary_date":"2026-03-08"}')`); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, quantity_type, value, start_date, content_hash, metadata)
		VALUES (gen_random_uuid(), gen_random_uuid(), 'p', 'steps', 1, now(), 'sd-ok', '{"summary_date":"2026-03-08"}'),
		       (gen_random_uuid(), gen_random_uuid(), 'p', 'steps', 1, now(), 'sd-bad', '{"summary_date":"not a date"}'),
		       (gen_random_uuid(), gen_random_uuid(), 'p', 'steps', 1, now(), 'sd-impossible', '{"summary_date":"2026-13-45"}'),
		       (gen_random_uuid(), gen_random_uuid(), 'p', 'steps', 1, now(), 'sd-word', '{"summary_date":"today"}');
		INSERT INTO ohs_data.category_samples (tenant_id, user_id, provider, category_type, value_int, start_date, content_hash, metadata)
		VALUES (gen_random_uuid(), gen_random_uuid(), 'p', 'menstrual_flow', 1, now(), 'cat-tz', '{"timezone":"Asia/Tokyo"}'),
		       (gen_random_uuid(), gen_random_uuid(), 'p', 'menstrual_flow', 1, now(), 'cat-none', '{}');
		INSERT INTO ohs_data.sleep_sessions (tenant_id, user_id, provider, bedtime_start, content_hash, metadata)
		VALUES (gen_random_uuid(), gen_random_uuid(), 'p', now(), 'sl-watch', '{"sourceName":"Apple Watch","synthesized":true}'),
		       (gen_random_uuid(), gen_random_uuid(), 'p', now(), 'sl-phone', '{"sourceName":"iPhone"}');
		INSERT INTO ohs_data.workouts (tenant_id, user_id, provider, activity_type, start_date, content_hash, metadata)
		VALUES (gen_random_uuid(), gen_random_uuid(), 'p', 'run', now(), 'wo-watch', '{"sourceName":"Apple Watch"}'),
		       (gen_random_uuid(), gen_random_uuid(), 'p', 'run', now(), 'wo-none', '{}');
		INSERT INTO ohs_data.daily_summaries (tenant_id, user_id, provider, summary_date, content_hash, metadata)
		VALUES (gen_random_uuid(), gen_random_uuid(), 'p', DATE '2026-03-09', 'ds-derived', '{"derived":true}'),
		       (gen_random_uuid(), gen_random_uuid(), 'p', DATE '2026-03-10', 'ds-plain', '{}')`); err != nil {
		t.Fatalf("seed promoted tables: %v", err)
	}
	if err := m.Migrate(50); err != nil {
		t.Fatalf("up to 50: %v", err)
	}

	type row struct {
		worn                 *bool
		tz                   *string
		derived, synthesized *bool
		sourceKey, sealed    []byte
	}
	want := map[string]struct {
		worn         bool
		tz           string // empty: NULL
		derived, syn bool
	}{
		"watch-by-name":    {true, "Europe/Paris", true, false},
		"watch-by-product": {true, "", false, true},
		"phone":            {false, "America/Los_Angeles", false, false},
		"bare":             {false, "", false, false},
	}
	for hash, w := range want {
		var r row
		if err := pool.QueryRow(ctx, `SELECT worn, tz, derived, synthesized, source_key, sealed
			FROM ohs_data.quantity_samples WHERE content_hash = $1`, hash).
			Scan(&r.worn, &r.tz, &r.derived, &r.synthesized, &r.sourceKey, &r.sealed); err != nil {
			t.Fatalf("%s: %v", hash, err)
		}
		if r.worn == nil || *r.worn != w.worn {
			t.Errorf("%s: worn = %v, want %v", hash, r.worn, w.worn)
		}
		if (w.tz == "") != (r.tz == nil) || (r.tz != nil && *r.tz != w.tz) {
			t.Errorf("%s: tz = %v, want %q", hash, r.tz, w.tz)
		}
		// A row with no flag is not derived, as in the code that reads it.
		if r.derived == nil || *r.derived != w.derived {
			t.Errorf("%s: derived = %v, want %v", hash, r.derived, w.derived)
		}
		if hash == "watch-by-product" && (r.synthesized == nil || !*r.synthesized) {
			t.Errorf("watch-by-product: synthesized = %v, want true", r.synthesized)
		}
		if r.sourceKey != nil || r.sealed != nil {
			t.Errorf("%s: source_key and sealed must stay NULL", hash)
		}
	}

	str := func(q, hash string) (v *string) {
		if err := pool.QueryRow(ctx, q, hash).Scan(&v); err != nil {
			t.Fatalf("%s %s: %v", q, hash, err)
		}
		return v
	}
	flag := func(q, hash string) (v *bool) {
		if err := pool.QueryRow(ctx, q, hash).Scan(&v); err != nil {
			t.Fatalf("%s %s: %v", q, hash, err)
		}
		return v
	}
	for hash, w := range map[string]string{"sd-ok": "2026-03-08", "sd-bad": "", "sd-impossible": "", "sd-word": ""} {
		got := str(`SELECT summary_date::text FROM ohs_data.quantity_samples WHERE content_hash = $1`, hash)
		if (w == "") != (got == nil) || (got != nil && *got != w) {
			t.Errorf("quantity summary_date %s = %v, want %q", hash, got, w)
		}
	}
	if got := str(`SELECT tz FROM ohs_data.category_samples WHERE content_hash = $1`, "cat-tz"); got == nil || *got != "Asia/Tokyo" {
		t.Errorf("category tz = %v", got)
	}
	if got := str(`SELECT tz FROM ohs_data.category_samples WHERE content_hash = $1`, "cat-none"); got != nil {
		t.Errorf("category tz without key = %v, want NULL", *got)
	}
	for _, c := range []struct {
		table, hash string
		worn        bool
	}{{"sleep_sessions", "sl-watch", true}, {"sleep_sessions", "sl-phone", false},
		{"workouts", "wo-watch", true}, {"workouts", "wo-none", false}} {
		got := flag(`SELECT worn FROM ohs_data.`+c.table+` WHERE content_hash = $1`, c.hash)
		if got == nil || *got != c.worn {
			t.Errorf("%s %s: worn = %v, want %v", c.table, c.hash, got, c.worn)
		}
	}
	if got := flag(`SELECT synthesized FROM ohs_data.sleep_sessions WHERE content_hash = $1`, "sl-watch"); got == nil || !*got {
		t.Errorf("sleep synthesized = %v, want true", got)
	}
	if got := flag(`SELECT synthesized FROM ohs_data.sleep_sessions WHERE content_hash = $1`, "sl-phone"); got == nil || *got {
		t.Errorf("sleep synthesized without key = %v, want false", got)
	}
	for _, hash := range []string{"sl-watch", "sl-phone"} {
		var k []byte
		if err := pool.QueryRow(ctx, `SELECT source_key FROM ohs_data.sleep_sessions WHERE content_hash = $1`, hash).Scan(&k); err != nil || k != nil {
			t.Errorf("sleep source_key %s = %v err=%v, want NULL", hash, k, err)
		}
	}
	for hash, w := range map[string]bool{"ds-derived": true, "ds-plain": false} {
		if got := flag(`SELECT derived FROM ohs_data.daily_summaries WHERE content_hash = $1`, hash); got == nil || *got != w {
			t.Errorf("summary derived %s = %v, want %v", hash, got, w)
		}
	}

	// A summary's date column is the one the metadata names; nothing to promote.
	var d string
	if err := pool.QueryRow(ctx, `SELECT summary_date::text FROM ohs_data.daily_summaries
		WHERE content_hash = 'sum' AND summary_date = (metadata->>'summary_date')::date`).Scan(&d); err != nil || d != "2026-03-08" {
		t.Errorf("summary_date = %q err=%v", d, err)
	}

	// A sealed row holds NULL where the value was.
	if _, err := pool.Exec(ctx, `UPDATE ohs_data.quantity_samples SET value = NULL, metadata = NULL,
		sealed = '\x02'::bytea WHERE content_hash = 'phone'`); err != nil {
		t.Errorf("sealed quantity row refused: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ohs_data.daily_summaries SET metadata = NULL, sealed = '\x02'::bytea`); err != nil {
		t.Errorf("sealed summary row refused: %v", err)
	}
}

// vault_keys: a user key needs kem_public, a tenant key must not carry one,
// versions are unique per owner, and removing the user removes its keys.
func TestVaultKeysConstraints(t *testing.T) {
	ctx := context.Background()
	url := TestDB(t)
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var tenant, user string
	if err := pool.QueryRow(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ('t') RETURNING id::text`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO ohs_core.end_users (tenant_id) VALUES ($1) RETURNING id::text`, tenant).Scan(&user); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) error { _, err := pool.Exec(ctx, q, args...); return err }
	if err := exec(`INSERT INTO ohs_core.vault_root (version, provider) VALUES ('r1', 'local')`); err != nil {
		t.Fatalf("root: %v", err)
	}
	ins := `INSERT INTO ohs_core.vault_keys (tenant_id, user_id, version, wrapped, parent_version, kem_public)
		VALUES ($1, $2, $3, '\x01', '1', $4)`
	if err := exec(ins, tenant, nil, 1, nil); err != nil {
		t.Fatalf("tenant key: %v", err)
	}
	if err := exec(ins, tenant, nil, 1, nil); err == nil {
		t.Error("two tenant keys of one version accepted")
	}
	if err := exec(ins, tenant, nil, 2, []byte{1}); err == nil {
		t.Error("tenant key with kem_public accepted")
	}
	if err := exec(ins, tenant, user, 1, nil); err == nil {
		t.Error("user key without kem_public accepted")
	}
	if err := exec(ins, tenant, user, 1, []byte{1}); err != nil {
		t.Fatalf("user key: %v", err)
	}
	if err := exec(ins, tenant, user, 1, []byte{1}); err == nil {
		t.Error("two user keys of one version accepted")
	}
	if err := exec(ins, tenant, "00000000-0000-0000-0000-000000000001", 1, []byte{1}); err == nil {
		t.Error("key of an unknown user accepted")
	}
	if err := exec(`DELETE FROM ohs_core.end_users WHERE id = $1`, user); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.vault_keys WHERE user_id IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Errorf("user keys left after the user is gone: %d err=%v", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.vault_keys WHERE user_id IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Errorf("tenant key count = %d err=%v, want 1", n, err)
	}
}
