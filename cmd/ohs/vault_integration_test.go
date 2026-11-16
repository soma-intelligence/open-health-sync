// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// vaultCLI is an instance as a release without the vault left it, upgraded:
// a user without keys, clear rows and a raw object in plain gzip.
type vaultCLI struct {
	t            *testing.T
	pool         *pgxpool.Pool
	tenant, user uuid.UUID
	rawKey       string
	raw          rawstore.Store
}

func newVaultCLI(t *testing.T, encKey []byte) *vaultCLI {
	t.Helper()
	ctx := context.Background()
	url := db.TestDB(t)
	rawURL := "file://" + t.TempDir()
	t.Setenv("OHS_DATABASE_URL", url)
	t.Setenv("OHS_RAW_STORE", rawURL)
	t.Setenv("OHS_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(encKey))
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	c := &vaultCLI{t: t, pool: pool}
	if c.raw, err = rawstore.Open(rawURL); err != nil {
		t.Fatal(err)
	}
	c.scan(&c.tenant, `INSERT INTO ohs_core.tenants (name) VALUES ('t') RETURNING id`)
	c.scan(&c.user, `INSERT INTO ohs_core.end_users (tenant_id, external_ref) VALUES ($1, 'u') RETURNING id`, c.tenant)
	at := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	for i := range 5 {
		c.exec(`INSERT INTO ohs_data.quantity_samples
			(tenant_id, user_id, provider, quantity_type, value, unit, start_date, end_date, source_name, content_hash, metadata)
			VALUES ($1, $2, 'dev', 'heart_rate', $3, 'bpm', $4, $4, 'Canary Watch', $5, '{}')`,
			c.tenant, c.user, 60+float64(i), at.Add(time.Duration(i)*time.Minute), "plain-"+string(rune('a'+i)))
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(`{"canary":"plain raw line"}` + "\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	c.rawKey = rawstore.Key(c.tenant, c.user, "dev", at, "b1", 0)
	if err := c.raw.Put(ctx, c.rawKey, bytes.NewReader(gz.Bytes())); err != nil {
		t.Fatal(err)
	}
	c.exec(`INSERT INTO ohs_sync.raw_objects (tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, data_type, created_at)
		VALUES ($1, $2, 'dev', $3, 'legacy-hash', $4, 'b1', 0, 'q', now() - INTERVAL '30 days')`, c.tenant, c.user, c.rawKey, gz.Len())
	return c
}

func (c *vaultCLI) exec(sql string, args ...any) {
	c.t.Helper()
	if _, err := c.pool.Exec(context.Background(), sql, args...); err != nil {
		c.t.Fatal(err)
	}
}

func (c *vaultCLI) scan(dest any, sql string, args ...any) {
	c.t.Helper()
	if err := c.pool.QueryRow(context.Background(), sql, args...).Scan(dest); err != nil {
		c.t.Fatal(err)
	}
}

// ohs runs the command and returns what it printed.
func (c *vaultCLI) ohs(args ...string) (string, error) {
	var buf bytes.Buffer
	err := run(args, &buf)
	return buf.String(), err
}

func (c *vaultCLI) mustOHS(args ...string) string {
	c.t.Helper()
	out, err := c.ohs(args...)
	if err != nil {
		c.t.Fatalf("ohs %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func wants(t *testing.T, out string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(out, p) {
			t.Fatalf("output lacks %q:\n%s", p, out)
		}
	}
}

// status, seal, rotate and reseal from the command line, on an instance that
// has only OHS_ENCRYPTION_KEY: the root key is derived from it.
func TestVaultCommands(t *testing.T) {
	c := newVaultCLI(t, bytes.Repeat([]byte{4}, 32))
	ctx := context.Background()

	out, err := c.ohs("vault", "status", "--check")
	if err == nil {
		t.Fatalf("status --check passes with clear rows:\n%s", out)
	}
	wants(t, out, "5 rows and 1 raw objects are still stored clear", "quantity_samples", "ohs vault seal")
	wants(t, c.mustOHS("vault", "status"), "5 rows and 1 raw objects")
	wants(t, c.mustOHS("vault", "seal", "--dry-run"), "5 rows and 1 raw objects to seal")
	wants(t, c.mustOHS("doctor"), "ok    vault key provider (local)", "warn  vault: 5 rows and 1 raw objects are still stored clear")

	out = c.mustOHS("vault", "seal", "--batch", "2")
	wants(t, out, "seal: 1 users, 5 rows, 1 raw objects", "quantity_samples", "vault: nothing is stored clear")
	if strings.Contains(out, "Canary") || strings.Contains(out, "plain-a") {
		t.Fatalf("output names a value:\n%s", out)
	}
	wants(t, c.mustOHS("vault", "status", "--check"), "nothing is stored clear")
	wants(t, c.mustOHS("vault", "seal"), "seal: 0 users, 0 rows, 0 raw objects")
	var clearValues, buckets int
	c.scan(&clearValues, `SELECT count(*) FROM ohs_data.quantity_samples WHERE value IS NOT NULL OR source_name IS NOT NULL OR sealed IS NULL`)
	c.scan(&buckets, `SELECT count(*) FROM ohs_data.quantity_hourly_sealed WHERE user_id = $1`, c.user)
	if clearValues != 0 || buckets != 1 {
		t.Fatalf("%d clear values, %d hourly buckets", clearValues, buckets)
	}
	if _, err := c.raw.Get(ctx, c.rawKey); err == nil {
		t.Fatal("the plain raw object is still in the store")
	}

	// A user key rotation, then reseal.
	out = c.mustOHS("vault", "rotate", "user", c.tenant.String(), c.user.String())
	wants(t, out, "key version 2", "ohs vault reseal --user "+c.user.String())
	wants(t, c.mustOHS("vault", "reseal", "--dry-run"), "1 users still have data under an older key version")
	out = c.mustOHS("vault", "reseal", "--user", c.user.String())
	wants(t, out, "reseal: 1 users, 6 rows, 1 raw objects, 1 old key versions retired")
	var old int
	c.scan(&old, `SELECT count(*) FROM ohs_data.quantity_samples WHERE substring(sealed from 2 for 4) <> '\x00000002'`)
	if old != 0 {
		t.Fatalf("%d rows still under version 1", old)
	}

	// Both key versions of the user are rewrapped under the new tenant key.
	wants(t, c.mustOHS("vault", "rotate", "tenant", c.tenant.String()), "2 user keys rewrapped")
	wants(t, c.mustOHS("vault", "seal"), "seal: 0 users")
	if _, err := vault.OpenProvider(ctx, config.Vault{Provider: "local"}); err == nil {
		t.Fatal("a local provider with no root key opens")
	}
}

// Moving from the root key derived from OHS_ENCRYPTION_KEY to a root key of
// its own: the server refuses to open keys until `ohs vault rotate root` ran,
// which finds the derived key by itself.
func TestVaultRotateRootFromTheDerivedKey(t *testing.T) {
	c := newVaultCLI(t, bytes.Repeat([]byte{4}, 32))
	c.mustOHS("vault", "seal")

	t.Setenv("OHS_VAULT_ROOT_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	out, err := c.ohs("vault", "seal")
	if err == nil || !strings.Contains(err.Error(), "ohs vault rotate root") {
		t.Fatalf("the new root key opens keys it never wrapped: %v\n%s", err, out)
	}
	out, err = c.ohs("doctor")
	if err == nil {
		t.Fatalf("doctor passes with a root key that opens nothing:\n%s", out)
	}
	wants(t, out, "fail  vault key provider (local): does not open the tenant keys")

	out = c.mustOHS("vault", "rotate", "root")
	wants(t, out, "1 tenant keys rewrapped", "Restart every ohs process")
	if strings.Contains(out, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))) {
		t.Fatalf("output shows the key:\n%s", out)
	}
	wants(t, c.mustOHS("vault", "seal"), "seal: 0 users")
	wants(t, c.mustOHS("doctor"), "ok    vault key provider (local) (opens the tenant keys)", "ok    vault (nothing is stored clear)")
	wants(t, c.mustOHS("vault", "rotate", "root"), "nothing to do")
}

// `ohs admin rekey` after an OHS_ENCRYPTION_KEY rotation also moves the tenant
// keys to the root key derived from the new one: the old encryption key can
// then leave the settings without the health data becoming unreadable.
func TestAdminRekeyRewrapsADerivedVaultRoot(t *testing.T) {
	oldKey, newKey := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	c := newVaultCLI(t, oldKey)
	c.mustOHS("vault", "seal")

	t.Setenv("OHS_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(newKey))
	t.Setenv("OHS_PREVIOUS_ENCRYPTION_KEYS", base64.StdEncoding.EncodeToString(oldKey))
	wants(t, c.mustOHS("doctor"), "warn  vault: 1 tenant keys are wrapped by a previous root key")
	wants(t, c.mustOHS("admin", "rekey"), "1 tenant keys rewrapped under the new one")

	t.Setenv("OHS_PREVIOUS_ENCRYPTION_KEYS", "")
	wants(t, c.mustOHS("vault", "seal"), "seal: 0 users")
	wants(t, c.mustOHS("admin", "rekey"), "0 tenant keys rewrapped under the new one, 1 already were")
}

// `ohs admin personal setup` gives the person's user vault keys, so an
// upload for them is accepted at once, and gives them to a user it finds
// without.
func TestPersonalSetupGivesTheUserKeys(t *testing.T) {
	c := newVaultCLI(t, bytes.Repeat([]byte{4}, 32))
	keysOf := func(ref string) (n int) {
		c.scan(&n, `SELECT count(*) FROM ohs_core.vault_keys k JOIN ohs_core.end_users u
			ON u.tenant_id = k.tenant_id AND u.id = k.user_id WHERE u.external_ref = $1`, ref)
		return n
	}
	wants(t, c.mustOHS("admin", "personal", "setup"), "OHS_USER=")
	if keysOf("me") != 1 {
		t.Fatal("the new user has no keys")
	}
	// A user of the personal tenant stored without keys.
	c.exec(`INSERT INTO ohs_core.end_users (tenant_id, external_ref)
		SELECT id, 'old' FROM ohs_core.tenants WHERE name = 'personal'`)
	c.mustOHS("admin", "personal", "setup", "--person", "old")
	if keysOf("old") != 1 {
		t.Fatal("a user found without keys still has none")
	}
	if c.mustOHS("admin", "personal", "setup"); keysOf("me") != 1 {
		t.Fatal("a second run changed the keys")
	}
}
