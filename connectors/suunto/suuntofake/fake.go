// SPDX-License-Identifier: AGPL-3.0-only

// Package suuntofake is an httptest stand-in for the Suunto Cloud API and its
// OAuth endpoints, for the connector's tests and for tests elsewhere that need
// Suunto lines. It answers from invented fixtures (testdata/: round values,
// fake keys) and keeps the rules the connector depends on:
//
//   - the token endpoint wants the client credentials in HTTP Basic and
//     answers an access token that is a JWT with a "user" claim;
//   - every API request needs a bearer token and the subscription key in
//     Ocp-Apim-Subscription-Key; a missing or wrong key is the gateway's 401
//     naming the subscription key;
//   - GET /v3/workouts filters on since and until (epoch ms, both inclusive;
//     the fake filters on the workout start), sorts by start, and pages with
//     limit and offset inside {"error": null, "payload": [...]};
//   - the 24/7 endpoints answer their fixture whatever the window, and
//     record the window they were asked for.
//
// The 24/7 fixtures have an invented shape: Suunto does not publish them.
package suuntofake

import (
	"embed"
	"encoding/base64"
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
)

//go:embed testdata/*.json
var files embed.FS

// Default credentials the fake accepts.
const (
	ClientID        = "test-client-id"
	ClientSecret    = "test-client-secret"
	SubscriptionKey = "test-subscription-key"
	RefreshToken    = "fake-refresh-token"
	Username        = "fake-suunto-user"
)

// Request is one request the fake received.
type Request struct {
	Method          string
	Path            string
	Auth            string
	SubscriptionKey string
	Form            url.Values
	Query           url.Values
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
	// TokenUser is the "user" claim of minted access tokens (Username when
	// empty).
	TokenUser string
	// DeauthStatus is the HTTP status of GET /oauth/deauthorize (200 when
	// zero).
	DeauthStatus int
	// DataHTTP, when set, is the HTTP status every API request answers,
	// with DataBody (a token error by default).
	DataHTTP int
	DataBody string
	// Status247, when set for a path, is the HTTP status that 24/7 path
	// answers.
	Status247 map[string]int
	// Workouts, when set, replaces the fixture list (raw JSON objects).
	Workouts []json.RawMessage
	// RateLimit, when it returns true for a path, answers that request with
	// HTTP 429 and RateLimitHeaders.
	RateLimit        func(path string) bool
	RateLimitHeaders http.Header
	// Quota, when it returns true for a path, answers the gateway's 403
	// "Out of call volume quota" with QuotaBody.
	Quota     func(path string) bool
	QuotaBody string
}

// New starts a fake, closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL of the fake, for both the OAuth and the API base.
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

// Workouts returns the fixture workouts, one raw JSON object each.
func Workouts() []json.RawMessage {
	var all []json.RawMessage
	if err := json.Unmarshal(File("workouts.json"), &all); err != nil {
		panic(err)
	}
	return all
}

// JWT returns an unsigned-looking JWT (the signature part is filler) whose
// payload is claims.
func JWT(claims map[string]any) string {
	enc := base64.RawURLEncoding
	h := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	p, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	return h + "." + enc.EncodeToString(p) + "." + enc.EncodeToString([]byte("fake-signature"))
}

// Token returns the access token the fake mints for user.
func Token(user string) string {
	return JWT(map[string]any{"user": user, "sub": "fake-subject", "exp": 1772668800, "authorities": []string{"ROLE_USER"}})
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"),
		SubscriptionKey: r.Header.Get("Ocp-Apim-Subscription-Key"), Form: form, Query: r.URL.Query(),
	})
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	write := func(b []byte) { _, _ = w.Write(b) }

	if s.RateLimit != nil && s.RateLimit(r.URL.Path) {
		for k, v := range s.RateLimitHeaders {
			w.Header()[k] = v
		}
		w.WriteHeader(http.StatusTooManyRequests)
		write([]byte(`{"statusCode":429,"message":"Rate limit is exceeded. Try again in 30 seconds."}`))
		return
	}
	if s.Quota != nil && s.Quota(r.URL.Path) {
		w.WriteHeader(http.StatusForbidden)
		b := s.QuotaBody
		if b == "" {
			b = `{"statusCode":403,"message":"Out of call volume quota. Quota will be replenished in 02:15:30."}`
		}
		write([]byte(b))
		return
	}

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/token":
		s.token(w, r, form)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/oauth/deauthorize":
		if s.DeauthStatus != 0 {
			w.WriteHeader(s.DeauthStatus)
		}
		return
	}

	if key := r.Header.Get("Ocp-Apim-Subscription-Key"); key != SubscriptionKey {
		w.WriteHeader(http.StatusUnauthorized)
		write([]byte(`{"statusCode":401,"message":"Access denied due to invalid subscription key. Make sure to provide a valid key for an active subscription."}`))
		return
	}
	if r.Header.Get("Authorization") == "" {
		w.WriteHeader(http.StatusUnauthorized)
		write([]byte(`{"error":"unauthorized","error_description":"Full authentication is required to access this resource"}`))
		return
	}
	if s.DataHTTP != 0 {
		w.WriteHeader(s.DataHTTP)
		b := s.DataBody
		if b == "" {
			b = `{"error":"invalid_token","error_description":"Access token expired"}`
		}
		write([]byte(b))
		return
	}
	if st := s.Status247[r.URL.Path]; st != 0 {
		w.WriteHeader(st)
		write([]byte(`{"error":"not available"}`))
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v3/workouts":
		write(s.workouts(r.URL.Query()))
	case r.Method == http.MethodGet && r.URL.Path == "/247samples/sleep":
		write(File("sleep.json"))
	case r.Method == http.MethodGet && r.URL.Path == "/247samples/recovery":
		write(File("recovery.json"))
	case r.Method == http.MethodGet && r.URL.Path == "/247/daily-activity-statistics":
		write(File("daily_activity.json"))
	default:
		w.WriteHeader(http.StatusNotFound)
		write([]byte(`{"statusCode":404,"message":"Resource not found"}`))
	}
}

// token answers POST /oauth/token: client credentials in HTTP Basic, then
// grant_type authorization_code (with code) or refresh_token.
func (s *Server) token(w http.ResponseWriter, r *http.Request, form url.Values) {
	if s.TokenStatus != 0 {
		w.WriteHeader(s.TokenStatus)
		if s.TokenBody != "" {
			_, _ = w.Write([]byte(s.TokenBody))
		}
		return
	}
	if s.TokenBody != "" {
		_, _ = w.Write([]byte(s.TokenBody))
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok || id != ClientID || secret != ClientSecret {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized","error_description":"Bad client credentials"}`))
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		if form.Get("code") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
			return
		}
	case "refresh_token":
		if form.Get("refresh_token") != RefreshToken {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid refresh token"}`))
			return
		}
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unsupported_grant_type"}`))
		return
	}
	user := s.TokenUser
	if user == "" {
		user = Username
	}
	b, _ := json.Marshal(map[string]any{
		"access_token":  Token(user),
		"token_type":    "bearer",
		"refresh_token": RefreshToken,
		"expires_in":    86399,
		"scope":         "workout",
	})
	_, _ = w.Write(b)
}

// workouts answers GET /v3/workouts.
func (s *Server) workouts(q url.Values) []byte {
	all := s.Workouts
	if all == nil {
		all = Workouts()
	}
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	until, err := strconv.ParseInt(q.Get("until"), 10, 64)
	if err != nil {
		until = 1 << 62
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit < 1 {
		limit = 50
	}
	type item struct {
		at  int64
		raw json.RawMessage
	}
	var kept []item
	for _, w := range all {
		var head struct {
			StartTime int64 `json:"startTime"`
		}
		if json.Unmarshal(w, &head) != nil {
			continue
		}
		if head.StartTime >= since && head.StartTime <= until {
			kept = append(kept, item{at: head.StartTime, raw: w})
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].at < kept[j].at })
	page := []json.RawMessage{}
	for i := offset; i < len(kept) && i < offset+limit; i++ {
		page = append(page, kept[i].raw)
	}
	b, err := json.Marshal(map[string]any{"error": nil, "payload": page, "metadata": map[string]any{"workoutcount": strconv.Itoa(len(page))}})
	if err != nil {
		s.t.Errorf("suuntofake: %v", err)
	}
	return b
}
