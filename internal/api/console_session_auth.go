// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/consolesession"
	"github.com/soma-intelligence/open-health-sync/internal/logging"
)

// sessionExempt are the patterns a read only session may call with a write
// method: signing out and choosing the tenant to view change no data.
var sessionExempt = map[string]bool{"DELETE /console/session": true, "PUT /console/session/tenant": true}

// sessionOwnEvent are the patterns that write their own audit event: the
// request audit records nothing for them.
var sessionOwnEvent = map[string]bool{"POST /console/session": true, "PUT /console/session/tenant": true}

// sessionAuth resolves the token of the console cookie for request r. It is
// installed on the auth slot only when the console is on. An error it returns
// never holds the token, its hash or the CSRF token: the middleware logs it.
func (s *server) sessionAuth(r *http.Request, token string) (auth.Principal, error) {
	sess, err := s.sessions.Lookup(r.Context(), auth.Hash(token))
	if errors.Is(err, consolesession.ErrNotFound) {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	if err != nil {
		return auth.Principal{}, err
	}
	if !sameOrigin(r, s.d.PublicURL) {
		return auth.Principal{}, &auth.ForbiddenError{Detail: auth.DetailOrigin}
	}
	safe := r.Method == http.MethodGet || r.Method == http.MethodHead
	if !safe && r.Method != http.MethodOptions {
		got := auth.Hash(r.Header.Get(auth.CSRFHeader))
		if subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRFHash)) != 1 {
			return auth.Principal{}, &auth.ForbiddenError{Detail: auth.DetailCSRF}
		}
	}
	if sess.ReadOnly && !safe && !sessionExempt[r.Pattern] {
		return auth.Principal{}, &auth.ForbiddenError{Detail: auth.DetailReadOnly}
	}
	p := auth.Principal{
		TenantID:      sess.ActingTenantID,
		KeyID:         sess.APIKeyID.String(),
		Admin:         sess.TenantAdmin,
		KeyScopes:     sess.KeyScopes,
		ActorTenantID: sess.TenantID,
		SessionID:     sess.ID,
	}
	// The cookie request draws on the bucket of its key, as a bearer
	// request of that key does.
	if l := s.apiLimit.current(); l.perAddr.on() {
		if ok, wait, _ := l.allow(limitKey(p), ""); !ok {
			s.metrics.rateLimited[groupAPI].Add(1)
			logging.Ctx(r.Context()).Debug().Str("route_group", groupAPI).Msg("rate_limited")
			return auth.Principal{}, &auth.LimitedError{RetryAfter: wait}
		}
	}
	return p, nil
}

// originOf is scheme://host of a URL, lower case, with the default port of
// the scheme dropped.
func originOf(scheme, host string) string {
	scheme, host = strings.ToLower(scheme), strings.ToLower(host)
	switch {
	case scheme == "http" && strings.HasSuffix(host, ":80"):
		host = strings.TrimSuffix(host, ":80")
	case scheme == "https" && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443")
	}
	return scheme + "://" + host
}

// sameOrigin reports whether the Origin header of r, when there is one, is
// the origin of this server: the public URL's when set, else the request's
// own Host. No Origin header (a same origin GET, a non browser client) is
// the same origin; "null" is not.
func sameOrigin(r *http.Request, publicURL string) bool {
	vals := r.Header.Values("Origin")
	if len(vals) == 0 {
		return true
	}
	if len(vals) > 1 {
		return false
	}
	o, err := url.Parse(vals[0])
	if err != nil || o.Scheme == "" || o.Host == "" || (o.Path != "" && o.Path != "/") || o.User != nil {
		return false
	}
	if publicURL != "" {
		p, err := url.Parse(publicURL)
		if err != nil || p.Host == "" {
			return false
		}
		return originOf(o.Scheme, o.Host) == originOf(p.Scheme, p.Host)
	}
	return strings.EqualFold(o.Host, r.Host)
}

// localHost reports whether host (with or without a port) is the machine
// itself: localhost, 127.0.0.1 or ::1.
func localHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// requestHTTPS reports whether r reached the server over TLS, directly or
// through a trusted proxy that says so.
func requestHTTPS(r *http.Request, trusted []netip.Prefix) bool {
	if r.TLS != nil {
		return true
	}
	peer, ok := parseHop(r.RemoteAddr)
	if !ok || !isTrusted(peer, trusted) {
		return false
	}
	vals := strings.Split(strings.Join(r.Header.Values("X-Forwarded-Proto"), ","), ",")
	return strings.EqualFold(strings.TrimSpace(vals[len(vals)-1]), "https")
}

// cookieSecure reports whether the console cookie carries Secure: always,
// but for an http public URL on this machine or, without a public URL, a
// plain http request to this machine.
func cookieSecure(r *http.Request, publicURL string, trusted []netip.Prefix) bool {
	if publicURL != "" {
		p, err := url.Parse(publicURL)
		if err != nil {
			return true
		}
		return !strings.EqualFold(p.Scheme, "http") || !localHost(p.Host)
	}
	return requestHTTPS(r, trusted) || !localHost(r.Host)
}

// plainHTTP reports whether the console is served over plain http to a
// remote host: the session cookie would be refused by browsers or travel in
// the clear. With a public URL it is the public URL that says so (plain http
// on a remote host, or one that does not parse); without one, the request.
func plainHTTP(r *http.Request, publicURL string, trusted []netip.Prefix) bool {
	if publicURL != "" {
		p, err := url.Parse(publicURL)
		if err != nil {
			return true
		}
		return strings.EqualFold(p.Scheme, "http") && !localHost(p.Host)
	}
	return !requestHTTPS(r, trusted) && !localHost(r.Host)
}
