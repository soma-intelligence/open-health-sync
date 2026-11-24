// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// Users stored without keys (before the vault, or by a tool that has none)
// get them at the next start, more than one batch of them.
func TestStartGivesKeysToUsersWithout(t *testing.T) {
	ctx := context.Background()
	url := db.TestDB(t)
	pool, err := db.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	var tenant string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ('old') RETURNING id`).Scan(&tenant))
	_, err = pool.Exec(ctx, `INSERT INTO ohs_core.end_users (tenant_id, external_ref)
		SELECT $1, 'u-' || i FROM generate_series(1, 501) i`, tenant)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO ohs_core.end_users (tenant_id, external_ref, status) VALUES ($1, 'leaving', 'deleting')`, tenant)
	require.NoError(t, err)

	cfg := testConfig(t, url)
	logs := &syncBuffer{}
	start(t, cfg, logs)

	count := func(sql string) (n int) {
		require.NoError(t, pool.QueryRow(ctx, sql, tenant).Scan(&n))
		return n
	}
	assert.Zero(t, count(`SELECT count(*) FROM ohs_core.end_users u WHERE tenant_id = $1 AND status = 'active'
		AND NOT EXISTS (SELECT 1 FROM ohs_core.vault_keys k WHERE k.tenant_id = u.tenant_id AND k.user_id = u.id)`),
		"every active user has keys")
	assert.Equal(t, 501, count(`SELECT count(*) FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id IS NOT NULL`))
	assert.Equal(t, 1, count(`SELECT count(*) FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id IS NULL`),
		"one tenant key")
	assert.Zero(t, count(`SELECT count(*) FROM ohs_core.vault_keys k JOIN ohs_core.end_users u
		ON u.tenant_id = k.tenant_id AND u.id = k.user_id WHERE k.tenant_id = $1 AND u.status = 'deleting'`),
		"a user being deleted gets none")

	// The vault is on the api: /readyz checks it.
	res, err := http.Get("http://" + cfg.Listen + "/readyz")
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusOK, res.StatusCode)
}

// A user created through the api has keys at once.
func TestCreatedUserHasKeys(t *testing.T) {
	ctx := context.Background()
	url := db.TestDB(t)
	cfg := testConfig(t, url)
	logs := &syncBuffer{}
	start(t, cfg, logs)
	keys := bootstrapKeys(t, cfg, logs.String())
	require.Len(t, keys, 1)

	req, err := http.NewRequest(http.MethodPost, "http://"+cfg.Listen+"/v1/users", strings.NewReader(`{"external_ref":"a"}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+keys[0])
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusCreated, res.StatusCode)

	pool, err := db.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	var users, withKeys int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE EXISTS (
		SELECT 1 FROM ohs_core.vault_keys k WHERE k.tenant_id = u.tenant_id AND k.user_id = u.id))
		FROM ohs_core.end_users u`).Scan(&users, &withKeys))
	assert.Equal(t, 1, users)
	assert.Equal(t, 1, withKeys)
}

// countingProvider counts the calls that reach the key provider.
type countingProvider struct {
	vault.KeyProvider
	wraps, unwraps atomic.Int64
}

func (p *countingProvider) Wrap(ctx context.Context, plain, aad []byte) ([]byte, string, error) {
	p.wraps.Add(1)
	return p.KeyProvider.Wrap(ctx, plain, aad)
}

func (p *countingProvider) Unwrap(ctx context.Context, wrapped, aad []byte, version string) ([]byte, error) {
	p.unwraps.Add(1)
	return p.KeyProvider.Unwrap(ctx, wrapped, aad, version)
}

// The startup pass unwraps each tenant key once, whatever the number of
// users and batches: the tenant keys are committed first, then served from
// the cache to every batch transaction.
func TestVaultKeysPassUnwrapsOncePerTenant(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	for i := range 3 {
		var tenant string
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ($1) RETURNING id`,
			fmt.Sprintf("t-%d", i)).Scan(&tenant))
		_, err = pool.Exec(ctx, `INSERT INTO ohs_core.end_users (tenant_id, external_ref)
			SELECT $1, 'u-' || i FROM generate_series(1, 300) i`, tenant)
		require.NoError(t, err)
	}
	local, err := vault.OpenProvider(ctx, testVault(t))
	require.NoError(t, err)
	p := &countingProvider{KeyProvider: local}
	v := vault.New(pool, p, vault.Options{CanOpen: true})

	n, err := ensureVaultKeys(ctx, pool, v)
	require.NoError(t, err)
	assert.Equal(t, 900, n, "two batches, across tenants")
	assert.Equal(t, int64(3), p.wraps.Load(), "one tenant key created per tenant")
	assert.Equal(t, int64(3), p.unwraps.Load(), "one unwrap per tenant")
	var without int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.end_users u WHERE NOT EXISTS (
		SELECT 1 FROM ohs_core.vault_keys k WHERE k.tenant_id = u.tenant_id AND k.user_id = u.id)`).Scan(&without))
	assert.Zero(t, without)

	// Nothing left: a second pass calls the provider no more.
	n, err = ensureVaultKeys(ctx, pool, v)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, int64(3), p.unwraps.Load())

	// A ctx that ends during the pass is a shutdown: no error.
	_, err = pool.Exec(ctx, `INSERT INTO ohs_core.end_users (tenant_id, external_ref)
		SELECT id, 'late' FROM ohs_core.tenants`)
	require.NoError(t, err)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = ensureVaultKeys(cctx, pool, v)
	require.NoError(t, err)
}
