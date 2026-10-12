// SPDX-License-Identifier: AGPL-3.0-only

// Package whoopfake is an httptest stand-in for the WHOOP API v2, for the
// connector's tests and for tests elsewhere that need WHOOP lines (pull
// through the connector, then process). It answers from fixtures with
// invented values (testdata/: user 10001, offset +01:00, fake ids) shaped
// like the documented v2 responses, and keeps the rules of the real API that
// the connector depends on:
//
//   - collections filtered on start (inclusive) and end (exclusive), a record
//     that spans start or end included, as WHOOP documents (a recovery, which
//     has no span, by created_at), paginated with nextToken / next_token,
//     at most 25 records a page (a larger limit is refused with 400), and
//     next_token absent on the last page;
//   - a cycle, a sleep and a workout readable by id, the sleep of a cycle;
//   - Bearer authentication, the OAuth consent redirect and token endpoint,
//     and DELETE /v2/user/access.
//
// With Strict set it also keeps the OAuth state of the real server: a code is
// good once, a refresh rotates the refresh token and retires every access
// token issued before it, and a data request needs a live access token.
package whoopfake

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

// UserID is the WHOOP user id of the fixtures.
const UserID = "10001"

// Request is one request the fake received.
type Request struct {
	Method string
	Path   string
	Auth   string
	Form   url.Values
	Query  url.Values
	At     time.Time
}

// Fault is an answer Fail asks the fake to give instead of the normal one.
type Fault struct {
	Status int
	Header map[string]string
	Body   string
}

// Server is the fake. Set the knobs before the first request; Records may
// also be changed later with SetRecords.
type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu       sync.Mutex
	requests []Request
	live     map[string]string

	// PageSize is how many records a collection page holds (default 2, so
	// the fixtures span several pages).
	PageSize int
	// TokenStatus and TokenBody override the token endpoint answer.
	TokenStatus int
	TokenBody   string
	// ProfileStatus, when set, is the HTTP status of the profile endpoint.
	ProfileStatus int
	// DataHTTP, when set, is the HTTP status every data endpoint answers.
	DataHTTP int
	// Records, when set for a path, replaces that collection's fixture.
	Records map[string]string
	// RateLimit, when it returns true for a path, answers that request with
	// HTTP 429, RetryAfter as Retry-After and RateLimitReset as
	// X-RateLimit-Reset (each only when set).
	RateLimit      func(path string) bool
	RetryAfter     string
	RateLimitReset string
	// Fail, when set, is asked before each request to a data endpoint (n
	// counts the requests to that path, from 1); a non-nil Fault is the
	// answer.
	Fail func(path string, n int) *Fault
	// Page, when set, may answer a collection request itself (ok true).
	Page func(path string, q url.Values) (body string, ok bool)

	// Strict turns on the OAuth state of the real server (see the package
	// comment). ClientID and ClientSecret, when set, are the only client
	// credentials the token endpoint accepts (else invalid_client).
	Strict       bool
	ClientID     string
	ClientSecret string
	// OmitRefreshToken answers the token endpoint without a refresh token,
	// as WHOOP does when the offline scope was not granted.
	OmitRefreshToken bool

	issued    int
	codes     map[string]bool // code -> still good
	access    map[string]bool // access token -> live
	refresh   map[string]bool // refresh token -> live
	pathCount map[string]int
	revoked   int
}

// New starts a fake, closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		t: t, PageSize: 2,
		codes: map[string]bool{}, access: map[string]bool{}, refresh: map[string]bool{},
		pathCount: map[string]int{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL of the fake, for both the auth and the API base.
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

// SetRecords replaces the records of the collection at path (a JSON object
// with a "records" array), safely while the fake serves.
func (s *Server) SetRecords(p, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live == nil {
		s.live = map[string]string{}
	}
	s.live[p] = body
}

// Revocations counts the DELETE /v2/user/access requests answered 204.
func (s *Server) Revocations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revoked
}

// ExpireAccessTokens retires every access token issued so far (Strict), as
// their one hour lifetime would.
func (s *Server) ExpireAccessTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.access {
		s.access[k] = false
	}
}

// IssueTokens returns a fresh live access and refresh token pair (Strict),
// for tests that start from a connected user.
func (s *Server) IssueTokens() (access, refresh string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issueLocked()
}

func (s *Server) issueLocked() (string, string) {
	s.issued++
	a := fmt.Sprintf("fake-access-token-%d", s.issued)
	r := fmt.Sprintf("fake-refresh-token-%d", s.issued)
	s.access[a] = true
	s.refresh[r] = true
	return a, r
}

// File returns one fixture.
func File(name string) []byte {
	b, err := files.ReadFile(path.Join("testdata", name))
	if err != nil {
		panic(err)
	}
	return b
}

// collections maps each collection path to its fixture and the fields its
// window filter reads: the record's span, or one time for a recovery.
var collections = map[string]struct{ file, startField, endField string }{
	"/v2/cycle":            {"cycles.json", "start", "end"},
	"/v2/recovery":         {"recoveries.json", "created_at", ""},
	"/v2/activity/sleep":   {"sleeps.json", "start", "end"},
	"/v2/activity/workout": {"workouts.json", "start", "end"},
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Form: form, Query: r.URL.Query(), At: time.Now()})
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	write := func(b []byte) { _, _ = w.Write(b) }

	if s.RateLimit != nil && s.RateLimit(r.URL.Path) {
		if s.RetryAfter != "" {
			w.Header().Set("Retry-After", s.RetryAfter)
		}
		if s.RateLimitReset != "" {
			w.Header().Set("X-RateLimit-Reset", s.RateLimitReset)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}

	switch r.URL.Path {
	case "/oauth2/auth":
		s.consent(w, r)
		return
	case "/oauth2/token":
		s.token(w, r, form)
		return
	}

	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !s.liveAccess(token) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if s.Fail != nil {
		s.mu.Lock()
		s.pathCount[r.URL.Path]++
		n := s.pathCount[r.URL.Path]
		s.mu.Unlock()
		if f := s.Fail(r.URL.Path, n); f != nil {
			for k, v := range f.Header {
				w.Header().Set(k, v)
			}
			w.WriteHeader(f.Status)
			write([]byte(f.Body))
			return
		}
	}
	if r.URL.Path == "/v2/user/access" {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.mu.Lock()
		s.revoked++
		for k := range s.access {
			s.access[k] = false
		}
		for k := range s.refresh {
			s.refresh[k] = false
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/v2/user/profile/basic" {
		if s.ProfileStatus != 0 {
			w.WriteHeader(s.ProfileStatus)
			return
		}
		write(File("profile.json"))
		return
	}
	if s.DataHTTP != 0 {
		w.WriteHeader(s.DataHTTP)
		write([]byte(`{}`))
		return
	}

	if c, ok := collections[r.URL.Path]; ok {
		if s.Page != nil {
			if b, ok := s.Page(r.URL.Path, r.URL.Query()); ok {
				write([]byte(b))
				return
			}
		}
		if limit, _ := strconv.Atoi(r.URL.Query().Get("limit")); limit > 25 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		write(s.page(r.URL.Path, c.file, c.startField, c.endField, r.URL.Query()))
		return
	}
	byID := func(collection, file, id string) {
		for _, rec := range s.records(collection, file) {
			if idOf(rec["id"]) == id {
				b, err := json.Marshal(rec)
				s.must(err)
				write(b)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}
	switch p := r.URL.Path; {
	case strings.HasPrefix(p, "/v2/cycle/") && strings.HasSuffix(p, "/sleep"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/v2/cycle/"), "/sleep")
		for _, rec := range s.records("/v2/activity/sleep", "sleeps.json") {
			if idOf(rec["cycle_id"]) == id && rec["nap"] != true {
				b, err := json.Marshal(rec)
				s.must(err)
				write(b)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	case strings.HasPrefix(p, "/v2/cycle/"):
		byID("/v2/cycle", "cycles.json", strings.TrimPrefix(p, "/v2/cycle/"))
	case strings.HasPrefix(p, "/v2/activity/sleep/"):
		byID("/v2/activity/sleep", "sleeps.json", strings.TrimPrefix(p, "/v2/activity/sleep/"))
	case strings.HasPrefix(p, "/v2/activity/workout/"):
		byID("/v2/activity/workout", "workouts.json", strings.TrimPrefix(p, "/v2/activity/workout/"))
	case p == "/v2/user/measurement/body":
		write(File("body.json"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// consent answers the consent page at once: it redirects to redirect_uri with
// a new code and the state, as a user who accepts would be.
func (s *Server) consent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || back.Scheme == "" || q.Get("response_type") != "code" || q.Get("client_id") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.issued++
	code := fmt.Sprintf("fake-code-%d", s.issued)
	s.codes[code] = true
	s.mu.Unlock()
	v := back.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	v.Set("scope", q.Get("scope"))
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

// token answers the token endpoint.
func (s *Server) token(w http.ResponseWriter, r *http.Request, form url.Values) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.TokenStatus != 0 {
		w.WriteHeader(s.TokenStatus)
	}
	if s.TokenBody != "" {
		_, _ = w.Write([]byte(s.TokenBody))
		return
	}
	if s.TokenStatus != 0 {
		return
	}
	if !s.Strict {
		_, _ = w.Write(File("token.json"))
		return
	}
	oauthErr := func(status int, code string) {
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"error":%q}`, code)
	}
	if (s.ClientID != "" && form.Get("client_id") != s.ClientID) ||
		(s.ClientSecret != "" && form.Get("client_secret") != s.ClientSecret) {
		oauthErr(http.StatusUnauthorized, "invalid_client")
		return
	}
	s.mu.Lock()
	switch form.Get("grant_type") {
	case "authorization_code":
		if !s.codes[form.Get("code")] {
			s.mu.Unlock()
			oauthErr(http.StatusBadRequest, "invalid_grant")
			return
		}
		s.codes[form.Get("code")] = false
	case "refresh_token":
		if !s.refresh[form.Get("refresh_token")] {
			s.mu.Unlock()
			oauthErr(http.StatusBadRequest, "invalid_grant")
			return
		}
		// Rotation: the refresh token is spent, the access tokens retired.
		s.refresh[form.Get("refresh_token")] = false
		for k := range s.access {
			s.access[k] = false
		}
	default:
		s.mu.Unlock()
		oauthErr(http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	a, rt := s.issueLocked()
	s.mu.Unlock()
	answer := map[string]any{
		"access_token": a, "expires_in": 3600, "token_type": "bearer",
		"scope": "read:recovery read:cycles read:sleep read:workout read:profile read:body_measurement offline",
	}
	if !s.OmitRefreshToken {
		answer["refresh_token"] = rt
	}
	b, err := json.Marshal(answer)
	s.must(err)
	_, _ = w.Write(b)
}

// liveAccess reports whether an access token is accepted: any non-empty one,
// or with Strict one issued and not retired.
func (s *Server) liveAccess(token string) bool {
	if token == "" {
		return false
	}
	if !s.Strict {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.access[token]
}

// records reads a collection fixture (or its override).
func (s *Server) records(p, file string) []map[string]any {
	raw := File(file)
	s.mu.Lock()
	if o, ok := s.live[p]; ok {
		raw = []byte(o)
	} else if o, ok := s.Records[p]; ok {
		raw = []byte(o)
	}
	s.mu.Unlock()
	var all struct {
		Records []map[string]any `json:"records"`
	}
	s.must(json.Unmarshal(raw, &all))
	return all.Records
}

// page answers one collection request: the records in the window,
// PageSize (at most limit) at a time, the offset of the next page as
// next_token, absent on the last page.
//
// A record with a span is in the window when it started before end and ended
// at or after start (an open cycle has not ended); one with a single time
// when that time lies in [start, end).
func (s *Server) page(p, file, startField, endField string, q url.Values) []byte {
	start, _ := time.Parse(time.RFC3339Nano, q.Get("start"))
	end, err := time.Parse(time.RFC3339Nano, q.Get("end"))
	if err != nil {
		end = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	var kept []map[string]any
	for _, rec := range s.records(p, file) {
		ts, _ := rec[startField].(string)
		from, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil || !from.Before(end) {
			continue
		}
		to := from
		if endField != "" {
			to = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
			if es, ok := rec[endField].(string); ok {
				if t, err := time.Parse(time.RFC3339Nano, es); err == nil {
					to = t
				}
			}
		}
		if to.Before(start) {
			continue
		}
		kept = append(kept, rec)
	}
	// WHOOP sorts by start time (a recovery: created_at here), newest first.
	sort.SliceStable(kept, func(i, j int) bool {
		a, _ := kept[i][startField].(string)
		b, _ := kept[j][startField].(string)
		ta, _ := time.Parse(time.RFC3339Nano, a)
		tb, _ := time.Parse(time.RFC3339Nano, b)
		return ta.After(tb)
	})
	size := s.PageSize
	if limit, err := strconv.Atoi(q.Get("limit")); err == nil && limit > 0 && limit < size {
		size = limit
	}
	offset, _ := strconv.Atoi(q.Get("nextToken"))
	if offset > len(kept) || offset < 0 {
		offset = len(kept)
	}
	last := min(offset+size, len(kept))
	out := map[string]any{"records": append([]map[string]any{}, kept[offset:last]...)}
	if last < len(kept) {
		out["next_token"] = strconv.Itoa(last)
	}
	b, err := json.Marshal(out)
	s.must(err)
	return b
}

// idOf renders a decoded JSON id (number or string).
func idOf(v any) string {
	switch id := v.(type) {
	case float64:
		return strconv.FormatInt(int64(id), 10)
	case string:
		return id
	}
	return ""
}

func (s *Server) must(err error) {
	if err != nil {
		s.t.Errorf("whoopfake: %v", err)
	}
}
