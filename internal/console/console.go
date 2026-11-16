// SPDX-License-Identifier: AGPL-3.0-only

// Package console serves the admin console at /console (docs/console.md): a
// React single page app, built by Vite from web/console into dist/ and
// embedded in the binary. The page holds no server-side state and has no auth
// of its own: it asks for a tenant API key once, trades it at POST
// /console/session for an HttpOnly session cookie and a CSRF token it keeps in
// memory, and calls the documented API routes on the same origin with them.
// Every path under /console that is not a built file answers index.html (the
// client router reads the URL), except under assets/, where a missing file is
// a plain 404 so a stale page never runs HTML as script. Hashed files under
// assets/ are immutable; everything else is revalidated. Beside it,
// /console/setup is the old first-run page (temporary, replaced by the React
// console): it posts the setup token to POST /v1/setup and shows the first
// tenant's API key once. Both are mounted only when OHS_CONSOLE is true or
// read-only.
package console

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist is the Vite build (make console); it is committed so a plain go build
// needs no Node.
//
//go:embed all:dist
var dist embed.FS

// static holds the old setup page, served until the React console replaces it.
//
//go:embed static/setup.html static/setup.mjs
var static embed.FS

// CSP is the Content-Security-Policy of every console response: no inline
// script or style, no third-party origin, no framing, no form submission.
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

const (
	cacheRevalidate = "no-cache"
	cacheImmutable  = "public, max-age=31536000, immutable"
)

// contentTypes are the only extensions served from dist; a file with another
// extension is 404.
var contentTypes = map[string]string{
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".woff2": "font/woff2",
	".svg":   "image/svg+xml",
	".html":  "text/html; charset=utf-8",
}

// Mount registers GET /console and GET /console/{path...} on mux (HEAD follows
// GET). The more specific /console/session routes keep their own handlers.
func Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /console", func(w http.ResponseWriter, r *http.Request) { serve(w, "") })
	mux.HandleFunc("GET /console/{path...}", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r.PathValue("path"))
	})
}

// serve writes the answer for name, the path under /console/.
func serve(w http.ResponseWriter, name string) {
	h := w.Header()
	h.Set("Content-Security-Policy", CSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	name = strings.TrimSuffix(name, "/")
	if name != "" && !fs.ValidPath(name) {
		notFound(w)
		return
	}
	switch name {
	case "setup": // temporary: the old setup page, removed with its replacement
		writeFrom(w, static, "static/setup.html", contentTypes[".html"], "no-store")
		return
	case "setup.mjs":
		writeFrom(w, static, "static/setup.mjs", contentTypes[".js"], cacheRevalidate)
		return
	}
	if name != "" {
		if info, err := fs.Stat(dist, path.Join("dist", name)); err == nil && !info.IsDir() {
			ct, ok := contentTypes[path.Ext(name)]
			if !ok {
				notFound(w)
				return
			}
			cache := cacheRevalidate
			if strings.HasPrefix(name, "assets/") {
				cache = cacheImmutable
			}
			writeFrom(w, dist, path.Join("dist", name), ct, cache)
			return
		}
		if name == "assets" || strings.HasPrefix(name, "assets/") {
			notFound(w)
			return
		}
	}
	writeFrom(w, dist, "dist/index.html", contentTypes[".html"], cacheRevalidate)
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "404 page not found", http.StatusNotFound)
}

func writeFrom(w http.ResponseWriter, fsys fs.FS, file, contentType, cache string) {
	body, err := fs.ReadFile(fsys, file)
	if err != nil { // dist has no index.html: the build was not run
		http.Error(w, "console assets missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cache)
	_, _ = w.Write(body)
}
