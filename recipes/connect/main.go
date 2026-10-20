// SPDX-License-Identifier: AGPL-3.0-only

// Connect links your accounts (Oura, WHOOP, Fitbit, Withings, ...) and shows
// how fresh each one is.
//
//	go run ./recipes/connect          # what is connected, and what can be
//	go run ./recipes/connect oura     # opens the Oura sign in page
//
// Each provider needs its keys set on the server first: see docs/connectors.
// Apple Health has no sign in: import an export with `ohs import apple-health`.
// Needs Go 1.23 or later and nothing else. Recipe page: docs/recipes/connect.md
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
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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

// source is how fresh one connected account is.
type source struct {
	Provider      string  `json:"provider"`
	State         string  `json:"state"`
	SyncedThrough *string `json:"synced_through"`
	LastError     *string `json:"last_error"`
}

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: connect [provider]\n\nConnect your accounts and see how fresh each one is.")
	}
	flag.Parse()
	var providers struct {
		Data []struct {
			Slug  string   `json:"slug"`
			Kinds []string `json:"kinds"`
		} `json:"data"`
	}
	fetch("GET", "/v1/providers", nil, nil, &providers)
	var oauth []string
	for _, p := range providers.Data {
		if slices.Contains(p.Kinds, "oauth") {
			oauth = append(oauth, p.Slug)
		}
	}
	slices.Sort(oauth)

	if name := flag.Arg(0); name != "" {
		if !slices.Contains(oauth, name) {
			can := strings.Join(oauth, ", ")
			if can == "" {
				can = "nothing yet"
			}
			fail("%s cannot be connected on this server; it can connect: %s", name, can)
		}
		var c struct {
			AuthorizeURL string `json:"authorize_url"`
		}
		fetch("POST", "/v1/users/"+user+"/connections/"+name, map[string]any{}, nil, &c)
		fmt.Printf("Sign in to %s and allow access:\n\n  %s\n\n", name, c.AuthorizeURL)
		openBrowser(c.AuthorizeURL)
		fmt.Println("Data starts arriving a few seconds after you allow it. Run go run ./recipes/connect to check.")
		return
	}

	var freshness struct {
		Data []source `json:"data"`
	}
	fetch("GET", "/v1/users/"+user+"/freshness", nil, nil, &freshness)
	slices.SortFunc(freshness.Data, func(a, b source) int { return strings.Compare(a.Provider, b.Provider) })
	connected := map[string]bool{}
	if len(freshness.Data) > 0 {
		fmt.Printf("%-16s%-14s%-22sLast error\n", "Source", "Status", "Synced through")
		for _, f := range freshness.Data {
			connected[f.Provider] = true
			through, lastErr := "-", ""
			if f.SyncedThrough != nil {
				through = strings.Replace((*f.SyncedThrough)[:min(16, len(*f.SyncedThrough))], "T", " ", 1)
			}
			if f.LastError != nil {
				lastErr = *f.LastError
			}
			fmt.Printf("%-16s%-14s%-22s%s\n", f.Provider, f.State, through, lastErr)
		}
		fmt.Println()
	}
	var todo []string
	for _, s := range oauth {
		if !connected[s] {
			todo = append(todo, s)
		}
	}
	if len(todo) > 0 {
		fmt.Println("Can connect: " + strings.Join(todo, ", ") + "  (go run ./recipes/connect <name>)")
	}
	if len(oauth) == 0 {
		fmt.Println("No provider keys are set on this server yet: see docs/connectors to add one.")
	}
}

// openBrowser opens link in the default browser, when there is one.
func openBrowser(link string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", link)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		cmd = exec.Command("xdg-open", link)
	}
	_ = cmd.Start()
}
