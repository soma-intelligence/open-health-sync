// SPDX-License-Identifier: AGPL-3.0-only

// Command training-load prints your training week by week: sessions, time,
// distance and active energy from your workouts, and how the last 7 days
// compare with the weeks before.
//
//	go run ./recipes/training-load              # the last 8 weeks
//	go run ./recipes/training-load -weeks 26
//
// Put it next to heart-trend: a jump in training time with a rising resting
// heart rate often means you are not recovering. This is a trend, not a
// diagnosis. Needs Go 1.23 or later and nothing else.
// Recipe page: docs/recipes/training-load.md
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

type workout struct {
	StartDate    string   `json:"start_date"`
	DurationS    *float64 `json:"duration_s"`
	DistanceM    *float64 `json:"distance_m"`
	ActiveEnergy *float64 `json:"active_energy_kcal"`
	ActivityType string   `json:"activity_type"`
}

// week is the training of one 7 day window.
type week struct {
	sessions          int
	minutes, km, kcal float64
}

func main() {
	weeks := flag.Int("weeks", 8, "how many weeks")
	flag.Parse()
	*weeks = max(*weeks, 2)
	// Windows of 7 days that end today, so the last one is always "this week".
	first := daysAgo(7**weeks - 1)

	// The merged view keeps one copy of a workout two apps both recorded (a
	// run on the watch that also reached Strava), so nothing counts twice.
	q := span(first.Format(iso), "")
	q.Set("view", "merged")
	ws := records[workout]("workouts", q)
	if len(ws) == 0 {
		fail("no workouts since %s: connect a watch or log a workout first", first.Format(iso))
	}

	byWeek := make([]week, *weeks)
	types := map[string]int{}
	for _, w := range ws {
		t, err := time.Parse(time.RFC3339, w.StartDate)
		if err != nil {
			continue
		}
		i := int(t.In(time.Local).Sub(first).Hours() / 24 / 7)
		if i < 0 || i >= *weeks {
			continue
		}
		b := &byWeek[i]
		b.sessions++
		b.minutes += value(w.DurationS) / 60
		b.km += value(w.DistanceM) / 1000
		b.kcal += value(w.ActiveEnergy)
		types[w.ActivityType]++
	}

	fmt.Printf("%-12s%10s%10s%10s%10s\n", "7 days to", "Sessions", "Time", "Distance", "Energy")
	for i, b := range byWeek {
		last := first.AddDate(0, 0, 7*i+6)
		fmt.Printf("%s  %10d%10s%10s%10s\n", last.Format("Mon 02 Jan"), b.sessions,
			fmt.Sprintf("%dh%02d", int(b.minutes)/60, int(b.minutes)%60),
			fmt.Sprintf("%.1f km", b.km), fmt.Sprintf("%.0f kcal", b.kcal))
	}

	// The last 7 days against the mean of the 4 weeks before: a sudden jump in
	// training time is the classic setup for an injury or a bad week.
	before := byWeek[max(len(byWeek)-5, 0) : len(byWeek)-1]
	var base float64
	for _, b := range before {
		base += b.minutes
	}
	base /= float64(len(before))
	if now := byWeek[len(byWeek)-1].minutes; base > 0 {
		fmt.Printf("\nLast 7 days against the %d weeks before: time %+.0f%% (%.0f min against %.0f)\n",
			len(before), (now-base)/base*100, now, base)
	}
	fmt.Printf("Workouts: %d, most often %s\n", len(ws), top(types))
}

// value is *v, 0 when the workout does not say.
func value(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// top is the most frequent activity type.
func top(counts map[string]int) string {
	best := "-"
	for t, n := range counts {
		if t != "" && (best == "-" || n > counts[best] || (n == counts[best] && t < best)) {
			best = t
		}
	}
	return best
}
