// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/demo"
)

// Server is the sandbox provider: an OAuth 2.0 authorization server (consent
// page, token endpoint) and a data API, in one http.Handler. It keeps no
// state: codes and tokens are signed with its key and carry what they grant,
// so any replica, before or after a restart, accepts what another issued.
// Codes are not single use on their own; the core's OAuth state, which is,
// guards the callback.
//
//	GET  /authorize   the consent page (response_type=code, client_id,
//	                  redirect_uri, state, scope)
//	POST /token       grant_type=authorization_code or refresh_token, with
//	                  client_id and client_secret
//	GET  /v1/user                       {"id", "persona"}
//	GET  /v1/sleep?start=&end=          {"data": [Sleep]}
//	GET  /v1/heart_rate?start=&end=     {"data": [HeartRate]}
//	GET  /v1/steps?start=&end=          {"data": [Steps]}
//	GET  /v1/workouts?start=&end=       {"data": [Workout]}
//
// start and end are RFC 3339; a record is listed when it starts in
// [start, end). Every data request needs a bearer access token.
type Server struct {
	key          []byte
	clientID     string
	clientSecret string
	now          func() time.Time
	accessTTL    time.Duration

	// Counters, for tests and the docs' "what happened" checks.
	codes, exchanges, refreshes, dataCalls atomic.Int64
}

// Lifetimes of what the server issues. The access token is short on purpose,
// so a connection exercises the refresh grant within its first day.
const (
	codeTTL            = 10 * time.Minute
	DefaultAccessTTL   = time.Hour
	refreshTTL         = 30 * 24 * time.Hour
	maxWindow          = 31 * 24 * time.Hour
	tokenPrefix        = "sbx_"
	grantedScope       = "sleep heart_rate steps workouts"
	callbackPathSuffix = "/oauth/" + Slug + "/callback"
)

// newServer returns a server signing with key.
func newServer(key []byte, clientID, clientSecret string, now func() time.Time, accessTTL time.Duration) *Server {
	if accessTTL <= 0 {
		accessTTL = DefaultAccessTTL
	}
	return &Server{key: key, clientID: clientID, clientSecret: clientSecret, now: now, accessTTL: accessTTL}
}

// claims is what a code or token carries.
type claims struct {
	Typ      string `json:"typ"` // code, access, refresh
	Sub      string `json:"sub"` // the sandbox account
	Persona  string `json:"p"`
	Exp      int64  `json:"exp"`
	Redirect string `json:"ru,omitempty"` // codes: the redirect_uri they were issued for
}

func (s *Server) sign(c claims) string {
	body, _ := json.Marshal(c)
	payload := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	return tokenPrefix + payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

var errBadToken = errors.New("sandbox: invalid or expired token")

func (s *Server) verify(tok, typ string) (claims, error) {
	rest, ok := strings.CutPrefix(tok, tokenPrefix)
	if !ok {
		return claims{}, errBadToken
	}
	payload, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return claims{}, errBadToken
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return claims{}, errBadToken
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return claims{}, errBadToken
	}
	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return claims{}, errBadToken
	}
	var c claims
	if err := json.Unmarshal(body, &c); err != nil || c.Typ != typ || s.now().Unix() >= c.Exp {
		return claims{}, errBadToken
	}
	if _, ok := demo.LookupPersona(c.Persona); !ok || c.Persona == "" {
		return claims{}, errBadToken
	}
	return c, nil
}

// ServeHTTP routes one request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/authorize":
		page, err := s.consentPage(r.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	case r.Method == http.MethodPost && r.URL.Path == "/token":
		s.token(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/"):
		s.data(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	}
}

// personaChoice is one link of the consent page.
type personaChoice struct {
	Name, Description, Href string
}

var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sandbox provider (synthetic data)</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem;color:#1a1a1a}
.warn{border:2px solid #b45309;background:#fffbeb;padding:.75rem 1rem;border-radius:.5rem}
a.btn{display:block;margin:.5rem 0;padding:.75rem 1rem;border:1px solid #ccc;border-radius:.5rem;text-decoration:none;color:inherit}
a.btn:hover{border-color:#555}small{color:#555}</style></head>
<body><h1>Sandbox provider</h1>
<p class="warn"><strong>This is not a real provider.</strong> Every value it serves is invented by the
Open Health Sync demo generator and is stored with the provider <code>sandbox</code> and the metadata
<code>"synthetic": true</code>. It exists to try the connect flow without a vendor account.</p>
<p>Choose the invented person whose data this connection reads:</p>
{{range .Choices}}<a class="btn" href="{{.Href}}"><strong>Allow as {{.Name}}</strong><br><small>{{.Description}}</small></a>
{{end}}<a class="btn" href="{{.Deny}}">Deny</a>
</body></html>
`))

// consentPage renders the consent page of an authorization request: one
// link per demo persona, each carrying a code for that persona, and a deny
// link. The redirect_uri must end in /oauth/sandbox/callback.
func (s *Server) consentPage(q url.Values) ([]byte, error) {
	if q.Get("response_type") != "code" {
		return nil, errors.New("response_type must be code")
	}
	if q.Get("client_id") != s.clientID {
		return nil, errors.New("unknown client_id")
	}
	redirect := q.Get("redirect_uri")
	if !validRedirect(redirect) {
		return nil, errors.New("redirect_uri must be an http or https URL ending in " + callbackPathSuffix)
	}
	state := q.Get("state")
	if state == "" {
		return nil, errors.New("state is required")
	}
	link := func(v url.Values) string {
		u, _ := url.Parse(redirect)
		q := u.Query()
		for k, vs := range v {
			q[k] = vs
		}
		u.RawQuery = q.Encode()
		return u.String()
	}
	exp := s.now().Add(codeTTL).Unix()
	choices := make([]personaChoice, 0, len(demo.Personas))
	for _, p := range demo.Personas {
		code := s.sign(claims{Typ: "code", Sub: accountID(s.key, state, p.Name), Persona: p.Name, Exp: exp, Redirect: redirect})
		choices = append(choices, personaChoice{Name: p.Name, Description: p.Description,
			Href: link(url.Values{"code": {code}, "state": {state}})})
	}
	s.codes.Add(1)
	var b bytes.Buffer
	err := consentTmpl.Execute(&b, struct {
		Choices []personaChoice
		Deny    string
	}{choices, link(url.Values{"error": {"access_denied"}, "state": {state}})})
	return b.Bytes(), err
}

// validRedirect accepts an absolute http or https URL, without credentials,
// whose path is the sandbox callback.
func validRedirect(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	return strings.HasSuffix(u.Path, callbackPathSuffix)
}

// accountID names the sandbox account a consent creates: one per consent
// and persona, so reconnecting gives a new account id but the same data.
func accountID(key []byte, state, persona string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("account|" + state + "|" + persona))
	return "sbx-" + persona + "-" + fmt.Sprintf("%x", mac.Sum(nil)[:6])
}

// tokenAnswer is the answer of POST /token.
type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	User         struct {
		ID      string `json:"id"`
		Persona string `json:"persona"`
	} `json:"user"`
}

// token serves POST /token.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	f := r.PostForm
	if f.Get("client_id") != s.clientID ||
		subtle.ConstantTimeCompare([]byte(f.Get("client_secret")), []byte(s.clientSecret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	var c claims
	var err error
	switch f.Get("grant_type") {
	case "authorization_code":
		c, err = s.verify(f.Get("code"), "code")
		if err == nil && c.Redirect != f.Get("redirect_uri") {
			err = errBadToken
		}
		if err == nil {
			s.exchanges.Add(1)
		}
	case "refresh_token":
		c, err = s.verify(f.Get("refresh_token"), "refresh")
		if err == nil {
			s.refreshes.Add(1)
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	now := s.now()
	a := tokenAnswer{
		AccessToken:  s.sign(claims{Typ: "access", Sub: c.Sub, Persona: c.Persona, Exp: now.Add(s.accessTTL).Unix()}),
		RefreshToken: s.sign(claims{Typ: "refresh", Sub: c.Sub, Persona: c.Persona, Exp: now.Add(refreshTTL).Unix()}),
		TokenType:    "Bearer",
		ExpiresIn:    int64(s.accessTTL / time.Second),
		Scope:        grantedScope,
	}
	a.User.ID, a.User.Persona = c.Sub, c.Persona
	writeJSON(w, http.StatusOK, a)
}

// data serves the /v1 endpoints.
func (s *Server) data(w http.ResponseWriter, r *http.Request) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	c, err := s.verify(tok, "access")
	if !ok || err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	s.dataCalls.Add(1)
	if r.URL.Path == "/v1/user" {
		writeJSON(w, http.StatusOK, map[string]string{"id": c.Sub, "persona": c.Persona})
		return
	}
	q := r.URL.Query()
	from, err1 := time.Parse(time.RFC3339, q.Get("start"))
	to, err2 := time.Parse(time.RFC3339, q.Get("end"))
	if err1 != nil || err2 != nil || to.Before(from) || to.Sub(from) > maxWindow {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "start and end must be RFC 3339, at most 31 days apart"})
		return
	}
	ds, err := generate(c.Persona, from, to, s.now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var list any
	switch strings.TrimPrefix(r.URL.Path, "/v1/") {
	case DataTypeSleep:
		list = nonNil(ds.Sleep)
	case DataTypeHeartRate:
		list = nonNil(ds.HeartRate)
	case DataTypeSteps:
		list = nonNil(ds.Steps)
	case DataTypeWorkouts:
		list = nonNil(ds.Workouts)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
