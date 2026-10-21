// SPDX-License-Identifier: AGPL-3.0-only

// Command ingest benchmarks the Apple Health upload path of ohs: how fast it
// takes in a batch of HealthKit samples, how much memory and CPU it uses doing
// it, and how fast it reads a day back. See docs/benchmarks/ingest.md.
//
//	go run ./bench/ingest gen    -out DIR [-days 14] [-hr-interval 60] [-seed 42]
//	go run ./bench/ingest run    -data DIR -out FILE [-concurrency 4] [-reads 50]
//	go run ./bench/ingest report RESULTS_DIR
//
// gen writes DIR/<day>_<part>.ndjson.gz, one request per (day, type), and
// DIR/manifest.json. run starts from an empty database, starts `ohs serve`,
// uploads every request of the manifest, waits until every record is stored,
// then reads one day of heart rate back. It samples the RSS and CPU time of the
// server every 100 ms and the CPU time of the Postgres container. report prints
// the median of each metric over the <profile>-<run>.json files run wrote.
// Standard library only; bench/ingest/setup.sh prepares Postgres and bin/ohs.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ingest gen|run|report [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = gen(os.Args[2:])
	case "run":
		err = run(os.Args[2:])
	case "report":
		err = report(os.Args[2:], os.Stdout)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ingest:", err)
		os.Exit(1)
	}
}

// ---- gen ----

const tzName = "Europe/Paris"

// January in Paris: UTC+01:00, no DST change.
var tz = time.FixedZone("CET", 3600)

type hkType struct {
	id, unit string
	every    int // seconds between samples; 0 takes -hr-interval
}

var types = []hkType{
	{"HKQuantityTypeIdentifierHeartRate", "count/min", 0},
	{"HKQuantityTypeIdentifierStepCount", "count", 300},
	{"HKQuantityTypeIdentifierActiveEnergyBurned", "kcal", 300},
	{"HKQuantityTypeIdentifierOxygenSaturation", "%", 3600},
}

type request struct {
	Name    string `json:"name"`
	Day     string `json:"day"`
	Part    int    `json:"part"`
	Type    string `json:"type"`
	Records int    `json:"records"`
}

type manifest struct {
	Seed       uint64    `json:"seed"`
	Days       int       `json:"days"`
	Start      string    `json:"start"`
	HRInterval int       `json:"hr_interval"`
	Requests   []request `json:"requests"`
	Records    int       `json:"records"`
}

// line is one HealthKit sample in the NDJSON upload format.
type line struct {
	QuantityType   string            `json:"quantityType"`
	StartDate      string            `json:"startDate"`
	EndDate        string            `json:"endDate"`
	Value          float64           `json:"value"`
	Unit           string            `json:"unit"`
	UUID           string            `json:"uuid"`
	SourceName     string            `json:"sourceName"`
	SourceBundleID string            `json:"sourceBundleId"`
	Device         string            `json:"device"`
	DeviceModel    string            `json:"deviceModel"`
	ProductType    string            `json:"productType"`
	WasUserEntered bool              `json:"wasUserEntered"`
	Metadata       map[string]string `json:"metadata"`
}

// values is a plausible series of n values for one type.
func values(rng *mrand.Rand, id string, n int) []float64 {
	out := make([]float64, n)
	switch {
	case strings.HasSuffix(id, "HeartRate"):
		v := 70.0
		for i := range out {
			v = math.Min(170, math.Max(48, v+rng.NormFloat64()*2.5))
			out[i] = round(v, 1)
		}
	case strings.HasSuffix(id, "StepCount"):
		for i := range out {
			if rng.IntN(4) == 3 {
				out[i] = float64(10 + rng.IntN(591))
			}
		}
	case strings.HasSuffix(id, "ActiveEnergyBurned"):
		for i := range out {
			out[i] = round(0.5+rng.Float64()*24.5, 3)
		}
	default:
		for i := range out {
			out[i] = round(0.94+rng.Float64()*0.05, 2)
		}
	}
	return out
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func isoUTC(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// recordID is a deterministic UUID-shaped id: hi from the seed, lo a counter.
func recordID(hi, lo uint64) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", hi>>32, (hi>>16)&0xffff, hi&0xffff, lo>>48, lo&0xffffffffffff)
}

func gen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	out := fs.String("out", "", "output directory (required)")
	days := fs.Int("days", 14, "number of local days")
	start := fs.String("start", "2026-01-05", "first local day, YYYY-MM-DD")
	hr := fs.Int("hr-interval", 60, "seconds between heart rate samples")
	seed := fs.Uint64("seed", 42, "random seed")
	_ = fs.Parse(args)
	if *out == "" || *hr <= 0 || *days <= 0 {
		return errors.New("gen: -out is required, -days and -hr-interval must be positive")
	}
	day0, err := time.ParseInLocation("2006-01-02", *start, tz)
	if err != nil {
		return fmt.Errorf("gen: -start: %w", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	rng := mrand.New(mrand.NewPCG(*seed, *seed))
	idHi := rng.Uint64()
	m := manifest{Seed: *seed, Days: *days, Start: *start, HRInterval: *hr}
	var counter uint64
	for d := range *days {
		t0 := day0.AddDate(0, 0, d)
		day := t0.Format("2006-01-02")
		for part, ty := range types {
			step := ty.every
			if step == 0 {
				step = *hr
			}
			n := 86400 / step
			var body bytes.Buffer
			enc := json.NewEncoder(&body)
			instant := strings.HasSuffix(ty.id, "HeartRate") || strings.HasSuffix(ty.id, "OxygenSaturation")
			for i, v := range values(rng, ty.id, n) {
				s := t0.Add(time.Duration(i*step) * time.Second)
				e := s
				if !instant {
					e = s.Add(time.Duration(step) * time.Second)
				}
				counter++
				if err := enc.Encode(line{
					QuantityType: ty.id, StartDate: isoUTC(s), EndDate: isoUTC(e), Value: v, Unit: ty.unit,
					UUID: recordID(idHi, counter), SourceName: "Apple Watch", SourceBundleID: "com.apple.health.BENCH",
					Device: "Apple Watch", DeviceModel: "Watch", ProductType: "Watch7,1",
					Metadata: map[string]string{"HKTimeZone": tzName},
				}); err != nil {
					return err
				}
			}
			name := fmt.Sprintf("%s_%d", day, part)
			var gz bytes.Buffer
			// A zero header time keeps the gzip bytes identical across runs.
			w := gzip.NewWriter(&gz)
			if _, err := w.Write(body.Bytes()); err != nil {
				return err
			}
			if err := w.Close(); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(*out, name+".ndjson.gz"), gz.Bytes(), 0o644); err != nil {
				return err
			}
			m.Requests = append(m.Requests, request{Name: name, Day: day, Part: part, Type: ty.id, Records: n})
			m.Records += n
		}
	}
	b, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "manifest.json"), b, 0o644); err != nil {
		return err
	}
	fmt.Printf("%d requests, %d records -> %s\n", len(m.Requests), m.Records, *out)
	return nil
}

// ---- run ----

type latency struct {
	N    int     `json:"n"`
	Mean float64 `json:"mean"`
	P50  float64 `json:"p50"`
	P95  float64 `json:"p95"`
	P99  float64 `json:"p99"`
	Max  float64 `json:"max"`
}

type result struct {
	System      string `json:"system"`
	Version     string `json:"version"`
	Concurrency int    `json:"concurrency"`
	Dataset     struct {
		Seed         uint64 `json:"seed"`
		Days         int    `json:"days"`
		Start        string `json:"start"`
		HRInterval   int    `json:"hr_interval"`
		Records      int    `json:"records"`
		Requests     int    `json:"requests"`
		RequestBytes int64  `json:"request_bytes"`
	} `json:"dataset"`
	Ingest struct {
		Statuses            map[string]int `json:"statuses"`
		Retries503          int            `json:"retries_503"`
		Stored              int            `json:"stored"`
		Expected            int            `json:"expected"`
		UploadSeconds       float64        `json:"upload_seconds"`
		EndToEndSeconds     float64        `json:"end_to_end_seconds"`
		UploadRecordsPerS   float64        `json:"upload_records_per_s"`
		EndToEndRecordsPerS float64        `json:"end_to_end_records_per_s"`
		UploadLatencyMS     latency        `json:"upload_latency_ms"`
	} `json:"ingest"`
	MemoryBytes struct {
		Idle map[string]int64 `json:"idle"`
		Peak map[string]int64 `json:"peak"`
	} `json:"memory_bytes"`
	CPUSeconds map[string]float64 `json:"cpu_seconds"`
	Read       struct {
		Day       string  `json:"day"`
		Rows      int     `json:"rows"`
		LatencyMS latency `json:"latency_ms"`
	} `json:"read"`
	StorageBytes struct {
		Database int64 `json:"database"`
		RawFiles int64 `json:"raw_files"`
	} `json:"storage_bytes"`
}

type runner struct {
	bin, base, key, user, token string
	pgPort                      int
	env                         []string
	client                      *http.Client
}

func sh(env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// psql uses the host client, so the Postgres container's CPU time holds only
// the server's work.
func (r *runner) psql(db, sql string) (string, error) {
	out, err := sh(nil, "psql", "-h", "127.0.0.1", "-p", strconv.Itoa(r.pgPort), "-U", "postgres", "-d", db, "-tAc", sql)
	return strings.TrimSpace(out), err
}

func (r *runner) do(method, path string, body []byte, headers map[string]string) (int, []byte, http.Header, error) {
	req, err := http.NewRequest(method, r.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header, err
}

func (r *runner) tenant() map[string]string {
	return map[string]string{"Authorization": "Bearer " + r.key, "Content-Type": "application/json"}
}

var tenantKey = regexp.MustCompile(`ohs_sk_\S+`)

func (r *runner) setupUser() error {
	out, err := sh(r.env, r.bin, "admin", "tenant", "create", "--name", "bench")
	if err != nil {
		return err
	}
	if r.key = tenantKey.FindString(out); r.key == "" {
		return fmt.Errorf("no tenant key in: %s", out)
	}
	var v struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	_, b, _, err := r.do("POST", "/v1/users", []byte("{}"), r.tenant())
	if err != nil || json.Unmarshal(b, &v) != nil {
		return fmt.Errorf("create user: %v %s", err, b)
	}
	r.user = v.ID
	_, b, _, err = r.do("POST", "/v1/users/"+r.user+"/tokens", []byte(`{"scopes":["upload"],"ttl_seconds":86400}`), r.tenant())
	if err != nil || json.Unmarshal(b, &v) != nil {
		return fmt.Errorf("create token: %v %s", err, b)
	}
	r.token = v.Token
	return nil
}

func (r *runner) stored() (int, error) {
	out, err := r.psql("ohs", "SELECT count(*) FROM ohs_data.quantity_samples")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

func (r *runner) queueEmpty() bool {
	s, b, _, err := r.do("GET", "/v1/users/"+r.user+"/coverage", nil, r.tenant())
	var v struct {
		PendingJobs int `json:"pending_jobs"`
	}
	return err == nil && s == 200 && json.Unmarshal(b, &v) == nil && v.PendingJobs == 0
}

func (r *runner) readDay(day, next string) (int, error) {
	rows, cursor := 0, ""
	for {
		q := url.Values{"type": {"heart_rate"}, "start": {day}, "end": {next}, "limit": {"1000"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		s, b, _, err := r.do("GET", "/v1/users/"+r.user+"/samples?"+q.Encode(), nil, r.tenant())
		if err != nil || s != 200 {
			return 0, fmt.Errorf("read: %d %v %.300s", s, err, b)
		}
		var page struct {
			Data       []json.RawMessage `json:"data"`
			NextCursor string            `json:"next_cursor"`
		}
		if err := json.Unmarshal(b, &page); err != nil {
			return 0, err
		}
		rows += len(page.Data)
		if cursor = page.NextCursor; cursor == "" {
			return rows, nil
		}
	}
}

// procTree is pid and all its descendants.
func procTree(root int) []int {
	children := map[int][]int{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		f := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
		if len(f) > 1 {
			ppid, _ := strconv.Atoi(f[1])
			children[ppid] = append(children[ppid], pid)
		}
	}
	out, todo := []int{}, []int{root}
	for len(todo) > 0 {
		pid := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		out = append(out, pid)
		todo = append(todo, children[pid]...)
	}
	return out
}

var (
	pageSize = int64(os.Getpagesize())
	clkTck   = 100.0 // USER_HZ, fixed at 100 on Linux
)

// usage is RSS bytes and CPU seconds summed over pid and its descendants.
func usage(pid int) (int64, float64) {
	var rss int64
	var cpu float64
	for _, p := range procTree(pid) {
		statm, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", p))
		if err != nil {
			continue
		}
		if f := strings.Fields(string(statm)); len(f) > 1 {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			rss += n * pageSize
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p))
		if err != nil {
			continue
		}
		f := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
		if len(f) > 12 {
			ut, _ := strconv.ParseFloat(f[11], 64)
			st, _ := strconv.ParseFloat(f[12], 64)
			cpu += (ut + st) / clkTck
		}
	}
	return rss, cpu
}

// pgCPUSeconds is the CPU time used so far by the Postgres container (cgroup v1 or v2).
func pgCPUSeconds(container string) float64 {
	out, err := sh(nil, "docker", "inspect", "-f", "{{.Id}}", container)
	if err != nil {
		return math.NaN()
	}
	id := strings.TrimSpace(out)
	if b, err := os.ReadFile("/sys/fs/cgroup/cpuacct/docker/" + id + "/cpuacct.usage"); err == nil {
		n, _ := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		return n / 1e9
	}
	for _, p := range []string{"/sys/fs/cgroup/system.slice/docker-" + id + ".scope/cpu.stat", "/sys/fs/cgroup/docker/" + id + "/cpu.stat"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "usage_usec "); ok {
				n, _ := strconv.ParseFloat(v, 64)
				return n / 1e6
			}
		}
	}
	return math.NaN()
}

func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(xs))
	k := float64(len(s)-1) * p / 100
	lo := int(k)
	hi := min(lo+1, len(s)-1)
	return s[lo] + (s[hi]-s[lo])*(k-float64(lo))
}

func summarize(ms []float64) latency {
	var sum float64
	for _, v := range ms {
		sum += v
	}
	return latency{N: len(ms), Mean: sum / float64(max(len(ms), 1)), P50: pct(ms, 50), P95: pct(ms, 95), P99: pct(ms, 99), Max: slices.Max(append([]float64{0}, ms...))}
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	data := fs.String("data", "", "directory written by gen (required)")
	out := fs.String("out", "", "result file (required)")
	work := fs.String("work", "/tmp/ohs-bench", "work directory: raw store and logs")
	conc := fs.Int("concurrency", 4, "concurrent upload clients")
	reads := fs.Int("reads", 50, "timed reads of one day")
	pgPort := fs.Int("pg-port", envInt("BENCH_PG_PORT", 15432), "benchmark Postgres port")
	bin := fs.String("ohs-bin", "bin/ohs", "ohs binary")
	timeout := fs.Duration("timeout", time.Hour, "time to wait for processing")
	_ = fs.Parse(args)
	if *data == "" || *out == "" {
		return errors.New("run: -data and -out are required")
	}
	container := os.Getenv("BENCH_PG_CONTAINER")
	if container == "" {
		container = "ohs-bench-pg"
	}
	var m manifest
	if b, err := os.ReadFile(filepath.Join(*data, "manifest.json")); err != nil {
		return err
	} else if err := json.Unmarshal(b, &m); err != nil {
		return err
	}

	keyBytes := make([]byte, 32) // a fresh key per run
	_, _ = rand.Read(keyBytes)
	raw := filepath.Join(*work, "ohs-raw")
	r := &runner{
		bin: *bin, base: "http://127.0.0.1:18080", pgPort: *pgPort,
		client: &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{MaxIdleConnsPerHost: *conc}},
		env: append(os.Environ(),
			fmt.Sprintf("OHS_DATABASE_URL=postgres://ohs:ohs@127.0.0.1:%d/ohs?sslmode=disable&pool_max_conns=20", *pgPort),
			"OHS_ENCRYPTION_KEY="+base64.StdEncoding.EncodeToString(keyBytes),
			"OHS_RAW_STORE=file://"+raw,
			"OHS_LISTEN=127.0.0.1:18080",
			"OHS_PUBLIC_URL=http://127.0.0.1:18080",
			"OHS_LOG_LEVEL=warn",
		),
	}

	// Empty database and raw store, migrations, then the server. Not timed.
	if _, err := r.psql("postgres", `DROP DATABASE IF EXISTS "ohs" WITH (FORCE)`); err != nil {
		return err
	}
	if _, err := r.psql("postgres", `CREATE DATABASE "ohs" OWNER "ohs"`); err != nil {
		return err
	}
	if err := os.RemoveAll(raw); err != nil {
		return err
	}
	if err := os.MkdirAll(raw, 0o755); err != nil {
		return err
	}
	if _, err := sh(r.env, r.bin, "migrate", "up"); err != nil {
		return err
	}
	logDir := filepath.Join(*work, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	logFile, err := os.Create(filepath.Join(logDir, "server.log"))
	if err != nil {
		return err
	}
	defer logFile.Close()
	srv := exec.Command(r.bin, "serve")
	srv.Env, srv.Stdout, srv.Stderr = r.env, logFile, logFile
	srv.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := srv.Start(); err != nil {
		return err
	}
	defer func() {
		_ = syscall.Kill(-srv.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = srv.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(40 * time.Second):
			_ = syscall.Kill(-srv.Process.Pid, syscall.SIGKILL)
		}
	}()
	if err := waitReady(r); err != nil {
		return err
	}
	if err := r.setupUser(); err != nil {
		return err
	}
	version, _ := sh(r.env, r.bin, "version")

	time.Sleep(5 * time.Second) // let start-up work settle before the idle sample
	pid := srv.Process.Pid
	idleRSS, _ := usage(pid)
	_, cpu0 := usage(pid)
	pg0 := pgCPUSeconds(container)

	ctx, stopSampling := context.WithCancel(context.Background())
	defer stopSampling()
	var peak int64
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			rss, _ := usage(pid)
			peak = max(peak, rss)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	var (
		mu        sync.Mutex
		lat       []float64
		statuses  = map[string]int{}
		retries   int
		sentBytes int64
		queue     = make(chan request)
		wg        sync.WaitGroup
	)
	send := func(req request) {
		body, err := os.ReadFile(filepath.Join(*data, req.Name+".ndjson.gz"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		headers := map[string]string{
			"Authorization": "Bearer " + r.token, "Content-Type": "application/gzip",
			"X-OHS-Type": req.Type, "X-OHS-Day": req.Day, "X-OHS-Batch": "bench", "X-OHS-Part": strconv.Itoa(req.Part),
		}
		for {
			t := time.Now()
			s, b, h, err := r.do("POST", "/v1/ingest/apple-health", body, headers)
			ms := float64(time.Since(t).Microseconds()) / 1000
			if err == nil && s == 503 { // back-pressure: honour Retry-After, count it
				mu.Lock()
				retries++
				mu.Unlock()
				wait, _ := strconv.ParseFloat(h.Get("Retry-After"), 64)
				time.Sleep(time.Duration(max(wait, 1) * float64(time.Second)))
				continue
			}
			mu.Lock()
			lat = append(lat, ms)
			statuses[strconv.Itoa(s)]++
			sentBytes += int64(len(body))
			mu.Unlock()
			if err != nil || s >= 400 {
				fmt.Fprintf(os.Stderr, "upload %s: %d %v %.200s\n", req.Name, s, err, b)
			}
			return
		}
	}
	t0 := time.Now()
	for range *conc {
		wg.Go(func() {
			for req := range queue {
				send(req)
			}
		})
	}
	for _, req := range m.Requests {
		queue <- req
	}
	close(queue)
	wg.Wait()
	tUploaded := time.Since(t0).Seconds()

	// Processed: every record stored, or the queue empty and the count still
	// for 10 s (some records were not stored).
	last, tLast := -1, time.Now()
	deadline := time.Now().Add(*timeout)
	for {
		n, err := r.stored()
		if err != nil {
			return err
		}
		now := time.Now()
		if n != last {
			last, tLast = n, now
		}
		if n >= m.Records || (now.Sub(tLast) > 10*time.Second && r.queueEmpty()) {
			break
		}
		if now.After(deadline) {
			fmt.Fprintln(os.Stderr, "timeout waiting for processing")
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	tDone := tLast.Sub(t0).Seconds()
	stopSampling()
	<-sampled
	_, cpu1 := usage(pid)
	pg1 := pgCPUSeconds(container)

	// Reads: one local day of heart rate, every page, after a short warm-up.
	start, _ := time.Parse("2006-01-02", m.Start)
	day := start.AddDate(0, 0, m.Days/2).Format("2006-01-02")
	next := start.AddDate(0, 0, m.Days/2+1).Format("2006-01-02")
	var rows int
	for range 5 {
		if rows, err = r.readDay(day, next); err != nil {
			return err
		}
	}
	var readMS []float64
	for range *reads {
		t := time.Now()
		if rows, err = r.readDay(day, next); err != nil {
			return err
		}
		readMS = append(readMS, float64(time.Since(t).Microseconds())/1000)
	}

	var res result
	res.System, res.Version, res.Concurrency = "open-health-sync", strings.TrimSpace(version), *conc
	d := &res.Dataset
	d.Seed, d.Days, d.Start, d.HRInterval, d.Records, d.Requests, d.RequestBytes = m.Seed, m.Days, m.Start, m.HRInterval, m.Records, len(m.Requests), sentBytes
	in := &res.Ingest
	in.Statuses, in.Retries503, in.Stored, in.Expected = statuses, retries, last, m.Records
	in.UploadSeconds, in.EndToEndSeconds = tUploaded, tDone
	in.UploadRecordsPerS, in.EndToEndRecordsPerS = float64(m.Records)/tUploaded, float64(last)/tDone
	in.UploadLatencyMS = summarize(lat)
	res.MemoryBytes.Idle = map[string]int64{"server": idleRSS, "total": idleRSS}
	res.MemoryBytes.Peak = map[string]int64{"server": peak, "total": peak}
	res.CPUSeconds = map[string]float64{"server": cpu1 - cpu0, "postgres": pg1 - pg0}
	res.Read.Day, res.Read.Rows, res.Read.LatencyMS = day, rows, summarize(readMS)
	if s, err := r.psql("postgres", "SELECT pg_database_size('ohs')"); err == nil {
		res.StorageBytes.Database, _ = strconv.ParseInt(s, 10, 64)
	}
	_ = filepath.WalkDir(raw, func(_ string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			if fi, err := e.Info(); err == nil {
				res.StorageBytes.RawFiles += fi.Size()
			}
		}
		return nil
	})

	b, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func waitReady(r *runner) error {
	end := time.Now().Add(2 * time.Minute)
	for time.Now().Before(end) {
		if s, _, _, err := r.do("GET", "/readyz", nil, nil); err == nil && s == 200 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("ohs serve did not become ready, see the server log")
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

// ---- report ----

const mb = 1024 * 1024

type metric struct {
	name string
	get  func(result) float64
}

var metrics = []metric{
	{"End to end, upload to stored (s)", func(r result) float64 { return r.Ingest.EndToEndSeconds }},
	{"End to end throughput (records/s)", func(r result) float64 { return r.Ingest.EndToEndRecordsPerS }},
	{"Upload phase alone (records/s)", func(r result) float64 { return r.Ingest.UploadRecordsPerS }},
	{"Upload latency p50 (ms)", func(r result) float64 { return r.Ingest.UploadLatencyMS.P50 }},
	{"Upload latency p95 (ms)", func(r result) float64 { return r.Ingest.UploadLatencyMS.P95 }},
	{"Upload latency p99 (ms)", func(r result) float64 { return r.Ingest.UploadLatencyMS.P99 }},
	{"Bytes sent (MB)", func(r result) float64 { return float64(r.Dataset.RequestBytes) / mb }},
	{"Idle memory, RSS (MB)", func(r result) float64 { return float64(r.MemoryBytes.Idle["total"]) / mb }},
	{"Peak memory, RSS (MB)", func(r result) float64 { return float64(r.MemoryBytes.Peak["total"]) / mb }},
	{"Server CPU per 10k records (s)", func(r result) float64 { return r.CPUSeconds["server"] * 10000 / float64(r.Dataset.Records) }},
	{"Postgres CPU per 10k records (s)", func(r result) float64 { return r.CPUSeconds["postgres"] * 10000 / float64(r.Dataset.Records) }},
	{"Read one day of heart rate p50 (ms)", func(r result) float64 { return r.Read.LatencyMS.P50 }},
	{"Read one day of heart rate p95 (ms)", func(r result) float64 { return r.Read.LatencyMS.P95 }},
	{"Database size (MB)", func(r result) float64 { return float64(r.StorageBytes.Database) / mb }},
	{"Raw payloads kept (MB)", func(r result) float64 { return float64(r.StorageBytes.RawFiles) / mb }},
}

func median(xs []float64) float64 {
	s := slices.Sorted(slices.Values(xs))
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}

func fmtNum(v float64) string {
	switch {
	case v >= 1000:
		s := strconv.FormatInt(int64(math.Round(v)), 10)
		for i := len(s) - 3; i > 0; i -= 3 {
			s = s[:i] + "," + s[i:]
		}
		return s
	case v >= 10:
		return strconv.FormatFloat(v, 'f', 1, 64)
	default:
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
}

func report(args []string, w io.Writer) error {
	if len(args) != 1 {
		return errors.New("report: give the results directory")
	}
	files, err := filepath.Glob(filepath.Join(args[0], "*-*.json"))
	if err != nil {
		return err
	}
	runs := map[string][]result{}
	for _, f := range files {
		base := strings.TrimSuffix(filepath.Base(f), ".json")
		profile := base[:strings.LastIndexByte(base, '-')]
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var r result
		if err := json.Unmarshal(b, &r); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		runs[profile] = append(runs[profile], r)
	}
	profiles := slices.Sorted(func(yield func(string) bool) {
		for p := range runs {
			if !yield(p) {
				return
			}
		}
	})
	// baseline first, then the others by name.
	slices.SortStableFunc(profiles, func(a, b string) int {
		return boolInt(a != "baseline") - boolInt(b != "baseline")
	})
	for _, p := range profiles {
		rs := runs[p]
		d := rs[0].Dataset
		fmt.Fprintf(w, "### %s: %s records in %d requests (%d days, heart rate every %d s), %d clients, median of %d runs\n\n",
			p, fmtNum(float64(d.Records)), d.Requests, d.Days, d.HRInterval, rs[0].Concurrency, len(rs))
		fmt.Fprintln(w, "| Metric | Open Health Sync |")
		fmt.Fprintln(w, "|---|---:|")
		for _, m := range metrics {
			vals := make([]float64, len(rs))
			for i, r := range rs {
				vals[i] = m.get(r)
			}
			fmt.Fprintf(w, "| %s | %s |\n", m.name, fmtNum(median(vals)))
		}
		stored := make([]string, len(rs))
		for i, r := range rs {
			stored[i] = fmt.Sprintf("%s (%s)", fmtNum(float64(r.Ingest.Stored)), fmtNum(float64(r.Ingest.Expected)))
		}
		fmt.Fprintf(w, "| Records stored, per run (of sent) | %s |\n\n", strings.Join(stored, "<br>"))
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
