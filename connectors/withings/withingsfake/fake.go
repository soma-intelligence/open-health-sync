// SPDX-License-Identifier: AGPL-3.0-only

// Package withingsfake is an httptest stand-in for the Withings API, for the
// connector's tests and for tests elsewhere that need real Withings lines
// (pull through the connector, then process). It answers from recorded,
// anonymised responses (testdata/: round values, Europe/Paris, fake ids) and
// keeps the rules of the real API that the connector depends on: pagination
// with more/offset, calendar date windows (inclusive) on getactivity, sleep
// getsummary and getworkouts, at most 24 hours per call on
// getintradayactivity and sleep action=get.
package withingsfake

import (
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strconv"
	"sync"
	"testing"
)

//go:embed testdata/*.json
var files embed.FS

// Request is one request the fake received.
type Request struct {
	Path   string
	Auth   string
	Form   url.Values
	Query  url.Values
	Action string
}

// Server is the fake. Set the knobs before the first request.
type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu       sync.Mutex
	requests []Request

	// TokenStatus and TokenBody override the token endpoint answer.
	TokenStatus int
	TokenBody   string
	// DataStatus, when set, is the inner status every data endpoint answers;
	// DataHTTP the HTTP status.
	DataStatus int
	DataHTTP   int
	// SignalFails makes heart action=get answer an error status.
	SignalFails bool
	// NoSleepSummary makes sleep getsummary answer no night.
	NoSleepSummary bool
	// SleepSummaryBody, when set, is the whole getsummary answer.
	SleepSummaryBody string
	// Bodies, keyed by path and action ("/v2/measure getactivity", "/v2/sleep
	// get"), replace the recorded answer of that request with a whole body.
	Bodies map[string]string
	// NotifyStatus is the inner status of the notify endpoint.
	NotifyStatus int
	// RateLimit, when it returns true for a path and action, answers that
	// request with a rate limit: HTTP 429, or inner status 601 when
	// RateLimitInner is set, with RetryAfter as the Retry-After header.
	RateLimit      func(path, action string) bool
	RateLimitInner bool
	RetryAfter     string
}

// New starts a fake, closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL of the fake, for both the account and the API base.
func (s *Server) URL() string { return s.srv.URL }

// Client is an HTTP client for the fake.
func (s *Server) Client() *http.Client { return s.srv.Client() }

// Requests returns every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Calls returns the requests to path with action ("" for any action).
func (s *Server) Calls(p, action string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Path == p && (action == "" || r.Action == action) {
			out = append(out, r)
		}
	}
	return out
}

// File returns one recorded response.
func File(name string) []byte {
	b, err := files.ReadFile(path.Join("testdata", name))
	if err != nil {
		panic(err)
	}
	return b
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	action := form.Get("action")
	if action == "" {
		action = r.URL.Query().Get("action")
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Form: form, Query: r.URL.Query(), Action: action})
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	write := func(b []byte) { _, _ = w.Write(b) }

	if s.RateLimit != nil && s.RateLimit(r.URL.Path, action) {
		if s.RetryAfter != "" {
			w.Header().Set("Retry-After", s.RetryAfter)
		}
		if s.RateLimitInner {
			write([]byte(`{"status":601,"body":{}}`))
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}

	switch r.URL.Path {
	case "/v2/oauth2":
		if s.TokenStatus != 0 {
			w.WriteHeader(s.TokenStatus)
		}
		if s.TokenBody != "" {
			write([]byte(s.TokenBody))
			return
		}
		write(File("token.json"))
		return
	case "/notify":
		write([]byte(`{"status":` + strconv.Itoa(s.NotifyStatus) + `,"body":{}}`))
		return
	}
	if s.DataHTTP != 0 {
		w.WriteHeader(s.DataHTTP)
		write([]byte(`{}`))
		return
	}
	if s.DataStatus != 0 {
		write([]byte(`{"status":` + strconv.Itoa(s.DataStatus) + `,"body":{}}`))
		return
	}

	if b, ok := s.Bodies[r.URL.Path+" "+action]; ok {
		write([]byte(b))
		return
	}

	switch {
	case r.URL.Path == "/measure" && action == "getmeas":
		if form.Get("offset") == "1" {
			write(File("measure_page2.json"))
		} else {
			write(File("measure_page1.json"))
		}
	case r.URL.Path == "/v2/measure" && action == "getactivity":
		write(s.byDate("activity.json", "activities", form))
	case r.URL.Path == "/v2/measure" && action == "getworkouts":
		write(s.byDate("workouts.json", "series", form))
	case r.URL.Path == "/v2/measure" && action == "getintradayactivity":
		write(s.intraday(form))
	case r.URL.Path == "/v2/sleep" && action == "getsummary":
		if s.SleepSummaryBody != "" {
			write([]byte(s.SleepSummaryBody))
			return
		}
		if s.NoSleepSummary {
			write([]byte(`{"status":0,"body":{"series":[],"more":false,"offset":0}}`))
			return
		}
		write(s.byDate("sleep_summary.json", "series", form))
	case r.URL.Path == "/v2/sleep" && action == "get":
		write(s.sleepDetail(form))
	case r.URL.Path == "/v2/heart" && action == "list":
		write(File("heart_list.json"))
	case r.URL.Path == "/v2/heart" && action == "get":
		if s.SignalFails {
			write([]byte(`{"status":503,"body":{}}`))
			return
		}
		write(File("heart_get.json"))
	default:
		w.WriteHeader(http.StatusNotFound)
		write([]byte(`{"status":2555}`))
	}
}

// byDate answers a calendar date endpoint with the recorded items whose
// "date" lies in [startdateymd, enddateymd], both inclusive.
func (s *Server) byDate(file, list string, form url.Values) []byte {
	var all struct {
		Body map[string]json.RawMessage `json:"body"`
	}
	s.must(json.Unmarshal(File(file), &all))
	var items []map[string]any
	s.must(json.Unmarshal(all.Body[list], &items))
	first, last := form.Get("startdateymd"), form.Get("enddateymd")
	kept := []map[string]any{}
	for _, it := range items {
		d, _ := it["date"].(string)
		if d >= first && d <= last {
			kept = append(kept, it)
		}
	}
	return s.answer(map[string]any{list: kept, "more": false, "offset": 0})
}

// window reads startdate and enddate, the end capped at 24 hours after the
// start as Withings does for intraday activity and sleep detail.
func window(form url.Values) (int64, int64) {
	start, _ := strconv.ParseInt(form.Get("startdate"), 10, 64)
	end, _ := strconv.ParseInt(form.Get("enddate"), 10, 64)
	if end > start+86400 {
		end = start + 86400
	}
	return start, end
}

func (s *Server) intraday(form url.Values) []byte {
	var all struct {
		Body struct {
			Series map[string]json.RawMessage `json:"series"`
		} `json:"body"`
	}
	s.must(json.Unmarshal(File("intraday.json"), &all))
	start, end := window(form)
	out := map[string]json.RawMessage{}
	for k, v := range all.Body.Series {
		ts, _ := strconv.ParseInt(k, 10, 64)
		if ts >= start && ts < end {
			out[k] = v
		}
	}
	return s.answer(map[string]any{"series": out})
}

func (s *Server) sleepDetail(form url.Values) []byte {
	var all struct {
		Body struct {
			Series []struct {
				Startdate int64 `json:"startdate"`
				Enddate   int64 `json:"enddate"`
				State     int   `json:"state"`
			} `json:"series"`
		} `json:"body"`
	}
	s.must(json.Unmarshal(File("sleep_get.json"), &all))
	start, end := window(form)
	out := []any{}
	for _, seg := range all.Body.Series {
		if seg.Startdate >= start && seg.Startdate < end {
			out = append(out, seg)
		}
	}
	return s.answer(map[string]any{"series": out})
}

func (s *Server) answer(body any) []byte {
	b, err := json.Marshal(map[string]any{"status": 0, "body": body})
	s.must(err)
	return b
}

func (s *Server) must(err error) {
	if err != nil {
		s.t.Errorf("withingsfake: %v", err)
	}
}
