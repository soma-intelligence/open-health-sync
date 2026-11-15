// SPDX-License-Identifier: AGPL-3.0-only

// Package consolesession keeps the server sessions of the web console: a
// session token and a CSRF token, each stored as SHA-256 hex only, tied to
// the API key the person signed in with.
package consolesession

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
)

const (
	// IdleTimeout ends a session that made no request for this long.
	IdleTimeout = 12 * time.Hour
	// MaxAge ends a session this long after it was created.
	MaxAge = 7 * 24 * time.Hour
	// TouchEvery bounds how often last_seen_at is written.
	TouchEvery = time.Minute
)

var (
	// ErrNotFound means no live session: unknown, ended, or its key revoked or expired.
	ErrNotFound = errors.New("consolesession: no such session")
	// ErrTenantNotFound means the tenant to act as does not exist.
	ErrTenantNotFound = errors.New("consolesession: no such tenant")
)

// Session is a live console session with what the console needs to show.
type Session struct {
	ID, TenantID, APIKeyID, ActingTenantID uuid.UUID
	ReadOnly                               bool
	CSRFHash                               string // SHA-256 hex
	CreatedAt, LastSeenAt                  time.Time
	TenantName                             string
	TenantAdmin                            bool
	ActingTenantName                       string
	ActingTenantAdmin                      bool
	KeyPrefix                              string // auth.KeyPrefixLen characters
	KeyScopes                              []string
}

// ExpiresAt is when the session ends whatever its activity.
func (s Session) ExpiresAt() time.Time { return s.CreatedAt.Add(MaxAge) }

// Store reads and writes console sessions.
type Store struct{ pool *pgxpool.Pool }

// New returns a Store on pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// NewSecret returns 32 random bytes as base64url without padding (43 characters).
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("consolesession: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func interval(d time.Duration) string { return fmt.Sprintf("%d microseconds", d.Microseconds()) }

// live is the condition of a session that has not ended, on aliases s and k.
// A session that views another tenant than its own ends when its tenant stops
// being the instance admin.
const live = `k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > now())
	AND (s.acting_tenant_id = s.tenant_id
		OR EXISTS (SELECT 1 FROM ohs_core.tenants st WHERE st.id = s.tenant_id AND st.admin))
	AND s.last_seen_at > now() - $2::interval AND s.created_at > now() - $3::interval`

const selectSession = `SELECT s.id, s.tenant_id, s.api_key_id, s.acting_tenant_id, s.read_only, s.csrf_hash,
	s.created_at, s.last_seen_at, t.name, t.admin, a.name, a.admin, k.key_prefix, k.scopes
	FROM ohs_core.console_sessions s
	JOIN ohs_core.api_keys k ON k.id = s.api_key_id
	JOIN ohs_core.tenants t ON t.id = s.tenant_id
	JOIN ohs_core.tenants a ON a.id = s.acting_tenant_id`

func scan(r pgx.Row) (Session, error) {
	var s Session
	err := r.Scan(&s.ID, &s.TenantID, &s.APIKeyID, &s.ActingTenantID, &s.ReadOnly, &s.CSRFHash,
		&s.CreatedAt, &s.LastSeenAt, &s.TenantName, &s.TenantAdmin, &s.ActingTenantName, &s.ActingTenantAdmin,
		&s.KeyPrefix, &s.KeyScopes)
	return s, err
}

// byID reads a live session by id.
func (s *Store) byID(ctx context.Context, id uuid.UUID) (Session, error) {
	sess, err := scan(s.pool.QueryRow(ctx, selectSession+` WHERE s.id = $1 AND `+live,
		id, interval(IdleTimeout), interval(MaxAge)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("consolesession: read: %w", err)
	}
	return sess, nil
}

// Create opens a session for a live key of the tenant and returns the session
// token and the CSRF token (shown once, stored hashed). A key that is not the
// tenant's, or is revoked or expired, is ErrNotFound.
func (s *Store) Create(ctx context.Context, tenantID, apiKeyID uuid.UUID, readOnly bool) (token, csrf string, sess Session, err error) {
	if token, err = NewSecret(); err != nil {
		return "", "", Session{}, err
	}
	if csrf, err = NewSecret(); err != nil {
		return "", "", Session{}, err
	}
	var id uuid.UUID
	err = s.pool.QueryRow(ctx,
		`INSERT INTO ohs_core.console_sessions (token_hash, tenant_id, api_key_id, acting_tenant_id, read_only, csrf_hash)
		 SELECT $1, k.tenant_id, k.id, k.tenant_id, $4, $5
		 FROM ohs_core.api_keys k
		 WHERE k.id = $2 AND k.tenant_id = $3 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > now())
		 RETURNING id`,
		auth.Hash(token), apiKeyID, tenantID, readOnly, auth.Hash(csrf)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", Session{}, ErrNotFound
	}
	if err != nil {
		return "", "", Session{}, fmt.Errorf("consolesession: create: %w", err)
	}
	sess, err = s.byID(ctx, id)
	if err != nil {
		return "", "", Session{}, err
	}
	return token, csrf, sess, nil
}

// Lookup resolves the SHA-256 hex of a session token. Ended sessions (idle,
// too old, key revoked or expired) are ErrNotFound. last_seen_at is written
// at most once a minute, best effort.
func (s *Store) Lookup(ctx context.Context, tokenHash string) (Session, error) {
	sess, err := scan(s.pool.QueryRow(ctx, selectSession+` WHERE s.token_hash = $1 AND `+live,
		tokenHash, interval(IdleTimeout), interval(MaxAge)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("consolesession: lookup: %w", err)
	}
	if time.Since(sess.LastSeenAt) > TouchEvery {
		// Best effort, and at most once a minute per session.
		if _, terr := s.pool.Exec(ctx,
			`UPDATE ohs_core.console_sessions SET last_seen_at = now() WHERE id = $1`, sess.ID); terr != nil {
			slog.DebugContext(ctx, "consolesession: touch", "error", terr)
		}
	}
	return sess, nil
}

// RotateCSRF replaces the CSRF token of a live session and returns the new one.
func (s *Store) RotateCSRF(ctx context.Context, id uuid.UUID) (string, error) {
	csrf, err := NewSecret()
	if err != nil {
		return "", err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE ohs_core.console_sessions s SET csrf_hash = $4
		FROM ohs_core.api_keys k WHERE k.id = s.api_key_id AND s.id = $1 AND `+live,
		id, interval(IdleTimeout), interval(MaxAge), auth.Hash(csrf))
	if err != nil {
		return "", fmt.Errorf("consolesession: rotate csrf: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return csrf, nil
}

// SetActingTenant points a live session at another tenant. An unknown tenant
// is ErrTenantNotFound and leaves the row unchanged; an unknown or ended
// session is ErrNotFound.
func (s *Store) SetActingTenant(ctx context.Context, id, tenantID uuid.UUID) (Session, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ohs_core.tenants WHERE id = $1)`, tenantID).Scan(&exists); err != nil {
		return Session{}, fmt.Errorf("consolesession: acting tenant: %w", err)
	}
	if !exists {
		return Session{}, ErrTenantNotFound
	}
	tag, err := s.pool.Exec(ctx, `UPDATE ohs_core.console_sessions s SET acting_tenant_id = $4
		FROM ohs_core.api_keys k WHERE k.id = s.api_key_id AND s.id = $1 AND `+live,
		id, interval(IdleTimeout), interval(MaxAge), tenantID)
	if err != nil {
		return Session{}, fmt.Errorf("consolesession: acting tenant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Session{}, ErrNotFound
	}
	return s.byID(ctx, id)
}

// Delete ends a session (sign out). An unknown id is not an error.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM ohs_core.console_sessions WHERE id = $1`, id); err != nil {
		return fmt.Errorf("consolesession: delete: %w", err)
	}
	return nil
}

// PurgeEnded deletes the sessions that are idle, too old, or whose key is
// revoked or expired, and returns how many.
func (s *Store) PurgeEnded(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ohs_core.console_sessions s USING ohs_core.api_keys k
		WHERE k.id = s.api_key_id AND (k.revoked_at IS NOT NULL OR k.expires_at <= now()
			OR s.last_seen_at <= now() - $1::interval OR s.created_at <= now() - $2::interval)`,
		interval(IdleTimeout), interval(MaxAge))
	if err != nil {
		return 0, fmt.Errorf("consolesession: purge: %w", err)
	}
	return tag.RowsAffected(), nil
}
