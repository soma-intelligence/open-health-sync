// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// sealedEnv is an instance with a vault and one user holding sealed heart
// rate samples.
type sealedEnv struct {
	pool     *pgxpool.Pool
	store    *tenant.Store
	provider *downProvider
	opts     vault.Options
	key      string
	tenant   uuid.UUID
	user     uuid.UUID
	start    time.Time
}

func newSealedEnv(t *testing.T) *sealedEnv {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	local, err := vault.OpenProvider(ctx, config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{6}, 32)})
	require.NoError(t, err)
	e := &sealedEnv{pool: pool, provider: &downProvider{KeyProvider: local},
		opts: vault.Options{CanOpen: true, CacheUsers: 8, CacheTTL: time.Minute}, start: time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)}
	writer := vault.New(pool, e.provider, e.opts)
	e.store = tenant.New(pool).WithVault(writer)
	ten, key, err := e.store.CreateTenant(ctx, "Sealed")
	require.NoError(t, err)
	u, err := e.store.CreateUser(ctx, ten.ID, "sealed-1")
	require.NoError(t, err)
	e.key, e.tenant, e.user = key, ten.ID, u.ID

	mr := &model.MapResult{}
	for i := range 5 {
		at := e.start.Add(time.Duration(i) * time.Minute)
		s := model.QuantitySample{Provider: "oura", SourceName: "Ring", QuantityType: "heart_rate", Value: 60 + float64(i),
			Unit: "bpm", IsPrimarySource: true, StartDate: at, EndDate: at, Metadata: map[string]any{"timezone": "UTC"}}
		s.ContentHash = model.ComputeContentHash(map[string]string{"at": at.Format(time.RFC3339)})
		mr.QuantitySamples = append(mr.QuantitySamples, s)
	}
	_, err = etl.WriteSynthetic(ctx, pool, writer, ten.ID, u.ID, "oura", mr)
	require.NoError(t, err)
	var clear int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM ohs_data.quantity_samples WHERE user_id = $1
		AND (sealed IS NULL OR value IS NOT NULL)`, u.ID).Scan(&clear))
	require.Zero(t, clear, "the samples are sealed")
	return e
}

// router serves the instance with a vault of its own (a cold key cache).
func (e *sealedEnv) router() http.Handler {
	return NewRouter(Deps{Pool: e.pool, Store: e.store, Vault: vault.New(e.pool, e.provider, e.opts),
		Metrics: true, MetricsToken: "metrics-token"})
}

func (e *sealedEnv) get(h http.Handler, path, bearer string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	h.ServeHTTP(rec, req)
	return rec
}

// Plan Task 13, step 3: a sealed row that does not open fails the request
// with 500 and its request id, records the audit event vault.open_failed
// with the table and no value, and counts in the metric. The list never
// comes back without that row.
func TestOpenFailureIs500NotEmpty(t *testing.T) {
	e := newSealedEnv(t)
	ctx := context.Background()
	h := e.router()
	base := "/v1/users/" + e.user.String()

	rec := e.get(h, base+"/samples?type=heart_rate", e.key)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var page struct {
		Data []struct {
			ID    int64   `json:"id"`
			Value float64 `json:"value"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Data, 5)
	assert.Equal(t, 60.0, page.Data[0].Value, "the sealed values are served")
	assert.Equal(t, 64.0, page.Data[4].Value)

	// One bit of the third row's sealed value.
	_, err := e.pool.Exec(ctx, `UPDATE ohs_data.quantity_samples
		SET sealed = set_byte(sealed, length(sealed) - 3, get_byte(sealed, length(sealed) - 3) # 4) WHERE id = $1`, page.Data[2].ID)
	require.NoError(t, err)

	for _, path := range []string{
		base + "/samples?type=heart_rate",
		base + "/samples?type=heart_rate&view=all&limit=3",
		base + "/samples/stats?type=heart_rate&interval=hour&tz=Asia/Kolkata&start=2026-09-24&end=2026-09-25",
		base + "/fhir/Observation",
		base + "/omh/data-points",
	} {
		rec := e.get(h, path, e.key)
		require.Equal(t, http.StatusInternalServerError, rec.Code, path+": "+rec.Body.String())
		var problem struct {
			Status    int    `json:"status"`
			Detail    string `json:"detail"`
			RequestID string `json:"request_id"`
			Data      any    `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem), path)
		assert.NotEmpty(t, problem.RequestID, path)
		assert.Equal(t, rec.Header().Get("X-Request-Id"), problem.RequestID, path)
		assert.Nil(t, problem.Data, "no page beside the failure: "+path)
		assert.NotContains(t, rec.Body.String(), `"value"`, "no record in the answer: "+path)

		// The audit event of this request: who, which table, no value.
		var (
			tenantID uuid.UUID
			userID   *uuid.UUID
			actor    string
			detail   map[string]any
		)
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT tenant_id, user_id, actor_type, detail FROM ohs_core.audit_events
			WHERE action = 'vault.open_failed' AND request_id = $1`, problem.RequestID).Scan(&tenantID, &userID, &actor, &detail), path)
		assert.Equal(t, e.tenant, tenantID)
		require.NotNil(t, userID)
		assert.Equal(t, e.user, *userID)
		assert.Equal(t, "system", actor)
		assert.Equal(t, map[string]any{"table": "quantity_samples"}, detail, "the table and nothing else")
	}

	// A page that does not hold the row is not affected.
	rec = e.get(h, base+"/samples?type=heart_rate&view=all&limit=2", e.key)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	assert.Len(t, page.Data, 2)

	rec = e.get(h, "/metrics", "metrics-token")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `ohs_vault_open_failures_total{kind="row"} 5`, "one per failed request")
}

// A user whose keys are gone (an erasure shredded them, the rows are not
// removed yet) reads as an absent user; a key provider that is down with a
// cold cache answers 503 with Retry-After; an instance without a vault that
// meets a sealed row answers 500. None of them serves an empty page.
func TestSealedReadsWithoutKeys(t *testing.T) {
	e := newSealedEnv(t)
	ctx := context.Background()
	path := "/v1/users/" + e.user.String() + "/samples?type=heart_rate"

	e.provider.down.Store(true)
	rec := e.get(e.router(), path, e.key)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Equal(t, "5", rec.Header().Get("Retry-After"))
	e.provider.down.Store(false)

	noVault := NewRouter(Deps{Pool: e.pool, Store: e.store})
	rec = e.get(noVault, path, e.key)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	_, err := e.pool.Exec(ctx, `DELETE FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user)
	require.NoError(t, err)
	rec = e.get(e.router(), path, e.key)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
