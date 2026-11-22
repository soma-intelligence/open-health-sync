// SPDX-License-Identifier: AGPL-3.0-only

// Package wahoofake is an httptest stand-in for the Wahoo Cloud API v1 and
// its OAuth endpoints, for the connector's tests. It answers from fixtures
// built from the example payloads of Wahoo's API reference (testdata/) and
// keeps the rules of the real API the connector depends on: GET /v1/workouts
// always sorts by start, newest first, pages with page (from 1) and per_page
// (30 by default) and answers an envelope with total; a workout summary not
// there yet is a 404; decimals are strings.
package wahoofake

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

//go:embed testdata/*.json
var files embed.FS

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
	issued   int

	// TokenStatus and TokenBody override the token endpoint answer.
	TokenStatus int
	TokenBody   string
	// Rotate makes each token answer issue a new pair,
	// fake-access-token-rN and fake-refresh-token-rN.
	Rotate bool
	// DataHTTP, when set, is the HTTP status every API request answers.
	DataHTTP int
	// RateLimit, when it returns true for a path, answers that request with
	// HTTP 429 and RateLimitHeaders.
	RateLimit        func(path string) bool
	RateLimitHeaders http.Header

	// Workouts, when set, replaces the fixture list (raw JSON objects);
	// Summaries are the answers of GET /v1/workouts/{id}/workout_summary by
	// workout id (the fixture summary for 56519 when nil).
	Workouts  []json.RawMessage
	Summaries map[string]json.RawMessage
	// User, when set, replaces the fixture user.
	User json.RawMessage
}

// New starts a fake, closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL of the fake.
func (s *Server) URL() string { return s.srv.URL }

// Client is an HTTP client for the fake.
func (s *Server) Client() *http.Client { return s.srv.Client() }

// SetWorkouts replaces the listed workouts and the summaries, safe to call
// while the fake serves.
func (s *Server) SetWorkouts(workouts []json.RawMessage, summaries map[string]json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Workouts, s.Summaries = workouts, summaries
}

// Requests returns every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Calls returns the requests to p.
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

// Workouts returns the workouts of the list fixture, one raw object each.
func Workouts() []json.RawMessage {
	var p struct {
		Workouts []json.RawMessage `json:"workouts"`
	}
	if err := json.Unmarshal(File("workouts.json"), &p); err != nil {
		panic(err)
	}
	return p.Workouts
}

// Edit returns raw (a JSON object) with fields replaced.
func Edit(raw []byte, edits map[string]any) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(err)
	}
	for k, v := range edits {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Form: form, Query: r.URL.Query()})
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	write := func(b []byte) { _, _ = w.Write(b) }

	if s.RateLimit != nil && s.RateLimit(r.URL.Path) {
		for k, v := range s.RateLimitHeaders {
			w.Header()[k] = v
		}
		w.WriteHeader(http.StatusTooManyRequests)
		write([]byte(`{"error":"Too Many Requests"}`))
		return
	}

	if r.Method == http.MethodPost && r.URL.Path == "/oauth/token" {
		s.token(w)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		write([]byte(`{"error":"Unauthorized"}`))
		return
	}
	if s.DataHTTP != 0 {
		w.WriteHeader(s.DataHTTP)
		write([]byte(`{"error":"refused"}`))
		return
	}
	switch {
	case r.Method == http.MethodDelete && r.URL.Path == "/v1/permissions":
		write([]byte(`{"success":true}`))
	case r.Method == http.MethodGet && r.URL.Path == "/v1/user":
		s.mu.Lock()
		u := s.User
		s.mu.Unlock()
		if u == nil {
			u = File("user.json")
		}
		write(u)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/workouts":
		write(s.list(r.URL.Query()))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/workouts/") && strings.HasSuffix(r.URL.Path, "/workout_summary"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/workouts/"), "/workout_summary")
		s.mu.Lock()
		sums := s.Summaries
		s.mu.Unlock()
		if sums == nil {
			sums = map[string]json.RawMessage{"56519": File("workout_summary.json")}
		}
		if b, ok := sums[id]; ok {
			write(b)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		write([]byte(`{"error":"Not Found"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		write([]byte(`{"error":"Not Found"}`))
	}
}

// token answers POST /oauth/token.
func (s *Server) token(w http.ResponseWriter) {
	if s.TokenStatus != 0 {
		w.WriteHeader(s.TokenStatus)
	}
	if s.TokenBody != "" {
		_, _ = w.Write([]byte(s.TokenBody))
		return
	}
	if !s.Rotate {
		_, _ = w.Write(File("token.json"))
		return
	}
	s.mu.Lock()
	s.issued++
	n := s.issued
	s.mu.Unlock()
	_, _ = fmt.Fprintf(w, `{"access_token":"fake-access-token-r%d","token_type":"Bearer","expires_in":7200,"refresh_token":"fake-refresh-token-r%d","scope":"user_read workouts_read offline_data","created_at":%d}`,
		n, n, time.Now().Unix())
}

// list answers GET /v1/workouts: newest start first, one page.
func (s *Server) list(q url.Values) []byte {
	s.mu.Lock()
	all := s.Workouts
	s.mu.Unlock()
	if all == nil {
		all = Workouts()
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per < 1 {
		per = 30
	}
	type item struct {
		at  time.Time
		raw json.RawMessage
	}
	items := make([]item, 0, len(all))
	for _, a := range all {
		var head struct {
			Starts string `json:"starts"`
		}
		_ = json.Unmarshal(a, &head)
		at, _ := time.Parse(time.RFC3339, head.Starts)
		items = append(items, item{at: at, raw: a})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].at.After(items[j].at) })
	out := []json.RawMessage{}
	for i := (page - 1) * per; i < len(items) && i < page*per; i++ {
		out = append(out, items[i].raw)
	}
	b, err := json.Marshal(map[string]any{
		"workouts": out, "total": len(items), "page": page, "per_page": per,
		"order": "descending", "sort": "starts",
	})
	if err != nil {
		s.t.Errorf("wahoofake: %v", err)
	}
	return b
}
