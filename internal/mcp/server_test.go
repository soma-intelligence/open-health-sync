// SPDX-License-Identifier: AGPL-3.0-only

package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/mcp"
	"github.com/soma-intelligence/open-health-sync/internal/prompts"
)

// fakeAuth serves NewHandler behind the same wrapper the router uses, with
// an in-memory lookup: no database, so only calls refused before any query.
type fakeAuth struct {
	srv    *httptest.Server
	key    string // tenant API key
	read   string // user token, scope read
	upload string // user token, scope upload
	user   uuid.UUID
}

func newFake(t *testing.T) *fakeAuth {
	t.Helper()
	tenantID, user := uuid.New(), uuid.New()
	key, _, _ := auth.NewSecret(auth.PrefixSecretKey)
	rd, _, _ := auth.NewSecret(auth.PrefixUserToken)
	up, _, _ := auth.NewSecret(auth.PrefixUserToken)
	principals := map[string]auth.Principal{
		auth.Hash(key): {TenantID: tenantID},
		auth.Hash(rd):  {TenantID: tenantID, UserID: &user, Scopes: []string{"read"}},
		auth.Hash(up):  {TenantID: tenantID, UserID: &user, Scopes: []string{"upload"}},
	}
	lookup := func(_ context.Context, hash string) (auth.Principal, error) {
		if p, ok := principals[hash]; ok {
			return p, nil
		}
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	h := auth.MiddlewareAny(lookup, auth.TenantKey, auth.UserRead)(mcp.NewHandler(mcp.Deps{}))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &fakeAuth{srv: srv, key: key, read: rd, upload: up, user: user}
}

// bearer adds the Authorization header to every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// Connect opens an MCP client session on url with token.
func Connect(t *testing.T, url, token string) *sdk.ClientSession {
	t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearer{token}}, MaxRetries: -1,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call runs a tool and returns the result text and whether it is a tool error.
func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	txt, ok := res.Content[0].(*sdk.TextContent)
	require.True(t, ok)
	return txt.Text, res.IsError
}

func TestToolsList(t *testing.T) {
	f := newFake(t)
	for _, tok := range []string{f.key, f.read} {
		cs := Connect(t, f.srv.URL, tok)
		res, err := cs.ListTools(context.Background(), nil)
		require.NoError(t, err)
		var names []string
		for _, tl := range res.Tools {
			names = append(names, tl.Name)
			assert.NotEmpty(t, tl.Description, tl.Name)
		}
		// Raw samples can be thousands a day: the descriptions steer an
		// agent to daily summaries and narrow windows first.
		for _, tl := range res.Tools {
			if tl.Name == "get_samples" {
				assert.Contains(t, tl.Description, "get_daily")
				assert.Contains(t, tl.Description, "narrow")
			}
		}
		assert.Contains(t, cs.InitializeResult().Instructions, "get_daily")
		slices.Sort(names)
		assert.Equal(t, []string{"get_category_samples", "get_coverage", "get_daily", "get_ecg_recordings", "get_hrv", "get_menstrual_cycles",
			"get_nutrition", "get_rr_intervals", "get_sample_stats", "get_samples", "get_sleep", "get_tags", "get_workout_route", "get_workouts", "list_connections",
			"list_quantity_types", "list_users"}, names)
	}
}

func TestRefusedBeforeAnyQuery(t *testing.T) {
	f := newFake(t)
	key := Connect(t, f.srv.URL, f.key)
	rd := Connect(t, f.srv.URL, f.read)

	msg, isErr := call(t, key, "get_samples", map[string]any{"type": "heart_rate"})
	assert.True(t, isErr)
	assert.Contains(t, msg, "user_id is required")

	msg, isErr = call(t, rd, "get_samples", map[string]any{"type": "heart_rate", "user_id": uuid.NewString()})
	assert.True(t, isErr)
	assert.Contains(t, msg, "own user")

	msg, isErr = call(t, rd, "list_users", nil)
	assert.True(t, isErr)
	assert.Contains(t, msg, "tenant API key")

	for _, lim := range []int{5000, 1001, -1} {
		msg, isErr = call(t, key, "get_samples", map[string]any{"type": "heart_rate", "user_id": f.user.String(), "limit": lim})
		assert.True(t, isErr, lim)
		assert.Equal(t, "limit must be between 1 and 1000 (default 100)", msg, lim)
	}

	msg, isErr = call(t, key, "get_samples", map[string]any{"type": "heart_rate", "user_id": f.user.String(), "start": "yesterday"})
	assert.True(t, isErr)
	assert.Contains(t, msg, "start")

	msg, isErr = call(t, key, "get_samples", map[string]any{"type": "heart_rate", "user_id": "not-a-uuid"})
	assert.True(t, isErr)
	assert.Equal(t, "user not found", msg)
}

func TestHTTPAuth(t *testing.T) {
	f := newFake(t)
	post := func(token string) int {
		req, _ := http.NewRequest(http.MethodPost, f.srv.URL, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = res.Body.Close()
		return res.StatusCode
	}
	assert.Equal(t, http.StatusUnauthorized, post(""))
	assert.Equal(t, http.StatusForbidden, post(f.upload))

	// Without the auth wrapper there is no principal: refused, never served.
	rec := httptest.NewRecorder()
	mcp.NewHandler(mcp.Deps{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// The embedded argument groups flatten into one object, as in the REST query.
func TestInputSchemas(t *testing.T) {
	f := newFake(t)
	cs := Connect(t, f.srv.URL, f.key)
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	type schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	got := map[string]schema{}
	for _, tl := range res.Tools {
		raw, err := json.Marshal(tl.InputSchema)
		require.NoError(t, err)
		var s schema
		require.NoError(t, json.Unmarshal(raw, &s))
		got[tl.Name] = s
		assert.True(t, tl.Annotations != nil && tl.Annotations.ReadOnlyHint, tl.Name)
	}
	keys := func(m map[string]any) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		return out
	}
	assert.ElementsMatch(t, []string{"user_id", "type", "start", "end", "view", "cursor", "limit"}, keys(got["get_samples"].Properties))
	assert.Equal(t, []string{"type"}, got["get_samples"].Required)
	assert.ElementsMatch(t, []string{"user_id", "workout_id", "start", "end", "cursor", "limit"}, keys(got["get_workout_route"].Properties))
	assert.Equal(t, []string{"workout_id"}, got["get_workout_route"].Required)
	assert.ElementsMatch(t, []string{"user_id", "session_id", "start", "end", "view", "cursor", "limit"}, keys(got["get_sleep"].Properties))
	assert.ElementsMatch(t, []string{"user_id", "start", "end", "view", "cursor", "limit"}, keys(got["get_daily"].Properties))
	assert.ElementsMatch(t, []string{"user_id", "start", "end", "cursor", "limit"}, keys(got["get_nutrition"].Properties))
	assert.ElementsMatch(t, []string{"cursor", "limit"}, keys(got["list_users"].Properties))
	assert.Empty(t, got["list_quantity_types"].Properties)
	assert.ElementsMatch(t, []string{"user_id"}, keys(got["list_connections"].Properties))
	assert.ElementsMatch(t, []string{"user_id", "provider", "start", "end"}, keys(got["get_coverage"].Properties))
	assert.ElementsMatch(t, []string{"user_id", "type", "interval", "tz", "start", "end", "provider", "view"}, keys(got["get_sample_stats"].Properties))
	assert.ElementsMatch(t, []string{"type", "start", "end"}, got["get_sample_stats"].Required)
	assert.ElementsMatch(t, []string{"user_id", "start", "end", "cursor", "limit"}, keys(got["get_menstrual_cycles"].Properties))
	assert.Empty(t, got["get_menstrual_cycles"].Required)
	assert.ElementsMatch(t, []string{"user_id", "recording_id", "start", "end", "cursor", "limit"}, keys(got["get_ecg_recordings"].Properties))
	assert.Empty(t, got["get_ecg_recordings"].Required)
}

// A browser page on another site cannot drive /mcp with a credential the
// browser holds; an agent (no Sec-Fetch-Site, no Origin) passes.
func TestCrossOriginRefused(t *testing.T) {
	f := newFake(t)
	post := func(hdr ...string) int {
		req, _ := http.NewRequest(http.MethodPost, f.srv.URL,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Authorization", "Bearer "+f.key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = res.Body.Close()
		return res.StatusCode
	}
	assert.Equal(t, http.StatusForbidden, post("Sec-Fetch-Site", "cross-site"))
	assert.Equal(t, http.StatusForbidden, post("Origin", "https://evil.example"))
	assert.Equal(t, http.StatusOK, post())
	assert.Equal(t, http.StatusOK, post("Sec-Fetch-Site", "same-origin"))
}

// A request the MCP transport refuses (no JSON content type, a body that is
// not JSON-RPC, a missing Accept, a cross-origin page, another method) is an
// RFC 9457 problem like every other error of the API, never text/plain.
func TestTransportErrorsAreProblems(t *testing.T) {
	f := newFake(t)
	for _, tc := range []struct {
		method, body string
		hdr          []string
		status       int
	}{
		{http.MethodPost, "", nil, http.StatusUnsupportedMediaType},
		{http.MethodPost, "{", []string{"Content-Type", "application/json", "Accept", "application/json, text/event-stream"}, http.StatusBadRequest},
		{http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, []string{"Content-Type", "application/json"}, http.StatusBadRequest},
		{http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, []string{"Content-Type", "application/json",
			"Accept", "application/json, text/event-stream", "Sec-Fetch-Site", "cross-site"}, http.StatusForbidden},
		{http.MethodGet, "", nil, http.StatusMethodNotAllowed},
	} {
		req, _ := http.NewRequest(tc.method, f.srv.URL, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+f.key)
		for i := 0; i+1 < len(tc.hdr); i += 2 {
			req.Header.Set(tc.hdr[i], tc.hdr[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		var p struct {
			Status int    `json:"status"`
			Detail string `json:"detail"`
		}
		err = json.NewDecoder(res.Body).Decode(&p)
		_ = res.Body.Close()
		assert.Equal(t, tc.status, res.StatusCode, "%s %q", tc.method, tc.body)
		assert.Equal(t, "application/problem+json", res.Header.Get("Content-Type"), "%s %q", tc.method, tc.body)
		require.NoError(t, err, "%s %q", tc.method, tc.body)
		assert.Equal(t, tc.status, p.Status)
		assert.NotEmpty(t, p.Detail)
	}
}

// The prompt library is listed with its blanks as arguments, and a prompt
// comes back filled, followed by the rules every answer holds to.
func TestPrompts(t *testing.T) {
	f := newFake(t)
	cs := Connect(t, f.srv.URL, f.key)
	res, err := cs.ListPrompts(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, res.Prompts, len(prompts.Library))
	byName := map[string]*sdk.Prompt{}
	for _, p := range res.Prompts {
		byName[p.Name] = p
		assert.NotEmpty(t, p.Description, p.Name)
	}
	taper := byName["taper_check"]
	require.NotNil(t, taper)
	var args []string
	for _, a := range taper.Arguments {
		args = append(args, a.Name)
		assert.Equal(t, a.Name == "event_date", a.Required, a.Name)
	}
	assert.Equal(t, []string{"event_date", "user_id"}, args)

	got, err := cs.GetPrompt(context.Background(), &sdk.GetPromptParams{Name: "taper_check",
		Arguments: map[string]string{"event_date": "2026-11-01", "user_id": f.user.String()}})
	require.NoError(t, err)
	require.Len(t, got.Messages, 1)
	assert.Equal(t, sdk.Role("user"), got.Messages[0].Role)
	txt, ok := got.Messages[0].Content.(*sdk.TextContent)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(txt.Text, "My event is on 2026-11-01. Compare the last 14 days"), txt.Text)
	assert.Contains(t, txt.Text, "Read the data of user_id "+f.user.String()+".")
	assert.Contains(t, txt.Text, "do not diagnose")

	_, err = cs.GetPrompt(context.Background(), &sdk.GetPromptParams{Name: "taper_check"})
	assert.ErrorContains(t, err, "event_date")
	assert.Contains(t, cs.InitializeResult().Instructions, "do not diagnose")
}

// A NUL in a text argument is refused by name before any query: Postgres
// refuses it in text, which used to surface as an internal error.
func TestNULArgumentRefused(t *testing.T) {
	f := newFake(t)
	cs := Connect(t, f.srv.URL, f.read)
	for tool, args := range map[string]map[string]any{
		"get_samples":      {"type": "heart_rate\x00"},
		"get_coverage":     {"provider": "oura\x00"},
		"get_rr_intervals": {"series_id": "\x00"},
		"get_tags":         {"kind": "illness", "start": "2026-01-01\x00"},
	} {
		txt, isErr := call(t, cs, tool, args)
		assert.True(t, isErr, tool)
		assert.Contains(t, txt, "holds a NUL character", tool)
		assert.NotContains(t, txt, "internal error", tool)
	}
}
