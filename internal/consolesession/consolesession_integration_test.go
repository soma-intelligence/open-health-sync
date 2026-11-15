// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package consolesession_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/consolesession"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

type env struct {
	pool       *pgxpool.Pool
	ts         *tenant.Store
	cs         *consolesession.Store
	a, b       tenant.Tenant
	keyA, keyB string
	keyIDA     uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	e := &env{pool: pool, ts: tenant.New(pool), cs: consolesession.New(pool)}
	e.a, e.keyA, err = e.ts.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	e.b, e.keyB, err = e.ts.CreateTenant(ctx, "Other")
	require.NoError(t, err)
	p, err := e.ts.LookupKey(ctx, auth.Hash(e.keyA))
	require.NoError(t, err)
	e.keyIDA = uuid.MustParse(p.KeyID)
	return e
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

func (e *env) open(t *testing.T) (string, consolesession.Session) {
	t.Helper()
	token, _, sess, err := e.cs.Create(context.Background(), e.a.ID, e.keyIDA, false)
	require.NoError(t, err)
	return token, sess
}

func TestCreateAndLookup(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	token, csrf, sess, err := e.cs.Create(ctx, e.a.ID, e.keyIDA, true)
	require.NoError(t, err)
	require.Len(t, token, 43)
	require.Len(t, csrf, 43)
	require.NotEqual(t, token, csrf)

	got, err := e.cs.Lookup(ctx, auth.Hash(token))
	require.NoError(t, err)
	require.Equal(t, sess.ID, got.ID)
	require.Equal(t, e.a.ID, got.TenantID)
	require.Equal(t, e.keyIDA, got.APIKeyID)
	require.Equal(t, e.a.ID, got.ActingTenantID)
	require.True(t, got.ReadOnly)
	require.Equal(t, auth.Hash(csrf), got.CSRFHash)
	require.Equal(t, e.keyA[:auth.KeyPrefixLen], got.KeyPrefix)
	require.Equal(t, e.a.Admin, got.TenantAdmin)
	require.Equal(t, "Acme", got.TenantName)
	require.Equal(t, got.CreatedAt.Add(consolesession.MaxAge), got.ExpiresAt())

	var row string
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT s::text FROM ohs_core.console_sessions s`).Scan(&row))
	require.NotContains(t, row, token)
	require.NotContains(t, row, csrf)

	_, err = e.cs.Lookup(ctx, auth.Hash("nope"))
	require.ErrorIs(t, err, consolesession.ErrNotFound)
	_, _, _, err = e.cs.Create(ctx, e.b.ID, e.keyIDA, false)
	require.ErrorIs(t, err, consolesession.ErrNotFound, "a key of another tenant")
}

func TestLookupIdleAndAbsoluteExpiry(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	token, sess := e.open(t)
	h := auth.Hash(token)

	e.exec(t, `UPDATE ohs_core.console_sessions SET last_seen_at = now() - interval '12 hours 1 minute' WHERE id = $1`, sess.ID)
	_, err := e.cs.Lookup(ctx, h)
	require.ErrorIs(t, err, consolesession.ErrNotFound)

	e.exec(t, `UPDATE ohs_core.console_sessions SET last_seen_at = now() - interval '11 hours 59 minutes' WHERE id = $1`, sess.ID)
	_, err = e.cs.Lookup(ctx, h)
	require.NoError(t, err)

	e.exec(t, `UPDATE ohs_core.console_sessions SET created_at = now() - interval '7 days 1 minute', last_seen_at = now() WHERE id = $1`, sess.ID)
	_, err = e.cs.Lookup(ctx, h)
	require.ErrorIs(t, err, consolesession.ErrNotFound)
}

func TestLookupRefusesRevokedOrExpiredKey(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	token, _ := e.open(t)
	h := auth.Hash(token)
	_, err := e.cs.Lookup(ctx, h)
	require.NoError(t, err)

	e.exec(t, `UPDATE ohs_core.api_keys SET expires_at = now() - interval '1 minute' WHERE id = $1`, e.keyIDA)
	_, err = e.cs.Lookup(ctx, h)
	require.ErrorIs(t, err, consolesession.ErrNotFound)
	e.exec(t, `UPDATE ohs_core.api_keys SET expires_at = NULL WHERE id = $1`, e.keyIDA)
	_, err = e.cs.Lookup(ctx, h)
	require.NoError(t, err)

	require.NoError(t, e.ts.RevokeKeyByID(ctx, e.a.ID, e.keyIDA))
	_, err = e.cs.Lookup(ctx, h)
	require.ErrorIs(t, err, consolesession.ErrNotFound)
}

func TestLookupTouchesAtMostOncePerMinute(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	token, sess := e.open(t)
	h := auth.Hash(token)
	seen := func() time.Time {
		var ts time.Time
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT last_seen_at FROM ohs_core.console_sessions WHERE id = $1`, sess.ID).Scan(&ts))
		return ts
	}

	e.exec(t, `UPDATE ohs_core.console_sessions SET last_seen_at = now() - interval '30 seconds' WHERE id = $1`, sess.ID)
	before := seen()
	_, err := e.cs.Lookup(ctx, h)
	require.NoError(t, err)
	require.True(t, seen().Equal(before), "within a minute: unchanged")

	e.exec(t, `UPDATE ohs_core.console_sessions SET last_seen_at = now() - interval '2 minutes' WHERE id = $1`, sess.ID)
	_, err = e.cs.Lookup(ctx, h)
	require.NoError(t, err)
	var gap time.Duration
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT now() - last_seen_at FROM ohs_core.console_sessions WHERE id = $1`, sess.ID).Scan(&gap))
	require.Less(t, gap, time.Second)
}

func TestRotateCSRF(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	token, csrf, sess, err := e.cs.Create(ctx, e.a.ID, e.keyIDA, false)
	require.NoError(t, err)
	next, err := e.cs.RotateCSRF(ctx, sess.ID)
	require.NoError(t, err)
	require.Len(t, next, 43)
	require.NotEqual(t, csrf, next)
	got, err := e.cs.Lookup(ctx, auth.Hash(token))
	require.NoError(t, err)
	require.Equal(t, auth.Hash(next), got.CSRFHash)
	require.NotEqual(t, auth.Hash(csrf), got.CSRFHash)

	_, err = e.cs.RotateCSRF(ctx, uuid.New())
	require.ErrorIs(t, err, consolesession.ErrNotFound)
}

func TestSetActingTenant(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	token, sess := e.open(t)
	h := auth.Hash(token)

	got, err := e.cs.SetActingTenant(ctx, sess.ID, e.b.ID)
	require.NoError(t, err)
	require.Equal(t, e.b.ID, got.ActingTenantID)
	require.Equal(t, "Other", got.ActingTenantName)
	look, err := e.cs.Lookup(ctx, h)
	require.NoError(t, err)
	require.Equal(t, e.b.ID, look.ActingTenantID)
	require.Equal(t, "Other", look.ActingTenantName)
	require.Equal(t, e.a.ID, look.TenantID)

	_, err = e.cs.SetActingTenant(ctx, sess.ID, uuid.New())
	require.ErrorIs(t, err, consolesession.ErrTenantNotFound)
	look, err = e.cs.Lookup(ctx, h)
	require.NoError(t, err)
	require.Equal(t, e.b.ID, look.ActingTenantID, "unchanged")

	_, err = e.cs.SetActingTenant(ctx, uuid.New(), e.b.ID)
	require.ErrorIs(t, err, consolesession.ErrNotFound)
}

func TestSessionEndsWithItsTenant(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	_, sess := e.open(t)
	_, err := e.cs.SetActingTenant(ctx, sess.ID, e.b.ID)
	require.NoError(t, err)
	e.exec(t, `DELETE FROM ohs_core.tenants WHERE id = $1`, e.b.ID)
	var n int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.console_sessions WHERE id = $1`, sess.ID).Scan(&n))
	require.Zero(t, n)
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	token, sess := e.open(t)
	require.NoError(t, e.cs.Delete(ctx, sess.ID))
	require.NoError(t, e.cs.Delete(ctx, sess.ID))
	_, err := e.cs.Lookup(ctx, auth.Hash(token))
	require.ErrorIs(t, err, consolesession.ErrNotFound)
}

func TestPurgeEnded(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	keyB2, err := e.ts.CreateKey(ctx, e.b.ID)
	require.NoError(t, err)
	p, err := e.ts.LookupKey(ctx, auth.Hash(keyB2))
	require.NoError(t, err)
	revokedKey := uuid.MustParse(p.KeyID)

	liveTok, _ := e.open(t)
	_, idle := e.open(t)
	_, old := e.open(t)
	_, _, revoked, err := e.cs.Create(ctx, e.b.ID, revokedKey, false)
	require.NoError(t, err)

	e.exec(t, `UPDATE ohs_core.console_sessions SET last_seen_at = now() - interval '13 hours' WHERE id = $1`, idle.ID)
	e.exec(t, `UPDATE ohs_core.console_sessions SET created_at = now() - interval '8 days' WHERE id = $1`, old.ID)
	require.NoError(t, e.ts.RevokeKeyByID(ctx, e.b.ID, revokedKey))
	_ = revoked

	n, err := e.cs.PurgeEnded(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	_, err = e.cs.Lookup(ctx, auth.Hash(liveTok))
	require.NoError(t, err)
	var left int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.console_sessions`).Scan(&left))
	require.Equal(t, 1, left)
}
