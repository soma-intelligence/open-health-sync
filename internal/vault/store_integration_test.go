// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package vault

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/db"
)

// countingProvider is the local provider with a count of its calls and a
// switch that makes it fail as an unreachable KMS does.
type countingProvider struct {
	KeyProvider
	wraps, unwraps atomic.Int64
	down           atomic.Bool
}

func newCountingProvider(t *testing.T) *countingProvider {
	t.Helper()
	p, err := newLocalProvider(rootKey(7), nil)
	require.NoError(t, err)
	return &countingProvider{KeyProvider: p}
}

func (p *countingProvider) Wrap(ctx context.Context, plain, aad []byte) ([]byte, string, error) {
	p.wraps.Add(1)
	if p.down.Load() {
		return nil, "", fmt.Errorf("%w: connection refused", ErrProviderUnavailable)
	}
	return p.KeyProvider.Wrap(ctx, plain, aad)
}

func (p *countingProvider) Unwrap(ctx context.Context, wrapped, aad []byte, version string) ([]byte, error) {
	p.unwraps.Add(1)
	if p.down.Load() {
		return nil, fmt.Errorf("%w: connection refused", ErrProviderUnavailable)
	}
	return p.KeyProvider.Unwrap(ctx, wrapped, aad, version)
}

func (p *countingProvider) calls() int64 { return p.wraps.Load() + p.unwraps.Load() }

type storeEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant uuid.UUID
}

func newStoreEnv(t *testing.T) *storeEnv {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	e := &storeEnv{t: t, pool: pool}
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ('t') RETURNING id`).Scan(&e.tenant))
	return e
}

func (e *storeEnv) user(ref string) uuid.UUID {
	e.t.Helper()
	var u uuid.UUID
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`INSERT INTO ohs_core.end_users (tenant_id, external_ref) VALUES ($1, $2) RETURNING id`, e.tenant, ref).Scan(&u))
	return u
}

// ensure gives user keys in a transaction of its own, as user creation does.
func (e *storeEnv) ensure(v *Vault, user uuid.UUID) error {
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := v.EnsureUser(ctx, tx, e.tenant, user); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (e *storeEnv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (e *storeEnv) vault(p KeyProvider, o Options) *Vault {
	if o.CacheUsers == 0 {
		o.CacheUsers = 16
	}
	if o.CacheTTL == 0 {
		o.CacheTTL = time.Minute
	}
	return New(e.pool, p, o)
}

func TestEnsureUserIdempotentAndConcurrent(t *testing.T) {
	e := newStoreEnv(t)
	p := newCountingProvider(t)
	v := e.vault(p, Options{CanOpen: true})
	u := e.user("u-1")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() { errs[i] = e.ensure(v, u) })
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.NoError(t, e.ensure(v, u), "again: nothing to do")

	require.Equal(t, 1, e.count(`SELECT count(*) FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id IS NULL`, e.tenant), "one tenant key")
	require.Equal(t, 1, e.count(`SELECT count(*) FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id = $2`, e.tenant, u), "one user key")
	require.Equal(t, 1, e.count(`SELECT count(*) FROM ohs_core.vault_root WHERE provider = 'local'`))
	require.Equal(t, 2, e.count(`SELECT count(*) FROM ohs_core.audit_events WHERE tenant_id = $1 AND action = 'vault.key_created'`, e.tenant),
		"one event for the tenant key, one for the user key")

	// Two users racing on a tenant with no key yet still make one tenant key.
	e2 := newStoreEnv(t)
	u2a, u2b := e2.user("a"), e2.user("b")
	v2 := e2.vault(p, Options{CanOpen: true})
	wg.Go(func() { errs[0] = e2.ensure(v2, u2a) })
	wg.Go(func() { errs[1] = e2.ensure(v2, u2b) })
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.Equal(t, 1, e2.count(`SELECT count(*) FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id IS NULL`, e2.tenant))
	for _, user := range []uuid.UUID{u2a, u2b} {
		_, err := v2.ForUser(context.Background(), e2.tenant, user)
		require.NoError(t, err, "each user key opens under the one tenant key")
	}
}

func TestPublicKeyMatchesUserKeys(t *testing.T) {
	ctx := context.Background()
	e := newStoreEnv(t)
	v := e.vault(newCountingProvider(t), Options{CanOpen: true})
	u := e.user("u-1")
	_, _, err := v.PublicKey(ctx, e.pool, e.tenant, u)
	require.ErrorIs(t, err, ErrNoKeys)
	_, err = v.ForUser(ctx, e.tenant, u)
	require.ErrorIs(t, err, ErrNoKeys)
	require.NoError(t, e.ensure(v, u))

	pub, ver, err := v.PublicKey(ctx, e.pool, e.tenant, u)
	require.NoError(t, err)
	require.Equal(t, uint32(1), ver)
	uv, err := v.ForUser(ctx, e.tenant, u)
	require.NoError(t, err)
	k, err := uv.Keys(1)
	require.NoError(t, err)
	want, err := k.Public()
	require.NoError(t, err)
	require.Equal(t, want, pub)
	_, err = uv.Keys(2)
	require.ErrorIs(t, err, ErrOpen)
}

func TestUserVaultSealOpen(t *testing.T) {
	ctx := context.Background()
	e := newStoreEnv(t)
	m := NewMetrics()
	v := e.vault(newCountingProvider(t), Options{CanOpen: true, Metrics: m})
	u, other := e.user("u-1"), e.user("u-2")
	require.NoError(t, e.ensure(v, u))
	require.NoError(t, e.ensure(v, other))
	uv, err := v.ForUser(ctx, e.tenant, u)
	require.NoError(t, err)
	ov, err := v.ForUser(ctx, e.tenant, other)
	require.NoError(t, err)

	tbl := Schema["sleep_phases"]
	vals := make([]any, len(tbl.Sealed))
	vals[0] = "deep"
	sealed, err := uv.SealRow(tbl, []byte("id-1"), vals)
	require.NoError(t, err)
	got, err := uv.OpenRow(tbl, []byte("id-1"), sealed)
	require.NoError(t, err)
	require.Equal(t, vals, got)

	_, err = uv.OpenRow(tbl, []byte("id-2"), sealed)
	require.ErrorIs(t, err, ErrOpen, "another row")
	_, err = ov.OpenRow(tbl, []byte("id-1"), sealed)
	require.ErrorIs(t, err, ErrOpen, "another user")
	_, err = uv.SealRow(tbl, nil, []any{"too few"})
	require.Error(t, err)
	require.Equal(t, int64(1), m.sealsOK.Load())
	require.Equal(t, int64(1), m.sealsErr.Load())
	require.Equal(t, int64(1), m.opensOK.Load())
	require.Equal(t, int64(2), m.opensErr.Load())
	require.Equal(t, int64(2), m.failRow.Load())

	h := uv.ContentHash([]byte("canonical"))
	require.Len(t, h, 64)
	_, err = hex.DecodeString(h)
	require.NoError(t, err)
	require.Equal(t, h, uv.ContentHash([]byte("canonical")), "deterministic for dedup")
	require.NotEqual(t, h, ov.ContentHash([]byte("canonical")), "keyed per user")
	k, err := uv.Keys(1)
	require.NoError(t, err)
	mac := k.MAC("content", []byte("canonical"))
	require.Equal(t, hex.EncodeToString(mac[:]), h)

	plain, err := tbl.Encode(vals)
	require.NoError(t, err)
	rowMAC := k.MAC("row:sleep_phases", plain)
	require.Equal(t, rowMAC[:], uv.ContentMAC(tbl, vals))
	require.Nil(t, uv.ContentMAC(tbl, []any{"too few"}))
	src := k.MAC("source", []byte("Watch"))
	require.Equal(t, src[:], uv.SourceKey("Watch"))
}

func TestForUserUnwrapsOncePerTenantPerTTL(t *testing.T) {
	ctx := context.Background()
	e := newStoreEnv(t)
	u1, u2 := e.user("u-1"), e.user("u-2")
	setup := e.vault(newCountingProvider(t), Options{CanOpen: true})
	require.NoError(t, e.ensure(setup, u1))
	require.NoError(t, e.ensure(setup, u2))

	p := newCountingProvider(t)
	m := NewMetrics()
	clk := &fakeClock{t: time.Now()}
	v := e.vault(p, Options{CanOpen: true, CacheTTL: 10 * time.Minute, Metrics: m})
	v.setClock(clk.now)
	for range 3 {
		_, err := v.ForUser(ctx, e.tenant, u1)
		require.NoError(t, err)
		_, err = v.ForUser(ctx, e.tenant, u2)
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), p.unwraps.Load(), "one unwrap for the tenant, then the caches answer")
	require.Equal(t, int64(2), m.cacheMiss.Load())
	require.Equal(t, int64(4), m.cacheHit.Load())
	require.Positive(t, m.providerCount.Load())

	clk.t = clk.t.Add(10 * time.Minute)
	_, err := v.ForUser(ctx, e.tenant, u1)
	require.NoError(t, err)
	_, err = v.ForUser(ctx, e.tenant, u2)
	require.NoError(t, err)
	require.Equal(t, int64(2), p.unwraps.Load(), "after the time to live, one more")
}

func TestCannotOpenNeverCallsProvider(t *testing.T) {
	ctx := context.Background()
	e := newStoreEnv(t)
	u := e.user("u-1")
	require.NoError(t, e.ensure(e.vault(newCountingProvider(t), Options{CanOpen: true}), u))

	p := newCountingProvider(t)
	v := e.vault(p, Options{CanOpen: false})
	pub, ver, err := v.PublicKey(ctx, e.pool, e.tenant, u)
	require.NoError(t, err)
	require.Equal(t, uint32(1), ver)
	require.NotEmpty(t, pub.MLKEM)
	_, err = v.ForUser(ctx, e.tenant, u)
	require.ErrorIs(t, err, ErrCannotOpen)
	require.ErrorIs(t, e.ensure(v, e.user("u-2")), ErrCannotOpen)
	require.NoError(t, v.Ready(ctx), "a process that cannot open needs no provider to be ready")
	require.Zero(t, p.calls())
}

func TestProviderDown(t *testing.T) {
	ctx := context.Background()
	e := newStoreEnv(t)
	u := e.user("u-1")
	require.NoError(t, v0(e).Ready(ctx), "no tenant key yet: nothing to check")
	require.NoError(t, e.ensure(v0(e), u))

	// Cold cache: the outage is the caller's 503, never an open failure.
	p := newCountingProvider(t)
	p.down.Store(true)
	m := NewMetrics()
	cold := e.vault(p, Options{CanOpen: true, Metrics: m})
	_, err := cold.ForUser(ctx, e.tenant, u)
	require.ErrorIs(t, err, ErrProviderUnavailable)
	require.ErrorIs(t, cold.Ready(ctx), ErrProviderUnavailable)
	require.Zero(t, m.failKey.Load())
	tx, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, cold.EnsureUser(ctx, tx, e.tenant, e.user("u-2")), ErrProviderUnavailable)
	require.NoError(t, tx.Rollback(ctx))

	// Warm cache: the keys already unwrapped keep serving.
	p.down.Store(false)
	_, err = cold.ForUser(ctx, e.tenant, u)
	require.NoError(t, err)
	require.NoError(t, cold.Ready(ctx))
	p.down.Store(true)
	_, err = cold.ForUser(ctx, e.tenant, u)
	require.NoError(t, err)
	require.NoError(t, cold.Ready(ctx))
}

func v0(e *storeEnv) *Vault { return e.vault(newCountingProvider(e.t), Options{CanOpen: true}) }

func TestWrappedUserKeyThatDoesNotOpen(t *testing.T) {
	ctx := context.Background()
	e := newStoreEnv(t)
	u := e.user("u-1")
	require.NoError(t, e.ensure(v0(e), u))
	_, err := e.pool.Exec(ctx, `UPDATE ohs_core.vault_keys SET wrapped = set_byte(wrapped, 30, get_byte(wrapped, 30) # 1)
		WHERE tenant_id = $1 AND user_id = $2`, e.tenant, u)
	require.NoError(t, err)
	m := NewMetrics()
	v := e.vault(newCountingProvider(t), Options{CanOpen: true, Metrics: m})
	_, err = v.ForUser(ctx, e.tenant, u)
	require.ErrorIs(t, err, ErrOpen)
	require.False(t, errors.Is(err, ErrProviderUnavailable))
	require.Equal(t, int64(1), m.failKey.Load())
}

func TestShredNotifyEvictsOtherVault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newStoreEnv(t)
	u, kept := e.user("u-1"), e.user("u-2")
	a := e.vault(newCountingProvider(t), Options{CanOpen: true})
	require.NoError(t, e.ensure(a, u))
	require.NoError(t, e.ensure(a, kept))

	listening := make(chan struct{}, 1)
	a.onListen = func() { listening <- struct{}{} }
	done := make(chan error, 1)
	go func() { done <- a.Listen(ctx) }()
	select {
	case <-listening:
	case <-time.After(10 * time.Second):
		t.Fatal("Listen did not start")
	}
	_, err := a.ForUser(ctx, e.tenant, u)
	require.NoError(t, err)
	_, err = a.ForUser(ctx, e.tenant, kept)
	require.NoError(t, err)
	require.True(t, a.cachedUser(e.tenant, u))

	b := e.vault(newCountingProvider(t), Options{CanOpen: true})
	tx, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, b.Shred(ctx, tx, e.tenant, u))
	require.True(t, a.cachedUser(e.tenant, u), "nothing is sent before the commit")
	require.NoError(t, tx.Commit(ctx))

	require.Eventually(t, func() bool { return !a.cachedUser(e.tenant, u) }, time.Second, 10*time.Millisecond,
		"the other vault evicts on the notification")
	require.True(t, a.cachedUser(e.tenant, kept), "only the shredded user leaves")
	_, err = a.ForUser(ctx, e.tenant, u)
	require.ErrorIs(t, err, ErrNoKeys)
	_, _, err = a.PublicKey(ctx, e.pool, e.tenant, u)
	require.ErrorIs(t, err, ErrNoKeys)
	require.Zero(t, e.count(`SELECT count(*) FROM ohs_core.vault_keys WHERE user_id = $1`, u))

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not stop")
	}
}

// cachedUser reports whether the user's keys are in the cache (tests).
func (v *Vault) cachedUser(tenant, user uuid.UUID) bool {
	return v.users.get(userRef{tenant, user}, func(*userEntry) {})
}
