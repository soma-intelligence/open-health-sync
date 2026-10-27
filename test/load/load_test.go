// SPDX-License-Identifier: AGPL-3.0-only

//go:build load

// Package load fills a LOCAL server with the synthetic Apple Health data of
// several invented people (internal/synth) and times the read API. It never
// runs in CI and refuses any host but localhost or 127.0.0.1.
//
//	OHS_LOAD_URL=http://127.0.0.1:18093 OHS_LOAD_KEY=ohs_sk_... \
//	  go test -tags load -count=1 -v -timeout 60m ./test/load/
//
// Env: OHS_LOAD_USERS (default 6), OHS_LOAD_DAYS (default 30),
// OHS_LOAD_VOLUME (sampling scale, default 1), OHS_LOAD_RUNS (timed runs per
// query, default 40), OHS_LOAD_WAIT (seconds to wait for the worker, default
// 1800), OHS_LOAD_TIME_USER (skip the upload, time the reads of this user).
package load

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/applehealth"
)

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

type client struct {
	base string
	http *http.Client
}

func (c *client) do(t *testing.T, method, path, token string, body io.Reader, hdr map[string]string) (int, []byte, time.Duration) {
	t.Helper()
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, time.Since(start)
}

func (c *client) json(t *testing.T, method, path, token string, in any, want int) map[string]any {
	t.Helper()
	b, _ := json.Marshal(in)
	code, out, _ := c.do(t, method, path, token, bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if code != want {
		t.Fatalf("%s %s: %d %s", method, path, code, out)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	return m
}

type part struct {
	dataType, day string
	body          []byte
}

func parts(t *testing.T, tl *synth.Timeline) ([]part, int) {
	t.Helper()
	type key struct{ dt, day string }
	var order []key
	groups := map[key]*bytes.Buffer{}
	n := 0
	for _, l := range applehealth.Lines(tl) {
		if !l.Valid() || l.Tag == "duplicate" {
			continue
		}
		k := key{l.DataType, l.Day.UTC().Format(time.DateOnly)}
		if groups[k] == nil {
			groups[k] = &bytes.Buffer{}
			order = append(order, k)
		}
		groups[k].Write(l.JSON)
		groups[k].WriteByte('\n')
		n++
	}
	var out []part
	for _, k := range order {
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write(groups[k].Bytes())
		_ = zw.Close()
		out = append(out, part{k.dt, k.day, gz.Bytes()})
	}
	return out, n
}

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(float64(len(s)-1) * p)
	return s[i]
}

func TestLoad(t *testing.T) {
	base := os.Getenv("OHS_LOAD_URL")
	key := os.Getenv("OHS_LOAD_KEY")
	if base == "" || key == "" {
		t.Skip("set OHS_LOAD_URL and OHS_LOAD_KEY")
	}
	u, err := url.Parse(base)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatalf("refusing %q: the load test only runs against a local server", base)
	}
	users, days := envInt("OHS_LOAD_USERS", 6), envInt("OHS_LOAD_DAYS", 30)
	vol, _ := strconv.ParseFloat(os.Getenv("OHS_LOAD_VOLUME"), 64)
	runs, wait := envInt("OHS_LOAD_RUNS", 40), envInt("OHS_LOAD_WAIT", 1800)
	c := &client{base: base, http: &http.Client{Timeout: 5 * time.Minute}}

	type person struct{ id, token string }
	var people []person
	if id := os.Getenv("OHS_LOAD_TIME_USER"); id != "" {
		// Only time the reads of a user filled by an earlier run.
		users = 0
		people = append(people, person{id: id})
	}
	var totalLines, totalParts, totalBytes int
	uploadStart := time.Now()
	for i := 0; i < users; i++ {
		u := c.json(t, "POST", "/v1/users", key, map[string]any{"external_ref": fmt.Sprintf("load-%d-%d", time.Now().Unix(), i)}, 201)
		id := u["id"].(string)
		tok := c.json(t, "POST", "/v1/users/"+id+"/tokens", key, map[string]any{"scopes": []string{"upload"}}, 201)["token"].(string)
		people = append(people, person{id, tok})
		tl := synth.Build(synth.Options{Seed: uint64(100 + i), Days: days, Volume: vol,
			Persona:   synth.Personas[i%len(synth.Personas)],
			Scenarios: []synth.Scenario{synth.Baseline, synth.DST, synth.MultiDevice}})
		ps, n := parts(t, tl)
		totalLines += n
		totalParts += len(ps)
		var wg sync.WaitGroup
		var refused atomic.Int64
		ch := make(chan int)
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range ch {
					p := ps[j]
					for attempt := 0; ; attempt++ {
						code, b, _ := c.do(t, "POST", "/v1/ingest/apple-health", tok, bytes.NewReader(p.body), map[string]string{
							"Content-Type": "application/gzip", "X-OHS-Type": p.dataType, "X-OHS-Day": p.day,
							"X-OHS-Batch": "load", "X-OHS-Part": strconv.Itoa(j)})
						if code == http.StatusServiceUnavailable && attempt < 20 {
							time.Sleep(500 * time.Millisecond)
							continue
						}
						if code != http.StatusOK {
							refused.Add(1)
							t.Logf("part %d %s %s: %d %s", j, p.dataType, p.day, code, b)
						}
						break
					}
				}
			}()
		}
		for j := range ps {
			totalBytes += len(ps[j].body)
			ch <- j
		}
		close(ch)
		wg.Wait()
		t.Logf("user %d (%s, persona %s): %d lines in %d parts, %d refused", i, id, synth.Personas[i%len(synth.Personas)], n, len(ps), refused.Load())
	}
	t.Logf("uploaded %d lines in %d parts (%d gzip bytes) for %d users in %s",
		totalLines, totalParts, totalBytes, users, time.Since(uploadStart).Round(time.Millisecond))

	procStart := time.Now()
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for _, p := range people[:users] {
		for {
			code, b, _ := c.do(t, "GET", "/v1/users/"+p.id+"/coverage", key, nil, nil)
			var cov struct {
				Pending int `json:"pending_jobs"`
				Parked  int `json:"parked_jobs"`
			}
			_ = json.Unmarshal(b, &cov)
			if code == 200 && cov.Pending == 0 {
				if cov.Parked != 0 {
					t.Errorf("user %s: %d parked jobs", p.id, cov.Parked)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("user %s still pending: %s", p.id, b)
			}
			time.Sleep(time.Second)
		}
	}
	t.Logf("worker drained every user %s after the last upload", time.Since(procStart).Round(time.Millisecond))

	// Times reads of the busiest user.
	p := people[0]
	queries := []string{
		"/samples?type=heart_rate&limit=1000",
		"/samples?type=heart_rate&view=all&limit=1000",
		"/samples?type=step_count&limit=1000",
		"/samples/stats?type=heart_rate&interval=day&start=2026-03-01&end=2026-05-01",
		"/samples/stats?type=step_count&interval=hour&start=2026-03-01&end=2026-05-01",
		"/samples/stats?type=heart_rate&interval=day&view=all&start=2026-03-01&end=2026-05-01",
		"/daily",
		"/sleep",
		"/workouts",
		"/coverage",
		"/hrv",
		"/fhir/Observation?limit=100",
		"/samples?type=heart_rate&limit=1000&start=2026-04-10&end=2026-04-12",
	}
	t.Logf("%-50s %8s %8s %8s %6s", "query", "p50", "p95", "max", "code")
	for _, q := range queries {
		var ds []time.Duration
		last := 0
		for r := 0; r < runs; r++ {
			code, _, d := c.do(t, "GET", "/v1/users/"+p.id+q, key, nil, nil)
			last = code
			ds = append(ds, d)
		}
		t.Logf("%-50s %8s %8s %8s %6d", q, pct(ds, .5).Round(10*time.Microsecond), pct(ds, .95).Round(10*time.Microsecond),
			pct(ds, 1).Round(10*time.Microsecond), last)
	}
	// Full pagination of one user's heart rate.
	start := time.Now()
	n, pages, cursor := 0, 0, ""
	for {
		path := "/v1/users/" + p.id + "/samples?type=heart_rate&view=all&limit=1000"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		code, b, _ := c.do(t, "GET", path, key, nil, nil)
		if code != 200 {
			t.Fatalf("page %d: %d %s", pages, code, b)
		}
		var pg struct {
			Data []json.RawMessage `json:"data"`
			Next *string           `json:"next_cursor"`
		}
		_ = json.Unmarshal(b, &pg)
		n += len(pg.Data)
		pages++
		if pg.Next == nil || *pg.Next == "" {
			break
		}
		cursor = *pg.Next
	}
	t.Logf("paged %d heart rate samples in %d pages in %s", n, pages, time.Since(start).Round(time.Millisecond))
	// One full export.
	for _, ds := range []string{"samples"} {
		code, b, d := c.do(t, "GET", "/v1/users/"+p.id+"/exports/"+ds, key, nil, nil)
		t.Logf("export %s: %d, %d bytes in %s", ds, code, len(b), d.Round(time.Millisecond))
	}
}
