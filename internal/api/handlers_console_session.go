// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/consolesession"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

type consoleSignInJSON struct {
	Key string `json:"key"`
}

type consoleTenantSwitchJSON struct {
	TenantID uuid.UUID `json:"tenant_id"`
}

type consoleTenantJSON struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Admin bool      `json:"admin"`
}

type consoleSessionJSON struct {
	CSRFToken    string            `json:"csrf_token,omitempty"`
	Tenant       consoleTenantJSON `json:"tenant"`
	ActingTenant consoleTenantJSON `json:"acting_tenant"`
	KeyPrefix    string            `json:"key_prefix"`
	KeyScopes    []string          `json:"key_scopes"`
	ReadOnly     bool              `json:"read_only"`
	ExpiresAt    time.Time         `json:"expires_at"`
}

const errNeedsSession = "this route needs a console session"

func toConsoleSession(sess consolesession.Session, csrf string) consoleSessionJSON {
	return consoleSessionJSON{
		CSRFToken:    csrf,
		Tenant:       consoleTenantJSON{ID: sess.TenantID, Name: sess.TenantName, Admin: sess.TenantAdmin},
		ActingTenant: consoleTenantJSON{ID: sess.ActingTenantID, Name: sess.ActingTenantName, Admin: sess.ActingTenantAdmin},
		KeyPrefix:    sess.KeyPrefix,
		KeyScopes:    sess.KeyScopes,
		ReadOnly:     sess.ReadOnly,
		ExpiresAt:    sess.ExpiresAt().UTC(),
	}
}

// mountConsoleSession mounts the session routes. Sign in takes the key in its
// body and no credential; the others run behind the session cookie.
func (s *server) mountConsoleSession(mux *http.ServeMux) {
	mux.HandleFunc("POST /console/session", s.signInConsole)
	s.tenantRoute(mux, "GET /console/session", s.getConsoleSession)
	s.tenantRoute(mux, "DELETE /console/session", s.signOutConsole)
	s.tenantRoute(mux, "PUT /console/session/tenant", s.switchConsoleTenant)
}

// setSessionCookie sets the console cookie, or clears it for an empty token.
// It has no Max-Age when set: the server enforces the lifetime.
func setSessionCookie(w http.ResponseWriter, token string, secure bool) {
	c := &http.Cookie{
		Name: auth.SessionCookie, Value: token, Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	}
	if token == "" {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// signInConsole opens a console session for a tenant API key.
func (s *server) signInConsole(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !sameOrigin(r, s.d.PublicURL) {
		problem(w, http.StatusForbidden, auth.DetailOrigin)
		return
	}
	if plainHTTP(r, s.d.PublicURL, s.d.TrustedProxies) {
		if s.d.PublicURL != "" {
			problem(w, http.StatusForbidden, "OHS_PUBLIC_URL is plain http on a remote host; the console needs its https address")
			return
		}
		problem(w, http.StatusForbidden, "the console signs in over https, or on localhost; set OHS_PUBLIC_URL to the https address")
		return
	}
	var in consoleSignInJSON
	if !decodeJSON(w, r, &in, false) {
		return
	}
	if len(in.Key) < len(auth.PrefixSecretKey) || in.Key[:len(auth.PrefixSecretKey)] != auth.PrefixSecretKey {
		problem(w, http.StatusForbidden, "the console needs a tenant API key (ohs_sk_), not a user token")
		return
	}
	hash := auth.Hash(in.Key)
	ctx, cancel := context.WithTimeout(r.Context(), auth.LookupTimeout)
	p, err := s.lookup(ctx, hash)
	cancel()
	var limited *auth.LimitedError
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		if wait, tooMany := s.refused(r); tooMany {
			auth.WriteTooManyRequests(w, wait)
			return
		}
		f := auth.Failure{Hash: hash}
		if len(in.Key) > auth.KeyPrefixLen {
			f.Prefix = in.Key[:auth.KeyPrefixLen]
		}
		s.authFailed(r, f)
		auth.WriteProblem(w, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized), "the key is not valid")
		return
	case errors.As(err, &limited):
		auth.WriteTooManyRequests(w, limited.RetryAfter)
		return
	case err != nil:
		internalError(w, r, "console_sign_lookup_failed", err)
		return
	}
	if !p.Can(auth.ScopeTenantRead) {
		problem(w, http.StatusForbidden, "the console needs a key with the "+auth.ScopeTenantRead+" scope")
		return
	}
	keyID, err := uuid.Parse(p.KeyID)
	if err != nil {
		internalError(w, r, "console_sign_key_id_failed", err)
		return
	}
	token, csrf, sess, err := s.sessions.Create(r.Context(), p.TenantID, keyID, s.d.ConsoleReadOnly)
	if errors.Is(err, consolesession.ErrNotFound) {
		// The key ended between the lookup and now.
		auth.WriteProblem(w, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized), "the key is not valid")
		return
	}
	if err != nil {
		internalError(w, r, "create_console_session_failed", err)
		return
	}
	setSessionCookie(w, token, cookieSecure(r, s.d.PublicURL, s.d.TrustedProxies))
	at := sess.TenantID
	s.record(r, audit.Event{
		Tenant: sess.TenantID, ActorType: audit.APIKey, ActorID: sess.APIKeyID.String(),
		Action: audit.SessionCreate, Method: r.Method, Route: r.Pattern, Status: http.StatusCreated,
		RequestID: reqid.From(r.Context()), ActorTenant: &at,
		Detail: map[string]any{"key_prefix": sess.KeyPrefix},
	})
	writeJSON(w, http.StatusCreated, toConsoleSession(sess, csrf))
}

// sessionPrincipal is the caller of a session route; a bearer key is not one.
func sessionPrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, _ := auth.From(r.Context())
	if p.SessionID == uuid.Nil {
		auth.WriteProblem(w, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized), errNeedsSession)
		return p, false
	}
	return p, true
}

// getConsoleSession answers the session with a fresh CSRF token; the old one
// stops working.
func (s *server) getConsoleSession(w http.ResponseWriter, r *http.Request) {
	p, ok := sessionPrincipal(w, r)
	if !ok {
		return
	}
	csrf, err := s.sessions.RotateCSRF(r.Context(), p.SessionID)
	if errors.Is(err, consolesession.ErrNotFound) {
		auth.WriteProblem(w, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized), errNeedsSession)
		return
	}
	if err != nil {
		internalError(w, r, "rotate_console_csrf_failed", err)
		return
	}
	sess, ok := s.cookieSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toConsoleSession(sess, csrf))
}

// cookieSession reads the session of the request's cookie.
func (s *server) cookieSession(w http.ResponseWriter, r *http.Request) (consolesession.Session, bool) {
	c, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		auth.WriteProblem(w, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized), errNeedsSession)
		return consolesession.Session{}, false
	}
	sess, err := s.sessions.Lookup(r.Context(), auth.Hash(c.Value))
	if errors.Is(err, consolesession.ErrNotFound) {
		auth.WriteProblem(w, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized), errNeedsSession)
		return consolesession.Session{}, false
	}
	if err != nil {
		internalError(w, r, "read_console_session_failed", err)
		return consolesession.Session{}, false
	}
	return sess, true
}

// signOutConsole ends the session and clears the cookie.
func (s *server) signOutConsole(w http.ResponseWriter, r *http.Request) {
	p, ok := sessionPrincipal(w, r)
	if !ok {
		return
	}
	if err := s.sessions.Delete(r.Context(), p.SessionID); err != nil {
		internalError(w, r, "delete_console_session_failed", err)
		return
	}
	setSessionCookie(w, "", cookieSecure(r, s.d.PublicURL, s.d.TrustedProxies))
	w.WriteHeader(http.StatusNoContent)
}

// switchConsoleTenant points an admin session at another tenant.
func (s *server) switchConsoleTenant(w http.ResponseWriter, r *http.Request) {
	p, ok := sessionPrincipal(w, r)
	if !ok {
		return
	}
	if !p.Admin {
		problem(w, http.StatusForbidden, "only a session of the instance admin tenant switches tenant")
		return
	}
	var in consoleTenantSwitchJSON
	if !decodeJSON(w, r, &in, false) {
		return
	}
	if _, err := s.d.Store.GetTenantSummary(r.Context(), in.TenantID); errors.Is(err, tenant.ErrNotFound) {
		notFound(w, "tenant")
		return
	} else if err != nil {
		internalError(w, r, "switch_console_tenant_failed", err)
		return
	}
	from := p.TenantID
	sess, err := s.sessions.SetActingTenant(r.Context(), p.SessionID, in.TenantID)
	switch {
	case errors.Is(err, consolesession.ErrTenantNotFound):
		notFound(w, "tenant")
		return
	case errors.Is(err, consolesession.ErrNotFound):
		auth.WriteProblem(w, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized), errNeedsSession)
		return
	case err != nil:
		internalError(w, r, "switch_console_tenant_failed", err)
		return
	}
	at := p.Actor()
	s.record(r, audit.Event{
		Tenant: p.Actor(), ActorType: audit.APIKey, ActorID: p.KeyID,
		Action: audit.SessionTenant, Method: r.Method, Route: r.Pattern, Status: http.StatusOK,
		RequestID: reqid.From(r.Context()), ActorTenant: &at,
		Detail: map[string]any{"from_tenant_id": from.String(), "to_tenant_id": in.TenantID.String()},
	})
	writeJSON(w, http.StatusOK, toConsoleSession(sess, ""))
}
