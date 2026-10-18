// SPDX-License-Identifier: AGPL-3.0-only

// Package eightsleepfake is an httptest stand-in for the Eight Sleep account
// sign-in and the trends endpoint, for the connector's tests and for tests
// elsewhere that need Eight Sleep lines. It answers from invented nights
// (testdata/: round values, fake ids) and keeps the rules the connector
// depends on, as pyEight describes them: a password grant in a JSON body
// with the client credentials, a bearer token on the client API, trends
// filtered by from and to dates. It was not checked against Eight Sleep's
// own servers.
package eightsleepfake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Paths served.
const (
	TokenPath = "/v1/tokens"
	APIPrefix = "/v1"
)

// UserID is the Eight Sleep user id of the fake's user.
const UserID = "0000fake0000user0000000000000001"

// Account credentials the fake accepts by default.
const (
	Email    = "sleeper@example.org"
	Password = "correct horse battery staple"
)

// Request is one request the fake received.
type Request struct {
	Method string
	Path   string
	Auth   string
	Query  url.Values
	Body   map[string]string
}

// Server is the fake. Set the knobs before the first request.
type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu       sync.Mutex
	requests []Request
	signIns  int

	// ClientID and ClientSecret are the client credentials the token
	// endpoint accepts (any when empty).
	ClientID, ClientSecret string
	// Email and Password are the account the token endpoint accepts
	// (Email and Password constants when empty).
	Email, Password string
	// TokenStatus, when set, is the status the token endpoint answers.
	TokenStatus int
	// AccessToken is the bearer token the API accepts (any when empty).
	AccessToken string
	// DataHTTP, when set, is the status the trends endpoint answers.
	DataHTTP int
	// RetryAfter is the Retry-After header of a 429 answer.
	RetryAfter string
}

// New starts a fake that stops with the test.
func New(t testing.TB) *Server {
	s := &Server{t: t}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the fake's base URL. The connector's AuthURL is URL()+TokenPath and
// its APIBase URL()+APIPrefix.
func (s *Server) URL() string { return s.srv.URL }

// Client is an HTTP client for the fake.
func (s *Server) Client() *http.Client { return s.srv.Client() }

// Requests returns the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// SignIns counts the successful sign-ins.
func (s *Server) SignIns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signIns
}

// Days returns the testdata nights, one raw JSON object each.
func Days() [][]byte {
	var answer struct {
		Days []json.RawMessage `json:"days"`
	}
	b, err := files.ReadFile("testdata/trends.json")
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(b, &answer); err != nil {
		panic(err)
	}
	out := make([][]byte, len(answer.Days))
	for i, d := range answer.Days {
		out[i] = d
	}
	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	req := Request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Query: r.URL.Query()}
	if r.Method == http.MethodPost {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		req.Body = body
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	switch {
	case r.Method == http.MethodPost && r.URL.Path == TokenPath:
		s.token(w, req)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, APIPrefix+"/users/") && strings.HasSuffix(r.URL.Path, "/trends"):
		s.trends(w, r, req)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) token(w http.ResponseWriter, req Request) {
	if s.TokenStatus != 0 {
		s.status(w, s.TokenStatus)
		return
	}
	b := req.Body
	email, password := s.Email, s.Password
	if email == "" {
		email = Email
	}
	if password == "" {
		password = Password
	}
	switch {
	case b == nil || b["grant_type"] != "password":
		writeJSON(w, http.StatusBadRequest, `{"error":"unsupported_grant_type"}`)
	case s.ClientID != "" && (b["client_id"] != s.ClientID || b["client_secret"] != s.ClientSecret):
		writeJSON(w, http.StatusUnauthorized, `{"error":"invalid_client"}`)
	case b["username"] != email || b["password"] != password:
		writeJSON(w, http.StatusBadRequest, `{"error":"invalid_grant"}`)
	default:
		s.mu.Lock()
		s.signIns++
		s.mu.Unlock()
		tok, _ := files.ReadFile("testdata/token.json")
		writeJSON(w, http.StatusOK, string(tok))
	}
}

func (s *Server) trends(w http.ResponseWriter, r *http.Request, req Request) {
	if s.AccessToken != "" && req.Auth != "Bearer "+s.AccessToken {
		writeJSON(w, http.StatusUnauthorized, `{"message":"unauthorized"}`)
		return
	}
	if !strings.HasPrefix(req.Auth, "Bearer ") {
		writeJSON(w, http.StatusUnauthorized, `{"message":"unauthorized"}`)
		return
	}
	if s.DataHTTP != 0 {
		s.status(w, s.DataHTTP)
		return
	}
	user := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, APIPrefix+"/users/"), "/trends")
	if user != UserID {
		writeJSON(w, http.StatusForbidden, `{"message":"forbidden"}`)
		return
	}
	from, err1 := time.Parse(time.DateOnly, req.Query.Get("from"))
	to, err2 := time.Parse(time.DateOnly, req.Query.Get("to"))
	if err1 != nil || err2 != nil || to.Before(from) {
		writeJSON(w, http.StatusBadRequest, `{"message":"from and to must be dates"}`)
		return
	}
	var days []json.RawMessage
	for _, d := range Days() {
		var obj struct {
			Day string `json:"day"`
		}
		if json.Unmarshal(d, &obj) != nil {
			continue
		}
		day, err := time.Parse(time.DateOnly, obj.Day)
		if err != nil || day.Before(from) || day.After(to) {
			continue
		}
		days = append(days, d)
	}
	b, err := json.Marshal(map[string]any{"days": days})
	if err != nil {
		s.t.Errorf("eightsleepfake: %v", err)
	}
	writeJSON(w, http.StatusOK, string(b))
}

func (s *Server) status(w http.ResponseWriter, code int) {
	if code == http.StatusTooManyRequests && s.RetryAfter != "" {
		w.Header().Set("Retry-After", s.RetryAfter)
	}
	writeJSON(w, code, fmt.Sprintf(`{"status":%d}`, code))
}

func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}
