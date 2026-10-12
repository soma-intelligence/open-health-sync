// SPDX-License-Identifier: AGPL-3.0-only

// Package ultrahumanfake is an httptest stand-in for the Ultrahuman
// Partnership API (token, revoke, metrics and user_info endpoints), for the
// connector's tests. It answers from invented responses (testdata/: round
// values, a fake user at UTC+01:00) shaped like the published third-party
// fixtures, and keeps the documented rules the connector depends on: client
// credentials in the token form, bearer tokens on the data endpoints, one
// date per metrics call.
package ultrahumanfake

import (
	"embed"
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
	Form   url.Values
}

// Server is the fake. Set the knobs before the first request.
type Server struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []Request

	// ClientID and ClientSecret are the credentials the token and revoke
	// endpoints accept (any when empty).
	ClientID, ClientSecret string
	// AccessToken is the bearer the data endpoints accept (any when empty).
	AccessToken string
	// TokenStatus and TokenBody override the token endpoint answer.
	TokenStatus int
	TokenBody   string
	// UserInfoStatus, when set, is the answer of user_info.
	UserInfoStatus int
	// MetricsStatus, when it returns non-zero for a date, is the HTTP status
	// of that date's metrics call (with RetryAfter on a 429).
	MetricsStatus func(date string) int
	RetryAfter    string
	// Days maps a date to the testdata file it answers; a date absent from
	// it answers an empty metric list.
	Days map[string]string
}

// New starts a fake, closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{Days: map[string]string{
		"2026-03-08": "metrics_2026-03-08.json",
		"2026-03-09": "metrics_2026-03-09.json",
	}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL of the fake, for the auth and API bases.
func (s *Server) URL() string { return s.srv.URL }

// Client is an HTTP client for the fake.
func (s *Server) Client() *http.Client { return s.srv.Client() }

// Requests returns every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Calls returns the requests to path p.
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
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Query: r.URL.Query(), Form: form})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/partners/oauth/token":
		s.token(w, form)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/api/partners/oauth/revoke":
		if form.Get("token") == "" || !s.clientOK(form) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
		return
	}

	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || (s.AccessToken != "" && auth != "Bearer "+s.AccessToken) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/partners/v1/user_data/user_info":
		if s.UserInfoStatus != 0 {
			w.WriteHeader(s.UserInfoStatus)
			return
		}
		_, _ = w.Write(File("user_info.json"))
	case r.Method == http.MethodGet && r.URL.Path == "/api/partners/v1/user_data/metrics":
		date := r.URL.Query().Get("date")
		if date == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if s.MetricsStatus != nil {
			if st := s.MetricsStatus(date); st != 0 {
				if st == http.StatusTooManyRequests && s.RetryAfter != "" {
					w.Header().Set("Retry-After", s.RetryAfter)
				}
				w.WriteHeader(st)
				return
			}
		}
		name, ok := s.Days[date]
		if !ok {
			name = "metrics_empty.json"
		}
		_, _ = w.Write(File(name))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) clientOK(form url.Values) bool {
	return (s.ClientID == "" || form.Get("client_id") == s.ClientID) &&
		(s.ClientSecret == "" || form.Get("client_secret") == s.ClientSecret)
}

func (s *Server) token(w http.ResponseWriter, form url.Values) {
	if s.TokenStatus != 0 || s.TokenBody != "" {
		if s.TokenStatus != 0 {
			w.WriteHeader(s.TokenStatus)
		}
		_, _ = w.Write([]byte(s.TokenBody))
		return
	}
	if !s.clientOK(form) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"Client authentication failed."}`))
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		if form.Get("code") == "" || form.Get("redirect_uri") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid authorization code."}`))
			return
		}
		_, _ = w.Write(File("token.json"))
	case "refresh_token":
		if form.Get("refresh_token") == "" || form.Get("refresh_token") == "spent" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid refresh token."}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"fake-access-token-2","token_type":"Bearer","expires_in":86400,"refresh_token":"fake-refresh-token-2","scope":"profile ring_data cgm_data","created_at":1773100800}`))
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unsupported_grant_type"}`))
	}
}
