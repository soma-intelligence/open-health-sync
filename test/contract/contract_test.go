// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package contract

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/app"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/shl"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

const fixtureDir = "../../connectors/applehealth/testdata/e2e"

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

type reply struct {
	status int
	body   []byte
}

func (r reply) json(t *testing.T, v any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(r.body, v), "%s", r.body)
}

func (c *client) do(method, path, secret string, body io.Reader, header map[string]string) reply {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, body)
	require.NoError(c.t, err)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	require.NoError(c.t, err)
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	require.NoError(c.t, err)
	return reply{status: res.StatusCode, body: b}
}

func (c *client) json(method, path, secret, body string) reply {
	c.t.Helper()
	var r io.Reader
	h := map[string]string{}
	if body != "" {
		r = strings.NewReader(body)
		h["Content-Type"] = "application/json"
	}
	return c.do(method, path, secret, r, h)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// startServer runs app.Run with every role on a fresh database and returns
// its base URL and a pool on the same database.
// contractMetricsToken is the OHS_METRICS_TOKEN of the contract server.
const contractMetricsToken = "contract-metrics-token"

// contractSetupToken is the OHS_SETUP_TOKEN of the contract server.
const contractSetupToken = "contract-setup-token"

func startServer(t *testing.T) (string, string) {
	t.Helper()
	url := db.TestDB(t)
	addr := freeAddr(t)
	cfg := config.Config{
		DatabaseURL:   url,
		RawStore:      "file://" + t.TempDir(),
		Listen:        addr,
		LogLevel:      "warn",
		PublicURL:     "http://" + addr,
		Roles:         map[string]bool{"api": true, "collect": true, "etl": true},
		Metrics:       true,
		MetricsToken:  contractMetricsToken,
		SetupToken:    contractSetupToken,
		EncryptionKey: make([]byte, 32),
		// Four OAuth callbacks and four setup attempts per address, so the
		// run sees a 429 on each route.
		RateLimitOAuth: config.RateLimit{Rate: 0.001, Burst: 4},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(40 * time.Second):
			t.Error("server did not stop")
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		res, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("server stopped: %v", err)
		default:
		}
		require.True(t, time.Now().Before(deadline), "server never became healthy")
		time.Sleep(50 * time.Millisecond)
	}
	return "http://" + addr, url
}

// TestQuickstartMatchesOpenAPI replays the README quickstart (plus the reads
// and a few refusals) through the recording client. Every request and
// response must match api/openapi.yaml: documented route, parameters, status
// and body, and no response member the document does not name.
func TestQuickstartMatchesOpenAPI(t *testing.T) {
	base, url := startServer(t)
	rec, err := NewRecorder(context.Background(), "../../api/openapi.yaml", base)
	require.NoError(t, err)
	c := &client{t: t, base: base, http: &http.Client{Transport: rec, Timeout: 30 * time.Second}}

	// The browser first run (/console/setup), while no tenant exists.
	assert.Equal(t, 200, c.json("GET", "/v1/setup", "", "").status)
	assert.Equal(t, 401, c.json("POST", "/v1/setup", "", `{"setup_token":"not-the-setup-token"}`).status)
	assert.Equal(t, 400, c.json("POST", "/v1/setup", "", `{"setup_token":"`+contractSetupToken+`","tenant_name":"a\u0000"}`).status)
	r := c.json("POST", "/v1/setup", "", `{"setup_token":"`+contractSetupToken+`","tenant_name":"first run"}`)
	require.Equal(t, 201, r.status, "%s", r.body)
	var first struct {
		APIKey string `json:"api_key"`
	}
	r.json(t, &first)
	assert.Equal(t, 200, c.json("GET", "/v1/providers", first.APIKey, "").status)
	assert.Equal(t, 409, c.json("POST", "/v1/setup", "", `{"setup_token":"`+contractSetupToken+`"}`).status)
	assert.Equal(t, 429, c.json("POST", "/v1/setup", "", `{"setup_token":"`+contractSetupToken+`"}`).status, "over the setup rate limit")
	assert.Equal(t, 200, c.json("GET", "/v1/setup", "", "").status)

	// ohs admin tenant create --name contract
	pool, err := db.Open(context.Background(), url)
	require.NoError(t, err)
	_, key, err := tenant.New(pool).CreateTenant(context.Background(), "contract")
	pool.Close()
	require.NoError(t, err)

	// Ops.
	assert.Equal(t, 200, c.json("GET", "/healthz", "", "").status)
	assert.Equal(t, 200, c.json("GET", "/readyz", "", "").status)
	assert.Equal(t, 401, c.json("GET", "/metrics", "", "").status)
	assert.Equal(t, 200, c.json("GET", "/metrics", contractMetricsToken, "").status)
	assert.Equal(t, 200, c.json("GET", "/v1/providers", key, "").status)
	assert.Equal(t, 200, c.json("GET", "/v1/audit?limit=5", key, "").status)
	assert.Equal(t, 400, c.json("GET", "/v1/audit?cursor=abc", key, "").status)

	// The tenant, its keys and the pipeline, as the console reads them.
	assert.Equal(t, 200, c.json("GET", "/v1/tenant", key, "").status)
	r = c.json("POST", "/v1/keys", key, "")
	require.Equal(t, 201, r.status, "%s", r.body)
	var made struct{ ID string }
	r.json(t, &made)
	assert.Equal(t, 400, c.json("POST", "/v1/keys", key, `{"name":"x"}`).status)
	assert.Equal(t, 200, c.json("GET", "/v1/keys", key, "").status)
	assert.Equal(t, 204, c.json("DELETE", "/v1/keys/"+made.ID, key, "").status)
	assert.Equal(t, 404, c.json("DELETE", "/v1/keys/not-a-key", key, "").status)
	assert.Equal(t, 200, c.json("GET", "/v1/pipeline", key, "").status)
	assert.Equal(t, 200, c.json("GET", "/v1/pipeline/sync-runs?status=failed&limit=5", key, "").status)
	assert.Equal(t, 400, c.json("GET", "/v1/pipeline/sync-runs?status=lost", key, "").status)
	assert.Equal(t, 200, c.json("GET", "/v1/pipeline/etl-jobs?status=parked", key, "").status)
	assert.Equal(t, 200, c.json("POST", "/v1/pipeline/etl-jobs/retry", key, `{}`).status)
	assert.Equal(t, 404, c.json("POST", "/v1/pipeline/etl-jobs/999999/retry", key, "").status)

	// Provider settings: only the instance admin, the first tenant, may.
	admin := first.APIKey
	assert.Equal(t, 403, c.json("GET", "/v1/admin/providers", key, "").status)
	assert.Equal(t, 200, c.json("GET", "/v1/admin/providers", admin, "").status)
	assert.Equal(t, 200, c.json("GET", "/v1/admin/providers/apple_health", admin, "").status)
	assert.Equal(t, 404, c.json("GET", "/v1/admin/providers/nope", admin, "").status)
	assert.Equal(t, 409, c.json("PATCH", "/v1/admin/providers/apple_health", admin, `{"enabled":false}`).status)
	assert.Equal(t, 400, c.json("PATCH", "/v1/admin/providers/dexcom", admin, `{}`).status)
	assert.Equal(t, 404, c.json("DELETE", "/v1/admin/providers/dexcom", admin, "").status)
	r = c.json("PATCH", "/v1/admin/providers/dexcom", admin, `{"keys":{"CLIENT_ID":"contract-id","CLIENT_SECRET":"contract-secret"}}`)
	assert.Equal(t, 200, r.status, "%s", r.body)
	assert.Equal(t, 200, c.json("DELETE", "/v1/admin/providers/dexcom", admin, "").status)

	// Tenants and their keys, for the instance admin's own tooling.
	assert.Equal(t, 403, c.json("GET", "/v1/admin/tenants", key, "").status)
	assert.Equal(t, 400, c.json("POST", "/v1/admin/tenants", admin, `{"name":" "}`).status)
	r = c.json("POST", "/v1/admin/tenants", admin, `{"name":"contract team"}`)
	require.Equal(t, 201, r.status, "%s", r.body)
	var team struct {
		ID  string `json:"id"`
		Key struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"key"`
	}
	r.json(t, &team)
	teamPath := "/v1/admin/tenants/" + team.ID
	assert.Equal(t, 200, c.json("GET", "/v1/admin/tenants?limit=2", admin, "").status)
	assert.Equal(t, 400, c.json("GET", "/v1/admin/tenants?cursor=%25%25", admin, "").status)
	assert.Equal(t, 200, c.json("GET", teamPath, admin, "").status)
	assert.Equal(t, 404, c.json("GET", "/v1/admin/tenants/00000000-0000-4000-8000-000000000000", admin, "").status)
	assert.Equal(t, 200, c.json("GET", teamPath+"/pipeline", admin, "").status)
	assert.Equal(t, 200, c.json("GET", teamPath+"/keys", admin, "").status)
	r = c.json("POST", teamPath+"/keys", admin, "")
	require.Equal(t, 201, r.status, "%s", r.body)
	var teamKey struct{ ID string }
	r.json(t, &teamKey)
	assert.Equal(t, 204, c.json("DELETE", teamPath+"/keys/"+teamKey.ID, admin, "").status)
	assert.Equal(t, 404, c.json("DELETE", teamPath+"/keys/not-a-key", admin, "").status)
	var adminTenant struct {
		ID  string `json:"id"`
		Key struct{ ID string }
	}
	c.json("GET", "/v1/tenant", admin, "").json(t, &adminTenant)
	assert.Equal(t, 409, c.json("DELETE", "/v1/admin/tenants/"+adminTenant.ID+"/keys/"+adminTenant.Key.ID, admin, "").status)
	r = c.json("POST", teamPath+"/demo-user", admin, `{"days":2}`)
	require.Equal(t, 201, r.status, "%s", r.body)
	assert.Equal(t, 200, c.json("POST", teamPath+"/demo-user", admin, `{"days":2,"external_ref":"demo-user"}`).status)
	assert.Equal(t, 400, c.json("POST", teamPath+"/demo-user", admin, `{"days":91}`).status)
	assert.Equal(t, 200, c.json("GET", "/v1/tenant", team.Key.Key, "").status)

	// User and token.
	r = c.json("POST", "/v1/users", key, `{"external_ref":"contract-user"}`)
	require.Equal(t, 201, r.status, "%s", r.body)
	var user struct{ ID string }
	r.json(t, &user)
	assert.Equal(t, 201, c.do("POST", "/v1/users", key, nil, map[string]string{"Idempotency-Key": "k1"}).status, "empty body")
	assert.Equal(t, 200, c.json("GET", "/v1/users?limit=10", key, "").status)
	assert.Equal(t, 200, c.json("GET", "/v1/users/"+user.ID, key, "").status)
	// Consent, on a source the fixture does not use.
	consents := "/v1/users/" + user.ID + "/consents"
	assert.Equal(t, 200, c.json("PUT", consents+"/oura", key, `{"granted":["heart","sleep"],"evidence":"form v1"}`).status)
	assert.Equal(t, 400, c.json("PUT", consents+"/oura", key, `{"granted":["genome"]}`).status)
	assert.Equal(t, 200, c.json("DELETE", consents+"/oura?evidence=withdrawn", key, "").status)
	assert.Equal(t, 200, c.json("GET", consents, key, "").status)
	assert.Equal(t, 200, c.json("GET", consents+"/history?limit=2", key, "").status)
	r = c.json("POST", "/v1/users/"+user.ID+"/tokens", key, `{"scopes":["upload"]}`)
	require.Equal(t, 201, r.status, "%s", r.body)
	var tok struct{ Token string }
	r.json(t, &tok)

	// Every part of the fixture, then one again (duplicate).
	parts, err := filepath.Glob(filepath.Join(fixtureDir, "*.ndjson"))
	require.NoError(t, err)
	require.NotEmpty(t, parts)
	upload := func(path string) reply {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		var gz bytes.Buffer
		w := gzip.NewWriter(&gz)
		_, err = w.Write(raw)
		require.NoError(t, err)
		require.NoError(t, w.Close())
		part, typ, _ := strings.Cut(strings.TrimSuffix(filepath.Base(path), ".ndjson"), "_")
		return c.do("POST", "/v1/ingest/apple-health", tok.Token, &gz, map[string]string{
			"Content-Type": "application/gzip",
			"X-OHS-Type":   typ, "X-OHS-Day": "2026-01-10", "X-OHS-Batch": "contract", "X-OHS-Part": part,
		})
	}
	for _, p := range parts {
		r := upload(p)
		require.Equal(t, 200, r.status, "%s: %s", p, r.body)
	}
	assert.Equal(t, 200, upload(parts[0]).status)

	// Menstrual flow from Apple Health and from Health Connect, for the
	// derived cycles (invented days).
	gz := func(body string) io.Reader {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		_, err := w.Write([]byte(body))
		require.NoError(t, err)
		require.NoError(t, w.Close())
		return &b
	}
	var flow strings.Builder
	for _, d := range []string{"2026-01-02", "2026-01-03", "2026-01-30"} {
		fmt.Fprintf(&flow, `{"categoryType":"HKCategoryTypeIdentifierMenstrualFlow","startDate":"%sT08:00:00.000Z",`+
			`"endDate":"%sT08:00:00.000Z","value":3,"sourceName":"Health","metadata":{"HKTimeZone":"Europe/Paris"}}`+"\n", d, d)
	}
	r = c.do("POST", "/v1/ingest/apple-health", tok.Token, gz(flow.String()), map[string]string{
		"Content-Type": "application/gzip", "X-OHS-Type": "HKCategoryTypeIdentifierMenstrualFlow",
		"X-OHS-Day": "2026-01-10", "X-OHS-Batch": "contract", "X-OHS-Part": "90",
	})
	require.Equal(t, 200, r.status, "%s", r.body)
	r = c.do("POST", "/v1/ingest/health-connect", tok.Token, gz(
		`{"recordType":"MenstruationFlow","metadata":{"id":"mf-1"},"time":"2026-01-03T09:00:00Z","zoneOffset":"+01:00","flow":"heavy"}`+"\n"),
		map[string]string{
			"Content-Type": "application/gzip", "X-OHS-Type": "MenstruationFlow",
			"X-OHS-Day": "2026-01-03", "X-OHS-Batch": "contract", "X-OHS-Part": "91",
		})
	require.Equal(t, 200, r.status, "%s", r.body)

	// Coverage until processed (ruling R4).
	deadline := time.Now().Add(60 * time.Second)
	for {
		r := c.json("GET", "/v1/users/"+user.ID+"/coverage", key, "")
		require.Equal(t, 200, r.status, "%s", r.body)
		var cov struct {
			PendingJobs int `json:"pending_jobs"`
		}
		r.json(t, &cov)
		if cov.PendingJobs == 0 {
			break
		}
		require.True(t, time.Now().Before(deadline), "jobs still pending: %s", r.body)
		time.Sleep(200 * time.Millisecond)
	}
	assert.Equal(t, 200, c.json("GET", "/v1/users/"+user.ID+"/coverage?provider=apple_health&start=2026-01-01&end=2026-02-01", key, "").status)

	// Reads.
	u := "/v1/users/" + user.ID
	for _, p := range []string{
		u + "/samples?type=heart_rate&limit=1000",
		u + "/samples?type=step_count&start=2026-01-10&end=2026-01-12T00:00:00Z",
		u + "/samples?type=oxygen_saturation",
		u + "/category-samples",
		u + "/nutrition",
		u + "/daily",
		u + "/samples/stats?type=heart_rate&start=2026-01-01&end=2026-02-01",
		u + "/samples/stats?type=step_count&interval=hour&tz=Asia/Kolkata&start=2026-01-10&end=2026-01-12T00:00:00Z",
		u + "/daily?view=all",
		u + "/samples?type=step_count&view=all",
		u + "/sleep?view=merged",
		u + "/source-priorities",
	} {
		r := c.json("GET", p, key, "")
		assert.Equal(t, 200, r.status, "%s: %s", p, r.body)
	}
	r = c.json("PUT", u+"/source-priorities", key, `{"priorities":{"sleep":["oura","apple_health"],"*":["apple_health"]}}`)
	assert.Equal(t, 200, r.status, "%s", r.body)
	assert.Equal(t, 400, c.json("PUT", u+"/source-priorities", key, `{"priorities":{"sleep":[]}}`).status)
	assert.Equal(t, 400, c.json("GET", u+"/daily?view=raw", key, "").status)
	// RR intervals and the HRV derived from them: the fixtures carry no
	// heartbeat series, so one is written straight to the table.
	pool, err = db.Open(context.Background(), url)
	require.NoError(t, err)
	at := time.Date(2026, 1, 10, 7, 0, 0, 0, time.UTC)
	for i := range 12 {
		at = at.Add(800 * time.Millisecond)
		_, err := pool.Exec(context.Background(), `INSERT INTO ohs_data.heartbeat_samples
			(tenant_id, user_id, provider, series_id, "timestamp", rr_ms, content_hash)
			SELECT tenant_id, id, 'apple_health', 'contract-series', $2, 800, $3 FROM ohs_core.end_users WHERE id = $1`,
			user.ID, at, "contract-hb-"+strconv.Itoa(i))
		require.NoError(t, err)
	}
	// An ECG recording with invented samples, written the same way.
	var ecgID int64
	ecgAt := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	require.NoError(t, pool.QueryRow(context.Background(), `INSERT INTO ohs_data.ecg_recordings
		(tenant_id, user_id, provider, source_slug, start_date, end_date, classification, symptoms_status,
		 average_heart_rate, sampling_frequency, number_of_measurements, content_hash)
		SELECT tenant_id, id, 'apple_health', 'apple_health', $2, $3, 1, 1, 61, 512, 32, 'contract-ecg'
		FROM ohs_core.end_users WHERE id = $1 RETURNING id`, user.ID, ecgAt, ecgAt.Add(time.Second)).Scan(&ecgID))
	_, err = pool.Exec(context.Background(), `INSERT INTO ohs_data.ecg_samples
		(tenant_id, user_id, ecg_id, ts, time_since_sample_start, voltage_uv, content_hash)
		SELECT u.tenant_id, u.id, $2::bigint, $3::timestamptz + make_interval(secs => g / 512.0), g / 512.0,
			(g % 8) * 2.5 - 5, 'contract-ecg-' || g::text
		FROM ohs_core.end_users u, generate_series(0, 31) AS g WHERE u.id = $1`, user.ID, ecgID, ecgAt)
	require.NoError(t, err)
	pool.Close()
	for _, p := range []string{u + "/rr-intervals?limit=5", u + "/hrv", u + "/hrv?series_id=contract-series&start=2026-01-10"} {
		r := c.json("GET", p, key, "")
		assert.Equal(t, 200, r.status, "%s: %s", p, r.body)
		assert.Contains(t, string(r.body), "contract-series", p)
	}
	assert.Equal(t, 400, c.json("GET", u+"/hrv?cursor=nope", key, "").status)

	// ECG recordings: the list, one recording with its samples, its FHIR
	// Observation, and the export.
	ecgPath := strconv.FormatInt(ecgID, 10)
	for _, p := range []string{
		u + "/ecg",
		u + "/ecg?limit=1&start=2026-01-10&end=2026-01-11",
		u + "/ecg/" + ecgPath,
		u + "/fhir/Observation/ecg-" + ecgPath,
		u + "/fhir/Observation?code=http://loinc.org%7C11524-6",
		u + "/exports/ecg",
		u + "/exports/ecg?format=parquet",
	} {
		r := c.json("GET", p, key, "")
		assert.Equal(t, 200, r.status, "%s: %s", p, r.body)
	}
	assert.Equal(t, 404, c.json("GET", u+"/ecg/999999999", key, "").status)
	assert.Equal(t, 404, c.json("GET", u+"/fhir/Observation/ecg-999999999", key, "").status)
	assert.Equal(t, 400, c.json("GET", u+"/ecg?cursor=%25%25", key, "").status)

	// Page through heart rate with a small limit to exercise next_cursor.
	r = c.json("GET", u+"/samples?type=heart_rate&limit=100", key, "")
	require.Equal(t, 200, r.status)
	var page struct {
		NextCursor *string `json:"next_cursor"`
	}
	r.json(t, &page)
	require.NotNil(t, page.NextCursor)
	assert.Equal(t, 200, c.json("GET", u+"/samples?type=heart_rate&limit=100&cursor="+*page.NextCursor, key, "").status)

	// Menstrual cycles derived from both providers' flow, then paged.
	r = c.json("GET", u+"/menstrual-cycles", key, "")
	require.Equal(t, 200, r.status, "%s", r.body)
	var cycles struct {
		Data []struct {
			StartDate       string   `json:"start_date"`
			CycleLengthDays *int     `json:"cycle_length_days"`
			Providers       []string `json:"providers"`
		} `json:"data"`
	}
	r.json(t, &cycles)
	require.Len(t, cycles.Data, 2, "%s", r.body)
	assert.Equal(t, "2026-01-02", cycles.Data[0].StartDate)
	assert.Equal(t, []string{"apple_health", "health_connect"}, cycles.Data[0].Providers)
	require.NotNil(t, cycles.Data[0].CycleLengthDays)
	assert.Equal(t, 28, *cycles.Data[0].CycleLengthDays)
	assert.Nil(t, cycles.Data[1].CycleLengthDays)
	r = c.json("GET", u+"/menstrual-cycles?limit=1&start=2026-01-01", key, "")
	require.Equal(t, 200, r.status, "%s", r.body)
	r.json(t, &page)
	require.NotNil(t, page.NextCursor)
	assert.Equal(t, 200, c.json("GET", u+"/menstrual-cycles?limit=1&cursor="+*page.NextCursor, key, "").status)
	assert.Equal(t, 400, c.json("GET", u+"/menstrual-cycles?cursor=%25%25", key, "").status)

	// FHIR: a page of Observations, its next link, and one Observation by id.
	r = c.json("GET", u+"/fhir/Observation?type=heart_rate&limit=2", key, "")
	require.Equal(t, 200, r.status, "%s", r.body)
	var bundle struct {
		Link []struct {
			Relation string `json:"relation"`
			URL      string `json:"url"`
		} `json:"link"`
		Entry []struct {
			FullURL string `json:"fullUrl"`
		} `json:"entry"`
	}
	r.json(t, &bundle)
	require.Len(t, bundle.Entry, 2)
	var next string
	for _, l := range bundle.Link {
		if l.Relation == "next" {
			next = l.URL
		}
	}
	require.True(t, strings.HasPrefix(next, base+"/"), "next link %q under OHS_PUBLIC_URL", next)
	assert.Equal(t, 200, c.json("GET", strings.TrimPrefix(next, base), key, "").status)
	require.True(t, strings.HasPrefix(bundle.Entry[0].FullURL, base+"/"))
	assert.Equal(t, 200, c.json("GET", strings.TrimPrefix(bundle.Entry[0].FullURL, base), key, "").status)
	assert.Equal(t, 200, c.json("GET", u+"/fhir/Observation?code=http://loinc.org%7C8867-4&limit=5", key, "").status)
	assert.Equal(t, 200, c.json("GET", u+"/fhir/Observation?start=2026-01-10&end=2026-01-11", key, "").status)
	assert.Equal(t, 400, c.json("GET", u+"/fhir/Observation?code=8867-4", key, "").status)
	assert.Equal(t, 404, c.json("GET", u+"/fhir/Observation/qs-999999999", key, "").status, "no such row")

	r = c.json("GET", u+"/sleep", key, "")
	require.Equal(t, 200, r.status)
	var sleep struct{ Data []struct{ ID int64 } }
	r.json(t, &sleep)
	require.NotEmpty(t, sleep.Data)
	assert.Equal(t, 200, c.json("GET", u+"/sleep/"+strconv.FormatInt(sleep.Data[0].ID, 10)+"/phases", key, "").status)

	r = c.json("GET", u+"/workouts", key, "")
	require.Equal(t, 200, r.status)
	var workouts struct{ Data []struct{ ID int64 } }
	r.json(t, &workouts)
	require.NotEmpty(t, workouts.Data)
	r = c.json("GET", u+"/workouts/"+strconv.FormatInt(workouts.Data[0].ID, 10)+"/route", key, "")
	assert.Equal(t, 200, r.status, "a workout without GPS has an empty route: %s", r.body)
	assert.JSONEq(t, `{"data":[],"next_cursor":null}`, string(r.body))

	// FHIR: sleep sessions, workouts and daily summaries are Observations too.
	assert.Equal(t, 200, c.json("GET", u+"/fhir/Observation/sleep-"+strconv.FormatInt(sleep.Data[0].ID, 10), key, "").status)
	assert.Equal(t, 200, c.json("GET", u+"/fhir/Observation/workout-"+strconv.FormatInt(workouts.Data[0].ID, 10), key, "").status)
	assert.Equal(t, 200, c.json("GET", u+"/fhir/Observation?category=activity&limit=3", key, "").status)
	assert.Equal(t, 200, c.json("GET", u+"/fhir/Observation?code=http://loinc.org%7C85354-9", key, "").status)
	assert.Equal(t, 200, c.json("GET", u+"/fhir/Observation?code="+
		"https://github.com/soma-intelligence/open-health-sync/fhir/CodeSystem/record-type%7Csleep_session", key, "").status)
	assert.Equal(t, 400, c.json("GET", u+"/fhir/Observation?category=vitals", key, "").status)
	assert.Equal(t, 404, c.json("GET", u+"/fhir/Observation/bp-999999999", key, "").status)

	// FHIR: the Patient, its devices, the Provenance of an Observation, $everything.
	assert.Equal(t, 200, c.json("GET", u+"/fhir/Patient/"+user.ID, key, "").status)
	assert.Equal(t, 404, c.json("GET", u+"/fhir/Patient/00000000-0000-4000-8000-000000000000", key, "").status)
	assert.Equal(t, 200, c.json("GET", u+"/fhir/metadata", key, "").status)
	r = c.json("GET", u+"/fhir/Device", key, "")
	require.Equal(t, 200, r.status, "%s", r.body)
	var devices struct {
		Entry []struct {
			FullURL string `json:"fullUrl"`
		} `json:"entry"`
	}
	r.json(t, &devices)
	require.NotEmpty(t, devices.Entry, "the fixture names its devices")
	assert.Equal(t, 200, c.json("GET", strings.TrimPrefix(devices.Entry[0].FullURL, base), key, "").status)
	assert.Equal(t, 200, c.json("GET", u+"/fhir/Device?start=2026-01-10&end=2026-01-11", key, "").status)
	assert.Equal(t, 400, c.json("GET", u+"/fhir/Device?limit=2", key, "").status)
	assert.Equal(t, 404, c.json("GET", u+"/fhir/Device/dev-0000000000000000", key, "").status)
	obsID := strings.TrimPrefix(bundle.Entry[0].FullURL, base+u+"/fhir/Observation/")
	r = c.json("GET", u+"/fhir/Provenance?target=Observation/"+obsID, key, "")
	require.Equal(t, 200, r.status, "%s", r.body)
	assert.Contains(t, string(r.body), `"prov-`+obsID+`"`)
	assert.Equal(t, 200, c.json("GET", u+"/fhir/Provenance/prov-"+obsID, key, "").status)
	assert.Equal(t, 400, c.json("GET", u+"/fhir/Provenance", key, "").status)
	assert.Equal(t, 404, c.json("GET", u+"/fhir/Provenance/prov-qs-999999999", key, "").status)
	everything := u + "/fhir/Patient/" + user.ID + "/$everything"
	r = c.json("GET", everything+"?limit=3", key, "")
	require.Equal(t, 200, r.status, "%s", r.body)
	var page1 struct {
		Link []struct {
			Relation string `json:"relation"`
			URL      string `json:"url"`
		} `json:"link"`
	}
	r.json(t, &page1)
	next = ""
	for _, l := range page1.Link {
		if l.Relation == "next" {
			next = l.URL
		}
	}
	require.NotEmpty(t, next)
	assert.Equal(t, 200, c.json("GET", strings.TrimPrefix(next, base), key, "").status)
	assert.Equal(t, 400, c.json("GET", everything+"?cursor=bm9wZQ", key, "").status)

	// Open mHealth: a page of data points, its next cursor, one schema, a refusal.
	r = c.json("GET", u+"/omh/data-points?limit=2", key, "")
	require.Equal(t, 200, r.status, "%s", r.body)
	var omhPage struct {
		Data       []json.RawMessage `json:"data"`
		NextCursor *string           `json:"next_cursor"`
	}
	r.json(t, &omhPage)
	require.NotEmpty(t, omhPage.Data)
	require.NotNil(t, omhPage.NextCursor)
	assert.Equal(t, 200, c.json("GET", u+"/omh/data-points?limit=2&cursor="+*omhPage.NextCursor, key, "").status)
	assert.Equal(t, 200, c.json("GET", u+"/omh/data-points?schema=omh:heart-rate&start=2026-01-10&end=2026-01-11", key, "").status)
	assert.Equal(t, 400, c.json("GET", u+"/omh/data-points?schema=heart_rate", key, "").status)
	assert.Equal(t, 200, c.json("GET", u+"/omh/data-points?view=all&limit=2", key, "").status)

	// Exports: CSV and Parquet files, and the refusals.
	for _, p := range []string{
		u + "/exports/samples",
		u + "/exports/samples?format=parquet&type=heart_rate",
		u + "/exports/sleep?format=csv&start=2026-01-01&end=2026-02-01",
		u + "/exports/workouts?format=parquet",
		u + "/exports/category-samples",
		u + "/exports/nutrition?format=parquet",
		u + "/exports/daily",
		u + "/exports/daily?format=parquet&view=all",
	} {
		r := c.json("GET", p, key, "")
		assert.Equal(t, 200, r.status, "%s: %s", p, r.body)
	}
	assert.Equal(t, 400, c.json("GET", u+"/exports/samples?format=xlsx", key, "").status)
	assert.Equal(t, 400, c.json("GET", u+"/exports/samples?limit=10", key, "").status)

	// The user archive: a zip with the manifest, and a user token without read refused.
	r = c.json("GET", u+"/archive", key, "")
	require.Equal(t, 200, r.status, "%s", r.body)
	zr, err := zip.NewReader(bytes.NewReader(r.body), int64(len(r.body)))
	require.NoError(t, err)
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	assert.Contains(t, names, "manifest.json")
	assert.Contains(t, names, "fhir/bundle.json")
	assert.True(t, slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, "raw/apple_health/") }),
		"the uploaded parts: %v", names)
	assert.Equal(t, 403, c.json("GET", u+"/archive", tok.Token, "").status)

	// Connections: an Apple Health user has none (device uploads are not connections).
	r = c.json("GET", u+"/connections", key, "")
	assert.Equal(t, 200, r.status, "%s", r.body)
	assert.JSONEq(t, `{"data":[]}`, string(r.body))

	// Time tags: create (with a replay), list with a window, read, close, delete.
	r = c.do("POST", u+"/tags", key, strings.NewReader(`{"kind":"travel","label":"Trip","start_at":"2026-01-08",
		"timezone":"Asia/Tokyo","source":"contract"}`), map[string]string{"Content-Type": "application/json", "Idempotency-Key": "tag-1"})
	require.Equal(t, 201, r.status, "%s", r.body)
	var tag struct{ ID string }
	r.json(t, &tag)
	assert.Equal(t, 201, c.do("POST", u+"/tags", key, strings.NewReader(`{"kind":"travel","label":"Trip","start_at":"2026-01-08",
		"timezone":"Asia/Tokyo","source":"contract"}`), map[string]string{"Content-Type": "application/json", "Idempotency-Key": "tag-1"}).status)
	r = c.json("POST", u+"/tags", key, `{"kind":"illness","start_at":"2026-01-09T08:00:00Z","end_at":"2026-01-12T00:00:00Z","note":"invented"}`)
	require.Equal(t, 201, r.status, "%s", r.body)
	assert.Equal(t, 200, c.json("GET", u+"/tags?start=2026-01-10&end=2026-01-11&kind=travel&limit=1", key, "").status)
	r = c.json("GET", u+"/tags?limit=1", key, "")
	require.Equal(t, 200, r.status)
	r.json(t, &page)
	require.NotNil(t, page.NextCursor)
	assert.Equal(t, 200, c.json("GET", u+"/tags?limit=1&cursor="+*page.NextCursor, key, "").status)
	assert.Equal(t, 200, c.json("GET", u+"/tags/"+tag.ID, key, "").status)
	assert.Equal(t, 200, c.json("PATCH", u+"/tags/"+tag.ID, key, `{"end_at":"2026-01-15","label":null}`).status)
	assert.Equal(t, 400, c.json("PATCH", u+"/tags/"+tag.ID, key, `{"end_at":"2026-01-01"}`).status)
	assert.Equal(t, 400, c.json("POST", u+"/tags", key, `{"kind":"travel","start_at":"2026-01-08","timezone":"Mars/Base"}`).status)
	assert.Equal(t, 204, c.json("DELETE", u+"/tags/"+tag.ID, key, "").status)
	assert.Equal(t, 404, c.json("GET", u+"/tags/"+tag.ID, key, "").status)

	// Self reports: a rating (with a replay), the same from the user's own
	// app (a duplicate), a refusal, then the rating read back as a sample and
	// as a FHIR survey Observation once processed.
	feeling := `{"reports":[{"id":"0192f1a2-0000-7000-8000-00000000c0de","kind":"feeling","value":4,
		"start_at":"2026-01-10T07:30:00Z","timezone":"Europe/Paris"}]}`
	for range 2 {
		r = c.do("POST", u+"/self-reports", key, strings.NewReader(feeling),
			map[string]string{"Content-Type": "application/json", "Idempotency-Key": "self-report-1"})
		require.Equal(t, 202, r.status, "%s", r.body)
	}
	r = c.json("POST", u+"/self-reports", tok.Token, feeling)
	require.Equal(t, 202, r.status, "%s", r.body)
	assert.Contains(t, string(r.body), `"duplicate":true`)
	assert.Equal(t, 400, c.json("POST", u+"/self-reports", key,
		`{"reports":[{"kind":"feeling","value":9,"start_at":"2026-01-10T07:30:00Z"}]}`).status)
	deadline = time.Now().Add(60 * time.Second)
	for {
		r = c.json("GET", u+"/samples?type=reported_feeling", key, "")
		require.Equal(t, 200, r.status, "%s", r.body)
		if strings.Contains(string(r.body), "0192f1a2-0000-7000-8000-00000000c0de") {
			break
		}
		require.True(t, time.Now().Before(deadline), "self report not processed: %s", r.body)
		time.Sleep(200 * time.Millisecond)
	}
	r = c.json("GET", u+"/fhir/Observation?category=survey", key, "")
	require.Equal(t, 200, r.status, "%s", r.body)
	assert.Contains(t, string(r.body), `"performer"`)

	// Share links: make two, list and read them, then open one as its reader
	// would (a wrong passcode, the file at a location, the file), revoke it,
	// and the refusals. The public routes take four requests per address.
	shares := u + "/shares"
	expires := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	r = c.json("POST", shares, key, `{"categories":["sleep","heart"],"start":"2026-01-01","end":"2026-02-01",
		"expires_at":"`+expires+`","passcode":"invented-passcode","label":"Contract reader"}`)
	require.Equal(t, 201, r.status, "%s", r.body)
	var share struct {
		ID           string `json:"id"`
		URI          string `json:"uri"`
		Observations int    `json:"observations"`
	}
	r.json(t, &share)
	assert.Positive(t, share.Observations)
	r = c.json("POST", shares, key, `{"categories":["activity"],"start":"2026-01-10","end":"2026-01-11","expires_at":"`+expires+`"}`)
	require.Equal(t, 201, r.status, "%s", r.body)
	assert.Equal(t, 400, c.json("POST", shares, key, `{"categories":["location"],"start":"2026-01-01","end":"2026-02-01","expires_at":"`+expires+`"}`).status)
	assert.Equal(t, 400, c.json("POST", shares, key, `{"categories":["heart"],"start":"2026-01-01","end":"2026-02-01","expires_at":"2026-01-01"}`).status)
	assert.Equal(t, 403, c.json("GET", shares, tok.Token, "").status)
	r = c.json("GET", shares+"?limit=1", key, "")
	require.Equal(t, 200, r.status, "%s", r.body)
	r.json(t, &page)
	require.NotNil(t, page.NextCursor)
	assert.Equal(t, 200, c.json("GET", shares+"?limit=1&cursor="+*page.NextCursor, key, "").status)
	assert.Equal(t, 400, c.json("GET", shares+"?cursor=nope", key, "").status)
	assert.Equal(t, 404, c.json("GET", shares+"/00000000-0000-4000-8000-000000000000", key, "").status)

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(share.URI, shl.Prefix))
	require.NoError(t, err)
	var payload shl.Payload
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.NoError(t, payload.Validate())
	assert.Equal(t, shl.FlagPasscode, payload.Flag)
	manifest := strings.TrimPrefix(payload.URL, base)
	require.True(t, strings.HasPrefix(manifest, "/shl/"), "the link's url %q under OHS_PUBLIC_URL", payload.URL)
	assert.Equal(t, 204, c.do("OPTIONS", manifest, "", nil, map[string]string{
		"Origin": "https://viewer.invalid", "Access-Control-Request-Method": "POST"}).status)
	r = c.json("POST", manifest, "", `{"recipient":"Contract reader","passcode":"not-the-passcode"}`)
	assert.Equal(t, 401, r.status, "%s", r.body)
	assert.Contains(t, string(r.body), `"remainingAttempts":9`)
	r = c.json("POST", manifest, "", `{"recipient":"Contract reader","passcode":"invented-passcode","embeddedLengthMax":0}`)
	require.Equal(t, 200, r.status, "%s", r.body)
	var mf struct {
		Files []struct {
			Location string `json:"location"`
		} `json:"files"`
	}
	r.json(t, &mf)
	require.Len(t, mf.Files, 1)
	r = c.json("GET", strings.TrimPrefix(mf.Files[0].Location, base), "", "")
	require.Equal(t, 200, r.status, "%s", r.body)
	key32, err := shl.DecodeKey(payload.Key)
	require.NoError(t, err)
	plainFile, hdr, err := shl.Decrypt(key32, string(r.body), 64<<20)
	require.NoError(t, err)
	assert.Equal(t, shl.ContentFHIR, hdr.Cty)
	assert.Contains(t, string(plainFile), `"resourceType":"Bundle"`)
	assert.Equal(t, 200, c.json("GET", shares+"/"+share.ID, key, "").status)
	assert.Equal(t, 204, c.json("DELETE", shares+"/"+share.ID, key, "").status)
	assert.Equal(t, 204, c.json("DELETE", shares+"/"+share.ID, key, "").status, "revoking twice changes nothing")
	assert.Equal(t, 404, c.json("POST", manifest, "", `{"recipient":"Contract reader","passcode":"invented-passcode"}`).status)
	assert.Equal(t, 429, c.json("POST", manifest, "", `{"recipient":"Contract reader"}`).status, "over the share rate limit")
	assert.Equal(t, 200, c.json("GET", "/shl/view", "", "").status)
	assert.Equal(t, 200, c.json("GET", "/shl/view/view.mjs", "", "").status)
	assert.Equal(t, 404, c.json("GET", "/shl/view/missing.js", "", "").status)

	// Refusals are documented problems too.
	assert.Equal(t, 401, c.json("GET", "/v1/users", "", "").status)
	assert.Equal(t, 401, c.json("GET", "/v1/users", "ohs_sk_unknown", "").status)
	assert.Equal(t, 403, c.json("GET", "/v1/users", tok.Token, "").status)
	assert.Equal(t, 404, c.json("GET", "/v1/users/00000000-0000-4000-8000-000000000000", key, "").status)
	assert.Equal(t, 400, c.json("GET", u+"/samples?type=heart_rate&limit=5000", key, "").status)
	assert.Equal(t, 400, c.json("GET", u+"/samples?type=heartrate", key, "").status, "unknown type")
	assert.Equal(t, 400, c.json("POST", "/v1/users/"+user.ID+"/tokens", key, `{"scopes":["root"]}`).status)
	assert.Equal(t, 409, c.json("POST", "/v1/users", key, `{"external_ref":"contract-user"}`).status)
	assert.Equal(t, 400, c.do("POST", "/v1/ingest/apple-health", tok.Token, strings.NewReader("x"), map[string]string{
		"Content-Type": "application/gzip", "X-OHS-Type": "HKQuantityTypeIdentifierHeartRate",
		"X-OHS-Day": "2026-01-10", "X-OHS-Batch": "contract", "X-OHS-Part": "99",
	}).status, "not gzip")
	// The callback's own pages are HTML, which the recorder does not decode:
	// the calls spending the address's tokens go around it.
	plain := &client{t: t, base: base, http: &http.Client{Timeout: 30 * time.Second}}
	for range 4 {
		assert.Equal(t, 400, plain.json("GET", "/oauth/withings/callback?state=unknown", "", "").status, "unknown state")
	}
	r = c.json("GET", "/oauth/withings/callback?state=unknown", "", "")
	assert.Equal(t, 429, r.status, "over the callback rate limit: %s", r.body)

	// Erasing the user's data from one provider (a device provider, no
	// connection), and its job read back until done.
	assert.Equal(t, 404, c.json("DELETE", u+"/connections/withings?delete_data=true", key, "").status)
	assert.Equal(t, 404, c.json("DELETE", u+"/data/not_a_provider", key, "").status)
	r = c.json("DELETE", u+"/data/apple_health", key, "")
	require.Equal(t, 202, r.status, "%s", r.body)
	var erase struct {
		JobID string `json:"job_id"`
	}
	r.json(t, &erase)
	deadline = time.Now().Add(60 * time.Second)
	for {
		r = c.json("GET", "/v1/deletions/"+erase.JobID, key, "")
		require.Equal(t, 200, r.status, "%s", r.body)
		var dj struct {
			Status   string  `json:"status"`
			Provider *string `json:"provider"`
		}
		r.json(t, &dj)
		require.NotNil(t, dj.Provider)
		assert.Equal(t, "apple_health", *dj.Provider)
		if dj.Status == "done" {
			break
		}
		require.True(t, time.Now().Before(deadline), "provider deletion not done: %s", r.body)
		time.Sleep(200 * time.Millisecond)
	}
	r = c.json("GET", u+"/samples?type=heart_rate", key, "")
	require.Equal(t, 200, r.status, "%s", r.body)
	assert.JSONEq(t, `{"data":[],"next_cursor":null}`, string(r.body), "the provider's data is gone")

	// Deletion, and its job read back until done.
	r = c.json("DELETE", u, key, "")
	require.Equal(t, 202, r.status, "%s", r.body)
	var job struct {
		JobID string `json:"job_id"`
	}
	r.json(t, &job)
	assert.Equal(t, 404, c.json("GET", u, key, "").status)
	assert.Equal(t, 404, c.json("GET", "/v1/deletions/00000000-0000-4000-8000-000000000000", key, "").status)
	deadline = time.Now().Add(60 * time.Second)
	for {
		r = c.json("GET", "/v1/deletions/"+job.JobID, key, "")
		require.Equal(t, 200, r.status, "%s", r.body)
		var dj struct {
			Status string `json:"status"`
		}
		r.json(t, &dj)
		if dj.Status == "done" {
			break
		}
		require.True(t, time.Now().Before(deadline), "deletion not done: %s", r.body)
		time.Sleep(200 * time.Millisecond)
	}

	calls := rec.Calls()
	require.NotEmpty(t, calls)
	failed := 0
	for _, call := range calls {
		if len(call.Problems) == 0 {
			continue
		}
		failed++
		t.Errorf("%s %s -> %d (%s):\n  %s", call.Method, call.Path, call.Status, call.Operation,
			strings.Join(call.Problems, "\n  "))
	}
	t.Logf("%d calls validated against api/openapi.yaml, %d with problems", len(calls), failed)

	// Which documented operations this run exercised, and with which statuses.
	statuses := map[string][]int{}
	for _, call := range calls {
		if call.Operation != "" && !slices.Contains(statuses[call.Operation], call.Status) {
			statuses[call.Operation] = append(statuses[call.Operation], call.Status)
		}
	}
	var covered, missed []string
	for _, op := range rec.Operations() {
		if st, ok := statuses[op]; ok {
			slices.Sort(st)
			covered = append(covered, fmt.Sprintf("%s %v", op, st))
		} else {
			missed = append(missed, op)
		}
	}
	t.Logf("operations covered (%d of %d):\n  %s", len(covered), len(covered)+len(missed), strings.Join(covered, "\n  "))
	t.Logf("operations not exercised: %s", strings.Join(missed, ", "))
}
