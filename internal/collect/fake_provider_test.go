// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// fakeSlug is the slug of the test provider.
const fakeSlug = "fakefit"

// inboundSecret signs fake notifications.
const inboundSecret = "fake-inbound-secret"

// fakeProvider is an OAuth + push provider backed by an httptest server.
// Modes switch how its token and data endpoints answer.
type fakeProvider struct {
	srv *httptest.Server

	refreshMode atomic.Value // "ok" | "revoked" | "keep" (no new refresh token issued)
	// lastRefresh is the refresh token of the last refresh request.
	lastRefresh atomic.Value
	fetchMode   atomic.Value // "ok" | "429" | "429long" | "revoked" | "noscope" | "500" | "hang"

	// onRefusal, when set, runs on every 401 of the data endpoint with the
	// refusal count, before the answer is sent.
	onRefusal atomic.Pointer[func(n int64)]
	refusalN  atomic.Int64
	// beforeEmit, when set, runs inside Fetch just before the emit.
	beforeEmit atomic.Pointer[func()]
	// afterExchange, when set, runs inside Exchange once the code is traded.
	afterExchange atomic.Pointer[func()]

	issued   atomic.Int64 // tokens issued, makes every token unique
	refreshN atomic.Int64
	fetchN   atomic.Int64

	mu       sync.Mutex
	userOf   map[string]string // code -> provider user id
	access   map[string]string // valid access token -> provider user id
	revoked  []string          // tokens passed to Revoke
	subCalls []subCall         // Subscribe and Unsubscribe calls, in order

	// subscribeFail makes Subscribe fail.
	subscribeFail atomic.Bool
	// unsubscribeHang makes Unsubscribe wait until its context ends.
	unsubscribeHang atomic.Bool
	// onSubscribe, when set, runs inside Subscribe before it answers.
	onSubscribe atomic.Pointer[func()]
	rejected    map[string]bool // access tokens the data endpoint refuses (401)
	lastAuth    string          // Authorization of the last data call
}

func newFakeProvider() *fakeProvider {
	f := &fakeProvider{userOf: map[string]string{}, access: map[string]string{}}
	f.refreshMode.Store("ok")
	f.fetchMode.Store("ok")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("GET /data", f.data)
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeProvider) Close() { f.srv.Close() }

// grant registers an authorization code for a provider user.
func (f *fakeProvider) grant(code, providerUser string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userOf[code] = providerUser
}

func (f *fakeProvider) issue(providerUser string) map[string]any {
	n := f.issued.Add(1)
	access := fmt.Sprintf("access-secret-%d-%s", n, providerUser)
	f.mu.Lock()
	f.access[access] = providerUser
	f.mu.Unlock()
	return map[string]any{
		"access_token":  access,
		"refresh_token": fmt.Sprintf("refresh-secret-%d-%s", n, providerUser),
		"expires_in":    3600,
		"userid":        providerUser,
	}
}

func (f *fakeProvider) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		f.mu.Lock()
		u, ok := f.userOf[r.Form.Get("code")]
		delete(f.userOf, r.Form.Get("code"))
		f.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(f.issue(u))
	case "refresh_token":
		f.refreshN.Add(1)
		// Slow enough that concurrent pulls overlap the refresh.
		time.Sleep(200 * time.Millisecond)
		f.lastRefresh.Store(r.Form.Get("refresh_token"))
		if f.refreshMode.Load() == "revoked" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
			return
		}
		out := f.issue(r.Form.Get("userid"))
		if f.refreshMode.Load() == "keep" {
			// RFC 6749 section 6: the server MAY issue a new refresh token.
			delete(out, "refresh_token")
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		http.Error(w, "bad grant", http.StatusBadRequest)
	}
}

func (f *fakeProvider) data(w http.ResponseWriter, r *http.Request) {
	f.fetchN.Add(1)
	f.mu.Lock()
	f.lastAuth = r.Header.Get("Authorization")
	refused := f.rejected[strings.TrimPrefix(f.lastAuth, "Bearer ")]
	f.mu.Unlock()
	if refused {
		http.Error(w, "token expired", http.StatusUnauthorized)
		return
	}
	switch f.fetchMode.Load() {
	case "429":
		w.Header().Set("Retry-After", "120")
		http.Error(w, "slow down", http.StatusTooManyRequests)
		return
	case "429long":
		w.Header().Set("Retry-After", "7200")
		http.Error(w, "daily quota spent", http.StatusTooManyRequests)
		return
	case "noscope":
		http.Error(w, "scope not granted", http.StatusForbidden)
		return
	case "revoked":
		n := f.refusalN.Add(1)
		if hook := f.onRefusal.Load(); hook != nil {
			(*hook)(n)
		}
		http.Error(w, "revoked", http.StatusUnauthorized)
		return
	case "500":
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	case "hang":
		// Answers only once the caller gives up.
		<-r.Context().Done()
		return
	}
	from := r.URL.Query().Get("from")
	// Two records per window, identical for identical windows.
	_, _ = fmt.Fprintf(w, "{\"type\":\"steps\",\"start\":%q,\"value\":100}\n{\"type\":\"steps\",\"start\":%q,\"value\":200}\n", from, from)
}

// fakeConnector implements OAuthConnector, PushConnector and Revoker.
type fakeConnector struct{ f *fakeProvider }

func (c fakeConnector) Slug() string                  { return fakeSlug }
func (c fakeConnector) Enabled(connector.Config) bool { return true }
func (c fakeConnector) Map(connector.RawRecord) (model.MapResult, error) {
	return model.MapResult{}, nil
}

func (c fakeConnector) AuthorizeURL(state, redirect string) (string, error) {
	v := url.Values{"state": {state}, "redirect_uri": {redirect}}
	return c.f.srv.URL + "/authorize?" + v.Encode(), nil
}

func (c fakeConnector) postToken(ctx context.Context, form url.Values) (connector.Tokens, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.f.srv.URL+"/token", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return connector.Tokens{}, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return connector.Tokens{}, connector.RateLimited(resp.Header, time.Now())
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest:
		return connector.Tokens{}, connector.ErrNeedsReauth
	case resp.StatusCode != http.StatusOK:
		return connector.Tokens{}, fmt.Errorf("token endpoint: %d", resp.StatusCode)
	}
	var body struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		ExpiresIn int    `json:"expires_in"`
		UserID    string `json:"userid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return connector.Tokens{}, err
	}
	return connector.Tokens{
		Access: body.Access, Refresh: body.Refresh, ProviderUserID: body.UserID,
		Expiry: time.Now().Add(time.Duration(body.ExpiresIn) * time.Second), Scopes: []string{"activity"},
	}, nil
}

func (c fakeConnector) Exchange(ctx context.Context, code, redirect string) (connector.Tokens, error) {
	t, err := c.postToken(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}})
	if h := c.f.afterExchange.Load(); h != nil && err == nil {
		(*h)()
	}
	if errors.Is(err, connector.ErrNeedsReauth) {
		return t, errors.New("code refused")
	}
	return t, err
}

func (c fakeConnector) Refresh(ctx context.Context, t connector.Tokens) (connector.Tokens, error) {
	return c.postToken(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}, "userid": {t.ProviderUserID}})
}

func (c fakeConnector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	v := url.Values{"from": {from.UTC().Format(time.RFC3339)}, "to": {to.UTC().Format(time.RFC3339)}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.f.srv.URL+"/data?"+v.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+conn.Tokens.Access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		return connector.RateLimited(resp.Header, time.Now())
	case http.StatusUnauthorized:
		return connector.ErrNeedsReauth
	case http.StatusForbidden:
		return fmt.Errorf("data endpoint: 403: %w", connector.ErrMissingScope)
	default:
		return fmt.Errorf("data endpoint: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	lines := bytes.Split(bytes.TrimSpace(body), []byte("\n"))
	if hook := c.f.beforeEmit.Load(); hook != nil {
		(*hook)()
	}
	return emit.Emit(ctx, "steps", time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC), lines)
}

// Inbound notifications: JSON {"userid","startdate","enddate"} signed with
// X-Fake-Signature = hex(HMAC-SHA256(secret, body)).
func (c fakeConnector) HandleInbound(_ context.Context, r *http.Request) ([]connector.InboundEvent, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, []byte(inboundSecret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Fake-Signature"))) {
		return nil, errors.New("bad signature")
	}
	var n struct {
		UserID  string `json:"userid"`
		Start   string `json:"startdate"`
		End     string `json:"enddate"`
		Deleted string `json:"deleted"`
	}
	if err := json.Unmarshal(body, &n); err != nil {
		return nil, err
	}
	if n.Deleted != "" {
		// A deletion notice: stored verbatim, for Map to turn into a deletion.
		return []connector.InboundEvent{{ProviderUserID: n.UserID, DataType: "deletion", Lines: [][]byte{body}}}, nil
	}
	s, _ := strconv.ParseInt(n.Start, 10, 64)
	e, _ := strconv.ParseInt(n.End, 10, 64)
	return []connector.InboundEvent{{ProviderUserID: n.UserID, DataType: "steps", From: time.Unix(s, 0), To: time.Unix(e, 0)}}, nil
}

func (c fakeConnector) Revoke(_ context.Context, t connector.Tokens) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.revoked = append(c.f.revoked, t.Access)
	return nil
}

// subCall is one Subscribe or Unsubscribe call.
type subCall struct {
	action, access, url string
}

func (c fakeConnector) Subscribe(_ context.Context, conn connector.Connection, callbackURL string) error {
	if hook := c.f.onSubscribe.Load(); hook != nil {
		(*hook)()
	}
	if c.f.subscribeFail.Load() {
		return errors.New("subscribe refused by the provider")
	}
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.subCalls = append(c.f.subCalls, subCall{"subscribe", conn.Tokens.Access, callbackURL})
	return nil
}

func (c fakeConnector) Unsubscribe(ctx context.Context, conn connector.Connection, callbackURL string) error {
	if c.f.unsubscribeHang.Load() {
		<-ctx.Done()
		return ctx.Err()
	}
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.subCalls = append(c.f.subCalls, subCall{"unsubscribe", conn.Tokens.Access, callbackURL})
	return nil
}

func (f *fakeProvider) subscriptionCalls() []subCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]subCall(nil), f.subCalls...)
}

// rejectCurrentTokens makes the data endpoint refuse every access token
// issued so far, as a provider does when they expire early.
func (f *fakeProvider) rejectCurrentTokens() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rejected == nil {
		f.rejected = map[string]bool{}
	}
	for tok := range f.access {
		f.rejected[tok] = true
	}
}

func (f *fakeProvider) lastAuthorization() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAuth
}

func (f *fakeProvider) revokedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

func sign(body []byte) string {
	mac := hmac.New(sha256.New, []byte(inboundSecret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
