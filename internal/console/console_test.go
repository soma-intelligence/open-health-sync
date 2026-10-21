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
	return serveMode(t, false, method, path)
}

func serveMode(t *testing.T, readOnly bool, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Mount(mux, readOnly)
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
		"/console/lib.mjs":     "text/javascript; charset=utf-8",
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
	for _, name := range []string{"index.html", "setup.html"} {
		page, err := static.ReadFile("static/" + name)
		if err != nil {
			t.Fatal(err)
		}
		html := string(page)
		if regexp.MustCompile(`<script(?:\s[^>]*)?>\s*[^<\s]`).MatchString(html) {
			t.Errorf("%s has an inline script", name)
		}
		if regexp.MustCompile(`(?i)<style|\sstyle=|\son[a-z]+=`).MatchString(html) {
			t.Errorf("%s has inline style or an event handler attribute", name)
		}
		if regexp.MustCompile(`(?i)(src|href)="(https?:)?//`).MatchString(html) {
			t.Errorf("%s loads from another origin", name)
		}
	}
}

// The scripts build the page from API data with textContent and created
// elements only, and every request goes through the one wrapper of lib.mjs,
// whose read-only mode refuses any method but GET.
func TestScriptAvoidsUnsafeSinks(t *testing.T) {
	for _, name := range []string{"app.mjs", "lib.mjs"} {
		js, err := static.ReadFile("static/" + name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(js)
		for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "localStorage", "XMLHttpRequest", "console.log"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s uses %s", name, bad)
			}
		}
	}
	app, _ := static.ReadFile("static/app.mjs")
	lib, _ := static.ReadFile("static/lib.mjs")
	if n := strings.Count(string(app), "fetch("); n != 1 {
		t.Errorf("app.mjs calls fetch %d times; every request goes through makeApi", n)
	}
	if strings.Contains(string(lib), "fetch(") {
		t.Error("lib.mjs calls fetch itself; it takes the fetch to use")
	}
	if !strings.Contains(string(app), "makeApi((u, o) => window.fetch(u, o), getKey, { readOnly })") {
		t.Error("app.mjs does not build its one request wrapper in the page's mode")
	}
	for _, want := range []string{`credentials: "omit"`, `redirect: "error"`, `referrerPolicy: "no-referrer"`} {
		if !strings.Contains(string(lib), want) {
			t.Errorf("lib.mjs requests lack %s", want)
		}
	}
}

func TestModeInPage(t *testing.T) {
	for readOnly, want := range map[bool]string{false: ModeManage, true: ModeReadOnly} {
		rec := serveMode(t, readOnly, "GET", "/console")
		if rec.Code != http.StatusOK {
			t.Fatalf("readOnly %v: %d", readOnly, rec.Code)
		}
		tag := `<meta name="ohs-console-mode" content="` + want + `">`
		if !strings.Contains(rec.Body.String(), tag) {
			t.Errorf("readOnly %v: the page lacks %s", readOnly, tag)
		}
	}
	// The script treats a page without the manage tag as read only.
	app, _ := static.ReadFile("static/app.mjs")
	if !strings.Contains(string(app), `readOnly = !meta || meta.getAttribute("content") !== "manage"`) {
		t.Error("app.mjs does not default to read only")
	}
}

func TestSetupPageServed(t *testing.T) {
	rec := serveConsole(t, "GET", "/console/setup")
	if rec.Code != http.StatusOK {
		t.Fatalf("/console/setup: %d", rec.Code)
	}
	assertSecurityHeaders(t, rec)
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control %q", got)
	}
	if !strings.Contains(rec.Body.String(), `src="/console/setup.mjs"`) {
		t.Error("setup page lacks its script")
	}
	rec = serveConsole(t, "GET", "/console/setup.mjs")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("/console/setup.mjs: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	assertSecurityHeaders(t, rec)
	if rec := serveConsole(t, "POST", "/console/setup"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /console/setup: %d", rec.Code)
	}
}

// The setup script shows the key with textContent only and makes one kind
// of request: to /v1/setup on this origin, without cookies.
func TestSetupScriptAvoidsUnsafeSinks(t *testing.T) {
	js, err := static.ReadFile("static/setup.mjs")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "localStorage", "XMLHttpRequest", "console.log"} {
		if strings.Contains(src, bad) {
			t.Errorf("setup.mjs uses %s", bad)
		}
	}
	if n := strings.Count(src, "fetch("); n != 1 {
		t.Errorf("setup.mjs calls fetch %d times; every request goes through makeSetupClient", n)
	}
	if !strings.Contains(src, `SETUP_PATH = "/v1/setup"`) || !strings.Contains(src, `credentials: "omit"`) || !strings.Contains(src, `redirect: "error"`) {
		t.Error("setup.mjs does not pin its path, cookies and redirects")
	}
	for _, m := range []string{`"PUT"`, `"PATCH"`, `"DELETE"`} {
		if strings.Contains(src, m) {
			t.Errorf("setup.mjs mentions method %s", m)
		}
	}
}
