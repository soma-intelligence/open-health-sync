// SPDX-License-Identifier: AGPL-3.0-only

// Package garminconnectfake is an httptest stand-in for the Garmin Connect
// account sign-in and the Connect service endpoints, for the connector's
// tests and for tests elsewhere that need Garmin Connect lines. It answers
// from invented fixtures (testdata/: round values, fake ids and a fake
// bearer) and keeps the rules the connector depends on, as
// python-garminconnect documents them: a JSON mobile login that answers a
// service ticket, a ticket exchange for a bearer, and bearer-authenticated
// Connect reads. It was not checked against Garmin's own servers.
package garminconnectfake

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Paths served.
const (
	LoginPath = "/mobile/api/login"
	TokenPath = "/di-oauth2-service/oauth/token"
)

// Account the login accepts by default.
const (
	Email    = "runner@example.org"
	Password = "correct horse battery staple"
)

// SampleDate is the calendar date the fixtures describe.
const SampleDate = "2026-04-20"

// Subject is the "sub" claim of the fake bearer: the account's stable id.
const Subject = "fake-garmin-guid-0001"

// fakeBearer is a JWT (header.payload.signature) whose payload names Subject.
// It is not signed by anything; the connector reads the subject claim without
// verifying it.
var fakeBearer = buildJWT(Subject)

// Request is one request the fake received.
type Request struct {
	Method string
	Path   string
	Auth   string
}

// Server is the fake. Set the knobs before the first request.
type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu       sync.Mutex
	requests []Request
	signIns  int

	// Email and Password are the account the login accepts (constants when
	// empty).
	Email, Password string
	// LoginStatus, when set, is the HTTP status the login answers.
	LoginStatus int
	// LoginResponseType, when set, is the responseStatus.type the login
	// answers (for example "MFA_REQUIRED").
	LoginResponseType string
	// TokenStatus, when set, is the HTTP status the token endpoint answers.
	TokenStatus int
	// APIStatus, when set, is the HTTP status every Connect endpoint answers.
	APIStatus int
	// RetryAfter is the Retry-After header of a 429 answer.
	RetryAfter string
}

// New starts a fake and registers its cleanup.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL of the fake.
func (s *Server) URL() string { return s.srv.URL }

// TokenURL is the full DI token endpoint URL of the fake.
func (s *Server) TokenURL() string { return s.srv.URL + TokenPath }

// Client is an HTTP client wired to the fake.
func (s *Server) Client() *http.Client { return s.srv.Client() }

// Bearer is the access token the Connect endpoints accept.
func (s *Server) Bearer() string { return fakeBearer }

// SignIns is how many logins the fake accepted.
func (s *Server) SignIns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signIns
}

// Requests returns the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

func (s *Server) email() string {
	if s.Email != "" {
		return s.Email
	}
	return Email
}

func (s *Server) password() string {
	if s.Password != "" {
		return s.Password
	}
	return Password
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization")})
	s.mu.Unlock()

	switch {
	case r.Method == http.MethodPost && r.URL.Path == LoginPath:
		s.handleLogin(w, r)
	case r.Method == http.MethodPost && r.URL.Path == TokenPath:
		s.handleToken(w, r)
	case r.Method == http.MethodGet:
		s.handleAPI(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.LoginStatus != 0 {
		s.writeStatus(w, s.LoginStatus)
		return
	}
	var body struct {
		Username, Password string
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if s.LoginResponseType != "" {
		writeJSON(w, http.StatusOK, map[string]any{"responseStatus": map[string]string{"type": s.LoginResponseType}})
		return
	}
	if body.Username != s.email() || body.Password != s.password() {
		writeJSON(w, http.StatusOK, map[string]any{"responseStatus": map[string]string{"type": "INVALID_USERNAME_PASSWORD"}})
		return
	}
	s.mu.Lock()
	s.signIns++
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"serviceTicketId": "ST-0000-fake",
		"responseStatus":  map[string]string{"type": "SUCCESSFUL"},
	})
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if s.TokenStatus != 0 {
		s.writeStatus(w, s.TokenStatus)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  fakeBearer,
		"refresh_token": "rt-0000-fake",
		"expires_in":    3600,
	})
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	if s.APIStatus != 0 {
		s.writeStatus(w, s.APIStatus)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeBearer {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := r.URL.Path
	switch {
	case path == "/usersummary-service/v2/dailysummaries":
		_, _ = w.Write(fixture(s.t, "dailies.json"))
	case path == "/sleep-service/sleep/dailySleepData":
		if r.URL.Query().Get("date") == SampleDate {
			_, _ = w.Write(fixture(s.t, "sleep.json"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	case strings.HasPrefix(path, "/hrv-service/hrv/"):
		if strings.HasSuffix(path, SampleDate) {
			_, _ = w.Write(fixture(s.t, "hrv.json"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	case path == "/activitylist-service/activities/search/activities":
		if r.URL.Query().Get("start") == "0" {
			_, _ = w.Write(fixture(s.t, "activities.json"))
			return
		}
		_, _ = w.Write([]byte("[]"))
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) writeStatus(w http.ResponseWriter, status int) {
	if status == http.StatusTooManyRequests && s.RetryAfter != "" {
		w.Header().Set("Retry-After", s.RetryAfter)
	}
	w.WriteHeader(status)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fixture reads one testdata file.
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := files.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// Fixture exposes one testdata file to tests in other packages.
func Fixture(t testing.TB, name string) []byte { return fixture(t, name) }

// buildJWT makes an unsigned JWT whose payload carries the subject.
func buildJWT(sub string) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	header := enc(map[string]string{"alg": "none", "typ": "JWT"})
	payload := enc(map[string]string{"sub": sub})
	return fmt.Sprintf("%s.%s.%s", header, payload, base64.RawURLEncoding.EncodeToString([]byte("sig")))
}
