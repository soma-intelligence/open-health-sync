// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package tenant_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

func newStore(t *testing.T) *tenant.Store {
	t.Helper()
	pool, err := db.Open(context.Background(), db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return tenant.New(pool)
}

func TestCreateTenantAndLookupKey(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, key, err := s.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	require.Equal(t, "Acme", ten.Name)
	require.Regexp(t, `^ohs_sk_[0-9A-Za-z]{43}$`, key)

	p, err := s.LookupKey(ctx, auth.Hash(key))
	require.NoError(t, err)
	require.Equal(t, ten.ID, p.TenantID)
	require.Nil(t, p.UserID)
	require.Nil(t, p.Scopes)

	_, err = s.LookupKey(ctx, auth.Hash("ohs_sk_nope"))
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
}

func TestKeyStoredHashedOnly(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	_, key, err := s.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	var n int
	require.NoError(t, s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM ohs_core.api_keys WHERE key_hash = $1 AND key_prefix = $2 AND position($3 in key_prefix || key_hash) = 0`,
		auth.Hash(key), key[:12], key).Scan(&n))
	require.Equal(t, 1, n)
}

func TestRevokeKey(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, key, _ := s.CreateTenant(ctx, "Acme")
	other, _, _ := s.CreateTenant(ctx, "Other")

	require.ErrorIs(t, s.RevokeKey(ctx, other.ID, auth.Hash(key)), tenant.ErrNotFound, "other tenant cannot revoke")
	_, err := s.LookupKey(ctx, auth.Hash(key))
	require.NoError(t, err)

	require.NoError(t, s.RevokeKey(ctx, ten.ID, auth.Hash(key)))
	_, err = s.LookupKey(ctx, auth.Hash(key))
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
	require.NoError(t, s.RevokeKey(ctx, ten.ID, auth.Hash(key)), "revoking twice is fine")
	require.ErrorIs(t, s.RevokeKey(ctx, ten.ID, auth.Hash("missing")), tenant.ErrNotFound)
}

func TestCreateAndGetUser(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "Acme")
	u, err := s.CreateUser(ctx, ten.ID, "ext-1")
	require.NoError(t, err)
	require.Equal(t, "ext-1", u.ExternalRef)
	require.Equal(t, "active", u.Status)
	got, err := s.GetUser(ctx, ten.ID, u.ID)
	require.NoError(t, err)
	require.Equal(t, u.ID, got.ID)

	_, err = s.CreateUser(ctx, ten.ID, "ext-1")
	require.ErrorIs(t, err, tenant.ErrConflict)

	// Empty external refs never collide.
	_, err = s.CreateUser(ctx, ten.ID, "")
	require.NoError(t, err)
	_, err = s.CreateUser(ctx, ten.ID, "")
	require.NoError(t, err)
}

func TestEnsureUser(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a, _, _ := s.CreateTenant(ctx, "A")
	b, _, _ := s.CreateTenant(ctx, "B")

	u, created, err := s.EnsureUser(ctx, a.ID, "demo-user")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "demo-user", u.ExternalRef)

	again, created, err := s.EnsureUser(ctx, a.ID, "demo-user")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, u.ID, again.ID)

	// The same ref in another tenant is another user.
	other, created, err := s.EnsureUser(ctx, b.ID, "demo-user")
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, u.ID, other.ID)

	_, _, err = s.EnsureUser(ctx, uuid.New(), "demo-user")
	require.ErrorIs(t, err, tenant.ErrNotFound)
	_, _, err = s.EnsureUser(ctx, a.ID, "")
	require.Error(t, err)
}

func TestGetUserOtherTenant(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a, _, _ := s.CreateTenant(ctx, "A")
	b, _, _ := s.CreateTenant(ctx, "B")
	u, err := s.CreateUser(ctx, a.ID, "x")
	require.NoError(t, err)
	_, err = s.GetUser(ctx, b.ID, u.ID)
	require.ErrorIs(t, err, tenant.ErrNotFound)
	_, err = s.GetUser(ctx, a.ID, uuid.New())
	require.ErrorIs(t, err, tenant.ErrNotFound)
	_, _, err = s.MintUserToken(ctx, b.ID, u.ID, []string{"upload"}, time.Minute)
	require.ErrorIs(t, err, tenant.ErrNotFound)
}

func TestListUsersPagination(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a, _, _ := s.CreateTenant(ctx, "A")
	b, _, _ := s.CreateTenant(ctx, "B")
	var want []uuid.UUID
	for i := 0; i < 7; i++ {
		u, err := s.CreateUser(ctx, a.ID, "")
		require.NoError(t, err)
		want = append(want, u.ID)
	}
	_, err := s.CreateUser(ctx, b.ID, "")
	require.NoError(t, err)

	var got []uuid.UUID
	cursor := ""
	pages := 0
	for {
		users, next, err := s.ListUsers(ctx, a.ID, cursor, 3)
		require.NoError(t, err)
		for _, u := range users {
			require.Equal(t, a.ID, u.TenantID)
			got = append(got, u.ID)
		}
		pages++
		if next == "" {
			break
		}
		cursor = next
	}
	require.Equal(t, 3, pages)
	require.Equal(t, want, got)

	all, next, err := s.ListUsers(ctx, a.ID, "", 0)
	require.NoError(t, err)
	require.Len(t, all, 7)
	require.Empty(t, next)

	_, _, err = s.ListUsers(ctx, a.ID, "!!!", 10)
	require.ErrorIs(t, err, tenant.ErrInvalidCursor)
}

func TestMintAndLookupUserToken(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "Acme")
	u, _ := s.CreateUser(ctx, ten.ID, "x")

	tok, exp, err := s.MintUserToken(ctx, ten.ID, u.ID, []string{"upload", "connect"}, 0)
	require.NoError(t, err)
	require.Regexp(t, `^ohs_ut_[0-9A-Za-z]{43}$`, tok)
	require.WithinDuration(t, time.Now().Add(time.Hour), exp, 10*time.Second)

	p, err := s.LookupKey(ctx, auth.Hash(tok))
	require.NoError(t, err)
	require.Equal(t, ten.ID, p.TenantID)
	require.NotNil(t, p.UserID)
	require.Equal(t, u.ID, *p.UserID)
	require.ElementsMatch(t, []string{"upload", "connect"}, p.Scopes)
}

func TestMintValidation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "Acme")
	u, _ := s.CreateUser(ctx, ten.ID, "x")

	_, _, err := s.MintUserToken(ctx, ten.ID, u.ID, nil, time.Minute)
	require.ErrorIs(t, err, tenant.ErrInvalidScopes)
	_, _, err = s.MintUserToken(ctx, ten.ID, u.ID, []string{"admin"}, time.Minute)
	require.ErrorIs(t, err, tenant.ErrInvalidScopes)
	_, _, err = s.MintUserToken(ctx, ten.ID, u.ID, []string{"upload", "tenant"}, time.Minute)
	require.ErrorIs(t, err, tenant.ErrInvalidScopes)

	_, _, err = s.MintUserToken(ctx, ten.ID, u.ID, []string{"upload"}, 24*time.Hour)
	require.NoError(t, err)
	_, _, err = s.MintUserToken(ctx, ten.ID, u.ID, []string{"upload"}, 24*time.Hour+time.Second)
	require.ErrorIs(t, err, tenant.ErrTTLTooLong)
	_, _, err = s.MintUserToken(ctx, ten.ID, u.ID, []string{"upload", "read"}, 48*time.Hour)
	require.ErrorIs(t, err, tenant.ErrTTLTooLong)
	_, _, err = s.MintUserToken(ctx, ten.ID, u.ID, []string{"read"}, 2592000*time.Second)
	require.NoError(t, err)
	_, _, err = s.MintUserToken(ctx, ten.ID, u.ID, []string{"read", "read"}, 2592000*time.Second)
	require.NoError(t, err)
	_, _, err = s.MintUserToken(ctx, ten.ID, u.ID, []string{"read"}, 2592000*time.Second+time.Second)
	require.ErrorIs(t, err, tenant.ErrTTLTooLong)
}

func TestTokenExpires(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "Acme")
	u, _ := s.CreateUser(ctx, ten.ID, "x")
	tok, _, err := s.MintUserToken(ctx, ten.ID, u.ID, []string{"upload"}, time.Minute)
	require.NoError(t, err)
	_, err = s.LookupKey(ctx, auth.Hash(tok))
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.user_tokens SET expires_at = now() - interval '1 second' WHERE hash = $1`, auth.Hash(tok))
	require.NoError(t, err)
	_, err = s.LookupKey(ctx, auth.Hash(tok))
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
}

func TestMintSubSecondTTLInvalid(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "Acme")
	u, _ := s.CreateUser(ctx, ten.ID, "x")
	_, _, err := s.MintUserToken(ctx, ten.ID, u.ID, []string{"upload"}, 500*time.Millisecond)
	require.ErrorIs(t, err, tenant.ErrTTLInvalid)
}

func TestRevokeUserToken(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "Acme")
	u, _ := s.CreateUser(ctx, ten.ID, "x")
	tok, _, _ := s.MintUserToken(ctx, ten.ID, u.ID, []string{"upload"}, time.Hour)
	require.NoError(t, s.RevokeKey(ctx, ten.ID, auth.Hash(tok)))
	_, err := s.LookupKey(ctx, auth.Hash(tok))
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
}

func TestDeletingUserTokenRejected(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "Acme")
	u, _ := s.CreateUser(ctx, ten.ID, "x")
	tok, _, _ := s.MintUserToken(ctx, ten.ID, u.ID, []string{"upload"}, time.Hour)
	_, err := s.Pool().Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, u.ID)
	require.NoError(t, err)
	_, err = s.LookupKey(ctx, auth.Hash(tok))
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
	_, _, err = s.MintUserToken(ctx, ten.ID, u.ID, []string{"upload"}, time.Hour)
	require.ErrorIs(t, err, tenant.ErrNotFound)
}

func TestIdempotentReplay(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "A")
	other, _, _ := s.CreateTenant(ctx, "B")
	var calls int
	do := func(body string) func() (int, []byte, error) {
		return func() (int, []byte, error) { calls++; return 201, []byte(body), nil }
	}

	st, b, err := s.Idempotent(ctx, ten.ID, "k1", "h1", do(`{"a":  1}`))
	require.NoError(t, err)
	require.Equal(t, 201, st)
	require.Equal(t, `{"a":  1}`, string(b))

	st, b, err = s.Idempotent(ctx, ten.ID, "k1", "h1", do(`{"other":true}`))
	require.NoError(t, err)
	require.Equal(t, 201, st)
	require.Equal(t, `{"a":  1}`, string(b), "stored bytes replayed verbatim")
	require.Equal(t, 1, calls)

	// Same key, other tenant: independent.
	_, b, err = s.Idempotent(ctx, other.ID, "k1", "h1", do(`{"b":2}`))
	require.NoError(t, err)
	require.Equal(t, `{"b":2}`, string(b))
	require.Equal(t, 2, calls)
}

func TestIdempotentDifferentBody(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "A")
	_, _, err := s.Idempotent(ctx, ten.ID, "k", "h1", func() (int, []byte, error) { return 200, []byte("x"), nil })
	require.NoError(t, err)
	called := false
	_, _, err = s.Idempotent(ctx, ten.ID, "k", "h2", func() (int, []byte, error) { called = true; return 200, nil, nil })
	require.ErrorIs(t, err, tenant.ErrIdempotencyMismatch)
	require.False(t, called)
}

func TestIdempotentErrorsAndServerFaultsNotStored(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "A")
	_, _, err := s.Idempotent(ctx, ten.ID, "k", "h", func() (int, []byte, error) { return 0, nil, context.Canceled })
	require.ErrorIs(t, err, context.Canceled)
	st, _, err := s.Idempotent(ctx, ten.ID, "k", "h", func() (int, []byte, error) { return 503, []byte("down"), nil })
	require.NoError(t, err)
	require.Equal(t, 503, st)
	st, b, err := s.Idempotent(ctx, ten.ID, "k", "h", func() (int, []byte, error) { return 200, []byte("ok"), nil })
	require.NoError(t, err)
	require.Equal(t, 200, st)
	require.Equal(t, "ok", string(b))
}

func TestIdempotentWindow24h(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "A")
	_, _, err := s.Idempotent(ctx, ten.ID, "k", "h1", func() (int, []byte, error) { return 200, []byte("old"), nil })
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.idempotency_keys SET created_at = now() - interval '25 hours'`)
	require.NoError(t, err)
	// Past the window the key is free again, even with another body.
	st, b, err := s.Idempotent(ctx, ten.ID, "k", "h2", func() (int, []byte, error) { return 201, []byte("new"), nil })
	require.NoError(t, err)
	require.Equal(t, 201, st)
	require.Equal(t, "new", string(b))
	st, b, err = s.Idempotent(ctx, ten.ID, "k", "h2", func() (int, []byte, error) { return 500, nil, nil })
	require.NoError(t, err)
	require.Equal(t, 201, st)
	require.Equal(t, "new", string(b))
}

func TestIdempotentConcurrentSameKey(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "A")
	var calls atomic.Int32
	type res struct {
		st   int
		body string
		err  error
	}
	results := make([]res, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, b, err := s.Idempotent(ctx, ten.ID, "k", "h", func() (int, []byte, error) {
				calls.Add(1)
				time.Sleep(300 * time.Millisecond)
				return 202, []byte("done"), nil
			})
			results[i] = res{st, string(b), err}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
	ok := 0
	for _, r := range results {
		if r.err == nil {
			require.Equal(t, 202, r.st)
			require.Equal(t, "done", r.body)
			ok++
		} else {
			require.ErrorIs(t, r.err, tenant.ErrIdempotencyInProgress)
		}
	}
	require.GreaterOrEqual(t, ok, 1)
}

func TestIdempotentDoesNotHoldConnection(t *testing.T) {
	cfg, err := pgxpool.ParseConfig(db.TestDB(t))
	require.NoError(t, err)
	cfg.MaxConns = 2
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer pool.Close()
	s := tenant.New(pool)
	ten, _, err := s.CreateTenant(ctx, "A")
	require.NoError(t, err)

	errs := make([]error, 3)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = s.Idempotent(ctx, ten.ID, fmt.Sprintf("k%d", i), "h", func() (int, []byte, error) {
				var n int
				if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&n); err != nil {
					return 0, nil, err
				}
				return 200, []byte("ok"), nil
			})
		}()
	}
	wg.Wait()
	for _, e := range errs {
		require.NoError(t, e)
	}
}

func TestIdempotentFailureDeletesClaim(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "A")
	_, _, err := s.Idempotent(ctx, ten.ID, "k", "h", func() (int, []byte, error) { return 0, nil, context.Canceled })
	require.Error(t, err)
	var n int
	require.NoError(t, s.Pool().QueryRow(ctx, `SELECT count(*) FROM ohs_core.idempotency_keys`).Scan(&n))
	require.Zero(t, n)
	ran := false
	_, _, err = s.Idempotent(ctx, ten.ID, "k", "h", func() (int, []byte, error) { ran = true; return 200, nil, nil })
	require.NoError(t, err)
	require.True(t, ran)
}

func TestIdempotentStalePendingTakenOver(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "A")
	_, err := s.Pool().Exec(ctx, `INSERT INTO ohs_core.idempotency_keys (tenant_id, key, request_hash, status_code, response_body, created_at)
		VALUES ($1, 'k', 'h', 0, ''::bytea, now() - interval '6 minutes')`, ten.ID)
	require.NoError(t, err)
	st, _, err := s.Idempotent(ctx, ten.ID, "k", "h", func() (int, []byte, error) { return 201, []byte("x"), nil })
	require.NoError(t, err)
	require.Equal(t, 201, st)

	_, err = s.Pool().Exec(ctx, `INSERT INTO ohs_core.idempotency_keys (tenant_id, key, request_hash, status_code, response_body)
		VALUES ($1, 'k2', 'h', 0, ''::bytea)`, ten.ID)
	require.NoError(t, err)
	_, _, err = s.Idempotent(ctx, ten.ID, "k2", "h", func() (int, []byte, error) { return 201, nil, nil })
	require.ErrorIs(t, err, tenant.ErrIdempotencyInProgress)
	_, _, err = s.Idempotent(ctx, ten.ID, "k2", "other", func() (int, []byte, error) { return 201, nil, nil })
	require.ErrorIs(t, err, tenant.ErrIdempotencyMismatch)
}

func TestIdempotentKeyLength(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "A")
	f := func() (int, []byte, error) { return 200, nil, nil }
	_, _, err := s.Idempotent(ctx, ten.ID, strings.Repeat("a", 256), "h", f)
	require.ErrorIs(t, err, tenant.ErrIdempotencyKeyInvalid)
	_, _, err = s.Idempotent(ctx, ten.ID, strings.Repeat("a", 255), "h", f)
	require.NoError(t, err)
}

func TestPurgeIdempotencyDropsExpiredRows(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	s := tenant.New(pool)
	ten, _, err := s.CreateTenant(ctx, "A")
	require.NoError(t, err)
	for _, k := range []string{"old", "fresh", "old-claim"} {
		_, _, err := s.Idempotent(ctx, ten.ID, k, "h", func() (int, []byte, error) { return 201, []byte(`{}`), nil })
		require.NoError(t, err)
	}
	_, err = pool.Exec(ctx, `UPDATE ohs_core.idempotency_keys SET created_at = now() - interval '25 hours' WHERE key IN ('old', 'old-claim')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE ohs_core.idempotency_keys SET status_code = 0 WHERE key = 'old-claim'`)
	require.NoError(t, err)

	n, err := s.PurgeIdempotency(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	var keys []string
	rows, err := pool.Query(ctx, `SELECT key FROM ohs_core.idempotency_keys`)
	require.NoError(t, err)
	for rows.Next() {
		var k string
		require.NoError(t, rows.Scan(&k))
		keys = append(keys, k)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"fresh"}, keys)
}
