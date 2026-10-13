// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package deletion_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/deletion"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// The two invented providers of the seeded rows: the target and another one.
const (
	target = "withings"
	other  = "oura"
)

func (e *env) requestProviderDelete(t *testing.T, userID uuid.UUID, provider string) *httptest.ResponseRecorder {
	t.Helper()
	known := []string{target, other, applehealth.Slug}
	mux := http.NewServeMux()
	mux.Handle("DELETE /v1/users/{id}/data/{provider}", auth.Middleware(e.store.LookupKey, auth.TenantKey)(
		deletion.NewProviderDeleteHandler(deletion.Deps{Pool: e.pool, KnownProvider: func(s string) bool {
			return slices.Contains(known, s)
		}})))
	req := httptest.NewRequest(http.MethodDelete, "/v1/users/"+userID.String()+"/data/"+provider, nil)
	req.Header.Set("Authorization", "Bearer "+e.key)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func jobOf(t *testing.T, rec *httptest.ResponseRecorder) uuid.UUID {
	t.Helper()
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var resp struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	id, err := uuid.Parse(resp.JobID)
	require.NoError(t, err)
	return id
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

func (e *env) id(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var id int64
	require.NoError(t, e.pool.QueryRow(context.Background(), sql, args...).Scan(&id))
	return id
}

// seed writes one row of every user-scoped table for the user and provider
// (invented values), plus a raw file under the provider's prefix, and returns
// the tables it wrote to. connStatus is the status of the connection row.
func (e *env) seed(t *testing.T, user uuid.UUID, provider, connStatus string) []string {
	t.Helper()
	ctx := context.Background()
	ten := e.tenant.ID
	at := time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC)
	h := func(s string) string { return fmt.Sprintf("%s-%s-%s", user, provider, s) }
	key := rawstore.Key(ten, user, provider, at, "seed", 0)
	require.NoError(t, e.raw.Put(ctx, key, strings.NewReader("{}\n")))

	e.exec(t, `INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, status, provider_user_id)
		VALUES ($1, $2, $3, $4, 'pu-invented')`, ten, user, provider, connStatus)
	e.exec(t, `INSERT INTO ohs_sync.oauth_states (state, tenant_id, user_id, provider, expires_at)
		VALUES ($4, $1, $2, $3, now() + interval '10 minutes')`, ten, user, provider, h("state"))
	e.exec(t, `INSERT INTO ohs_sync.pull_jobs (tenant_id, user_id, provider, from_time, to_time, status)
		VALUES ($1, $2, $3, $4::timestamptz, $4::timestamptz + interval '1 day', 'parked')`, ten, user, provider, at)
	e.exec(t, `INSERT INTO ohs_sync.pull_coverage (tenant_id, user_id, provider, metric, date, records_count)
		VALUES ($1, $2, $3, 'heart_rate', $4, 3)`, ten, user, provider, at)
	e.exec(t, `INSERT INTO ohs_sync.sync_log (tenant_id, user_id, provider, stage, raw_key, status)
		VALUES ($1, $2, $3, 'etl', $4, 'success')`, ten, user, provider, key)
	e.exec(t, `INSERT INTO ohs_sync.inbound_subscriptions (tenant_id, user_id, provider, topic, callback_url, status)
		VALUES ($1, $2, $3, 'topic', 'https://ohs.example.test/inbound', 'removed')`, ten, user, provider)
	e.exec(t, `INSERT INTO ohs_sync.raw_objects (tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, data_type)
		VALUES ($1, $2, $3, $4, $5, 3, 'seed', 0, 'heart_rate')`, ten, user, provider, key, h("raw"))
	e.exec(t, `INSERT INTO ohs_sync.etl_jobs (tenant_id, user_id, provider, key, status)
		VALUES ($1, $2, $3, $4, 'done')`, ten, user, provider, key)

	e.exec(t, `INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, quantity_type, value, start_date, content_hash)
		VALUES ($1, $2, $3, 'heart_rate', 61, $4, $5)`, ten, user, provider, at, h("qs"))
	e.exec(t, `INSERT INTO ohs_data.category_samples (tenant_id, user_id, provider, category_type, value_int, start_date, content_hash)
		VALUES ($1, $2, $3, 'mindful_session', 0, $4, $5)`, ten, user, provider, at, h("cs"))
	sleep := e.id(t, `INSERT INTO ohs_data.sleep_sessions (tenant_id, user_id, provider, bedtime_start, content_hash)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, ten, user, provider, at, h("sleep"))
	e.exec(t, `INSERT INTO ohs_data.sleep_phases (tenant_id, user_id, session_id, phase, start_date, duration_s, content_hash)
		VALUES ($1, $2, $3, 'deep', $4, 600, $5)`, ten, user, sleep, at, h("phase"))
	workout := e.id(t, `INSERT INTO ohs_data.workouts (tenant_id, user_id, provider, activity_type, start_date, content_hash)
		VALUES ($1, $2, $3, 'running', $4, $5) RETURNING id`, ten, user, provider, at, h("workout"))
	e.exec(t, `INSERT INTO ohs_data.route_points (tenant_id, user_id, workout_id, "timestamp", latitude, longitude, content_hash)
		VALUES ($1, $2, $3, $4, 10.5, 20.5, $5)`, ten, user, workout, at, h("route"))
	e.exec(t, `INSERT INTO ohs_data.workout_tracks (tenant_id, user_id, workout_id, length_m, point_count, started_at, ended_at)
		VALUES ($1, $2, $3, 100, 1, $4, $4)`, ten, user, workout, at)
	e.exec(t, `INSERT INTO ohs_data.nutrition_logs (tenant_id, user_id, provider, start_date, energy_kcal, content_hash)
		VALUES ($1, $2, $3, $4, 300, $5)`, ten, user, provider, at, h("meal"))
	ecg := e.id(t, `INSERT INTO ohs_data.ecg_recordings (tenant_id, user_id, provider, start_date, classification,
			symptoms_status, sampling_frequency, number_of_measurements, content_hash)
		VALUES ($1, $2, $3, $4, 1, 1, 512, 1, $5) RETURNING id`, ten, user, provider, at, h("ecg"))
	e.exec(t, `INSERT INTO ohs_data.ecg_samples (tenant_id, user_id, ecg_id, ts, time_since_sample_start, voltage_uv, content_hash)
		VALUES ($1, $2, $3, $4, 0, 12.5, $5)`, ten, user, ecg, at, h("volt"))
	e.exec(t, `INSERT INTO ohs_data.heartbeat_samples (tenant_id, user_id, provider, series_id, "timestamp", rr_ms, content_hash)
		VALUES ($1, $2, $3, 'series', $4, 900, $5)`, ten, user, provider, at, h("beat"))
	e.exec(t, `INSERT INTO ohs_data.daily_summaries (tenant_id, user_id, provider, summary_date, step_count, content_hash)
		VALUES ($1, $2, $3, $4, 4000, $5)`, ten, user, provider, at, h("daily"))
	e.exec(t, `INSERT INTO ohs_data.observations (tenant_id, user_id, provider, ts, canonical_code, value_num, content_hash)
		VALUES ($1, $2, $3, $4, 'invented_code', 1, $5)`, ten, user, provider, at, h("obs"))
	e.exec(t, `INSERT INTO ohs_data.failed_records (tenant_id, user_id, provider, raw_key, line_number, reason_code)
		VALUES ($1, $2, $3, $4, 1, 'parse')`, ten, user, provider, key)
	e.exec(t, `INSERT INTO ohs_data.deleted_records (tenant_id, user_id, provider, external_id)
		VALUES ($1, $2, $3, 'removed-1')`, ten, user, provider)

	return []string{
		"ohs_data.category_samples", "ohs_data.daily_summaries", "ohs_data.deleted_records",
		"ohs_data.ecg_recordings", "ohs_data.ecg_samples", "ohs_data.failed_records",
		"ohs_data.heartbeat_samples", "ohs_data.nutrition_logs", "ohs_data.observations",
		"ohs_data.quantity_samples", "ohs_data.route_points", "ohs_data.sleep_phases",
		"ohs_data.sleep_sessions", "ohs_data.workout_tracks", "ohs_data.workouts",
		"ohs_sync.connections", "ohs_sync.etl_jobs", "ohs_sync.inbound_subscriptions",
		"ohs_sync.oauth_states", "ohs_sync.pull_coverage", "ohs_sync.pull_jobs",
		"ohs_sync.raw_objects", "ohs_sync.sync_log",
	}
}

// providerRows counts the rows of (user, provider) in every user table; a
// child table's rows are counted through their parent.
func (e *env) providerRows(t *testing.T, user uuid.UUID, provider string) map[string]int {
	t.Helper()
	children := deletion.ChildTables()
	out := map[string]int{}
	for _, tbl := range e.userTables(t) {
		q := fmt.Sprintf(`SELECT count(*) FROM %s WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`, tbl)
		if c, ok := children[tbl]; ok {
			q = fmt.Sprintf(`SELECT count(*) FROM %s c WHERE c.tenant_id = $1 AND c.user_id = $2
				AND EXISTS (SELECT 1 FROM %s p WHERE p.id = c.%s AND p.provider = $3)`, tbl, c[1], c[0])
		}
		var n int
		require.NoError(t, e.pool.QueryRow(context.Background(), q, e.tenant.ID, user, provider).Scan(&n))
		if n > 0 {
			out[tbl] = n
		}
	}
	return out
}

func (e *env) providerFiles(t *testing.T, user uuid.UUID, provider string) []string {
	t.Helper()
	keys, err := e.raw.List(context.Background(), fmt.Sprintf("raw/%s/%s/%s/", e.tenant.ID, user, provider))
	require.NoError(t, err)
	return keys
}

func (e *env) addWebhookEndpoint(t *testing.T) {
	t.Helper()
	e.exec(t, `INSERT INTO ohs_core.webhook_endpoints (tenant_id, url, secret_sealed)
		VALUES ($1, 'https://hooks.example.test/ohs', '\x00')`, e.tenant.ID)
}

func (e *env) jobStatus(t *testing.T, id uuid.UUID) (string, *string) {
	t.Helper()
	var status string
	var provider *string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT status, provider FROM ohs_core.deletion_jobs WHERE id = $1`, id).Scan(&status, &provider))
	return status, provider
}

// Rows of every table for two users and two providers: only the target user's
// rows of the target provider go, raw files included.
func TestProviderDeletionRemovesOnlyThatUserAndProvider(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.newUser(t, "a"), e.newUser(t, "b")
	e.addWebhookEndpoint(t)
	seeded := e.seed(t, a.id, target, "disconnected")
	e.seed(t, a.id, other, "active")
	e.seed(t, b.id, target, "active")
	e.seed(t, b.id, other, "active")

	// The seed covers every user-scoped table: a new table fails here until
	// it is seeded, and so proven erased.
	assert.ElementsMatch(t, e.userTables(t), seeded,
		"seed() must write every table with a user_id column; add the new table to it")
	for _, tbl := range seeded {
		require.Equal(t, 1, e.providerRows(t, a.id, target)[tbl], tbl)
	}
	keepA, keepB, keepB2 := e.providerRows(t, a.id, other), e.providerRows(t, b.id, target), e.providerRows(t, b.id, other)
	require.Len(t, keepA, len(seeded))

	job := jobOf(t, e.requestProviderDelete(t, a.id, target))
	assert.Equal(t, job, jobOf(t, e.requestProviderDelete(t, a.id, target)), "asking again returns the same job")
	status, provider := e.jobStatus(t, job)
	assert.Equal(t, "pending", status)
	require.NotNil(t, provider)
	assert.Equal(t, target, *provider)

	n, err := e.worker(nil).Pass(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	assert.Empty(t, e.providerRows(t, a.id, target), "no row of the user and provider is left")
	assert.Empty(t, e.providerFiles(t, a.id, target))
	assert.Equal(t, keepA, e.providerRows(t, a.id, other), "the user's other provider is untouched")
	assert.Equal(t, keepB, e.providerRows(t, b.id, target), "another user's rows of the provider are untouched")
	assert.Equal(t, keepB2, e.providerRows(t, b.id, other))
	assert.NotEmpty(t, e.providerFiles(t, a.id, other))
	assert.NotEmpty(t, e.providerFiles(t, b.id, target))
	_, err = e.store.GetActiveUser(ctx, e.tenant.ID, a.id)
	assert.NoError(t, err, "the user stays")

	status, _ = e.jobStatus(t, job)
	assert.Equal(t, "done", status)
	var payload []byte
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT payload FROM ohs_core.webhook_deliveries
		WHERE tenant_id = $1 AND event_type = 'data.erased'`, e.tenant.ID).Scan(&payload))
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(payload, &body))
	assert.Equal(t, target, body.Data["provider"])
	assert.Equal(t, job.String(), body.Data["job_id"])
	assert.Equal(t, a.id.String(), body.Data["user_id"])
	assert.Equal(t, "a", body.Data["external_ref"])

	// Nothing left to do.
	n, err = e.worker(nil).Pass(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestProviderDeletionRefusals(t *testing.T) {
	e := newEnv(t)
	a := e.newUser(t, "a")
	e.exec(t, `INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, status) VALUES ($1, $2, $3, 'active')`,
		e.tenant.ID, a.id, target)
	e.exec(t, `INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, status) VALUES ($1, $2, $3, 'needs_reauth')`,
		e.tenant.ID, a.id, other)

	rec := e.requestProviderDelete(t, a.id, target)
	assert.Equal(t, http.StatusConflict, rec.Code, "a live connection is disconnected with delete_data=true instead")
	assert.Contains(t, rec.Body.String(), "delete_data=true")
	assert.Equal(t, http.StatusConflict, e.requestProviderDelete(t, a.id, other).Code)
	assert.Equal(t, http.StatusNotFound, e.requestProviderDelete(t, a.id, "not_a_provider").Code)
	assert.Equal(t, http.StatusNotFound, e.requestProviderDelete(t, a.id, "Not.Valid").Code)
	assert.Equal(t, http.StatusNotFound, e.requestProviderDelete(t, uuid.New(), applehealth.Slug).Code)
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_core.deletion_jobs`).Scan(&n))
	assert.Zero(t, n)

	// A device provider has no connection: accepted. The user's own deletion
	// is a separate job.
	job := jobOf(t, e.requestProviderDelete(t, a.id, applehealth.Slug))
	userJob := jobOf(t, e.requestDelete(t, a.id))
	assert.NotEqual(t, job, userJob)
	assert.Equal(t, http.StatusNotFound, e.requestProviderDelete(t, a.id, applehealth.Slug).Code, "a user being deleted is 404")

	// Both jobs finish; the user job erases everything anyway.
	_, err := e.worker(nil).Pass(context.Background())
	require.NoError(t, err)
	for _, id := range []uuid.UUID{job, userJob} {
		status, _ := e.jobStatus(t, id)
		assert.Equal(t, "done", status)
	}
}

// Real uploads through the ETL: after the erasure, processing the user's raw
// objects again (the jobs left, or the old key) brings nothing back, and an
// upload while the job is pending is refused and leaves nothing.
func TestProviderDeletionIsNotUndoneByReprocessing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.newUser(t, "a"), e.newUser(t, "b")
	e.ingest(t, a)
	e.ingest(t, b)
	e.seed(t, a.id, other, "active")
	keepOther := e.providerRows(t, a.id, other)
	beforeB := e.rowsFor(t, b.id)
	require.NotEmpty(t, e.providerRows(t, a.id, applehealth.Slug)["ohs_data.quantity_samples"])
	oldKeys := e.providerFiles(t, a.id, applehealth.Slug)
	require.NotEmpty(t, oldKeys)

	job := jobOf(t, e.requestProviderDelete(t, a.id, applehealth.Slug))

	// While pending, a new upload (other bytes) is refused and its file
	// removed.
	var fresh bytes.Buffer
	zw := gzip.NewWriter(&fresh)
	_, err := zw.Write([]byte(`{"type":"HKQuantityTypeIdentifierHeartRate","value":70,"unit":"count/min","startDate":"2026-01-11T08:00:00Z","endDate":"2026-01-11T08:00:00Z"}` + "\n"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/apple-health", &fresh)
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-OHS-Type", "HKQuantityTypeIdentifierHeartRate")
	req.Header.Set("X-OHS-Day", "2026-01-11")
	req.Header.Set("X-OHS-Batch", "batch-2")
	req.Header.Set("X-OHS-Part", "0")
	rec := httptest.NewRecorder()
	e.upload.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, oldKeys, e.providerFiles(t, a.id, applehealth.Slug), "the refused part left no file")

	n, err := e.worker(nil).Pass(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	status, _ := e.jobStatus(t, job)
	assert.Equal(t, "done", status)

	assert.Empty(t, e.providerRows(t, a.id, applehealth.Slug))
	assert.Empty(t, e.providerFiles(t, a.id, applehealth.Slug))
	assert.Equal(t, keepOther, e.providerRows(t, a.id, other))
	assert.Equal(t, beforeB, e.rowsFor(t, b.id))

	// Reprocess: no ETL job is left for the provider, and an old key is
	// not registered any more.
	var left int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_sync.etl_jobs
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`, e.tenant.ID, a.id, applehealth.Slug).Scan(&left))
	assert.Zero(t, left)
	for _, k := range oldKeys {
		_, err := etl.ProcessWith(ctx, e.pool, e.raw, queue.ETLJob{
			TenantID: e.tenant.ID, UserID: a.id, Provider: applehealth.Slug, Key: k, Tier: queue.TierHighFreq,
		}, etl.Options{Lookup: func(slug string) (connector.Connector, bool) {
			return applehealth.New(), slug == applehealth.Slug
		}})
		assert.Error(t, err)
	}
	assert.Empty(t, e.providerRows(t, a.id, applehealth.Slug))

	// Once done, the user can upload again: new data is kept.
	e.ingest(t, a)
	assert.NotEmpty(t, e.providerRows(t, a.id, applehealth.Slug)["ohs_data.quantity_samples"])
}

// readThenErase returns the object, but erases the provider's data between
// the read and the ETL's write: the race of an ETL job in flight.
type readThenErase struct {
	rawstore.Store
	erase func()
}

func (s readThenErase) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := s.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return nil, err
	}
	s.erase()
	return io.NopCloser(bytes.NewReader(b)), nil
}

func TestEtlJobInFlightDuringProviderErasureWritesNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.newUser(t, "a")
	rec := e.post(t, a.token, gz(t))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows, err := e.pool.Query(ctx, `SELECT key FROM ohs_sync.etl_jobs WHERE user_id = $1`, a.id)
	require.NoError(t, err)
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	require.Len(t, keys, 1)

	store := readThenErase{Store: e.raw, erase: func() {
		jobOf(t, e.requestProviderDelete(t, a.id, applehealth.Slug))
		n, err := e.worker(nil).Pass(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, n)
	}}
	c, err := etl.ProcessWith(ctx, e.pool, store, queue.ETLJob{
		TenantID: e.tenant.ID, UserID: a.id, Provider: applehealth.Slug, Key: keys[0], Tier: queue.TierHighFreq,
	}, etl.Options{Lookup: func(slug string) (connector.Connector, bool) {
		return applehealth.New(), slug == applehealth.Slug
	}})
	require.NoError(t, err)
	assert.Empty(t, c)
	assert.Empty(t, e.rowsFor(t, a.id), "the object read before the erasure is not written after it")
}

// A pull of the provider still running when the erasure is due: the job
// waits for it, then runs.
func TestProviderDeletionWaitsForRunningPull(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.newUser(t, "a")
	e.seed(t, a.id, target, "disconnected")
	pull := e.id(t, `INSERT INTO ohs_sync.pull_jobs (tenant_id, user_id, provider, from_time, to_time, status, claimed_at)
		VALUES ($1, $2, $3, now() - interval '1 day', now(), 'running', now()) RETURNING id`, e.tenant.ID, a.id, target)
	job := jobOf(t, e.requestProviderDelete(t, a.id, target))

	n, err := e.worker(nil).Pass(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
	status, _ := e.jobStatus(t, job)
	assert.Equal(t, "pending", status)
	assert.NotEmpty(t, e.providerFiles(t, a.id, target), "nothing is deleted while the pull runs")

	e.exec(t, `UPDATE ohs_sync.pull_jobs SET status = 'done', claimed_at = NULL WHERE id = $1`, pull)
	n, err = e.worker(nil).Pass(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, e.providerRows(t, a.id, target))
}

// Rows in compressed chunks are erased too, whatever the server's
// per-transaction decompression limit: the provider is not a segment column,
// so batches are decompressed.
func TestProviderDeletionRemovesRowsInCompressedChunks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.newUser(t, "a"), e.newUser(t, "b")
	e.ingest(t, a)
	e.ingest(t, b)
	e.seed(t, a.id, other, "active")
	require.NotEmpty(t, e.providerRows(t, a.id, applehealth.Slug)["ohs_data.quantity_samples"])
	keepB := e.rowsFor(t, b.id)
	_, err := e.pool.Exec(ctx, `SELECT compress_chunk(c) FROM show_chunks('ohs_data.quantity_samples') c`)
	require.NoError(t, err)
	_, err = e.pool.Exec(ctx, `DO $$ BEGIN EXECUTE format(
		'ALTER DATABASE %I SET timescaledb.max_tuples_decompressed_per_dml_transaction = 1', current_database()); END $$`)
	require.NoError(t, err)
	e.pool.Reset()

	jobOf(t, e.requestProviderDelete(t, a.id, applehealth.Slug))
	n, err := e.worker(nil).Pass(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, e.providerRows(t, a.id, applehealth.Slug))
	assert.Equal(t, 1, e.providerRows(t, a.id, other)["ohs_data.quantity_samples"], "the other provider's sample in the same compressed batch stays")
	assert.Equal(t, keepB, e.rowsFor(t, b.id))
}
