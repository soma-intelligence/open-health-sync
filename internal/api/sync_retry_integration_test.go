// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
)

// parkedRun inserts a sync run of the user that gave up (or, with outcome
// inactive, was cancelled because the connection stopped) and returns its id.
func (e *env) parkedRun(tenantID, userID uuid.UUID, provider, from, to, outcome string) int64 {
	e.t.Helper()
	var id int64
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`INSERT INTO ohs_sync.pull_jobs (tenant_id, user_id, provider, from_time, to_time, status, outcome, attempts, last_error)
		 VALUES ($1, $2, $3, $4, $5, 'parked', $6, 5, 'HTTP 500') RETURNING id`,
		tenantID, userID, provider, from, to, outcome).Scan(&id))
	return id
}

type syncRunJSON struct {
	ID       int64   `json:"id"`
	Provider string  `json:"provider"`
	Status   string  `json:"status"`
	Attempts int     `json:"attempts"`
	Error    *string `json:"error"`
}

// A failed sync run is queued again by id, and every failed run of a
// connection at once, with a tenant key.
func TestRetrySyncRunRoutes(t *testing.T) {
	e := newReadsEnv(t)
	u := e.user(e.tenantA, "retry")
	other := e.user(e.tenantB, "other")
	users := "/v1/users/" + u.String()
	failed := e.parkedRun(e.tenantA, u, "withings", "2026-02-01", "2026-02-02", "failed")
	retry := users + "/sync-runs/" + strconv.FormatInt(failed, 10) + "/retry"

	// Only the tenant's key, for its own user and run.
	assert.Equal(t, http.StatusUnauthorized, e.do("POST", retry, "", nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("POST", retry, e.keyB, nil).code, "another tenant's user")
	assert.Equal(t, http.StatusNotFound,
		e.do("POST", "/v1/users/"+other.String()+"/sync-runs/"+strconv.FormatInt(failed, 10)+"/retry", e.keyB, nil).code,
		"another tenant's run")
	for _, id := range []string{"999999", "0", "abc"} {
		r := e.do("POST", users+"/sync-runs/"+id+"/retry", e.keyA, nil)
		assert.Equal(t, http.StatusNotFound, r.code, id)
		assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"), id)
	}
	assert.Equal(t, http.StatusMethodNotAllowed, e.do("GET", retry, e.keyA, nil).code)

	r := e.do("POST", retry, e.keyA, nil, "X-Request-Id", "req-retry-1")
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	assert.Equal(t, users+"/sync-runs/"+strconv.FormatInt(failed, 10), r.header.Get("Location"))
	var run syncRunJSON
	r.json(t, &run)
	assert.Equal(t, failed, run.ID)
	assert.Equal(t, "queued", run.Status)
	assert.Zero(t, run.Attempts, "fresh attempts")

	// Queued, it cannot be retried again.
	r = e.do("POST", retry, e.keyA, nil)
	assert.Equal(t, http.StatusConflict, r.code, string(r.body))
	assert.Contains(t, string(r.body), "only a failed or cancelled run can be retried")

	// A failed run whose window another run already covers.
	twin := e.parkedRun(e.tenantA, u, "withings", "2026-02-01", "2026-02-02", "failed")
	r = e.do("POST", users+"/sync-runs/"+strconv.FormatInt(twin, 10)+"/retry", e.keyA, nil)
	assert.Equal(t, http.StatusConflict, r.code, string(r.body))
	assert.Contains(t, string(r.body), "another run already covers this window")

	// Every failed run of one connection: not the cancelled one, not
	// another provider's, not the one whose window is queued.
	e.parkedRun(e.tenantA, u, "withings", "2026-02-02", "2026-02-03", "failed")
	e.parkedRun(e.tenantA, u, "withings", "2026-02-03", "2026-02-04", "failed")
	e.parkedRun(e.tenantA, u, "withings", "2026-02-04", "2026-02-05", "inactive")
	oura := e.parkedRun(e.tenantA, u, "oura", "2026-02-01", "2026-02-02", "failed")
	all := users + "/connections/withings/sync-runs/retry"
	assert.Equal(t, http.StatusUnauthorized, e.do("POST", all, "", nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("POST", all, e.keyB, nil).code)
	r = e.do("POST", all, e.keyA, nil, "X-Request-Id", "req-retry-2")
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	assert.JSONEq(t, `{"retried":2}`, string(r.body))
	r = e.do("POST", all, e.keyA, nil)
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	assert.JSONEq(t, `{"retried":0}`, string(r.body), "nothing left to retry")
	var status string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT status FROM ohs_sync.pull_jobs WHERE id = $1`, oura).Scan(&status))
	assert.Equal(t, "parked", status, "another connection's run is left alone")

	// Both are recorded as a sync started by the caller, not as a read or a
	// new connection.
	var log auditPage
	e.do("GET", "/v1/audit?limit=100", e.keyA, nil).json(t, &log)
	byRequest := map[string]audit.Entry{}
	for _, ev := range log.Data {
		byRequest[ev.RequestID] = ev
	}
	assert.Equal(t, audit.ConnectionSync, byRequest["req-retry-1"].Action)
	assert.Equal(t, "POST /v1/users/{id}/sync-runs/{run}/retry", byRequest["req-retry-1"].Route)
	assert.Equal(t, audit.ConnectionSync, byRequest["req-retry-2"].Action)
	assert.Equal(t, "POST /v1/users/{id}/connections/{provider}/sync-runs/retry", byRequest["req-retry-2"].Route)
}

// An instance admin retries a tenant's failed runs through the same
// handlers.
func TestAdminRetriesSyncRunsOfATenant(t *testing.T) {
	e := newReadsEnv(t)
	blue := e.newTenant("Team Blue")
	u := e.user(blue.ID, "retry")
	base := "/v1/admin/tenants/" + blue.ID.String() + "/users/" + u.String()
	failed := e.parkedRun(blue.ID, u, "withings", "2026-02-01", "2026-02-02", "failed")
	e.parkedRun(blue.ID, u, "withings", "2026-02-02", "2026-02-03", "failed")
	retry := base + "/sync-runs/" + strconv.FormatInt(failed, 10) + "/retry"

	assert.Equal(t, http.StatusForbidden, e.do("POST", retry, blue.Key.Key, nil).code, "not an admin key")
	assert.Equal(t, http.StatusForbidden, e.do("POST", base+"/connections/withings/sync-runs/retry", e.keyB, nil).code)

	r := e.do("POST", retry, e.keyA, nil)
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	assert.Equal(t, base+"/sync-runs/"+strconv.FormatInt(failed, 10), r.header.Get("Location"),
		"the admin route of the run")
	var run syncRunJSON
	r.json(t, &run)
	assert.Equal(t, "queued", run.Status)
	assert.Equal(t, http.StatusConflict, e.do("POST", retry, e.keyA, nil).code)

	r = e.do("POST", base+"/connections/withings/sync-runs/retry", e.keyA, nil)
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	assert.JSONEq(t, `{"retried":1}`, string(r.body))

	// The same answers as the tenant's own key gets.
	mine := e.do("POST", "/v1/users/"+u.String()+"/connections/withings/sync-runs/retry", blue.Key.Key, nil)
	admin := e.do("POST", base+"/connections/withings/sync-runs/retry", e.keyA, nil)
	assert.Equal(t, mine.code, admin.code)
	assert.JSONEq(t, string(mine.body), string(admin.body))
}
