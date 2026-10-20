// SPDX-License-Identifier: AGPL-3.0-only

// Command dashboard writes a one page dashboard of your steps, sleep, resting heart
// rate and HRV.
//
//	go run ./recipes/dashboard                     # last 90 days, writes health-dashboard.html
//	go run ./recipes/dashboard -days 365 -out year.html
//
// The page is a single HTML file with no script and nothing loaded from the
// internet: open it in a browser, keep it, print it. It holds your health data,
// so share it with care. Needs Go 1.23 or later and nothing else.
// Recipe page: docs/recipes/dashboard.md
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// commas writes v rounded to a whole number, with thousands separators.
func commas(v float64) string {
	s := strconv.FormatInt(int64(math.Round(math.Abs(v))), 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if math.Round(v) < 0 {
		s = "-" + s
	}
	return s
}

// chart is an inline SVG: one bar or dot per day and the 7 day average as a line.
func chart(title, unit string, values map[string]float64, start time.Time, days int, bars bool, format func(float64) string) string {
	const w, h, pad = 720.0, 180.0, 28.0
	if len(values) == 0 {
		return fmt.Sprintf(`<section><h2>%s</h2><p class="none">No data in this window.</p></section>`, html.EscapeString(title))
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range values {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	lo *= 0.95
	if bars {
		lo = 0
	}
	if hi *= 1.05; hi == 0 {
		hi = 1
	}
	scale := hi - lo
	if scale == 0 {
		scale = 1
	}
	x := func(i int) float64 { return pad + (w-2*pad)*(float64(i)+0.5)/float64(days) }
	y := func(v float64) float64 { return h - pad - (h-2*pad)*(v-lo)/scale }
	var marks, avg []string
	for i := range days {
		d := start.AddDate(0, 0, i)
		if v, ok := values[d.Format(iso)]; ok {
			tip := fmt.Sprintf("<title>%s: %s %s</title>", d.Format("Mon 02 Jan"), format(v), unit)
			if bars {
				bw := math.Max((w-2*pad)/float64(days)-1, 1)
				marks = append(marks, fmt.Sprintf(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="mark">%s</rect>`,
					x(i)-bw/2, y(v), bw, h-pad-y(v), tip))
			} else {
				marks = append(marks, fmt.Sprintf(`<circle cx="%.1f" cy="%.1f" r="2.5" class="mark">%s</circle>`, x(i), y(v), tip))
			}
		}
		var sum float64
		n := 0
		for k := range 7 {
			if v, ok := values[d.AddDate(0, 0, -k).Format(iso)]; ok {
				sum, n = sum+v, n+1
			}
		}
		if n >= 4 {
			avg = append(avg, fmt.Sprintf("%.1f,%.1f", x(i), y(sum/float64(n))))
		}
	}
	var recent float64
	nRecent := 0
	after := start.AddDate(0, 0, days-8).Format(iso)
	for d, v := range values {
		if d > after {
			recent, nRecent = recent+v, nRecent+1
		}
	}
	now := "-"
	if nRecent > 0 {
		now = format(recent / float64(nRecent))
	}
	labels := fmt.Sprintf(`<text x="%g" y="%g" class="axis">%s</text>`, pad, h-8, start.Format("02 Jan")) +
		fmt.Sprintf(`<text x="%g" y="%g" class="axis" text-anchor="end">%s</text>`, w-pad, h-8, start.AddDate(0, 0, days-1).Format("02 Jan")) +
		fmt.Sprintf(`<text x="%g" y="16" class="axis">%s</text>`, pad, format(hi))
	if !bars {
		labels += fmt.Sprintf(`<text x="%g" y="%g" class="axis">%s</text>`, pad, h-pad-4, format(lo))
	}
	line := ""
	if len(avg) > 0 {
		line = fmt.Sprintf(`<polyline points="%s" class="avg"/>`, strings.Join(avg, " "))
	}
	t := html.EscapeString(title)
	return fmt.Sprintf(`<section><h2>%s <span>%s %s, last 7 days</span></h2>`, t, now, unit) +
		fmt.Sprintf(`<svg viewBox="0 0 %g %g" role="img" aria-label="%s">`, w, h, t) +
		labels + strings.Join(marks, "") + line + "</svg></section>"
}

const page = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>My health</title>
<style>
:root { --bg: #fbfaf8; --fg: #1d1d1f; --muted: #6e6e73; --mark: #8fb3e8; --avg: #1f5fbf; --card: #fff; }
@media (prefers-color-scheme: dark) {
  :root { --bg: #111214; --fg: #f2f2f2; --muted: #9a9aa0; --mark: #35527d; --avg: #8ab4ff; --card: #1a1b1e; }
}
body { margin: 0; background: var(--bg); color: var(--fg); font: 15px/1.5 system-ui, sans-serif; }
main { max-width: 760px; margin: 0 auto; padding: 24px 16px 48px; }
h1 { font-size: 24px; margin: 0 0 4px; }
p.sub, h2 span, .none { color: var(--muted); }
section { background: var(--card); border-radius: 12px; padding: 12px 16px; margin: 16px 0; }
h2 { font-size: 16px; margin: 0 0 4px; display: flex; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
h2 span { font-weight: 400; font-size: 14px; }
svg { width: 100%; height: auto; display: block; }
.mark { fill: var(--mark); } .avg { fill: none; stroke: var(--avg); stroke-width: 2.5; }
.axis { fill: var(--muted); font-size: 12px; }
</style></head>
<body><main>
<h1>My health, last {days} days</h1>
<p class="sub">{sources}. Line: 7 day average. Made {made} with Open Health Sync.</p>
{charts}
</main></body></html>
`

func main() {
	days := flag.Int("days", 90, "how many days")
	out := flag.String("out", "health-dashboard.html", "the page to write")
	flag.Parse()
	start := daysAgo(*days - 1)
	from := start.Format(iso)

	steps, stepsApp := daily("step_count", from, "")
	slept, sleepApp := nights(start.AddDate(0, 0, -1).Format(iso), "")
	rhr, rhrApp := daily("resting_hr", from, "")
	if len(rhr) == 0 {
		rhr, rhrApp = sampleMeans("resting_heart_rate", from, "")
	}
	hrv, hrvApp := daily("hrv_avg_ms", from, "")
	if len(hrv) == 0 {
		hrv, hrvApp = sampleMeans("hrv_sdnn", from, "")
	}
	seen := map[string]bool{}
	for _, app := range []string{stepsApp, sleepApp, rhrApp, hrvApp} {
		if app != "" {
			seen[app] = true
		}
	}
	sources := "No source yet"
	if len(seen) > 0 {
		sources = "From " + html.EscapeString(strings.Join(slices.Sorted(maps.Keys(seen)), ", "))
	}

	f0 := func(v float64) string { return fmt.Sprintf("%.0f", v) }
	f1 := func(v float64) string { return fmt.Sprintf("%.1f", v) }
	charts := strings.Join([]string{
		chart("Steps", "steps", steps, start, *days, true, commas),
		chart("Sleep", "h", slept, start, *days, true, f1),
		chart("Resting heart rate", "bpm", rhr, start, *days, false, f0),
		chart("Heart rate variability", "ms", hrv, start, *days, false, f0),
	}, "\n")
	doc := strings.NewReplacer("{days}", strconv.Itoa(*days), "{sources}", sources,
		"{made}", time.Now().Format("02 January 2006"), "{charts}", charts).Replace(page)
	if err := os.WriteFile(*out, []byte(doc), 0o644); err != nil {
		fail("%v", err)
	}
	fmt.Printf("wrote %s: open it in a browser\n", *out)
}
