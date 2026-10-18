// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// passwordFake is the fake provider without a consent page: users sign in
// with a password, which it checks and keeps in the refresh token.
type passwordFake struct {
	fakeConnector
	mu      *sync.Mutex
	signIns *[]string
}

const fakePassword = "right password"

func (c passwordFake) AuthorizeURL(string, string) (string, error) {
	panic("AuthorizeURL must not be called for a PasswordConnector")
}

func (c passwordFake) SignIn(_ context.Context, email, password string) (connector.Tokens, error) {
	c.mu.Lock()
	*c.signIns = append(*c.signIns, email)
	c.mu.Unlock()
	if password != fakePassword {
		return connector.Tokens{}, connector.ErrBadCredentials
	}
	return connector.Tokens{
		Access: "pw-access-secret", Refresh: "pw-credentials:" + password,
		ProviderUserID: "pu-pw", Expiry: time.Now().Add(time.Hour),
	}, nil
}

type signInEnv struct {
	*connEnv
	mu      sync.Mutex
	signIns []string
}

func newSignInEnv(t *testing.T) *signInEnv {
	t.Helper()
	s := &signInEnv{}
	s.connEnv = newConnEnvWrapped(t, collect.SchedulerConfig{Concurrency: 8}, func(c fakeConnector) connector.Connector {
		return passwordFake{fakeConnector: c, mu: &s.mu, signIns: &s.signIns}
	})
	h := collect.NewHandlers(s.deps)
	s.mux.Handle("GET /oauth/{provider}/signin", h.SignInPage())
	s.mux.Handle("POST /oauth/{provider}/signin", h.SignIn())
	return s
}

// startSignIn begins a connection and returns the state of the sign-in URL.
func (e *signInEnv) startSignIn(t *testing.T, user uuid.UUID, redirect string) string {
	t.Helper()
	body, _ := json.Marshal(collect.StartRequest{RedirectURI: redirect})
	rec := e.do(t, http.MethodPost, "/v1/users/"+user.String()+"/connections/"+fakeSlug, e.keyA, body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp collect.StartResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	u, err := url.Parse(resp.AuthorizeURL)
	require.NoError(t, err)
	assert.Equal(t, "https://ohs.example.test/oauth/"+fakeSlug+"/signin", u.Scheme+"://"+u.Host+u.Path)
	state := u.Query().Get("state")
	require.NotEmpty(t, state)
	return state
}

func (e *signInEnv) post(t *testing.T, state, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"state": {state}, "email": {email}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/"+fakeSlug+"/signin", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func TestSignInFlow(t *testing.T) {
	e := newSignInEnv(t)
	ctx := context.Background()
	state := e.startSignIn(t, e.userA, "https://app.example.test/done")

	page := e.do(t, http.MethodGet, "/oauth/"+fakeSlug+"/signin?state="+url.QueryEscape(state), "", nil)
	require.Equal(t, http.StatusOK, page.Code, page.Body.String())
	assert.Contains(t, page.Body.String(), `name="password"`)
	assert.Contains(t, page.Body.String(), `value="`+state+`"`)
	csp := page.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "form-action 'self' https://app.example.test;")
	assert.Contains(t, csp, "frame-ancestors 'none'")
	assert.Equal(t, "no-store", page.Header().Get("Cache-Control"))

	// A wrong password asks again and keeps the state.
	rec := e.post(t, state, "a@example.org", "wrong")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "did not accept this email and password")
	assert.Contains(t, rec.Body.String(), `value="a@example.org"`, "the email is kept")
	assert.NotContains(t, rec.Body.String(), "wrong", "the password is never echoed")
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.connections WHERE user_id = $1`, e.userA))

	// An empty field is refused before the provider is asked.
	rec = e.post(t, state, "a@example.org", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = e.post(t, state, "a@example.org", fakePassword)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "app.example.test", loc.Host)
	assert.Equal(t, "connected", loc.Query().Get("status"))
	assert.Equal(t, "active", e.connStatus(t, e.userA))
	e.mu.Lock()
	assert.Equal(t, []string{"a@example.org", "a@example.org"}, e.signIns)
	e.mu.Unlock()

	// The credentials are sealed like any token.
	var accessEnc, refreshEnc []byte
	var puid string
	require.NoError(t, e.pool.QueryRow(ctx, `
		SELECT access_token_enc, refresh_token_enc, provider_user_id FROM ohs_sync.connections
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`, e.tenantA, e.userA, fakeSlug).
		Scan(&accessEnc, &refreshEnc, &puid))
	assert.Equal(t, "pu-pw", puid)
	assert.NotContains(t, string(refreshEnc), fakePassword)
	plain, err := e.box.OpenFor(refreshEnc, []byte(e.tenantA.String()+"|"+e.userA.String()+"|"+fakeSlug))
	require.NoError(t, err)
	assert.Equal(t, "pw-credentials:"+fakePassword, string(plain))
	assert.Positive(t, e.jobs(t, e.userA, "pending"), "the backfill is queued")

	// The state is spent.
	rec = e.post(t, state, "a@example.org", fakePassword)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSignInResultPageWithoutRedirect(t *testing.T) {
	e := newSignInEnv(t)
	state := e.startSignIn(t, e.userA, "")
	rec := e.post(t, state, "a@example.org", fakePassword)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "is connected")
}

func TestSignInExpiredState(t *testing.T) {
	e := newSignInEnv(t)
	state := e.startSignIn(t, e.userA, "https://app.example.test/done")
	e.setNow(time.Now().Add(collect.StateTTL + time.Minute))
	rec := e.post(t, state, "a@example.org", fakePassword)
	require.Equal(t, http.StatusFound, rec.Code)
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "error", loc.Query().Get("status"))
	e.mu.Lock()
	assert.Empty(t, e.signIns, "an expired state never reaches the provider")
	e.mu.Unlock()
	assert.Equal(t, 0, e.scalar(t, `SELECT count(*) FROM ohs_sync.oauth_states`))
}

func TestSignInUnknownState(t *testing.T) {
	e := newSignInEnv(t)
	rec := e.do(t, http.MethodGet, "/oauth/"+fakeSlug+"/signin?state=nope", "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = e.post(t, "nope", "a@example.org", fakePassword)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = e.do(t, http.MethodGet, "/oauth/"+fakeSlug+"/signin", "", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSignInPageRefusesOAuthProviders(t *testing.T) {
	e := newConnEnv(t)
	h := collect.NewHandlers(e.deps)
	e.mux.Handle("GET /oauth/{provider}/signin", h.SignInPage())
	state := e.start(t, e.keyA, e.userA, "https://app.example.test/done")
	rec := e.do(t, http.MethodGet, "/oauth/"+fakeSlug+"/signin?state="+url.QueryEscape(state), "", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
