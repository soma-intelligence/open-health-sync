// SPDX-License-Identifier: AGPL-3.0-only

// Weekly-digest writes last week of your health in a short Markdown note,
// against the week before.
//
//	go run ./recipes/weekly-digest                  # prints the note
//	go run ./recipes/weekly-digest -out week.md     # writes it to a file
//
// Paste it in your notes, send it to yourself every Monday with cron, or post
// it to a chat with a webhook. Needs Go 1.23 or later and nothing else.
// Recipe page: docs/recipes/weekly-digest.md
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

type workout struct {
	StartDate    string   `json:"start_date"`
	DurationS    *float64 `json:"duration_s"`
	ActivityType string   `json:"activity_type"`
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// line is one bullet of the note, or "" when there is no value this week.
func line(label string, now, before float64, format func(float64) string, unit string, better bool) string {
	if math.IsNaN(now) {
		return ""
	}
	text := fmt.Sprintf("- **%s**: %s%s", label, format(now), unit)
	if !math.IsNaN(before) && before != 0 {
		d := (now - before) / before
		arrow := "→"
		if math.Abs(d) >= 0.03 {
			arrow = map[bool]string{true: "↑", false: "↓"}[d > 0]
		}
		text += fmt.Sprintf(" %s %+.0f%% vs the week before", arrow, d*100)
		if math.Abs(d) >= 0.03 && (d > 0) == better {
			text += " 👍"
		}
	}
	return text
}

func main() {
	out := flag.String("out", "", "write the note to this file instead of printing it")
	flag.Parse()
	weekday := (int(time.Now().Weekday()) + 6) % 7 // Monday is 0
	this := daysAgo(weekday + 7)                   // Monday of last full week
	prev, end := this.AddDate(0, 0, -7), this.AddDate(0, 0, 7)
	thisDay, prevDay, endDay := this.Format(iso), prev.Format(iso), end.Format(iso)

	split := func(values map[string]float64) (now, before []float64) {
		for d, v := range values {
			switch {
			case thisDay <= d && d < endDay:
				now = append(now, v)
			case prevDay <= d && d < thisDay:
				before = append(before, v)
			}
		}
		return now, before
	}

	steps, _ := daily("step_count", prevDay, endDay)
	rhr, _ := daily("resting_hr", prevDay, endDay)
	if len(rhr) == 0 {
		rhr, _ = sampleMeans("resting_heart_rate", prevDay, endDay)
	}
	hrv, _ := daily("hrv_avg_ms", prevDay, endDay)
	slept, _ := nights(prev.AddDate(0, 0, -1).Format(iso), endDay)
	var wNow, wBefore []workout
	for _, w := range records[workout]("workouts", span(prevDay, endDay)) {
		if d := w.StartDate[:min(10, len(w.StartDate))]; thisDay <= d && d < endDay {
			wNow = append(wNow, w)
		} else {
			wBefore = append(wBefore, w)
		}
	}
	minutes := func(ws []workout) float64 {
		var s float64
		for _, w := range ws {
			if w.DurationS != nil {
				s += *w.DurationS
			}
		}
		return s / 60
	}

	sNow, sBefore := split(steps)
	slNow, slBefore := split(slept)
	rNow, rBefore := split(rhr)
	hNow, hBefore := split(hrv)
	training := minutes(wNow)
	if training == 0 {
		training = math.NaN()
	}
	f1 := func(v float64) string { return fmt.Sprintf("%.1f", v) }
	f0 := func(v float64) string { return fmt.Sprintf("%.0f", v) }

	note := []string{"## Health, week of " + this.Format("02 January 2006"), ""}
	for _, item := range []string{
		line("Steps", mean(sNow), mean(sBefore), commas, " a day", true),
		line("Sleep", mean(slNow), mean(slBefore), f1, " h a night", true),
		line("Resting heart rate", mean(rNow), mean(rBefore), f1, " bpm", false),
		line("HRV", mean(hNow), mean(hBefore), f0, " ms", true),
		line("Training", training, minutes(wBefore), f0, fmt.Sprintf(" min in %d workouts", len(wNow)), true),
	} {
		if item != "" {
			note = append(note, item)
		}
	}
	// Activities by count, most frequent first, ties in the order they came.
	var kinds []string
	count := map[string]int{}
	for _, w := range wNow {
		if count[w.ActivityType] == 0 {
			kinds = append(kinds, w.ActivityType)
		}
		count[w.ActivityType]++
	}
	slices.SortStableFunc(kinds, func(a, b string) int { return count[b] - count[a] })
	if len(kinds) > 0 {
		parts := make([]string, len(kinds))
		for i, k := range kinds {
			parts[i] = fmt.Sprintf("%d × %s", count[k], strings.ReplaceAll(k, "_", " "))
		}
		note = append(note, "  "+strings.Join(parts, ", "))
	}
	if len(note) == 2 {
		note = append(note, "No data for that week yet.")
	}
	text := strings.Join(note, "\n") + "\n"
	if *out == "" {
		fmt.Print(text)
		return
	}
	if err := os.WriteFile(*out, []byte(text), 0o644); err != nil {
		fail("%v", err)
	}
	fmt.Println("wrote " + *out)
}
