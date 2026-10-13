// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// syncBuffer is a log sink safe for concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

func testConfig(t *testing.T, url string) config.Config {
	return config.Config{
		DatabaseURL:     url,
		RawStore:        "file://" + t.TempDir(),
		Listen:          freeAddr(t),
		LogLevel:        "info",
		BootstrapTenant: "dev",
		Roles:           map[string]bool{"api": true, "etl": true},
		Metrics:         true,
		EncryptionKey:   make([]byte, 32),
	}
}

// start runs the app until the test ends and waits for /healthz.
func start(t *testing.T, cfg config.Config, logs *syncBuffer) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, logs) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		res, err := http.Get("http://" + cfg.Listen + "/healthz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case err := <-done:
			cancel()
			t.Fatalf("app stopped early: %v\n%s", err, logs)
		default:
		}
		require.True(t, time.Now().Before(deadline), "app never became healthy:\n%s", logs)
		time.Sleep(50 * time.Millisecond)
	}
	var once sync.Once
	var stopErr error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case stopErr = <-done:
			case <-time.After(30 * time.Second):
				stopErr = context.DeadlineExceeded
			}
		})
		return stopErr
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

func bootstrapKeys(t *testing.T, logs string) []string {
	t.Helper()
	var keys []string
	sc := bufio.NewScanner(strings.NewReader(logs))
	for sc.Scan() {
		var line map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &line), "every log line is JSON: %q", sc.Text())
		if k, ok := line["api_key"].(string); ok {
			assert.Equal(t, "WARN", line["level"])
			keys = append(keys, k)
		}
	}
	return keys
}

func TestRunServesAndBootstraps(t *testing.T) {
	url := db.TestDB(t)
	cfg := testConfig(t, url)
	logs := &syncBuffer{}
	stop := start(t, cfg, logs)

	keys := bootstrapKeys(t, logs.String())
	require.Len(t, keys, 1, "the bootstrap key is logged once:\n%s", logs)
	req, _ := http.NewRequest("GET", "http://"+cfg.Listen+"/v1/providers", nil)
	req.Header.Set("Authorization", "Bearer "+keys[0])
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusOK, res.StatusCode)

	get := func(addr, p string) int {
		t.Helper()
		res, err := http.Get("http://" + addr + p)
		require.NoError(t, err)
		res.Body.Close()
		return res.StatusCode
	}
	assert.Equal(t, http.StatusOK, get(cfg.Listen, "/readyz"))
	assert.Equal(t, http.StatusForbidden, get(cfg.Listen, "/metrics"), "no metrics token, no scrape")
	require.NoError(t, stop(), "graceful shutdown")

	// A second start finds the tenant: no new key, nothing logged. It
	// serves /metrics on an address of its own.
	cfg.Listen = freeAddr(t)
	cfg.MetricsListen = freeAddr(t)
	logs2 := &syncBuffer{}
	stop2 := start(t, cfg, logs2)
	assert.Equal(t, http.StatusOK, get(cfg.MetricsListen, "/metrics"))
	assert.Equal(t, http.StatusNotFound, get(cfg.Listen, "/metrics"))
	require.NoError(t, stop2())
	assert.Empty(t, bootstrapKeys(t, logs2.String()))
	assert.NotContains(t, logs2.String(), keys[0])

	pool, err := db.Open(context.Background(), url)
	require.NoError(t, err)
	defer pool.Close()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_core.tenants WHERE name = 'dev'`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestRunETLOnlyHasNoListener(t *testing.T) {
	cfg := testConfig(t, db.TestDB(t))
	cfg.Roles = map[string]bool{"etl": true}
	cfg.BootstrapTenant = ""
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, &syncBuffer{}) }()
	time.Sleep(500 * time.Millisecond)
	_, err := http.Get("http://" + cfg.Listen + "/healthz")
	assert.Error(t, err, "the etl role alone does not listen")
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("etl role did not stop")
	}
}

func TestRunFailsFast(t *testing.T) {
	cfg := testConfig(t, db.TestDB(t))
	cfg.LogLevel = "loud"
	err := run(context.Background(), cfg, &syncBuffer{})
	assert.ErrorContains(t, err, "OHS_LOG_LEVEL")

	cfg = testConfig(t, "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	err = run(context.Background(), cfg, &syncBuffer{})
	assert.Error(t, err)

	// The listen address is taken: Run returns the error instead of hanging.
	l, lerr := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, lerr)
	defer l.Close()
	cfg = testConfig(t, db.TestDB(t))
	cfg.Listen = l.Addr().String()
	errc := make(chan error, 1)
	go func() { errc <- run(context.Background(), cfg, &syncBuffer{}) }()
	select {
	case err := <-errc:
		assert.Error(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("run did not fail on a taken port")
	}
}

// blockingStore hangs every Get: until the read context ends when honour is
// true, forever otherwise (a stuck driver call).
type blockingStore struct {
	rawstore.Store
	honour  bool
	entered chan struct{}
}

func (b blockingStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return nil, b.block(ctx)
}

// DeletePrefix hangs like Get (a stuck deletion job).
func (b blockingStore) DeletePrefix(ctx context.Context, prefix string) error {
	return b.block(ctx)
}

func (b blockingStore) block(ctx context.Context) error {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	if b.honour {
		<-ctx.Done()
		return ctx.Err()
	}
	select {} // never returns
}

// queueStuckJob registers one raw object for a fresh user and queues its job.
func queueStuckJob(t *testing.T, url string) {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	st := tenant.New(pool)
	ten, _, err := st.CreateTenant(ctx, "stuck")
	require.NoError(t, err)
	u, err := st.CreateUser(ctx, ten.ID, "")
	require.NoError(t, err)
	key := rawstore.Key(ten.ID, u.ID, "apple_health", time.Now().UTC(), "b", 0)
	_, err = pool.Exec(ctx, `INSERT INTO ohs_sync.raw_objects (tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, data_type)
		VALUES ($1, $2, 'apple_health', $3, 'h', 1, 'b', 0, 'HKQuantityTypeIdentifierHeartRate')`, ten.ID, u.ID, key)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO ohs_sync.etl_jobs (tenant_id, user_id, provider, key, tier) VALUES ($1, $2, 'apple_health', $3, 'high_freq')`,
		ten.ID, u.ID, key)
	require.NoError(t, err)
}

// queueStuckDeletion queues a deletion job for a fresh user.
func queueStuckDeletion(t *testing.T, url string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	st := tenant.New(pool)
	ten, _, err := st.CreateTenant(ctx, "stuck")
	require.NoError(t, err)
	u, err := st.CreateUser(ctx, ten.ID, "")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, u.ID)
	require.NoError(t, err)
	var id uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO ohs_core.deletion_jobs (tenant_id, user_id) VALUES ($1, $2) RETURNING id`, ten.ID, u.ID).Scan(&id))
	return id
}

func TestRunShutdownBoundedByGrace(t *testing.T) {
	type tc struct {
		job    string // "etl" or "deletion"
		honour bool
	}
	for _, c := range []tc{{"etl", true}, {"etl", false}, {"deletion", true}, {"deletion", false}} {
		honour := c.honour
		name := c.job + " " + map[bool]string{true: "job honours cancel", false: "job ignores cancel"}[honour]
		t.Run(name, func(t *testing.T) {
			url := db.TestDB(t)
			var deletionJob uuid.UUID
			if c.job == "etl" {
				queueStuckJob(t, url)
			} else {
				deletionJob = queueStuckDeletion(t, url)
			}
			cfg := testConfig(t, url)
			cfg.BootstrapTenant = ""
			entered := make(chan struct{}, 1)
			opts := options{
				grace:  500 * time.Millisecond,
				margin: 500 * time.Millisecond,
				wrapRaw: func(s rawstore.Store) rawstore.Store {
					return blockingStore{Store: s, honour: honour, entered: entered}
				},
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- runWith(ctx, cfg, &syncBuffer{}, opts) }()
			select {
			case <-entered:
			case <-time.After(30 * time.Second):
				t.Fatal("the job never started")
			}
			start := time.Now()
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("run did not return within the shutdown grace")
			}
			took := time.Since(start)
			assert.Less(t, took, 2500*time.Millisecond)
			if honour {
				assert.GreaterOrEqual(t, took, opts.grace, "the job in flight got its grace")
			}
			if c.job == "deletion" && honour {
				pool, err := db.Open(context.Background(), url)
				require.NoError(t, err)
				defer pool.Close()
				var status string
				require.NoError(t, pool.QueryRow(context.Background(),
					`SELECT status FROM ohs_core.deletion_jobs WHERE id = $1`, deletionJob).Scan(&status))
				assert.Equal(t, "pending", status, "a deletion cut short stays pending")
			}
		})
	}
}

func TestRunDeletesUserThroughWorker(t *testing.T) {
	url := db.TestDB(t)
	cfg := testConfig(t, url)
	logs := &syncBuffer{}
	start(t, cfg, logs)
	keys := bootstrapKeys(t, logs.String())
	require.Len(t, keys, 1)
	client := &http.Client{Timeout: 10 * time.Second}
	call := func(method, path, body string) (int, []byte) {
		req, err := http.NewRequest(method, "http://"+cfg.Listen+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+keys[0])
		res, err := client.Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	code, b := call("POST", "/v1/users", `{"external_ref":"bye"}`)
	require.Equal(t, http.StatusCreated, code, string(b))
	var u struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(b, &u))

	code, b = call("DELETE", "/v1/users/"+u.ID, "")
	require.Equal(t, http.StatusAccepted, code, string(b))
	var job struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal(b, &job))
	require.NotEmpty(t, job.JobID)
	code, _ = call("GET", "/v1/users/"+u.ID, "")
	assert.Equal(t, http.StatusNotFound, code)

	pool, err := db.Open(context.Background(), url)
	require.NoError(t, err)
	defer pool.Close()
	require.Eventually(t, func() bool {
		var n int
		err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM ohs_core.end_users WHERE id = $1`, u.ID).Scan(&n)
		return err == nil && n == 0
	}, 20*time.Second, 100*time.Millisecond, "the etl role's worker finishes the job:\n%s", logs)
	var status string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM ohs_core.deletion_jobs WHERE id = $1`, job.JobID).Scan(&status))
	assert.Equal(t, "done", status)
}
