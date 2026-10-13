// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
)

func (e *connEnv) runs(t *testing.T, query string) collect.SyncRunList {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/v1/users/"+e.userA.String()+"/sync-runs"+query, e.keyA, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list collect.SyncRunList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	return list
}

func (e *connEnv) syncNow(t *testing.T, body string) (int, collect.SyncRun, string) {
	t.Helper()
	var b []byte
	if body != "" {
		b = []byte(body)
	}
	rec := e.do(t, http.MethodPost, "/v1/users/"+e.userA.String()+"/connections/"+fakeSlug+"/sync", e.keyA, b)
	var run collect.SyncRun
	if rec.Code < 300 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &run))
	}
	return rec.Code, run, rec.Header().Get("Location")
}

// addEndpoint subscribes tenant A to every event type, so the events the
// tests look for are written as deliveries.
func (e *connEnv) addEndpoint(t *testing.T) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), `INSERT INTO ohs_core.webhook_endpoints (tenant_id, url, secret_sealed)
		VALUES ($1, 'https://example.com/hook', '\x00')`, e.tenantA)
	require.NoError(t, err)
}

// events returns the data of tenant A's deliveries of one type, oldest first.
func (e *connEnv) events(t *testing.T, typ string) []map[string]any {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT payload -> 'data' FROM ohs_core.webhook_deliveries
		WHERE tenant_id = $1 AND event_type = $2 ORDER BY id`, e.tenantA, typ)
	require.NoError(t, err)
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var d map[string]any
		require.NoError(t, rows.Scan(&d))
		out = append(out, d)
	}
	require.NoError(t, rows.Err())
	return out
}

func (e *connEnv) health(t *testing.T) collect.ConnectionView {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/v1/users/"+e.userA.String()+"/connections", e.keyA, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var list collect.ConnectionList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Data, 1)
	return list.Data[0]
}

// retryDue makes the pending jobs due now, and ends any provider pause.
func (e *connEnv) retryDue(t *testing.T) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), `UPDATE ohs_sync.pull_jobs SET not_before = now() WHERE status = 'pending'`)
	require.NoError(t, err)
	_, err = e.pool.Exec(context.Background(), `UPDATE ohs_core.provider_limits SET paused_until = now() - interval '1 second'`)
	require.NoError(t, err)
}

func TestSyncRunsListTheHistory(t *testing.T) {
	e := newConnEnv(t)
	e.connect(t, e.keyA, e.userA, "pu-1")

	list := e.runs(t, "")
	require.Len(t, list.Data, 5, "the 30 day backfill in weekly runs")
	for _, r := range list.Data {
		assert.Equal(t, "backfill", r.Trigger)
		assert.Equal(t, collect.RunQueued, r.Status)
		assert.NotNil(t, r.NextAttemptAt)
		assert.Nil(t, r.StartedAt)
	}
	assert.Greater(t, list.Data[0].ID, list.Data[4].ID, "newest first")
	assert.Nil(t, list.NextCursor)

	e.drain(t)
	list = e.runs(t, "?provider="+fakeSlug)
	require.Len(t, list.Data, 5)
	for _, r := range list.Data {
		assert.Equal(t, collect.RunSucceeded, r.Status)
		require.NotNil(t, r.ObjectsStored)
		assert.Positive(t, *r.ObjectsStored)
		assert.NotNil(t, r.StartedAt)
		assert.NotNil(t, r.FinishedAt)
		assert.Nil(t, r.Error)
		assert.Nil(t, r.NextAttemptAt)
	}
	assert.Empty(t, e.runs(t, "?provider=other").Data)

	// Pages.
	page := e.runs(t, "?limit=2")
	require.Len(t, page.Data, 2)
	require.NotNil(t, page.NextCursor)
	next := e.runs(t, "?limit=2&cursor="+*page.NextCursor)
	require.Len(t, next.Data, 2)
	assert.Less(t, next.Data[0].ID, page.Data[1].ID)
	last := e.runs(t, "?limit=2&cursor="+*next.NextCursor)
	require.Len(t, last.Data, 1)
	assert.Nil(t, last.NextCursor)

	for _, q := range []string{"?limit=0", "?limit=101", "?cursor=abc"} {
		rec := e.do(t, http.MethodGet, "/v1/users/"+e.userA.String()+"/sync-runs"+q, e.keyA, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, q)
	}
	// Another tenant sees nothing.
	rec := e.do(t, http.MethodGet, "/v1/users/"+e.userA.String()+"/sync-runs", e.keyB, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = e.do(t, http.MethodGet, fmt.Sprintf("/v1/users/%s/sync-runs/%d", e.userA, list.Data[0].ID), e.keyA, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = e.do(t, http.MethodGet, fmt.Sprintf("/v1/users/%s/sync-runs/%d", e.userA, list.Data[0].ID+1000), e.keyA, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSyncNowQueuesAPriorityRun(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 10, 13, 5, 0, 0, time.UTC)
	e.setNow(at)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)

	// Routine work waiting: the manual run is claimed first.
	e.setNow(at.Add(3 * time.Hour))
	n, err := e.sched.Schedule(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	code, run, loc := e.syncNow(t, "")
	require.Equal(t, http.StatusAccepted, code)
	assert.Equal(t, "manual", run.Trigger)
	assert.Equal(t, collect.RunQueued, run.Status)
	assert.Equal(t, at.Add(-time.Hour), run.WindowStart, "from where the data ends, with the overlap")
	assert.Equal(t, at.Add(3*time.Hour), run.WindowEnd)
	assert.Equal(t, fmt.Sprintf("/v1/users/%s/sync-runs/%d", e.userA, run.ID), loc)

	// Asked again while it waits: the same run.
	code, again, _ := e.syncNow(t, "")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, run.ID, again.ID)

	n, err = e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	rec := e.do(t, http.MethodGet, loc, e.keyA, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &run))
	assert.Equal(t, collect.RunSucceeded, run.Status)
	assert.Equal(t, 1, e.jobs(t, e.userA, "pending"), "the scheduled run still waits")

	// Done: a new request queues a new run, here with its own start.
	code, next, _ := e.syncNow(t, `{"from":"2026-10-01T00:00:00Z"}`)
	require.Equal(t, http.StatusAccepted, code)
	assert.NotEqual(t, run.ID, next.ID)
	assert.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), next.WindowStart)

	for _, body := range []string{`{"from":"2026-08-01T00:00:00Z"}`, `{"from":"2027-01-01T00:00:00Z"}`, `{"to":"x"}`, `[]`} {
		code, _, _ := e.syncNow(t, body)
		assert.Equal(t, http.StatusBadRequest, code, body)
	}
	rec = e.do(t, http.MethodPost, "/v1/users/"+e.userA.String()+"/connections/nope/sync", e.keyA, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = e.do(t, http.MethodPost, "/v1/users/"+e.userA.String()+"/connections/"+fakeSlug+"/sync", e.keyB, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSyncNowRefusedWhenTheConnectionCannotSync(t *testing.T) {
	e := newConnEnv(t)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.fake.fetchMode.Store("revoked")
	e.drain(t)
	require.Equal(t, collect.StatusNeedsReauth, e.connStatus(t, e.userA))
	code, _, _ := e.syncNow(t, "")
	assert.Equal(t, http.StatusConflict, code)

	rec := e.do(t, http.MethodDelete, "/v1/users/"+e.userA.String()+"/connections/"+fakeSlug, e.keyA, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	code, _, _ = e.syncNow(t, "")
	assert.Equal(t, http.StatusConflict, code)
}

func TestFailingConnectionBreaksAndRestores(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.addEndpoint(t)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	assert.True(t, e.health(t).Health.Healthy)

	e.fake.fetchMode.Store("500")
	code, run, _ := e.syncNow(t, "")
	require.Equal(t, http.StatusAccepted, code)
	for i := range collect.FailingAfter {
		e.retryDue(t)
		n, err := e.sched.PullOnce(ctx, 1)
		require.NoError(t, err)
		require.Equal(t, 1, n)
		if i < collect.FailingAfter-1 {
			assert.Empty(t, e.events(t, "connection.broken"), "not broken before %d failures", collect.FailingAfter)
		}
	}
	got := e.runs(t, "").Data[0]
	assert.Equal(t, run.ID, got.ID)
	assert.Equal(t, collect.RunRetrying, got.Status)
	assert.Equal(t, collect.FailingAfter, got.Attempts)
	require.NotNil(t, got.Error)
	assert.Contains(t, *got.Error, "500")
	require.NotNil(t, got.NextAttemptAt)

	broken := e.events(t, "connection.broken")
	require.Len(t, broken, 1)
	assert.Equal(t, "failing", broken[0]["reason"])
	assert.Equal(t, "wait", broken[0]["action"])
	assert.Equal(t, fakeSlug, broken[0]["provider"])
	assert.Equal(t, e.userA.String(), broken[0]["user_id"])
	h := e.health(t).Health
	assert.False(t, h.Healthy)
	require.NotNil(t, h.Reason)
	assert.Equal(t, "failing", *h.Reason)
	assert.NotNil(t, h.Since)
	list := e.freshness(t)
	assert.False(t, list.Data[0].Health.Healthy)

	// One more failure announces nothing new.
	e.retryDue(t)
	_, err := e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	assert.Len(t, e.events(t, "connection.broken"), 1)

	e.fake.fetchMode.Store("ok")
	e.retryDue(t)
	_, err = e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	restored := e.events(t, "connection.restored")
	require.Len(t, restored, 1)
	assert.Equal(t, "failing", restored[0]["previous_reason"])
	assert.True(t, e.health(t).Health.Healthy)
	assert.Equal(t, collect.RunSucceeded, e.runs(t, "").Data[0].Status)
}

func TestLongRateLimitBreaksUntilTheNextPull(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.addEndpoint(t)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)

	// A short limit is routine.
	e.fake.fetchMode.Store("429")
	_, _, _ = e.syncNow(t, "")
	_, err := e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, collect.RunRateLimited, e.runs(t, "").Data[0].Status)
	assert.Empty(t, e.events(t, "connection.broken"))
	assert.True(t, e.health(t).Health.Healthy)

	// A spent daily quota breaks the connection.
	e.fake.fetchMode.Store("429long")
	e.retryDue(t)
	_, err = e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	broken := e.events(t, "connection.broken")
	require.Len(t, broken, 1)
	assert.Equal(t, "rate_limited", broken[0]["reason"])
	assert.Equal(t, "wait", broken[0]["action"])
	assert.NotEmpty(t, broken[0]["retry_at"])
	h := e.health(t).Health
	assert.False(t, h.Healthy)
	require.NotNil(t, h.RetryAt)
	assert.WithinDuration(t, time.Now().Add(2*time.Hour), *h.RetryAt, time.Minute)
	run := e.runs(t, "").Data[0]
	assert.Equal(t, collect.RunRateLimited, run.Status)
	assert.Equal(t, 0, run.Attempts, "a rate limit burns no attempt")
	require.NotNil(t, run.Error)

	e.fake.fetchMode.Store("ok")
	e.retryDue(t)
	_, err = e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	require.Len(t, e.events(t, "connection.restored"), 1)
	h = e.health(t).Health
	assert.True(t, h.Healthy)
	assert.Nil(t, h.RetryAt)
}

func TestRefusedGrantBreaksUntilTheUserConnectsAgain(t *testing.T) {
	for _, tc := range []struct{ mode, reason string }{{"revoked", "needs_reauth"}, {"noscope", "missing_scope"}} {
		t.Run(tc.mode, func(t *testing.T) {
			e := newConnEnv(t)
			e.addEndpoint(t)
			e.connect(t, e.keyA, e.userA, "pu-1")
			e.fake.fetchMode.Store(tc.mode)
			e.drain(t)
			assert.Equal(t, collect.StatusNeedsReauth, e.connStatus(t, e.userA))
			broken := e.events(t, "connection.broken")
			require.Len(t, broken, 1, "once, though five runs met the refusal")
			assert.Equal(t, tc.reason, broken[0]["reason"])
			assert.Equal(t, "reconnect", broken[0]["action"])
			assert.Len(t, e.events(t, "connection.needs_reauth"), 1, "the older event is still sent")
			h := e.health(t).Health
			require.NotNil(t, h.Reason)
			assert.Equal(t, tc.reason, *h.Reason)
			require.NotNil(t, h.Action)
			assert.Equal(t, "reconnect", *h.Action)

			statuses := map[string]int{}
			for _, r := range e.runs(t, "").Data {
				statuses[r.Status]++
			}
			assert.Equal(t, 5, statuses[collect.RunFailed]+statuses[collect.RunCancelled])
			assert.Positive(t, statuses[collect.RunFailed])

			e.fake.fetchMode.Store("ok")
			e.connect(t, e.keyA, e.userA, "pu-1")
			restored := e.events(t, "connection.restored")
			require.Len(t, restored, 1)
			assert.Equal(t, tc.reason, restored[0]["previous_reason"])
			assert.True(t, e.health(t).Health.Healthy)
		})
	}
}

func TestFinishedRunsArePruned(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	_, err := e.pool.Exec(ctx, `UPDATE ohs_sync.pull_jobs SET created_at = now() - interval '31 days'
		WHERE id = (SELECT min(id) FROM ohs_sync.pull_jobs)`)
	require.NoError(t, err)
	_, err = e.sched.Schedule(ctx)
	require.NoError(t, err)
	assert.Len(t, e.runs(t, "?provider="+fakeSlug).Data, 4)
}

func TestRateLimitPausesTheWholeProvider(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	userB, err := e.store.CreateUser(ctx, e.tenantA, "a-2")
	require.NoError(t, err)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.connect(t, e.keyA, userB.ID, "pu-2")
	e.drain(t)

	e.fake.fetchMode.Store("429")
	_, _, _ = e.syncNow(t, "")
	n, err := e.sched.PullOnce(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	before := e.fake.fetchN.Load()

	// The other user's pull waits for the pause instead of drawing its own 429.
	rec := e.do(t, http.MethodPost, "/v1/users/"+userB.ID.String()+"/connections/"+fakeSlug+"/sync", e.keyA, nil)
	require.Equal(t, http.StatusAccepted, rec.Code)
	n, err = e.sched.PullOnce(ctx, 4)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, before, e.fake.fetchN.Load(), "no request while the provider is paused")
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_core.provider_limits WHERE provider = $1 AND paused_until > now()`, fakeSlug))

	e.fake.fetchMode.Store("ok")
	e.retryDue(t)
	e.drain(t)
	assert.Equal(t, 0, e.jobs(t, userB.ID, "pending"))
	assert.Equal(t, 0, e.scalar(t, `SELECT consecutive FROM ohs_core.provider_limits WHERE provider = $1`, fakeSlug),
		"a success resets the backoff")
}
