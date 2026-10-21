// SPDX-License-Identifier: AGPL-3.0-only

// Command compare-sources prints the same figure from every app that measures it,
// day by day, and how far apart they are.
//
//	go run ./recipes/compare-sources                       # steps, last 14 days
//	go run ./recipes/compare-sources -metric sleep         # also: resting_hr, hrv, steps
//	go run ./recipes/compare-sources -metric resting_hr -days 30
//
// Wear a ring and a watch, or log on a phone and a band, and they never agree.
// Needs Go 1.23 or later and nothing else. Recipe page: docs/recipes/compare-sources.md
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Connection: OHS_URL, OHS_KEY and OHS_USER from the environment, or from the
// file `ohs admin personal setup` writes (~/.ohs.env, or the path in OHS_ENV_FILE).
var baseURL, key, user = settings()

func settings() (string, string, string) {
	path := os.Getenv("OHS_ENV_FILE")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".ohs.env")
	}
	if b, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			name = strings.TrimSpace(name)
			if ok && !strings.HasPrefix(name, "#") && os.Getenv(name) == "" {
				_ = os.Setenv(name, strings.TrimSpace(value))
			}
		}
	}
	var missing []string
	for _, n := range []string{"OHS_URL", "OHS_KEY", "OHS_USER"} {
		if os.Getenv(n) == "" {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		fail("missing %s: run `ohs admin personal setup > ~/.ohs.env` first", strings.Join(missing, ", "))
	}
	return strings.TrimRight(os.Getenv("OHS_URL"), "/"), os.Getenv("OHS_KEY"), os.Getenv("OHS_USER")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

var client = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 2 * time.Minute}}

// call sends one request and returns the response; an error ends the program.
func call(method, path string, body any, params url.Values) *http.Response {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	u := baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequest(method, u, r)
	if err != nil {
		fail("%s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fail("cannot reach %s: %v", baseURL, err)
	}
	if resp.StatusCode >= 400 {
		var problem struct {
			Detail string `json:"detail"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&problem)
		resp.Body.Close()
		fail("%s", strings.TrimSpace(fmt.Sprintf("%s %s: %d %s", method, path, resp.StatusCode, problem.Detail)))
	}
	return resp
}

// fetch sends one request and decodes its JSON answer into out.
func fetch(method, path string, body any, params url.Values, out any) {
	resp := call(method, path, body, params)
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		fail("%s %s: %v", method, path, err)
	}
}

// records is every record of one of the user's lists, page after page. It
// asks for every source's rows: the recipe picks or compares the apps itself.
func records[T any](resource string, params url.Values) []T {
	q := url.Values{"limit": {"1000"}, "view": {"all"}}
	for k, v := range params {
		q[k] = v
	}
	var out []T
	for {
		var page struct {
			Data       []T    `json:"data"`
			NextCursor string `json:"next_cursor"`
		}
		fetch("GET", "/v1/users/"+user+"/"+resource, nil, q, &page)
		out = append(out, page.Data...)
		if page.NextCursor == "" {
			return out
		}
		q.Set("cursor", page.NextCursor)
	}
}

// span is the start and end filters of a list; an empty end is left out.
func span(start, end string) url.Values {
	q := url.Values{"start": {start}}
	if end != "" {
		q.Set("end", end)
	}
	return q
}

// Dates are local days written YYYY-MM-DD, so they sort as strings.
const iso = "2006-01-02"

func daysAgo(n int) time.Time {
	y, m, d := time.Now().Date()
	return time.Date(y, m, d-n, 0, 0, 0, 0, time.Local)
}

type sleep struct {
	Provider          string         `json:"provider"`
	BedtimeStart      string         `json:"bedtime_start"`
	BedtimeEnd        *string        `json:"bedtime_end"`
	TotalSleepSeconds *float64       `json:"total_sleep_seconds"`
	AwakeSeconds      *float64       `json:"awake_seconds"`
	Metadata          map[string]any `json:"metadata"`
}

// nightsByApp is the hours slept per night, for each app: {app: {date: hours}}.
//
// A night is named by the morning it ends on: sleep that ends between 14:00
// the day before and 14:00 counts for that date, naps included, so a night
// split in two sessions is added up. A session without a sleep total counts
// its time in bed, less the time awake when the app gives it.
func nightsByApp(start, end string) map[string]map[string]float64 {
	byApp := map[string]map[string]float64{}
	for _, s := range records[sleep]("sleep", span(start, end)) {
		if s.BedtimeEnd == nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, *s.BedtimeEnd)
		if err != nil {
			continue
		}
		var slept float64
		if s.TotalSleepSeconds != nil {
			slept = *s.TotalSleepSeconds
		} else if t0, err := time.Parse(time.RFC3339, s.BedtimeStart); err == nil {
			slept = t.Sub(t0).Seconds()
			if s.AwakeSeconds != nil {
				slept -= *s.AwakeSeconds
			}
		}
		if slept <= 0 {
			continue
		}
		if name, _ := s.Metadata["timezone"].(string); name != "" {
			if loc, err := time.LoadLocation(name); err == nil {
				t = t.In(loc)
			}
		}
		if byApp[s.Provider] == nil {
			byApp[s.Provider] = map[string]float64{}
		}
		byApp[s.Provider][t.Add(10*time.Hour).Format(iso)] += slept / 3600
	}
	return byApp
}

type sample struct {
	Provider  string  `json:"provider"`
	StartDate string  `json:"start_date"`
	Value     float64 `json:"value"`
}

type metric struct {
	field, unit, format string // daily summary field (none for sleep), unit, how to print a value
}

var metrics = map[string]metric{
	"steps":      {"step_count", "steps", "%.0f"},
	"resting_hr": {"resting_hr", "bpm", "%.1f"},
	"hrv":        {"hrv_avg_ms", "ms", "%.0f"},
	"sleep":      {"", "hours", "%.2f"},
}

// values is {app: {date: value}} for one metric.
func values(name, start string) map[string]map[string]float64 {
	m := metrics[name]
	if m.field == "" {
		return nightsByApp(start, "")
	}
	out := map[string]map[string]float64{}
	put := func(app, day string, v float64) {
		if out[app] == nil {
			out[app] = map[string]float64{}
		}
		out[app][day] = v
	}
	for _, r := range records[map[string]any]("daily", url.Values{"start": {start}}) {
		if v, ok := r[m.field].(float64); ok {
			app, _ := r["provider"].(string)
			day, _ := r["date"].(string)
			put(app, day, v)
		}
	}
	if name == "resting_hr" { // Apple Health sends it as samples, not in a daily summary
		type key struct{ app, day string }
		sums, counts := map[key]float64{}, map[key]float64{}
		for _, s := range records[sample]("samples", url.Values{"type": {"resting_heart_rate"}, "start": {start}}) {
			if _, done := out[s.Provider]; !done {
				k := key{s.Provider, s.StartDate[:min(10, len(s.StartDate))]}
				sums[k] += s.Value
				counts[k]++
			}
		}
		for k, sum := range sums {
			put(k.app, k.day, sum/counts[k])
		}
	}
	return out
}

func main() {
	name := flag.String("metric", "steps", "steps, resting_hr, hrv or sleep")
	days := flag.Int("days", 14, "how many days")
	flag.Parse()
	m, ok := metrics[*name]
	if !ok {
		fail("-metric: pick one of %s", strings.Join(slices.Sorted(maps.Keys(metrics)), ", "))
	}
	start := daysAgo(*days).Format(iso)
	data := values(*name, start)
	apps := slices.Sorted(maps.Keys(data))
	if len(apps) == 0 {
		fail("no %s since %s", *name, start)
	}
	seen := map[string]bool{}
	for _, v := range data {
		for d := range v {
			if d >= start {
				seen[d] = true
			}
		}
	}
	dayList := slices.Sorted(maps.Keys(seen))
	f := func(v float64) string { return fmt.Sprintf(m.format, v) }

	plural := ""
	if len(apps) > 1 {
		plural = "s"
	}
	fmt.Printf("%s (%s), %d source%s\n\n", *name, m.unit, len(apps), plural)
	fmt.Printf("%-12s", "Day")
	for _, a := range apps {
		fmt.Printf("%16s", a)
	}
	fmt.Println()
	for _, d := range dayList {
		fmt.Printf("%-12s", d)
		for _, a := range apps {
			cell := "-"
			if v, ok := data[a][d]; ok {
				cell = f(v)
			}
			fmt.Printf("%16s", cell)
		}
		fmt.Println()
	}

	if len(apps) < 2 {
		fmt.Println("\nOnly one source measures this: connect a second one to compare.")
		return
	}
	fmt.Println()
	for i, x := range apps {
		for _, y := range apps[i+1:] {
			var both []string
			for _, d := range dayList {
				_, okx := data[x][d]
				_, oky := data[y][d]
				if okx && oky {
					both = append(both, d)
				}
			}
			if len(both) == 0 {
				fmt.Printf("%s and %s: no day measured by both\n", x, y)
				continue
			}
			var gap, mean, bias float64
			for _, d := range both {
				gap += math.Abs(data[x][d] - data[y][d])
				mean += (data[x][d] + data[y][d]) / 2
				bias += data[x][d] - data[y][d]
			}
			n := float64(len(both))
			gap, mean, bias = gap/n, mean/n, bias/n
			share := ""
			if mean != 0 {
				share = fmt.Sprintf(" (%.0f%%)", gap/mean*100)
			}
			side := "higher"
			if bias < 0 {
				side = "lower"
			}
			fmt.Printf("%s vs %s: %d days, typical gap %s %s%s, %s reads %s %s %s on average\n",
				x, y, len(both), f(gap), m.unit, share, x, f(math.Abs(bias)), m.unit, side)
		}
	}
}
