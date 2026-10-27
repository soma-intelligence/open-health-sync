// SPDX-License-Identifier: AGPL-3.0-only

// Package tenant stores tenants, API keys, end users, user tokens and idempotency
// records. It is the only package that touches the ohs_core auth tables.
package tenant

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/pgtext"
)

var (
	// ErrNotFound means the row does not exist or belongs to another tenant.
	ErrNotFound = errors.New("tenant: not found")
	// ErrConflict means a uniqueness rule was hit (for example a duplicate external ref).
	ErrConflict = errors.New("tenant: conflict")
	// ErrInvalidCursor means a list cursor could not be decoded.
	ErrInvalidCursor = errors.New("tenant: invalid cursor")
	// ErrInvalidScopes means a token was requested with no scope or an unknown one.
	ErrInvalidScopes = errors.New("tenant: invalid scopes")
	// ErrTTLTooLong means the requested token lifetime exceeds the maximum for its scopes.
	ErrTTLTooLong = errors.New("tenant: ttl too long")
	// ErrIdempotencyMismatch means an Idempotency-Key was reused with a different request.
	ErrIdempotencyMismatch = errors.New("tenant: idempotency key reused with a different request")
	// ErrIdempotencyInProgress means another call with the same key is still running (API: 409).
	ErrIdempotencyInProgress = errors.New("tenant: idempotent request in progress")
	// ErrIdempotencyKeyInvalid means the key is empty, longer than 255 bytes,
	// or not text Postgres stores (invalid UTF-8 or a NUL).
	ErrIdempotencyKeyInvalid = errors.New("tenant: invalid idempotency key")
	// ErrTTLInvalid means a positive token lifetime shorter than one second.
	ErrTTLInvalid = errors.New("tenant: ttl too short")
	// ErrSetupDone means the instance already has a tenant, so the first-run
	// setup is closed (API: 409).
	ErrSetupDone = errors.New("tenant: setup already done")
)

// Token lifetimes.
const (
	DefaultTokenTTL   = time.Hour
	MaxTokenTTL       = 24 * time.Hour
	MaxReadTokenTTL   = 2592000 * time.Second // 30 days, tokens whose only scope is read
	idempotencyTTL    = 24 * time.Hour
	pendingTakeover   = 5 * time.Minute
	maxIdempotencyKey = 255
	touchEvery        = time.Minute
	keyPrefixLen      = 12
	maxListLimit      = 1000
	defaultListLimit  = 100
)

// Tenant is one customer of the deployment.
type Tenant struct {
	ID        uuid.UUID
	Name      string
	CreatedAt time.Time
	// Admin marks the instance admin tenant: its API keys may change
	// instance-wide settings (the /v1/admin routes). The first tenant of an
	// instance is the admin.
	Admin bool
}

// User is an end user of a tenant.
type User struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	ExternalRef string
	Status      string
	CreatedAt   time.Time
}

// Store is the Postgres backed tenant store.
type Store struct {
	pool *pgxpool.Pool
}

// New wraps an open pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the pool for tests and sibling stores.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// CreateTenant creates a tenant with its first API key. The plaintext key is
// returned once and never stored.
func (s *Store) CreateTenant(ctx context.Context, name string) (Tenant, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Tenant{}, "", fmt.Errorf("tenant: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	t, full, _, err := insertTenant(ctx, tx, name)
	if err != nil {
		return Tenant{}, "", err
	}
	if err := commit(ctx, tx); err != nil {
		return Tenant{}, "", err
	}
	return t, full, nil
}

// CreateTenantAudited is CreateTenant that also describes the first key and
// records ev in the audit log in the same transaction, for the new tenant and
// with the key's id and prefix added to its detail: a failed audit write
// leaves no tenant behind. The new tenant is an instance admin only when it
// is the instance's first.
func (s *Store) CreateTenantAudited(ctx context.Context, name string, ev audit.Event) (Tenant, string, APIKey, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Tenant{}, "", APIKey{}, fmt.Errorf("tenant: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	t, full, k, err := insertTenant(ctx, tx, name)
	if err != nil {
		return Tenant{}, "", APIKey{}, err
	}
	if err := audit.Record(ctx, tx, keyEvent(ev, t.ID, k)); err != nil {
		return Tenant{}, "", APIKey{}, err
	}
	if err := commit(ctx, tx); err != nil {
		return Tenant{}, "", APIKey{}, err
	}
	return t, full, k, nil
}

// EnsureTenant returns the oldest tenant named name, creating it with a first
// API key when none exists. The plaintext key is non-empty only when the
// tenant was created by this call. Concurrent callers (several instances
// starting at once) are serialized by an advisory lock on the name, so one
// tenant is created.
func (s *Store) EnsureTenant(ctx context.Context, name string) (Tenant, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Tenant{}, "", fmt.Errorf("tenant: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ohs_core.tenants:' || $1, 0))`, name); err != nil {
		return Tenant{}, "", fmt.Errorf("tenant: lock name: %w", err)
	}
	var t Tenant
	err = tx.QueryRow(ctx,
		`SELECT id, name, created_at, admin FROM ohs_core.tenants WHERE name = $1 ORDER BY created_at, id LIMIT 1`, name,
	).Scan(&t.ID, &t.Name, &t.CreatedAt, &t.Admin)
	if err == nil {
		return t, "", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, "", fmt.Errorf("tenant: find tenant: %w", err)
	}
	t, full, _, err := insertTenant(ctx, tx, name)
	if err != nil {
		return Tenant{}, "", err
	}
	if err := commit(ctx, tx); err != nil {
		return Tenant{}, "", err
	}
	return t, full, nil
}

// tenantCreationLock is the advisory lock key that serializes every tenant
// creation with CreateFirstTenant, across every instance sharing the
// database: the first-run check never misses a tenant being created by
// another path (`ohs admin`, the bootstrap tenant).
const tenantCreationLock = "ohs_core.tenant-creation"

// CreateFirstTenant creates a tenant with its first API key only while the
// instance has no tenant at all, and ErrSetupDone otherwise. Concurrent
// callers are serialized by an advisory lock, so exactly one of them creates
// it. When ev is not nil it is recorded in the audit log in the same
// transaction, for the new tenant and with the key's prefix in its detail:
// a failed audit write leaves no tenant behind. The plaintext key is
// returned once and never stored.
func (s *Store) CreateFirstTenant(ctx context.Context, name string, ev *audit.Event) (Tenant, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Tenant{}, "", fmt.Errorf("tenant: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockTenantCreation(ctx, tx); err != nil {
		return Tenant{}, "", err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ohs_core.tenants)`).Scan(&exists); err != nil {
		return Tenant{}, "", fmt.Errorf("tenant: count tenants: %w", err)
	}
	if exists {
		return Tenant{}, "", ErrSetupDone
	}
	t, full, _, err := insertTenant(ctx, tx, name)
	if err != nil {
		return Tenant{}, "", err
	}
	if ev != nil {
		e := *ev
		e.Tenant = t.ID
		e.Detail = map[string]any{"key_prefix": full[:keyPrefixLen] + "..."}
		if err := audit.Record(ctx, tx, e); err != nil {
			return Tenant{}, "", err
		}
	}
	if err := commit(ctx, tx); err != nil {
		return Tenant{}, "", err
	}
	return t, full, nil
}

// SetAdmin marks the tenant as the instance admin, or clears the mark. An
// unknown tenant is ErrNotFound. Several tenants may be admins.
func (s *Store) SetAdmin(ctx context.Context, tenantID uuid.UUID, admin bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE ohs_core.tenants SET admin = $2 WHERE id = $1`, tenantID, admin)
	if err != nil {
		return fmt.Errorf("tenant: set admin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetTenant returns one tenant. An unknown id is ErrNotFound.
func (s *Store) GetTenant(ctx context.Context, tenantID uuid.UUID) (Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx, `SELECT id, name, created_at, admin FROM ohs_core.tenants WHERE id = $1`, tenantID).
		Scan(&t.ID, &t.Name, &t.CreatedAt, &t.Admin)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, ErrNotFound
	}
	if err != nil {
		return Tenant{}, fmt.Errorf("tenant: get tenant: %w", err)
	}
	return t, nil
}

// HasTenants reports whether the instance has at least one tenant.
func (s *Store) HasTenants(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ohs_core.tenants)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("tenant: count tenants: %w", err)
	}
	return exists, nil
}

// lockTenantCreation takes the transaction's tenant creation lock. Advisory
// locks stack within a session, so taking it twice in one transaction is fine.
func lockTenantCreation(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, tenantCreationLock); err != nil {
		return fmt.Errorf("tenant: lock tenant creation: %w", err)
	}
	return nil
}

// insertTenant creates a tenant and its first API key, and describes the
// key. The first tenant of the instance is its admin: creations are
// serialized by the tenant creation lock, so exactly one tenant is ever
// created first.
func insertTenant(ctx context.Context, tx pgx.Tx, name string) (Tenant, string, APIKey, error) {
	if err := lockTenantCreation(ctx, tx); err != nil {
		return Tenant{}, "", APIKey{}, err
	}
	full, hash, err := auth.NewSecret(auth.PrefixSecretKey)
	if err != nil {
		return Tenant{}, "", APIKey{}, err
	}
	var t Tenant
	if err := tx.QueryRow(ctx,
		`INSERT INTO ohs_core.tenants (name, admin) VALUES ($1, NOT EXISTS (SELECT 1 FROM ohs_core.tenants))
		 RETURNING id, name, created_at, admin`, name,
	).Scan(&t.ID, &t.Name, &t.CreatedAt, &t.Admin); err != nil {
		return Tenant{}, "", APIKey{}, fmt.Errorf("tenant: insert tenant: %w", err)
	}
	k := APIKey{Prefix: full[:keyPrefixLen]}
	if err := tx.QueryRow(ctx,
		`INSERT INTO ohs_core.api_keys (tenant_id, key_prefix, key_hash) VALUES ($1, $2, $3) RETURNING id, created_at`,
		t.ID, k.Prefix, hash,
	).Scan(&k.ID, &k.CreatedAt); err != nil {
		return Tenant{}, "", APIKey{}, fmt.Errorf("tenant: insert key: %w", err)
	}
	return t, full, k, nil
}

// CreateUser adds an end user. An empty externalRef is stored as NULL, so
// unreferenced users never collide.
func (s *Store) CreateUser(ctx context.Context, tenantID uuid.UUID, externalRef string) (User, error) {
	var ref *string
	if externalRef != "" {
		ref = &externalRef
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO ohs_core.end_users (tenant_id, external_ref) VALUES ($1, $2)
		 RETURNING id, tenant_id, coalesce(external_ref, ''), status, created_at`, tenantID, ref)
	u, err := scanUser(row)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return User{}, ErrConflict
		case "23503":
			return User{}, ErrNotFound
		}
	}
	return u, err
}

// GetUser returns a user of the tenant. A user of another tenant is ErrNotFound.
func (s *Store) GetUser(ctx context.Context, tenantID, userID uuid.UUID) (User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, coalesce(external_ref, ''), status, created_at
		 FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2`, tenantID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// GetActiveUser is GetUser for the API: a user being deleted is ErrNotFound.
func (s *Store) GetActiveUser(ctx context.Context, tenantID, userID uuid.UUID) (User, error) {
	u, err := s.GetUser(ctx, tenantID, userID)
	if err != nil {
		return User{}, err
	}
	if u.Status != "active" {
		return User{}, ErrNotFound
	}
	return u, nil
}

// EnsureUser returns the tenant's user with this external ref, creating it
// when none exists, and whether this call created it. A user with that ref
// being deleted is ErrConflict: the ref is free again once the deletion ends.
func (s *Store) EnsureUser(ctx context.Context, tenantID uuid.UUID, externalRef string) (User, bool, error) {
	if externalRef == "" {
		return User{}, false, errors.New("tenant: EnsureUser needs an external ref")
	}
	find := func() (User, error) {
		return scanUser(s.pool.QueryRow(ctx,
			`SELECT id, tenant_id, coalesce(external_ref, ''), status, created_at
			 FROM ohs_core.end_users WHERE tenant_id = $1 AND external_ref = $2`, tenantID, externalRef))
	}
	u, err := find()
	if errors.Is(err, pgx.ErrNoRows) {
		u, err = s.CreateUser(ctx, tenantID, externalRef)
		if err == nil {
			return u, true, nil
		}
		if !errors.Is(err, ErrConflict) {
			return User{}, false, err
		}
		u, err = find() // created by a concurrent call
	}
	if err != nil {
		return User{}, false, fmt.Errorf("tenant: find user: %w", err)
	}
	if u.Status != "active" {
		return User{}, false, ErrConflict
	}
	return u, false, nil
}

// ListUsers pages through a tenant's active users, oldest first. A user being
// deleted is not listed. The returned cursor is empty on the last page.
// limit <= 0 means 100; it is capped at 1000.
func (s *Store) ListUsers(ctx context.Context, tenantID uuid.UUID, cursor string, limit int) ([]User, string, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)

	afterTime := time.Time{}
	afterID := uuid.Nil
	if cursor != "" {
		var err error
		if afterTime, afterID, err = decodeCursor(cursor); err != nil {
			return nil, "", err
		}
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, tenant_id, coalesce(external_ref, ''), status, created_at
		 FROM ohs_core.end_users
		 WHERE tenant_id = $1 AND status = 'active' AND (created_at, id) > ($2, $3)
		 ORDER BY created_at, id LIMIT $4`,
		tenantID, afterTime, afterID, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("tenant: list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("tenant: list users: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

func encodeCursor(t time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(c string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	ts, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	return t, id, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanUser(r scanner) (User, error) {
	var u User
	err := r.Scan(&u.ID, &u.TenantID, &u.ExternalRef, &u.Status, &u.CreatedAt)
	return u, err
}

// MintUserToken issues an opaque ohs_ut_ token for an active user of the tenant.
// ttl <= 0 means one hour. Scopes must be a non-empty subset of upload, connect,
// read. The lifetime is capped at 24 hours, or 30 days when read is the only scope.
func (s *Store) MintUserToken(ctx context.Context, tenantID, userID uuid.UUID, scopes []string, ttl time.Duration) (string, time.Time, error) {
	scopes, err := normalizeScopes(scopes)
	if err != nil {
		return "", time.Time{}, err
	}
	if ttl > 0 && ttl < time.Second {
		return "", time.Time{}, ErrTTLInvalid
	}
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	limit := MaxTokenTTL
	if len(scopes) == 1 && scopes[0] == string(auth.UserRead) {
		limit = MaxReadTokenTTL
	}
	if ttl > limit {
		return "", time.Time{}, ErrTTLTooLong
	}

	full, hash, err := auth.NewSecret(auth.PrefixUserToken)
	if err != nil {
		return "", time.Time{}, err
	}
	var exp time.Time
	err = s.pool.QueryRow(ctx,
		`INSERT INTO ohs_core.user_tokens (hash, tenant_id, user_id, scopes, expires_at)
		 SELECT $1, u.tenant_id, u.id, $4, now() + $5::interval
		 FROM ohs_core.end_users u
		 WHERE u.tenant_id = $2 AND u.id = $3 AND u.status = 'active'
		 RETURNING expires_at`,
		hash, tenantID, userID, scopes, fmt.Sprintf("%d microseconds", ttl.Microseconds()),
	).Scan(&exp)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, ErrNotFound
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("tenant: mint token: %w", err)
	}
	return full, exp.UTC(), nil
}

func normalizeScopes(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, ErrInvalidScopes
	}
	var out []string
	for _, sc := range in {
		switch auth.Scope(sc) {
		case auth.UserUpload, auth.UserConnect, auth.UserRead:
		default:
			return nil, ErrInvalidScopes
		}
		if !slices.Contains(out, sc) {
			out = append(out, sc)
		}
	}
	return out, nil
}

// LookupKey resolves the hash of an API key or user token. It satisfies auth.Lookup.
// Unknown, revoked and expired secrets, and tokens of users being deleted, return
// auth.ErrUnauthenticated.
func (s *Store) LookupKey(ctx context.Context, hash string) (auth.Principal, error) {
	var (
		tenantID uuid.UUID
		keyID    uuid.UUID
		stale    bool
		admin    bool
	)
	err := s.pool.QueryRow(ctx,
		`SELECT k.tenant_id, k.id,
		        k.last_used_at IS NULL OR k.last_used_at < now() - $2::interval, t.admin
		 FROM ohs_core.api_keys k JOIN ohs_core.tenants t ON t.id = k.tenant_id
		 WHERE k.key_hash = $1 AND k.revoked_at IS NULL`,
		hash, fmt.Sprintf("%d microseconds", touchEvery.Microseconds()),
	).Scan(&tenantID, &keyID, &stale, &admin)
	if err == nil {
		if stale {
			// Best effort, and at most once a minute per key.
			if _, terr := s.pool.Exec(ctx, `UPDATE ohs_core.api_keys SET last_used_at = now() WHERE key_hash = $1`, hash); terr != nil {
				slog.DebugContext(ctx, "tenant: touch api key", "error", terr)
			}
		}
		return auth.Principal{TenantID: tenantID, KeyID: keyID.String(), Admin: admin}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return auth.Principal{}, fmt.Errorf("tenant: lookup key: %w", err)
	}

	var (
		userID uuid.UUID
		scopes []string
	)
	err = s.pool.QueryRow(ctx,
		`SELECT t.tenant_id, t.user_id, t.scopes
		 FROM ohs_core.user_tokens t
		 JOIN ohs_core.end_users u ON u.tenant_id = t.tenant_id AND u.id = t.user_id
		 WHERE t.hash = $1 AND t.revoked_at IS NULL AND t.expires_at > now() AND u.status = 'active'`,
		hash,
	).Scan(&tenantID, &userID, &scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	if err != nil {
		return auth.Principal{}, fmt.Errorf("tenant: lookup token: %w", err)
	}
	return auth.Principal{TenantID: tenantID, UserID: &userID, Scopes: scopes, KeyID: audit.TokenID(hash)}, nil
}

// RevokeKey revokes an API key or user token of the tenant by hash. Revoking an
// already revoked secret succeeds; an unknown one, or another tenant's, is ErrNotFound.
func (s *Store) RevokeKey(ctx context.Context, tenantID uuid.UUID, hash string) error {
	for _, q := range []string{
		`UPDATE ohs_core.api_keys SET revoked_at = coalesce(revoked_at, now()) WHERE tenant_id = $1 AND key_hash = $2`,
		`UPDATE ohs_core.user_tokens SET revoked_at = coalesce(revoked_at, now()) WHERE tenant_id = $1 AND hash = $2`,
	} {
		tag, err := s.pool.Exec(ctx, q, tenantID, hash)
		if err != nil {
			return fmt.Errorf("tenant: revoke: %w", err)
		}
		if tag.RowsAffected() > 0 {
			return nil
		}
	}
	return ErrNotFound
}

// Idempotent runs do once per (tenant, key) within 24 hours and replays the stored
// status and body afterwards. requestHash fingerprints the request: the same key
// with another hash returns ErrIdempotencyMismatch (the API answers 422).
//
// Protocol: a short transaction claims the key with a pending row (status_code 0),
// the connection is released, do runs with no connection or lock held, and a second
// statement stores the result. A concurrent call with the same key while the first
// is running gets ErrIdempotencyInProgress (the API answers 409); a pending row
// older than 5 minutes is taken over, so a crashed caller does not block the key.
// An error from do, or a status of 500 and above, deletes the claim so a retry runs
// again. Semantics are at-least-once: if the process dies mid do, or a takeover
// races a slow first call, do may run twice for one key. Keys longer than 255
// bytes, and keys that are not valid UTF-8 or hold a NUL, are refused.
func (s *Store) Idempotent(ctx context.Context, tenantID uuid.UUID, key, requestHash string, do func() (int, []byte, error)) (int, []byte, error) {
	if key == "" || len(key) > maxIdempotencyKey || !pgtext.Valid(key) {
		return 0, nil, ErrIdempotencyKeyInvalid
	}
	status, body, done, err := s.claim(ctx, tenantID, key, requestHash)
	if err != nil || done {
		return status, body, err
	}

	// The caller's context may be cancelled by the time we settle the claim.
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	release := func() {
		if _, derr := s.pool.Exec(settle,
			`DELETE FROM ohs_core.idempotency_keys WHERE tenant_id = $1 AND key = $2 AND status_code = 0 AND request_hash = $3`,
			tenantID, key, requestHash); derr != nil {
			slog.ErrorContext(ctx, "tenant: release idempotency claim", "error", derr)
		}
	}

	status, body, err = do()
	if err != nil {
		release()
		return 0, nil, err
	}
	if status >= 500 || status <= 0 {
		release()
		return status, body, nil
	}
	if body == nil {
		body = []byte{}
	}
	var user *uuid.UUID
	if sub, ok := ctx.Value(idemSubjectKey{}).(*idemSubject); ok && sub.set {
		user = &sub.user
	}
	// A response that names a user is stored only while that user is active
	// (FOR SHARE waits for a deletion in flight): a deleted user's response
	// must never be replayed. The response is still sent this once.
	tag, err := s.pool.Exec(settle,
		`UPDATE ohs_core.idempotency_keys SET status_code = $4, response_body = $5, user_id = $6, created_at = now()
		 WHERE tenant_id = $1 AND key = $2 AND request_hash = $3 AND status_code = 0
		   AND ($6::uuid IS NULL OR EXISTS (
			SELECT 1 FROM ohs_core.end_users
			WHERE tenant_id = $1 AND id = $6 AND status = 'active' FOR SHARE))`,
		tenantID, key, requestHash, status, body, user)
	if err != nil {
		return 0, nil, fmt.Errorf("tenant: idempotency store: %w", err)
	}
	if tag.RowsAffected() == 0 && user != nil {
		release()
	}
	return status, body, nil
}

// idemSubjectKey carries the *idemSubject of one Idempotent call.
type idemSubjectKey struct{}

type idemSubject struct {
	user uuid.UUID
	set  bool
}

// WithIdempotencySubject returns a context for an Idempotent call in which
// the work may name the end user its response is about (SetIdempotencyUser).
// Pass the returned context both to Idempotent and to the work.
func WithIdempotencySubject(ctx context.Context) context.Context {
	return context.WithValue(ctx, idemSubjectKey{}, &idemSubject{})
}

// SetIdempotencyUser records that the response being produced names userID
// (POST /v1/users), so the stored response is deleted with that user. It is a
// no-op outside a context from WithIdempotencySubject.
func SetIdempotencyUser(ctx context.Context, userID uuid.UUID) {
	if sub, ok := ctx.Value(idemSubjectKey{}).(*idemSubject); ok {
		sub.user, sub.set = userID, true
	}
}

// PurgeIdempotency deletes the stored responses (and abandoned claims) older
// than the 24 hour idempotency window and returns how many went. Expired rows
// are never replayed, so this only keeps the table small and drops personal
// data that has no further use.
func (s *Store) PurgeIdempotency(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ohs_core.idempotency_keys WHERE created_at < now() - make_interval(secs => $1::double precision)`,
		idempotencyTTL.Seconds())
	if err != nil {
		return 0, fmt.Errorf("tenant: purge idempotency keys: %w", err)
	}
	return tag.RowsAffected(), nil
}

// claim returns done=true with a stored result, an error, or done=false when the
// caller now owns a pending row and must run the work.
func (s *Store) claim(ctx context.Context, tenantID uuid.UUID, key, requestHash string) (status int, body []byte, done bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, nil, false, fmt.Errorf("tenant: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx,
		`INSERT INTO ohs_core.idempotency_keys (tenant_id, key, request_hash, status_code, response_body)
		 VALUES ($1, $2, $3, 0, ''::bytea) ON CONFLICT (tenant_id, key) DO NOTHING`,
		tenantID, key, requestHash)
	if err != nil {
		return 0, nil, false, fmt.Errorf("tenant: idempotency claim: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return 0, nil, false, commit(ctx, tx)
	}

	var (
		hash string
		age  time.Duration
	)
	var ageSec float64
	err = tx.QueryRow(ctx,
		`SELECT status_code, response_body, request_hash, extract(epoch FROM now() - created_at)
		 FROM ohs_core.idempotency_keys WHERE tenant_id = $1 AND key = $2 FOR UPDATE`,
		tenantID, key).Scan(&status, &body, &hash, &ageSec)
	if err != nil {
		return 0, nil, false, fmt.Errorf("tenant: idempotency lookup: %w", err)
	}
	age = time.Duration(ageSec * float64(time.Second))

	takeOver := false
	switch {
	case status == 0 && age >= pendingTakeover:
		takeOver = true
	case status == 0:
		if hash != requestHash {
			return 0, nil, false, ErrIdempotencyMismatch
		}
		return 0, nil, false, ErrIdempotencyInProgress
	case age >= idempotencyTTL:
		takeOver = true
	default:
		if hash != requestHash {
			return 0, nil, false, ErrIdempotencyMismatch
		}
		return status, body, true, nil
	}
	if takeOver {
		if _, err := tx.Exec(ctx,
			`UPDATE ohs_core.idempotency_keys
			 SET request_hash = $3, status_code = 0, response_body = ''::bytea, created_at = now()
			 WHERE tenant_id = $1 AND key = $2`, tenantID, key, requestHash); err != nil {
			return 0, nil, false, fmt.Errorf("tenant: idempotency takeover: %w", err)
		}
	}
	return 0, nil, false, commit(ctx, tx)
}

func commit(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("tenant: commit: %w", err)
	}
	return nil
}

// Store.LookupKey must stay usable as the auth middleware's lookup.
var _ auth.Lookup = (*Store)(nil).LookupKey
