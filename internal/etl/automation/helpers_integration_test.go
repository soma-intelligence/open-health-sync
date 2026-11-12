// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package automation

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	rs     rawstore.Store
	tenant uuid.UUID
	user   uuid.UUID
	seq    int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool, err := db.Open(context.Background(), db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	rs, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)
	e := &env{t: t, pool: pool, rs: rs}
	e.tenant = e.newTenant()
	e.user = e.newUser(e.tenant)
	return e
}

func (e *env) newTenant() uuid.UUID {
	var id uuid.UUID
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`INSERT INTO ohs_core.tenants (name) VALUES ('t') RETURNING id`).Scan(&id))
	return id
}

func (e *env) newUser(tenant uuid.UUID) uuid.UUID {
	var id uuid.UUID
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`INSERT INTO ohs_core.end_users (tenant_id) VALUES ($1) RETURNING id`, tenant).Scan(&id))
	return id
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	_, err := e.pool.Exec(context.Background(), sql, args...)
	require.NoError(e.t, err)
}

func (e *env) scalar(sql string, args ...any) int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

// file puts a raw file in the store for the env's user and returns its key.
func (e *env) file(provider, day string) string {
	e.t.Helper()
	e.seq++
	d, err := time.Parse("2006-01-02", day)
	require.NoError(e.t, err)
	key := rawstore.Key(e.tenant, e.user, provider, d, fmt.Sprintf("b%d", e.seq), 0)
	require.NoError(e.t, e.rs.Put(context.Background(), key, bytes.NewReader([]byte("x"))))
	return key
}

// register inserts the raw_objects row of key, created age ago.
func (e *env) register(provider, key string, age time.Duration) {
	e.t.Helper()
	e.seq++
	e.exec(`INSERT INTO ohs_sync.raw_objects
		(tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, created_at, data_type)
		VALUES ($1, $2, $3, $4, $5, 1, 'b', 0, now() - make_interval(secs => $6), 'heart_rate')`,
		e.tenant, e.user, provider, key, fmt.Sprintf("h%d", e.seq), age.Seconds())
}

// stored is file plus register, one hour old.
func (e *env) stored(provider, day string) string {
	k := e.file(provider, day)
	e.register(provider, k, time.Hour)
	return k
}

func (e *env) job(key, status string) {
	e.t.Helper()
	e.exec(`INSERT INTO ohs_sync.etl_jobs (tenant_id, user_id, provider, key, status) VALUES ($1, $2, 'p', $3, $4)`,
		e.tenant, e.user, key, status)
}

func (e *env) jobs(key string) int {
	return e.scalar(`SELECT count(*) FROM ohs_sync.etl_jobs WHERE key = $1`, key)
}

func (e *env) failed(key, reason string, age time.Duration) {
	e.t.Helper()
	e.exec(`INSERT INTO ohs_data.failed_records (tenant_id, user_id, provider, raw_key, line_number, reason_code, created_at)
		VALUES ($1, $2, 'p', $3, 1, $4, now() - make_interval(secs => $5))`,
		e.tenant, e.user, key, reason, age.Seconds())
}

func (e *env) failedStatus(key, status string) int {
	return e.scalar(`SELECT count(*) FROM ohs_data.failed_records WHERE raw_key = $1 AND status = $2`, key, status)
}

// bytesReader is the body of a raw file whose content does not matter.
func bytesReader() *bytes.Reader { return bytes.NewReader([]byte("x")) }
