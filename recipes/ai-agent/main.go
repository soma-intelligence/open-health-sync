// SPDX-License-Identifier: AGPL-3.0-only

// Ai-agent lets an AI assistant read your health data, read only, through MCP.
//
//	go run ./recipes/ai-agent      # a token valid 30 days, and the setup lines
//
// Then ask it things like "how did I sleep the weeks I trained the most?". The
// token can read, never write or delete, and stops working after 30 days.
// Needs Go 1.23 or later and nothing else. Recipe page: docs/recipes/ai-agent.md
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Connection: OHS_URL, OHS_KEY and OHS_USER from the environment, or from the
// file `ohs admin personal setup` writes (~/.ohs.env, or the path in OHS_ENV_FILE).
var baseURL, key, user = settings()

func settings() (string, string, string) {
	path := os.Getenv("OHS_ENV_FILE")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".ohs.env")
	}
	if b, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			name = strings.TrimSpace(name)
			if ok && !strings.HasPrefix(name, "#") && os.Getenv(name) == "" {
				_ = os.Setenv(name, strings.TrimSpace(value))
			}
		}
	}
	var missing []string
	for _, n := range []string{"OHS_URL", "OHS_KEY", "OHS_USER"} {
		if os.Getenv(n) == "" {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		fail("missing %s: run `ohs admin personal setup > ~/.ohs.env` first", strings.Join(missing, ", "))
	}
	return strings.TrimRight(os.Getenv("OHS_URL"), "/"), os.Getenv("OHS_KEY"), os.Getenv("OHS_USER")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

var client = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 2 * time.Minute}}

// call sends one request and returns the response; an error ends the program.
func call(method, path string, body any, params url.Values) *http.Response {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	u := baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequest(method, u, r)
	if err != nil {
		fail("%s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fail("cannot reach %s: %v", baseURL, err)
	}
	if resp.StatusCode >= 400 {
		var problem struct {
			Detail string `json:"detail"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&problem)
		resp.Body.Close()
		fail("%s", strings.TrimSpace(fmt.Sprintf("%s %s: %d %s", method, path, resp.StatusCode, problem.Detail)))
	}
	return resp
}

// fetch sends one request and decodes its JSON answer into out.
func fetch(method, path string, body any, params url.Values, out any) {
	resp := call(method, path, body, params)
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		fail("%s %s: %v", method, path, err)
	}
}

func main() {
	days := flag.Int("days", 30, "token lifetime in days, at most 30")
	flag.Parse()
	var t struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	fetch("POST", "/v1/users/"+user+"/tokens", map[string]any{"scopes": []string{"read"}, "ttl_seconds": *days * 86400}, nil, &t)
	fmt.Printf("Read only token, valid until %s. Keep it private.\n\n", t.ExpiresAt[:min(10, len(t.ExpiresAt))])
	fmt.Println("Claude Code:")
	fmt.Println()
	fmt.Printf("  claude mcp add --transport http open-health-sync %s/mcp \\\n    --header \"Authorization: Bearer %s\"\n\n", baseURL, t.Token)
	fmt.Println("Claude Desktop, or any client that starts a local command (claude_desktop_config.json):")
	fmt.Println()
	type server struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		Env     struct {
			URL string `json:"OHS_MCP_URL"`
			Key string `json:"OHS_MCP_KEY"`
		} `json:"env"`
	}
	s := server{Command: "ohs", Args: []string{"mcp"}}
	s.Env.URL, s.Env.Key = baseURL, t.Token
	config := map[string]map[string]server{"mcpServers": {"open-health-sync": s}}
	b, _ := json.MarshalIndent(config, "", "  ")
	fmt.Println(string(b))
	fmt.Println("\nOther clients: docs/mcp.md")
}
