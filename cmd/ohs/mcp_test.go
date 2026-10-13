// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/mcp"
)

// fakeOHS serves /mcp like an Open Health Sync instance would, with an
// in-memory lookup (no database: only tools/list and refusals work).
func fakeOHS(t *testing.T) (url, key, readToken string) {
	t.Helper()
	tenantID, user := uuid.New(), uuid.New()
	key, _, _ = auth.NewSecret(auth.PrefixSecretKey)
	readToken, _, _ = auth.NewSecret(auth.PrefixUserToken)
	principals := map[string]auth.Principal{
		auth.Hash(key):       {TenantID: tenantID},
		auth.Hash(readToken): {TenantID: tenantID, UserID: &user, Scopes: []string{"read"}},
	}
	lookup := func(_ context.Context, h string) (auth.Principal, error) {
		if p, ok := principals[h]; ok {
			return p, nil
		}
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", auth.MiddlewareAny(lookup, auth.TenantKey, auth.UserRead)(mcp.NewHandler(mcp.Deps{})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, key, readToken
}

func buildOHS(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ohs")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// stdioSession drives a spawned `ohs mcp` over its stdin and stdout.
type stdioSession struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
}

func startMCP(t *testing.T, bin string, env ...string) *stdioSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin, "mcp")
	cmd.Env = append(os.Environ(), env...)
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = in.Close()
		_ = cmd.Wait()
	})
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	return &stdioSession{t: t, in: in, out: sc}
}

func (s *stdioSession) send(msg string) {
	s.t.Helper()
	if _, err := io.WriteString(s.in, msg+"\n"); err != nil {
		s.t.Fatal(err)
	}
}

// reply reads lines until the response with id arrives.
func (s *stdioSession) reply(id int) map[string]any {
	s.t.Helper()
	for s.out.Scan() {
		var m map[string]any
		if err := json.Unmarshal(s.out.Bytes(), &m); err != nil {
			s.t.Fatalf("not JSON on stdout: %q", s.out.Text())
		}
		if v, ok := m["id"].(float64); ok && int(v) == id {
			return m
		}
	}
	s.t.Fatalf("no reply %d: %v", id, s.out.Err())
	return nil
}

func (s *stdioSession) initialize() {
	s.t.Helper()
	s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	r := s.reply(1)
	if r["error"] != nil {
		s.t.Fatalf("initialize: %v", r["error"])
	}
	res := r["result"].(map[string]any)
	if !strings.Contains(res["instructions"].(string), "list_users") {
		s.t.Fatalf("instructions not forwarded: %v", res)
	}
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
}

func TestMCPStdioProxy(t *testing.T) {
	url, key, readToken := fakeOHS(t)
	bin := buildOHS(t)

	s := startMCP(t, bin, "OHS_MCP_URL="+url, "OHS_MCP_KEY="+key)
	s.initialize()
	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	r := s.reply(2)
	var names []string
	for _, tl := range r["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	slices.Sort(names)
	want := []string{"get_category_samples", "get_coverage", "get_daily", "get_hrv", "get_menstrual_cycles",
		"get_nutrition", "get_rr_intervals", "get_samples", "get_sleep", "get_tags", "get_workout_route", "get_workouts",
		"list_connections", "list_quantity_types", "list_users"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v", names)
	}
	// A call is forwarded and its tool error comes back as is.
	s.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_samples","arguments":{"type":"heart_rate"}}}`)
	res := s.reply(3)["result"].(map[string]any)
	if res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "user_id is required") {
		t.Fatalf("call result = %v", res)
	}

	// A user token, through OHS_PUBLIC_URL (OHS_MCP_URL unset; the path /mcp is added).
	s = startMCP(t, bin, "OHS_MCP_URL=", "OHS_PUBLIC_URL="+url+"/", "OHS_MCP_KEY="+readToken)
	s.initialize()
	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_users","arguments":{}}}`)
	res = s.reply(2)["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("list_users with a user token = %v", res)
	}
}

func TestMCPStdioErrors(t *testing.T) {
	url, _, _ := fakeOHS(t)
	bin := buildOHS(t)
	for name, env := range map[string][]string{
		"no key":    {"OHS_MCP_URL=" + url, "OHS_MCP_KEY="},
		"bad key":   {"OHS_MCP_URL=" + url, "OHS_MCP_KEY=ohs_sk_wrong"},
		"bad url":   {"OHS_MCP_URL=ftp://x", "OHS_MCP_KEY=k"},
		"extra arg": {"OHS_MCP_URL=" + url, "OHS_MCP_KEY=k"},
	} {
		args := []string{"mcp"}
		if name == "extra arg" {
			args = append(args, "nope")
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdin = strings.NewReader("")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("%s: expected failure, got %s", name, out)
		}
		if strings.Contains(string(out), "ohs_sk_wrong") {
			t.Fatalf("%s: the key leaked into the output: %s", name, out)
		}
		if name == "bad key" && !strings.Contains(string(out), "refused OHS_MCP_KEY") {
			t.Fatalf("bad key: the message should blame the key: %s", out)
		}
	}
}

func TestMCPConnectHint(t *testing.T) {
	for status, want := range map[int]string{
		401: "refused OHS_MCP_KEY",
		403: "scope read",
		404: "no MCP endpoint",
		405: "no MCP endpoint",
		502: "",
		0:   "",
	} {
		got := mcpConnectHint(status)
		if want == "" && got != "" || !strings.Contains(got, want) {
			t.Errorf("%d: got %q, want %q", status, got, want)
		}
	}
}

func TestMCPEndpoint(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		vars map[string]string
		want string
	}{
		{map[string]string{}, "http://localhost:8080/mcp"},
		{map[string]string{"OHS_PUBLIC_URL": "https://ohs.example.org/"}, "https://ohs.example.org/mcp"},
		{map[string]string{"OHS_PUBLIC_URL": "https://a", "OHS_MCP_URL": "https://b/base"}, "https://b/base/mcp"},
		{map[string]string{"OHS_MCP_URL": "https://b/mcp"}, "https://b/mcp"},
		{map[string]string{"OHS_MCP_URL": "http://127.0.0.1:8080"}, "http://127.0.0.1:8080/mcp"},
		{map[string]string{"OHS_MCP_URL": "http://[::1]:8080"}, "http://[::1]:8080/mcp"},
		{map[string]string{"OHS_MCP_URL": "http://ohs.localhost"}, "http://ohs.localhost/mcp"},
		{map[string]string{"OHS_MCP_URL": "http://LocalHost:8080"}, "http://LocalHost:8080/mcp"},
		{map[string]string{"OHS_MCP_URL": "http://Ohs.LOCALHOST"}, "http://Ohs.LOCALHOST/mcp"},
	} {
		got, err := mcpEndpoint(env(c.vars))
		if err != nil || got.String() != c.want {
			t.Fatalf("%v: got %v, %v; want %q", c.vars, got, err, c.want)
		}
	}
	for _, bad := range []string{"localhost:8080", "ftp://x", "http://ohs.example.org",
		"http://user:s3cret@ohs.example.org", "http://10.0.0.5:8080", "%zz",
		"user:s3cret@host:80", "ftp://user:s3cret@host"} {
		_, err := mcpEndpoint(env(map[string]string{"OHS_MCP_URL": bad}))
		if err == nil {
			t.Fatalf("%q must be refused", bad)
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Fatalf("%q: the password leaked: %v", bad, err)
		}
	}
	_, err := mcpEndpoint(env(map[string]string{"OHS_MCP_URL": "http://ohs.example.org"}))
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain http to a remote host must point to https: %v", err)
	}
}

// The bearer secret never leaves for another origin: redirects are not
// followed, and the transport only signs requests to the configured endpoint.
func TestProxyClientKeepsSecret(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
	}))
	t.Cleanup(other.Close)
	redirect := func(to string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, to+"/mcp", http.StatusTemporaryRedirect)
		})
	}

	// http endpoint redirecting to another host.
	srv := httptest.NewServer(redirect(other.URL))
	t.Cleanup(srv.Close)
	ep, _ := url.Parse(srv.URL + "/mcp")
	c := newProxyClient(ep, "ohs_sk_secret", nil)
	res, err := c.Post(ep.String(), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect followed: %d", res.StatusCode)
	}
	// Even a direct request through the client to another origin is unsigned.
	res, err = c.Get(other.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	// https endpoint redirecting to http.
	tls := httptest.NewTLSServer(redirect(other.URL))
	t.Cleanup(tls.Close)
	ep, _ = url.Parse(tls.URL + "/mcp")
	c = newProxyClient(ep, "ohs_sk_secret", tls.Client().Transport)
	res, err = c.Post(ep.String(), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("https to http redirect followed: %d", res.StatusCode)
	}
	if leaked.Load() {
		t.Fatal("the bearer reached another origin")
	}

	// The endpoint itself is signed.
	var got string
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Get("Authorization") }))
	t.Cleanup(own.Close)
	ep, _ = url.Parse(own.URL + "/mcp")
	res, err = newProxyClient(ep, "ohs_sk_secret", nil).Get(ep.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if got != "Bearer ohs_sk_secret" {
		t.Fatalf("endpoint not signed: %q", got)
	}
}

// TestMCPStdioEOF pipes a whole session from a file: stdin reaches EOF right
// after the last request, and every request must still get its response.
func TestMCPStdioEOF(t *testing.T) {
	url, key, _ := fakeOHS(t)
	bin := buildOHS(t)
	reqs := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_samples","arguments":{"type":"heart_rate"}}}`,
	}, "\n") + "\n"
	file := filepath.Join(t.TempDir(), "requests.jsonl")
	if err := os.WriteFile(file, []byte(reqs), 0o600); err != nil {
		t.Fatal(err)
	}
	for run := range 3 {
		in, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, bin, "mcp")
		cmd.Env = append(os.Environ(), "OHS_MCP_URL="+url, "OHS_MCP_KEY="+key)
		cmd.Stdin = in
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		cancel()
		_ = in.Close()
		if err != nil {
			t.Fatalf("run %d: ohs mcp: %v", run, err)
		}
		ids := map[int]bool{}
		for line := range strings.Lines(string(out)) {
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("run %d: not JSON on stdout: %q", run, line)
			}
			if m["error"] != nil {
				t.Fatalf("run %d: error response: %v", run, m)
			}
			if v, ok := m["id"].(float64); ok {
				ids[int(v)] = true
			}
		}
		if len(ids) != 3 || !ids[1] || !ids[2] || !ids[3] {
			t.Fatalf("run %d: responses for ids %v, want 1, 2 and 3; stdout:\n%s", run, ids, out)
		}
	}
}
