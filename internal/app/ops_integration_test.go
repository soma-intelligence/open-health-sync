// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// opsGet fetches one route of the ops listener. An unreachable listener is
// status 0.
func opsGet(addr, path string) (status int, contentType, body string) {
	res, err := http.Get("http://" + addr + path)
	if err != nil {
		return 0, "", err.Error()
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Content-Type"), string(b)
}

// startWorker runs a process without the api role until the test ends and
// waits for its ops listener.
func startWorker(t *testing.T, roles string, url string, opts options) (addr string, logs *syncBuffer) {
	t.Helper()
	cfg := testConfig(t, url)
	cfg.Roles = map[string]bool{}
	for _, r := range strings.Split(roles, ",") {
		cfg.Roles[r] = true
	}
	cfg.BootstrapTenant = ""
	cfg.OpsListen = freeAddr(t)
	// One slot per tier: one long job takes the whole tier.
	cfg.ETLConcurrency = 1
	logs = &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWith(ctx, cfg, logs, opts) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Error("the worker did not stop")
		}
	})
	require.Eventually(t, func() bool {
		status, _, _ := opsGet(cfg.OpsListen, "/healthz")
		return status == http.StatusOK
	}, 20*time.Second, 50*time.Millisecond, "the ops listener never answered:\n%s", logs)
	_, err := http.Get("http://" + cfg.Listen + "/healthz")
	require.Error(t, err, "a process without the api role does not serve the API")
	return cfg.OpsListen, logs
}

// A process with worker roles only serves /healthz, /readyz and /metrics on
// OHS_OPS_LISTEN, in the API's shapes, and nothing else.
func TestRunWorkerOnlyServesOps(t *testing.T) {
	url := db.TestDB(t)
	// A job whose raw object is not in the store: it ends, one way or the
	// other, and its duration is observed.
	queueStuckJob(t, url)
	addr, logs := startWorker(t, "collect,etl", url, options{grace: time.Second, margin: time.Second})

	status, ctype, body := opsGet(addr, "/healthz")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "application/json", ctype)
	assert.JSONEq(t, `{"status":"ok"}`, body)

	status, ctype, body = opsGet(addr, "/readyz")
	assert.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "application/json", ctype)
	assert.JSONEq(t, `{"status":"ready"}`, body)

	var metrics string
	require.Eventually(t, func() bool {
		status, ctype, metrics = opsGet(addr, "/metrics")
		return status == http.StatusOK &&
			strings.Contains(metrics, `ohs_job_duration_seconds_count{queue="etl",kind="high_freq",outcome="`) &&
			strings.Contains(metrics, `ohs_queue_depth{queue="pull"}`)
	}, 20*time.Second, 100*time.Millisecond, "the metrics never held a job and the pull backlog:\n%s", logs)
	assert.Equal(t, "text/plain; version=0.0.4; charset=utf-8", ctype)
	for _, loop := range []string{"etl_standard", "etl_high_freq", "webhooks", "deletion", "consent_purge",
		"pull_scheduler", "pull_subscriber", "pull_workers"} {
		assert.Contains(t, metrics, `ohs_loop_last_tick_unix_seconds{loop="`+loop+`"} `)
		assert.Contains(t, metrics, `ohs_loop_stalled{loop="`+loop+`"} 0`)
	}
	for _, queue := range []string{"etl_standard", "etl_high_freq", "webhook", "pull"} {
		// The first sample may have caught the high_freq job still queued.
		assert.Contains(t, metrics, `ohs_queue_depth{queue="`+queue+`"} `)
		assert.Contains(t, metrics, `ohs_queue_oldest_job_age_seconds{queue="`+queue+`"} `)
	}
	assert.Contains(t, metrics, `ohs_queue_depth{queue="pull"} 0`)
	assert.Contains(t, metrics, `ohs_queue_oldest_job_age_seconds{queue="webhook"} 0`)
	// What the API serves for the etl role is served here too.
	assert.Contains(t, metrics, "# TYPE ohs_etl_parked_jobs gauge\n")
	assert.Contains(t, metrics, "ohs_etl_run_failures_total ")
	assert.NotContains(t, metrics, "ohs_http_requests_total", "no API, no API metrics")

	// Nothing else is served, and no route asks for a credential.
	for _, path := range []string{"/", "/v1/users", "/openapi.yaml", "/console"} {
		status, _, _ = opsGet(addr, path)
		assert.Equal(t, http.StatusNotFound, status, path)
	}
	res, err := http.Post("http://"+addr+"/healthz", "application/json", nil)
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, res.StatusCode)
}

// One ETL job that hangs on its only slot blocks its tier's loop. While the
// job is inside the time it was given the worker is busy, not hung. Past
// it, /healthz answers 503 and names the loop, and only that one.
func TestRunBlockedLoopFailsHealthz(t *testing.T) {
	url := db.TestDB(t)
	queueStuckJob(t, url)
	entered := make(chan struct{}, 1)
	jobLimit := 9 * time.Second
	addr, logs := startWorker(t, "etl", url, options{
		grace: 500 * time.Millisecond, margin: 500 * time.Millisecond,
		wrapRaw: func(s rawstore.Store) rawstore.Store {
			return blockingStore{Store: s, honour: true, entered: entered}
		},
		// The tier's loop polls every 2 s: stalled after 6 s of silence,
		// were it not for the job, which is given 9 s.
		noMinStall: true, jobLimit: jobLimit,
	})
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatalf("the job never started:\n%s", logs)
	}
	blockedAt := time.Now()

	var (
		status      int
		ctype, body string
	)
	require.Eventually(t, func() bool {
		status, ctype, body = opsGet(addr, "/healthz")
		if status == http.StatusOK {
			assert.JSONEq(t, `{"status":"ok"}`, body)
		}
		return status == http.StatusServiceUnavailable
	}, 30*time.Second, 100*time.Millisecond, "the blocked loop never failed /healthz:\n%s", logs)
	assert.GreaterOrEqual(t, time.Since(blockedAt), jobLimit-time.Second,
		"a worker busy on one long job is not hung while the job is inside its limit")

	assert.Equal(t, "application/problem+json", ctype)
	var p struct {
		Status int    `json:"status"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &p), body)
	assert.Equal(t, http.StatusServiceUnavailable, p.Status)
	assert.True(t, strings.HasPrefix(p.Detail, "background loops stalled: etl_high_freq (a job ran past its limit"), p.Detail)
	assert.NotContains(t, p.Detail, ";", "the loops that still come round are not named")

	// The other loops still come round and the process is still ready:
	// liveness and readiness are apart.
	status, _, body = opsGet(addr, "/readyz")
	assert.Equal(t, http.StatusOK, status, body)
	_, _, metrics := opsGet(addr, "/metrics")
	assert.Contains(t, metrics, `ohs_loop_stalled{loop="etl_high_freq"} 1`)
	assert.Contains(t, metrics, `ohs_loop_stalled{loop="etl_standard"} 0`)
	assert.Contains(t, metrics, `ohs_loop_stalled{loop="webhooks"} 0`)
	assert.Contains(t, logs.String(), `"message":"healthz_loops_stalled"`)
	assert.Contains(t, logs.String(), "background loops stalled: etl_high_freq")
}
