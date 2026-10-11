// SPDX-License-Identifier: AGPL-3.0-only

package tenant

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
)

// ErrAmbiguousPrefix means a key prefix matches more than one key of the
// tenant: pass the whole key instead.
var ErrAmbiguousPrefix = errors.New("tenant: key prefix matches several keys")

// APIKey describes one tenant API key, never its secret.
type APIKey struct {
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

// ListTenants lists every tenant, oldest first.
func (s *Store) ListTenants(ctx context.Context) ([]TenantSummary, error) {
	rows, err := s.pool.Query(ctx, `SELECT t.id, t.name, t.created_at,
		(SELECT count(*) FROM ohs_core.api_keys k WHERE k.tenant_id = t.id AND k.revoked_at IS NULL)
		FROM ohs_core.tenants t ORDER BY t.created_at, t.id`)
	if err != nil {
		return nil, fmt.Errorf("tenant: list tenants: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TenantSummary, error) {
		var t TenantSummary
		err := r.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.ActiveKeys)
		return t, err
	})
}

// CreateKey adds an API key to an existing tenant, for rotation: deploy the
// new key, then revoke the old one. The plaintext key is returned once.
func (s *Store) CreateKey(ctx context.Context, tenantID uuid.UUID) (string, error) {
	full, hash, err := auth.NewSecret(auth.PrefixSecretKey)
	if err != nil {
		return "", err
	}
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO ohs_core.api_keys (tenant_id, key_prefix, key_hash)
		 SELECT id, $2, $3 FROM ohs_core.tenants WHERE id = $1`,
		tenantID, full[:keyPrefixLen], hash)
	if err != nil {
		return "", fmt.Errorf("tenant: create key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return full, nil
}

// ListKeys lists the API keys of a tenant, revoked ones included, oldest first.
func (s *Store) ListKeys(ctx context.Context, tenantID uuid.UUID) ([]APIKey, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT key_prefix, created_at, last_used_at, revoked_at FROM ohs_core.api_keys
		 WHERE tenant_id = $1 ORDER BY created_at, id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("tenant: list keys: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (APIKey, error) {
		var k APIKey
		err := r.Scan(&k.Prefix, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt)
		return k, err
	})
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
