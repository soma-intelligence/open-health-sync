// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

// Package vault holds the end to end tests of the health data vault. This
// file captures the read outputs of the server before any vault code exists,
// so a later test can prove that the same reads give the same bodies once
// the data is sealed. The golden files hold demo (synthetic) data only.
package vault

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/demo"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// goldenDir holds one file per captured output.
const goldenDir = "testdata/golden"

// goldenWriteEnv turns TestGoldenCapture on: without it the test skips, so a
// normal run never rewrites the golden files.
const goldenWriteEnv = "OHS_VAULT_GOLDEN_WRITE"

// goldenToday is the day after the last demo day. It is fixed, and in the
// past, so the demo records do not depend on the clock.
var goldenToday = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// goldenDays is the number of demo days seeded.
const goldenDays = 7

// goldenSeed is the seed of the demo person.
const goldenSeed = 1

// goldenFixture is a served instance holding the demo data of one user.
type goldenFixture struct {
	srv  *httptest.Server
	key  string
	user uuid.UUID
}

// seedGolden serves the API on a migrated database and fills one user with
// the demo data set, then with what the demo does not generate: ECG
// recordings, a heartbeat series and menstrual flow, all invented and fixed.
func seedGolden(t *testing.T) goldenFixture {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	st := tenant.New(pool)
	tn, key, err := st.CreateTenant(ctx, "Golden")
	require.NoError(t, err)
	u, err := st.CreateUser(ctx, tn.ID, "golden-1")
	require.NoError(t, err)

	batches, err := demo.Generate(demo.Options{Days: goldenDays, Seed: goldenSeed, Today: goldenToday})
	require.NoError(t, err)
	for _, b := range batches {
		_, err := etl.WriteSynthetic(ctx, pool, tn.ID, u.ID, b.Provider, b.Records)
		require.NoError(t, err, b.Provider)
	}
	seedExtras(t, pool, tn.ID, u.ID)
	_, err = pool.Exec(ctx, `CALL refresh_continuous_aggregate('ohs_data.quantity_hourly', NULL, NULL)`)
	require.NoError(t, err)

	srv := httptest.NewServer(api.NewRouter(api.Deps{Pool: pool, Store: st}))
	t.Cleanup(srv.Close)
	return goldenFixture{srv: srv, key: key, user: u.ID}
}

// seedExtras writes the record kinds the demo person has none of.
func seedExtras(t *testing.T, pool *pgxpool.Pool, tn, u uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	// Menstrual flow: two cycles, so the derived cycle list has a length.
	flow := &model.MapResult{}
	for i, day := range []string{"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04", "2026-09-29", "2026-09-30"} {
		d, err := time.Parse("2006-01-02", day)
		require.NoError(t, err)
		flow.CategorySamples = append(flow.CategorySamples, model.CategorySample{
			Provider: "apple_health", ExternalID: fmt.Sprintf("golden-flow-%d", i),
			CategoryType: "menstrual_flow", ValueInt: 3, ValueLabel: "medium",
			StartDate: d.Add(8 * time.Hour), EndDate: d.Add(9 * time.Hour), SourceName: "Health",
		})
	}
	_, err := etl.WriteSynthetic(ctx, pool, tn, u, "apple_health", flow)
	require.NoError(t, err)

	// One heartbeat series of 120 beats.
	beats := &model.MapResult{}
	at := time.Date(2026, 9, 30, 22, 0, 0, 0, time.UTC)
	for i := range 120 {
		rr := 800 + float64((i*37)%90) - 45
		beats.HeartbeatSamples = append(beats.HeartbeatSamples, model.HeartbeatSample{
			SeriesID: "golden-series", Timestamp: at, RRMS: rr,
		})
		at = at.Add(time.Duration(rr * float64(time.Millisecond)))
	}
	_, err = etl.WriteSynthetic(ctx, pool, tn, u, "apple_health", beats)
	require.NoError(t, err)

	// Two ECG recordings with their voltage samples, as the writer stores
	// them (sample time = start + offset).
	for i, rec := range []struct {
		provider string
		start    time.Time
		class    int
		hz       float64
		n        int
	}{
		{"apple_health", time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC), 1, 512, 200},
		{"withings", time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC), 0, 300, 120},
	} {
		end := rec.start.Add(time.Duration(float64(rec.n) / rec.hz * float64(time.Second)))
		var id int64
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO ohs_data.ecg_recordings
			(tenant_id, user_id, provider, source_slug, start_date, end_date, classification, symptoms_status,
			 average_heart_rate, sampling_frequency, number_of_measurements, content_hash, metadata)
			VALUES ($1, $2, $3, $3, $4, $5, $6, 0, 64, $7, $8, $9, '{"synthetic":true}') RETURNING id`,
			tn, u, rec.provider, rec.start, end, rec.class, rec.hz, rec.n, fmt.Sprintf("golden-ecg-%d", i)).Scan(&id))
		_, err := pool.Exec(ctx, `INSERT INTO ohs_data.ecg_samples
			(tenant_id, user_id, ecg_id, ts, time_since_sample_start, voltage_uv, content_hash)
			SELECT $1, $2, $3::bigint, $4::timestamptz + make_interval(secs => g / $5::float8), g / $5::float8,
				(g % 50) * 1.5 - 20, 'golden-ecg-' || $3::text || '-' || g::text
			FROM generate_series(0, $6::int - 1) AS g`, tn, u, id, rec.start, rec.hz, rec.n)
		require.NoError(t, err)
	}
}

// uuidRe matches any UUID: tenant and user ids differ on every run.
var uuidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// hostRe matches the address of the test server in an absolute link, and
// cursorRe a paging cursor in one: the port changes and a cursor encodes row
// ids.
var (
	hostRe   = regexp.MustCompile(`http://127\.0\.0\.1:[0-9]+`)
	cursorRe = regexp.MustCompile(`cursor=[A-Za-z0-9_%=-]+`)
)

// volatileKeys are the object keys and CSV columns whose value changes from
// run to run, in every body. id is the row id of each record of the lists
// (samples, sleep, workouts, ECG, cycles, FHIR resources, the id column of
// the CSV exports); ids come from sequences that other writes advance.
// ingested_at is the ingestion time (samples, RR intervals);
// creation_date_time is the Open mHealth header's ingestion time; recorded
// and issued are the ingestion times of FHIR Provenance and Observation.
// request_id is generated per request: no captured 200 body carries it today
// (it is in idempotent replies and share and error bodies), it is listed
// so a capture of such an output stays stable.
// Among the captured outputs none carries created_at or updated_at (other
// outputs do: timetag, the MCP user, webhooks, shares), and last_synced_at
// (coverage only) is stripped by that output alone (see captureOutputs). A
// future capture of an output that carries created_at or updated_at must
// list the key here, or strip it for that output, with its reason.
var volatileKeys = map[string]bool{
	"id": true, "ingested_at": true, "request_id": true,
	"creation_date_time": true, "recorded": true, "issued": true,
}

// volatileKey says whether a key holds something that differs between two
// captures of the same data. A number under a key ending in _id is a row id.
func volatileKey(k string, v any) bool {
	if volatileKeys[k] {
		return true
	}
	if _, num := v.(json.Number); num && strings.HasSuffix(k, "_id") {
		return true
	}
	return false
}

// normalize removes what changes between two captures of the same data:
// ingestion times, request ids, row ids, paging cursors and generated UUIDs
// (every UUID is replaced by a fixed text). A JSON body is re-encoded with
// sorted keys; a CSV body loses its volatile columns.
//
// Blind spot, on purpose: ids and UUIDs are masked, so normalize cannot see a
// vault that renumbers or reorders rows, or corrupts a UUID valued field
// (user_id and the like), and cursors are masked too. A test that uses these
// bodies as the reference (Task 19) must cover row ids, ordering and paging
// with a check of its own.
func normalize(body []byte, also ...string) []byte {
	body = uuidRe.ReplaceAll(body, []byte("<uuid>"))
	body = hostRe.ReplaceAll(body, []byte("http://<host>"))
	body = cursorRe.ReplaceAll(body, []byte("cursor=<cursor>"))
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return normalizeCSV(body)
	}
	out, err := json.MarshalIndent(strip(v, also), "", "  ")
	if err != nil {
		return body
	}
	return append(out, '\n')
}

// normalizeCSV drops the volatile columns of a CSV body. A body that is not
// CSV is returned as it is.
func normalizeCSV(body []byte) []byte {
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil || len(rows) == 0 {
		return body
	}
	var keep []int
	for i, h := range rows[0] {
		if !volatileKey(h, nil) {
			keep = append(keep, i)
		}
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	for _, r := range rows {
		row := make([]string, 0, len(keep))
		for _, i := range keep {
			row = append(row, r[i])
		}
		_ = w.Write(row)
	}
	w.Flush()
	return buf.Bytes()
}

// strip drops the volatile keys from a decoded JSON value and hides the
// paging cursors, which encode row ids. also names more keys to drop.
func strip(v any, also []string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if volatileKey(k, e) || slices.Contains(also, k) {
				delete(x, k)
				continue
			}
			if k == "next_cursor" && e != nil {
				x[k] = "<cursor>"
				continue
			}
			x[k] = strip(e, also)
		}
	case []any:
		for i := range x {
			x[i] = strip(x[i], also)
		}
	}
	return v
}

// captureOutputs calls every read output of the user and returns the
// normalized bodies by name. Paged lists are followed to their end, a page
// per entry (name, name.p2...). It fails the test on any answer other than
// 200.
func captureOutputs(t *testing.T, srv *httptest.Server, key string, user uuid.UUID) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	base := "/v1/users/" + user.String()

	fetch := func(path string) []byte {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, res.StatusCode, path+": "+string(body))
		return body
	}
	// list captures a paged list, one entry per page.
	list := func(name, path string) {
		t.Helper()
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		cursor := ""
		for page := 1; ; page++ {
			p := path
			if cursor != "" {
				p += sep + "cursor=" + url.QueryEscape(cursor)
			}
			body := fetch(p)
			entry := name
			if page > 1 {
				entry = fmt.Sprintf("%s.p%d", name, page)
			}
			out[entry] = normalize(body)
			var env struct {
				Next *string `json:"next_cursor"`
			}
			if json.Unmarshal(body, &env) != nil || env.Next == nil || *env.Next == "" {
				return
			}
			cursor = *env.Next
			require.Less(t, page, 50, name+": paging does not end")
		}
	}
	// ids reads the id of every record of a list page.
	ids := func(path string) []string {
		t.Helper()
		var env struct {
			Data []struct {
				ID json.Number `json:"id"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(fetch(path), &env))
		var r []string
		for _, d := range env.Data {
			r = append(r, d.ID.String())
		}
		return r
	}

	// The quantity types of the demo person, plus the ones added by hand.
	batches, err := demo.Generate(demo.Options{Days: goldenDays, Seed: goldenSeed, Today: goldenToday})
	require.NoError(t, err)
	var types []string
	for _, b := range batches {
		for _, q := range b.Records.QuantitySamples {
			types = append(types, q.QuantityType)
		}
	}
	slices.Sort(types)
	types = slices.Compact(types)
	require.NotEmpty(t, types)
	for _, qt := range types {
		list("samples_"+qt, base+"/samples?type="+qt+"&limit=1000")
	}

	// Stats, hourly and daily, in three zones. The window is whole days.
	for _, qt := range []string{"heart_rate", "step_count"} {
		for _, iv := range []string{"hour", "day"} {
			for _, tz := range []string{"UTC", "America/Los_Angeles", "Asia/Kolkata"} {
				path := fmt.Sprintf("%s/samples/stats?type=%s&interval=%s&tz=%s&start=2026-09-24&end=2026-10-01",
					base, qt, iv, url.QueryEscape(tz))
				out[fmt.Sprintf("stats_%s_%s_%s", qt, iv, strings.ReplaceAll(tz, "/", "-"))] = normalize(fetch(path))
			}
		}
	}
	out["stats_heart_rate_day_all"] = normalize(fetch(base + "/samples/stats?type=heart_rate&interval=day&view=all&start=2026-09-24&end=2026-10-01"))

	list("category_samples", base+"/category-samples?limit=1000")
	list("sleep", base+"/sleep?limit=1000")
	for i, id := range ids(base + "/sleep?limit=1000") {
		list(fmt.Sprintf("sleep_phases_%02d", i), base+"/sleep/"+id+"/phases?limit=1000")
	}
	list("workouts", base+"/workouts?limit=1000")
	for i, id := range ids(base + "/workouts?limit=1000") {
		list(fmt.Sprintf("workout_route_%02d", i), base+"/workouts/"+id+"/route?limit=1000")
	}
	list("nutrition", base+"/nutrition?limit=1000")
	list("daily", base+"/daily?limit=1000")
	list("menstrual_cycles", base+"/menstrual-cycles?limit=1000")
	list("ecg", base+"/ecg?limit=1000")
	for i, id := range ids(base + "/ecg?limit=1000") {
		out[fmt.Sprintf("ecg_%02d", i)] = normalize(fetch(base + "/ecg/" + id))
	}
	list("rr_intervals", base+"/rr-intervals?limit=1000")
	list("hrv", base+"/hrv?limit=1000")
	// Coverage alone carries last_synced_at, the time of the write.
	out["coverage"] = normalize(fetch(base+"/coverage?start=2026-09-24&end=2026-10-01"), "last_synced_at")

	out["fhir_everything"] = normalize(fetch(base + "/fhir/Patient/" + user.String() + "/$everything?limit=200"))
	// One page of each: the lists are long.
	out["omh_data_points"] = normalize(fetch(base + "/omh/data-points?limit=200"))
	for _, ds := range []string{"samples", "category-samples", "sleep", "workouts", "nutrition", "daily", "ecg"} {
		out["export_"+ds] = normalize(fetch(base + "/exports/" + ds + "?format=csv"))
	}

	// MCP: get_daily and get_sleep are the daily and sleep summaries.
	cs := mcpSession(t, srv.URL+"/mcp", key)
	for _, tool := range []string{"get_daily", "get_sleep", "get_sample_stats"} {
		args := map[string]any{"user_id": user.String()}
		if tool == "get_sample_stats" {
			args["type"], args["start"], args["end"] = "heart_rate", "2026-09-24", "2026-10-01"
		}
		res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
		require.NoError(t, err, tool)
		require.False(t, res.IsError, tool)
		require.Len(t, res.Content, 1, tool)
		txt, ok := res.Content[0].(*sdk.TextContent)
		require.True(t, ok, tool)
		out["mcp_"+tool] = normalize([]byte(txt.Text))
	}
	return out
}

// bearer is a round tripper that adds the API key to every MCP request.
type bearer struct{ key string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

// mcpSession opens an MCP client session on endpoint, authenticated with the
// API key, and closes it when the test ends.
func mcpSession(t *testing.T, endpoint, key string) *sdk.ClientSession {
	t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "golden", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: &http.Client{Transport: bearer{key}}, MaxRetries: -1,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// TestGoldenCapture writes the golden files. It runs only with
// OHS_VAULT_GOLDEN_WRITE=1, on the commit before any vault code.
func TestGoldenCapture(t *testing.T) {
	if os.Getenv(goldenWriteEnv) != "1" {
		t.Skip("set " + goldenWriteEnv + "=1 to rewrite the golden files")
	}
	f := seedGolden(t)
	got := captureOutputs(t, f.srv, f.key, f.user)
	require.NoError(t, os.RemoveAll(goldenDir))
	require.NoError(t, os.MkdirAll(goldenDir, 0o755))
	for name, body := range got {
		ext := ".json"
		if strings.HasPrefix(name, "export_") {
			ext = ".csv"
		}
		require.NoError(t, os.WriteFile(filepath.Join(goldenDir, name+ext), body, 0o644))
	}
	t.Logf("wrote %d golden files", len(got))
}

// TestGoldenStable captures twice, on two separate instances with other
// tenant and user ids, and compares: whatever differs is what normalize
// still misses.
func TestGoldenStable(t *testing.T) {
	a := seedGolden(t)
	first := captureOutputs(t, a.srv, a.key, a.user)
	b := seedGolden(t)
	second := captureOutputs(t, b.srv, b.key, b.user)
	require.Equal(t, len(first), len(second))
	for name, body := range first {
		require.Equal(t, string(body), string(second[name]), name)
	}
	for name, body := range first {
		require.NotEmpty(t, body, name)
	}
}
