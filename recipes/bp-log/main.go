// SPDX-License-Identifier: AGPL-3.0-only

// Command bp-log prints your blood pressure readings as a log to bring to
// your doctor: each reading with its date and time, then the averages, in
// the morning and in the evening.
//
//	go run ./recipes/bp-log                  # the last 30 days
//	go run ./recipes/bp-log -days 90
//	go run ./recipes/bp-log -csv > bp.csv    # the readings as CSV, for a spreadsheet
//
// It reads the readings of any cuff that reaches the server: OMRON, Withings,
// or one that writes to Apple Health or Health Connect. It only lists and
// averages them; what they mean is for your doctor. Needs Go 1.23 or later and
// nothing else. Recipe page: docs/recipes/bp-log.md
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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

type sample struct {
	Provider  string  `json:"provider"`
	StartDate string  `json:"start_date"`
	Value     float64 `json:"value"`
}

// reading is one blood pressure measurement: the systolic and diastolic
// samples a cuff writes with the same time.
type reading struct {
	at                  time.Time
	systolic, diastolic float64
	app                 string
}

func main() {
	days := flag.Int("days", 30, "how many days")
	asCSV := flag.Bool("csv", false, "print the readings as CSV")
	flag.Parse()
	start := daysAgo(max(*days, 1) - 1).Format(iso)

	readings := pair(series("blood_pressure_systolic", start), series("blood_pressure_diastolic", start))
	if len(readings) == 0 {
		fail("no blood pressure readings since %s: connect OMRON or Withings, or a cuff that writes to Apple Health or Health Connect", start)
	}

	if *asCSV {
		w := csv.NewWriter(os.Stdout)
		_ = w.Write([]string{"date", "time", "systolic_mmhg", "diastolic_mmhg", "source"})
		for _, r := range readings {
			_ = w.Write([]string{r.at.Format(iso), r.at.Format("15:04"),
				fmt.Sprintf("%.0f", r.systolic), fmt.Sprintf("%.0f", r.diastolic), r.app})
		}
		w.Flush()
		return
	}

	fmt.Printf("%-18s%14s  %s\n", "Date", "mmHg", "Source")
	for _, r := range readings {
		fmt.Printf("%-18s%14s  %s\n", r.at.Format("Mon 02 Jan 15:04"),
			fmt.Sprintf("%.0f / %.0f", r.systolic, r.diastolic), r.app)
	}
	fmt.Println()
	average("Average", readings)
	average("Morning (before noon)", filter(readings, func(r reading) bool { return r.at.Hour() < 12 }))
	average("Evening (from 6 pm)", filter(readings, func(r reading) bool { return r.at.Hour() >= 18 }))
}

// series is every sample of one type since start, merged view: when two apps
// recorded the same reading (a Withings cuff that also writes to Apple
// Health), it comes once.
func series(kind, start string) []sample {
	q := span(start, "")
	q.Set("type", kind)
	q.Set("view", "merged")
	return records[sample]("samples", q)
}

// pair matches each systolic sample with the diastolic sample of the same app
// and time, oldest first, in local time.
func pair(sys, dia []sample) []reading {
	diastolic := map[string]float64{}
	for _, d := range dia {
		diastolic[d.Provider+" "+d.StartDate] = d.Value
	}
	var out []reading
	for _, s := range sys {
		d, ok := diastolic[s.Provider+" "+s.StartDate]
		at, err := time.Parse(time.RFC3339, s.StartDate)
		if !ok || err != nil {
			continue
		}
		out = append(out, reading{at: at.In(time.Local), systolic: s.Value, diastolic: d, app: s.Provider})
	}
	slices.SortFunc(out, func(a, b reading) int { return a.at.Compare(b.at) })
	return out
}

func filter(rs []reading, keep func(reading) bool) []reading {
	var out []reading
	for _, r := range rs {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

func average(label string, rs []reading) {
	if len(rs) == 0 {
		return
	}
	var sys, dia float64
	for _, r := range rs {
		sys, dia = sys+r.systolic, dia+r.diastolic
	}
	n := float64(len(rs))
	fmt.Printf("%-22s %.0f / %.0f mmHg over %d readings\n", label+":", sys/n, dia/n, len(rs))
}
