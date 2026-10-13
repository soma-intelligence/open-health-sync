// SPDX-License-Identifier: AGPL-3.0-only

// Package console serves the read-only admin console at /console: one HTML
// page, one JavaScript module and one stylesheet, embedded in the binary. The
// page holds no server-side state and has no auth of its own: it asks for a
// tenant API key, keeps it in the tab's sessionStorage and calls the existing
// read routes of the API (GET only) on the same origin. It is mounted only
// when OHS_CONSOLE=true.
package console

import (
	"embed"
	"net/http"
)

//go:embed static/index.html static/app.mjs static/console.css
var static embed.FS

// CSP is the Content-Security-Policy of every console response: no inline
// script or style, no third-party origin, no framing, no form submission.
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

type asset struct {
	file, contentType, cacheControl string
}

// assets are the only files served under /console/; any other path is 404,
// so there is no directory listing and no path to traverse.
var assets = map[string]asset{
	"":            {"static/index.html", "text/html; charset=utf-8", "no-store"},
	"app.mjs":     {"static/app.mjs", "text/javascript; charset=utf-8", "no-cache"},
	"console.css": {"static/console.css", "text/css; charset=utf-8", "no-cache"},
}

// Mount registers GET /console and GET /console/{asset} on mux (HEAD
// follows GET).
func Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /console", func(w http.ResponseWriter, r *http.Request) { serve(w, "") })
	mux.HandleFunc("GET /console/{asset...}", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r.PathValue("asset"))
	})
}

func serve(w http.ResponseWriter, name string) {
	h := w.Header()
	h.Set("Content-Security-Policy", CSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	a, ok := assets[name]
	if !ok {
		h.Set("Cache-Control", "no-store")
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	body, err := static.ReadFile(a.file)
	if err != nil { // cannot happen: the files are embedded at build time
		http.Error(w, "console asset missing", http.StatusInternalServerError)
		return
	}
	h.Set("Content-Type", a.contentType)
	h.Set("Cache-Control", a.cacheControl)
	_, _ = w.Write(body)
}
