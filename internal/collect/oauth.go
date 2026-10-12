// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// StateTTL is how long an OAuth state stays valid.
const StateTTL = 10 * time.Minute

// maxStartBody caps the StartConnection request body.
const maxStartBody = 64 << 10

// maxRedirectURI caps the tenant's redirect_uri.
const maxRedirectURI = 2048

//go:embed page/result.html
var pageFS embed.FS

var resultPage = template.Must(template.ParseFS(pageFS, "page/result.html"))

// StartRequest is the StartConnection body. It may be empty.
type StartRequest struct {
	RedirectURI string `json:"redirect_uri"`
}

// StartResponse is the StartConnection answer.
type StartResponse struct {
	AuthorizeURL string `json:"authorize_url"`
}

// CallbackURL is where a provider sends the user back after consent.
func (d Deps) CallbackURL(provider string) string {
	return d.publicBase() + "/oauth/" + url.PathEscape(provider) + "/callback"
}

// InboundURL is where a provider sends its notifications (before any
// decoration the connector adds, such as a signature).
func (d Deps) InboundURL(provider string) string {
	return d.publicBase() + "/inbound/" + url.PathEscape(provider)
}

func (d Deps) publicBase() string {
	base := strings.TrimRight(d.PublicURL, "/")
	if base == "" {
		base = "http://localhost:8080"
	}
	return base
}

// hashState is how a state is stored: only its SHA-256, so a database read
// does not hand out live states.
func hashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

func newState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// validRedirect accepts an absolute https URL without credentials; plain
// http only for a loopback host (local development).
func validRedirect(s string) bool {
	if len(s) > maxRedirectURI {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return true
		}
	}
	return false
}

// StartConnection serves POST /v1/users/{id}/connections/{provider}. Mount
// behind auth.Middleware with auth.TenantKey. Body {"redirect_uri": "..."}
// (optional); answers 200 {"authorize_url": "..."}. The state it embeds is
// single use and expires after StateTTL. Each call mints a fresh state, so a
// retried request is harmless.
func (h *Handlers) StartConnection() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID, userID, ok := h.caller(w, r)
		if !ok {
			return
		}
		provider := r.PathValue("provider")
		oc, ok := h.d.oauth(provider)
		if !ok {
			writeNotFound(w, "this provider is not available on this instance")
			return
		}
		var req StartRequest
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStartBody))
		if err != nil {
			auth.WriteProblem(w, http.StatusBadRequest, "Bad Request", "the body could not be read")
			return
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				auth.WriteProblem(w, http.StatusBadRequest, "Bad Request", "the body must be a JSON object")
				return
			}
		}
		if req.RedirectURI != "" && !validRedirect(req.RedirectURI) {
			auth.WriteProblem(w, http.StatusBadRequest, "Bad Request", "redirect_uri must be an absolute https URL (http only for localhost)")
			return
		}

		state, err := newState()
		if err != nil {
			writeInternal(w)
			return
		}
		authorize, err := oc.AuthorizeURL(state, h.d.CallbackURL(provider))
		if err != nil {
			slog.ErrorContext(r.Context(), "collect: authorize url", "provider", provider, "error", safeErr(err))
			writeInternal(w)
			return
		}
		var redirect *string
		if req.RedirectURI != "" {
			redirect = &req.RedirectURI
		}
		if _, err := h.d.Pool.Exec(r.Context(), `
			INSERT INTO ohs_sync.oauth_states (state, tenant_id, user_id, provider, redirect_uri, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			hashState(state), tenantID, userID, provider, redirect, h.d.now().Add(StateTTL)); err != nil {
			slog.ErrorContext(r.Context(), "collect: store oauth state", "tenant_id", tenantID, "user_id", userID, "error", err)
			writeInternal(w)
			return
		}
		writeJSON(w, http.StatusOK, StartResponse{AuthorizeURL: authorize})
	})
}

// oauthState is a consumed oauth_states row.
type oauthState struct {
	tenant, user uuid.UUID
	redirect     string
	expiresAt    time.Time
}

// takeState consumes a state: it is deleted whatever happens next, so it can
// only ever be used once.
func (h *Handlers) takeState(ctx context.Context, provider, state string) (oauthState, bool, error) {
	var s oauthState
	err := h.d.Pool.QueryRow(ctx, `
		DELETE FROM ohs_sync.oauth_states WHERE state = $1 AND provider = $2
		RETURNING tenant_id, user_id, coalesce(redirect_uri, ''), expires_at`,
		hashState(state), provider).Scan(&s.tenant, &s.user, &s.redirect, &s.expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, false, nil
	}
	if err != nil {
		return s, false, fmt.Errorf("take oauth state: %w", err)
	}
	return s, true, nil
}

// OAuthCallback serves GET /oauth/{provider}/callback. It is provider facing:
// mount it without auth. The state names the tenant and user; the code is
// exchanged, the tokens sealed and stored, and a 30 day backfill enqueued.
// The user lands on the tenant's redirect_uri with status=connected|error and
// provider=<slug>, or on the result page when no redirect_uri was given.
func (h *Handlers) OAuthCallback() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		provider := r.PathValue("provider")
		q := r.URL.Query()
		state := q.Get("state")
		if state == "" {
			renderResult(w, http.StatusBadRequest, provider, false)
			return
		}
		st, found, err := h.takeState(ctx, provider, state)
		if err != nil {
			slog.ErrorContext(ctx, "collect: oauth callback", "provider", provider, "error", err)
			renderResult(w, http.StatusInternalServerError, provider, false)
			return
		}
		if !found {
			// Unknown, already used, or for another provider: no tenant to
			// send the user back to.
			renderResult(w, http.StatusBadRequest, provider, false)
			return
		}
		log := slog.With("tenant_id", st.tenant, "user_id", st.user, "provider", provider)
		if err := h.complete(ctx, provider, st, q); err != nil {
			log.WarnContext(ctx, "collect: connection failed", "error", safeErr(err))
			h.finish(w, r, st, provider, false)
			return
		}
		log.InfoContext(ctx, "collect: connection established")
		h.finish(w, r, st, provider, true)
	})
}

// complete validates the callback and stores the connection.
func (h *Handlers) complete(ctx context.Context, provider string, st oauthState, q url.Values) error {
	if !h.d.now().Before(st.expiresAt) {
		return errors.New("state expired")
	}
	if e := q.Get("error"); e != "" {
		return errors.New("provider refused consent")
	}
	code := q.Get("code")
	if code == "" {
		return errors.New("callback without code")
	}
	u, err := h.d.Tenants.GetUser(ctx, st.tenant, st.user)
	if errors.Is(err, tenant.ErrNotFound) || (err == nil && u.Status != "active") {
		return errors.New("user no longer exists")
	}
	if err != nil {
		return err
	}
	oc, ok := h.d.oauth(provider)
	if !ok {
		return errors.New("provider not available")
	}
	var tokens connector.Tokens
	if se, ok := oc.(connector.StateExchanger); ok {
		tokens, err = se.ExchangeState(ctx, code, q.Get("state"), h.d.CallbackURL(provider))
	} else {
		tokens, err = oc.Exchange(ctx, code, h.d.CallbackURL(provider))
	}
	if err != nil {
		return fmt.Errorf("exchange: %w", err)
	}
	err = h.d.connect(ctx, st.tenant, st.user, provider, tokens)
	if errors.Is(err, errInactive) {
		// The user started being deleted during the exchange: the grant
		// just issued would otherwise stay valid at the provider.
		if rv, ok := oc.(connector.Revoker); ok {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout)
			if rerr := rv.Revoke(rctx, tokens); rerr != nil {
				slog.WarnContext(ctx, "collect: revoking the grant of a user being deleted failed",
					"tenant_id", st.tenant, "user_id", st.user, "provider", provider, "error", safeErr(rerr))
			} else {
				slog.InfoContext(ctx, "collect: grant of a user being deleted revoked",
					"tenant_id", st.tenant, "user_id", st.user, "provider", provider)
			}
			cancel()
		}
		return errors.New("user no longer exists")
	}
	return err
}

// connect stores the sealed tokens as an active connection, enqueues the 30
// day backfill and, for a provider that pushes notifications, a pending
// subscription row, in one transaction.
func (d Deps) connect(ctx context.Context, tenantID, userID uuid.UUID, provider string, t connector.Tokens) error {
	s, err := d.seal(tenantID, userID, provider, t)
	if err != nil {
		return err
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockActiveUser(ctx, tx, tenantID, userID); err != nil {
		return err
	}
	// A connection that is active and already backfilled (the user went
	// through consent again) only gets new tokens.
	var prevStatus string
	var prevBackfill *time.Time
	err = tx.QueryRow(ctx, `
		SELECT status, initial_backfill_at FROM ohs_sync.connections
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 FOR UPDATE`,
		tenantID, userID, provider).Scan(&prevStatus, &prevBackfill)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load connection: %w", err)
	}
	backfilled := err == nil && prevStatus == StatusActive && prevBackfill != nil
	now := d.now()
	backfillAt := &now
	if backfilled {
		backfillAt = prevBackfill
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, status, provider_user_id,
			access_token_enc, refresh_token_enc, token_expires_at, scopes, initial_backfill_at)
		VALUES ($1, $2, $3, 'active', $4, $5, $6, $7, $8, $9)
		ON CONFLICT (tenant_id, user_id, provider) DO UPDATE SET
			status = 'active',
			provider_user_id = coalesce(EXCLUDED.provider_user_id, ohs_sync.connections.provider_user_id),
			access_token_enc = EXCLUDED.access_token_enc,
			refresh_token_enc = EXCLUDED.refresh_token_enc,
			token_expires_at = EXCLUDED.token_expires_at,
			scopes = EXCLUDED.scopes,
			initial_backfill_at = EXCLUDED.initial_backfill_at,
			updated_at = now()`,
		tenantID, userID, provider, s.providerUserID, s.access, s.refresh, s.expiresAt, s.scopes, backfillAt); err != nil {
		return fmt.Errorf("store connection: %w", err)
	}
	if !backfilled {
		if err := enqueueBackfill(ctx, tx, tenantID, userID, provider, now); err != nil {
			return err
		}
	}
	if _, ok := d.subscriber(provider); ok {
		if err := d.enqueueSubscription(ctx, tx, tenantID, userID, provider); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// finish sends the user to the tenant's redirect_uri, or renders the page.
func (h *Handlers) finish(w http.ResponseWriter, r *http.Request, st oauthState, provider string, ok bool) {
	status := "error"
	if ok {
		status = "connected"
	}
	if st.redirect == "" {
		code := http.StatusOK
		if !ok {
			code = http.StatusBadRequest
		}
		renderResult(w, code, provider, ok)
		return
	}
	u, err := url.Parse(st.redirect)
	if err != nil {
		renderResult(w, http.StatusBadRequest, provider, false)
		return
	}
	v := u.Query()
	v.Set("status", status)
	v.Set("provider", provider)
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func renderResult(w http.ResponseWriter, code int, provider string, ok bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(code)
	_ = resultPage.Execute(w, struct {
		Connected bool
		Provider  string
	}{Connected: ok, Provider: provider})
}
