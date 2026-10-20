// SPDX-License-Identifier: AGPL-3.0-only

// Sleep-report prints your last nights of sleep in the terminal, whatever app
// or ring measured them.
//
//	go run ./recipes/sleep-report             # the last 14 nights
//	go run ./recipes/sleep-report -days 60
//
// Needs Go 1.23 or later and nothing else. Recipe page: docs/recipes/sleep-report.md
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

// nights is the hours slept per night ({date: hours}) from the app that
// reports the most nights, and that app.
func nights(start, end string) (map[string]float64, string) {
	return most(nightsByApp(start, end))
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

func hours(h float64) string {
	m := int(math.Round(h * 60))
	return fmt.Sprintf("%dh%02d", m/60, m%60)
}

func main() {
	days := flag.Int("days", 14, "how many nights")
	flag.Parse()
	first := daysAgo(*days - 1).Format(iso)
	slept, app := nights(daysAgo(*days).Format(iso), "")
	var rows []string
	for d := range slept {
		if d >= first {
			rows = append(rows, d)
		}
	}
	slices.Sort(rows)
	if len(rows) == 0 {
		fail("no sleep recorded since %s: connect a ring, a watch or a phone first", first)
	}

	plural := ""
	if len(rows) > 1 {
		plural = "s"
	}
	fmt.Printf("Sleep, last %d night%s (%s)\n\n", len(rows), plural, app)
	var sum float64
	short, long, under := rows[0], rows[0], 0
	for _, d := range rows {
		v := slept[d]
		t, _ := time.Parse(iso, d)
		bar := strings.Repeat("█", int(math.Round(v/10*40))) // 40 characters is 10 hours
		fmt.Printf("%s  %5s  %s\n", t.Format("Mon 02 Jan"), hours(v), bar)
		sum += v
		if v < slept[short] {
			short = d
		}
		if v > slept[long] {
			long = d
		}
		if v < 7 {
			under++
		}
	}
	day := func(d string) string { t, _ := time.Parse(iso, d); return t.Format("Mon 02 Jan") }
	fmt.Printf("\nAverage %s, shortest %s (%s), longest %s (%s)\n",
		hours(sum/float64(len(rows))), hours(slept[short]), day(short), hours(slept[long]), day(long))
	fmt.Printf("Nights under 7 hours: %d of %d\n", under, len(rows))
	fmt.Println("Measured by more than one app? go run ./recipes/compare-sources -metric sleep")
}
