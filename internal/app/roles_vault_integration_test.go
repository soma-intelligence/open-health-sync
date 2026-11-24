// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// runUntil runs the app in the background and returns the function that
// stops it and waits for it.
func runUntil(t *testing.T, cfg config.Config, logs *syncBuffer) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, logs) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err, "logs:\n%s", logs)
		case <-time.After(60 * time.Second):
			t.Fatalf("the app did not stop:\n%s", logs)
		}
	}
	t.Cleanup(stop)
	return stop
}

// A process that runs the collect role alone seals what it pulls and cannot
// open anything stored: it never reaches the key provider, even to start, and
// holds no key. A process with the api and etl roles then processes what it
// stored (docs/specs/vault.md, "Roles and keys").
//
// The key provider of the collect-only process is a transit server that
// counts every request and refuses them all: one call would both be counted
// and stop the process from starting. Inbound notifications are received by
// the api role's listener in this codebase, so the pull is what a
// collect-only process does; the handlers with a Vault that cannot open are
// covered in internal/collect.
func TestCollectOnlyProcessCannotOpenStoredData(t *testing.T) {
	startFakeOAuth(t)
	ctx := context.Background()
	url := db.TestDB(t)
	rawURL := "file://" + t.TempDir()
	pool, err := db.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// 1. An api-only process creates the user (with keys) and the connection.
	apiCfg := collectConfig(t)
	apiCfg.DatabaseURL, apiCfg.RawStore = url, rawURL
	apiCfg.Roles = map[string]bool{"api": true}
	logs := &syncBuffer{}
	stopAPI := start(t, apiCfg, logs)
	keys := bootstrapKeys(t, apiCfg, logs.String())
	require.Len(t, keys, 1)
	api := apiClient{t: t, base: "http://" + apiCfg.Listen, key: keys[0], c: &http.Client{Timeout: 10 * time.Second}}
	code, b := api.call("POST", "/v1/users", `{"external_ref":"split"}`)
	require.Equal(t, http.StatusCreated, code, string(b))
	var u struct {
		ID uuid.UUID `json:"id"`
	}
	require.NoError(t, json.Unmarshal(b, &u))
	code, b = api.call("POST", "/v1/users/"+u.ID.String()+"/connections/"+appFakeSlug, "")
	require.Equal(t, http.StatusOK, code, string(b))
	var startResp struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	require.NoError(t, json.Unmarshal(b, &startResp))
	res, err := (&http.Client{Timeout: 10 * time.Second}).Get(startResp.AuthorizeURL)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.NoError(t, stopAPI())
	var tenant uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT tenant_id FROM ohs_core.end_users WHERE id = $1`, u.ID).Scan(&tenant))

	// 2. The collect-only process, its key provider a server that counts.
	var providerCalls atomic.Int64
	transit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		http.Error(w, "sealed", http.StatusServiceUnavailable)
	}))
	t.Cleanup(transit.Close)
	collectCfg := collectConfig(t)
	collectCfg.DatabaseURL, collectCfg.RawStore = url, rawURL
	collectCfg.Roles = map[string]bool{"collect": true}
	collectCfg.BootstrapTenant = ""
	collectCfg.Vault = config.Vault{Provider: "hashicorp", HashiCorpAddr: transit.URL, HashiCorpMount: "transit", HashiCorpKey: "ohs"}
	collectLogs := &syncBuffer{}
	stopCollect := runUntil(t, collectCfg, collectLogs)

	var key string
	require.Eventually(t, func() bool {
		err := pool.QueryRow(ctx, `SELECT key FROM ohs_sync.raw_objects WHERE tenant_id = $1 AND user_id = $2`, tenant, u.ID).Scan(&key)
		return err == nil
	}, 60*time.Second, 200*time.Millisecond, "the collect-only process pulls and stores:\n%s", collectLogs)
	stopCollect()

	raw, err := rawstore.Open(rawURL)
	require.NoError(t, err)
	rc, err := raw.Get(ctx, key)
	require.NoError(t, err)
	stored, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(stored, []byte("OHSV")), "the pulled object is stored sealed")
	assert.NotContains(t, string(stored), `"bpm"`, "none of its plaintext is in the store")
	h, sealed, err := vault.ReadObjectHeader(bytes.NewReader(stored))
	require.NoError(t, err)
	require.True(t, sealed)
	assert.EqualValues(t, 1, h.Version)
	assert.Zero(t, providerCalls.Load(), "a collect-only process makes no call to the key provider")

	// The Vault such a process holds: public keys only.
	closed := vault.New(pool, nil, vault.Options{CanOpen: false})
	_, _, err = closed.PublicKey(ctx, pool, tenant, u.ID)
	require.NoError(t, err)
	_, err = closed.ForUser(ctx, tenant, u.ID)
	require.ErrorIs(t, err, vault.ErrCannotOpen)
	_, _, err = vault.OpenObject(bytes.NewReader(stored), func(uint32) (*vault.UserKeys, error) { return nil, vault.ErrCannotOpen })
	require.Error(t, err, "without the private key the object does not open")

	var rows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM ohs_data.quantity_samples WHERE user_id = $1`, u.ID).Scan(&rows))
	require.Zero(t, rows, "nothing was processed yet: no etl role ran")

	// 3. A process with api and etl opens the object and writes sealed rows.
	etlCfg := collectConfig(t)
	etlCfg.DatabaseURL, etlCfg.RawStore = url, rawURL
	etlCfg.Roles = map[string]bool{"api": true, "etl": true}
	etlLogs := &syncBuffer{}
	start(t, etlCfg, etlLogs)
	require.Eventually(t, func() bool {
		var n int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM ohs_data.quantity_samples
			WHERE user_id = $1 AND sealed IS NOT NULL AND value IS NULL`, u.ID).Scan(&n)
		return err == nil && n == 1
	}, 60*time.Second, 200*time.Millisecond, "the etl role processes the sealed object:\n%s", etlLogs)
	assert.False(t, strings.Contains(etlLogs.String(), "does not open"), "logs:\n%s", etlLogs)
}
