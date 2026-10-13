// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/deletion"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// Connection statuses (ohs_sync.connections.status).
const (
	StatusActive       = "active"
	StatusNeedsReauth  = "needs_reauth"
	StatusDisconnected = "disconnected"
)

// Lookup finds the connector registered under a provider slug. Production
// wiring passes one that only answers for enabled connectors.
type Lookup func(slug string) (connector.Connector, bool)

// Deps is what the connection handlers and the pull scheduler need.
type Deps struct {
	Pool    *pgxpool.Pool
	Raw     rawstore.Store
	Box     *crypto.Box
	Tenants *tenant.Store
	// Lookup resolves connectors; nil means the default registry.
	Lookup Lookup
	// PublicURL is OHS_PUBLIC_URL; the OAuth callback is
	// PublicURL + "/oauth/{provider}/callback".
	PublicURL string
	// Slugs lists the providers enabled on this instance (the scheduler only
	// schedules these). Nil means: the providers found in the connections
	// table that Lookup resolves.
	Slugs []string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) lookup(slug string) (connector.Connector, bool) {
	if d.Lookup != nil {
		return d.Lookup(slug)
	}
	return connector.Get(slug)
}

func (d Deps) oauth(slug string) (connector.OAuthConnector, bool) {
	c, ok := d.lookup(slug)
	if !ok {
		return nil, false
	}
	return connector.AsOAuth(c)
}

func (d Deps) check(what string) {
	if d.Pool == nil || d.Box == nil {
		panic("collect: " + what + " needs Pool and Box")
	}
}

// Handlers serves the connection routes. Each method returns the handler for
// one route; the router mounts it at the spec path (Go 1.22 patterns, the
// handlers read r.PathValue("id") and r.PathValue("provider")).
type Handlers struct{ d Deps }

// NewHandlers returns the connection handlers. Deps needs Pool, Box and
// Tenants, and Raw for the raw lines of inbound notifications.
func NewHandlers(d Deps) *Handlers {
	d.check("NewHandlers")
	if d.Tenants == nil {
		panic("collect: NewHandlers needs Tenants")
	}
	return &Handlers{d: d}
}

// SealAAD binds a sealed token to its row (R15): tenant_id|user_id|provider.
func SealAAD(tenantID, userID uuid.UUID, provider string) []byte {
	return []byte(tenantID.String() + "|" + userID.String() + "|" + provider)
}

// connRow is one ohs_sync.connections row.
type connRow struct {
	tenant, user   uuid.UUID
	provider       string
	status         string
	providerUserID string
	accessEnc      []byte
	refreshEnc     []byte
	expiresAt      *time.Time
	scopes         []string
	lastSyncAt     *time.Time
	createdAt      time.Time
}

const connCols = `tenant_id, user_id, provider, status, coalesce(provider_user_id, ''),
	access_token_enc, refresh_token_enc, token_expires_at, scopes, last_sync_at, created_at`

func scanConn(row pgx.Row) (connRow, error) {
	var c connRow
	err := row.Scan(&c.tenant, &c.user, &c.provider, &c.status, &c.providerUserID,
		&c.accessEnc, &c.refreshEnc, &c.expiresAt, &c.scopes, &c.lastSyncAt, &c.createdAt)
	return c, err
}

// open decrypts the row's tokens into a connector.Connection.
func (d Deps) open(c connRow) (connector.Connection, error) {
	aad := SealAAD(c.tenant, c.user, c.provider)
	conn := connector.Connection{
		TenantID: c.tenant, UserID: c.user, Provider: c.provider,
		Tokens: connector.Tokens{Scopes: c.scopes, ProviderUserID: c.providerUserID},
	}
	if c.lastSyncAt != nil {
		conn.LastSyncAt = *c.lastSyncAt
	}
	if c.expiresAt != nil {
		conn.Tokens.Expiry = *c.expiresAt
	}
	if c.accessEnc == nil {
		return conn, errors.New("connection holds no access token")
	}
	access, err := d.Box.OpenFor(c.accessEnc, aad)
	if err != nil {
		return conn, fmt.Errorf("open access token: %w", err)
	}
	conn.Tokens.Access = string(access)
	if c.refreshEnc != nil {
		refresh, err := d.Box.OpenFor(c.refreshEnc, aad)
		if err != nil {
			return conn, fmt.Errorf("open refresh token: %w", err)
		}
		conn.Tokens.Refresh = string(refresh)
	}
	return conn, nil
}

// sealed are the token columns of one connection.
type sealed struct {
	access, refresh []byte
	expiresAt       *time.Time
	scopes          []string
	providerUserID  *string
}

func (d Deps) seal(tenantID, userID uuid.UUID, provider string, t connector.Tokens) (sealed, error) {
	if t.Access == "" {
		return sealed{}, errors.New("provider returned no access token")
	}
	aad := SealAAD(tenantID, userID, provider)
	var s sealed
	var err error
	if s.access, err = d.Box.SealFor([]byte(t.Access), aad); err != nil {
		return sealed{}, err
	}
	if t.Refresh != "" {
		if s.refresh, err = d.Box.SealFor([]byte(t.Refresh), aad); err != nil {
			return sealed{}, err
		}
	}
	if !t.Expiry.IsZero() {
		e := t.Expiry.UTC()
		s.expiresAt = &e
	}
	s.scopes = t.Scopes
	if s.scopes == nil {
		s.scopes = []string{}
	}
	if t.ProviderUserID != "" {
		id := t.ProviderUserID
		s.providerUserID = &id
	}
	return s, nil
}

// execer is a pool or a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

const (
	reasonNeedsReauth  = "connection needs reauthorization"
	reasonDisconnected = "connection disconnected"
	reasonInactive     = "connection is not active"
)

// parkPending stops the pending pull jobs of one connection: they are parked
// with reason, so no worker claims them. Running jobs are settled by their
// worker, which checks the connection status.
func parkPending(ctx context.Context, q execer, tenantID, userID uuid.UUID, provider, reason string) error {
	_, err := q.Exec(ctx, `
		UPDATE ohs_sync.pull_jobs SET status = 'parked', claimed_at = NULL, last_error = $4,
			outcome = 'inactive', finished_at = now()
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status = 'pending'`,
		tenantID, userID, provider, reason)
	if err != nil {
		return fmt.Errorf("park pending pull jobs: %w", err)
	}
	return nil
}

// markNeedsReauth flags an active connection needs_reauth, broken for reason
// (BrokenNeedsReauth or BrokenMissingScope), and parks its pending pull jobs.
// The scheduler only pulls active connections, so nothing more is enqueued
// until the user connects again.
func markNeedsReauth(ctx context.Context, q execer, tenantID, userID uuid.UUID, provider, reason string, now time.Time) error {
	tag, err := q.Exec(ctx, `
		UPDATE ohs_sync.connections SET status = 'needs_reauth', updated_at = now()
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status = 'active'`,
		tenantID, userID, provider)
	if err != nil {
		return fmt.Errorf("mark needs_reauth: %w", err)
	}
	if tag.RowsAffected() > 0 {
		if _, err := webhook.Enqueue(ctx, q, webhook.Event{
			Tenant: tenantID, User: &userID, Type: webhook.ConnectionNeedsReauth,
			Data: map[string]any{"provider": provider},
		}); err != nil {
			return err
		}
		if err := markBroken(ctx, q, tenantID, userID, provider, reason, now, nil); err != nil {
			return err
		}
	}
	return parkPending(ctx, q, tenantID, userID, provider, reasonNeedsReauth)
}

// caller resolves the user a /v1/users/{id}/... request is about. A tenant
// key may name any user of its tenant; a user token only its own user. An
// unknown user, another tenant's user, or one being deleted is 404.
func (h *Handlers) caller(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	p, ok := auth.From(r.Context())
	if !ok {
		auth.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "a valid API key is required")
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeNotFound(w, "user not found")
		return uuid.Nil, uuid.Nil, false
	}
	if p.UserID != nil && *p.UserID != id {
		writeNotFound(w, "user not found")
		return uuid.Nil, uuid.Nil, false
	}
	u, err := h.d.Tenants.GetUser(r.Context(), p.TenantID, id)
	if errors.Is(err, tenant.ErrNotFound) || (err == nil && u.Status != "active") {
		writeNotFound(w, "user not found")
		return uuid.Nil, uuid.Nil, false
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "collect: look up user", "tenant_id", p.TenantID, "error", err)
		writeInternal(w)
		return uuid.Nil, uuid.Nil, false
	}
	return p.TenantID, id, true
}

func writeNotFound(w http.ResponseWriter, detail string) {
	auth.WriteProblem(w, http.StatusNotFound, "Not Found", detail)
}

func writeBadRequest(w http.ResponseWriter, detail string) {
	auth.WriteProblem(w, http.StatusBadRequest, "Bad Request", detail)
}

func writeInternal(w http.ResponseWriter) {
	auth.WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "the request could not be completed, retry later")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ConnectionView is one entry of the ListConnections response. Tokens never
// leave the store.
type ConnectionView = read.Connection

// ConnectionList is the ListConnections response.
type ConnectionList struct {
	Data []ConnectionView `json:"data"`
}

// ListConnections serves GET /v1/users/{id}/connections. Mount behind
// auth.Middleware with auth.TenantKey. It lists every connection of the user
// with its status (active, needs_reauth, disconnected) and last_sync_at.
func (h *Handlers) ListConnections() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID, userID, ok := h.caller(w, r)
		if !ok {
			return
		}
		conns, err := read.Connections(r.Context(), h.d.Pool, tenantID, userID)
		if errors.Is(err, read.ErrNotFound) {
			writeNotFound(w, "user not found")
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "collect: list connections", "tenant_id", tenantID, "user_id", userID, "error", err)
			writeInternal(w)
			return
		}
		writeJSON(w, http.StatusOK, ConnectionList{Data: conns})
	})
}

// revokeTimeout bounds the best effort provider work on disconnect (token
// refresh, unsubscribe, revoke), per connection. A var for tests.
var revokeTimeout = 30 * time.Second

// finishTimeout bounds the wipe and commit of a disconnect after the
// provider calls.
const finishTimeout = 10 * time.Second

// DeleteConnection serves DELETE /v1/users/{id}/connections/{provider}. Mount
// behind auth.Middleware with auth.TenantKey. It refreshes expired tokens,
// unsubscribes push notifications and revokes the grant at the provider when
// the connector can (best effort, failures are logged), sets the
// connection disconnected, wipes its tokens and parks its pending pull jobs.
// 204 on success, also for an already disconnected connection; 404 when the
// user has no connection to that provider.
//
// With ?delete_data=true it also queues, in the same transaction, the
// erasure of everything the user holds from that provider (see package
// deletion), drops the user's unused consent states for it, and answers 202
// with the job id instead. Without the parameter (or with false) the data
// already collected is kept, as before.
func (h *Handlers) DeleteConnection() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var deleteData bool
		switch v := r.URL.Query().Get("delete_data"); v {
		case "", "false":
		case "true":
			deleteData = true
		default:
			auth.WriteProblem(w, http.StatusBadRequest, "Bad Request", "delete_data must be true or false")
			return
		}
		tenantID, userID, ok := h.caller(w, r)
		if !ok {
			return
		}
		provider := r.PathValue("provider")
		if deleteData && !deletion.ValidProvider(provider) {
			writeNotFound(w, "no connection to this provider")
			return
		}
		ctx := r.Context()
		// The row stays locked from the read through the revoke to the wipe,
		// so a refresh or a reconnect cannot interleave.
		tx, err := h.d.Pool.Begin(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "collect: begin", "error", err)
			writeInternal(w)
			return
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		c, err := scanConn(tx.QueryRow(ctx, `SELECT `+connCols+`
			FROM ohs_sync.connections WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
			FOR UPDATE`, tenantID, userID, provider))
		if errors.Is(err, pgx.ErrNoRows) {
			writeNotFound(w, "no connection to this provider")
			return
		}
		if err != nil {
			slog.ErrorContext(ctx, "collect: load connection", "tenant_id", tenantID, "user_id", userID, "provider", provider, "error", err)
			writeInternal(w)
			return
		}
		// From here the work outlives a client that hangs up: the provider
		// calls (bounded by revokeTimeout) and then the wipe and commit, so
		// what was unsubscribed at the provider is also recorded here.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout+finishTimeout)
		defer cancel()
		if c.status != StatusDisconnected {
			rctx, rcancel := context.WithTimeout(dctx, revokeTimeout)
			err := h.d.revokeLocked(dctx, rctx, tx, c)
			rcancel()
			if err != nil {
				slog.ErrorContext(ctx, "collect: disconnect", "tenant_id", tenantID, "user_id", userID, "provider", provider, "error", err)
				writeInternal(w)
				return
			}
		}
		if _, err := tx.Exec(dctx, `
			UPDATE ohs_sync.connections SET status = 'disconnected',
				access_token_enc = NULL, refresh_token_enc = NULL, token_expires_at = NULL,
				token_type = NULL, broken_reason = NULL, broken_since = NULL,
				rate_limited_until = NULL, updated_at = now()
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
			tenantID, userID, provider); err != nil {
			slog.ErrorContext(ctx, "collect: disconnect", "error", err)
			writeInternal(w)
			return
		}
		if err := parkPending(dctx, tx, tenantID, userID, provider, reasonDisconnected); err != nil {
			slog.ErrorContext(ctx, "collect: disconnect", "error", err)
			writeInternal(w)
			return
		}
		var jobID uuid.UUID
		if deleteData {
			// A consent link handed out earlier must not reconnect the
			// provider while (or after) its data is erased.
			if _, err := tx.Exec(dctx, `DELETE FROM ohs_sync.oauth_states
				WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`, tenantID, userID, provider); err != nil {
				slog.ErrorContext(ctx, "collect: disconnect", "error", err)
				writeInternal(w)
				return
			}
			if jobID, err = deletion.QueueProvider(dctx, tx, tenantID, userID, provider); err != nil {
				slog.ErrorContext(ctx, "collect: queue provider data deletion", "error", err)
				writeInternal(w)
				return
			}
		}
		if err := tx.Commit(dctx); err != nil {
			slog.ErrorContext(ctx, "collect: disconnect commit", "error", err)
			writeInternal(w)
			return
		}
		slog.InfoContext(ctx, "collect: connection disconnected", "tenant_id", tenantID, "user_id", userID, "provider", provider)
		if deleteData {
			slog.InfoContext(ctx, "collect: provider data deletion requested", "tenant_id", tenantID, "user_id", userID,
				"provider", provider, "job_id", jobID)
			deletion.WriteAccepted(w, jobID)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// RevokeAll ends at the providers every connection of a user that is not
// already disconnected: tokens refreshed when expired, push subscriptions
// unsubscribed and marked removed, grants revoked. It is the deletion worker's
// ConnectionRevoker: a caller whose context has ended starts nothing more,
// provider calls run under the caller's context capped at revokeTimeout per
// connection, and each connection's database work runs detached so what a
// provider already did is recorded. Provider failures are logged, never
// returned; a database failure is returned. The connection rows are left to the caller, which deletes them.
func (d Deps) RevokeAll(ctx context.Context, tenantID, userID uuid.UUID) error {
	rows, err := d.Pool.Query(ctx, `SELECT provider FROM ohs_sync.connections
		WHERE tenant_id = $1 AND user_id = $2 AND status <> 'disconnected' ORDER BY provider`, tenantID, userID)
	if err != nil {
		return fmt.Errorf("list connections: %w", err)
	}
	providers, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list connections: %w", err)
	}
	var errs []error
	for _, p := range providers {
		if err := d.revokeOne(ctx, tenantID, userID, p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
		}
	}
	return errors.Join(errs...)
}

func (d Deps) revokeOne(ctx context.Context, tenantID, userID uuid.UUID, provider string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Provider calls get the caller's context, capped at revokeTimeout; the
	// transaction runs detached so what the provider did is recorded even
	// when the caller's budget ends during a call.
	pctx, pcancel := context.WithTimeout(ctx, revokeTimeout)
	defer pcancel()
	dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout+finishTimeout)
	defer dcancel()
	tx, err := d.Pool.Begin(dctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(dctx) }()
	c, err := scanConn(tx.QueryRow(dctx, `SELECT `+connCols+`
		FROM ohs_sync.connections WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
		FOR UPDATE`, tenantID, userID, provider))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock connection: %w", err)
	}
	if c.status == StatusDisconnected {
		return nil
	}
	if err := d.revokeLocked(dctx, pctx, tx, c); err != nil {
		return err
	}
	if err := tx.Commit(dctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// maxErrText bounds a provider error kept in logs, sync_log or last_error.
const maxErrText = 200

// urlQuery matches a URL's query string or fragment.
var urlQuery = regexp.MustCompile(`(https?://[^\s?#"']*)[?#][^\s"']*`)

// safeErr is the text of an error that may come from a connector, made safe
// to log or store: URLs lose their query strings (codes, tokens), and the
// text is cut to maxErrText bytes so a quoted response body cannot leak
// health values.
func safeErr(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToValidUTF8(urlQuery.ReplaceAllString(err.Error(), "$1"), "?")
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) > maxErrText {
		n := maxErrText
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n]
	}
	return s
}
