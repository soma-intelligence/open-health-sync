// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/connectors/dexcom"
	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/providerset"
	"github.com/soma-intelligence/open-health-sync/internal/tenantkeys"
)

type tenantInfo struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Admin bool      `json:"admin"`
	Key   struct {
		ID     uuid.UUID `json:"id"`
		Prefix string    `json:"prefix"`
	} `json:"key"`
}

type keyInfo struct {
	ID        uuid.UUID  `json:"id"`
	Prefix    string     `json:"prefix"`
	RevokedAt *time.Time `json:"revoked_at"`
	Current   bool       `json:"current"`
	Key       string     `json:"key"`
}

func TestTenantAndKeys(t *testing.T) {
	e := newEnv(t)

	var a, b tenantInfo
	r := e.do("GET", "/v1/tenant", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &a)
	assert.Equal(t, e.tenantA, a.ID)
	assert.Equal(t, "Acme", a.Name)
	assert.True(t, a.Admin, "the first tenant is the instance admin")
	assert.True(t, strings.HasPrefix(e.keyA, a.Key.Prefix))
	r = e.do("GET", "/v1/tenant", e.keyB, nil)
	r.json(t, &b)
	assert.False(t, b.Admin)
	assert.Equal(t, e.tenantB, b.ID)

	var keys struct{ Data []keyInfo }
	r = e.do("GET", "/v1/keys", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &keys)
	require.Len(t, keys.Data, 1)
	assert.True(t, keys.Data[0].Current)
	assert.Equal(t, a.Key.ID, keys.Data[0].ID)

	// A new key: shown once, never cached, and it works.
	r = e.do("POST", "/v1/keys", e.keyA, nil)
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	var created keyInfo
	r.json(t, &created)
	require.True(t, strings.HasPrefix(created.Key, "ohs_sk_"), created.Key)
	assert.True(t, strings.HasPrefix(created.Key, created.Prefix))
	assert.False(t, created.Current)
	require.Equal(t, http.StatusOK, e.do("GET", "/v1/tenant", created.Key, nil).code)
	r = e.do("GET", "/v1/keys", e.keyA, nil)
	keys.Data = nil
	r.json(t, &keys)
	require.Len(t, keys.Data, 2)
	assert.NotContains(t, string(r.body), created.Key, "a listing never holds a secret")
	assert.Equal(t, http.StatusBadRequest, e.do("POST", "/v1/keys", e.keyA, `{"name":"x"}`).code)

	// The key making the call cannot revoke itself.
	r = e.do("DELETE", "/v1/keys/"+a.Key.ID.String(), e.keyA, nil)
	assert.Equal(t, http.StatusConflict, r.code, string(r.body))
	// Another tenant's key is not found, nor is a malformed id.
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", "/v1/keys/"+b.Key.ID.String(), e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", "/v1/keys/not-an-id", e.keyA, nil).code)
	require.Equal(t, http.StatusOK, e.do("GET", "/v1/tenant", e.keyB, nil).code, "B's key still works")

	r = e.do("DELETE", "/v1/keys/"+created.ID.String(), e.keyA, nil)
	require.Equal(t, http.StatusNoContent, r.code, string(r.body))
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/tenant", created.Key, nil).code)
	assert.Equal(t, http.StatusNoContent, e.do("DELETE", "/v1/keys/"+created.ID.String(), e.keyA, nil).code,
		"revoking a revoked key succeeds")

	// User tokens are refused.
	u := e.user(e.tenantA, "a-1")
	tok, _, err := e.store.MintUserToken(context.Background(), e.tenantA, u, []string{"read"}, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, e.do("POST", "/v1/keys", tok, nil).code)

	// The audit log names the action and the key.
	var page auditPage
	r = e.do("GET", "/v1/audit?action=key.", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &page)
	actions := map[string]int{}
	for _, ev := range page.Data {
		actions[ev.Action]++
		if ev.Action == audit.KeyRevoke && *ev.Status == http.StatusNoContent {
			assert.Equal(t, created.ID.String(), ev.Detail["key_id"])
		}
	}
	assert.Equal(t, 2, actions[audit.KeyCreate], "%+v", page.Data)
	assert.Equal(t, 5, actions[audit.KeyRevoke], "%+v", page.Data)
}

func TestPipeline(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	v := e.user(e.tenantA, "a-2")
	other := e.user(e.tenantB, "b-1")
	e.exec(`UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, v)
	e.exec(`INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, status, broken_reason) VALUES
		($1, $2, 'withings', 'active', NULL), ($1, $2, 'oura', 'active', 'failing'),
		($1, $3, 'oura', 'needs_reauth', 'needs_reauth'), ($4, $5, 'withings', 'active', NULL)`,
		e.tenantA, u, v, e.tenantB, other)
	e.exec(`INSERT INTO ohs_core.provider_limits (provider, paused_until) VALUES ('oura', now() + interval '1 hour'),
		('withings', now() - interval '1 hour')`)
	e.exec(`INSERT INTO ohs_sync.pull_jobs (tenant_id, user_id, provider, from_time, to_time, status, outcome, last_error) VALUES
		($1, $2, 'withings', '2026-02-01', '2026-02-02', 'pending', NULL, NULL),
		($1, $2, 'withings', '2026-02-02', '2026-02-03', 'pending', 'failed', 'timeout'),
		($1, $2, 'oura', '2026-02-01', '2026-02-02', 'pending', 'rate_limited', NULL),
		($1, $2, 'oura', '2026-02-02', '2026-02-03', 'running', NULL, NULL),
		($1, $2, 'oura', '2026-02-03', '2026-02-04', 'done', 'completed', NULL),
		($1, $2, 'oura', '2026-02-04', '2026-02-05', 'parked', 'failed', 'HTTP 500'),
		($1, $2, 'oura', '2026-02-05', '2026-02-06', 'parked', 'inactive', NULL),
		($3, $4, 'withings', '2026-02-01', '2026-02-02', 'parked', 'failed', 'B fails')`,
		e.tenantA, u, e.tenantB, other)
	// An old failure does not count in the last 24 hours.
	e.exec(`INSERT INTO ohs_sync.pull_jobs (tenant_id, user_id, provider, from_time, to_time, status, outcome, created_at)
		VALUES ($1, $2, 'oura', '2026-01-01', '2026-01-02', 'parked', 'failed', now() - interval '2 days')`, e.tenantA, u)
	e.exec(`INSERT INTO ohs_sync.etl_jobs (tenant_id, user_id, provider, key, status, attempts, last_error) VALUES
		($1, $2, 'apple_health', 'k1', 'pending', 0, NULL), ($1, $2, 'apple_health', 'k2', 'running', 0, NULL),
		($1, $2, 'apple_health', 'k3', 'done', 1, NULL), ($1, $2, 'apple_health', 'k4', 'parked', 5, 'read raw object: gone'),
		($1, $2, 'withings', 'k5', 'parked', 5, 'boom'), ($3, $4, 'apple_health', 'k6', 'parked', 5, 'B')`,
		e.tenantA, u, e.tenantB, other)
	e.exec(`INSERT INTO ohs_data.failed_records (tenant_id, user_id, provider, raw_key, line_number, reason_code, status) VALUES
		($1, $2, 'apple_health', 'k4', 1, 'validation', 'pending'), ($1, $2, 'apple_health', 'k4', 1, 'validation', 'pending'),
		($1, $2, 'withings', 'k5', NULL, 'job', 'pending'), ($3, $4, 'apple_health', 'k6', 1, 'parse', 'pending')`,
		e.tenantA, u, e.tenantB, other)
	e.exec(`INSERT INTO ohs_sync.raw_objects (tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part) VALUES
		($1, $2, 'apple_health', 'r1', 'h1', 100, 'b', 0), ($1, $2, 'apple_health', 'r2', 'h2', 50, 'b', 1),
		($3, $4, 'apple_health', 'r3', 'h3', 7, 'b', 0)`, e.tenantA, u, e.tenantB, other)
	e.exec(`INSERT INTO ohs_sync.raw_objects (tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, created_at)
		VALUES ($1, $2, 'apple_health', 'r0', 'h0', 1000, 'b', 0, now() - interval '3 days')`, e.tenantA, u)
	e.exec(`INSERT INTO ohs_core.webhook_endpoints (tenant_id, url, secret_sealed, status, failing_since) VALUES
		($1, 'https://a.example/ok', 'x'::bytea, 'active', NULL), ($1, 'https://a.example/failing', 'x'::bytea, 'active', now()),
		($1, 'https://a.example/off', 'x'::bytea, 'disabled', NULL)`, e.tenantA)
	e.exec(`INSERT INTO ohs_core.webhook_deliveries (tenant_id, endpoint_id, event_id, event_type, payload, status)
		SELECT $1, id, gen_random_uuid(), 'test', '{}', s FROM ohs_core.webhook_endpoints,
			unnest(ARRAY['pending', 'parked', 'done']) s WHERE url = 'https://a.example/failing'`, e.tenantA)

	type pipeline struct {
		Users   struct{ Active, Deleting int64 }
		Sources []struct {
			Slug        string
			Name        string
			Enabled     bool
			Connections map[string]int64
			Broken      int64
			PausedUntil *time.Time `json:"paused_until"`
		}
		Sync struct {
			Queued, Running, Retrying int64
			RateLimited               int64 `json:"rate_limited"`
			Failed24h                 int64 `json:"failed_24h"`
			Succeeded24h              int64 `json:"succeeded_24h"`
		}
		Processing struct {
			Pending, Running, Parked int64
			RejectedRecords          int64 `json:"rejected_records"`
		}
		Storage struct {
			RawObjects24h int64 `json:"raw_objects_24h"`
			RawBytes24h   int64 `json:"raw_bytes_24h"`
		}
		Delivery struct {
			Endpoints, Pending, Parked int64
			EndpointsFailing           int64 `json:"endpoints_failing"`
			EndpointsDisabled          int64 `json:"endpoints_disabled"`
		}
	}
	r := e.do("GET", "/v1/pipeline", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var p pipeline
	r.json(t, &p)
	assert.Equal(t, int64(1), p.Users.Active)
	assert.Equal(t, int64(1), p.Users.Deleting)
	require.Len(t, p.Sources, 3, "%+v", p.Sources)
	assert.Equal(t, "apple_health", p.Sources[0].Slug)
	assert.True(t, p.Sources[0].Enabled)
	assert.Equal(t, "Apple Health", p.Sources[0].Name)
	oura := p.Sources[1]
	assert.Equal(t, "oura", oura.Slug)
	assert.False(t, oura.Enabled, "not enabled here, listed for its connections")
	assert.Equal(t, map[string]int64{"active": 1, "needs_reauth": 1}, oura.Connections)
	assert.Equal(t, int64(1), oura.Broken)
	assert.NotNil(t, oura.PausedUntil)
	assert.Equal(t, map[string]int64{"active": 1}, p.Sources[2].Connections, "B's connection is not counted")
	assert.Nil(t, p.Sources[2].PausedUntil, "a pause that ended")
	assert.Equal(t, int64(1), p.Sync.Queued)
	assert.Equal(t, int64(1), p.Sync.Running)
	assert.Equal(t, int64(1), p.Sync.Retrying)
	assert.Equal(t, int64(1), p.Sync.RateLimited)
	assert.Equal(t, int64(1), p.Sync.Failed24h)
	assert.Equal(t, int64(1), p.Sync.Succeeded24h)
	assert.Equal(t, int64(1), p.Processing.Pending)
	assert.Equal(t, int64(1), p.Processing.Running)
	assert.Equal(t, int64(2), p.Processing.Parked)
	assert.Equal(t, int64(2), p.Processing.RejectedRecords, "k4 line 1 once, and object k5")
	assert.Equal(t, int64(2), p.Storage.RawObjects24h)
	assert.Equal(t, int64(150), p.Storage.RawBytes24h)
	assert.Equal(t, int64(3), p.Delivery.Endpoints)
	assert.Equal(t, int64(1), p.Delivery.EndpointsFailing)
	assert.Equal(t, int64(1), p.Delivery.EndpointsDisabled)
	assert.Equal(t, int64(1), p.Delivery.Pending)
	assert.Equal(t, int64(1), p.Delivery.Parked)

	// B sees its own pipeline only.
	r = e.do("GET", "/v1/pipeline", e.keyB, nil)
	p = pipeline{}
	r.json(t, &p)
	assert.Equal(t, int64(1), p.Processing.Parked)
	assert.Equal(t, int64(0), p.Delivery.Endpoints)
	assert.Equal(t, int64(7), p.Storage.RawBytes24h)

	// Sync runs across users.
	type runs struct {
		Data []struct {
			ID       int64
			UserID   uuid.UUID `json:"user_id"`
			Provider string
			Status   string
			Error    *string
		}
		NextCursor *string `json:"next_cursor"`
	}
	var rs runs
	r = e.do("GET", "/v1/pipeline/sync-runs?status=failed", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &rs)
	require.Len(t, rs.Data, 2, "the cancelled run and B's are left out")
	assert.Equal(t, u, rs.Data[0].UserID)
	assert.Greater(t, rs.Data[0].ID, rs.Data[1].ID, "newest first")
	require.NotNil(t, rs.Data[1].Error)
	assert.Equal(t, "HTTP 500", *rs.Data[1].Error)
	rs = runs{}
	r = e.do("GET", "/v1/pipeline/sync-runs?limit=3", e.keyA, nil)
	r.json(t, &rs)
	require.Len(t, rs.Data, 3)
	require.NotNil(t, rs.NextCursor)
	next := *rs.NextCursor
	rs = runs{}
	e.do("GET", "/v1/pipeline/sync-runs?limit=100&cursor="+next, e.keyA, nil).json(t, &rs)
	assert.Len(t, rs.Data, 5, "8 runs of A in all")
	rs = runs{}
	e.do("GET", "/v1/pipeline/sync-runs?provider=withings", e.keyA, nil).json(t, &rs)
	assert.Len(t, rs.Data, 2)
	for _, q := range []string{"status=done", "limit=0", "limit=101", "cursor=x"} {
		assert.Equal(t, http.StatusBadRequest, e.do("GET", "/v1/pipeline/sync-runs?"+q, e.keyA, nil).code, q)
	}

	// Processing jobs: parked by default, retried one by one or together.
	type jobs struct {
		Data []struct {
			ID            int64
			Provider      string
			Status        string
			Attempts      int
			Error         *string
			NextAttemptAt *time.Time `json:"next_attempt_at"`
		}
	}
	var js jobs
	r = e.do("GET", "/v1/pipeline/etl-jobs", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &js)
	require.Len(t, js.Data, 2)
	assert.Equal(t, "withings", js.Data[0].Provider)
	assert.Equal(t, "boom", *js.Data[0].Error)
	assert.Nil(t, js.Data[0].NextAttemptAt)
	k4 := js.Data[1].ID
	js = jobs{}
	e.do("GET", "/v1/pipeline/etl-jobs?status=pending", e.keyA, nil).json(t, &js)
	require.Len(t, js.Data, 1)
	assert.NotNil(t, js.Data[0].NextAttemptAt)
	assert.Equal(t, http.StatusBadRequest, e.do("GET", "/v1/pipeline/etl-jobs?status=done", e.keyA, nil).code)

	var bJobs jobs
	e.do("GET", "/v1/pipeline/etl-jobs", e.keyB, nil).json(t, &bJobs)
	require.Len(t, bJobs.Data, 1)
	r = e.do("POST", "/v1/pipeline/etl-jobs/"+itoa(bJobs.Data[0].ID)+"/retry", e.keyA, nil)
	assert.Equal(t, http.StatusNotFound, r.code, "another tenant's job")
	assert.Equal(t, http.StatusNotFound, e.do("POST", "/v1/pipeline/etl-jobs/x/retry", e.keyA, nil).code)

	r = e.do("POST", "/v1/pipeline/etl-jobs/"+itoa(k4)+"/retry", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var retried struct {
		Status   string
		Attempts int
	}
	r.json(t, &retried)
	assert.Equal(t, "pending", retried.Status)
	assert.Equal(t, 0, retried.Attempts)
	r = e.do("POST", "/v1/pipeline/etl-jobs/"+itoa(k4)+"/retry", e.keyA, nil)
	assert.Equal(t, http.StatusConflict, r.code, "no longer parked")

	var bulk struct{ Retried int64 }
	r = e.do("POST", "/v1/pipeline/etl-jobs/retry", e.keyA, `{"provider":"oura"}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &bulk)
	assert.Equal(t, int64(0), bulk.Retried)
	e.do("POST", "/v1/pipeline/etl-jobs/retry", e.keyA, nil).json(t, &bulk)
	assert.Equal(t, int64(1), bulk.Retried)
	js = jobs{}
	e.do("GET", "/v1/pipeline/etl-jobs", e.keyA, nil).json(t, &js)
	assert.Empty(t, js.Data)
	bJobs = jobs{}
	e.do("GET", "/v1/pipeline/etl-jobs", e.keyB, nil).json(t, &bJobs)
	assert.Len(t, bJobs.Data, 1, "B's parked job is untouched")
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// adminEnv runs Dexcom (keyed, with a validated optional REGION) and Apple
// Health (built in) with the provider settings in the database.
func adminEnv(t *testing.T, env map[string]config.ProviderKeys) (*env, *crypto.Box) {
	box, err := crypto.NewBox(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	e := newEnv(t, func(d *api.Deps) {
		set, err := providerset.New(context.Background(), providerset.Options{
			Connectors: []connector.Connector{applehealth.New(), dexcom.New(dexcom.Options{})},
			Env:        env,
			Store:      providerset.NewPG(d.Pool, box),
		})
		require.NoError(t, err)
		d.Providers = set
		d.TenantKeys = tenantkeys.New(context.Background(), tenantkeys.Options{
			Connectors: []connector.Connector{applehealth.New(), dexcom.New(dexcom.Options{})},
			Store:      tenantkeys.NewPG(d.Pool, box, collect.RetireTenantKeys),
		})
	})
	return e, box
}

type adminProvider struct {
	Slug    string
	Name    string
	Kinds   []string
	Status  string
	Enabled bool
	Source  *string
	Error   *string
	Keys    []struct {
		Name     string
		Env      string
		Required bool
		Secret   bool
		Set      bool
		Value    *string
	}
	URLs struct {
		Redirect *string
		Inbound  *string
	}
	UpdatedBy *string    `json:"updated_by"`
	UpdatedAt *time.Time `json:"updated_at"`
}

func enabledSlugs(t *testing.T, e *env) []string {
	t.Helper()
	var body struct{ Data []struct{ Slug string } }
	e.do("GET", "/v1/providers", e.keyB, nil).json(t, &body)
	out := []string{}
	for _, p := range body.Data {
		out = append(out, p.Slug)
	}
	return out
}

func TestAdminProviders(t *testing.T) {
	e, _ := adminEnv(t, nil)

	// Only a key of the admin tenant.
	assert.Equal(t, http.StatusForbidden, e.do("GET", "/v1/admin/providers", e.keyB, nil).code)
	assert.Equal(t, http.StatusForbidden, e.do("PATCH", "/v1/admin/providers/dexcom", e.keyB, `{"enabled":false}`).code)
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/admin/providers", "", nil).code)

	r := e.do("GET", "/v1/admin/providers", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var list struct {
		Data      []adminProvider
		PublicURL bool `json:"public_url"`
	}
	r.json(t, &list)
	assert.False(t, list.PublicURL)
	require.Len(t, list.Data, 2)
	apple, dx := list.Data[0], list.Data[1]
	assert.Equal(t, "built_in", apple.Status)
	assert.Equal(t, []string{"device"}, apple.Kinds)
	assert.Empty(t, apple.Keys)
	assert.Equal(t, "Dexcom", dx.Name)
	assert.Equal(t, "not_configured", dx.Status)
	assert.Nil(t, dx.Source)
	require.NotNil(t, dx.URLs.Redirect)
	assert.Equal(t, "http://localhost:8080/oauth/dexcom/callback", *dx.URLs.Redirect)
	assert.Nil(t, dx.URLs.Inbound)
	require.Len(t, dx.Keys, 3)
	assert.Equal(t, "CLIENT_ID", dx.Keys[0].Name)
	assert.Equal(t, "OHS_DEXCOM_CLIENT_ID", dx.Keys[0].Env)
	assert.True(t, dx.Keys[0].Required)
	assert.False(t, dx.Keys[0].Secret)
	assert.True(t, dx.Keys[1].Secret, "CLIENT_SECRET")
	assert.Equal(t, "REGION", dx.Keys[2].Name)
	assert.False(t, dx.Keys[2].Required)
	assert.Equal(t, []string{"apple_health"}, enabledSlugs(t, e))

	// Refused changes leave the provider as it was.
	r = e.do("PATCH", "/v1/admin/providers/dexcom", e.keyA, `{"enabled":true}`)
	assert.Equal(t, http.StatusUnprocessableEntity, r.code, string(r.body))
	assert.Contains(t, string(r.body), "CLIENT_ID and CLIENT_SECRET")
	r = e.do("PATCH", "/v1/admin/providers/dexcom", e.keyA,
		`{"keys":{"CLIENT_ID":"dx-client","CLIENT_SECRET":"dx-secret-value","REGION":"mars"}}`)
	assert.Equal(t, http.StatusUnprocessableEntity, r.code, string(r.body))
	assert.Contains(t, string(r.body), "mars")
	assert.NotContains(t, string(r.body), "dx-secret-value")
	r = e.do("PATCH", "/v1/admin/providers/dexcom", e.keyA, `{"keys":{"NOPE":"x"}}`)
	assert.Equal(t, http.StatusBadRequest, r.code, string(r.body))
	assert.Equal(t, http.StatusBadRequest, e.do("PATCH", "/v1/admin/providers/dexcom", e.keyA, `{}`).code)
	assert.Equal(t, http.StatusBadRequest, e.do("PATCH", "/v1/admin/providers/dexcom", e.keyA, `{"keys":{"CLIENT_ID":"a\u0000b"}}`).code)
	assert.Equal(t, http.StatusConflict, e.do("PATCH", "/v1/admin/providers/apple_health", e.keyA, `{"enabled":false}`).code)
	assert.Equal(t, http.StatusNotFound, e.do("PATCH", "/v1/admin/providers/nope", e.keyA, `{"enabled":false}`).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/admin/providers/nope", e.keyA, nil).code)
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_core.provider_settings`).Scan(&n))
	assert.Zero(t, n)

	// Saved: on at once, the secret never served back nor stored in clear.
	r = e.do("PATCH", "/v1/admin/providers/dexcom", e.keyA,
		`{"keys":{"CLIENT_ID":" dx-client ","CLIENT_SECRET":"dx-secret-value","REGION":"eu"}}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.NotContains(t, string(r.body), "dx-secret-value")
	var got adminProvider
	r.json(t, &got)
	assert.Equal(t, "active", got.Status)
	assert.Equal(t, "console", *got.Source)
	assert.Equal(t, "dx-client", *got.Keys[0].Value, "trimmed")
	assert.True(t, got.Keys[1].Set)
	assert.Nil(t, got.Keys[1].Value)
	assert.Equal(t, "eu", *got.Keys[2].Value)
	var a tenantInfo
	e.do("GET", "/v1/tenant", e.keyA, nil).json(t, &a)
	assert.Equal(t, a.Key.ID.String(), *got.UpdatedBy)
	assert.NotNil(t, got.UpdatedAt)
	var sealed []byte
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT keys_sealed FROM ohs_core.provider_settings WHERE slug = 'dexcom'`).Scan(&sealed))
	assert.NotContains(t, string(sealed), "dx-secret-value")
	assert.Equal(t, []string{"apple_health", "dexcom"}, enabledSlugs(t, e), "every tenant sees it")

	// A key left out is kept, a null one removed.
	r = e.do("PATCH", "/v1/admin/providers/dexcom", e.keyA, `{"keys":{"REGION":null}}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	got = adminProvider{}
	r.json(t, &got)
	assert.False(t, got.Keys[2].Set)
	assert.True(t, got.Keys[1].Set)
	r = e.do("PATCH", "/v1/admin/providers/dexcom", e.keyA, `{"keys":{"CLIENT_SECRET":null}}`)
	assert.Equal(t, http.StatusUnprocessableEntity, r.code, "a required key cannot go while it is on")

	// Off: kept, but not offered.
	r = e.do("PATCH", "/v1/admin/providers/dexcom", e.keyA, `{"enabled":false}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	got = adminProvider{}
	r.json(t, &got)
	assert.Equal(t, "off", got.Status)
	assert.False(t, got.Enabled)
	assert.True(t, got.Keys[1].Set)
	assert.Equal(t, []string{"apple_health"}, enabledSlugs(t, e))

	// Forget: back to nothing saved.
	r = e.do("DELETE", "/v1/admin/providers/dexcom", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	got = adminProvider{}
	r.json(t, &got)
	assert.Equal(t, "not_configured", got.Status)
	assert.True(t, got.Enabled)
	assert.Nil(t, got.UpdatedAt)
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", "/v1/admin/providers/dexcom", e.keyA, nil).code)

	// The audit log names the provider of each change.
	var page auditPage
	e.do("GET", "/v1/audit?action=config.write", e.keyA, nil).json(t, &page)
	require.NotEmpty(t, page.Data)
	for _, ev := range page.Data {
		if strings.HasPrefix(ev.Route, "PATCH /v1/admin/providers/") {
			assert.NotEmpty(t, ev.Detail["provider"], "%+v", ev)
		}
	}
}

func TestAdminProvidersFromEnvironment(t *testing.T) {
	e, _ := adminEnv(t, map[string]config.ProviderKeys{
		"dexcom": {"CLIENT_ID": "env-client", "CLIENT_SECRET": "env-secret"},
	})
	var got adminProvider
	r := e.do("GET", "/v1/admin/providers/dexcom", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &got)
	assert.Equal(t, "active", got.Status)
	assert.Equal(t, "env", *got.Source)
	assert.Equal(t, "env-client", *got.Keys[0].Value)
	assert.NotContains(t, string(r.body), "env-secret")

	r = e.do("PATCH", "/v1/admin/providers/dexcom", e.keyA, `{"keys":{"CLIENT_ID":"other"}}`)
	assert.Equal(t, http.StatusConflict, r.code, string(r.body))
	// The switch still applies.
	r = e.do("PATCH", "/v1/admin/providers/dexcom", e.keyA, `{"enabled":false}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	got = adminProvider{}
	r.json(t, &got)
	assert.Equal(t, "off", got.Status)
	assert.Equal(t, []string{"apple_health"}, enabledSlugs(t, e))
	r = e.do("PATCH", "/v1/admin/providers/dexcom", e.keyA, `{"enabled":true}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, []string{"apple_health", "dexcom"}, enabledSlugs(t, e))
}
