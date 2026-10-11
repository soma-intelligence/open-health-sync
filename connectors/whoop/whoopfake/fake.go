// SPDX-License-Identifier: AGPL-3.0-only

// Package whoopfake is an httptest stand-in for the WHOOP API v2, for the
// connector's tests and for tests elsewhere that need WHOOP lines (pull
// through the connector, then process). It answers from fixtures with
// invented values (testdata/: user 10001, offset +01:00, fake ids) and keeps
// the rules of the real API that the connector depends on: collections
// filtered on [start, end) and paginated with nextToken / next_token, at most
// 25 records a page, a cycle readable by id, Bearer authentication, and the
// OAuth token endpoint.
package whoopfake

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

// UserID is the WHOOP user id of the fixtures.
const UserID = "10001"

// Request is one request the fake received.
type Request struct {
	Method string
	Path   string
	Auth   string
	Form   url.Values
	Query  url.Values
}

// Server is the fake. Set the knobs before the first request.
type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu       sync.Mutex
	requests []Request

	// PageSize is how many records a collection page holds (default 2, so
	// the fixtures span several pages).
	PageSize int
	// TokenStatus and TokenBody override the token endpoint answer.
	TokenStatus int
	TokenBody   string
	// ProfileStatus, when set, is the HTTP status of the profile endpoint.
	ProfileStatus int
	// DataHTTP, when set, is the HTTP status every data endpoint answers.
	DataHTTP int
	// Records, when set for a path, replaces that collection's fixture.
	Records map[string]string
	// RateLimit, when it returns true for a path, answers that request with
	// HTTP 429, RetryAfter as Retry-After and RateLimitReset as
	// X-RateLimit-Reset (each only when set).
	RateLimit      func(path string) bool
	RetryAfter     string
	RateLimitReset string
}

// New starts a fake, closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t, PageSize: 2}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL of the fake, for both the auth and the API base.
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

// File returns one fixture.
func File(name string) []byte {
	b, err := files.ReadFile(path.Join("testdata", name))
	if err != nil {
		panic(err)
	}
	return b
}

// collections maps each collection path to its fixture and the field its
// window filter reads.
var collections = map[string]struct{ file, timeField string }{
	"/v2/cycle":            {"cycles.json", "start"},
	"/v2/recovery":         {"recoveries.json", "created_at"},
	"/v2/activity/sleep":   {"sleeps.json", "start"},
	"/v2/activity/workout": {"workouts.json", "start"},
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Form: form, Query: r.URL.Query()})
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	write := func(b []byte) { _, _ = w.Write(b) }

	if s.RateLimit != nil && s.RateLimit(r.URL.Path) {
		if s.RetryAfter != "" {
			w.Header().Set("Retry-After", s.RetryAfter)
		}
		if s.RateLimitReset != "" {
			w.Header().Set("X-RateLimit-Reset", s.RateLimitReset)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}

	if r.URL.Path == "/oauth2/token" {
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
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/v2/user/profile/basic" {
		if s.ProfileStatus != 0 {
			w.WriteHeader(s.ProfileStatus)
			return
		}
		write(File("profile.json"))
		return
	}
	if s.DataHTTP != 0 {
		w.WriteHeader(s.DataHTTP)
		write([]byte(`{}`))
		return
	}

	if c, ok := collections[r.URL.Path]; ok {
		write(s.page(r.URL.Path, c.file, c.timeField, r.URL.Query()))
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/v2/cycle/"):
		id := strings.TrimPrefix(r.URL.Path, "/v2/cycle/")
		for _, rec := range s.records("/v2/cycle", "cycles.json") {
			if idOf(rec["id"]) == id {
				b, err := json.Marshal(rec)
				s.must(err)
				write(b)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	case r.URL.Path == "/v2/user/measurement/body":
		write(File("body.json"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// records reads a collection fixture (or its override).
func (s *Server) records(p, file string) []map[string]any {
	raw := File(file)
	if o, ok := s.Records[p]; ok {
		raw = []byte(o)
	}
	var all struct {
		Records []map[string]any `json:"records"`
	}
	s.must(json.Unmarshal(raw, &all))
	return all.Records
}

// page answers one collection request: the records whose timeField lies in
// [start, end), PageSize (at most limit) at a time, the offset of the next
// page as next_token.
func (s *Server) page(p, file, timeField string, q url.Values) []byte {
	start, _ := time.Parse(time.RFC3339Nano, q.Get("start"))
	end, err := time.Parse(time.RFC3339Nano, q.Get("end"))
	if err != nil {
		end = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	var kept []map[string]any
	for _, rec := range s.records(p, file) {
		ts, _ := rec[timeField].(string)
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil || t.Before(start) || !t.Before(end) {
			continue
		}
		kept = append(kept, rec)
	}
	size := s.PageSize
	if limit, err := strconv.Atoi(q.Get("limit")); err == nil && limit > 0 && limit < size {
		size = limit
	}
	if limit, _ := strconv.Atoi(q.Get("limit")); limit > 25 {
		s.t.Errorf("whoopfake: limit %d over the documented maximum of 25", limit)
	}
	offset, _ := strconv.Atoi(q.Get("nextToken"))
	if offset > len(kept) {
		offset = len(kept)
	}
	last := min(offset+size, len(kept))
	out := map[string]any{"records": append([]map[string]any{}, kept[offset:last]...)}
	if last < len(kept) {
		out["next_token"] = strconv.Itoa(last)
	} else {
		out["next_token"] = nil
	}
	b, err := json.Marshal(out)
	s.must(err)
	return b
}

// idOf renders a decoded JSON id (number or string).
func idOf(v any) string {
	switch id := v.(type) {
	case float64:
		return strconv.FormatInt(int64(id), 10)
	case string:
		return id
	}
	return ""
}

func (s *Server) must(err error) {
	if err != nil {
		s.t.Errorf("whoopfake: %v", err)
	}
}
