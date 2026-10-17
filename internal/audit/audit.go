// SPDX-License-Identifier: AGPL-3.0-only

// Package audit keeps the append-only audit log (ohs_core.audit_events):
// who read, exported, changed or deleted which user's data, when, with which
// credential and with what outcome. An event never holds a health value, a
// secret or a query string. The api records every authenticated request,
// `ohs admin` its key commands and the deletion worker each erasure; the
// automation job prunes events past the retention period.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ActorType is the kind of credential or process behind an event.
type ActorType string

// Actor types.
const (
	// APIKey is a tenant API key; the actor id is the key's id.
	APIKey ActorType = "api_key"
	// UserToken is a user token; the actor id is TokenID of its hash.
	UserToken ActorType = "user_token"
	// Admin is the instance operator acting on a tenant: the `ohs admin`
	// command line (actor id "ohs admin"), or a key of an instance admin
	// tenant through /v1/admin/tenants (actor id "api_key:" and the key's
	// id), recorded on the tenant it changed.
	Admin ActorType = "admin"
	// System is the instance itself (the deletion worker, for example).
	System ActorType = "system"
	// ShareLink is a share link, used by whoever holds it; the actor id is
	// the share's id.
	ShareLink ActorType = "share_link"
)

// Actions. An API request is classified by Classify; the others are
// recorded by the code that performs them.
const (
	DataRead         = "data.read"
	DataExport       = "data.export"
	DataWrite        = "data.write"
	UserCreate       = "user.create"
	UserRead         = "user.read"
	UserDelete       = "user.delete"
	UserErased       = "user.erased"
	TokenCreate      = "token.create"
	ConnectionRead   = "connection.read"
	ConnectionCreate = "connection.create"
	ConnectionDelete = "connection.delete"
	ConnectionSync   = "connection.sync"
	ConsentRead      = "consent.read"
	ConsentWrite     = "consent.write"
	WebhookRead      = "webhook.read"
	WebhookWrite     = "webhook.write"
	AuditRead        = "audit.read"
	ConfigRead       = "config.read"
	ConfigWrite      = "config.write"
	TenantCreate     = "tenant.create"
	TenantAdmin      = "tenant.admin"
	KeyCreate        = "key.create"
	KeyRevoke        = "key.revoke"
	ShareCreate      = "share.create"
	ShareRead        = "share.read"
	ShareRevoke      = "share.revoke"
	// ShareAccess is a manifest request on a known share link, or a file
	// download; detail has the share id and the outcome.
	ShareAccess = "share.access"
	// TenantProvider is a provider turned off or back on for one tenant by
	// an instance admin; detail has the provider and enabled.
	TenantProvider = "tenant.provider"
)

// Event is one entry to record.
type Event struct {
	Tenant    uuid.UUID
	User      *uuid.UUID // the end user whose data the action touched, if one
	ActorType ActorType
	ActorID   string
	Action    string
	Method    string // HTTP method, empty outside a request
	Route     string // route pattern, never the URL
	Status    int    // HTTP status; 0 outside a request
	RequestID string
	// Detail holds identifiers only (a job id, a provider slug), never a
	// value read or written.
	Detail map[string]any
}

// TokenID names a user token in the log without revealing it: the first 16
// hex characters of its SHA-256 hash, which is what the database stores.
func TokenID(hash string) string {
	if len(hash) > 16 {
		return hash[:16]
	}
	return hash
}

// Execer is a pool or a transaction.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Record appends ev. In a transaction it is written, or not, with the change
// it describes.
func Record(ctx context.Context, db Execer, ev Event) error {
	if ev.Action == "" || ev.ActorType == "" {
		return errors.New("audit: an event needs an action and an actor type")
	}
	detail := []byte("{}")
	if len(ev.Detail) > 0 {
		var err error
		if detail, err = json.Marshal(ev.Detail); err != nil {
			return fmt.Errorf("audit: detail: %w", err)
		}
	}
	var status *int
	if ev.Status != 0 {
		status = &ev.Status
	}
	_, err := db.Exec(ctx, `
		INSERT INTO ohs_core.audit_events
		    (tenant_id, user_id, actor_type, actor_id, action, method, route, status, request_id, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		ev.Tenant, ev.User, string(ev.ActorType), ev.ActorID, ev.Action, ev.Method, ev.Route, status,
		ev.RequestID, detail)
	if err != nil {
		return fmt.Errorf("audit: record: %w", err)
	}
	return nil
}

// Classify names the action of an authenticated API request from its method
// and matched route pattern ("GET /v1/users/{id}/samples").
func Classify(method, pattern string) string {
	_, path, ok := strings.Cut(pattern, " ")
	if !ok {
		path = pattern
	}
	const userPrefix = "/v1/users/{id}"
	// An instance admin reading one tenant's users and their data: the
	// action of the tenant route it stands for.
	if rest, ok := strings.CutPrefix(path, "/v1/admin/tenants/{tenant_id}/users"); ok &&
		(rest == "" || strings.HasPrefix(rest, "/")) {
		return Classify(method, "/v1/users"+rest)
	}
	switch {
	case path == "/mcp":
		return DataRead // the MCP server is read only
	case strings.HasPrefix(path, "/v1/ingest/"):
		return DataWrite
	case path == "/v1/audit":
		return AuditRead
	case path == "/v1/admin/tenants" && method == "POST":
		return TenantCreate
	case path == "/v1/admin/tenants/{tenant_id}/keys" && method == "POST":
		return KeyCreate
	case path == "/v1/admin/tenants/{tenant_id}/keys/{key_id}" && method == "DELETE":
		return KeyRevoke
	case path == "/v1/admin/tenants/{tenant_id}/demo-user":
		return DataWrite // synthetic data for one of the tenant's users
	case path == "/v1/admin/tenants/{tenant_id}/providers/{provider}" && method == "PATCH":
		return TenantProvider
	case strings.HasPrefix(path, "/v1/keys"):
		switch method {
		case "POST":
			return KeyCreate
		case "DELETE":
			return KeyRevoke
		}
		return ConfigRead
	case strings.HasPrefix(path, "/v1/webhooks"):
		if method == "GET" {
			return WebhookRead
		}
		return WebhookWrite
	case path == "/v1/users":
		if method == "POST" {
			return UserCreate
		}
		return UserRead
	case path == userPrefix:
		if method == "DELETE" {
			return UserDelete
		}
		return UserRead
	case strings.HasPrefix(path, "/v1/deletions/"):
		return UserRead
	case strings.HasPrefix(path, userPrefix+"/tokens"):
		return TokenCreate
	case path == userPrefix+"/connect-sessions":
		return ConnectionCreate // a hosted connection page link
	case strings.HasPrefix(path, userPrefix+"/sync-runs"):
		return ConnectionRead
	case strings.HasPrefix(path, userPrefix+"/connections") && strings.HasSuffix(path, "/sync"):
		return ConnectionSync // an immediate pull
	case strings.HasPrefix(path, userPrefix+"/connections"):
		switch method {
		case "POST":
			return ConnectionCreate
		case "DELETE":
			return ConnectionDelete
		}
		return ConnectionRead
	case strings.HasPrefix(path, userPrefix+"/consents"):
		if method == "GET" {
			return ConsentRead
		}
		return ConsentWrite
	case strings.HasPrefix(path, userPrefix+"/shares"):
		switch method {
		case "POST":
			return ShareCreate
		case "DELETE":
			return ShareRevoke
		}
		return ShareRead
	case strings.HasPrefix(path, userPrefix+"/exports/") || path == userPrefix+"/archive":
		return DataExport
	case strings.HasPrefix(path, userPrefix+"/"):
		if method == "GET" {
			return DataRead
		}
		return ConfigWrite
	case method == "GET":
		return ConfigRead
	}
	return ConfigWrite
}

// Actor is who an Entry names.
type Actor struct {
	Type ActorType `json:"type"`
	ID   string    `json:"id"`
}

// Entry is a recorded event as GET /v1/audit serves it.
type Entry struct {
	ID        int64          `json:"id"`
	At        time.Time      `json:"at"`
	Actor     Actor          `json:"actor"`
	Action    string         `json:"action"`
	UserID    *uuid.UUID     `json:"user_id"`
	Method    string         `json:"method,omitempty"`
	Route     string         `json:"route,omitempty"`
	Status    *int           `json:"status"`
	RequestID string         `json:"request_id,omitempty"`
	Detail    map[string]any `json:"detail"`
}

// Filter selects entries of one tenant, newest first.
type Filter struct {
	User   *uuid.UUID
	Action string // exact action, or a prefix ending in "." ("data.")
	Since  time.Time
	Until  time.Time
	Before int64 // cursor: entries with a smaller id
	Limit  int
}

// Querier is a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// List returns up to f.Limit entries of the tenant matching f, newest first,
// and the cursor of the next page (0 on the last page).
func List(ctx context.Context, db Querier, tenant uuid.UUID, f Filter) ([]Entry, int64, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	q := `SELECT id, at, actor_type, actor_id, action, user_id, method, route, status, request_id, detail
	      FROM ohs_core.audit_events WHERE tenant_id = $1`
	args := []any{tenant}
	add := func(cond string, v any) {
		args = append(args, v)
		q += fmt.Sprintf(" AND "+cond, len(args))
	}
	if f.User != nil {
		add("user_id = $%d", *f.User)
	}
	if strings.HasSuffix(f.Action, ".") {
		add("starts_with(action, $%d)", f.Action)
	} else if f.Action != "" {
		add("action = $%d", f.Action)
	}
	if !f.Since.IsZero() {
		add("at >= $%d", f.Since)
	}
	if !f.Until.IsZero() {
		add("at < $%d", f.Until)
	}
	if f.Before > 0 {
		add("id < $%d", f.Before)
	}
	args = append(args, f.Limit+1)
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))

	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("audit: list: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var (
			e      Entry
			actor  string
			detail []byte
		)
		if err := rows.Scan(&e.ID, &e.At, &actor, &e.Actor.ID, &e.Action, &e.UserID, &e.Method, &e.Route,
			&e.Status, &e.RequestID, &detail); err != nil {
			return nil, 0, fmt.Errorf("audit: scan: %w", err)
		}
		e.Actor.Type = ActorType(actor)
		e.At = e.At.UTC()
		if err := json.Unmarshal(detail, &e.Detail); err != nil {
			return nil, 0, fmt.Errorf("audit: detail: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("audit: list: %w", err)
	}
	var next int64
	if len(out) > f.Limit {
		out = out[:f.Limit]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// Beginner opens a transaction (a pgxpool.Pool).
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// pruneBatch is how many events one prune transaction deletes.
const pruneBatch = 10000

// Prune deletes the events recorded before cutoff, in batches, and returns
// how many went. It is the only delete the append-only trigger lets through:
// each batch sets ohs.audit_prune for its own transaction.
func Prune(ctx context.Context, db Beginner, cutoff time.Time) (int64, error) {
	var total int64
	for {
		n, err := pruneOnce(ctx, db, cutoff)
		total += n
		if err != nil || n < pruneBatch {
			return total, err
		}
	}
}

func pruneOnce(ctx context.Context, db Beginner, cutoff time.Time) (int64, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("audit: prune: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('ohs.audit_prune', 'on', true)`); err != nil {
		return 0, fmt.Errorf("audit: prune: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM ohs_core.audit_events
		WHERE id IN (SELECT id FROM ohs_core.audit_events WHERE at < $1 ORDER BY id LIMIT $2)`,
		cutoff, pruneBatch)
	if err != nil {
		return 0, fmt.Errorf("audit: prune: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("audit: prune: %w", err)
	}
	return tag.RowsAffected(), nil
}
