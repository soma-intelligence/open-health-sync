// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// The first-run setup: with OHS_SETUP_TOKEN set, whoever presents the token
// creates the instance's first tenant and gets its API key, once, from a
// browser (/console/setup) instead of a terminal. The routes take no bearer
// credential; they close for good once any tenant exists.

const (
	// maxSetupBody caps the POST /v1/setup body: a token and a name.
	maxSetupBody = 4 << 10
	// maxTenantName is the longest tenant name the setup accepts, in bytes.
	maxTenantName = 100
	// defaultTenantName names the tenant when the request gives no name.
	defaultTenantName = "default"
)

type setupRequest struct {
	SetupToken string `json:"setup_token"`
	TenantName string `json:"tenant_name"`
}

type setupResponse struct {
	TenantID string `json:"tenant_id"`
	APIKey   string `json:"api_key"`
}

// mountSetup serves GET and POST /v1/setup when OHS_SETUP_TOKEN is set.
func (s *server) mountSetup(mux *http.ServeMux) {
	if s.d.SetupToken == "" {
		return
	}
	h := &setupHandler{store: s.d.Store, want: sha256.Sum256([]byte(s.d.SetupToken))}
	mux.HandleFunc("GET /v1/setup", h.status)
	// Wrong tokens are tried from one address at the pace of the OAuth
	// callback, the other unauthenticated route a browser drives.
	limit := s.liveLimiter(func(r RateLimits) (RateLimit, RateLimit) { return r.OAuth, RateLimit{} })
	mux.Handle("POST /v1/setup", s.rateLimit(groupSetup, limit, nil, http.HandlerFunc(h.create)))
}

type setupHandler struct {
	store *tenant.Store
	// want is the SHA-256 of the token: comparing digests of equal length
	// keeps the comparison constant time whatever the length presented.
	want [sha256.Size]byte
	// done is set once a tenant is seen. Tenants are never deleted, so the
	// setup never reopens and later calls skip the database.
	done atomic.Bool
}

// needed reports whether the instance still has no tenant.
func (h *setupHandler) needed(r *http.Request) (bool, error) {
	if h.done.Load() {
		return false, nil
	}
	has, err := h.store.HasTenants(r.Context())
	if err != nil {
		return false, err
	}
	if has {
		h.done.Store(true)
	}
	return !has, nil
}

func (h *setupHandler) status(w http.ResponseWriter, r *http.Request) {
	needed, err := h.needed(r)
	if err != nil {
		internalError(w, r, "api: setup status", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"needed": needed})
}

func (h *setupHandler) create(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, maxSetupBody)
	var req setupRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	// A setup already done is answered before the token is looked at, so
	// the route is no token oracle once it is closed, also on the first
	// call after a restart.
	needed, err := h.needed(r)
	if err != nil {
		internalError(w, r, "api: setup status", err)
		return
	}
	if !needed {
		setupDone(w)
		return
	}
	got := sha256.Sum256([]byte(req.SetupToken))
	if req.SetupToken == "" || subtle.ConstantTimeCompare(got[:], h.want[:]) != 1 {
		problem(w, http.StatusUnauthorized, "the setup token is wrong: it is the OHS_SETUP_TOKEN of this instance")
		return
	}
	name := strings.TrimSpace(req.TenantName)
	if name == "" {
		name = defaultTenantName
	}
	if detail := checkTenantName(name); detail != "" {
		badRequest(w, detail)
		return
	}
	ev := &audit.Event{
		ActorType: audit.System, ActorID: "setup", Action: audit.TenantCreate,
		Method: r.Method, Route: r.Pattern, Status: http.StatusCreated, RequestID: reqid.From(r.Context()),
	}
	t, key, err := h.store.CreateFirstTenant(r.Context(), name, ev)
	if errors.Is(err, tenant.ErrSetupDone) {
		h.done.Store(true)
		setupDone(w)
		return
	}
	if err != nil {
		internalError(w, r, "api: setup create tenant", err)
		return
	}
	h.done.Store(true)
	writeJSON(w, http.StatusCreated, setupResponse{TenantID: t.ID.String(), APIKey: key})
}

func setupDone(w http.ResponseWriter) {
	problem(w, http.StatusConflict, "this instance is already set up; create more keys with `ohs admin key create`")
}

// checkTenantName returns why name is refused, or "" when it is fine: valid
// UTF-8, at most maxTenantName bytes, no control character.
func checkTenantName(name string) string {
	if !utf8.ValidString(name) || len(name) > maxTenantName {
		return "tenant_name must be valid UTF-8 of at most 100 bytes"
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return "tenant_name must not contain control characters"
	}
	return ""
}
