// SPDX-License-Identifier: AGPL-3.0-only

// Package polarfake is an httptest stand-in for the Polar AccessLink API v3
// and its OAuth token endpoint, for the connector's tests and for tests
// elsewhere that need Polar lines. It answers from invented responses
// (testdata/: round values, fake ids, a +01:00 user) and keeps the rules of
// the real API that the connector depends on: HTTP Basic client
// authentication on the token endpoint, bearer tokens on /v3, user
// registration answering 409 once registered, inclusive from/to date ranges
// on daily activity and continuous heart rate (404 when a range holds no
// heart rate), 204 when there is nothing.
package polarfake

import (
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync"
	"testing"
)

//go:embed testdata/*.json
var files embed.FS

// Request is one request the fake received.
type Request struct {
	Method string
	Path   string
	Auth   string
	Query  url.Values
	Body   string
}

// Server is the fake. Set the knobs before the first request.
type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu         sync.Mutex
	requests   []Request
	registered map[string]bool

	// ClientID and ClientSecret are the Basic credentials the token endpoint
	// accepts (any when empty).
	ClientID, ClientSecret string
	// TokenStatus and TokenBody override the token endpoint answer.
	TokenStatus int
	TokenBody   string
	// RegisterStatus, when set, is the answer of POST /v3/users.
	RegisterStatus int
	// UserStatus, when set, is the answer of GET /v3/users/{id}.
	UserStatus int
	// DataHTTP, when set, is the HTTP status every data endpoint answers.
	DataHTTP int
	// Empty makes every data endpoint answer 204 (no data).
	Empty bool
	// RateLimit, when it returns true for a method and path, answers that
	// request with HTTP 429 and the headers below.
	RateLimit                                         func(method, path string) bool
	RetryAfter                                        string
	RateLimitUsage, RateLimitLimit, RateLimitResetHdr string
}

// New starts a fake, closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t, registered: map[string]bool{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL of the fake, for the auth, token and API bases.
func (s *Server) URL() string { return s.srv.URL }

// Client is an HTTP client for the fake.
func (s *Server) Client() *http.Client { return s.srv.Client() }

// Requests returns every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Calls returns the requests with method ("" for any) to path.
func (s *Server) Calls(method, p string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Path == p && (method == "" || r.Method == method) {
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
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Query: r.URL.Query(), Body: string(body)})
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	write := func(b []byte) { _, _ = w.Write(b) }

	if s.RateLimit != nil && s.RateLimit(r.Method, r.URL.Path) {
		for k, v := range map[string]string{"Retry-After": s.RetryAfter, "RateLimit-Usage": s.RateLimitUsage, "RateLimit-Limit": s.RateLimitLimit, "RateLimit-Reset": s.RateLimitResetHdr} {
			if v != "" {
				w.Header().Set(k, v)
			}
		}
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}

	if r.URL.Path == "/v2/oauth2/token" && r.Method == http.MethodPost {
		s.token(w, r, body)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v3/users":
		s.register(w, body)
		return
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v3/users/"):
		w.WriteHeader(http.StatusNoContent)
		return
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v3/users/") && isDigits(strings.TrimPrefix(r.URL.Path, "/v3/users/")):
		if s.UserStatus != 0 {
			w.WriteHeader(s.UserStatus)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v3/users/")
		write([]byte(`{"polar-user-id":` + id + `,"member-id":"ohs-` + id + `","registration-date":"2026-01-01T00:00:00.000Z"}`))
		return
	}

	if s.DataHTTP != 0 {
		w.WriteHeader(s.DataHTTP)
		return
	}
	if s.Empty {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v3/exercises":
		write(File("exercises.json"))
	case r.Method == http.MethodGet && r.URL.Path == "/v3/users/sleep":
		write(File("sleep.json"))
	case r.Method == http.MethodGet && r.URL.Path == "/v3/users/nightly-recharge":
		write(File("nightly_recharge.json"))
	case r.Method == http.MethodGet && r.URL.Path == "/v3/users/activities":
		var items []map[string]any
		s.must(json.Unmarshal(File("activities.json"), &items))
		kept := []map[string]any{}
		for _, it := range items {
			st, _ := it["start_time"].(string)
			if len(st) >= 10 && st[:10] >= q.Get("from") && st[:10] <= q.Get("to") {
				kept = append(kept, it)
			}
		}
		b, err := json.Marshal(kept)
		s.must(err)
		write(b)
	case r.Method == http.MethodGet && r.URL.Path == "/v3/users/continuous-heart-rate":
		var all struct {
			HeartRates []map[string]any `json:"heart_rates"`
		}
		s.must(json.Unmarshal(File("continuous_hr.json"), &all))
		kept := []map[string]any{}
		for _, it := range all.HeartRates {
			d, _ := it["date"].(string)
			if d >= q.Get("from") && d <= q.Get("to") {
				kept = append(kept, it)
			}
		}
		if len(kept) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, err := json.Marshal(map[string]any{"heart_rates": kept})
		s.must(err)
		write(b)
	case r.Method == http.MethodGet && r.URL.Path == "/v3/users/physical-info":
		write(File("physical_info.json"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request, body []byte) {
	id, secret, ok := r.BasicAuth()
	if !ok || (s.ClientID != "" && (id != s.ClientID || secret != s.ClientSecret)) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		return
	}
	form, _ := url.ParseQuery(string(body))
	if form.Get("grant_type") != "authorization_code" || form.Get("code") == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
		return
	}
	if s.TokenStatus != 0 {
		w.WriteHeader(s.TokenStatus)
	}
	if s.TokenBody != "" {
		_, _ = w.Write([]byte(s.TokenBody))
		return
	}
	_, _ = w.Write(File("token.json"))
}

func (s *Server) register(w http.ResponseWriter, body []byte) {
	var req map[string]string
	if err := json.Unmarshal(body, &req); err != nil || req["member-id"] == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if s.RegisterStatus != 0 {
		w.WriteHeader(s.RegisterStatus)
		return
	}
	s.mu.Lock()
	dup := s.registered[req["member-id"]]
	s.registered[req["member-id"]] = true
	s.mu.Unlock()
	if dup {
		w.WriteHeader(http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"polar-user-id":10101010,"member-id":"` + req["member-id"] + `","registration-date":"2026-03-01T00:00:00.000Z"}`))
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (s *Server) must(err error) {
	if err != nil {
		s.t.Errorf("polarfake: %v", err)
	}
}
