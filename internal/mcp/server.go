// SPDX-License-Identifier: AGPL-3.0-only

// Package mcp is the read-only Model Context Protocol server: Streamable HTTP
// at /mcp, on the API listener, behind the API's bearer authentication. Its
// tools mirror the REST read endpoints and call the same query layer
// (internal/api/read), so tenant isolation is enforced in one place. Its
// prompts are the prompt library of internal/prompts.
package mcp

import (
	"bytes"
	"net/http"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// Version is reported to clients in the initialize handshake (set by ohs serve).
var Version = "dev"

// Deps is what the tools read from.
type Deps struct {
	// DB runs the read queries (a *pgxpool.Pool).
	DB read.Querier
	// Store lists users for list_users.
	Store *tenant.Store
	// Webhooks serves list_webhooks; nil leaves the tool out.
	Webhooks *webhook.Store
}

// NewHandler returns the Streamable HTTP MCP handler. Mount it behind
// auth.MiddlewareAny(lookup, auth.TenantKey, auth.UserRead): every request
// needs the Principal that middleware puts on the context, and a request
// without one is refused with 401.
//
// The server is stateless: each HTTP request gets a server bound to its
// caller, so no session outlives the credential that opened it, and any
// instance can answer any request. Responses are plain JSON, no event stream.
func NewHandler(d Deps) http.Handler {
	cache := sdk.NewSchemaCache()
	var h http.Handler = sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		p, _ := auth.From(r.Context())
		return newServer(d, p, cache)
	}, &sdk.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// The localhost guard rejects requests that reach a loopback address
		// with another Host header, which is every reverse proxy in front of
		// a self-hosted instance. It defends unauthenticated local servers
		// against DNS rebinding; here every request carries a bearer secret
		// a rebinding page cannot know.
		DisableLocalhostProtection: true,
	})
	// A browser page on another site must not drive /mcp with a credential
	// the browser would attach: refuse cross-origin non-safe requests (by
	// Sec-Fetch-Site, else Origin against Host). Agents send neither header.
	h = http.NewCrossOriginProtection().Handler(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.From(r.Context()); !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			auth.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid API key or user token is required")
			return
		}
		pw := &problemWriter{ResponseWriter: w}
		h.ServeHTTP(pw, r)
		pw.finish()
	})
}

// maxErrorDetail bounds the text of a transport error kept as a problem's
// detail.
const maxErrorDetail = 512

// problemWriter turns the plain text errors of the transport and of the
// cross-origin guard (http.Error: a wrong content type, a body that is not
// JSON-RPC, a cross-site page, another method) into RFC 9457 problems, as
// every other error of the API is. Anything else passes through as written.
type problemWriter struct {
	http.ResponseWriter
	status int
	detail bytes.Buffer
}

func (w *problemWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	if code >= 400 && strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		w.status = code
		return
	}
	w.status = -1
	w.ResponseWriter.WriteHeader(code)
}

func (w *problemWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.status > 0 {
		if room := maxErrorDetail - w.detail.Len(); room > 0 {
			w.detail.Write(b[:min(len(b), room)])
		}
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// Flush lets a streamed answer through; a held error is written by finish.
func (w *problemWriter) Flush() {
	if w.status > 0 {
		return
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *problemWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *problemWriter) finish() {
	if w.status <= 0 {
		return
	}
	detail := strings.TrimSpace(w.detail.String())
	if detail == "" {
		detail = strings.ToLower(http.StatusText(w.status))
	}
	auth.WriteProblem(w.ResponseWriter, w.status, http.StatusText(w.status), detail)
}

const instructions = `Open Health Sync: read-only access to health data (quantity samples, ` +
	`category samples, sleep, workouts, nutrition, daily summaries, RR intervals and the HRV ` +
	`derived from them, menstrual cycles, ECG recordings, sync coverage) and the ` +
	`time tags that give it context (illness, travel, jet lag...). ` +
	`With a tenant API key, call list_users first and pass user_id to every other tool; ` +
	`with a personal user token, omit user_id. Times are RFC 3339 UTC or YYYY-MM-DD; ` +
	`start is inclusive, end exclusive. Lists return at most limit records (default 100, ` +
	`max 1000): when truncated is true, call again with cursor set to next_cursor. ` +
	`For trends over days or weeks start with get_daily (one row per day); raw samples ` +
	`can number thousands a day, so call get_samples with a narrow start and end. ` +
	`get_coverage shows which days and metrics hold data at all; get_tags lists the ` +
	`periods the user marked (an illness, a trip) that can explain unusual data. ` +
	`Compare a user with their own history, not population norms; when a figure is missing say so, ` +
	`never fill it in. The data is for wellness: do not diagnose; suggest a clinician when something ` +
	`keeps looking off. The server also lists prompts: ready-made questions about training, sleep and recovery.`

func newServer(d Deps, p auth.Principal, cache *sdk.SchemaCache) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "open-health-sync", Title: "Open Health Sync", Version: Version},
		&sdk.ServerOptions{Instructions: instructions, SchemaCache: cache})
	addTools(s, &tools{d: d, p: p})
	addPrompts(s)
	return s
}
