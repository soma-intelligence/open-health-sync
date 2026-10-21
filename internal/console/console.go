// SPDX-License-Identifier: AGPL-3.0-only

// Package console serves the admin console at /console (docs/console.md):
// one HTML page, two JavaScript modules and one stylesheet, embedded in the
// binary. The page holds no server-side state and has no auth of its own: it
// asks for a tenant API key, keeps it in the tab's sessionStorage and calls
// the documented API routes on the same origin, as curl would. In read-only
// mode the page refuses any request but GET and shows no action. Beside it,
// /console/setup is the separate first-run page: it posts the setup token to
// POST /v1/setup and shows the first tenant's API key once. Both are mounted
// only when OHS_CONSOLE is true or read-only.
package console

import (
	"bytes"
	"embed"
	"net/http"
)

//go:embed static/index.html static/app.mjs static/lib.mjs static/console.css static/setup.html static/setup.mjs
var static embed.FS

// Modes of the page, in its ohs-console-mode meta tag.
const (
	ModeManage   = "manage"
	ModeReadOnly = "read-only"
)

// modeTag is the meta tag of index.html that Mount sets to the mode.
const modeTag = `<meta name="ohs-console-mode" content="` + ModeManage + `">`

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
	"lib.mjs":     {"static/lib.mjs", "text/javascript; charset=utf-8", "no-cache"},
	"console.css": {"static/console.css", "text/css; charset=utf-8", "no-cache"},
	"setup":       {"static/setup.html", "text/html; charset=utf-8", "no-store"},
	"setup.mjs":   {"static/setup.mjs", "text/javascript; charset=utf-8", "no-cache"},
}

// Mount registers GET /console and GET /console/{asset} on mux (HEAD
// follows GET). readOnly serves the page without write actions.
func Mount(mux *http.ServeMux, readOnly bool) {
	page, err := static.ReadFile(assets[""].file)
	if err != nil || !bytes.Contains(page, []byte(modeTag)) {
		panic("console: index.html lacks its mode tag")
	}
	if readOnly {
		page = bytes.Replace(page, []byte(modeTag),
			[]byte(`<meta name="ohs-console-mode" content="`+ModeReadOnly+`">`), 1)
	}
	mux.HandleFunc("GET /console", func(w http.ResponseWriter, r *http.Request) { serve(w, "", page) })
	mux.HandleFunc("GET /console/{asset...}", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r.PathValue("asset"), page)
	})
}

// serve writes one asset; page is the index page in the mode of the mount.
func serve(w http.ResponseWriter, name string, page []byte) {
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
	body := page
	if name != "" {
		var err error
		if body, err = static.ReadFile(a.file); err != nil { // cannot happen: the files are embedded at build time
			http.Error(w, "console asset missing", http.StatusInternalServerError)
			return
		}
	}
	h.Set("Content-Type", a.contentType)
	h.Set("Cache-Control", a.cacheControl)
	_, _ = w.Write(body)
}
