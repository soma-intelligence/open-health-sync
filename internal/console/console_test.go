// SPDX-License-Identifier: AGPL-3.0-only

package console

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func serveConsole(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func distFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(dist, "dist/"+name)
	if err != nil {
		t.Fatalf("dist/%s: %v (run make console)", name, err)
	}
	return b
}

func assertSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	for _, d := range []string{
		"default-src 'self'", "script-src 'self'", "style-src 'self'", "connect-src 'self'",
		"img-src 'self' data:", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'none'",
	} {
		if !strings.Contains(h.Get("Content-Security-Policy"), d) {
			t.Errorf("CSP %q lacks %q", h.Get("Content-Security-Policy"), d)
		}
	}
	if strings.Contains(h.Get("Content-Security-Policy"), "unsafe") {
		t.Errorf("CSP allows unsafe sources: %q", h.Get("Content-Security-Policy"))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", h.Get("X-Content-Type-Options"))
	}
	if h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", h.Get("Referrer-Policy"))
	}
}

func TestIndexServedForConsoleRoutes(t *testing.T) {
	index := string(distFile(t, "index.html"))
	for _, p := range []string{
		"/console", "/console/", "/console/overview", "/console/users/0b6c/connections",
		"/console/settings/webhooks/OHS_WEBHOOK_PAUSE_AFTER",
	} {
		rec := serveConsole(t, "GET", p)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, rec.Code)
		}
		assertSecurityHeaders(t, rec)
		if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("%s: Content-Type %q", p, got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control %q", p, got)
		}
		if rec.Body.String() != index {
			t.Errorf("%s: body is not dist/index.html", p)
		}
	}
}

var hashedAsset = regexp.MustCompile(`^assets/[A-Za-z0-9_.-]+-[A-Za-z0-9_-]{8}\.(js|css|woff2)$`)

func TestHashedAssetsImmutable(t *testing.T) {
	types := map[string]string{
		".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".woff2": "font/woff2",
	}
	entries, err := fs.ReadDir(dist, "dist/assets")
	if err != nil || len(entries) == 0 {
		t.Fatalf("dist/assets: %v, %d files", err, len(entries))
	}
	for _, e := range entries {
		name := "assets/" + e.Name()
		if !hashedAsset.MatchString(name) {
			t.Errorf("%s is not a content hashed asset name", name)
			continue
		}
		rec := serveConsole(t, "GET", "/console/"+name)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d", name, rec.Code)
			continue
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("%s: Cache-Control %q", name, got)
		}
		ext := name[strings.LastIndex(name, "."):]
		if got := rec.Header().Get("Content-Type"); got != types[ext] {
			t.Errorf("%s: Content-Type %q, want %q", name, got, types[ext])
		}
		assertSecurityHeaders(t, rec)
	}
}

// A stale page asking for a hashed file that is gone must not get HTML back:
// the browser would run it as script.
func TestMissingAssetIs404(t *testing.T) {
	for _, p := range []string{"/console/assets/nope-12345678.js", "/console/assets/"} {
		rec := serveConsole(t, "GET", p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q", p, got)
		}
		if strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("%s: answered the index", p)
		}
		assertSecurityHeaders(t, rec)
	}
}

func TestTraversalNeverLeavesDist(t *testing.T) {
	for _, p := range []string{"/console/%2e%2e/console.go", "/console/../console.go", "/console/dist/index.html"} {
		rec := serveConsole(t, "GET", p)
		if rec.Code >= 500 {
			t.Errorf("%s: %d", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "package console") {
			t.Errorf("%s: answered a Go file", p)
		}
	}
}

// The page carries no inline script, style or event handler (the CSP would
// block them anyway), loads its scripts as modules from /console/assets/ and
// nothing from another origin.
func TestDistHasNoInlineCode(t *testing.T) {
	for _, name := range []string{"index.html"} {
		html := string(distFile(t, name))
		if regexp.MustCompile(`<script(?:\s[^>]*)?>\s*[^<\s]`).MatchString(html) {
			t.Errorf("%s has an inline script", name)
		}
		if regexp.MustCompile(`(?i)<style|\sstyle=|\son[a-z]+=`).MatchString(html) {
			t.Errorf("%s has inline style or an event handler attribute", name)
		}
		if regexp.MustCompile(`(?i)(src|href)="(https?:)?//`).MatchString(html) {
			t.Errorf("%s loads from another origin", name)
		}
		tags := regexp.MustCompile(`<script\b[^>]*>`).FindAllString(html, -1)
		if len(tags) == 0 {
			t.Errorf("%s has no script", name)
		}
		for _, tag := range tags {
			if !strings.Contains(tag, `type="module"`) || !regexp.MustCompile(`\ssrc="/console/assets/`).MatchString(tag) {
				t.Errorf("%s: script %s is not a module under /console/assets/", name, tag)
			}
		}
	}
}

func TestDistLoadsNothingFromElsewhere(t *testing.T) {
	remote := regexp.MustCompile(`(?:https?:)?//[A-Za-z0-9]`)
	files := []string{"index.html"}
	entries, _ := fs.ReadDir(dist, "dist/assets")
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".css") {
			files = append(files, "assets/"+e.Name())
		}
	}
	for _, f := range files {
		if loc := remote.FindString(string(distFile(t, f))); loc != "" {
			t.Errorf("%s references another origin: %q", f, loc)
		}
	}
}

func TestHeadFollowsGet(t *testing.T) {
	rec := serveConsole(t, "HEAD", "/console")
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD: %d", rec.Code)
	}
}

func TestOnlyGetIsServed(t *testing.T) {
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		rec := serveConsole(t, m, "/console")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /console: %d", m, rec.Code)
		}
	}
}

// /console/setup is the React app's index; the old script path is gone.
func TestSetupRouteServesTheApp(t *testing.T) {
	rec := serveConsole(t, "GET", "/console/setup")
	if rec.Code != http.StatusOK {
		t.Fatalf("/console/setup: %d", rec.Code)
	}
	assertSecurityHeaders(t, rec)
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control %q", got)
	}
	if rec.Body.String() != string(distFile(t, "index.html")) {
		t.Error("/console/setup is not dist/index.html")
	}
	rec = serveConsole(t, "GET", "/console/setup.mjs")
	if rec.Code != http.StatusNotFound {
		t.Errorf("/console/setup.mjs: %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<html") {
		t.Error("/console/setup.mjs answered the index")
	}
	if rec := serveConsole(t, "POST", "/console/setup"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /console/setup: %d", rec.Code)
	}
}
