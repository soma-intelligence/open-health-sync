// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// downProvider is the local provider behind a switch that makes it fail as
// an unreachable KMS does.
type downProvider struct {
	vault.KeyProvider
	down atomic.Bool
}

func (p *downProvider) Unwrap(ctx context.Context, wrapped, aad []byte, version string) ([]byte, error) {
	if p.down.Load() {
		return nil, fmt.Errorf("%w: connection refused", vault.ErrProviderUnavailable)
	}
	return p.KeyProvider.Unwrap(ctx, wrapped, aad, version)
}

// Review focus: a provider outage with a cold cache answers 503 with
// Retry-After on a read and on /readyz; a warm cache keeps serving.
func TestVaultProviderDown(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	st := tenant.New(pool)
	ten, _, err := st.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	u, err := st.CreateUser(ctx, ten.ID, "u-1")
	require.NoError(t, err)

	local, err := vault.OpenProvider(ctx, config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{3}, 32)})
	require.NoError(t, err)
	p := &downProvider{KeyProvider: local}
	opts := vault.Options{CanOpen: true, CacheUsers: 8, CacheTTL: time.Minute}
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, vault.New(pool, p, opts).EnsureUser(ctx, tx, ten.ID, u.ID))
	require.NoError(t, tx.Commit(ctx))

	// A read that needs the user's keys, as the store layer asks for them.
	newRouter := func(v *vault.Vault) http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /read/{id}", func(w http.ResponseWriter, r *http.Request) {
			if _, err := v.ForUser(r.Context(), ten.ID, uuid.MustParse(r.PathValue("id"))); err != nil {
				readError(w, r, err, "user")
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		})
		mux.Handle("/", NewRouter(Deps{Pool: pool, Store: st, Vault: v, Metrics: true, MetricsToken: "t"}))
		return mux
	}
	get := func(h http.Handler, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer t")
		h.ServeHTTP(rec, req)
		return rec
	}

	p.down.Store(true)
	cold := newRouter(vault.New(pool, p, opts))
	rec := get(cold, "/read/"+u.ID.String())
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Equal(t, "5", rec.Header().Get("Retry-After"))
	assert.Contains(t, rec.Body.String(), "key provider")
	rec = get(cold, "/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())

	p.down.Store(false)
	warm := newRouter(vault.New(pool, p, opts))
	require.Equal(t, http.StatusOK, get(warm, "/read/"+u.ID.String()).Code)
	require.Equal(t, http.StatusOK, get(warm, "/readyz").Code)
	p.down.Store(true)
	assert.Equal(t, http.StatusOK, get(warm, "/read/"+u.ID.String()).Code, "the cached keys keep serving")
	assert.Equal(t, http.StatusOK, get(warm, "/readyz").Code)

	rec = get(warm, "/metrics")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `ohs_vault_cache_total{result="hit"} 1`)
}
