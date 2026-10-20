// SPDX-License-Identifier: AGPL-3.0-only

// Heart-trend prints your resting heart rate and HRV week by week, and how
// this week compares.
//
//	go run ./recipes/heart-trend              # the last 8 weeks
//	go run ./recipes/heart-trend -weeks 26
//
// A rising resting heart rate with a falling HRV often goes with poor sleep,
// stress, a hard training block or a cold coming. This is a trend, not a
// diagnosis. Needs Go 1.23 or later and nothing else.
// Recipe page: docs/recipes/heart-trend.md
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

// daily is one value of a daily figure per date ({date: value}), and the app
// it came from.
//
// Several apps can report the same figure for the same day. This keeps the
// app that reports it on the most days, so a series never mixes two devices;
// the compare-sources recipe shows them side by side.
func daily(field, start, end string) (map[string]float64, string) {
	rows := map[string]map[string]float64{} // app: {date: value}
	for _, r := range records[map[string]any]("daily", span(start, end)) {
		v, ok := r[field].(float64)
		if !ok {
			continue
		}
		app, _ := r["provider"].(string)
		day, _ := r["date"].(string)
		if rows[app] == nil {
			rows[app] = map[string]float64{}
		}
		rows[app][day] = v
	}
	return most(rows)
}

// sampleMeans is the daily mean of a sample type ({date: value}) and its app,
// for apps with no daily summary.
func sampleMeans(kind, start, end string) (map[string]float64, string) {
	sums, counts := map[string]map[string]float64{}, map[string]map[string]float64{}
	q := span(start, end)
	q.Set("type", kind)
	for _, s := range records[sample]("samples", q) {
		if sums[s.Provider] == nil {
			sums[s.Provider], counts[s.Provider] = map[string]float64{}, map[string]float64{}
		}
		day := s.StartDate[:min(10, len(s.StartDate))]
		sums[s.Provider][day] += s.Value
		counts[s.Provider][day]++
	}
	for app, days := range sums {
		for d := range days {
			days[d] /= counts[app][d]
		}
	}
	return most(sums)
}

type sample struct {
	Provider  string  `json:"provider"`
	StartDate string  `json:"start_date"`
	Value     float64 `json:"value"`
}

// most is the series of the app with the most days, and that app.
func most(byApp map[string]map[string]float64) (map[string]float64, string) {
	app := ""
	for _, a := range slices.Sorted(maps.Keys(byApp)) {
		if app == "" || len(byApp[a]) > len(byApp[app]) {
			app = a
		}
	}
	return byApp[app], app
}

// weekly is the mean per 7 day window from first, oldest first; NaN for a
// window with no value.
func weekly(values map[string]float64, first time.Time, weeks int) []float64 {
	out := make([]float64, weeks)
	for w := range out {
		from, to := first.AddDate(0, 0, 7*w).Format(iso), first.AddDate(0, 0, 7*w+7).Format(iso)
		var sum, n float64
		for d, v := range values {
			if from <= d && d < to {
				sum, n = sum+v, n+1
			}
		}
		out[w] = sum / n // NaN when n is 0
	}
	return out
}

func main() {
	weeks := flag.Int("weeks", 8, "how many weeks")
	flag.Parse()
	*weeks = max(*weeks, 2)
	// Windows of 7 days that end today, so the last one is always "this week".
	first := daysAgo(7**weeks - 1)
	start := first.Format(iso)

	rhr, rhrApp := daily("resting_hr", start, "")
	if len(rhr) == 0 { // Apple Health sends resting heart rate as samples, not as a daily summary
		rhr, rhrApp = sampleMeans("resting_heart_rate", start, "")
	}
	hrv, hrvApp := daily("hrv_avg_ms", start, "")
	if len(hrv) == 0 {
		if hrv, hrvApp = sampleMeans("hrv_sdnn", start, ""); hrvApp != "" {
			hrvApp += " (SDNN)"
		}
	}
	if len(rhr) == 0 && len(hrv) == 0 {
		fail("no resting heart rate or HRV since %s: connect a watch, a ring or a band first", start)
	}

	r, h := weekly(rhr, first, *weeks), weekly(hrv, first, *weeks)
	cell := func(v float64, unit string) string {
		if math.IsNaN(v) {
			return "-"
		}
		return fmt.Sprintf("%.1f %s", v, unit)
	}
	fmt.Printf("%-12s%14s%12s\n", "7 days to", "Resting HR", "HRV")
	for w := range *weeks {
		last := first.AddDate(0, 0, 7*w+6)
		fmt.Printf("%s  %14s%12s\n", last.Format("Mon 02 Jan"), cell(r[w], "bpm"), cell(h[w], "ms"))
	}
	fmt.Printf("\nSources: resting HR from %s, HRV from %s\n", or(rhrApp, "-"), or(hrvApp, "-"))

	change := func(series []float64, unit string, pct bool) string {
		var sum, n float64
		for _, v := range series[max(len(series)-5, 0) : len(series)-1] {
			if !math.IsNaN(v) {
				sum, n = sum+v, n+1
			}
		}
		now := series[len(series)-1]
		if math.IsNaN(now) || n == 0 {
			return ""
		}
		base := sum / n
		if pct {
			return fmt.Sprintf("%+.0f%%", (now-base)/base*100)
		}
		return fmt.Sprintf("%+.1f %s", now-base, unit)
	}
	var parts []string
	if c := change(r, "bpm", false); c != "" {
		parts = append(parts, "resting HR "+c)
	}
	if c := change(h, "ms", true); c != "" {
		parts = append(parts, "HRV "+c)
	}
	if len(parts) > 0 {
		fmt.Printf("Last 7 days against the %d weeks before: %s\n", min(4, *weeks-1), strings.Join(parts, ", "))
	}
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
