// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package deletion_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/deletion"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// newVault is a Vault over the env's database, as another process holds one.
func (e *env) newVault(t *testing.T) *vault.Vault {
	t.Helper()
	p, err := vault.OpenProvider(context.Background(), config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{7}, 32)})
	require.NoError(t, err)
	return vault.New(e.pool, p, vault.Options{CanOpen: true})
}

// newVaultEnv is newEnv whose users are created with keys.
func newVaultEnv(t *testing.T) (*env, *vault.Vault) {
	e := newEnv(t)
	v := e.newVault(t)
	e.store = e.store.WithVault(v)
	return e, v
}

func (e *env) vaultWorker(raw rawstore.Store, v *vault.Vault) *deletion.Worker {
	return deletion.NewWorker(e.pool, raw, nil).WithVault(v).WithBackoff(0)
}

func (e *env) keys(t *testing.T, user uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id = $2`, e.tenant.ID, user).Scan(&n))
	return n
}

// The keys go first, in a transaction of their own: once it commits, what the
// deletion has not removed yet (a crash halfway) opens nowhere, and a later
// pass removes it.
func TestShredCommitsBeforeRowsGo(t *testing.T) {
	ctx := context.Background()
	e, v := newVaultEnv(t)
	a, b := e.newUser(t, "a"), e.newUser(t, "b")
	e.ingest(t, a)
	e.ingest(t, b)

	// One of a's rows carries a sealed value.
	uv, err := v.ForUser(ctx, e.tenant.ID, a.id)
	require.NoError(t, err)
	tbl := vault.Schema["quantity_samples"]
	vals := make([]any, len(tbl.Sealed))
	vals[0] = 72.0
	sealed, err := uv.SealRow(tbl, []byte("row-1"), vals)
	require.NoError(t, err)
	tag, err := e.pool.Exec(ctx, `UPDATE ohs_data.quantity_samples SET sealed = $3
		WHERE id = (SELECT id FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2 LIMIT 1)`,
		e.tenant.ID, a.id, sealed)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())

	// Another process, listening, its cache warm.
	other := e.newVault(t)
	lctx, stopListen := context.WithCancel(ctx)
	defer stopListen()
	go func() { _ = other.Listen(lctx) }()
	e.waitListening(t)
	_, err = other.ForUser(ctx, e.tenant.ID, a.id)
	require.NoError(t, err)

	require.Equal(t, http.StatusAccepted, e.requestDelete(t, a.id).Code)
	// The crash: the raw store fails after the shred committed.
	failing := &failingStore{Store: e.raw, failFor: fmt.Sprintf("raw/%s/%s/", e.tenant.ID, a.id)}
	n, err := e.vaultWorker(failing, v).Pass(ctx)
	require.Error(t, err)
	assert.Zero(t, n)

	assert.Zero(t, e.keys(t, a.id), "the shred committed")
	assert.Equal(t, 1, e.keys(t, b.id))
	assert.NotEmpty(t, e.rowsFor(t, a.id)["ohs_data.quantity_samples"], "the rows are still there")
	_, err = v.ForUser(ctx, e.tenant.ID, a.id)
	require.ErrorIs(t, err, vault.ErrNoKeys, "this process dropped its copy")
	require.Eventually(t, func() bool {
		_, err := other.ForUser(ctx, e.tenant.ID, a.id)
		return errors.Is(err, vault.ErrNoKeys)
	}, time.Second, 10*time.Millisecond, "the warm process drops its copy on the notification")
	_, err = e.newVault(t).ForUser(ctx, e.tenant.ID, a.id)
	require.ErrorIs(t, err, vault.ErrNoKeys, "a second process finds no keys")
	_, err = v.ForUser(ctx, e.tenant.ID, b.id)
	require.NoError(t, err, "the other user keeps its keys")

	// Even fresh keys for the same user (version 1 again) do not open the
	// remaining row: the shredded key is gone for good.
	tx, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, v.EnsureUser(ctx, tx, e.tenant.ID, a.id))
	require.NoError(t, tx.Commit(ctx))
	fresh, err := e.newVault(t).ForUser(ctx, e.tenant.ID, a.id)
	require.NoError(t, err)
	var stored []byte
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT sealed FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NOT NULL`, e.tenant.ID, a.id).Scan(&stored))
	_, err = fresh.OpenRow(tbl, []byte("row-1"), stored)
	require.ErrorIs(t, err, vault.ErrOpen)

	var shredded []map[string]any
	rows, err := e.pool.Query(ctx, `SELECT detail FROM ohs_core.audit_events
		WHERE tenant_id = $1 AND user_id = $2 AND action = 'vault.user_shredded'`, e.tenant.ID, a.id)
	require.NoError(t, err)
	for rows.Next() {
		var d map[string]any
		require.NoError(t, rows.Scan(&d))
		shredded = append(shredded, d)
	}
	require.NoError(t, rows.Err())
	require.Len(t, shredded, 1)
	assert.Equal(t, []string{"job_id"}, keysOf(shredded[0]), "the event names the job, no value")

	// A later pass finishes: the keys made since go first again, then the rest.
	failing.failFor = ""
	n, err = e.vaultWorker(failing, v).Pass(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, e.rowsFor(t, a.id))
	assert.Empty(t, e.rawFiles(t, a.id))
	assert.Zero(t, e.keys(t, a.id))
	assert.Equal(t, 1, e.keys(t, b.id))
	assert.NotEmpty(t, e.rowsFor(t, b.id))
}

// waitListening waits until a connection of the test database listens on
// the vault channel.
func (e *env) waitListening(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		var n int
		require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND query = 'LISTEN ohs_vault'`).Scan(&n))
		return n > 0
	}, 10*time.Second, 10*time.Millisecond)
}

func keysOf(m map[string]any) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

// An ETL job holding the user FOR SHARE holds the deletion up: the shred and
// the row deletes run once the job commits, and take the job's rows too.
func TestDeletionWaitsForEtlJob(t *testing.T) {
	ctx := context.Background()
	e, v := newVaultEnv(t)
	a := e.newUser(t, "a")
	e.ingest(t, a)

	// The job's write transaction, as the ETL takes it.
	job, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	defer job.Rollback(ctx) //nolint:errcheck // no-op after commit
	var status string
	require.NoError(t, job.QueryRow(ctx, `SELECT status FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR SHARE`,
		e.tenant.ID, a.id).Scan(&status))
	require.Equal(t, "active", status)
	_, err = job.Exec(ctx, `INSERT INTO ohs_sync.connections (tenant_id, user_id, provider) VALUES ($1, $2, 'withings')`, e.tenant.ID, a.id)
	require.NoError(t, err)

	done := make(chan int, 1)
	go func() {
		if e.requestDelete(t, a.id).Code != http.StatusAccepted {
			done <- -1
			return
		}
		n, _ := e.vaultWorker(e.raw, v).Pass(ctx)
		done <- n
	}()
	select {
	case n := <-done:
		t.Fatalf("the deletion did not wait for the job (%d)", n)
	case <-time.After(300 * time.Millisecond):
	}
	assert.Equal(t, 1, e.keys(t, a.id), "nothing is shredded while the job runs")
	require.NoError(t, job.Commit(ctx))

	select {
	case n := <-done:
		require.Equal(t, 1, n)
	case <-time.After(20 * time.Second):
		t.Fatal("the deletion did not finish after the job")
	}
	assert.Zero(t, e.keys(t, a.id))
	assert.Empty(t, e.rowsFor(t, a.id), "the job's rows went too")

	// The shred's own lock waits as well: a job that took the row after the
	// user turned deleting holds it until it stops.
	c := e.newUser(t, "c")
	require.Equal(t, http.StatusAccepted, e.requestDelete(t, c.id).Code)
	late, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	defer late.Rollback(ctx) //nolint:errcheck // no-op after rollback
	_, err = late.Exec(ctx, `SELECT 1 FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR SHARE`, e.tenant.ID, c.id)
	require.NoError(t, err)
	go func() {
		n, _ := e.vaultWorker(e.raw, v).Pass(ctx)
		done <- n
	}()
	select {
	case n := <-done:
		t.Fatalf("the shred did not wait for the job (%d)", n)
	case <-time.After(300 * time.Millisecond):
	}
	assert.Equal(t, 1, e.keys(t, c.id))
	require.NoError(t, late.Rollback(ctx))
	select {
	case n := <-done:
		require.Equal(t, 1, n)
	case <-time.After(20 * time.Second):
		t.Fatal("the deletion did not finish after the job")
	}
	assert.Zero(t, e.keys(t, c.id))
}

// A job that starts after the shred finds no keys. For a user being deleted
// it ends as such a job always has: completed, nothing written, never
// retried. An active user with no keys is an error the queue retries and
// then parks.
func TestEtlJobAfterShred(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, v := newVaultEnv(t)
	a, b := e.newUser(t, "a"), e.newUser(t, "b")
	require.Equal(t, http.StatusOK, e.post(t, a.token, gz(t)).Code)
	require.Equal(t, http.StatusAccepted, e.requestDelete(t, a.id).Code)
	failing := &failingStore{Store: e.raw, failFor: fmt.Sprintf("raw/%s/%s/", e.tenant.ID, a.id)}
	_, err := e.vaultWorker(failing, v).Pass(ctx)
	require.Error(t, err)
	require.Zero(t, e.keys(t, a.id))

	lookup := etl.WithLookup(func(slug string) (connector.Connector, bool) {
		return applehealth.New(), slug == applehealth.Slug
	})
	w := etl.NewWorkerWith(e.pool, e.raw, queue.NewETL(e.pool), queue.TierHighFreq, 1, lookup, etl.WithVault(v))
	stopped := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(stopped)
	}()
	require.Eventually(t, func() bool {
		var status string
		var attempts int
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT status, attempts FROM ohs_sync.etl_jobs WHERE tenant_id = $1 AND user_id = $2`,
			e.tenant.ID, a.id).Scan(&status, &attempts))
		require.Zero(t, attempts, "never failed, never retried")
		return status == "done"
	}, 20*time.Second, 50*time.Millisecond)
	cancel()
	<-stopped
	assert.Zero(t, e.rowsFor(t, a.id)["ohs_data.quantity_samples"])

	// An active user whose keys are missing: the job fails on the keys.
	require.Equal(t, http.StatusOK, e.post(t, b.token, gz(t)).Code)
	tx, err := e.pool.Begin(context.Background())
	require.NoError(t, err)
	require.NoError(t, v.Shred(context.Background(), tx, e.tenant.ID, b.id))
	require.NoError(t, tx.Commit(context.Background()))
	var key string
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT key FROM ohs_sync.etl_jobs WHERE tenant_id = $1 AND user_id = $2`,
		e.tenant.ID, b.id).Scan(&key))
	_, err = etl.ProcessWith(context.Background(), e.pool, e.raw, queue.ETLJob{
		TenantID: e.tenant.ID, UserID: b.id, Provider: applehealth.Slug, Key: key, Tier: queue.TierHighFreq,
	}, etl.Options{Vault: v, Lookup: func(slug string) (connector.Connector, bool) {
		return applehealth.New(), slug == applehealth.Slug
	}})
	require.ErrorIs(t, err, vault.ErrNoKeys)
	assert.Zero(t, e.rowsFor(t, b.id)["ohs_data.quantity_samples"])
}

// A provider erasure takes the provider's sealed hourly buckets with its
// samples; another provider's buckets stay as they are.
func TestProviderErasureRemovesItsHourlyBuckets(t *testing.T) {
	ctx := context.Background()
	e, v := newVaultEnv(t)
	a := e.newUser(t, "a")
	rec := e.post(t, a.token, gz(t))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows, err := e.pool.Query(ctx, `SELECT key FROM ohs_sync.etl_jobs WHERE tenant_id = $1 AND user_id = $2`, e.tenant.ID, a.id)
	require.NoError(t, err)
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	for _, k := range keys {
		_, err := etl.ProcessWith(ctx, e.pool, e.raw, queue.ETLJob{
			TenantID: e.tenant.ID, UserID: a.id, Provider: applehealth.Slug, Key: k, Tier: queue.TierHighFreq,
		}, etl.Options{Vault: v, Lookup: func(slug string) (connector.Connector, bool) {
			return applehealth.New(), slug == applehealth.Slug
		}})
		require.NoError(t, err)
	}
	require.NotZero(t, e.providerRows(t, a.id, applehealth.Slug)["ohs_data.quantity_hourly_sealed"], "the job wrote buckets")
	e.seed(t, a.id, other, "active")
	keep := e.providerRows(t, a.id, other)
	require.Equal(t, 1, keep["ohs_data.quantity_hourly_sealed"])

	jobOf(t, e.requestProviderDelete(t, a.id, applehealth.Slug))
	n, err := e.vaultWorker(e.raw, v).Pass(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, e.providerRows(t, a.id, applehealth.Slug))
	assert.Equal(t, keep, e.providerRows(t, a.id, other))
}
