// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

type env struct {
	pool    *pgxpool.Pool
	raw     rawstore.Store
	store   *tenant.Store
	handler http.Handler
	tenant  uuid.UUID
	user    uuid.UUID
	token   string
}

func newEnv(t *testing.T, tweak ...func(*collect.UploadDeps)) *env {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	raw, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)

	st := tenant.New(pool)
	ten, _, err := st.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	u, err := st.CreateUser(ctx, ten.ID, "user-1")
	require.NoError(t, err)
	tok, _, err := st.MintUserToken(ctx, ten.ID, u.ID, []string{"upload"}, time.Hour)
	require.NoError(t, err)

	deps := collect.UploadDeps{Pool: pool, Raw: raw, Device: applehealth.New()}
	for _, f := range tweak {
		f(&deps)
	}
	h := auth.Middleware(st.LookupKey, auth.UserUpload)(collect.NewUploadHandler(deps))
	return &env{pool: pool, raw: raw, store: st, handler: h, tenant: ten.ID, user: u.ID, token: tok}
}

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, err := w.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return b.Bytes()
}

func fixtureBody(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../connectors/applehealth/testdata/ios_upload_lines.ndjson")
	require.NoError(t, err)
	return gz(t, string(raw))
}

func headers(typ, day, batch, part string) map[string]string {
	return map[string]string{
		collect.HeaderType: typ, collect.HeaderDay: day, collect.HeaderBatch: batch, collect.HeaderPart: part,
	}
}

func stdHeaders() map[string]string {
	return headers("HKQuantityTypeIdentifierHeartRate", "2026-01-10", "batch-1", "0")
}

func (e *env) post(t *testing.T, token string, hdr map[string]string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/apple-health", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/gzip")
	for k, v := range hdr {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) collect.UploadResponse {
	t.Helper()
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var r collect.UploadResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &r))
	return r
}

func problem(t *testing.T, rec *httptest.ResponseRecorder, status int) string {
	t.Helper()
	require.Equal(t, status, rec.Code, rec.Body.String())
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	var p struct {
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	require.Equal(t, status, p.Status)
	return p.Detail
}

func (e *env) files(t *testing.T) []string {
	t.Helper()
	keys, err := e.raw.List(context.Background(), "raw/")
	require.NoError(t, err)
	return keys
}

func (e *env) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ohs_sync.`+table+` WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user).Scan(&n))
	return n
}

func TestUploadStoresRegistersAndQueues(t *testing.T) {
	e := newEnv(t)
	body := fixtureBody(t)

	rec := e.post(t, e.token, stdHeaders(), body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decode(t, rec)
	wantKey := "raw/" + e.tenant.String() + "/" + e.user.String() + "/apple_health/2026-01-10/batch-1_0.ndjson.gz"
	assert.Equal(t, wantKey, resp.Key)
	assert.False(t, resp.Duplicate)
	assert.Equal(t, []string{"deletion_lines"}, resp.Features, "the answer advertises the deletion line")

	// Stored verbatim.
	rc, err := e.raw.Get(context.Background(), wantKey)
	require.NoError(t, err)
	var stored bytes.Buffer
	_, err = stored.ReadFrom(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, body, stored.Bytes())

	sum := sha256.Sum256(body)
	var (
		provider, hash, batch, dataType string
		size                            int64
		part                            int
	)
	require.NoError(t, e.pool.QueryRow(context.Background(), `
		SELECT provider, content_hash, size_bytes, batch, part, data_type
		FROM ohs_sync.raw_objects WHERE key = $1`, wantKey).Scan(&provider, &hash, &size, &batch, &part, &dataType))
	assert.Equal(t, "apple_health", provider)
	assert.Equal(t, hex.EncodeToString(sum[:]), hash, "content_hash is the SHA-256 of the gzip bytes")
	assert.Equal(t, int64(len(body)), size)
	assert.Equal(t, "batch-1", batch)
	assert.Equal(t, 0, part)
	assert.Equal(t, "HKQuantityTypeIdentifierHeartRate", dataType)

	var tier, status, jobProvider string
	require.NoError(t, e.pool.QueryRow(context.Background(), `
		SELECT tier, status, provider FROM ohs_sync.etl_jobs WHERE key = $1`, wantKey).Scan(&tier, &status, &jobProvider))
	assert.Equal(t, "high_freq", tier)
	assert.Equal(t, "pending", status)
	assert.Equal(t, "apple_health", jobProvider)
}

func TestUploadDuplicatePart(t *testing.T) {
	e := newEnv(t)
	body := fixtureBody(t)

	first := e.post(t, e.token, stdHeaders(), body)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	assert.False(t, decode(t, first).Duplicate)

	second := e.post(t, e.token, stdHeaders(), body)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	r2 := decode(t, second)
	assert.True(t, r2.Duplicate)
	assert.Equal(t, decode(t, first).Key, r2.Key)

	assert.Len(t, e.files(t), 1, "one raw file")
	assert.Equal(t, 1, e.count(t, "raw_objects"))
	assert.Equal(t, 1, e.count(t, "etl_jobs"), "one job")
}

// The same bytes re-sent under another batch (a client that restarted its
// walk) are a duplicate of the object already holding them.
func TestUploadSameBodyOtherHeadersIsDuplicate(t *testing.T) {
	e := newEnv(t)
	body := fixtureBody(t)

	first := e.post(t, e.token, stdHeaders(), body)
	require.Equal(t, http.StatusOK, first.Code)
	key := decode(t, first).Key

	again := e.post(t, e.token, headers("HKQuantityTypeIdentifierHeartRate", "2026-01-10", "batch-2", "3"), body)
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	r := decode(t, again)
	assert.True(t, r.Duplicate)
	assert.Equal(t, key, r.Key, "answers with the key that holds the bytes")

	assert.Len(t, e.files(t), 1, "no new file")
	assert.Equal(t, 1, e.count(t, "etl_jobs"))
}

// Other bytes under a key already taken are refused: a raw file is never
// rewritten.
func TestUploadSameKeyOtherBodyConflicts(t *testing.T) {
	e := newEnv(t)
	first := fixtureBody(t)
	require.Equal(t, http.StatusOK, e.post(t, e.token, stdHeaders(), first).Code)

	other := gz(t, `{"quantityType":"HKQuantityTypeIdentifierHeartRate","value":61,"unit":"count/min","startDate":"2026-01-10T10:00:00Z"}`+"\n")
	rec := e.post(t, e.token, stdHeaders(), other)
	assert.Equal(t, "(batch, part) already used for this day with different content; batch and part must be unique across types",
		problem(t, rec, http.StatusConflict))

	keys := e.files(t)
	require.Len(t, keys, 1)
	rc, err := e.raw.Get(context.Background(), keys[0])
	require.NoError(t, err)
	var stored bytes.Buffer
	_, _ = stored.ReadFrom(rc)
	_ = rc.Close()
	assert.Equal(t, first, stored.Bytes(), "the stored file is untouched")
	assert.Equal(t, 1, e.count(t, "etl_jobs"))
}

// R10: a file already at the key with no registration (an earlier attempt
// whose transaction failed) is confirmed by reading it back, then registered.
func TestUploadFileWithoutRowIsRegistered(t *testing.T) {
	e := newEnv(t)
	body := fixtureBody(t)
	key := rawstore.Key(e.tenant, e.user, "apple_health", time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC), "batch-1", 0)
	require.NoError(t, e.raw.Put(context.Background(), key, bytes.NewReader(body)))

	rec := e.post(t, e.token, stdHeaders(), body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, key, decode(t, rec).Key)
	assert.Equal(t, 1, e.count(t, "raw_objects"))
	assert.Equal(t, 1, e.count(t, "etl_jobs"))
}

// A file at the key holding other bytes, with no registration, is a conflict.
func TestUploadUnregisteredOtherFileConflicts(t *testing.T) {
	e := newEnv(t)
	key := rawstore.Key(e.tenant, e.user, "apple_health", time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC), "batch-1", 0)
	require.NoError(t, e.raw.Put(context.Background(), key, bytes.NewReader(gz(t, "{}\n"))))

	problem(t, e.post(t, e.token, stdHeaders(), fixtureBody(t)), http.StatusConflict)
	assert.Equal(t, 0, e.count(t, "raw_objects"))
	assert.Equal(t, 0, e.count(t, "etl_jobs"))
}

func TestUploadBadGzip(t *testing.T) {
	e := newEnv(t)
	full := fixtureBody(t)
	for name, body := range map[string][]byte{
		"truncated gzip": full[:len(full)/2],
		"plain text":     []byte("{\"quantityType\":\"HKQuantityTypeIdentifierHeartRate\"}\n"),
		"bad crc":        append(append([]byte{}, full[:len(full)-8]...), 0, 0, 0, 0, 0, 0, 0, 0),
	} {
		t.Run(name, func(t *testing.T) {
			problem(t, e.post(t, e.token, stdHeaders(), body), http.StatusBadRequest)
		})
	}
	assert.Empty(t, e.files(t), "no file")
	assert.Equal(t, 0, e.count(t, "raw_objects"))
	assert.Equal(t, 0, e.count(t, "etl_jobs"), "no job")
}

func TestUploadBadLine(t *testing.T) {
	e := newEnv(t)
	for name, body := range map[string]string{
		"not json": "{\"a\":1}\nthis is not json\n",
		"no line":  "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			detail := problem(t, e.post(t, e.token, stdHeaders(), gz(t, body)), http.StatusBadRequest)
			assert.NotContains(t, detail, "this is not json", "the problem never quotes the data")
		})
	}
	assert.Empty(t, e.files(t))
	assert.Equal(t, 0, e.count(t, "etl_jobs"))
}

func TestUploadEmptyBody(t *testing.T) {
	e := newEnv(t)
	problem(t, e.post(t, e.token, stdHeaders(), nil), http.StatusBadRequest)
}

func TestUploadTooLarge(t *testing.T) {
	e := newEnv(t)
	body := make([]byte, collect.MaxUploadBytes+1)
	problem(t, e.post(t, e.token, stdHeaders(), body), http.StatusRequestEntityTooLarge)
	assert.Empty(t, e.files(t))
	assert.Equal(t, 0, e.count(t, "etl_jobs"))
}

// A part that expands past the cap is refused before anything is stored: by
// default 64 MiB, which a gzip bomb of blank lines (about 65 KB of gzip)
// reaches in well under a second. A lower cap applies as set.
func TestUploadDecompressionCap(t *testing.T) {
	e := newEnv(t)
	bomb := gz(t, strings.Repeat("\n", int(collect.DefaultMaxDecompressedBytes)+1))
	start := time.Now()
	detail := problem(t, e.post(t, e.token, stdHeaders(), bomb), http.StatusRequestEntityTooLarge)
	assert.Contains(t, detail, "64 MiB")
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Empty(t, e.files(t))
	assert.Equal(t, 0, e.count(t, "etl_jobs"))

	small := newEnv(t, func(d *collect.UploadDeps) { d.MaxDecompressedBytes = 1 << 10 })
	big := strings.Repeat(`{"type":"HKQuantityTypeIdentifierHeartRate","value":60,"unit":"count/min","startDate":"2026-01-10T08:00:00Z","endDate":"2026-01-10T08:00:00Z"}`+"\n", 20)
	problem(t, small.post(t, small.token, stdHeaders(), gz(t, big)), http.StatusRequestEntityTooLarge)
	assert.Equal(t, http.StatusOK, e.post(t, e.token, stdHeaders(), fixtureBody(t)).Code, "the default cap takes a real part")
}

func TestUploadMissingHeader(t *testing.T) {
	e := newEnv(t)
	body := fixtureBody(t)
	for _, h := range []string{collect.HeaderType, collect.HeaderDay, collect.HeaderBatch, collect.HeaderPart} {
		t.Run(h, func(t *testing.T) {
			hdr := stdHeaders()
			hdr[h] = ""
			detail := problem(t, e.post(t, e.token, hdr, body), http.StatusBadRequest)
			assert.Contains(t, detail, h, "the problem names the header")
		})
	}
	for name, hdr := range map[string]map[string]string{
		"day not a date":    headers("HKQuantityTypeIdentifierHeartRate", "2026-02-30", "b", "0"),
		"day with time":     headers("HKQuantityTypeIdentifierHeartRate", "2026-01-10T00:00:00Z", "b", "0"),
		"type with slash":   headers("a/b", "2026-01-10", "b", "0"),
		"batch with dot":    headers("HKQuantityTypeIdentifierHeartRate", "2026-01-10", "..", "0"),
		"negative part":     headers("HKQuantityTypeIdentifierHeartRate", "2026-01-10", "b", "-1"),
		"part with a sign":  headers("HKQuantityTypeIdentifierHeartRate", "2026-01-10", "b", "+1"),
		"part not a number": headers("HKQuantityTypeIdentifierHeartRate", "2026-01-10", "b", "one"),
	} {
		t.Run(name, func(t *testing.T) {
			problem(t, e.post(t, e.token, hdr, body), http.StatusBadRequest)
		})
	}
	assert.Empty(t, e.files(t))
}

func TestUploadWrongScope(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	readOnly, _, err := e.store.MintUserToken(ctx, e.tenant, e.user, []string{"read", "connect"}, time.Hour)
	require.NoError(t, err)
	problem(t, e.post(t, readOnly, stdHeaders(), fixtureBody(t)), http.StatusForbidden)

	// A tenant API key is not a user token either.
	_, apiKey, err := e.store.CreateTenant(ctx, "Other")
	require.NoError(t, err)
	problem(t, e.post(t, apiKey, stdHeaders(), fixtureBody(t)), http.StatusForbidden)

	problem(t, e.post(t, "ohs_ut_nope", stdHeaders(), fixtureBody(t)), http.StatusUnauthorized)
	assert.Empty(t, e.files(t))
}

// Two users may upload byte-identical parts: dedup is per user.
func TestUploadSameBodyOtherUserIsStored(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	body := fixtureBody(t)
	require.Equal(t, http.StatusOK, e.post(t, e.token, stdHeaders(), body).Code)

	u2, err := e.store.CreateUser(ctx, e.tenant, "user-2")
	require.NoError(t, err)
	tok2, _, err := e.store.MintUserToken(ctx, e.tenant, u2.ID, []string{"upload"}, time.Hour)
	require.NoError(t, err)
	rec := e.post(t, tok2, stdHeaders(), body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	r := decode(t, rec)
	assert.False(t, r.Duplicate)
	assert.True(t, strings.Contains(r.Key, u2.ID.String()))
	assert.Len(t, e.files(t), 2)
}

// The same part sent twice at once: both succeed, exactly one is the
// duplicate, and one file, one row and one job result.
func TestUploadConcurrentDuplicate(t *testing.T) {
	e := newEnv(t)
	body := fixtureBody(t)

	var wg sync.WaitGroup
	recs := make([]*httptest.ResponseRecorder, 2)
	start := make(chan struct{})
	for i := range recs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			recs[i] = e.post(t, e.token, stdHeaders(), body)
		}(i)
	}
	close(start)
	wg.Wait()

	dups := 0
	for _, rec := range recs {
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		if decode(t, rec).Duplicate {
			dups++
		}
	}
	assert.Equal(t, 1, dups, "exactly one answer is the duplicate")
	assert.Len(t, e.files(t), 1)
	assert.Equal(t, 1, e.count(t, "raw_objects"))
	assert.Equal(t, 1, e.count(t, "etl_jobs"))
}
