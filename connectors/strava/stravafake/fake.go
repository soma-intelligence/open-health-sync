// SPDX-License-Identifier: AGPL-3.0-only

// Package stravafake is an httptest stand-in for the Strava API v3 and its
// OAuth endpoints, for the connector's tests and for tests elsewhere that need
// real Strava lines. It answers from invented fixtures (testdata/: round
// values, fake ids) and keeps the rules of the real API that the connector
// depends on: GET /athlete/activities filters on the activity start with
// exclusive after and before epochs, sorts by start, and pages with page
// (from 1) and per_page; a page shorter than per_page is the last.
package stravafake

import (
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"sort"
	"strconv"
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

	// TokenStatus and TokenBody override the token endpoint answer.
	TokenStatus int
	TokenBody   string
	// DataHTTP, when set, is the HTTP status every API request answers.
	DataHTTP int
	// RevokeStatus is the HTTP status of POST /oauth/revoke (200 when zero).
	RevokeStatus int
	// Activities, when set, replaces the fixture list (raw JSON objects).
	Activities []json.RawMessage
	// RateLimit, when it returns true for a path, answers that request with
	// HTTP 429 and RateLimitHeaders.
	RateLimit        func(path string) bool
	RateLimitHeaders http.Header
}

// New starts a fake, closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL of the fake, for the OAuth base.
func (s *Server) URL() string { return s.srv.URL }

// APIURL is the base URL of the fake API v3.
func (s *Server) APIURL() string { return s.srv.URL + "/api/v3" }

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

// Activities returns the fixture activities, one raw JSON object each.
func Activities() []json.RawMessage {
	var all []json.RawMessage
	if err := json.Unmarshal(File("activities.json"), &all); err != nil {
		panic(err)
	}
	return all
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
		write([]byte(`{"message":"Rate Limit Exceeded","errors":[{"resource":"Application","field":"rate limit","code":"exceeded"}]}`))
		return
	}

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/token":
		if s.TokenStatus != 0 {
			w.WriteHeader(s.TokenStatus)
		}
		if s.TokenBody != "" {
			write([]byte(s.TokenBody))
			return
		}
		write(File("token.json"))
		return
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/revoke":
		if s.RevokeStatus != 0 {
			w.WriteHeader(s.RevokeStatus)
		}
		return
	}
	if s.DataHTTP != 0 {
		w.WriteHeader(s.DataHTTP)
		write([]byte(`{"message":"Authorization Error","errors":[{"resource":"Athlete","field":"access_token","code":"invalid"}]}`))
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v3/athlete/activities":
		write(s.activities(r.URL.Query()))
	default:
		w.WriteHeader(http.StatusNotFound)
		write([]byte(`{"message":"Record Not Found","errors":[{"resource":"resource","field":"path","code":"invalid"}]}`))
	}
}

// activities answers GET /athlete/activities: start strictly between after
// and before, sorted by start, one page.
func (s *Server) activities(q url.Values) []byte {
	all := s.Activities
	if all == nil {
		all = Activities()
	}
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	before, err := strconv.ParseInt(q.Get("before"), 10, 64)
	if err != nil {
		before = 1 << 62
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
		at  int64
		raw json.RawMessage
	}
	var kept []item
	for _, a := range all {
		var head struct {
			StartDate string `json:"start_date"`
		}
		if json.Unmarshal(a, &head) != nil {
			continue
		}
		at, err := time.Parse(time.RFC3339, head.StartDate)
		if err != nil {
			continue
		}
		if at.Unix() > after && at.Unix() < before {
			kept = append(kept, item{at: at.Unix(), raw: a})
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].at < kept[j].at })
	out := []json.RawMessage{}
	for i := (page - 1) * per; i < len(kept) && i < page*per; i++ {
		out = append(out, kept[i].raw)
	}
	b, err := json.Marshal(out)
	if err != nil {
		s.t.Errorf("stravafake: %v", err)
	}
	return b
}
