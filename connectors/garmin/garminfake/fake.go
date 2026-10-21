// SPDX-License-Identifier: AGPL-3.0-only

// Package garminfake is an httptest stand-in for Garmin's OAuth 2.0 token
// endpoint and the Health API (wellness-api), for the connector's tests and
// for tests elsewhere that need Garmin lines. It answers from invented
// summaries (testdata/: round values, fake ids, a +01:00 user) and keeps the
// rules the connector depends on, as public sources describe them: PKCE
// (S256) checked at the token exchange, client credentials in the form,
// refresh tokens that rotate, bearer tokens on the Health API, summary
// endpoints filtered by an upload window of at most 86400 seconds (400
// otherwise). It was not checked against Garmin's own sandbox.
package garminfake

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// Paths served.
const (
	TokenPath        = "/di-oauth2-service/oauth/token"
	UserIDPath       = "/wellness-api/rest/user/id"
	RegistrationPath = "/wellness-api/rest/user/registration"
	SummaryPrefix    = "/wellness-api/rest/"
)

// UserID is the Garmin user id of the fake's user.
const UserID = "a1b2c3d4-0000-4000-8000-000000000001"

// Request is one request the fake received.
type Request struct {
	Method string
	Path   string
	Auth   string
	Query  url.Values
	Form   url.Values
}

// Server is the fake. Set the knobs before the first request.
type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu        sync.Mutex
	requests  []Request
	refreshes int

	// ClientID and ClientSecret are the credentials the token endpoint
	// accepts (any when empty).
	ClientID, ClientSecret string
	// Challenge, when set, is the S256 code challenge the authorization
	// code was issued for: the exchange must send its verifier.
	Challenge string
	// Code is the authorization code the token endpoint accepts (any when
	// empty).
	Code string
	// TokenStatus and TokenBody override the token endpoint answer.
	TokenStatus int
	TokenBody   string
	// AccessToken is the bearer token the Health API accepts (any when
	// empty).
	AccessToken string
	// NotGranted answers these summary types with this status (400 or 403).
	NotGranted map[string]int
	// DataHTTP, when set, is the status every summary endpoint answers.
	DataHTTP int
	// RateLimit, when it returns true for a path, answers 429 with
	// RetryAfter.
	RateLimit  func(path string) bool
	RetryAfter string
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

// Summaries returns the items of testdata/<dataType>.json.
func Summaries(dataType string) []json.RawMessage {
	var items []json.RawMessage
	if err := json.Unmarshal(File(dataType+".json"), &items); err != nil {
		panic(err)
	}
	return items
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Query: r.URL.Query(), Form: form})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	if s.RateLimit != nil && s.RateLimit(r.URL.Path) {
		if s.RetryAfter != "" {
			w.Header().Set("Retry-After", s.RetryAfter)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if r.URL.Path == TokenPath {
		s.token(w, r, form)
		return
	}
	if s.AccessToken != "" && r.Header.Get("Authorization") != "Bearer "+s.AccessToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == UserIDPath && r.Method == http.MethodGet:
		_, _ = fmt.Fprintf(w, `{"userId":%q}`, UserID)
	case r.URL.Path == RegistrationPath && r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(r.URL.Path, SummaryPrefix) && r.Method == http.MethodGet:
		s.summaries(w, r, strings.TrimPrefix(r.URL.Path, SummaryPrefix))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request, form url.Values) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.TokenStatus != 0 {
		w.WriteHeader(s.TokenStatus)
		_, _ = io.WriteString(w, s.TokenBody)
		return
	}
	if (s.ClientID != "" && form.Get("client_id") != s.ClientID) || (s.ClientSecret != "" && form.Get("client_secret") != s.ClientSecret) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		if s.Code != "" && form.Get("code") != s.Code {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		v := form.Get("code_verifier")
		if len(v) < 43 || len(v) > 128 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_request","error_description":"code_verifier"}`)
			return
		}
		if s.Challenge != "" {
			sum := sha256.Sum256([]byte(v))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != s.Challenge {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"PKCE verification failed"}`)
				return
			}
		}
		_, _ = w.Write(File("token.json"))
	case "refresh_token":
		if form.Get("refresh_token") == "" || strings.HasPrefix(form.Get("refresh_token"), "revoked") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		s.mu.Lock()
		s.refreshes++
		n := s.refreshes + 1
		s.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"access_token":"fake-access-token-%d","token_type":"bearer","expires_in":86400,"refresh_token":"fake-refresh-token-%d","refresh_token_expires_in":7775998}`, n, n)
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"unsupported_grant_type"}`)
	}
}

// summaries answers one summary endpoint: the testdata items whose upload
// time (their start or measurement time plus duration, or their calendar
// date's end) lies in [uploadStart, uploadEnd).
func (s *Server) summaries(w http.ResponseWriter, r *http.Request, dataType string) {
	if code, ok := s.NotGranted[dataType]; ok {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, `{"errorMessage":"no permission"}`)
		return
	}
	if s.DataHTTP != 0 {
		w.WriteHeader(s.DataHTTP)
		return
	}
	if _, err := files.ReadFile(path.Join("testdata", dataType+".json")); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	from, err1 := strconv.ParseInt(q.Get("uploadStartTimeInSeconds"), 10, 64)
	to, err2 := strconv.ParseInt(q.Get("uploadEndTimeInSeconds"), 10, 64)
	if err1 != nil || err2 != nil || to <= from || to-from > 86400 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"errorMessage":"invalid upload window"}`)
		return
	}
	out := []json.RawMessage{}
	for _, it := range Summaries(dataType) {
		up := uploadTime(it)
		if up >= from && up < to {
			out = append(out, it)
		}
	}
	b, _ := json.Marshal(out)
	_, _ = w.Write(b)
}

// uploadTime is when the fake says a summary was uploaded.
func uploadTime(it json.RawMessage) int64 {
	var h struct {
		Start    *int64 `json:"startTimeInSeconds"`
		Measured *int64 `json:"measurementTimeInSeconds"`
		Duration int64  `json:"durationInSeconds"`
		Date     string `json:"calendarDate"`
	}
	_ = json.Unmarshal(it, &h)
	switch {
	case h.Start != nil:
		return *h.Start + h.Duration
	case h.Measured != nil:
		return *h.Measured + 60
	default:
		d, _ := time.Parse("2006-01-02", h.Date)
		return d.Add(23 * time.Hour).Unix()
	}
}
