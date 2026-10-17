// SPDX-License-Identifier: AGPL-3.0-only

package tenant

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
)

// ErrAmbiguousPrefix means a key prefix matches more than one key of the
// tenant: pass the whole key instead.
var ErrAmbiguousPrefix = errors.New("tenant: key prefix matches several keys")

// APIKey describes one tenant API key, never its secret. ID is what the
// audit log names the key by (auth.Principal.KeyID).
type APIKey struct {
	ID         uuid.UUID
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// TenantSummary is a tenant with its count of active API keys.
type TenantSummary struct {
	Tenant
	ActiveKeys int
}

// selectTenantSummary selects tenants with their count of active keys.
const selectTenantSummary = `SELECT t.id, t.name, t.created_at, t.admin,
	(SELECT count(*) FROM ohs_core.api_keys k WHERE k.tenant_id = t.id AND k.revoked_at IS NULL)
	FROM ohs_core.tenants t`

func scanTenantSummary(r pgx.CollectableRow) (TenantSummary, error) {
	var t TenantSummary
	err := r.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.Admin, &t.ActiveKeys)
	return t, err
}

// ListTenants lists every tenant, oldest first.
func (s *Store) ListTenants(ctx context.Context) ([]TenantSummary, error) {
	rows, err := s.pool.Query(ctx, selectTenantSummary+` ORDER BY t.created_at, t.id`)
	if err != nil {
		return nil, fmt.Errorf("tenant: list tenants: %w", err)
	}
	return pgx.CollectRows(rows, scanTenantSummary)
}

// ListTenantsPage pages through every tenant, oldest first, with the cursor
// of ListUsers. The returned cursor is empty on the last page. limit <= 0
// means 100; it is capped at 1000.
func (s *Store) ListTenantsPage(ctx context.Context, cursor string, limit int) ([]TenantSummary, string, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)
	afterTime, afterID := time.Time{}, uuid.Nil
	if cursor != "" {
		var err error
		if afterTime, afterID, err = decodeCursor(cursor); err != nil {
			return nil, "", err
		}
	}
	rows, err := s.pool.Query(ctx, selectTenantSummary+`
		WHERE (t.created_at, t.id) > ($1, $2) ORDER BY t.created_at, t.id LIMIT $3`, afterTime, afterID, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("tenant: list tenants: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanTenantSummary)
	if err != nil {
		return nil, "", fmt.Errorf("tenant: list tenants: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

// GetTenantSummary returns one tenant with its count of active keys. An
// unknown id is ErrNotFound.
func (s *Store) GetTenantSummary(ctx context.Context, tenantID uuid.UUID) (TenantSummary, error) {
	rows, err := s.pool.Query(ctx, selectTenantSummary+` WHERE t.id = $1`, tenantID)
	if err != nil {
		return TenantSummary{}, fmt.Errorf("tenant: get tenant: %w", err)
	}
	t, err := pgx.CollectExactlyOneRow(rows, scanTenantSummary)
	if errors.Is(err, pgx.ErrNoRows) {
		return TenantSummary{}, ErrNotFound
	}
	if err != nil {
		return TenantSummary{}, fmt.Errorf("tenant: get tenant: %w", err)
	}
	return t, nil
}

// CreateKey adds an API key to an existing tenant, for rotation: deploy the
// new key, then revoke the old one. The plaintext key is returned once.
func (s *Store) CreateKey(ctx context.Context, tenantID uuid.UUID) (string, error) {
	full, _, err := s.CreateKeyInfo(ctx, tenantID)
	return full, err
}

// CreateKeyInfo is CreateKey that also describes the new key.
func (s *Store) CreateKeyInfo(ctx context.Context, tenantID uuid.UUID) (string, APIKey, error) {
	return s.createKey(ctx, tenantID, nil)
}

// CreateKeyAudited is CreateKeyInfo that records ev in the audit log in the
// same transaction, for the tenant and with the new key's id and prefix added
// to its detail: a failed audit write leaves no key behind.
func (s *Store) CreateKeyAudited(ctx context.Context, tenantID uuid.UUID, ev audit.Event) (string, APIKey, error) {
	return s.createKey(ctx, tenantID, &ev)
}

func (s *Store) createKey(ctx context.Context, tenantID uuid.UUID, ev *audit.Event) (string, APIKey, error) {
	full, hash, err := auth.NewSecret(auth.PrefixSecretKey)
	if err != nil {
		return "", APIKey{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", APIKey{}, fmt.Errorf("tenant: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	k := APIKey{Prefix: full[:keyPrefixLen]}
	err = tx.QueryRow(ctx,
		`INSERT INTO ohs_core.api_keys (tenant_id, key_prefix, key_hash)
		 SELECT id, $2, $3 FROM ohs_core.tenants WHERE id = $1
		 RETURNING id, created_at`,
		tenantID, k.Prefix, hash).Scan(&k.ID, &k.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", APIKey{}, ErrNotFound
	}
	if err != nil {
		return "", APIKey{}, fmt.Errorf("tenant: create key: %w", err)
	}
	if ev != nil {
		if err := audit.Record(ctx, tx, keyEvent(*ev, tenantID, k)); err != nil {
			return "", APIKey{}, err
		}
	}
	if err := commit(ctx, tx); err != nil {
		return "", APIKey{}, err
	}
	return full, k, nil
}

// RevokeKeyByID revokes one API key of the tenant by its id. An id of no key
// of the tenant is ErrNotFound; revoking an already revoked key succeeds.
func (s *Store) RevokeKeyByID(ctx context.Context, tenantID, keyID uuid.UUID) error {
	_, err := s.revokeKeyByID(ctx, tenantID, keyID, nil)
	return err
}

// RevokeKeyAudited is RevokeKeyByID that returns the key and records ev in
// the audit log in the same transaction, for the tenant and with the key's id
// and prefix added to its detail: a failed audit write leaves the key as it
// was. Revoking a revoked key succeeds and is recorded again.
func (s *Store) RevokeKeyAudited(ctx context.Context, tenantID, keyID uuid.UUID, ev audit.Event) (APIKey, error) {
	return s.revokeKeyByID(ctx, tenantID, keyID, &ev)
}

func (s *Store) revokeKeyByID(ctx context.Context, tenantID, keyID uuid.UUID, ev *audit.Event) (APIKey, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return APIKey{}, fmt.Errorf("tenant: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var k APIKey
	err = tx.QueryRow(ctx,
		`UPDATE ohs_core.api_keys SET revoked_at = coalesce(revoked_at, now()) WHERE tenant_id = $1 AND id = $2
		 RETURNING id, key_prefix, created_at, last_used_at, revoked_at`,
		tenantID, keyID).Scan(&k.ID, &k.Prefix, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("tenant: revoke: %w", err)
	}
	if ev != nil {
		if err := audit.Record(ctx, tx, keyEvent(*ev, tenantID, k)); err != nil {
			return APIKey{}, err
		}
	}
	if err := commit(ctx, tx); err != nil {
		return APIKey{}, err
	}
	return k, nil
}

// keyEvent is ev recorded for tenantID, with the key's id and prefix added to
// a copy of its detail. The prefix is the one ListKeys shows followed by
// "...", as `ohs admin` writes it; the key itself never reaches the log.
func keyEvent(ev audit.Event, tenantID uuid.UUID, k APIKey) audit.Event {
	detail := make(map[string]any, len(ev.Detail)+2)
	maps.Copy(detail, ev.Detail)
	detail["key_id"] = k.ID.String()
	detail["key_prefix"] = k.Prefix + "..."
	ev.Tenant, ev.Detail = tenantID, detail
	return ev
}

// ListKeys lists the API keys of a tenant, revoked ones included, oldest
// first. An unknown tenant is ErrNotFound.
func (s *Store) ListKeys(ctx context.Context, tenantID uuid.UUID) ([]APIKey, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, key_prefix, created_at, last_used_at, revoked_at FROM ohs_core.api_keys
		 WHERE tenant_id = $1 ORDER BY created_at, id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("tenant: list keys: %w", err)
	}
	keys, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (APIKey, error) {
		var k APIKey
		err := r.Scan(&k.ID, &k.Prefix, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt)
		return k, err
	})
	if err != nil || len(keys) > 0 {
		return keys, err
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ohs_core.tenants WHERE id = $1)`, tenantID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("tenant: list keys: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	return keys, nil
}

// RevokeKeyByPrefix revokes one API key of the tenant, named by the prefix
// ListKeys shows or by the whole key. A prefix matching no key of the tenant
// is ErrNotFound, one matching several is ErrAmbiguousPrefix. Revoking an
// already revoked key succeeds.
func (s *Store) RevokeKeyByPrefix(ctx context.Context, tenantID uuid.UUID, key string) error {
	if len(key) > keyPrefixLen {
		return s.RevokeKey(ctx, tenantID, auth.Hash(key))
	}
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM ohs_core.api_keys WHERE tenant_id = $1 AND key_prefix = $2`,
		tenantID, key).Scan(&n); err != nil {
		return fmt.Errorf("tenant: revoke: %w", err)
	}
	switch {
	case n == 0:
		return ErrNotFound
	case n > 1:
		return ErrAmbiguousPrefix
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE ohs_core.api_keys SET revoked_at = coalesce(revoked_at, now()) WHERE tenant_id = $1 AND key_prefix = $2`,
		tenantID, key); err != nil {
		return fmt.Errorf("tenant: revoke: %w", err)
	}
	return nil
}
