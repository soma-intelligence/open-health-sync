// SPDX-License-Identifier: AGPL-3.0-only

// Package icufake is an httptest stand-in for the intervals.icu API v1 and
// its OAuth token endpoint, for the connector's tests. It answers from
// invented fixtures (testdata/: round values, fake ids) and keeps the rules of
// the real API the connector depends on: athlete id 0 names the token's
// athlete, GET /athlete/0/activities filters on the local start date with
// inclusive oldest and newest dates, and GET /athlete/0/wellness filters on
// the record's date the same way. A request without the expected bearer
// token is answered 401.
package icufake

import (
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"sort"
	"sync"
	"testing"
)

//go:embed testdata/*.json
var files embed.FS

// AccessToken is the token the fixture token answer hands out and the API
// accepts.
const AccessToken = "test-access-token"

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
	// DataFailures answers the first n API requests with HTTP 503.
	DataFailures int
	// DisconnectStatus is the HTTP status of DELETE /disconnect-app (200
	// when zero).
	DisconnectStatus int
	// Activities and Wellness, when set, replace the fixture lists.
	Activities, Wellness []json.RawMessage
	// RateLimit, when it returns true for a path, answers that request with
	// HTTP 429 and Retry-After: 120.
	RateLimit func(path string) bool
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

// APIURL is the base URL of the fake API v1.
func (s *Server) APIURL() string { return s.srv.URL + "/api/v1" }

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

// List returns the objects of a fixture holding a JSON array.
func List(name string) []json.RawMessage {
	var all []json.RawMessage
	if err := json.Unmarshal(File(name), &all); err != nil {
		panic(err)
	}
	return all
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Form: form, Query: r.URL.Query()})
	failing := false
	if s.DataFailures > 0 && r.URL.Path != "/api/oauth/token" {
		s.DataFailures--
		failing = true
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	write := func(b []byte) { _, _ = w.Write(b) }

	if s.RateLimit != nil && s.RateLimit(r.URL.Path) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		write([]byte(`{"status":429,"error":"Too Many Requests"}`))
		return
	}

	if r.Method == http.MethodPost && r.URL.Path == "/api/oauth/token" {
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

	if failing {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+AccessToken {
		w.WriteHeader(http.StatusUnauthorized)
		write([]byte(`{"status":401,"error":"Unauthorized"}`))
		return
	}
	if s.DataHTTP != 0 {
		w.WriteHeader(s.DataHTTP)
		write([]byte(`{"error":"refused"}`))
		return
	}
	switch {
	case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/disconnect-app":
		if s.DisconnectStatus != 0 {
			w.WriteHeader(s.DisconnectStatus)
		}
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/athlete/0/activities":
		all := s.Activities
		if all == nil {
			all = List("activities.json")
		}
		write(s.filter(all, r.URL.Query(), func(head map[string]any) string {
			v, _ := head["start_date_local"].(string)
			if len(v) >= 10 {
				return v[:10]
			}
			return ""
		}))
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/athlete/0/wellness":
		all := s.Wellness
		if all == nil {
			all = List("wellness.json")
		}
		write(s.filter(all, r.URL.Query(), func(head map[string]any) string {
			v, _ := head["id"].(string)
			return v
		}))
	default:
		w.WriteHeader(http.StatusNotFound)
		write([]byte(`{"status":404,"error":"Not Found"}`))
	}
}

// filter keeps the objects whose date (YYYY-MM-DD, compared as text) lies in
// [oldest, newest], sorted by date. A missing newest means no upper bound.
func (s *Server) filter(all []json.RawMessage, q url.Values, date func(map[string]any) string) []byte {
	oldest, newest := q.Get("oldest"), q.Get("newest")
	type item struct {
		date string
		raw  json.RawMessage
	}
	var kept []item
	for _, a := range all {
		var head map[string]any
		if json.Unmarshal(a, &head) != nil {
			continue
		}
		d := date(head)
		if d == "" || d < oldest || (newest != "" && d > newest) {
			continue
		}
		kept = append(kept, item{date: d, raw: a})
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].date < kept[j].date })
	out := []json.RawMessage{}
	for _, k := range kept {
		out = append(out, k.raw)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		s.t.Errorf("icufake: %v", err)
	}
	return b
}
