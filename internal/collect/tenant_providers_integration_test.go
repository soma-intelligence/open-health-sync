// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/tenantproviders"
)

// turn sets the fake provider's switch for a tenant, as the admin route does.
func (e *connEnv) turn(t *testing.T, tenantID uuid.UUID, enabled bool) {
	t.Helper()
	_, err := e.switches.Update(context.Background(), tenantID, fakeSlug, enabled, "test-key", nil)
	require.NoError(t, err)
}

func detailOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct{ Detail string }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p), rec.Body.String())
	return p.Detail
}

// While the provider is off for a tenant, its users cannot connect it or
// sync it; the connection and what it stored are kept, and another tenant
// is not affected. Turned back on, both work again.
func TestProviderOffRefusesConnectAndSync(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	stored := e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects WHERE user_id = $1`, e.userA)
	require.Positive(t, stored)
	userB, err := e.store.CreateUser(ctx, e.tenantB, "b-1")
	require.NoError(t, err)

	e.turn(t, e.tenantA, false)
	rec := e.do(t, http.MethodPost, "/v1/users/"+e.userA.String()+"/connections/"+fakeSlug, e.keyA, []byte(`{}`))
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, collect.ProviderOffDetail, detailOf(t, rec))
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.oauth_states`), "no state minted")
	rec = e.do(t, http.MethodPost, "/v1/users/"+e.userA.String()+"/connections/nope", e.keyA, []byte(`{}`))
	assert.Equal(t, http.StatusNotFound, rec.Code, "a provider the instance does not run is still unknown")

	rec = e.do(t, http.MethodPost, "/v1/users/"+e.userA.String()+"/connections/"+fakeSlug+"/sync", e.keyA, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, collect.ProviderOffDetail, detailOf(t, rec))
	assert.Equal(t, 0, e.jobs(t, e.userA, "pending"))

	// Kept: the connection is still active and listed, the data still there.
	assert.Equal(t, collect.StatusActive, e.connStatus(t, e.userA))
	assert.Equal(t, collect.StatusActive, e.health(t).Status)
	assert.Equal(t, stored, e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects WHERE user_id = $1`, e.userA))

	// Another tenant keeps the provider.
	e.start(t, e.keyB, userB.ID, "")

	e.turn(t, e.tenantA, true)
	e.start(t, e.keyA, e.userA, "")
	code, _, _ := e.syncNow(t, "")
	assert.Equal(t, http.StatusAccepted, code)
}

// A consent started before the switch went off does not store the
// connection: the code is not even exchanged.
func TestProviderOffRefusesTheCallback(t *testing.T) {
	e := newConnEnv(t)
	state := e.start(t, e.keyA, e.userA, "https://app.example.test/done")
	e.fake.grant("c1", "pu-1")
	e.turn(t, e.tenantA, false)
	rec := e.callback(t, state, "c1")
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "error", loc.Query().Get("status"))
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.connections`))
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.pull_jobs`))
	assert.Zero(t, e.fake.issued.Load(), "no token was issued")

	// Without a redirect: the result page, 403.
	e.turn(t, e.tenantA, true)
	state = e.start(t, e.keyA, e.userA, "")
	e.turn(t, e.tenantA, false)
	rec = e.callback(t, state, "c1")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, rec.Body.String(), "did not complete")
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.connections`))
}

// The hosted page leaves the provider out, refuses it by name (403), and a
// connect session cannot name it.
func TestProviderOffOnTheConnectPage(t *testing.T) {
	e := newPageEnv(t)
	token, _ := e.createSession(t, e.keyA, e.userA, `{}`)
	e.turn(t, e.tenantA, false)

	rec := e.do(t, http.MethodGet, "/connect?session="+token, "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), `action="connect/`+fakeSlug+`"`)
	rec = e.do(t, http.MethodGet, "/connect/"+fakeSlug+"?session="+token, "", nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "This source is turned off")
	rec = e.postConnect(t, fakeSlug, token)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.oauth_states`))

	rec = e.do(t, http.MethodPost, "/v1/users/"+e.userA.String()+"/connect-sessions", e.keyA,
		[]byte(`{"providers":["`+fakeSlug+`"]}`))
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Contains(t, detailOf(t, rec), collect.ProviderOffDetail)
	rec = e.do(t, http.MethodPost, "/v1/users/"+e.userA.String()+"/connect-sessions", e.keyA, []byte(`{}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code, "no provider left to offer")

	e.turn(t, e.tenantA, true)
	e.consentState(t, e.postConnect(t, fakeSlug, token))
}

// The scheduler plans no pull for a tenant that turned the provider off,
// and a pull already queued ends cancelled, without fetching, failing or
// retrying. Turned back on, the pulls resume.
func TestProviderOffStopsThePulls(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC)
	e.setNow(at)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	userB, err := e.store.CreateUser(ctx, e.tenantB, "b-1")
	require.NoError(t, err)
	e.connect(t, e.keyB, userB.ID, "pu-2")
	e.drain(t)

	// Due at the next slot, but off for A: only B's pull is planned.
	e.setNow(at.Add(time.Hour))
	e.turn(t, e.tenantA, false)
	n, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 0, e.jobs(t, e.userA, "pending"))
	assert.Equal(t, 1, e.jobs(t, userB.ID, "pending"))

	// A connection without its backfill is not backfilled either.
	e.exec(t, `UPDATE ohs_sync.connections SET initial_backfill_at = NULL WHERE user_id = $1`, e.userA)
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, 0, e.jobs(t, e.userA, "pending"))
	e.exec(t, `UPDATE ohs_sync.connections SET initial_backfill_at = $2 WHERE user_id = $1`, e.userA, at)

	// A notification for A is accepted and queues nothing for A.
	e.notify(t, "pu-1", at.Unix(), at.Add(time.Hour).Unix())
	assert.Equal(t, 0, e.jobs(t, e.userA, "pending"))

	// A pull queued before the switch went off.
	e.exec(t, `INSERT INTO ohs_sync.pull_jobs (tenant_id, user_id, provider, from_time, to_time)
		VALUES ($1, $2, $3, $4, $5)`, e.tenantA, e.userA, fakeSlug, at.Add(-2*time.Hour), at)
	fetched := e.fake.fetchN.Load()
	logged := e.scalar(t, `SELECT count(*) FROM ohs_sync.sync_log WHERE user_id = $1`, e.userA)
	stored := e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects WHERE user_id = $1`, e.userA)
	e.drain(t)
	assert.Equal(t, fetched+1, e.fake.fetchN.Load(), "only B's pull reached the provider")
	assert.Equal(t, 0, e.jobs(t, e.userA, "pending"), "not retried")
	var outcome, lastErr string
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT outcome, last_error FROM ohs_sync.pull_jobs
		WHERE user_id = $1 AND status = 'parked'`, e.userA).Scan(&outcome, &lastErr))
	assert.Equal(t, queue.OutcomeInactive, outcome)
	assert.Equal(t, "provider turned off for this tenant", lastErr)
	assert.Equal(t, logged, e.scalar(t, `SELECT count(*) FROM ohs_sync.sync_log WHERE user_id = $1`, e.userA))
	assert.Equal(t, stored, e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects WHERE user_id = $1`, e.userA))
	h := e.health(t)
	assert.Equal(t, collect.StatusActive, h.Status)
	assert.True(t, h.Health.Healthy, "not a failure of the connection")
	assert.Equal(t, collect.RunCancelled, e.runs(t, "?limit=1").Data[0].Status)

	// Back on: the next slot is planned again.
	e.turn(t, e.tenantA, true)
	e.setNow(at.Add(2 * time.Hour))
	n, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, 1, e.jobs(t, e.userA, "pending"))
}

// An upload, or a part stored for a route (self reports), to a provider
// turned off for the tenant is refused before anything is stored.
func TestProviderOffRefusesUploads(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	switches := tenantproviders.New(ctx, tenantproviders.NewPG(e.pool))
	deps := collect.UploadDeps{Pool: e.pool, Raw: e.raw, Device: applehealth.New(), Allowed: switches.Allowed}
	e.handler = auth.Middleware(e.store.LookupKey, auth.UserUpload)(collect.NewUploadHandler(deps))

	_, err := switches.Update(ctx, e.tenant, applehealth.Slug, false, "test-key", nil)
	require.NoError(t, err)
	rec := e.post(t, e.token, stdHeaders(), fixtureBody(t))
	assert.Equal(t, collect.ProviderOffDetail, problem(t, rec, http.StatusForbidden))
	_, err = collect.StorePart(ctx, deps, collect.Part{Tenant: e.tenant, User: e.user, Type: "x", Day: time.Now(),
		Batch: "b", Part: 0, Body: fixtureBody(t)})
	assert.ErrorIs(t, err, collect.ErrProviderOff)
	var n int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_sync.raw_objects`).Scan(&n))
	assert.Zero(t, n)
	files, err := e.raw.List(ctx, "raw/")
	require.NoError(t, err)
	assert.Empty(t, files)

	_, err = switches.Update(ctx, e.tenant, applehealth.Slug, true, "test-key", nil)
	require.NoError(t, err)
	rec = e.post(t, e.token, stdHeaders(), fixtureBody(t))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, decode(t, rec).Duplicate)
}

func (e *connEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}
