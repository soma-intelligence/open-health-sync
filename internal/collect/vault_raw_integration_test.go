// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// sealEnv is a user created with keys by a process that opens them, and a
// Vault of a collect-only process (CanOpen false, no key provider) to store
// with.
type sealEnv struct {
	pool         *pgxpool.Pool
	raw          rawstore.Store
	tenant, user uuid.UUID
	opener       *vault.Vault
	sealer       *vault.Vault
}

func newSealEnv(t *testing.T) *sealEnv {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	raw, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)
	p, err := vault.OpenProvider(ctx, config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{7}, 32)})
	require.NoError(t, err)
	opener := vault.New(pool, p, vault.Options{CanOpen: true})
	st := tenant.New(pool).WithVault(opener)
	ten, _, err := st.CreateTenant(ctx, "A")
	require.NoError(t, err)
	u, err := st.CreateUser(ctx, ten.ID, "u")
	require.NoError(t, err)
	return &sealEnv{pool: pool, raw: raw, tenant: ten.ID, user: u.ID, opener: opener,
		sealer: vault.New(pool, nil, vault.Options{})}
}

func (e *sealEnv) uploader(rs rawstore.Store, v *vault.Vault) *uploadHandler {
	return &uploadHandler{d: UploadDeps{Pool: e.pool, Raw: rs, Device: uploadOnly{"apple_health"}, Vault: v}, provider: "apple_health"}
}

func (e *sealEnv) req() uploadReq {
	return uploadReq{tenant: e.tenant, user: e.user, dataType: "steps",
		day: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), batch: "b1", part: 0}
}

func (e *sealEnv) key() string {
	q := e.req()
	return rawstore.Key(q.tenant, q.user, "apple_health", q.day, q.batch, q.part)
}

func (e *sealEnv) stored(t *testing.T, key string) []byte {
	t.Helper()
	rc, err := e.raw.Get(context.Background(), key)
	require.NoError(t, err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return b
}

func (e *sealEnv) files(t *testing.T) []string {
	t.Helper()
	keys, err := e.raw.List(context.Background(), "raw/")
	require.NoError(t, err)
	return keys
}

func (e *sealEnv) rows(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_sync.raw_objects`).Scan(&n))
	return n
}

func gzBody(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, err := w.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return b.Bytes()
}

const sealLines = `{"quantityType":"HKQuantityTypeIdentifierStepCount","value":4321,"unit":"count"}` + "\n"

// An upload stored by a collect-only process lands sealed: the magic, the
// digest as content_hash, none of its bytes, and it opens to the gzip bytes
// for a process that holds the keys.
func TestUploadStoresSealedObject(t *testing.T) {
	ctx := context.Background()
	e := newSealEnv(t)
	body := gzBody(t, sealLines)

	resp, status, err := e.uploader(e.raw, e.sealer).store(ctx, e.req(), body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, e.key(), resp.Key)

	stored := e.stored(t, e.key())
	assert.Equal(t, []byte("OHSV"), stored[:4])
	assert.False(t, bytes.Contains(stored, body), "the gzip bytes are not in the object")
	assert.False(t, bytes.Contains(stored, []byte("StepCount")), "nor any of the plaintext")
	assert.False(t, bytes.Contains(stored, []byte("4321")))

	pub, _, err := e.sealer.PublicKey(ctx, e.pool, e.tenant, e.user)
	require.NoError(t, err)
	want := vault.RawDigest(pub, body)
	var hash string
	var size int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT content_hash, size_bytes FROM ohs_sync.raw_objects WHERE key = $1`,
		e.key()).Scan(&hash, &size))
	assert.Equal(t, hex.EncodeToString(want[:]), hash, "content_hash is the RawDigest")
	assert.Equal(t, len(stored), size, "size_bytes is what the raw store holds")

	uv, err := e.opener.ForUser(ctx, e.tenant, e.user)
	require.NoError(t, err)
	h, plain, err := vault.OpenObject(bytes.NewReader(stored), uv.Keys)
	require.NoError(t, err)
	assert.Equal(t, want, h.Digest)
	got, err := io.ReadAll(plain)
	require.NoError(t, err)
	assert.Equal(t, body, got)
}

// A collect-only pull stores sealed objects, and a re-pull of the same bytes
// stores nothing new: the digest finds the registered object although the
// sealed bytes differ.
func TestPullSinkStoresSealedObjectOnce(t *testing.T) {
	ctx := context.Background()
	e := newSealEnv(t)
	d := Deps{Pool: e.pool, Raw: e.raw, Vault: e.sealer}
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	lines := [][]byte{[]byte(`{"steps":4321}`), []byte(`{"steps":1234}`)}

	first := newPullSink(d, queue.PullJob{ID: 1, TenantID: e.tenant, UserID: e.user, Provider: "p", ClaimedAt: time.Now()})
	require.NoError(t, first.Emit(ctx, "steps", day, lines))
	again := newPullSink(d, queue.PullJob{ID: 2, TenantID: e.tenant, UserID: e.user, Provider: "p", ClaimedAt: time.Now()})
	require.NoError(t, again.Emit(ctx, "steps", day, lines))

	assert.Equal(t, 1, first.stored)
	assert.Equal(t, 1, again.skipped)
	keys := e.files(t)
	require.Len(t, keys, 1)
	stored := e.stored(t, keys[0])
	assert.Equal(t, []byte("OHSV"), stored[:4])
	assert.False(t, bytes.Contains(stored, []byte("4321")))
	assert.Equal(t, 1, e.rows(t))
}

// cancelAfterPut cancels the request once the file is written, so its
// registration fails and the file stays unregistered.
type cancelAfterPut struct {
	rawstore.Store
	cancel context.CancelFunc
}

func (c cancelAfterPut) Put(ctx context.Context, key string, r io.Reader) error {
	if err := c.Store.Put(ctx, key, r); err != nil {
		return err
	}
	c.cancel()
	return nil
}

type storeAnswer struct {
	resp   UploadResponse
	status int
}

// failThenRetry stores body with a registration that fails, then again, then
// once more, and returns the two answers after the failure.
func (e *sealEnv) failThenRetry(t *testing.T, v *vault.Vault, body []byte) (retry, repeat storeAnswer, first []byte) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err := e.uploader(cancelAfterPut{e.raw, cancel}, v).store(ctx, e.req(), body)
	require.Error(t, err, "the registration fails")
	require.Len(t, e.files(t), 1, "the file stays")
	require.Zero(t, e.rows(t), "unregistered")
	first = e.stored(t, e.key())

	retry.resp, retry.status, err = e.uploader(e.raw, v).store(context.Background(), e.req(), body)
	require.NoError(t, err)
	repeat.resp, repeat.status, err = e.uploader(e.raw, v).store(context.Background(), e.req(), body)
	require.NoError(t, err)
	return retry, repeat, first
}

// Review focus: a re-upload after a failed registration answers as it does
// without the vault, and the first file is the one kept.
func TestSealedReuploadAfterFailedRegistration(t *testing.T) {
	body := gzBody(t, sealLines)

	legacy := newSealEnv(t)
	wantRetry, wantRepeat, _ := legacy.failThenRetry(t, nil, body)

	e := newSealEnv(t)
	retry, repeat, first := e.failThenRetry(t, e.sealer, body)
	assert.Equal(t, http.StatusOK, retry.status)
	assert.Equal(t, wantRetry.status, retry.status)
	assert.Equal(t, wantRetry.resp.Duplicate, retry.resp.Duplicate)
	assert.Equal(t, e.key(), retry.resp.Key)
	assert.Equal(t, wantRepeat.status, repeat.status)
	assert.Equal(t, wantRepeat.resp.Duplicate, repeat.resp.Duplicate)
	assert.True(t, repeat.resp.Duplicate, "the third send finds the registered object")

	assert.Equal(t, []string{e.key()}, e.files(t), "one object")
	assert.Equal(t, first, e.stored(t, e.key()), "the file of the failed attempt is kept, never rewritten")
	assert.Equal(t, 1, e.rows(t))
}

// Other bytes under a key whose file is there but unregistered still conflict
// (errOtherBytes, 409), with or without the vault.
func TestSealedOtherBytesUnderTheSameKey(t *testing.T) {
	a := gzBody(t, sealLines)
	b := gzBody(t, `{"quantityType":"HKQuantityTypeIdentifierStepCount","value":1,"unit":"count"}`+"\n")
	for name, sealed := range map[string]bool{"legacy": false, "sealed": true} {
		t.Run(name, func(t *testing.T) {
			e := newSealEnv(t)
			var v *vault.Vault
			if sealed {
				v = e.sealer
			}
			ctx, cancel := context.WithCancel(context.Background())
			_, _, err := e.uploader(cancelAfterPut{e.raw, cancel}, v).store(ctx, e.req(), a)
			require.Error(t, err)
			first := e.stored(t, e.key())

			rb, err := newRawBody(context.Background(), e.pool, v, e.tenant, e.user, b)
			require.NoError(t, err)
			_, err = putRaw(context.Background(), e.raw, e.key(), rb)
			require.ErrorIs(t, err, errOtherBytes)

			_, status, err := e.uploader(e.raw, v).store(context.Background(), e.req(), b)
			require.NoError(t, err)
			assert.Equal(t, http.StatusConflict, status)
			assert.Equal(t, first, e.stored(t, e.key()), "the stored file is untouched")
			assert.Zero(t, e.rows(t))
		})
	}
}

// With the vault on, a legacy file already under the key is still compared
// byte for byte: the same bytes are taken, other bytes conflict.
func TestSealedPutOverLegacyFile(t *testing.T) {
	ctx := context.Background()
	e := newSealEnv(t)
	body := gzBody(t, sealLines)
	require.NoError(t, e.raw.Put(ctx, e.key(), bytes.NewReader(body)))

	rb, err := newRawBody(ctx, e.pool, e.sealer, e.tenant, e.user, body)
	require.NoError(t, err)
	size, err := putRaw(ctx, e.raw, e.key(), rb)
	require.NoError(t, err)
	assert.Equal(t, len(body), size, "the size of the legacy file")

	other, err := newRawBody(ctx, e.pool, e.sealer, e.tenant, e.user, gzBody(t, "{}\n"))
	require.NoError(t, err)
	_, err = putRaw(ctx, e.raw, e.key(), other)
	require.ErrorIs(t, err, errOtherBytes)
	assert.Equal(t, body, e.stored(t, e.key()))
}

// A user with no keys gets nothing written in clear: errInactive once they
// are being deleted (their keys go first), an error before that.
func TestSealedUserWithoutKeys(t *testing.T) {
	ctx := context.Background()
	e := newSealEnv(t)
	var bare uuid.UUID
	require.NoError(t, e.pool.QueryRow(ctx, `INSERT INTO ohs_core.end_users (tenant_id) VALUES ($1) RETURNING id`,
		e.tenant).Scan(&bare))
	body := gzBody(t, sealLines)
	q := e.req()
	q.user = bare

	_, _, err := e.uploader(e.raw, e.sealer).store(ctx, q, body)
	require.ErrorIs(t, err, vault.ErrNoKeys)

	_, err = e.pool.Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, bare)
	require.NoError(t, err)
	_, _, err = e.uploader(e.raw, e.sealer).store(ctx, q, body)
	require.ErrorIs(t, err, errInactive)
	assert.Empty(t, e.files(t))
}
