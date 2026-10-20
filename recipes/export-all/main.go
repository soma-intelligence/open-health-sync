// SPDX-License-Identifier: AGPL-3.0-only

// Command export-all downloads a copy of all your health data, every app in one place,
// as CSV or Parquet.
//
//	go run ./recipes/export-all                    # CSV, into health-export-<date>/
//	go run ./recipes/export-all -format parquet    # for DuckDB, Polars, pandas, Spark
//
// Open the CSV files in any spreadsheet. Needs Go 1.23 or later and nothing else.
// Recipe page: docs/recipes/export-all.md
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

var datasets = []string{"daily", "sleep", "workouts", "samples", "category-samples", "nutrition"}

func main() {
	format := flag.String("format", "csv", "csv or parquet")
	since := flag.String("since", "", "keep records from this day on (YYYY-MM-DD); default everything")
	dir := flag.String("dir", "health-export-"+time.Now().Format("2006-01-02"), "where to write the files")
	flag.Parse()
	if *format != "csv" && *format != "parquet" {
		fail("-format: csv or parquet")
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fail("%v", err)
	}
	for _, ds := range datasets {
		path := filepath.Join(*dir, ds+"."+*format)
		q := url.Values{"format": {*format}, "view": {"all"}}
		if *since != "" {
			q.Set("start", *since)
		}
		resp := call("GET", "/v1/users/"+user+"/exports/"+ds, nil, q)
		f, err := os.Create(path)
		if err != nil {
			fail("%v", err)
		}
		n, err := io.Copy(f, resp.Body)
		resp.Body.Close()
		if err == nil {
			err = f.Close()
		}
		if err != nil {
			fail("%s: %v", path, err)
		}
		fmt.Printf("%-48s%12s bytes\n", path, commas(float64(n)))
	}
	if *format == "parquet" {
		fmt.Printf("\nduckdb -c \"SELECT * FROM '%s/daily.parquet' LIMIT 10\"\n", *dir)
	}
}
