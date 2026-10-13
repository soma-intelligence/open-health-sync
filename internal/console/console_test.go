// SPDX-License-Identifier: AGPL-3.0-only

package console

import (
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

func TestPageServedWithHeaders(t *testing.T) {
	for _, p := range []string{"/console", "/console/"} {
		rec := serveConsole(t, "GET", p)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, rec.Code)
		}
		assertSecurityHeaders(t, rec)
		if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("%s: Content-Type %q", p, got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q", p, got)
		}
		body := rec.Body.String()
		for _, want := range []string{`src="/console/app.mjs"`, `href="/console/console.css"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: page lacks %s", p, want)
			}
		}
	}
}

func TestHeadFollowsGet(t *testing.T) {
	rec := serveConsole(t, "HEAD", "/console")
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD: %d", rec.Code)
	}
}

func TestAssetsServed(t *testing.T) {
	for path, ct := range map[string]string{
		"/console/app.mjs":     "text/javascript; charset=utf-8",
		"/console/console.css": "text/css; charset=utf-8",
	} {
		rec := serveConsole(t, "GET", path)
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Fatalf("%s: %d, %d bytes", path, rec.Code, rec.Body.Len())
		}
		if got := rec.Header().Get("Content-Type"); got != ct {
			t.Errorf("%s: Content-Type %q, want %q", path, got, ct)
		}
		assertSecurityHeaders(t, rec)
	}
}

func TestUnknownAssetIs404(t *testing.T) {
	for _, p := range []string{"/console/nope.js", "/console/static/app.mjs", "/console/console.go", "/console/app.mjs/x", "/console/%2e%2e/console.go", "/consolex"} {
		rec := serveConsole(t, "GET", p)
		if p == "/consolex" {
			// Not a console route at all: the mux has no match.
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: %d", p, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, rec.Code)
		}
		assertSecurityHeaders(t, rec)
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

// The page carries no inline script, style or event handler (the CSP would
// block them anyway) and loads nothing from another origin.
func TestPageHasNoInlineCode(t *testing.T) {
	page, err := static.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if regexp.MustCompile(`<script(?:\s[^>]*)?>\s*[^<\s]`).MatchString(html) {
		t.Error("index.html has an inline script")
	}
	if regexp.MustCompile(`(?i)<style|\sstyle=|\son[a-z]+=`).MatchString(html) {
		t.Error("index.html has inline style or an event handler attribute")
	}
	if regexp.MustCompile(`(?i)(src|href)="(https?:)?//`).MatchString(html) {
		t.Error("index.html loads from another origin")
	}
}

// The script builds the page from API data with textContent and created
// elements only, and never issues a request other than through its GET-only
// wrapper.
func TestScriptAvoidsUnsafeSinks(t *testing.T) {
	js, err := static.ReadFile("static/app.mjs")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "localStorage", "XMLHttpRequest"} {
		if strings.Contains(src, bad) {
			t.Errorf("app.mjs uses %s", bad)
		}
	}
	if n := strings.Count(src, "fetch("); n != 1 {
		t.Errorf("app.mjs calls fetch %d times; every request goes through makeGet", n)
	}
	for _, m := range []string{`"POST"`, `"PUT"`, `"PATCH"`, `"DELETE"`} {
		if strings.Contains(src, m) {
			t.Errorf("app.mjs mentions method %s", m)
		}
	}
}
