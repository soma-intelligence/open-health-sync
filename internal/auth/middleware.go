// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/db"
)

// Scope is what a route demands of the caller.
type Scope string

const (
	// TenantKey demands a tenant API key (ohs_sk_). User tokens are refused.
	TenantKey Scope = "tenant"
	// TenantAdmin demands an API key of the instance admin tenant: the
	// routes that change instance-wide settings (/v1/admin).
	TenantAdmin Scope = "admin"
	// The user scopes demand a user token (ohs_ut_) holding that scope. Tenant keys are refused.
	UserUpload  Scope = "upload"
	UserConnect Scope = "connect"
	UserRead    Scope = "read"
)

// Principal is the authenticated caller. For an API key UserID and Scopes are nil.
type Principal struct {
	TenantID uuid.UUID
	UserID   *uuid.UUID
	Scopes   []string
	// KeyID names the credential in the audit log without revealing it: the
	// API key's id, or the first characters of a user token's hash.
	KeyID string
	// Admin is true for an API key of the instance admin tenant.
	Admin bool
}

// ErrUnauthenticated is what a Lookup returns for an unknown, revoked or expired
// secret, or one whose user is being deleted. Any other error is a server fault.
var ErrUnauthenticated = errors.New("auth: unauthenticated")

// Lookup resolves the SHA-256 hex hash of a presented secret.
type Lookup func(ctx context.Context, hash string) (Principal, error)

type ctxKey struct{}

type slotKey struct{}

// Slot receives the Principal a middleware further in authenticates, so a
// wrapper outside it (the audit log) learns who called.
type Slot struct {
	p      Principal
	ok     bool
	failed *Failure
	// onFailure is asked about each refused credential (OnFailure).
	onFailure func(Failure) (retryAfter time.Duration, limited bool)
}

// Failure describes a well formed credential the lookup refused (401), for
// the audit log. It never holds the secret.
type Failure struct {
	// Hash is the SHA-256 hex hash of the presented secret.
	Hash string
	// Prefix is the start of a presented API key, the part kept in clear to
	// tell keys apart (KeyPrefixLen characters). Empty for a user token.
	Prefix string
	// UserToken is true for a user token, false for an API key.
	UserToken bool
}

// KeyPrefixLen is how many characters of an API key are kept in clear: the
// prefix of listings and of the audit log.
const KeyPrefixLen = 12

// Failure returns the refused credential of the request, if there was one.
func (s *Slot) Failure() (Failure, bool) {
	if s.failed == nil {
		return Failure{}, false
	}
	return *s.failed, true
}

// OnFailure sets fn, which the middleware asks about a credential the
// lookup refused, after the lookup and before it answers: when fn reports
// limited, the answer is 429 with Retry-After instead of 401 (a caller that
// presented too many refused credentials). A credential the lookup accepts
// never reaches fn, so it is never refused by it.
func (s *Slot) OnFailure(fn func(Failure) (retryAfter time.Duration, limited bool)) {
	s.onFailure = fn
}

// LimitedError is what a Lookup returns for a credential that is valid but
// over its rate limit: the middleware answers 429 with Retry-After.
type LimitedError struct {
	// RetryAfter is how long until a request can pass.
	RetryAfter time.Duration
}

func (e *LimitedError) Error() string { return "auth: rate limited" }

// WithSlot returns ctx carrying a new, empty Slot.
func WithSlot(ctx context.Context) (context.Context, *Slot) {
	s := &Slot{}
	return context.WithValue(ctx, slotKey{}, s), s
}

// Principal returns the Principal authenticated during the request, if any.
func (s *Slot) Principal() (Principal, bool) { return s.p, s.ok }

// From returns the Principal the middleware put on the context.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// WithPrincipal returns ctx carrying p as the caller that handlers further
// in see, for a route that serves a tenant route's handler on behalf of
// another tenant (an instance admin reading one tenant's data). The Slot is
// left as it is: the request stays recorded as the one who authenticated.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// LookupTimeout bounds the credential lookup of a request. Past it the
// request answers 503 with Retry-After, like a database that refuses
// connections.
var LookupTimeout = 10 * time.Second

// Middleware authenticates the bearer secret with lookup and enforces want.
func Middleware(lookup Lookup, want Scope) func(http.Handler) http.Handler {
	return MiddlewareAny(lookup, want)
}

// MiddlewareAny authenticates the bearer secret with lookup and lets the
// request through when the credential satisfies at least one of wants (for
// example a tenant key or a user token with scope read). It panics without a
// scope: that is a wiring mistake.
func MiddlewareAny(lookup Lookup, wants ...Scope) func(http.Handler) http.Handler {
	if len(wants) == 0 {
		panic("auth: MiddlewareAny needs at least one scope")
	}
	forbidden := forbiddenDetail(wants)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Whatever a route behind a credential answers is for that
			// caller alone: no cache on the way may keep it. A handler that
			// knows better sets its own value.
			w.Header().Set("Cache-Control", "no-store")
			raw, ok := bearer(r)
			if !ok || (!strings.HasPrefix(raw, PrefixSecretKey) && !strings.HasPrefix(raw, PrefixUserToken)) {
				unauthorized(w)
				return
			}
			// A database that does not answer (a frozen host, a network
			// partition: no reset, no error) would hold the request, its
			// goroutine and a pool connection for as long as it stays
			// silent. The lookup is one indexed read; past LookupTimeout it
			// is an outage.
			lctx, cancel := context.WithTimeout(r.Context(), LookupTimeout)
			p, err := lookup(lctx, Hash(raw))
			timedOut := err != nil && errors.Is(lctx.Err(), context.DeadlineExceeded) && r.Context().Err() == nil
			cancel()
			if timedOut {
				slog.ErrorContext(r.Context(), "auth: lookup timed out", "timeout", LookupTimeout.String())
				WriteUnavailable(w, "the database did not answer in time; retry in a few seconds")
				return
			}
			if errors.Is(err, ErrUnauthenticated) {
				if s, ok := r.Context().Value(slotKey{}).(*Slot); ok {
					f := &Failure{Hash: Hash(raw), UserToken: strings.HasPrefix(raw, PrefixUserToken)}
					if !f.UserToken && len(raw) > KeyPrefixLen {
						f.Prefix = raw[:KeyPrefixLen]
					}
					s.failed = f
					if s.onFailure != nil {
						if wait, limited := s.onFailure(*f); limited {
							WriteTooManyRequests(w, wait)
							return
						}
					}
				}
				unauthorized(w)
				return
			}
			var limited *LimitedError
			if errors.As(err, &limited) {
				WriteTooManyRequests(w, limited.RetryAfter)
				return
			}
			if err != nil {
				// The cause may name infrastructure; it is logged, never sent. No token or hash.
				slog.ErrorContext(r.Context(), "auth: lookup failed", "error", err)
				if db.Unavailable(err) {
					WriteUnavailable(w, "the database is unavailable; retry in a few seconds")
					return
				}
				WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "authentication is temporarily unavailable")
				return
			}
			if !slices.ContainsFunc(wants, func(want Scope) bool { return allowed(p, want) }) {
				WriteProblem(w, http.StatusForbidden, "Forbidden", forbidden)
				return
			}
			if s, ok := r.Context().Value(slotKey{}).(*Slot); ok {
				s.p, s.ok = p, true
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
		})
	}
}

// forbiddenDetail tells a caller with the wrong credential which one the
// endpoint takes.
func forbiddenDetail(wants []Scope) string {
	parts := make([]string, 0, len(wants))
	for _, w := range wants {
		switch w {
		case TenantKey:
			parts = append(parts, "a tenant API key ("+PrefixSecretKey+")")
		case TenantAdmin:
			parts = append(parts, "an API key of the instance admin tenant (ohs admin tenant list shows it)")
		default:
			parts = append(parts, "a user token ("+PrefixUserToken+") with scope "+string(w))
		}
	}
	return "this endpoint needs " + strings.Join(parts, " or ")
}

func allowed(p Principal, want Scope) bool {
	switch want {
	case TenantKey:
		return p.UserID == nil
	case TenantAdmin:
		return p.UserID == nil && p.Admin
	}
	return p.UserID != nil && slices.Contains(p.Scopes, string(want))
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) <= len(p) || !strings.EqualFold(h[:len(p)], p) {
		return "", false
	}
	return strings.TrimSpace(h[len(p):]), true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid API key or user token is required")
}
