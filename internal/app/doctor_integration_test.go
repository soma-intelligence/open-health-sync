// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
)

func TestDoctorAllOK(t *testing.T) {
	out, err := runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL":   db.TestDB(t),
		"OHS_ENCRYPTION_KEY": goodKey(),
		"OHS_RAW_STORE":      "file://" + t.TempDir(),
	}, false, nil)
	require.NoError(t, err, out)
	require.Contains(t, out, "ok    database")
	require.Contains(t, out, "ok    raw store")
	require.Contains(t, out, "ok    encryption keys (0 sealed value(s), all under the active key)")
}

// pool_max_conns in the URL (as the compose file sets it) is for the pool,
// not the server: the doctor's database check must still pass.
func TestDoctorDatabaseURLWithPoolParams(t *testing.T) {
	out, err := runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL":   db.TestDB(t) + "&pool_max_conns=20",
		"OHS_ENCRYPTION_KEY": goodKey(),
		"OHS_RAW_STORE":      "file://" + t.TempDir(),
	}, true, nil)
	require.NoError(t, err, out)
	require.Contains(t, out, "ok    database")
}

// sealedConnection stores one connection whose access token is sealed with key.
func sealedConnection(t *testing.T, url string, key []byte) {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	var tenant, user uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ('t') RETURNING id`).Scan(&tenant))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO ohs_core.end_users (tenant_id, external_ref) VALUES ($1, 'u') RETURNING id`,
		tenant).Scan(&user))
	box, err := crypto.NewBox(key)
	require.NoError(t, err)
	access, err := box.SealFor([]byte("access-token"), collect.SealAAD(tenant, user, "oura"))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, access_token_enc)
		VALUES ($1, $2, 'oura', $3)`, tenant, user, access)
	require.NoError(t, err)
}

func TestDoctorReportsSealingKeys(t *testing.T) {
	oldKey, newKey := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	b64 := base64.StdEncoding.EncodeToString
	url := db.TestDB(t)
	sealedConnection(t, url, oldKey)
	base := map[string]string{
		"OHS_DATABASE_URL": url,
		"OHS_RAW_STORE":    "file://" + t.TempDir(),
	}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}

	// Mid rotation: the token still uses the previous key. A warning, not a failure.
	out, err := runDoctorT(t, with("OHS_ENCRYPTION_KEY", b64(newKey), "OHS_PREVIOUS_ENCRYPTION_KEYS", b64(oldKey)), false, nil)
	require.NoError(t, err, out)
	require.Contains(t, out, "warn  encryption keys: 1 sealed value(s) still use a previous key: run `ohs admin rekey`")
	require.NotContains(t, out, "you can remove")
	require.NotContains(t, out, b64(oldKey))

	// The old key dropped too early: the token opens with no key.
	out, err = runDoctorT(t, with("OHS_ENCRYPTION_KEY", b64(newKey)), false, nil)
	require.NoError(t, err, out)
	require.Contains(t, out, "warn  encryption keys: 1 sealed value(s) open with no configured key")

	// Still under the active key: nothing to do, and no previous key in use.
	out, err = runDoctorT(t, with("OHS_ENCRYPTION_KEY", b64(oldKey), "OHS_PREVIOUS_ENCRYPTION_KEYS", b64(newKey)), false, nil)
	require.NoError(t, err, out)
	require.Contains(t, out, "you can remove OHS_PREVIOUS_ENCRYPTION_KEYS")
	require.Contains(t, out, "ok    encryption keys (1 sealed value(s), all under the active key)")

	// --quick does not read the sealed values.
	out, err = runDoctorT(t, with("OHS_ENCRYPTION_KEY", b64(newKey)), true, nil)
	require.NoError(t, err, out)
	require.NotContains(t, out, "encryption keys")
}

func storeSettingRows(t *testing.T, url string, rows map[string]string) {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	for name, value := range rows {
		_, err := pool.Exec(ctx, `INSERT INTO ohs_core.instance_settings (name, value, updated_by) VALUES ($1, $2, $3)`,
			name, value, uuid.New())
		require.NoError(t, err)
	}
}

func TestDoctorListsStoredSettings(t *testing.T) {
	url := db.TestDB(t)
	storeSettingRows(t, url, map[string]string{"OHS_GONE": "1", "OHS_WEBHOOK_PAUSE_AFTER": "abc"})
	out, err := runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL":   url,
		"OHS_ENCRYPTION_KEY": goodKey(),
		"OHS_RAW_STORE":      "file://" + t.TempDir(),
	}, false, nil)
	require.NoError(t, err, out) // warnings never fail the doctor
	require.Contains(t, out, "warn  settings: stored values no release setting reads any more: OHS_GONE "+
		`(remove them with PATCH /v1/admin/settings {"OHS_GONE": null})`)
	require.Contains(t, out, `warn  settings: stored value skipped: OHS_WEBHOOK_PAUSE_AFTER: "abc" is not a non-negative integer`)
	require.NotContains(t, out, "OHS_WEBHOOK_PAUSE_AFTER: OHS_WEBHOOK_PAUSE_AFTER")
}

// A sealed secret no configured key opens is reported by name; its value
// never reaches the output.
func TestDoctorSettingsNeverPrintsASecret(t *testing.T) {
	url := db.TestDB(t)
	ctx := context.Background()
	other, err := crypto.NewBox(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	sealed, err := other.SealFor([]byte("super-secret-token"), settings.SealAAD("OHS_METRICS_TOKEN"))
	require.NoError(t, err)
	pool, err := db.Open(ctx, url)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO ohs_core.instance_settings (name, sealed, updated_by) VALUES ('OHS_METRICS_TOKEN', $1, $2)`,
		sealed, uuid.New())
	pool.Close()
	require.NoError(t, err)
	out, err := runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL":   url,
		"OHS_ENCRYPTION_KEY": goodKey(),
		"OHS_RAW_STORE":      "file://" + t.TempDir(),
	}, false, nil)
	require.NoError(t, err, out)
	require.Contains(t, out, "warn  settings: stored value skipped: OHS_METRICS_TOKEN: the stored value does not open with OHS_ENCRYPTION_KEY")
	require.NotContains(t, out, "super-secret-token")
}

func TestDoctorSettingsQuietWhenClean(t *testing.T) {
	url := db.TestDB(t)
	storeSettingRows(t, url, map[string]string{"OHS_WEBHOOK_PAUSE_AFTER": "5"})
	out, err := runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL":   url,
		"OHS_ENCRYPTION_KEY": goodKey(),
		"OHS_RAW_STORE":      "file://" + t.TempDir(),
	}, false, nil)
	require.NoError(t, err, out)
	require.NotContains(t, out, "settings:")
}

func TestDoctorSettingsSkippedBeforeMigration(t *testing.T) {
	url := db.TestDB(t)
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DROP TABLE ohs_core.instance_settings, ohs_core.instance_settings_revision`)
	pool.Close()
	require.NoError(t, err)
	out, err := runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL":   url,
		"OHS_ENCRYPTION_KEY": goodKey(),
		"OHS_RAW_STORE":      "file://" + t.TempDir(),
	}, false, nil)
	require.NoError(t, err, out)
	require.Contains(t, out, "skip  settings: the database is not migrated yet")
}
