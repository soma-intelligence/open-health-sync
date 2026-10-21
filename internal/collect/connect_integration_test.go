// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/tenantkeys"
	"github.com/soma-intelligence/open-health-sync/internal/tenantproviders"
)

// connEnv is a database, a raw store, the fake provider in a private
// registry, and the collect routes mounted at their spec paths.
type connEnv struct {
	pool  *pgxpool.Pool
	raw   rawstore.Store
	box   *crypto.Box
	store *tenant.Store
	fake  *fakeProvider
	deps  collect.Deps
	mux   *http.ServeMux
	sched *collect.Scheduler
	clock atomic.Pointer[time.Time]
	// switches are the providers turned off per tenant, read by deps.Allowed.
	switches *tenantproviders.Set
	// tenantKeys are the provider keys tenants brought, read by
	// deps.TenantKeys.
	tenantKeys *tenantkeys.Set

	tenantA, tenantB uuid.UUID
	keyA, keyB       string
	userA            uuid.UUID
}

func newConnEnv(t *testing.T) *connEnv {
	return newConnEnvWith(t, collect.SchedulerConfig{Concurrency: 8})
}

func newConnEnvWith(t *testing.T, cfg collect.SchedulerConfig) *connEnv {
	t.Helper()
	return newConnEnvWrapped(t, cfg, func(c fakeConnector) connector.Connector { return c })
}

// newConnEnvWrapped registers wrap(fake) as the provider, so a test can add
// optional connector interfaces.
func newConnEnvWrapped(t *testing.T, cfg collect.SchedulerConfig, wrap func(fakeConnector) connector.Connector) *connEnv {
	t.Helper()
	return newConnEnvTweaked(t, cfg, wrap, nil)
}

// newConnEnvTweaked is newConnEnvWrapped with tweak, when set, applied to the
// Deps before the routes and the scheduler are built.
func newConnEnvTweaked(t *testing.T, cfg collect.SchedulerConfig, wrap func(fakeConnector) connector.Connector,
	tweak func(*collect.Deps)) *connEnv {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	raw, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := crypto.NewBox(key)
	require.NoError(t, err)

	e := &connEnv{pool: pool, raw: raw, box: box, store: tenant.New(pool), fake: newFakeProvider()}
	t.Cleanup(e.fake.Close)
	reg := connector.NewRegistry()
	reg.Register(wrap(fakeConnector{f: e.fake}))

	ta, keyA, err := e.store.CreateTenant(ctx, "A")
	require.NoError(t, err)
	tb, keyB, err := e.store.CreateTenant(ctx, "B")
	require.NoError(t, err)
	ua, err := e.store.CreateUser(ctx, ta.ID, "a-1")
	require.NoError(t, err)
	e.tenantA, e.tenantB, e.keyA, e.keyB, e.userA = ta.ID, tb.ID, keyA, keyB, ua.ID
	e.switches = tenantproviders.New(ctx, tenantproviders.NewPG(pool))
	e.tenantKeys = tenantkeys.New(ctx, tenantkeys.Options{Connectors: reg.All(),
		Store: tenantkeys.NewPG(pool, box, collect.RetireTenantKeys)})

	e.deps = collect.Deps{
		Pool: pool, Raw: raw, Box: box, Tenants: e.store, Lookup: reg.Get,
		Allowed:    e.switches.Allowed,
		TenantKeys: e.tenantKeys,
		PublicURL:  "https://ohs.example.test",
		Now: func() time.Time {
			if p := e.clock.Load(); p != nil {
				return *p
			}
			return time.Now()
		},
	}
	if tweak != nil {
		tweak(&e.deps)
	}
	h := collect.NewHandlers(e.deps)
	tk := auth.Middleware(e.store.LookupKey, auth.TenantKey)
	e.mux = http.NewServeMux()
	e.mux.Handle("POST /v1/users/{id}/connections/{provider}", tk(h.StartConnection()))
	e.mux.Handle("GET /v1/users/{id}/connections", tk(h.ListConnections()))
	e.mux.Handle("GET /v1/users/{id}/freshness", tk(h.Freshness()))
	e.mux.Handle("DELETE /v1/users/{id}/connections/{provider}", tk(h.DeleteConnection()))
	e.mux.Handle("POST /v1/users/{id}/connections/{provider}/sync", tk(h.SyncNow()))
	e.mux.Handle("GET /v1/users/{id}/sync-runs", tk(h.ListSyncRuns()))
	e.mux.Handle("GET /v1/users/{id}/sync-runs/{run}", tk(h.GetSyncRun()))
	e.mux.Handle("GET /oauth/{provider}/callback", h.OAuthCallback())
	e.mux.Handle("POST /inbound/{provider}", h.Inbound())
	e.mux.Handle("GET /inbound/{provider}", h.InboundProbe())
	e.sched = collect.NewScheduler(e.deps, cfg)
	return e
}

func (e *connEnv) setNow(t time.Time) { e.clock.Store(&t) }

func (e *connEnv) do(t *testing.T, method, path, key string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// start begins a connection and returns the state from the authorize URL.
func (e *connEnv) start(t *testing.T, key string, user uuid.UUID, redirect string) string {
	t.Helper()
	body := []byte(`{}`)
	if redirect != "" {
		body, _ = json.Marshal(collect.StartRequest{RedirectURI: redirect})
	}
	rec := e.do(t, http.MethodPost, "/v1/users/"+user.String()+"/connections/"+fakeSlug, key, body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp collect.StartResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	u, err := url.Parse(resp.AuthorizeURL)
	require.NoError(t, err)
	assert.Equal(t, "https://ohs.example.test/oauth/"+fakeSlug+"/callback", u.Query().Get("redirect_uri"))
	state := u.Query().Get("state")
	require.NotEmpty(t, state)
	return state
}

func (e *connEnv) callback(t *testing.T, state, code string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{"state": {state}, "code": {code}}
	return e.do(t, http.MethodGet, "/oauth/"+fakeSlug+"/callback?"+q.Encode(), "", nil)
}

// connect runs the whole flow for user and returns the provider user id.
func (e *connEnv) connect(t *testing.T, key string, user uuid.UUID, providerUser string) {
	t.Helper()
	state := e.start(t, key, user, "https://app.example.test/done")
	code := "code-" + providerUser
	e.fake.grant(code, providerUser)
	rec := e.callback(t, state, code)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "connected", loc.Query().Get("status"))
}

func (e *connEnv) scalar(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (e *connEnv) connStatus(t *testing.T, user uuid.UUID) string {
	t.Helper()
	var s string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT status FROM ohs_sync.connections WHERE user_id = $1 AND provider = $2`, user, fakeSlug).Scan(&s))
	return s
}

func (e *connEnv) jobs(t *testing.T, user uuid.UUID, status string) int {
	t.Helper()
	return e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE user_id = $1 AND status = $2`, user, status)
}

// drain pulls until no job is due.
func (e *connEnv) drain(t *testing.T) {
	t.Helper()
	for range 20 {
		n, err := e.sched.PullOnce(context.Background(), 16)
		require.NoError(t, err)
		if n == 0 {
			return
		}
	}
	t.Fatal("pull queue did not drain")
}

func TestConnectFlowStoresSealedTokensAndBackfill(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC)
	e.setNow(now)

	state := e.start(t, e.keyA, e.userA, "https://app.example.test/done?x=1")
	e.fake.grant("good-code", "pu-1")
	rec := e.callback(t, state, "good-code")
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "app.example.test", loc.Host)
	assert.Equal(t, "connected", loc.Query().Get("status"))
	assert.Equal(t, fakeSlug, loc.Query().Get("provider"))
	assert.Equal(t, "1", loc.Query().Get("x"))

	// Tokens are sealed: the raw columns do not hold the token bytes, and
	// they open only with this row's aad.
	var accessEnc, refreshEnc []byte
	var puid string
	require.NoError(t, e.pool.QueryRow(ctx, `
		SELECT access_token_enc, refresh_token_enc, provider_user_id, status FROM ohs_sync.connections
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`, e.tenantA, e.userA, fakeSlug).
		Scan(&accessEnc, &refreshEnc, &puid, new(string)))
	assert.Equal(t, "pu-1", puid)
	assert.NotContains(t, string(accessEnc), "access-secret")
	assert.NotContains(t, string(refreshEnc), "refresh-secret")
	aad := []byte(e.tenantA.String() + "|" + e.userA.String() + "|" + fakeSlug)
	plain, err := e.box.OpenFor(accessEnc, aad)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(plain), "access-secret-"))
	_, err = e.box.OpenFor(accessEnc, []byte(e.tenantB.String()+"|"+e.userA.String()+"|"+fakeSlug))
	assert.Error(t, err, "a sealed token must not open for another row")

	// 30 day backfill in 7 day jobs, contiguous, ending now.
	rows, err := e.pool.Query(ctx, `SELECT from_time, to_time FROM ohs_sync.pull_jobs
		WHERE tenant_id = $1 AND user_id = $2 ORDER BY from_time`, e.tenantA, e.userA)
	require.NoError(t, err)
	var windows [][2]time.Time
	for rows.Next() {
		var f, to time.Time
		require.NoError(t, rows.Scan(&f, &to))
		windows = append(windows, [2]time.Time{f.UTC(), to.UTC()})
	}
	require.NoError(t, rows.Err())
	require.Len(t, windows, 5)
	assert.Equal(t, now.Add(-30*24*time.Hour), windows[0][0])
	assert.Equal(t, now, windows[4][1])
	for i, w := range windows {
		assert.LessOrEqual(t, w[1].Sub(w[0]), 7*24*time.Hour)
		if i > 0 {
			assert.Equal(t, windows[i-1][1], w[0], "windows are contiguous")
		}
	}

	// Listed as active.
	list := e.do(t, http.MethodGet, "/v1/users/"+e.userA.String()+"/connections", e.keyA, nil)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var got collect.ConnectionList
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &got))
	require.Len(t, got.Data, 1)
	assert.Equal(t, fakeSlug, got.Data[0].Provider)
	assert.Equal(t, collect.StatusActive, got.Data[0].Status)
	assert.Nil(t, got.Data[0].LastSyncAt)
	require.NotNil(t, got.Data[0].InitialBackfillAt)
	assert.Equal(t, now, *got.Data[0].InitialBackfillAt)
	assert.False(t, got.Data[0].CreatedAt.IsZero())
	assert.Equal(t, got.Data[0].CreatedAt, got.Data[0].ConnectedAt)
	// Under the names api/openapi.yaml gives them, which the SDKs and the
	// console decode.
	var raw struct{ Data []map[string]any }
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &raw))
	for _, k := range []string{"scopes", "initial_backfill_at", "created_at", "updated_at"} {
		assert.Contains(t, raw.Data[0], k)
	}
}

func TestStateIsSingleUse(t *testing.T) {
	e := newConnEnv(t)
	state := e.start(t, e.keyA, e.userA, "https://app.example.test/done")
	e.fake.grant("c1", "pu-1")
	require.Equal(t, http.StatusFound, e.callback(t, state, "c1").Code)

	// Replaying the state: no tenant to go back to, nothing stored again.
	e.fake.grant("c2", "pu-2")
	rec := e.callback(t, state, "c2")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	assert.Empty(t, rec.Header().Get("Location"))
	var puid string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT provider_user_id FROM ohs_sync.connections WHERE user_id = $1`, e.userA).Scan(&puid))
	assert.Equal(t, "pu-1", puid)
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.oauth_states`))
}

func TestStateExpiresAfterTenMinutes(t *testing.T) {
	e := newConnEnv(t)
	t0 := time.Now()
	e.setNow(t0)
	state := e.start(t, e.keyA, e.userA, "https://app.example.test/done")

	var hashed string
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT state FROM ohs_sync.oauth_states`).Scan(&hashed))
	assert.NotEqual(t, state, hashed, "the state is stored hashed")

	e.setNow(t0.Add(10*time.Minute + time.Second))
	e.fake.grant("late", "pu-1")
	rec := e.callback(t, state, "late")
	require.Equal(t, http.StatusFound, rec.Code)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	assert.Equal(t, "error", loc.Query().Get("status"))
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.connections`))
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs`))

	// Within ten minutes it works.
	e.setNow(t0)
	state = e.start(t, e.keyA, e.userA, "https://app.example.test/done")
	e.setNow(t0.Add(9 * time.Minute))
	e.fake.grant("ok", "pu-1")
	rec = e.callback(t, state, "ok")
	loc, _ = url.Parse(rec.Header().Get("Location"))
	assert.Equal(t, "connected", loc.Query().Get("status"))
}

func TestCrossTenantCannotConnectOrSee(t *testing.T) {
	e := newConnEnv(t)
	userPath := "/v1/users/" + e.userA.String() + "/connections"

	// Tenant B cannot start a connection for tenant A's user.
	rec := e.do(t, http.MethodPost, userPath+"/"+fakeSlug, e.keyB, []byte(`{}`))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.oauth_states`))

	// A state minted by tenant A completes only for tenant A's user.
	e.connect(t, e.keyA, e.userA, "pu-1")
	var tenantID uuid.UUID
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT tenant_id FROM ohs_sync.connections WHERE user_id = $1`, e.userA).Scan(&tenantID))
	assert.Equal(t, e.tenantA, tenantID)
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.connections WHERE tenant_id = $1`, e.tenantB))

	assert.Equal(t, http.StatusNotFound, e.do(t, http.MethodGet, userPath, e.keyB, nil).Code)
	assert.Equal(t, http.StatusNotFound, e.do(t, http.MethodDelete, userPath+"/"+fakeSlug, e.keyB, nil).Code)
	assert.Equal(t, collect.StatusActive, e.connStatus(t, e.userA))
}

func TestCallbackWithoutRedirectRendersPage(t *testing.T) {
	e := newConnEnv(t)
	state := e.start(t, e.keyA, e.userA, "")
	e.fake.grant("c", "pu-1")
	rec := e.callback(t, state, "c")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), fakeSlug+" is connected")
	assert.NotContains(t, rec.Body.String(), "http", "the page loads no external asset")

	// Consent refused: error page.
	state = e.start(t, e.keyA, e.userA, "")
	q := url.Values{"state": {state}, "error": {"access_denied"}}
	rec = e.do(t, http.MethodGet, "/oauth/"+fakeSlug+"/callback?"+q.Encode(), "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "did not complete")
}

func TestStartRejectsBadInput(t *testing.T) {
	e := newConnEnv(t)
	base := "/v1/users/" + e.userA.String() + "/connections/"
	assert.Equal(t, http.StatusNotFound, e.do(t, http.MethodPost, base+"nosuch", e.keyA, nil).Code)
	assert.Equal(t, http.StatusBadRequest, e.do(t, http.MethodPost, base+fakeSlug, e.keyA, []byte(`{"redirect_uri":"javascript:alert(1)"}`)).Code)
	assert.Equal(t, http.StatusBadRequest, e.do(t, http.MethodPost, base+fakeSlug, e.keyA, []byte(`{"redirect_uri":"http://app.example.test/done"}`)).Code,
		"plain http only for loopback")
	assert.Equal(t, http.StatusBadRequest, e.do(t, http.MethodPost, base+fakeSlug, e.keyA, []byte(`{"redirect_uri":"https://u:p@app.example.test/done"}`)).Code)
	for _, ok := range []string{"http://localhost:3000/cb", "http://127.0.0.1/cb", "http://[::1]:8080/cb", "https://app.example.test/cb"} {
		body, _ := json.Marshal(collect.StartRequest{RedirectURI: ok})
		assert.Equal(t, http.StatusOK, e.do(t, http.MethodPost, base+fakeSlug, e.keyA, body).Code, ok)
	}
	assert.Equal(t, http.StatusBadRequest, e.do(t, http.MethodPost, base+fakeSlug, e.keyA, []byte(`not json`)).Code)
	assert.Equal(t, http.StatusNotFound, e.do(t, http.MethodPost, "/v1/users/"+uuid.NewString()+"/connections/"+fakeSlug, e.keyA, nil).Code)
	assert.Equal(t, http.StatusUnauthorized, e.do(t, http.MethodPost, base+fakeSlug, "", nil).Code)
}

func TestPullStoresRawObjectsAndQueuesETL(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)

	assert.Equal(t, 5, e.jobs(t, e.userA, "done"))
	// One raw object per emit (one per job), data type recorded, each with
	// its standard tier ETL job under the same key.
	assert.Equal(t, 5, e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects WHERE user_id = $1 AND data_type = 'steps' AND provider = $2`, e.userA, fakeSlug))
	assert.Equal(t, 5, e.scalar(t, `SELECT count(*) FROM ohs_sync.etl_jobs j JOIN ohs_sync.raw_objects r USING (key)
		WHERE j.user_id = $1 AND j.tier = 'standard'`, e.userA))
	assert.Equal(t, 5, e.scalar(t, `SELECT count(*) FROM ohs_sync.sync_log WHERE user_id = $1 AND stage = 'pull' AND status = 'completed'`, e.userA))

	var key string
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT key FROM ohs_sync.raw_objects WHERE user_id = $1 ORDER BY id LIMIT 1`, e.userA).Scan(&key))
	assert.True(t, strings.HasPrefix(key, "raw/"+e.tenantA.String()+"/"+e.userA.String()+"/"+fakeSlug+"/"), key)
	assert.True(t, strings.HasSuffix(key, "_0.ndjson.gz"), key)
	rc, err := e.raw.Get(ctx, key)
	require.NoError(t, err)
	zr, err := gzip.NewReader(rc)
	require.NoError(t, err)
	content, err := io.ReadAll(zr)
	require.NoError(t, err)
	_ = rc.Close()
	assert.Equal(t, 2, strings.Count(string(content), "\n"))
	assert.Contains(t, string(content), `"value":100`)

	list := e.do(t, http.MethodGet, "/v1/users/"+e.userA.String()+"/connections", e.keyA, nil)
	var got collect.ConnectionList
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &got))
	require.NotNil(t, got.Data[0].LastSyncAt)

	// A second pull of the same window stores nothing new (content dedup).
	tx, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO ohs_sync.pull_jobs (tenant_id, user_id, provider, from_time, to_time)
		SELECT tenant_id, user_id, provider, from_time, to_time FROM ohs_sync.pull_jobs WHERE user_id = $1 ORDER BY id LIMIT 1`, e.userA)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	e.drain(t)
	assert.Equal(t, 5, e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects WHERE user_id = $1`, e.userA))
	files, err := e.raw.List(ctx, "raw/")
	require.NoError(t, err)
	assert.Len(t, files, 5, "a duplicate is not written to the raw store")
}

func TestRefreshRenewsExpiringTokens(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	var before []byte
	require.NoError(t, e.pool.QueryRow(ctx, `UPDATE ohs_sync.connections SET token_expires_at = now() + interval '1 minute'
		WHERE user_id = $1 RETURNING access_token_enc`, e.userA).Scan(&before))

	e.drain(t)
	assert.Equal(t, int64(1), e.fake.refreshN.Load(), "five pulls share one refresh")
	assert.Equal(t, 5, e.jobs(t, e.userA, "done"))
	var after []byte
	var exp time.Time
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT access_token_enc, token_expires_at FROM ohs_sync.connections WHERE user_id = $1`, e.userA).Scan(&after, &exp))
	assert.NotEqual(t, before, after)
	assert.True(t, exp.After(time.Now().Add(30*time.Minute)))
	aad := []byte(e.tenantA.String() + "|" + e.userA.String() + "|" + fakeSlug)
	plain, err := e.box.OpenFor(after, aad)
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+string(plain), e.fake.lastAuthorization(), "the pull used the refreshed token")
}

// RFC 6749 section 6: a refresh response MAY carry a new refresh token, and
// the client must then discard the old one; when it carries none, the old one
// stays valid and is kept. A provider that rotates (the fake's default) gets
// the new token stored; one that does not keeps the stored token, which the
// next refresh sends again.
func TestRefreshKeepsRefreshTokenWhenNoneIssued(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	aad := []byte(e.tenantA.String() + "|" + e.userA.String() + "|" + fakeSlug)
	storedRefresh := func() string {
		t.Helper()
		var enc []byte
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT refresh_token_enc FROM ohs_sync.connections
			WHERE tenant_id = $1 AND user_id = $2`, e.tenantA, e.userA).Scan(&enc))
		plain, err := e.box.OpenFor(enc, aad)
		require.NoError(t, err)
		return string(plain)
	}
	original := storedRefresh()
	require.NotEmpty(t, original)

	// The provider refuses the access token and issues no new refresh token.
	e.fake.refreshMode.Store("keep")
	e.fake.rejectCurrentTokens()
	e.drain(t)
	require.Equal(t, int64(1), e.fake.refreshN.Load())
	assert.Equal(t, original, e.fake.lastRefresh.Load(), "the refresh spent the stored token")
	assert.Equal(t, original, storedRefresh(), "no new refresh token: the old one is kept")
	assert.Equal(t, collect.StatusActive, e.connStatus(t, e.userA))

	// Next time it rotates: the kept token is the one sent, and the new one
	// replaces it.
	e.fake.refreshMode.Store("ok")
	e.fake.rejectCurrentTokens()
	e.notify(t, "pu-1", 1760000000, 1760003600)
	e.drain(t)
	require.Equal(t, int64(2), e.fake.refreshN.Load())
	assert.Equal(t, original, e.fake.lastRefresh.Load(), "the kept token still works")
	rotated := storedRefresh()
	assert.NotEqual(t, original, rotated, "a rotated token replaces the old one")
	assert.True(t, strings.HasPrefix(rotated, "refresh-secret-"))
}

func TestRefreshNeedsReauth(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	userB, err := e.store.CreateUser(ctx, e.tenantA, "a-2")
	require.NoError(t, err)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.connect(t, e.keyA, userB.ID, "pu-2")

	// User A's token is about to expire and the provider revoked the grant.
	_, err = e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET token_expires_at = now() - interval '1 minute' WHERE user_id = $1`, e.userA)
	require.NoError(t, err)
	e.fake.refreshMode.Store("revoked")
	// One pull of user A (oldest jobs first) hits the revoked grant: its four
	// other jobs are parked while still pending, never claimed.
	n, err := e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, 5, e.jobs(t, e.userA, "parked"))
	e.drain(t)
	assert.Equal(t, int64(1), e.fake.refreshN.Load(), "the revoked grant is tried once")

	assert.Equal(t, collect.StatusNeedsReauth, e.connStatus(t, e.userA))
	assert.Equal(t, 0, e.jobs(t, e.userA, "pending")+e.jobs(t, e.userA, "running")+e.jobs(t, e.userA, "done"))
	assert.Equal(t, 5, e.jobs(t, e.userA, "parked"))
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects WHERE user_id = $1`, e.userA))

	// The second connection keeps its jobs.
	assert.Equal(t, collect.StatusActive, e.connStatus(t, userB.ID))
	assert.Equal(t, 5, e.jobs(t, userB.ID, "done"))

	// Nothing more is scheduled for it: neither the regular pull nor a
	// provider notification.
	e.setNow(time.Now().Add(7 * time.Hour))
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, e.jobs(t, userB.ID, "pending"))
	assert.Equal(t, 0, e.jobs(t, e.userA, "pending"))
	body := []byte(`{"userid":"pu-1","startdate":"1760000000","enddate":"1760003600"}`)
	req := httptest.NewRequest(http.MethodPost, "/inbound/"+fakeSlug, bytes.NewReader(body))
	req.Header.Set("X-Fake-Signature", sign(body))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, e.jobs(t, e.userA, "pending"))

	list := e.do(t, http.MethodGet, "/v1/users/"+e.userA.String()+"/connections", e.keyA, nil)
	var got collect.ConnectionList
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &got))
	assert.Equal(t, collect.StatusNeedsReauth, got.Data[0].Status)

	// Connecting again revives it with a fresh backfill.
	e.fake.refreshMode.Store("ok")
	e.setNow(time.Now())
	e.connect(t, e.keyA, e.userA, "pu-1")
	assert.Equal(t, collect.StatusActive, e.connStatus(t, e.userA))
	assert.Equal(t, 5, e.jobs(t, e.userA, "pending"))
}

func TestFetchNeedsReauth(t *testing.T) {
	e := newConnEnv(t)
	_, err := e.pool.Exec(context.Background(), `INSERT INTO ohs_core.webhook_endpoints (tenant_id, url, event_types, secret_sealed)
		VALUES ($1, 'https://example.com/hook', '{connection.needs_reauth}', '\x00')`, e.tenantA)
	require.NoError(t, err)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.fake.fetchMode.Store("revoked")
	e.drain(t)
	assert.Equal(t, collect.StatusNeedsReauth, e.connStatus(t, e.userA))
	assert.Equal(t, 5, e.jobs(t, e.userA, "parked"))

	// The tenant's endpoint is told once, though five jobs met the refusal.
	var events int
	var provider, user string
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*), min(payload #>> '{data,provider}'),
		min(payload #>> '{data,user_id}') FROM ohs_core.webhook_deliveries
		WHERE tenant_id = $1 AND event_type = 'connection.needs_reauth'`, e.tenantA).Scan(&events, &provider, &user))
	assert.Equal(t, 1, events)
	assert.Equal(t, fakeSlug, provider)
	assert.Equal(t, e.userA.String(), user)
}

func TestRateLimitDefers(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.fake.fetchMode.Store("429")
	fixed := time.Now().UTC().Truncate(time.Microsecond)
	e.setNow(fixed)

	n, err := e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	var status string
	var attempts int
	var notBefore time.Time
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT status, attempts, not_before FROM ohs_sync.pull_jobs
		WHERE user_id = $1 AND not_before > now() + interval '1 minute'`, e.userA).Scan(&status, &attempts, &notBefore))
	assert.Equal(t, "pending", status)
	assert.Equal(t, 0, attempts, "a rate limit burns no attempt")
	assert.Equal(t, fixed.Add(120*time.Second), notBefore.UTC())
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.sync_log`))
	assert.Equal(t, collect.StatusActive, e.connStatus(t, e.userA))
}

func TestPullFailureRetriesWithBackoff(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.fake.fetchMode.Store("500")
	n, err := e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE attempts = 1 AND status = 'pending' AND last_error IS NOT NULL`))
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.sync_log WHERE status = 'error'`))
}

func TestInboundBadSignatureIs401(t *testing.T) {
	e := newConnEnv(t)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	before := e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs`)

	body := []byte(`{"userid":"pu-1","startdate":"1760000000","enddate":"1760003600"}`)
	req := httptest.NewRequest(http.MethodPost, "/inbound/"+fakeSlug, bytes.NewReader(body))
	req.Header.Set("X-Fake-Signature", "00"+sign(body)[2:])
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, before, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs`))
}

func TestInboundEnqueuesPull(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)

	body := []byte(`{"userid":"pu-1","startdate":"1760000000","enddate":"1760003600"}`)
	for range 2 { // a repeated notification does not double the job
		req := httptest.NewRequest(http.MethodPost, "/inbound/"+fakeSlug, bytes.NewReader(body))
		req.Header.Set("X-Fake-Signature", sign(body))
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	var from, to time.Time
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT from_time, to_time FROM ohs_sync.pull_jobs
		WHERE user_id = $1 AND status = 'pending'`, e.userA).Scan(&from, &to))
	assert.Equal(t, time.Unix(1760000000, 0).UTC(), from.UTC())
	assert.Equal(t, time.Unix(1760003600, 0).UTC(), to.UTC())

	// Unknown provider user: accepted, nothing enqueued.
	body = []byte(`{"userid":"nobody","startdate":"1760000000","enddate":"1760003600"}`)
	req := httptest.NewRequest(http.MethodPost, "/inbound/"+fakeSlug, bytes.NewReader(body))
	req.Header.Set("X-Fake-Signature", sign(body))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE status = 'pending'`))
}

func TestScheduleEverySixHoursWhileNotificationsArrive(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	connectedAt := time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC)
	e.setNow(connectedAt)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	// A notification arrived recently: the regular pull is a 6 hour backstop.
	_, err := e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET last_event_at = $1`, connectedAt)
	require.NoError(t, err)

	// Same slot as the connect: the backfill covers it.
	n, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// Next slot boundary (18:00): one job ending there, once.
	e.setNow(time.Date(2026, 10, 10, 18, 30, 0, 0, time.UTC))
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	var from, to time.Time
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT from_time, to_time FROM ohs_sync.pull_jobs WHERE status = 'pending'`).Scan(&from, &to))
	assert.Equal(t, time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC), to.UTC())
	assert.LessOrEqual(t, from.UTC(), time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC))

	// An inbound burst that widens the slot job does not get the slot
	// scheduled again.
	e.notify(t, "pu-1", time.Date(2026, 10, 10, 17, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 10, 10, 19, 0, 0, 0, time.UTC).Unix())
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT to_time FROM ohs_sync.pull_jobs WHERE status = 'pending'`).Scan(&to))
	assert.Equal(t, time.Date(2026, 10, 10, 19, 0, 0, 0, time.UTC), to.UTC())
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// A connection without a backfill gets one from the scheduler.
	_, err = e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET initial_backfill_at = NULL`)
	require.NoError(t, err)
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 6, e.jobs(t, e.userA, "pending"))
}

func TestDeleteConnectionRevokesAndWipes(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	path := "/v1/users/" + e.userA.String() + "/connections/" + fakeSlug

	rec := e.do(t, http.MethodDelete, path, e.keyA, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Len(t, e.fake.revokedTokens(), 1)
	assert.True(t, strings.HasPrefix(e.fake.revokedTokens()[0], "access-secret-"))

	assert.Equal(t, collect.StatusDisconnected, e.connStatus(t, e.userA))
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.connections
		WHERE access_token_enc IS NULL AND refresh_token_enc IS NULL AND token_expires_at IS NULL`))
	assert.Equal(t, 0, e.jobs(t, e.userA, "pending"))
	assert.Equal(t, 5, e.jobs(t, e.userA, "parked"))

	// Idempotent, and nothing scheduled any more.
	assert.Equal(t, http.StatusNoContent, e.do(t, http.MethodDelete, path, e.keyA, nil).Code)
	assert.Len(t, e.fake.revokedTokens(), 1)
	e.setNow(time.Now().Add(7 * time.Hour))
	n, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	list := e.do(t, http.MethodGet, "/v1/users/"+e.userA.String()+"/connections", e.keyA, nil)
	var got collect.ConnectionList
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &got))
	assert.Equal(t, collect.StatusDisconnected, got.Data[0].Status)

	assert.Equal(t, http.StatusNotFound, e.do(t, http.MethodDelete, "/v1/users/"+e.userA.String()+"/connections/other", e.keyA, nil).Code)
}

func TestRunStopsOnCancel(t *testing.T) {
	e := newConnEnv(t)
	e.connect(t, e.keyA, e.userA, "pu-1")
	s := collect.NewScheduler(e.deps, collect.SchedulerConfig{Concurrency: 2, Tick: 50 * time.Millisecond,
		IdleMin: 20 * time.Millisecond, IdleMax: 40 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	require.Eventually(t, func() bool { return e.jobs(t, e.userA, "done") == 5 }, 20*time.Second, 50*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// A pull stuck at the provider is cancelled ShutdownGrace after Run's
// context ends, so Run returns in bounded time.
func TestRunShutdownGraceCancelsStuckPull(t *testing.T) {
	e := newConnEnv(t)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.fake.fetchMode.Store("hang")
	const grace = 300 * time.Millisecond
	s := collect.NewScheduler(e.deps, collect.SchedulerConfig{Concurrency: 1, Tick: time.Hour,
		IdleMin: 20 * time.Millisecond, IdleMax: 40 * time.Millisecond, ShutdownGrace: grace})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	require.Eventually(t, func() bool { return e.fake.fetchN.Load() > 0 }, 20*time.Second, 20*time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop after the grace")
	}
	took := time.Since(start)
	assert.GreaterOrEqual(t, took, grace, "the pull got its grace")
	assert.Less(t, took, 5*time.Second)
}

func TestUserDeletedMidPullWritesNothing(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	hook := func() {
		_, err := e.pool.Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, e.userA)
		require.NoError(t, err)
	}
	e.fake.beforeEmit.Store(&hook)

	n, err := e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects`))
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.etl_jobs`))
	files, err := e.raw.List(ctx, "raw/")
	require.NoError(t, err)
	assert.Empty(t, files)
	assert.Equal(t, 1, e.jobs(t, e.userA, "parked"))

	// Nothing more is scheduled or enqueued for that user.
	e.fake.beforeEmit.Store(nil)
	e.setNow(time.Now().Add(7 * time.Hour))
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	body := []byte(`{"userid":"pu-1","startdate":"1760000000","enddate":"1760003600"}`)
	req := httptest.NewRequest(http.MethodPost, "/inbound/"+fakeSlug, bytes.NewReader(body))
	req.Header.Set("X-Fake-Signature", sign(body))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE from_time = to_timestamp(1760000000)`))
}

func TestDisabledProvidersDoNotStarveTheScheduler(t *testing.T) {
	e := newConnEnvWith(t, collect.SchedulerConfig{Concurrency: 8, Batch: 2})
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)

	// More connections than one batch, of a provider this instance lacks,
	// older and never synced: they sort first but must not fill the batch.
	for i := range 3 {
		u, err := e.store.CreateUser(ctx, e.tenantA, "off-"+string(rune('a'+i)))
		require.NoError(t, err)
		_, err = e.pool.Exec(ctx, `INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, created_at, initial_backfill_at)
			VALUES ($1, $2, 'disabledfit', now() - interval '1 year', NULL),
			       ($1, $2, 'disabledfit2', now() - interval '1 year', now() - interval '1 year')`, e.tenantA, u.ID)
		require.NoError(t, err)
	}
	userB, err := e.store.CreateUser(ctx, e.tenantA, "fresh")
	require.NoError(t, err)
	_, err = e.pool.Exec(ctx, `INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, access_token_enc, initial_backfill_at)
		VALUES ($1, $2, $3, '\x00', NULL)`, e.tenantA, userB.ID, fakeSlug)
	require.NoError(t, err)

	e.setNow(time.Now().Add(7 * time.Hour))
	n, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the enabled connection's regular pull and the other's backfill")
	assert.Equal(t, 1, e.jobs(t, e.userA, "pending"))
	assert.Equal(t, 5, e.jobs(t, userB.ID, "pending"))
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE provider LIKE 'disabled%'`))
}

func TestReconnectWhileActiveSkipsBackfill(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	t0 := time.Now()
	e.setNow(t0)
	e.connect(t, e.keyA, e.userA, "pu-1")
	var before []byte
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT access_token_enc FROM ohs_sync.connections`).Scan(&before))
	e.setNow(t0.Add(time.Hour)) // other backfill windows, were one enqueued
	e.connect(t, e.keyA, e.userA, "pu-1")
	assert.Equal(t, 5, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs`))
	var after []byte
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT access_token_enc FROM ohs_sync.connections`).Scan(&after))
	assert.NotEqual(t, before, after, "the new tokens are stored")
}

func TestInboundFansOutAcrossTenants(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	userB, err := e.store.CreateUser(ctx, e.tenantB, "b-1")
	require.NoError(t, err)
	e.connect(t, e.keyA, e.userA, "shared")
	e.connect(t, e.keyB, userB.ID, "shared")
	e.drain(t)

	body := []byte(`{"userid":"shared","startdate":"1760000000","enddate":"1760003600"}`)
	req := httptest.NewRequest(http.MethodPost, "/inbound/"+fakeSlug, bytes.NewReader(body))
	req.Header.Set("X-Fake-Signature", sign(body))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, e.jobs(t, e.userA, "pending"))
	assert.Equal(t, 1, e.jobs(t, userB.ID, "pending"))
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE tenant_id = $1 AND status = 'pending'`, e.tenantB))
}

// An event carrying raw lines is stored raw first, for every connection of
// the provider user, and queued for the ETL; nothing is pulled. The same
// notification delivered again is stored once.
func TestInboundStoresRawLines(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	userB, err := e.store.CreateUser(ctx, e.tenantB, "b-1")
	require.NoError(t, err)
	e.connect(t, e.keyA, e.userA, "shared")
	e.connect(t, e.keyB, userB.ID, "shared")
	e.drain(t)
	pulls := e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs`)
	raws := e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects`)

	body := []byte(`{"userid":"shared","deleted":"rec-1"}`)
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/inbound/"+fakeSlug, bytes.NewReader(body))
		req.Header.Set("X-Fake-Signature", sign(body))
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	assert.Equal(t, pulls, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs`), "a stored notice queues no pull")
	assert.Equal(t, raws+2, e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects`), "one object per connection, once")
	for _, o := range []struct{ tenant, user uuid.UUID }{{e.tenantA, e.userA}, {e.tenantB, userB.ID}} {
		var key string
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT key FROM ohs_sync.raw_objects
			WHERE tenant_id = $1 AND user_id = $2 AND data_type = 'deletion'`, o.tenant, o.user).Scan(&key))
		assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.etl_jobs WHERE tenant_id = $1 AND user_id = $2 AND key = $3`,
			o.tenant, o.user, key))
		rc, err := e.deps.Raw.Get(ctx, key)
		require.NoError(t, err)
		zr, err := gzip.NewReader(rc)
		require.NoError(t, err)
		stored, err := io.ReadAll(zr)
		require.NoError(t, err)
		rc.Close()
		assert.Equal(t, string(body)+"\n", string(stored), "the notification is stored verbatim")
	}

	// A provider user with no connection: accepted, nothing stored.
	body = []byte(`{"userid":"nobody","deleted":"rec-2"}`)
	req := httptest.NewRequest(http.MethodPost, "/inbound/"+fakeSlug, bytes.NewReader(body))
	req.Header.Set("X-Fake-Signature", sign(body))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, raws+2, e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects`))
}

func TestInboundTooLargeAndProbe(t *testing.T) {
	e := newConnEnv(t)
	body := append([]byte(`{"userid":"pu-1","pad":"`), bytes.Repeat([]byte("x"), 1<<20)...)
	body = append(body, []byte(`"}`)...)
	req := httptest.NewRequest(http.MethodPost, "/inbound/"+fakeSlug, bytes.NewReader(body))
	req.Header.Set("X-Fake-Signature", sign(body))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs`))

	for _, m := range []string{http.MethodHead, http.MethodGet} {
		rec := e.do(t, m, "/inbound/"+fakeSlug, "", nil)
		assert.Equal(t, http.StatusOK, rec.Code, m)
		assert.Empty(t, rec.Body.String(), m)
	}
	assert.Equal(t, http.StatusNotFound, e.do(t, http.MethodGet, "/inbound/nosuch", "", nil).Code)
}

func TestWrongProviderCallbackKeepsState(t *testing.T) {
	e := newConnEnv(t)
	state := e.start(t, e.keyA, e.userA, "https://app.example.test/done")
	q := url.Values{"state": {state}, "code": {"c"}}
	rec := e.do(t, http.MethodGet, "/oauth/otherfit/callback?"+q.Encode(), "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.oauth_states`))

	e.fake.grant("c", "pu-1")
	rec = e.callback(t, state, "c")
	loc, _ := url.Parse(rec.Header().Get("Location"))
	assert.Equal(t, "connected", loc.Query().Get("status"))
}

func TestFetchRefusedTokenIsRefreshedAndRetried(t *testing.T) {
	e := newConnEnv(t)
	e.connect(t, e.keyA, e.userA, "pu-1")
	// The access token still looks valid for an hour, but the provider
	// refuses it (401): one refresh, one retry, and the pull succeeds.
	e.fake.rejectCurrentTokens()
	e.drain(t)
	assert.Equal(t, int64(1), e.fake.refreshN.Load(), "five pulls share one forced refresh")
	assert.Equal(t, collect.StatusActive, e.connStatus(t, e.userA))
	assert.Equal(t, 5, e.jobs(t, e.userA, "done"))
	assert.Equal(t, 5, e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects`))
}

func (e *connEnv) notify(t *testing.T, providerUser string, from, to int64) {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"userid":%q,"startdate":"%d","enddate":"%d"}`, providerUser, from, to))
	req := httptest.NewRequest(http.MethodPost, "/inbound/"+fakeSlug, bytes.NewReader(body))
	req.Header.Set("X-Fake-Signature", sign(body))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestInboundCoalescesBursts(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)

	const h = 3600
	base := int64(1760000000)
	e.notify(t, "pu-1", base, base+h)         // new job
	e.notify(t, "pu-1", base+600, base+1200)  // covered
	e.notify(t, "pu-1", base+h/2, base+2*h)   // overlaps: widened
	e.notify(t, "pu-1", base+30*h, base+31*h) // disjoint: new job
	e.notify(t, "pu-1", base-200*h, base+h/2) // union over 7 days: new job

	rows, err := e.pool.Query(ctx, `SELECT extract(epoch FROM from_time)::bigint, extract(epoch FROM to_time)::bigint
		FROM ohs_sync.pull_jobs WHERE status = 'pending' ORDER BY from_time`)
	require.NoError(t, err)
	var got [][2]int64
	for rows.Next() {
		var f, to int64
		require.NoError(t, rows.Scan(&f, &to))
		got = append(got, [2]int64{f, to})
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, [][2]int64{{base - 200*h, base + h/2}, {base, base + 2*h}, {base + 30*h, base + 31*h}}, got)
}

func TestSecondRefusalAfterReconnectKeepsConnection(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.fake.fetchMode.Store("revoked")
	// First refusal: collect refreshes and retries. Before the second
	// refusal is answered, the user reconnects (new tokens).
	hook := func(n int64) {
		if n == 2 {
			e.connect(t, e.keyA, e.userA, "pu-1")
		}
	}
	e.fake.onRefusal.Store(&hook)

	n, err := e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, int64(2), e.fake.refusalN.Load())
	assert.Equal(t, collect.StatusActive, e.connStatus(t, e.userA), "the refused token was already replaced")
	assert.Equal(t, 0, e.jobs(t, e.userA, "parked"))
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE user_id = $1 AND attempts = 1 AND status = 'pending'`, e.userA),
		"the job is retried with the new token")
}

func TestInboundJobDoesNotHideSlotCatchUp(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.setNow(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	slot := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	lastSync := slot.Add(-72 * time.Hour)
	_, err := e.pool.Exec(ctx, `UPDATE ohs_sync.connections SET last_sync_at = $1`, lastSync)
	require.NoError(t, err)

	// A pending inbound job for the last day crosses the slot end.
	e.notify(t, "pu-1", slot.Add(-23*time.Hour).Unix(), slot.Add(time.Hour).Unix())
	e.setNow(slot.Add(30 * time.Minute))
	n, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the slot pull with its catch-up is still scheduled")
	var from time.Time
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT from_time FROM ohs_sync.pull_jobs WHERE to_time = $1`, slot).Scan(&from))
	assert.Equal(t, lastSync.Add(-time.Hour), from.UTC())

	// A job covering the whole catch-up window does count.
	_, err = e.pool.Exec(ctx, `DELETE FROM ohs_sync.pull_jobs WHERE to_time = $1`, slot)
	require.NoError(t, err)
	_, err = e.pool.Exec(ctx, `UPDATE ohs_sync.pull_jobs SET from_time = $1 WHERE status = 'pending'`, lastSync.Add(-time.Hour))
	require.NoError(t, err)
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

// A user whose deletion starts during the code exchange: no connection is
// stored and the grant the provider just issued is revoked again.
func TestCallbackRevokesGrantOfUserBeingDeleted(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	state := e.start(t, e.keyA, e.userA, "https://app.example.test/done")
	e.fake.grant("code-del", "pu-del")
	hook := func() {
		_, err := e.pool.Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, e.userA)
		require.NoError(t, err)
	}
	e.fake.afterExchange.Store(&hook)

	rec := e.callback(t, state, "code-del")
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Header().Get("Location"), "status=error")
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.connections`))
	revoked := e.fake.revokedTokens()
	require.Len(t, revoked, 1, "the fresh grant is revoked")
	assert.True(t, strings.HasPrefix(revoked[0], "access-secret-"))
}

// holdDeleting starts marking the user deleting in an open transaction (the
// row lock a DELETE /v1/users request holds) and returns its commit.
func (e *connEnv) holdDeleting(t *testing.T, user uuid.UUID) (commit func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	// Registered before any assertion can stop the test: an open
	// transaction would keep pool.Close waiting forever.
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	_, err = tx.Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, user)
	require.NoError(t, err)
	return func() { require.NoError(t, tx.Commit(ctx)) }
}

// A deletion that starts while the scheduler picks connections: the user
// row is held FOR SHARE, so the scheduler waits, sees the user deleting and
// writes no job the deletion would miss.
func TestScheduleWaitsForDeletionInFlight(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-race")
	e.drain(t)
	before := e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE user_id = $1`, e.userA)
	e.setNow(time.Now().Add(7 * time.Hour))

	commit := e.holdDeleting(t, e.userA)
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := e.sched.Schedule(ctx)
		done <- result{n, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("Schedule did not wait for the deletion in flight: n=%d err=%v", r.n, r.err)
	case <-time.After(300 * time.Millisecond):
	}
	commit()
	r := <-done
	require.NoError(t, r.err)
	assert.Equal(t, 0, r.n)
	assert.Equal(t, before, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE user_id = $1`, e.userA))
}

// The same race on an inbound notification.
func TestInboundWaitsForDeletionInFlight(t *testing.T) {
	e := newConnEnv(t)
	e.connect(t, e.keyA, e.userA, "pu-race-in")
	commit := e.holdDeleting(t, e.userA)
	body := []byte(`{"userid":"pu-race-in","startdate":"1760000000","enddate":"1760003600"}`)
	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/inbound/"+fakeSlug, bytes.NewReader(body))
		req.Header.Set("X-Fake-Signature", sign(body))
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, req)
		done <- rec.Code
	}()
	select {
	case code := <-done:
		t.Fatalf("inbound did not wait for the deletion in flight: %d", code)
	case <-time.After(300 * time.Millisecond):
	}
	commit()
	assert.Equal(t, http.StatusOK, <-done)
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs WHERE from_time = to_timestamp(1760000000)`))
}
