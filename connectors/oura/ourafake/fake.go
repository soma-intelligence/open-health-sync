// SPDX-License-Identifier: AGPL-3.0-only

// Package ourafake is an httptest stand-in for the Oura Cloud API v2, for the
// connector's tests and for tests elsewhere that need real Oura lines (pull
// through the connector, then process). It answers from invented responses
// (testdata/: round values, UTC+01:00, fake ids) and keeps the rules of the
// real API that the connector depends on: the token endpoint, Bearer auth,
// start_date and end_date (inclusive) filtering on each document's "day",
// start_datetime and end_datetime on heartrate, and pagination through
// next_token. The vo2_max documents are served at vO2_max, Oura's spelling
// of that endpoint.
package ourafake

import (
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

//go:embed testdata/*.json
var files embed.FS

// collectionPath is the prefix of every data endpoint.
const collectionPath = "/v2/usercollection/"

// Request is one request the fake received.
type Request struct {
	Path  string
	Auth  string
	Form  url.Values
	Query url.Values
}

// Server is the fake. Set the knobs before the first request.
type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu       sync.Mutex
	requests []Request

	// PageSize is how many documents one page holds (2 by default), so the
	// fixtures span several pages.
	PageSize int
	// TokenStatus and TokenBody override the token endpoint answer.
	TokenStatus int
	TokenBody   string
	// DataHTTP, when set, is the HTTP status every data endpoint answers.
	DataHTTP int
	// Forbidden lists the data types that answer 403.
	Forbidden map[string]bool
	// Status answers the endpoints it names with that HTTP status.
	Status map[string]int
	// Bodies overrides the whole answer of a data type (no filtering, no
	// pagination).
	Bodies map[string]string
	// RateLimit, when it returns true for a path, answers that request with
	// HTTP 429 and RetryAfter as the Retry-After header.
	RateLimit  func(path string) bool
	RetryAfter string
}

// New starts a fake, closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t, PageSize: 2}
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

// Calls returns the requests to path.
func (s *Server) Calls(p string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Path == p {
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
	s.mu.Lock()
	s.requests = append(s.requests, Request{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Form: form, Query: r.URL.Query()})
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	write := func(b []byte) { _, _ = w.Write(b) }

	if s.RateLimit != nil && s.RateLimit(r.URL.Path) {
		if s.RetryAfter != "" {
			w.Header().Set("Retry-After", s.RetryAfter)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		write([]byte(`{"detail":"rate limit exceeded"}`))
		return
	}

	if r.URL.Path == "/oauth/token" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if s.TokenStatus != 0 {
			w.WriteHeader(s.TokenStatus)
		}
		if s.TokenBody != "" {
			write([]byte(s.TokenBody))
			return
		}
		write(File("token.json"))
		return
	}

	if !strings.HasPrefix(r.URL.Path, collectionPath) || r.Method != http.MethodGet {
		w.WriteHeader(http.StatusNotFound)
		write([]byte(`{"detail":"not found"}`))
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		write([]byte(`{"detail":"missing token"}`))
		return
	}
	if s.DataHTTP != 0 {
		w.WriteHeader(s.DataHTTP)
		write([]byte(`{"detail":"error"}`))
		return
	}
	dataType := strings.TrimPrefix(r.URL.Path, collectionPath)
	if code, ok := s.Status[dataType]; ok {
		w.WriteHeader(code)
		write([]byte(`{"detail":"status"}`))
		return
	}
	if s.Forbidden[dataType] {
		w.WriteHeader(http.StatusForbidden)
		write([]byte(`{"detail":"forbidden"}`))
		return
	}
	if b, ok := s.Bodies[dataType]; ok {
		write([]byte(b))
		return
	}
	switch dataType {
	case "personal_info":
		write(File("personal_info.json"))
	case "daily_activity", "sleep", "daily_readiness", "daily_spo2", "workout", "heartrate",
		"daily_sleep", "daily_stress", "session", "vO2_max":
		write(s.list(fixtureName(dataType), r.URL.Query()))
	default:
		w.WriteHeader(http.StatusNotFound)
		write([]byte(`{"detail":"not found"}`))
	}
}

// fixtureName is the testdata file (without .json) of an endpoint: its path
// segment, except vO2_max, whose documents are filed under the data type the
// connector stores them as.
func fixtureName(endpoint string) string {
	if endpoint == "vO2_max" {
		return "vo2_max"
	}
	return endpoint
}

// list answers a list endpoint: the recorded documents inside the asked
// window, one page of them, with next_token naming the offset of the next.
func (s *Server) list(dataType string, q url.Values) []byte {
	var all struct {
		Data []map[string]any `json:"data"`
	}
	s.must(json.Unmarshal(File(dataType+".json"), &all))
	kept := []map[string]any{}
	for _, d := range all.Data {
		if inWindow(dataType, d, q) {
			kept = append(kept, d)
		}
	}
	offset, _ := strconv.Atoi(q.Get("next_token"))
	if offset > len(kept) {
		offset = len(kept)
	}
	end := len(kept)
	if s.PageSize > 0 && offset+s.PageSize < end {
		end = offset + s.PageSize
	}
	out := map[string]any{"data": kept[offset:end], "next_token": nil}
	if end < len(kept) {
		out["next_token"] = strconv.Itoa(end)
	}
	b, err := json.Marshal(out)
	s.must(err)
	return b
}

// inWindow reports whether document d answers the query: heartrate by
// timestamp in [start_datetime, end_datetime), the others by day in
// [start_date, end_date].
func inWindow(dataType string, d map[string]any, q url.Values) bool {
	if dataType == "heartrate" {
		ts, err := time.Parse(time.RFC3339, d["timestamp"].(string))
		if err != nil {
			return false
		}
		from, err1 := time.Parse(time.RFC3339, q.Get("start_datetime"))
		to, err2 := time.Parse(time.RFC3339, q.Get("end_datetime"))
		if err1 != nil || err2 != nil {
			return false
		}
		return !ts.Before(from) && ts.Before(to)
	}
	day, _ := d["day"].(string)
	return day >= q.Get("start_date") && day <= q.Get("end_date")
}

func (s *Server) must(err error) {
	if err != nil {
		s.t.Errorf("ourafake: %v", err)
	}
}
