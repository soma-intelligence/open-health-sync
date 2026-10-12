// SPDX-License-Identifier: AGPL-3.0-only

// Package ourafake is an httptest stand-in for the Oura Cloud API v2, for the
// connector's tests and for tests elsewhere that need real Oura lines (pull
// through the connector, then process). It answers from invented responses
// (testdata/: round values, UTC+01:00, fake ids) shaped like the response
// schemas of Oura's OpenAPI document (the connector's contract tests check
// them), and keeps the rules of the real API that the connector depends on:
// the token endpoint, Bearer auth, start_date and end_date (inclusive)
// filtering on each document's "day", start_datetime and end_datetime on
// heartrate, and pagination through next_token. The vo2_max documents are
// served at vO2_max, Oura's spelling of that endpoint.
//
// By default any Bearer token is accepted and the token endpoint answers
// testdata/token.json. With Rotate set it behaves like Oura's OAuth server:
// each grant issues a new access and refresh token, a refresh token works
// once, and only issued, unexpired, unrevoked access tokens read data.
package ourafake

import (
	"embed"
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

//go:embed testdata/*.json
var files embed.FS

// collectionPath is the prefix of every data endpoint.
const collectionPath = "/v2/usercollection/"

// Request is one request the fake received.
type Request struct {
	Method string
	Path   string
	Auth   string
	Form   url.Values
	Query  url.Values
}

// Fault is an answer Intercept forces on a request.
type Fault struct {
	Status int
	Header http.Header
	Body   string
}

// Server is the fake. Set the knobs before the first request, or use the
// methods, which are safe while requests are served.
type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu       sync.Mutex
	requests []Request
	docs     map[string][]map[string]any // data type -> documents, overriding testdata
	// token state when Rotate is set
	seq      int
	access   map[string]bool // issued and still valid
	refresh  map[string]bool // issued and not spent yet
	revoked  bool
	userID   string
	codeUsed map[string]bool

	// PageSize is how many documents one page holds (2 by default), so the
	// fixtures span several pages.
	PageSize int
	// TokenStatus and TokenBody override the token endpoint answer.
	TokenStatus int
	TokenBody   string
	// Rotate turns on the OAuth server behaviour described in the package
	// comment.
	Rotate bool
	// ExpiresIn is the lifetime the token endpoint announces with Rotate
	// (one day when zero).
	ExpiresIn int
	// ExclusiveEndDate makes end_date exclusive. Oura does not document which
	// it is; the connector must miss no day either way.
	ExclusiveEndDate bool
	// DataHTTP, when set, is the HTTP status every data endpoint answers.
	DataHTTP int
	// Forbidden lists the data types that answer 403.
	Forbidden map[string]bool
	// Status answers the endpoints it names with that HTTP status.
	Status map[string]int
	// Bodies overrides the whole answer of a data type (no filtering, no
	// pagination).
	Bodies map[string]string
	// RateLimit, when it returns true for a path, answers that request with
	// HTTP 429 and RetryAfter as the Retry-After header.
	RateLimit  func(path string) bool
	RetryAfter string
	// Intercept, when it returns a Fault, answers the request with it.
	Intercept func(r *http.Request) *Fault
}

// New starts a fake, closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t, PageSize: 2, access: map[string]bool{}, refresh: map[string]bool{}, codeUsed: map[string]bool{}, userID: "fake-oura-user-0001"}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL of the fake, for both the account and the API base.
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

// SetDocuments replaces the documents a data type serves (its testdata file
// otherwise). They are filtered and paginated like the recorded ones. The
// data type is the connector's (vo2_max, not vO2_max).
func (s *Server) SetDocuments(dataType string, docs ...map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.docs == nil {
		s.docs = map[string][]map[string]any{}
	}
	s.docs[dataType] = docs
}

// ExpireAccessTokens makes every access token issued so far invalid (with
// Rotate): the next data request answers 401, a refresh still works.
func (s *Server) ExpireAccessTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access = map[string]bool{}
}

// RevokeConsent is the user removing the application in their Oura account
// (with Rotate): every token stops working and a refresh is invalid_grant.
func (s *Server) RevokeConsent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked = true
	s.access = map[string]bool{}
	s.refresh = map[string]bool{}
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
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Form: form, Query: r.URL.Query()})
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	write := func(b []byte) { _, _ = w.Write(b) }

	if s.Intercept != nil {
		if f := s.Intercept(r); f != nil {
			for k, vs := range f.Header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(f.Status)
			write([]byte(f.Body))
			return
		}
	}

	if s.RateLimit != nil && s.RateLimit(r.URL.Path) {
		if s.RetryAfter != "" {
			w.Header().Set("Retry-After", s.RetryAfter)
		}
		w.Header().Set("X-RateLimit-Tier", "token")
		w.WriteHeader(http.StatusTooManyRequests)
		write([]byte(`{"detail":"rate limit exceeded"}`))
		return
	}

	if r.URL.Path == "/oauth/token" {
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
		if s.Rotate {
			status, b := s.grant(form)
			if status != http.StatusOK {
				w.WriteHeader(status)
			}
			write(b)
			return
		}
		write(File("token.json"))
		return
	}

	if !strings.HasPrefix(r.URL.Path, collectionPath) || r.Method != http.MethodGet {
		w.WriteHeader(http.StatusNotFound)
		write([]byte(`{"detail":"not found"}`))
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || !s.validAccess(strings.TrimPrefix(auth, "Bearer ")) {
		w.WriteHeader(http.StatusUnauthorized)
		write([]byte(`{"detail":"invalid or expired token"}`))
		return
	}
	if s.DataHTTP != 0 {
		w.WriteHeader(s.DataHTTP)
		write([]byte(`{"detail":"error"}`))
		return
	}
	dataType := strings.TrimPrefix(r.URL.Path, collectionPath)
	if code, ok := s.Status[dataType]; ok {
		w.WriteHeader(code)
		write([]byte(`{"detail":"status"}`))
		return
	}
	if s.Forbidden[dataType] {
		w.WriteHeader(http.StatusForbidden)
		write([]byte(`{"detail":"forbidden"}`))
		return
	}
	if b, ok := s.Bodies[dataType]; ok {
		write([]byte(b))
		return
	}
	switch dataType {
	case "personal_info":
		if s.Rotate {
			b, _ := json.Marshal(map[string]any{"id": s.userID})
			write(b)
			return
		}
		write(File("personal_info.json"))
	case "daily_activity", "sleep", "daily_readiness", "daily_spo2", "workout", "heartrate",
		"daily_sleep", "daily_stress", "session", "vO2_max":
		b, err := s.list(fixtureName(dataType), r.URL.Query())
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			write([]byte(`{"detail":"` + err.Error() + `"}`))
			return
		}
		write(b)
	default:
		w.WriteHeader(http.StatusNotFound)
		write([]byte(`{"detail":"not found"}`))
	}
}

// validAccess reports whether a data request may use token: any token
// without Rotate, an issued and valid one with it.
func (s *Server) validAccess(token string) bool {
	if token == "" {
		return false
	}
	if !s.Rotate {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.access[token]
}

// grant answers the token endpoint with Rotate: an authorization code (each
// works once) or a refresh token (each works once) gives a new pair.
func (s *Server) grant(form url.Values) (int, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	oauthErr := func(status int, code string) (int, []byte) {
		b, _ := json.Marshal(map[string]string{"error": code})
		return status, b
	}
	if form.Get("client_id") == "" || form.Get("client_secret") == "" {
		return oauthErr(http.StatusUnauthorized, "invalid_client")
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		code := form.Get("code")
		if code == "" || s.codeUsed[code] {
			return oauthErr(http.StatusBadRequest, "invalid_grant")
		}
		s.codeUsed[code] = true
		s.revoked = false
	case "refresh_token":
		rt := form.Get("refresh_token")
		if s.revoked || !s.refresh[rt] {
			return oauthErr(http.StatusBadRequest, "invalid_grant")
		}
		delete(s.refresh, rt)
	default:
		return oauthErr(http.StatusBadRequest, "unsupported_grant_type")
	}
	s.seq++
	at, rt := fmt.Sprintf("fake-access-token-r%d", s.seq), fmt.Sprintf("fake-refresh-token-r%d", s.seq)
	s.access[at] = true
	s.refresh[rt] = true
	exp := s.ExpiresIn
	if exp == 0 {
		exp = 86400
	}
	b, _ := json.Marshal(map[string]any{
		"token_type": "bearer", "access_token": at, "refresh_token": rt, "expires_in": exp,
		"scope": "personal daily heartrate workout session spo2Daily",
	})
	return http.StatusOK, b
}

// fixtureName is the testdata file (without .json) of an endpoint: its path
// segment, except vO2_max, whose documents are filed under the data type the
// connector stores them as.
func fixtureName(endpoint string) string {
	if endpoint == "vO2_max" {
		return "vo2_max"
	}
	return endpoint
}

// documents returns the documents of a data type: the ones set with
// SetDocuments, else its testdata file.
func (s *Server) documents(dataType string) []map[string]any {
	s.mu.Lock()
	set, ok := s.docs[dataType]
	s.mu.Unlock()
	if ok {
		return set
	}
	var all struct {
		Data []map[string]any `json:"data"`
	}
	s.must(json.Unmarshal(File(dataType+".json"), &all))
	return all.Data
}

// list answers a list endpoint: the documents inside the asked window, one
// page of them, with next_token naming the offset of the next. A heartrate
// answer has no next_token on its last page; the others carry null.
func (s *Server) list(dataType string, q url.Values) ([]byte, error) {
	if err := checkWindow(dataType, q); err != nil {
		return nil, err
	}
	kept := []map[string]any{}
	for _, d := range s.documents(dataType) {
		if inWindow(dataType, d, q, s.ExclusiveEndDate) {
			kept = append(kept, d)
		}
	}
	offset, _ := strconv.Atoi(q.Get("next_token"))
	if offset > len(kept) || offset < 0 {
		offset = len(kept)
	}
	end := len(kept)
	if s.PageSize > 0 && offset+s.PageSize < end {
		end = offset + s.PageSize
	}
	out := map[string]any{"data": kept[offset:end], "next_token": nil}
	if end < len(kept) {
		out["next_token"] = strconv.Itoa(end)
	} else if dataType == "heartrate" {
		delete(out, "next_token")
	}
	b, err := json.Marshal(out)
	s.must(err)
	return b, nil
}

// checkWindow refuses a malformed window, as Oura answers 400 to one.
func checkWindow(dataType string, q url.Values) error {
	if dataType == "heartrate" {
		for _, k := range []string{"start_datetime", "end_datetime"} {
			if _, err := time.Parse(time.RFC3339, q.Get(k)); err != nil {
				return fmt.Errorf("%s is not a date-time", k)
			}
		}
		return nil
	}
	for _, k := range []string{"start_date", "end_date"} {
		if _, err := time.Parse(time.DateOnly, q.Get(k)); err != nil {
			return fmt.Errorf("%s is not a date", k)
		}
	}
	return nil
}

// inWindow reports whether document d answers the query: heartrate by
// timestamp in [start_datetime, end_datetime), the others by day in
// [start_date, end_date].
func inWindow(dataType string, d map[string]any, q url.Values, exclusiveEnd bool) bool {
	if dataType == "heartrate" {
		raw, _ := d["timestamp"].(string)
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return false
		}
		from, err1 := time.Parse(time.RFC3339, q.Get("start_datetime"))
		to, err2 := time.Parse(time.RFC3339, q.Get("end_datetime"))
		if err1 != nil || err2 != nil {
			return false
		}
		return !ts.Before(from) && ts.Before(to)
	}
	day, _ := d["day"].(string)
	if exclusiveEnd {
		return day >= q.Get("start_date") && day < q.Get("end_date")
	}
	return day >= q.Get("start_date") && day <= q.Get("end_date")
}

func (s *Server) must(err error) {
	if err != nil {
		s.t.Errorf("ourafake: %v", err)
	}
}
