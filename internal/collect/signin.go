// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// The sign-in page of the providers without a consent page for third parties
// (connector.PasswordConnector). Starting a connection to one of them sends
// the user here instead of to the provider: GET /oauth/{provider}/signin
// carries the OAuth state like a consent URL would, and the form posts the
// account email and password back to the same path. The password goes to the
// connector's SignIn and nowhere else: it is never logged, and the connector
// decides what of it the sealed tokens keep.
//
// A refused password renders the form again and keeps the state, so a typo
// does not restart the flow; the state is consumed by a successful sign-in,
// or by any other failure. The route shares the rate limit of the OAuth
// callback.

// Sign-in form limits.
const (
	// maxSignInForm caps the body of POST /oauth/{provider}/signin.
	maxSignInForm = 8 << 10
	// maxCredential caps the email and the password.
	maxCredential = 512
)

var signInPage = template.Must(template.ParseFS(pageFS, "page/signin.html", "page/frame.html"))

// SignInURL is the sign-in page of a password provider for one state.
func (d Deps) SignInURL(provider, state string) string {
	return d.publicBase() + "/oauth/" + url.PathEscape(provider) + "/signin?state=" + url.QueryEscape(state)
}

type signInView struct {
	Provider string
	State    string
	Email    string
	Error    string
}

// peekState reads a live state without consuming it.
func (h *Handlers) peekState(ctx context.Context, provider, state string) (oauthState, bool, error) {
	var s oauthState
	err := h.d.Pool.QueryRow(ctx, `
		SELECT tenant_id, user_id, coalesce(redirect_uri, ''), expires_at
		FROM ohs_sync.oauth_states WHERE state = $1 AND provider = $2`,
		hashState(state), provider).Scan(&s.tenant, &s.user, &s.redirect, &s.expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, false, nil
	}
	if err != nil {
		return s, false, fmt.Errorf("read oauth state: %w", err)
	}
	return s, true, nil
}

// signInState checks the state of a sign-in request and the provider it
// names. It answers the request itself (and returns false) when the state is
// unknown, used, expired or not for a password provider.
func (h *Handlers) signInState(w http.ResponseWriter, r *http.Request, provider, state string) (oauthState, connector.PasswordConnector, bool) {
	ctx := r.Context()
	oc, ok := h.d.oauth(provider)
	var pc connector.PasswordConnector
	if ok {
		pc, ok = connector.AsPassword(oc)
	}
	if !ok || state == "" {
		renderResult(w, http.StatusNotFound, provider, false)
		return oauthState{}, nil, false
	}
	st, found, err := h.peekState(ctx, provider, state)
	if err != nil {
		slog.ErrorContext(ctx, "collect: sign-in page", "provider", provider, "error", err)
		renderResult(w, http.StatusInternalServerError, provider, false)
		return st, nil, false
	}
	if !found {
		renderResult(w, http.StatusBadRequest, provider, false)
		return st, nil, false
	}
	if !h.d.now().Before(st.expiresAt) {
		// Consumed so it cannot linger; the user goes back where they came
		// from, told it failed.
		_, _, _ = h.takeState(ctx, provider, state)
		h.finish(w, r, st, provider, false, http.StatusBadRequest)
		return st, nil, false
	}
	return st, pc, true
}

// SignInPage serves GET /oauth/{provider}/signin?state=S: the form asking
// for the provider account's email and password. Provider facing like the
// callback: mount it without auth.
func (h *Handlers) SignInPage() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provider := r.PathValue("provider")
		state := r.URL.Query().Get("state")
		st, _, ok := h.signInState(w, r, provider, state)
		if !ok {
			return
		}
		renderSignIn(w, http.StatusOK, st, signInView{Provider: providerName(provider), State: state})
	})
}

// SignIn serves POST /oauth/{provider}/signin with the form fields state,
// email and password. It signs in at the provider, stores the connection and
// ends like the OAuth callback: the tenant's redirect_uri with
// status=connected|error, or the result page.
func (h *Handlers) SignIn() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		provider := r.PathValue("provider")
		r.Body = http.MaxBytesReader(w, r.Body, maxSignInForm)
		if err := r.ParseForm(); err != nil {
			renderResult(w, http.StatusBadRequest, provider, false)
			return
		}
		state := r.PostForm.Get("state")
		st, pc, ok := h.signInState(w, r, provider, state)
		if !ok {
			return
		}
		email := strings.TrimSpace(r.PostForm.Get("email"))
		password := r.PostForm.Get("password")
		view := signInView{Provider: providerName(provider), State: state, Email: email}
		if email == "" || password == "" || len(email) > maxCredential || len(password) > maxCredential ||
			!utf8.ValidString(email) || !utf8.ValidString(password) {
			view.Error = "Enter the email and password of your " + view.Provider + " account."
			renderSignIn(w, http.StatusBadRequest, st, view)
			return
		}
		log := slog.With("tenant_id", st.tenant, "user_id", st.user, "provider", provider)
		oc, err := h.ready(ctx, provider, st)
		if err != nil {
			log.WarnContext(ctx, "collect: connection failed", "error", safeErr(err))
			_, _, _ = h.takeState(ctx, provider, state)
			code := http.StatusBadRequest
			if errors.Is(err, ErrProviderOff) {
				code = http.StatusForbidden
			}
			h.finish(w, r, st, provider, false, code)
			return
		}
		tokens, err := pc.SignIn(ctx, email, password)
		switch _, limited := connector.AsRateLimit(err); {
		case errors.Is(err, connector.ErrBadCredentials):
			log.InfoContext(ctx, "collect: provider refused the sign-in")
			view.Error = view.Provider + " did not accept this email and password. Check them and try again."
			renderSignIn(w, http.StatusUnauthorized, st, view)
			return
		case limited:
			log.WarnContext(ctx, "collect: provider rate limited the sign-in")
			view.Error = view.Provider + " is not accepting sign-ins right now. Try again in a few minutes."
			renderSignIn(w, http.StatusTooManyRequests, st, view)
			return
		}
		// From here the state is spent, whatever happens: a second post of
		// the same form finds nothing.
		_, found, terr := h.takeState(ctx, provider, state)
		if terr != nil {
			log.ErrorContext(ctx, "collect: sign-in", "error", terr)
			renderResult(w, http.StatusInternalServerError, provider, false)
			return
		}
		if !found {
			renderResult(w, http.StatusBadRequest, provider, false)
			return
		}
		if err == nil {
			err = h.store(ctx, provider, st, oc, tokens)
		} else {
			err = fmt.Errorf("sign in: %w", err)
		}
		if err != nil {
			log.WarnContext(ctx, "collect: connection failed", "error", safeErr(err))
			code := http.StatusBadRequest
			if errors.Is(err, ErrProviderOff) {
				code = http.StatusForbidden
			}
			h.finish(w, r, st, provider, false, code)
			return
		}
		log.InfoContext(ctx, "collect: connection established")
		h.finish(w, r, st, provider, true, http.StatusOK)
	})
}

// renderSignIn renders the sign-in form. The form posts to the page itself,
// and a successful post redirects to the tenant's redirect_uri, whose origin
// form-action must therefore allow.
func renderSignIn(w http.ResponseWriter, code int, st oauthState, v signInView) {
	sources := "'self'"
	if u, err := url.Parse(st.redirect); err == nil && st.redirect != "" {
		if origin := u.Scheme + "://" + u.Host; cspSource.MatchString(origin) {
			sources += " " + origin
		}
	}
	setPageHeaders(w, "default-src 'none'; style-src 'unsafe-inline'; form-action "+sources+"; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(code)
	if err := signInPage.Execute(w, v); err != nil {
		slog.Error("collect: render sign-in page", "error", err)
	}
}
