// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package synthflow

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/app"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/activityfiles"
	"github.com/soma-intelligence/open-health-sync/internal/synth/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/bluetooth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/dexcom"
	"github.com/soma-intelligence/open-health-sync/internal/synth/fitbit"
	"github.com/soma-intelligence/open-health-sync/internal/synth/healthconnect"
	"github.com/soma-intelligence/open-health-sync/internal/synth/intervalsicu"
	"github.com/soma-intelligence/open-health-sync/internal/synth/omron"
	"github.com/soma-intelligence/open-health-sync/internal/synth/oura"
	"github.com/soma-intelligence/open-health-sync/internal/synth/polar"
	"github.com/soma-intelligence/open-health-sync/internal/synth/selfreport"
	"github.com/soma-intelligence/open-health-sync/internal/synth/strava"
	"github.com/soma-intelligence/open-health-sync/internal/synth/suunto"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
	"github.com/soma-intelligence/open-health-sync/internal/synth/whoop"
	"github.com/soma-intelligence/open-health-sync/internal/synth/withings"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// provider is one renderer and how its lines reach the server: route is the
// upload path of a device provider, empty for a cloud provider whose lines
// are stored the way a pull stores them.
type provider struct {
	slug   string
	route  string
	render func(*synth.Timeline) []synth.Line
}

var providers = []provider{
	{applehealth.Slug, "/v1/ingest/apple-health", applehealth.Lines},
	{healthconnect.Slug, "/v1/ingest/health-connect", healthconnect.Lines},
	{bluetooth.Slug, "/v1/ingest/bluetooth", bluetooth.Lines},
	{activityfiles.Slug, "/v1/ingest/activity-files", activityfiles.Lines},
	{selfreport.Slug, "", selfreport.Lines},
	{oura.Slug, "", oura.Lines},
	{whoop.Slug, "", whoop.Lines},
	{polar.Slug, "", polar.Lines},
	{withings.Slug, "", withings.Lines},
	{strava.Slug, "", strava.Lines},
	{fitbit.Slug, "", fitbit.Lines},
	{dexcom.Slug, "", dexcom.Lines},
	{intervalsicu.Slug, "", intervalsicu.Lines},
	{suunto.Slug, "", suunto.Lines},
	{omron.Slug, "", omron.Lines},
}

// What the server did with one part, read from its own logs.
type verdict string

const (
	refused   verdict = "refused"   // the upload route answered 400
	failed    verdict = "failed"    // the ETL logged a failed record or object
	written   verdict = "written"   // at least one record written, none failed
	duplicate verdict = "duplicate" // only duplicates of stored records
	nothing   verdict = "nothing"   // processed, nothing written or failed
)

// part is one raw object sent to the server.
type part struct {
	key      string
	lines    []synth.Line
	refused  bool
	expected []synthtest.Result // the offline harness, one per line
	verdict  verdict
	reasons  []string
}

type env struct {
	t     *testing.T
	base  string
	key   string
	pool  *pgxpool.Pool
	raw   rawstore.Store
	tenat uuid.UUID
	now   time.Time
}

func TestEveryProviderThroughTheServer(t *testing.T) {
	// About twenty minutes on a 4 core machine: the nightly synthetic-data
	// workflow runs it, `make integration` does not.
	if os.Getenv("OHS_SYNTH_E2E") == "" {
		t.Skip("set OHS_SYNTH_E2E=1 to run every provider through the server")
	}
	ctx := context.Background()
	e := start(t)
	// A window a year back, so the DST scenario's autumn days are past too,
	// and Now is the real clock, the one the server checks "future" against.
	opt := synth.Options{
		Seed: 11, Days: 3, Now: e.now,
		Start:     time.Date(e.now.Year()-1, 3, 20, 0, 0, 0, 0, time.UTC),
		Scenarios: []synth.Scenario{synth.All},
	}
	tl := synth.Build(opt)

	// OHS_SYNTH_PROVIDERS=oura,whoop runs only those.
	if only := os.Getenv("OHS_SYNTH_PROVIDERS"); only != "" {
		keep := strings.Split(only, ",")
		providers = slices.DeleteFunc(slices.Clone(providers), func(p provider) bool { return !slices.Contains(keep, p.slug) })
	}
	report := map[string]*providerReport{}
	for _, p := range providers {
		c, ok := connector.Get(p.slug)
		require.True(t, ok, "connector %s is registered", p.slug)
		user, token := e.user(p.slug)
		lines := p.render(tl)
		parts := e.send(p, c, user, token, lines)
		report[p.slug] = &providerReport{slug: p.slug, user: user, parts: parts, lines: len(lines)}
	}
	t.Logf("sent in %s", time.Since(e.now).Round(time.Second))
	for _, r := range report {
		e.waitProcessed(r.user)
	}
	t.Logf("processed in %s", time.Since(e.now).Round(time.Second))
	for _, p := range providers {
		r := report[p.slug]
		e.judge(r)
		e.inspect(r)
		e.readAPI(r)
		t.Logf("%s read back at %s", p.slug, time.Since(e.now).Round(time.Second))
	}

	// The invariants: nothing the vendor documents is lost, nothing broken
	// is stored, nothing stored breaks the model, no job is left parked.
	for _, p := range providers {
		r := report[p.slug]
		t.Run(p.slug, func(t *testing.T) {
			if r.parked > 0 {
				t.Errorf("%d parked jobs: %v", r.parked, r.parkedWhy)
			}
			for _, pt := range r.parts {
				for _, l := range pt.lines {
					if l.Valid() && (pt.verdict == failed || pt.verdict == refused) {
						t.Errorf("documented %s line not stored (%s %v): %.200s", l.DataType, pt.verdict, pt.reasons, l.JSON)
					}
					if l.Fault == synth.FaultMalformed && pt.verdict == written {
						t.Errorf("malformed %s line stored (%s): %.200s", l.DataType, l.Tag, l.JSON)
					}
				}
			}
			require.NotZero(t, r.rows, "the baseline stores something")
			for _, b := range r.broken {
				t.Errorf("stored record breaks the model: %s", b)
			}
			for _, f := range r.findings {
				t.Logf("finding: %s", f)
			}
			for _, a := range r.api {
				t.Errorf("read API: %s", a)
			}
		})
	}
	if path := os.Getenv("OHS_SYNTH_REPORT"); path != "" {
		require.NoError(t, os.WriteFile(path, []byte(markdown(opt, report)), 0o644))
	}
	_ = ctx
}

func start(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dbURL := db.TestDB(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	rawDir := "file://" + t.TempDir()
	cfg := config.Config{
		DatabaseURL:   dbURL,
		RawStore:      rawDir,
		Listen:        addr,
		LogLevel:      "error",
		PublicURL:     "http://" + addr,
		Roles:         map[string]bool{"api": true, "collect": true, "etl": true},
		EncryptionKey: bytes.Repeat([]byte{9}, 32),
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- app.Run(runCtx, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(40 * time.Second):
			t.Error("server did not stop")
		}
	})
	base := "http://" + addr
	eventually(t, 30*time.Second, "/healthz", func() bool {
		select {
		case err := <-done:
			t.Fatalf("server stopped: %v", err)
		default:
		}
		res, err := http.Get(base + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == http.StatusOK
	})
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	tid, key, err := tenant.New(pool).CreateTenant(ctx, "synthflow")
	require.NoError(t, err)
	raw, err := rawstore.Open(rawDir)
	require.NoError(t, err)
	return &env{t: t, base: base, key: key, pool: pool, raw: raw, tenat: tid.ID, now: time.Now().UTC()}
}

var client = &http.Client{Timeout: 90 * time.Second}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		require.True(t, time.Now().Before(deadline), "timed out waiting for %s", what)
		time.Sleep(200 * time.Millisecond)
	}
}

func (e *env) do(method, path, secret string, body io.Reader, header map[string]string) (int, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.base+path, body)
	require.NoError(e.t, err)
	req.Header.Set("Authorization", "Bearer "+secret)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	began := time.Now()
	res, err := client.Do(req)
	if err != nil {
		// A route that does not answer within the client's timeout is a
		// finding, not a hang of the whole test.
		return 0, []byte(fmt.Sprintf("no answer after %s: %v", time.Since(began).Round(time.Second), err))
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, []byte(fmt.Sprintf("answer cut after %s (%d bytes read): %v", time.Since(began).Round(time.Second), len(b), err))
	}
	if d := time.Since(began); d > 5*time.Second {
		e.t.Logf("slow: %s %s took %s, %d bytes", method, path, d.Round(time.Millisecond), len(b))
	}
	return res.StatusCode, b
}

func (e *env) post(path, body string) []byte {
	e.t.Helper()
	code, b := e.do("POST", path, e.key, strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
	require.Equal(e.t, http.StatusCreated, code, "%s %s", path, b)
	return b
}

// user creates the user of one provider and an upload token.
func (e *env) user(slug string) (uuid.UUID, string) {
	e.t.Helper()
	var u struct {
		ID uuid.UUID `json:"id"`
	}
	require.NoError(e.t, json.Unmarshal(e.post("/v1/users", `{"external_ref":"synth-`+slug+`"}`), &u))
	var tok struct {
		Token string `json:"token"`
	}
	require.NoError(e.t, json.Unmarshal(e.post("/v1/users/"+u.ID.String()+"/tokens", `{"scopes":["upload"]}`), &tok))
	return u.ID, tok.Token
}

// send groups the documented lines into one part per data type and day and
// sends every other line as a part of its own.
func (e *env) send(p provider, c connector.Connector, user uuid.UUID, token string, lines []synth.Line) []*part {
	e.t.Helper()
	type group struct {
		dataType string
		day      time.Time
	}
	var order []group
	good := map[group][]synth.Line{}
	var parts []*part
	n := 0
	for _, l := range lines {
		if l.Day.IsZero() {
			l.Day = e.now.Truncate(24 * time.Hour)
		}
		if !l.Valid() || p.slug == activityfiles.Slug { // the files route takes one file per part
			parts = append(parts, e.store(p, c, user, token, l.Day, l.DataType, "fault", n, []synth.Line{l}))
			n++
			continue
		}
		g := group{l.DataType, l.Day.UTC().Truncate(24 * time.Hour)}
		if _, ok := good[g]; !ok {
			order = append(order, g)
		}
		good[g] = append(good[g], l)
	}
	for _, g := range order {
		parts = append(parts, e.store(p, c, user, token, g.day, g.dataType, "good", n, good[g]))
		n++
	}
	return parts
}

func (e *env) store(p provider, c connector.Connector, user uuid.UUID, token string, day time.Time, dataType, batch string, n int, lines []synth.Line) *part {
	e.t.Helper()
	day = day.UTC().Truncate(24 * time.Hour)
	var body bytes.Buffer
	for _, l := range lines {
		body.Write(l.JSON)
		body.WriteByte('\n')
	}
	pt := &part{lines: lines, key: rawstore.Key(e.tenat, user, p.slug, day, batch, n)}
	for _, l := range lines {
		pt.expected = append(pt.expected, synthtest.Run(c, []synth.Line{l}, e.now)...)
	}
	gz := gzipped(e.t, body.Bytes())
	if p.route != "" {
		code, b := e.do("POST", p.route, token, bytes.NewReader(gz), map[string]string{
			"Content-Type": "application/gzip", "X-OHS-Type": dataType, "X-OHS-Day": day.Format(time.DateOnly),
			"X-OHS-Batch": batch, "X-OHS-Part": strconv.Itoa(n),
		})
		switch code {
		case http.StatusOK:
			var up struct {
				Duplicate bool `json:"duplicate"`
			}
			if json.Unmarshal(b, &up) == nil && up.Duplicate {
				pt.refused, pt.verdict, pt.reasons = true, duplicate, []string{"same bytes already stored"}
			}
		case http.StatusBadRequest:
			pt.refused, pt.verdict, pt.reasons = true, refused, []string{problem(b)}
		default:
			e.t.Fatalf("%s %s part %d: %d %s", p.route, dataType, n, code, b)
		}
		return pt
	}
	ctx := context.Background()
	sum := sha256.Sum256(gz)
	require.NoError(e.t, e.raw.Put(ctx, pt.key, bytes.NewReader(gz)))
	tx, err := e.pool.Begin(ctx)
	require.NoError(e.t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		INSERT INTO ohs_sync.raw_objects (tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, data_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING`,
		e.tenat, user, p.slug, pt.key, hex.EncodeToString(sum[:]), len(gz), batch, n, dataType)
	require.NoError(e.t, err)
	if tag.RowsAffected() == 0 {
		// The same bytes are already registered: a pull stores nothing.
		pt.refused, pt.verdict, pt.reasons = true, duplicate, []string{"same bytes already stored"}
		return pt
	}
	require.NoError(e.t, queue.EnqueueETL(ctx, tx, queue.ETLJob{TenantID: e.tenat, UserID: user, Provider: p.slug, Key: pt.key}))
	require.NoError(e.t, tx.Commit(ctx))
	return pt
}

func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	_, err := w.Write(b)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return out.Bytes()
}

func problem(b []byte) string {
	var p struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(b, &p) == nil && p.Detail != "" {
		return p.Detail
	}
	return string(b)
}

type coverage struct {
	PendingJobs     int   `json:"pending_jobs"`
	ParkedJobs      int   `json:"parked_jobs"`
	RejectedRecords int64 `json:"rejected_records"`
}

func (e *env) coverage(user uuid.UUID) coverage {
	e.t.Helper()
	code, b := e.do("GET", "/v1/users/"+user.String()+"/coverage", e.key, nil, nil)
	require.Equal(e.t, http.StatusOK, code, "%s", b)
	var c coverage
	require.NoError(e.t, json.Unmarshal(b, &c))
	return c
}

// waitProcessed waits for the user's jobs; on a timeout it names the jobs
// still pending, with their attempts and last error.
func (e *env) waitProcessed(user uuid.UUID) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	for e.coverage(user).PendingJobs != 0 {
		if time.Now().After(deadline) {
			rows, err := e.pool.Query(context.Background(), `SELECT provider, key, status, attempts, coalesce(last_error, ''), not_before
				FROM ohs_sync.etl_jobs WHERE user_id = $1 AND status IN ('pending', 'running') ORDER BY attempts DESC LIMIT 15`, user)
			require.NoError(e.t, err)
			for rows.Next() {
				var prov, key, status, msg string
				var attempts int
				var nb time.Time
				require.NoError(e.t, rows.Scan(&prov, &key, &status, &attempts, &msg, &nb))
				e.t.Logf("stuck: %s %s %s attempts=%d not_before=%s %s", prov, key, status, attempts, nb.Format(time.RFC3339), clip(msg, 300))
			}
			e.t.Fatalf("timed out waiting for the ETL jobs of %s: %+v", user, e.coverage(user))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// judge reads what the ETL logged for each part.
func (e *env) judge(r *providerReport) {
	ctx := context.Background()
	for _, pt := range r.parts {
		if pt.refused {
			continue
		}
		var wrote, dups, fails int
		var logged bool
		rows, err := e.pool.Query(ctx, `SELECT records_written, duplicates_skipped, records_failed, coalesce(error_message, '')
			FROM ohs_sync.sync_log WHERE stage = 'etl' AND raw_key = $1`, pt.key)
		require.NoError(e.t, err)
		for rows.Next() {
			var w, d, f int
			var msg string
			require.NoError(e.t, rows.Scan(&w, &d, &f, &msg))
			wrote, dups, fails, logged = wrote+w, dups+d, fails+f, true
			if msg != "" {
				pt.reasons = append(pt.reasons, msg)
			}
		}
		require.NoError(e.t, rows.Err())
		frows, err := e.pool.Query(ctx, `SELECT coalesce(reason_code, ''), coalesce(error_message, '')
			FROM ohs_data.failed_records WHERE raw_key = $1`, pt.key)
		require.NoError(e.t, err)
		for frows.Next() {
			var code, msg string
			require.NoError(e.t, frows.Scan(&code, &msg))
			fails++
			pt.reasons = append(pt.reasons, code+": "+msg)
		}
		require.NoError(e.t, frows.Err())
		switch {
		case fails > 0:
			pt.verdict = failed
		case wrote > 0:
			pt.verdict = written
		case dups > 0:
			pt.verdict = duplicate
		default:
			pt.verdict = nothing
		}
		if !logged && fails == 0 {
			pt.reasons = append(pt.reasons, "no sync_log row")
		}
	}
	c := e.coverage(r.user)
	r.parked, r.rejected = c.ParkedJobs, c.RejectedRecords
	rows, err := e.pool.Query(ctx, `SELECT left(coalesce(last_error, ''), 200), count(*) FROM ohs_sync.etl_jobs
		WHERE user_id = $1 AND status = 'parked' GROUP BY 1 ORDER BY 2 DESC LIMIT 5`, r.user)
	require.NoError(e.t, err)
	for rows.Next() {
		var msg string
		var n int
		require.NoError(e.t, rows.Scan(&msg, &n))
		r.parkedWhy = append(r.parkedWhy, fmt.Sprintf("%d x %s", n, msg))
	}
	require.NoError(e.t, rows.Err())
}

// providerReport is what one provider's data became.
type providerReport struct {
	slug      string
	user      uuid.UUID
	lines     int
	parts     []*part
	parked    int
	parkedWhy []string
	rejected  int64
	rows      int64
	suspects  int64
	broken    []string
	findings  []string
	api       []string
	types     []string
}

// inspect checks every stored record of the user directly in the database.
func (e *env) inspect(r *providerReport) {
	ctx := context.Background()
	future := e.now.Add(48 * time.Hour)
	count := func(q string, args ...any) int64 {
		var n int64
		require.NoError(e.t, e.pool.QueryRow(ctx, q, args...).Scan(&n), q)
		return n
	}
	tables := []struct{ table, start, end string }{
		{"quantity_samples", "start_date", "end_date"},
		{"category_samples", "start_date", "end_date"},
		{"sleep_sessions", "bedtime_start", "bedtime_end"},
		{"workouts", "start_date", "end_date"},
		{"nutrition_logs", "start_date", "end_date"},
	}
	for _, tb := range tables {
		where := ` FROM ohs_data.` + tb.table + ` WHERE tenant_id = $1 AND user_id = $2`
		r.rows += count(`SELECT count(*)`+where, e.tenat, r.user)
		if n := count(`SELECT count(*)`+where+` AND `+tb.start+` > $3`, e.tenat, r.user, future); n > 0 {
			r.broken = append(r.broken, fmt.Sprintf("%d %s start more than 48 h in the future", n, tb.table))
		}
		if n := count(`SELECT count(*)`+where+` AND `+tb.end+` < `+tb.start, e.tenat, r.user); n > 0 {
			r.broken = append(r.broken, fmt.Sprintf("%d %s end before their start", n, tb.table))
		}
		if n := count(`SELECT count(*)`+where+` AND `+tb.start+` < '2000-01-01'`, e.tenat, r.user); n > 0 {
			// Open: no rule bounds the past yet (docs/specs/synthetic-data.md).
			r.findings = append(r.findings, fmt.Sprintf("%d %s start before 2000 (Unix or FIT epoch dates)", n, tb.table))
		}
	}
	q := ` FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2`
	if n := count(`SELECT count(*)`+q+` AND (value = 'NaN' OR value = 'Infinity' OR value = '-Infinity')`, e.tenat, r.user); n > 0 {
		r.broken = append(r.broken, fmt.Sprintf("%d quantity samples are NaN or infinite", n))
	}
	r.suspects = count(`SELECT count(*)`+q+` AND metadata->>'quality_flag' = 'suspect'`, e.tenat, r.user)

	// The same value at the same instant from the same source, twice.
	rows, err := e.pool.Query(ctx, `SELECT quantity_type, start_date, value, count(*)`+q+`
		GROUP BY quantity_type, start_date, end_date, value, unit, source_name, source_bundle_id, device_model, external_id
		HAVING count(*) > 1 ORDER BY 1, 2 LIMIT 20`, e.tenat, r.user)
	require.NoError(e.t, err)
	for rows.Next() {
		var typ string
		var at time.Time
		var v float64
		var n int
		require.NoError(e.t, rows.Scan(&typ, &at, &v, &n))
		r.findings = append(r.findings, fmt.Sprintf("stored twice: %s %s = %v, %d rows", typ, at.Format(time.RFC3339), v, n))
	}
	require.NoError(e.t, rows.Err())

	trows, err := e.pool.Query(ctx, `SELECT DISTINCT quantity_type`+q+` ORDER BY 1`, e.tenat, r.user)
	require.NoError(e.t, err)
	types, err := pgx.CollectRows(trows, pgx.RowTo[string])
	require.NoError(e.t, err)
	r.types = types
}

// readAPI reads every quantity type back through the REST API and checks
// that each other output answers.
func (e *env) readAPI(r *providerReport) {
	u := "/v1/users/" + r.user.String()
	ctx := context.Background()
	for _, typ := range r.types {
		var want int64
		require.NoError(e.t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_data.quantity_samples
			WHERE tenant_id = $1 AND user_id = $2 AND quantity_type = $3`, e.tenat, r.user, typ).Scan(&want))
		var got int64
		next := u + "/samples?view=all&limit=1000&type=" + url.QueryEscape(typ)
		for next != "" {
			code, b := e.do("GET", next, e.key, nil, nil)
			if code != http.StatusOK {
				r.api = append(r.api, fmt.Sprintf("GET samples type=%s: %d %.200s", typ, code, b))
				break
			}
			var page struct {
				Data       []json.RawMessage `json:"data"`
				NextCursor *string           `json:"next_cursor"`
			}
			require.NoError(e.t, json.Unmarshal(b, &page))
			got += int64(len(page.Data))
			next = ""
			if page.NextCursor != nil {
				next = u + "/samples?view=all&limit=1000&type=" + url.QueryEscape(typ) + "&cursor=" + url.QueryEscape(*page.NextCursor)
			}
		}
		if got != want {
			r.api = append(r.api, fmt.Sprintf("samples type=%s: the API serves %d of %d stored rows", typ, got, want))
		}
	}
	for _, path := range []string{
		u + "/sleep", u + "/workouts", u + "/category-samples", u + "/nutrition", u + "/daily",
		u + "/fhir/Observation", u + "/omh/data-points",
	} {
		if code, b := e.do("GET", path, e.key, nil, nil); code != http.StatusOK {
			r.api = append(r.api, fmt.Sprintf("GET %s: %d %.200s", strings.TrimPrefix(path, u), code, b))
		}
	}
	// The export streams every sample: its speed is a finding, an error is
	// a failure. view=all reads the rows as stored, the default merges the
	// sources, so the two show what the merge costs.
	for _, path := range []string{u + "/exports/samples", u + "/exports/samples?view=all"} {
		began := time.Now()
		code, b := e.do("GET", path, e.key, nil, nil)
		took := time.Since(began).Round(100 * time.Millisecond)
		switch {
		case code == 0:
			r.findings = append(r.findings, fmt.Sprintf("GET %s: %s", strings.TrimPrefix(path, u), b))
		case code != http.StatusOK:
			r.api = append(r.api, fmt.Sprintf("GET %s: %d %.200s", strings.TrimPrefix(path, u), code, b))
		case took > 10*time.Second:
			r.findings = append(r.findings, fmt.Sprintf("GET %s: %d bytes in %s", strings.TrimPrefix(path, u), len(b), took))
		}
	}
}

// agrees says whether the server's verdict on a part matches the harness.
func agrees(pt *part) bool {
	want := map[synthtest.Outcome]bool{}
	for _, x := range pt.expected {
		want[x.Outcome] = true
	}
	switch pt.verdict {
	case refused, duplicate:
		// The upload route checks more than Map (it wants the route's data
		// type, one file per part), and the same bytes are processed once.
		return pt.verdict == duplicate || !pt.lines[0].Valid()
	case failed:
		return want[synthtest.ParseError] || want[synthtest.MapError] || want[synthtest.UnitError] || want[synthtest.Rejected]
	case written:
		return want[synthtest.Stored]
	default:
		return want[synthtest.Empty] || want[synthtest.Stored]
	}
}

// markdown is the anomaly report: per provider, what the server caught and
// what it let through.
func markdown(opt synth.Options, report map[string]*providerReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Synthetic data through the whole server\n\n")
	fmt.Fprintf(&b, "Seed %d, %d days from %s, scenario all. Every line with a fault is its own part.\n\n",
		opt.Seed, opt.Days, opt.Start.Format(time.DateOnly))
	b.WriteString("| Provider | Lines | Faulty | Refused at upload | Refused by ETL | Stored with a suspect flag | Faulty and stored | Rows stored | Server and harness disagree |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	var slugs []string
	for s := range report {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	let := map[string]map[string]int{} // provider -> fault -> faulty lines stored
	var disagree []string
	for _, s := range slugs {
		r := report[s]
		var faulty, door, etl, through, dis int
		let[s] = map[string]int{}
		for _, pt := range r.parts {
			if len(pt.lines) != 1 || pt.lines[0].Valid() {
				continue
			}
			l := pt.lines[0]
			faulty++
			switch pt.verdict {
			case refused:
				door++
			case failed:
				etl++
			case written:
				through++
				let[s][string(l.Fault)+" "+l.Tag]++
			}
			if !agrees(pt) {
				dis++
				var want []string
				for _, x := range pt.expected {
					want = append(want, string(x.Outcome))
				}
				disagree = append(disagree, fmt.Sprintf("| %s | %s | %s | %s | %s |", s, l.Fault,
					strings.Join(want, ","), pt.verdict, digits.ReplaceAllString(clip(strings.Join(pt.reasons, "; "), 120), "N")))
			}
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d | %d |\n", s, r.lines, faulty, door, etl, r.suspects, through, r.rows, dis)
	}
	b.WriteString("\n## Faulty lines the server stored\n\nBy fault and tag. A fault here is not always a bug (an unknown field is ignored on purpose); each is a value the server keeps although the line breaks the vendor's documentation.\n\n")
	b.WriteString("| Provider | Fault and tag | Lines |\n|---|---|---|\n")
	for _, s := range slugs {
		keys := make([]string, 0, len(let[s]))
		for k := range let[s] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "| %s | %s | %d |\n", s, strings.TrimSpace(k), let[s][k])
		}
	}
	b.WriteString("\n## What the server got wrong or slow\n\n")
	listed := false
	for _, s := range slugs {
		r := report[s]
		var items []string
		if r.parked > 0 {
			items = append(items, fmt.Sprintf("%d parked jobs: %s", r.parked, strings.Join(r.parkedWhy, "; ")))
		}
		items = append(items, r.broken...)
		items = append(items, r.api...)
		items = append(items, r.findings...)
		for _, it := range items {
			fmt.Fprintf(&b, "- %s: %s\n", s, clip(it, 400))
			listed = true
		}
	}
	if !listed {
		b.WriteString("Nothing.\n")
	}
	b.WriteString("\n## Server and harness disagree\n\nLines grouped by provider, fault, the harness outcome, the server's verdict and its reason (numbers as N).\n\n")
	if len(disagree) == 0 {
		b.WriteString("None.\n")
	} else {
		count := map[string]int{}
		for _, d := range disagree {
			count[d]++
		}
		keys := make([]string, 0, len(count))
		for k := range count {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		b.WriteString("| Provider | Fault | Harness | Server | Server's reason | Lines |\n|---|---|---|---|---|---|\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "%s %d |\n", k, count[k])
		}
	}
	return b.String()
}

var digits = regexp.MustCompile(`[0-9]+`)

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "|", "/")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
