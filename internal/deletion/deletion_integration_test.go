// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package deletion_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/deletion"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/timetag"
)

func ptr[T any](v T) *T { return &v }

type env struct {
	pool   *pgxpool.Pool
	raw    rawstore.Store
	store  *tenant.Store
	tenant tenant.Tenant
	key    string
	upload http.Handler
	del    http.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	raw, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)
	st := tenant.New(pool)
	ten, key, err := st.CreateTenant(ctx, "Acme")
	require.NoError(t, err)

	up := auth.Middleware(st.LookupKey, auth.UserUpload)(collect.NewUploadHandler(collect.UploadDeps{
		Pool: pool, Raw: raw, Device: applehealth.New(),
	}))
	mux := http.NewServeMux()
	mux.Handle("DELETE /v1/users/{id}", auth.Middleware(st.LookupKey, auth.TenantKey)(deletion.NewDeleteHandler(deletion.Deps{Pool: pool})))
	return &env{pool: pool, raw: raw, store: st, tenant: ten, key: key, upload: up, del: mux}
}

type user struct {
	id    uuid.UUID
	token string
}

func (e *env) newUser(t *testing.T, ref string) user {
	t.Helper()
	ctx := context.Background()
	u, err := e.store.CreateUser(ctx, e.tenant.ID, ref)
	require.NoError(t, err)
	tok, _, err := e.store.MintUserToken(ctx, e.tenant.ID, u.ID, []string{"upload"}, time.Hour)
	require.NoError(t, err)
	return user{id: u.ID, token: tok}
}

func gz(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../connectors/applehealth/testdata/ios_upload_lines.ndjson")
	require.NoError(t, err)
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, err = w.Write(raw)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return b.Bytes()
}

func (e *env) post(t *testing.T, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/apple-health", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(collect.HeaderType, "HKQuantityTypeIdentifierHeartRate")
	req.Header.Set(collect.HeaderDay, "2026-01-10")
	req.Header.Set(collect.HeaderBatch, "batch-1")
	req.Header.Set(collect.HeaderPart, "0")
	rec := httptest.NewRecorder()
	e.upload.ServeHTTP(rec, req)
	return rec
}

// ingest uploads the fixture for u and runs its ETL job to completion.
func (e *env) ingest(t *testing.T, u user) {
	t.Helper()
	rec := e.post(t, u.token, gz(t))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	e.process(t, u.id)
}

func (e *env) process(t *testing.T, userID uuid.UUID) etl.Counts {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT key FROM ohs_sync.etl_jobs WHERE tenant_id = $1 AND user_id = $2`, e.tenant.ID, userID)
	require.NoError(t, err)
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	var total etl.Counts
	for _, k := range keys {
		c, err := etl.ProcessWith(context.Background(), e.pool, e.raw, queue.ETLJob{
			TenantID: e.tenant.ID, UserID: userID, Provider: applehealth.Slug, Key: k, Tier: queue.TierHighFreq,
		}, etl.Options{Lookup: func(slug string) (connector.Connector, bool) {
			return applehealth.New(), slug == applehealth.Slug
		}})
		require.NoError(t, err)
		if len(c) > 0 {
			total = c
		}
	}
	return total
}

func (e *env) requestDelete(t *testing.T, userID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/v1/users/"+userID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+e.key)
	rec := httptest.NewRecorder()
	e.del.ServeHTTP(rec, req)
	return rec
}

// userTables lists every table of ohs_sync and ohs_data that carries a user_id.
func (e *env) userTables(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `
		SELECT c.table_schema || '.' || c.table_name
		FROM information_schema.columns c
		JOIN information_schema.tables t USING (table_schema, table_name)
		WHERE c.table_schema IN ('ohs_sync', 'ohs_data') AND c.column_name = 'user_id'
		  AND t.table_type = 'BASE TABLE'
		ORDER BY 1`)
	require.NoError(t, err)
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	require.Greater(t, len(tables), 20)
	for _, want := range []string{"quantity_samples", "daily_summaries", "raw_objects", "etl_jobs", "connections", "failed_records", "sync_log", "pull_jobs", "time_tags"} {
		found := false
		for _, tbl := range tables {
			if strings.HasSuffix(tbl, "."+want) {
				found = true
			}
		}
		require.True(t, found, "table %s must be user-scoped", want)
	}
	return tables
}

func (e *env) rowsFor(t *testing.T, userID uuid.UUID) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, tbl := range e.userTables(t) {
		var n int
		require.NoError(t, e.pool.QueryRow(context.Background(),
			fmt.Sprintf(`SELECT count(*) FROM %s WHERE tenant_id = $1 AND user_id = $2`, tbl),
			e.tenant.ID, userID).Scan(&n))
		if n > 0 {
			out[tbl] = n
		}
	}
	return out
}

func (e *env) rawFiles(t *testing.T, userID uuid.UUID) []string {
	t.Helper()
	keys, err := e.raw.List(context.Background(), fmt.Sprintf("raw/%s/%s/", e.tenant.ID, userID))
	require.NoError(t, err)
	return keys
}

func (e *env) worker(rev deletion.ConnectionRevoker) *deletion.Worker {
	return deletion.NewWorker(e.pool, e.raw, rev)
}

type fakeRevoker struct {
	calls []uuid.UUID
	err   error
}

func (f *fakeRevoker) RevokeAll(_ context.Context, _, userID uuid.UUID) error {
	f.calls = append(f.calls, userID)
	return f.err
}

func TestDeleteRemovesEverythingOfTheUserOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.newUser(t, "a"), e.newUser(t, "b")
	e.ingest(t, a)
	e.ingest(t, b)
	_, err := e.pool.Exec(ctx, `INSERT INTO ohs_sync.connections (tenant_id, user_id, provider) VALUES ($1, $2, 'withings')`, e.tenant.ID, a.id)
	require.NoError(t, err)
	for _, u := range []user{a, b} {
		_, err = timetag.Create(ctx, e.pool, e.tenant.ID, u.id, timetag.Fields{Kind: "illness",
			StartAt: time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC), Note: ptr("invented note")})
		require.NoError(t, err)
	}

	beforeA, beforeB := e.rowsFor(t, a.id), e.rowsFor(t, b.id)
	require.Equal(t, 1, beforeA["ohs_data.time_tags"], "time tags are user rows")
	require.NotEmpty(t, beforeA["ohs_data.quantity_samples"])
	require.NotEmpty(t, beforeA["ohs_sync.raw_objects"])
	require.Equal(t, 1, beforeA["ohs_sync.connections"])
	require.NotEmpty(t, e.rawFiles(t, a.id))

	rec := e.requestDelete(t, a.id)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var resp struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	jobID, err := uuid.Parse(resp.JobID)
	require.NoError(t, err)

	// At once: the user is deleting, its tokens are dead, an upload is refused.
	got, err := e.store.GetUser(ctx, e.tenant.ID, a.id)
	require.NoError(t, err)
	assert.Equal(t, "deleting", got.Status)
	_, err = e.store.GetActiveUser(ctx, e.tenant.ID, a.id)
	assert.ErrorIs(t, err, tenant.ErrNotFound)
	assert.Equal(t, http.StatusUnauthorized, e.post(t, a.token, gz(t)).Code)

	// Asking twice returns the same job.
	rec2 := e.requestDelete(t, a.id)
	require.Equal(t, http.StatusAccepted, rec2.Code)
	assert.Contains(t, rec2.Body.String(), resp.JobID)

	rev := &fakeRevoker{err: errors.New("provider is down")}
	n, err := e.worker(rev).Pass(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []uuid.UUID{a.id}, rev.calls, "revoke is attempted, its failure does not block")

	assert.Empty(t, e.rowsFor(t, a.id), "no row of the user is left in any ohs_sync or ohs_data table")
	assert.Empty(t, e.rawFiles(t, a.id))
	_, err = e.store.GetUser(ctx, e.tenant.ID, a.id)
	assert.ErrorIs(t, err, tenant.ErrNotFound)
	var tokens int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.user_tokens WHERE user_id = $1`, a.id).Scan(&tokens))
	assert.Zero(t, tokens)
	var status string
	var finished *time.Time
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT status, finished_at FROM ohs_core.deletion_jobs WHERE id = $1`, jobID).Scan(&status, &finished))
	assert.Equal(t, "done", status)
	assert.NotNil(t, finished)

	// The other user is untouched.
	assert.Equal(t, beforeB, e.rowsFor(t, b.id))
	assert.NotEmpty(t, e.rawFiles(t, b.id))
	_, err = e.store.GetActiveUser(ctx, e.tenant.ID, b.id)
	assert.NoError(t, err)

	// Nothing left to do, and a second pass is a no-op.
	n, err = e.worker(rev).Pass(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)

	// The user is gone: deleting again is 404.
	assert.Equal(t, http.StatusNotFound, e.requestDelete(t, a.id).Code)
}

func TestDeleteOfUnknownOrForeignUser(t *testing.T) {
	e := newEnv(t)
	assert.Equal(t, http.StatusNotFound, e.requestDelete(t, uuid.New()).Code)

	other := tenant.New(e.pool)
	ten2, _, err := other.CreateTenant(context.Background(), "Other")
	require.NoError(t, err)
	u2, err := other.CreateUser(context.Background(), ten2.ID, "x")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, e.requestDelete(t, u2.ID).Code)
	got, err := other.GetUser(context.Background(), ten2.ID, u2.ID)
	require.NoError(t, err)
	assert.Equal(t, "active", got.Status)

	req := httptest.NewRequest(http.MethodDelete, "/v1/users/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+e.key)
	rec := httptest.NewRecorder()
	e.del.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// An ETL job claimed while the user is being deleted completes without writing.
func TestEtlJobDuringDeletionWritesNothing(t *testing.T) {
	e := newEnv(t)
	a := e.newUser(t, "a")
	rec := e.post(t, a.token, gz(t))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Equal(t, http.StatusAccepted, e.requestDelete(t, a.id).Code)
	c := e.process(t, a.id)
	assert.Empty(t, c)
	assert.Zero(t, e.rowsFor(t, a.id)["ohs_data.quantity_samples"])

	_, err := e.worker(nil).Pass(context.Background())
	require.NoError(t, err)
	assert.Empty(t, e.rowsFor(t, a.id))
	assert.Empty(t, e.rawFiles(t, a.id))
}

// An upload that got past the token check, but meets a deleting user when it
// registers, is 410 and leaves nothing behind.
func TestUploadRacingDeletionIs410(t *testing.T) {
	e := newEnv(t)
	a := e.newUser(t, "a")
	// Mark deleting behind the token check's back: the token is looked up first,
	// so run the handler with a lookup that still sees the user.
	_, err := e.pool.Exec(context.Background(), `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, a.id)
	require.NoError(t, err)
	stale := func(ctx context.Context, _ string) (auth.Principal, error) {
		return auth.Principal{TenantID: e.tenant.ID, UserID: &a.id, Scopes: []string{"upload"}}, nil
	}
	h := auth.Middleware(stale, auth.UserUpload)(collect.NewUploadHandler(collect.UploadDeps{
		Pool: e.pool, Raw: e.raw, Device: applehealth.New(),
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/apple-health", bytes.NewReader(gz(t)))
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set(collect.HeaderType, "HKQuantityTypeIdentifierHeartRate")
	req.Header.Set(collect.HeaderDay, "2026-01-10")
	req.Header.Set(collect.HeaderBatch, "batch-1")
	req.Header.Set(collect.HeaderPart, "0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusGone, rec.Code, rec.Body.String())
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.Empty(t, e.rawFiles(t, a.id), "the raw file written for the refused part is removed")
	assert.Empty(t, e.rowsFor(t, a.id))
}

// A failing job is skipped and backed off; it never blocks the other jobs of
// the pass, and is retried once its backoff is over.
func TestFailingJobDoesNotBlockOthers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	bad, good := e.newUser(t, "bad"), e.newUser(t, "good")
	e.ingest(t, bad)
	e.ingest(t, good)
	require.Equal(t, http.StatusAccepted, e.requestDelete(t, bad.id).Code) // older job first
	require.Equal(t, http.StatusAccepted, e.requestDelete(t, good.id).Code)

	failing := &failingStore{Store: e.raw, failFor: fmt.Sprintf("raw/%s/%s/", e.tenant.ID, bad.id)}
	w := deletion.NewWorker(e.pool, failing, nil)
	n, err := w.Pass(ctx)
	require.Error(t, err)
	assert.Equal(t, 1, n, "the good job completes in the same pass")
	assert.Empty(t, e.rowsFor(t, good.id))
	assert.NotEmpty(t, e.rowsFor(t, bad.id), "nothing of the failed job is half deleted")
	var status string
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT status FROM ohs_core.deletion_jobs WHERE user_id = $1`, bad.id).Scan(&status))
	assert.Equal(t, "pending", status)

	// Backed off: the next pass leaves it alone.
	failing.failFor = ""
	n, err = w.Pass(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.NotEmpty(t, e.rowsFor(t, bad.id))

	// A worker without backoff delay retries and finishes it.
	n, err = deletion.NewWorker(e.pool, failing, nil).Pass(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, e.rowsFor(t, bad.id))
}

// Two workers on one job: it is finished exactly once.
func TestConcurrentWorkersFinishAJobOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.newUser(t, "a")
	e.ingest(t, a)
	require.Equal(t, http.StatusAccepted, e.requestDelete(t, a.id).Code)

	block := make(chan struct{})
	entered := make(chan struct{})
	done := make(chan int)
	go func() {
		n, _ := e.worker(&blockingRevoker{entered: entered, block: block}).Pass(ctx)
		done <- n
	}()
	<-entered
	n2, err := e.worker(nil).Pass(ctx)
	require.NoError(t, err)
	close(block)
	n1 := <-done
	assert.Equal(t, 1, n1+n2, "one worker finishes the job, the other finds it done")
	assert.Empty(t, e.rowsFor(t, a.id))
}

type blockingRevoker struct {
	entered chan struct{}
	block   chan struct{}
}

func (b *blockingRevoker) RevokeAll(context.Context, uuid.UUID, uuid.UUID) error {
	close(b.entered)
	<-b.block
	return nil
}

// failingStore fails DeletePrefix for the prefix in failFor.
type failingStore struct {
	rawstore.Store
	failFor string
}

func (f *failingStore) DeletePrefix(ctx context.Context, p string) error {
	if f.failFor != "" && p == f.failFor {
		return errors.New("raw store unavailable")
	}
	return f.Store.DeletePrefix(ctx, p)
}

// Rows in compressed chunks are deleted too, whatever the server's
// per-transaction decompression limit.
func TestDeleteRemovesRowsInCompressedChunks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.newUser(t, "a")
	e.ingest(t, a)
	require.NotEmpty(t, e.rowsFor(t, a.id)["ohs_data.quantity_samples"])
	_, err := e.pool.Exec(ctx, `SELECT compress_chunk(c) FROM show_chunks('ohs_data.quantity_samples') c`)
	require.NoError(t, err)
	// A limit far below one compressed batch.
	_, err = e.pool.Exec(ctx, `DO $$ BEGIN EXECUTE format(
		'ALTER DATABASE %I SET timescaledb.max_tuples_decompressed_per_dml_transaction = 1', current_database()); END $$`)
	require.NoError(t, err)
	e.pool.Reset() // new connections pick up the database setting

	require.Equal(t, http.StatusAccepted, e.requestDelete(t, a.id).Code)
	n, err := e.worker(&fakeRevoker{}).Pass(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, e.rowsFor(t, a.id))
}
