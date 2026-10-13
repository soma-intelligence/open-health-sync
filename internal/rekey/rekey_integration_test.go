// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package rekey_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/rekey"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

var (
	oldKey  = bytes.Repeat([]byte{1}, 32)
	newKey  = bytes.Repeat([]byte{2}, 32)
	lostKey = bytes.Repeat([]byte{9}, 32)
)

// legacySeal writes the form stored before key ids: nonce || ciphertext || tag.
func legacySeal(t *testing.T, key, plain, aad []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	nonce := make([]byte, aead.NonceSize())
	_, err = rand.Read(nonce)
	require.NoError(t, err)
	return aead.Seal(nonce, nonce, plain, aad)
}

type fixture struct {
	pool               *pgxpool.Pool
	tenant             uuid.UUID
	users              [3]uuid.UUID
	oldHook, freshHook uuid.UUID
	lostAccess         []byte
	hookSecret         string
}

func seed(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	f := fixture{pool: pool}
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ('t') RETURNING id`).Scan(&f.tenant))
	for i := range f.users {
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO ohs_core.end_users (tenant_id, external_ref) VALUES ($1, $2) RETURNING id`,
			f.tenant, "u-"+string(rune('a'+i))).Scan(&f.users[i]))
	}
	oldBox, err := crypto.NewBox(oldKey)
	require.NoError(t, err)
	newBox, err := crypto.NewKeyring(newKey, oldKey)
	require.NoError(t, err)
	lostBox, err := crypto.NewBox(lostKey)
	require.NoError(t, err)

	insert := func(user uuid.UUID, provider string, access, refresh []byte) {
		_, err := pool.Exec(ctx, `INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, access_token_enc, refresh_token_enc)
			VALUES ($1, $2, $3, $4, $5)`, f.tenant, user, provider, access, refresh)
		require.NoError(t, err)
	}
	seal := func(b *crypto.Box, user uuid.UUID, provider, plain string) []byte {
		s, err := b.SealFor([]byte(plain), collect.SealAAD(f.tenant, user, provider))
		require.NoError(t, err)
		return s
	}
	// Under the old key, with a header.
	insert(f.users[0], "oura", seal(oldBox, f.users[0], "oura", "access-a"), seal(oldBox, f.users[0], "oura", "refresh-a"))
	// Under the old key, written before key ids; no refresh token.
	insert(f.users[1], "oura", legacySeal(t, oldKey, []byte("access-b"), collect.SealAAD(f.tenant, f.users[1], "oura")), nil)
	// Under a key nobody has any more.
	f.lostAccess = seal(lostBox, f.users[2], "oura", "access-c")
	insert(f.users[2], "oura", f.lostAccess, seal(lostBox, f.users[2], "oura", "refresh-c"))
	// Already under the new key.
	insert(f.users[0], "withings", seal(newBox, f.users[0], "withings", "access-d"), seal(newBox, f.users[0], "withings", "refresh-d"))

	_, f.hookSecret, err = webhook.NewStore(pool, oldBox).Create(ctx, f.tenant, "https://example.org/a", "", []string{})
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM ohs_core.webhook_endpoints WHERE url = 'https://example.org/a'`).Scan(&f.oldHook))
	_, _, err = webhook.NewStore(pool, newBox).Create(ctx, f.tenant, "https://example.org/b", "", []string{})
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM ohs_core.webhook_endpoints WHERE url = 'https://example.org/b'`).Scan(&f.freshHook))
	return f
}

func byColumn[T any](rows []T, col func(T) string) map[string]T {
	m := map[string]T{}
	for _, r := range rows {
		m[col(r)] = r
	}
	return m
}

func TestRunRekeysEverySealedColumn(t *testing.T) {
	ctx := context.Background()
	f := seed(t)
	box, err := crypto.NewKeyring(newKey, oldKey)
	require.NoError(t, err)

	survey, err := rekey.Inspect(ctx, f.pool, box)
	require.NoError(t, err)
	s := byColumn(survey, func(s rekey.Survey) string { return s.Column })
	require.Equal(t, rekey.Survey{Column: "ohs_sync.connections.access_token_enc", Current: 1, Previous: 2, Unknown: 1},
		s["ohs_sync.connections.access_token_enc"])
	require.Equal(t, rekey.Survey{Column: "ohs_core.webhook_endpoints.secret_sealed", Current: 1, Previous: 1},
		s["ohs_core.webhook_endpoints.secret_sealed"])

	var failures []rekey.Failure
	// A batch of 2 rows makes the walk span several pages.
	counts, err := rekey.Run(ctx, f.pool, box, rekey.Options{Batch: 2, OnFailure: func(x rekey.Failure) { failures = append(failures, x) }})
	require.NoError(t, err)
	c := byColumn(counts, func(c rekey.Counts) string { return c.Column })
	require.Len(t, c, len(rekey.Columns()))
	require.Equal(t, rekey.Counts{Column: "ohs_sync.connections.access_token_enc", Rewritten: 2, Current: 1, Failed: 1},
		c["ohs_sync.connections.access_token_enc"])
	require.Equal(t, rekey.Counts{Column: "ohs_sync.connections.refresh_token_enc", Rewritten: 1, Current: 1, Failed: 1},
		c["ohs_sync.connections.refresh_token_enc"])
	require.Equal(t, rekey.Counts{Column: "ohs_core.webhook_endpoints.secret_sealed", Rewritten: 1, Current: 1},
		c["ohs_core.webhook_endpoints.secret_sealed"])
	require.Len(t, failures, 2)
	for _, x := range failures {
		require.Contains(t, x.Row, f.users[2].String())
		require.Contains(t, x.Row, "provider=oura")
		require.NotContains(t, x.Row, "access-c")
	}

	// Every value now opens with the new key alone, bound to the same row.
	alone, err := crypto.NewBox(newKey)
	require.NoError(t, err)
	tokens := map[string]string{}
	rows, err := f.pool.Query(ctx, `SELECT user_id, provider, access_token_enc, refresh_token_enc FROM ohs_sync.connections
		WHERE tenant_id = $1 AND user_id <> $2`, f.tenant, f.users[2])
	require.NoError(t, err)
	for rows.Next() {
		var user uuid.UUID
		var provider string
		var access, refresh []byte
		require.NoError(t, rows.Scan(&user, &provider, &access, &refresh))
		aad := collect.SealAAD(f.tenant, user, provider)
		plain, err := alone.OpenFor(access, aad)
		require.NoError(t, err)
		tokens[string(plain)] = provider
		require.False(t, alone.NeedsRekey(access))
		if refresh != nil {
			plain, err := alone.OpenFor(refresh, aad)
			require.NoError(t, err)
			tokens[string(plain)] = provider
		}
		_, err = alone.OpenFor(access, collect.SealAAD(f.tenant, f.users[2], provider))
		require.Error(t, err, "a rekeyed token stays bound to its row")
	}
	require.NoError(t, rows.Err())
	require.Len(t, tokens, 5) // access-a, refresh-a, access-b, access-d, refresh-d
	var hook []byte
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT secret_sealed FROM ohs_core.webhook_endpoints WHERE id = $1`, f.oldHook).Scan(&hook))
	plain, err := alone.OpenFor(hook, webhook.SecretAAD(f.oldHook))
	require.NoError(t, err)
	require.Equal(t, f.hookSecret, string(plain))

	// The row no key opens is left exactly as it was, never deleted.
	var lost []byte
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT access_token_enc FROM ohs_sync.connections
		WHERE tenant_id = $1 AND user_id = $2 AND provider = 'oura'`, f.tenant, f.users[2]).Scan(&lost))
	require.Equal(t, f.lostAccess, lost)

	// Running it again changes nothing.
	failures = nil
	counts, err = rekey.Run(ctx, f.pool, box, rekey.Options{OnFailure: func(x rekey.Failure) { failures = append(failures, x) }})
	require.NoError(t, err)
	for _, c := range counts {
		require.Zero(t, c.Rewritten, c.Column)
	}
	c = byColumn(counts, func(c rekey.Counts) string { return c.Column })
	require.Equal(t, 3, c["ohs_sync.connections.access_token_enc"].Current)
	require.Len(t, failures, 2)

	survey, err = rekey.Inspect(ctx, f.pool, box)
	require.NoError(t, err)
	for _, s := range survey {
		require.Zero(t, s.Previous, s.Column)
		require.Zero(t, s.Legacy, s.Column)
	}
}

// A serving process can rewrite a token between the batch read and the row
// lock: Run reads the row again under the lock and keeps the newer value.
func TestRunKeepsConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	f := seed(t)
	box, err := crypto.NewKeyring(newKey, oldKey)
	require.NoError(t, err)

	// Hold the lock on one row while Run starts, then refresh its token
	// under the new key, as a serving process would.
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck
	aad := collect.SealAAD(f.tenant, f.users[0], "oura")
	fresh, err := box.SealFor([]byte("access-refreshed"), aad)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE ohs_sync.connections SET access_token_enc = $4
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`, f.tenant, f.users[0], "oura", fresh)
	require.NoError(t, err)

	done := make(chan error, 1)
	var counts []rekey.Counts
	go func() {
		var err error
		counts, err = rekey.Run(ctx, f.pool, box, rekey.Options{})
		done <- err
	}()
	// Run waits on the lock (or has not reached the row yet).
	require.NoError(t, tx.Commit(ctx))
	require.NoError(t, <-done)

	var access []byte
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT access_token_enc FROM ohs_sync.connections
		WHERE tenant_id = $1 AND user_id = $2 AND provider = 'oura'`, f.tenant, f.users[0]).Scan(&access))
	plain, err := box.OpenFor(access, aad)
	require.NoError(t, err)
	require.Equal(t, "access-refreshed", string(plain))
	c := byColumn(counts, func(c rekey.Counts) string { return c.Column })
	require.Equal(t, 1, c["ohs_sync.connections.access_token_enc"].Failed)
}
