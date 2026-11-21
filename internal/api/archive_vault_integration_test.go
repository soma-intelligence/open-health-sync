// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// An upload is stored sealed; the archive gives the user back the payload
// as it was sent, and no sealed byte. A user without keys gets no archive.
func TestUserArchiveOpensSealedRaw(t *testing.T) {
	ctx := context.Background()
	var (
		raw rawstore.Store
		v   *vault.Vault
	)
	e := newEnv(t, func(d *api.Deps) {
		p, err := vault.OpenProvider(ctx, config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{5}, 32)})
		require.NoError(t, err)
		v = vault.New(d.Pool, p, vault.Options{CanOpen: true})
		d.Vault = v
		raw = d.Raw
	})
	u := e.user(e.tenantA, "a-1")
	tx, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, v.EnsureUser(ctx, tx, e.tenantA, u))
	require.NoError(t, tx.Commit(ctx))

	lines, err := os.ReadFile("../../connectors/applehealth/testdata/ios_upload_lines.ndjson")
	require.NoError(t, err)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, err = zw.Write(lines)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	body := gz.Bytes()

	tok, _, err := e.store.MintUserToken(ctx, e.tenantA, u, []string{"upload"}, time.Hour)
	require.NoError(t, err)
	r := e.do("POST", "/v1/ingest/apple-health", tok, body, "Content-Type", "application/gzip",
		collect.HeaderType, "HKQuantityTypeIdentifierHeartRate", collect.HeaderDay, "2026-01-10",
		collect.HeaderBatch, "batch-1", collect.HeaderPart, "0")
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var up collect.UploadResponse
	r.json(t, &up)
	stored, err := raw.Get(ctx, up.Key)
	require.NoError(t, err)
	var sealed bytes.Buffer
	_, err = sealed.ReadFrom(stored)
	require.NoError(t, err)
	require.NoError(t, stored.Close())
	require.Equal(t, []byte("OHSV"), sealed.Bytes()[:4], "stored sealed")

	r = e.do("GET", "/v1/users/"+u.String()+"/archive", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	files := openArchive(t, r.body)
	assert.Equal(t, body, files["raw/apple_health/2026-01-10/batch-1_0.ndjson.gz"], "the payload as sent")
	for name, b := range files {
		assert.False(t, bytes.Contains(b, []byte("OHSV")), "%s holds sealed bytes", name)
	}

	// Without keys: a 500, not an archive the user cannot read.
	bare := e.user(e.tenantA, "a-2")
	r = e.do("GET", "/v1/users/"+bare.String()+"/archive", e.keyA, nil)
	assert.Equal(t, http.StatusInternalServerError, r.code)
	assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"))
}
