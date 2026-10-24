// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

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
	p  Principal
	ok bool
}

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
			raw, ok := bearer(r)
			if !ok || (!strings.HasPrefix(raw, PrefixSecretKey) && !strings.HasPrefix(raw, PrefixUserToken)) {
				unauthorized(w)
				return
			}
			p, err := lookup(r.Context(), Hash(raw))
			if errors.Is(err, ErrUnauthenticated) {
				unauthorized(w)
				return
			}
			if err != nil {
				// The cause may name infrastructure; it is logged, never sent. No token or hash.
				slog.ErrorContext(r.Context(), "auth: lookup failed", "error", err)
				if db.Unavailable(err) {
					WriteUnavailable(w)
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
