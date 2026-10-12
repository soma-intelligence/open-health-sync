// SPDX-License-Identifier: AGPL-3.0-only

// Package fitbitfake is an httptest stand-in for the Google Health API v4,
// Google's OAuth token and revoke endpoints, and the webhook public keyset,
// for the Fitbit connector's tests. It answers from invented data points
// (testdata/<data type>.json: round values, fake ids, a +01:00 user) and keeps
// the rules the connector depends on: client credentials in the token form,
// bearer tokens on /v4, page sizes capped at 10,000 (25 for exercise and
// sleep) with nextPageToken paging, int64 values written as JSON strings,
// and Google's error body shape. Signer makes a P-256 key, its Tink JSON
// keyset and Tink-prefixed signatures of webhook bodies.
package fitbitfake

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
)

//go:embed testdata/*.json
var files embed.FS

// HealthUserID is the Google Health user id the identity endpoint answers.
const HealthUserID = "111222333444555666"

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

	mu       sync.Mutex
	requests []Request
	points   map[string][]json.RawMessage

	// ClientID and ClientSecret are the credentials the token endpoint
	// accepts.
	ClientID, ClientSecret string
	// Scope is the granted scope of token answers (space separated).
	Scope string
	// TokenStatus and TokenBody override the token endpoint answer.
	TokenStatus int
	TokenBody   string
	// NoRefreshToken leaves refresh_token out of token answers.
	NoRefreshToken bool
	// IdentityStatus and IdentityBody override the identity answer.
	IdentityStatus int
	IdentityBody   string
	// RevokeStatus overrides the revoke answer (200).
	RevokeStatus int
	// Forbidden data types answer 403 PERMISSION_DENIED.
	Forbidden map[string]bool
	// Status, when it returns non-zero for a data type, is that type's answer.
	Status func(dataType string) int
	// RetryAfter is the Retry-After header of a 429 or 504 Status answer.
	RetryAfter string
	// Keyset is served at /keyset.json; empty answers 404.
	Keyset []byte
}

// New starts a fake loaded with the testdata points, closed when the test
// ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t, points: map[string][]json.RawMessage{}, Forbidden: map[string]bool{}}
	entries, err := fs.ReadDir(files, "testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		dt := strings.TrimSuffix(e.Name(), ".json")
		var pts []json.RawMessage
		if err := json.Unmarshal(File(e.Name()), &pts); err != nil {
			t.Fatalf("fitbitfake: %s: %v", e.Name(), err)
		}
		s.points[dt] = pts
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the base URL of the fake.
func (s *Server) URL() string { return s.srv.URL }

// Client is an HTTP client for the fake.
func (s *Server) Client() *http.Client { return s.srv.Client() }

// SetPoints replaces the points of a data type.
func (s *Server) SetPoints(dataType string, pts []json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.points[dataType] = pts
}

// Requests returns every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Calls returns the requests to p (any method).
func (s *Server) Calls(p string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Path == p {
			out = append(out, r)
		}
	}
	return out
}

// File returns one testdata file.
func File(name string) []byte {
	b, err := files.ReadFile(path.Join("testdata", name))
	if err != nil {
		panic(err)
	}
	return b
}

func googleError(w http.ResponseWriter, code int, status, reason string) {
	w.WriteHeader(code)
	details := ""
	if reason != "" {
		details = fmt.Sprintf(`,"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":%q,"domain":"health.googleapis.com"}]`, reason)
	}
	fmt.Fprintf(w, `{"error":{"code":%d,"message":"fake error","status":%q%s}}`, code, status, details)
}

const dataPrefix = "/v4/users/me/dataTypes/"

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Query: r.URL.Query(), Form: r.PostForm})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/token":
		s.token(w, r)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/revoke":
		if s.RevokeStatus != 0 {
			w.WriteHeader(s.RevokeStatus)
			if s.RevokeStatus == http.StatusBadRequest {
				_, _ = io.WriteString(w, `{"error":"invalid_token","error_description":"Token expired or revoked"}`)
			}
			return
		}
		_, _ = io.WriteString(w, `{}`)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/keyset.json":
		if len(s.Keyset) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(s.Keyset)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		googleError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v4/users/me/identity":
		if s.IdentityStatus != 0 {
			w.WriteHeader(s.IdentityStatus)
			_, _ = io.WriteString(w, s.IdentityBody)
			return
		}
		fmt.Fprintf(w, `{"name":"users/me/identity","healthUserId":%q,"legacyUserId":"ABC123"}`, HealthUserID)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, dataPrefix) && strings.HasSuffix(r.URL.Path, "/dataPoints"):
		dt := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, dataPrefix), "/dataPoints")
		s.list(w, r, dt)
	default:
		googleError(w, http.StatusNotFound, "NOT_FOUND", "")
	}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if s.TokenStatus != 0 || s.TokenBody != "" {
		if s.TokenStatus != 0 {
			w.WriteHeader(s.TokenStatus)
		}
		_, _ = io.WriteString(w, s.TokenBody)
		return
	}
	if r.PostForm.Get("client_id") != s.ClientID || r.PostForm.Get("client_secret") != s.ClientSecret {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"Unauthorized"}`)
		return
	}
	var access string
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		if r.PostForm.Get("code") != "good-code" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Bad Request"}`)
			return
		}
		access = "fake-access-token-1"
	case "refresh_token":
		if r.PostForm.Get("refresh_token") != "fake-refresh-token" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
			return
		}
		access = "fake-access-token-2"
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"unsupported_grant_type"}`)
		return
	}
	body := map[string]any{"access_token": access, "expires_in": 3599, "token_type": "Bearer", "scope": s.Scope}
	if !s.NoRefreshToken && r.PostForm.Get("grant_type") == "authorization_code" {
		body["refresh_token"] = "fake-refresh-token"
	}
	_ = json.NewEncoder(w).Encode(body)
}

// list serves dataPoints.list: the points of dt, paged. The filter is
// recorded but not applied (the connector's filters are tested on their
// text).
func (s *Server) list(w http.ResponseWriter, r *http.Request, dt string) {
	if s.Status != nil {
		if code := s.Status(dt); code != 0 {
			if s.RetryAfter != "" {
				w.Header().Set("Retry-After", s.RetryAfter)
			}
			googleError(w, code, http.StatusText(code), "")
			return
		}
	}
	if s.Forbidden[dt] {
		googleError(w, http.StatusForbidden, "PERMISSION_DENIED", "")
		return
	}
	q := r.URL.Query()
	if q.Get("filter") == "" {
		googleError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "")
		return
	}
	size, _ := strconv.Atoi(q.Get("pageSize"))
	limit := 10000
	if dt == "exercise" || dt == "sleep" {
		limit = 25
	}
	if size <= 0 || size > limit {
		size = limit
	}
	start := 0
	if tok := q.Get("pageToken"); tok != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(tok, "page-"))
		if err != nil {
			googleError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "")
			return
		}
		start = n
	}
	s.mu.Lock()
	pts := s.points[dt]
	s.mu.Unlock()
	if start > len(pts) {
		start = len(pts)
	}
	end := min(start+size, len(pts))
	out := map[string]any{"dataPoints": pts[start:end]}
	if end < len(pts) {
		out["nextPageToken"] = "page-" + strconv.Itoa(end)
	}
	_ = json.NewEncoder(w).Encode(out)
}

// Signer is a webhook signing key: an ECDSA P-256 key with a Tink key id.
type Signer struct {
	KeyID uint32
	key   *ecdsa.PrivateKey
}

// NewSigner makes a fresh key.
func NewSigner(t testing.TB, keyID uint32) *Signer {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &Signer{KeyID: keyID, key: k}
}

// Keyset returns the Tink JSON keyset of the signers' public keys, each
// with output prefix TINK.
func Keyset(signers ...*Signer) []byte {
	type keyData struct {
		TypeURL         string `json:"typeUrl"`
		Value           string `json:"value"`
		KeyMaterialType string `json:"keyMaterialType"`
	}
	type key struct {
		KeyData          keyData `json:"keyData"`
		Status           string  `json:"status"`
		KeyID            uint32  `json:"keyId"`
		OutputPrefixType string  `json:"outputPrefixType"`
	}
	ks := struct {
		PrimaryKeyID uint32 `json:"primaryKeyId"`
		Key          []key  `json:"key"`
	}{}
	for i, s := range signers {
		if i == 0 {
			ks.PrimaryKeyID = s.KeyID
		}
		ks.Key = append(ks.Key, key{
			KeyData: keyData{
				TypeURL:         "type.googleapis.com/google.crypto.tink.EcdsaPublicKey",
				Value:           base64.StdEncoding.EncodeToString(s.publicKeyProto()),
				KeyMaterialType: "ASYMMETRIC_PUBLIC",
			},
			Status: "ENABLED", KeyID: s.KeyID, OutputPrefixType: "TINK",
		})
	}
	b, _ := json.Marshal(ks)
	return b
}

// publicKeyProto serializes the public key as a Tink EcdsaPublicKey:
// version 0, params {hash SHA256 (3), curve NIST_P256 (2), encoding DER (2)},
// x and y as big-endian bytes with a leading zero byte (as Tink writes them).
func (s *Signer) publicKeyProto() []byte {
	raw, err := s.key.PublicKey.Bytes() // 0x04 || X || Y
	if err != nil {
		panic(err)
	}
	x, y := raw[1:33], raw[33:65]
	field := func(num int, wire byte, v []byte) []byte {
		b := binary.AppendUvarint(nil, uint64(num<<3)|uint64(wire))
		if wire == 2 {
			b = binary.AppendUvarint(b, uint64(len(v)))
		}
		return append(b, v...)
	}
	params := append(append(field(1, 0, []byte{3}), field(2, 0, []byte{2})...), field(3, 0, []byte{2})...)
	out := field(1, 0, []byte{0})
	out = append(out, field(2, 2, params)...)
	out = append(out, field(3, 2, append([]byte{0}, x...))...)
	out = append(out, field(4, 2, append([]byte{0}, y...))...)
	return out
}

// Sign returns the GOOGLE-HEALTH-API-SIGNATURE value of body: base64 of the
// Tink prefix (0x01, key id) followed by the DER signature of body.
func (s *Signer) Sign(body []byte) string {
	digest := sha256.Sum256(body)
	der, err := ecdsa.SignASN1(rand.Reader, s.key, digest[:])
	if err != nil {
		panic(err)
	}
	prefix := []byte{1, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(prefix[1:], s.KeyID)
	return base64.StdEncoding.EncodeToString(append(prefix, der...))
}
