// SPDX-License-Identifier: AGPL-3.0-only

package tenantproviders

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
)

// PG is the Postgres Store.
type PG struct{ pool *pgxpool.Pool }

// NewPG returns the Postgres Store.
func NewPG(pool *pgxpool.Pool) *PG { return &PG{pool: pool} }

// Off lists the providers turned off, for every tenant.
func (s *PG) Off(ctx context.Context) ([]Key, error) {
	rows, err := s.pool.Query(ctx, `SELECT tenant_id, provider FROM ohs_core.tenant_providers WHERE NOT enabled`)
	if err != nil {
		return nil, fmt.Errorf("tenantproviders: list off: %w", err)
	}
	keys, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Key, error) {
		var k Key
		err := r.Scan(&k.Tenant, &k.Provider)
		return k, err
	})
	if err != nil {
		return nil, fmt.Errorf("tenantproviders: list off: %w", err)
	}
	return keys, nil
}

const settingCols = `tenant_id, provider, enabled, coalesce(updated_by, ''), updated_at`

func scanSetting(r pgx.Row) (Setting, error) {
	var st Setting
	err := r.Scan(&st.Tenant, &st.Provider, &st.Enabled, &st.UpdatedBy, &st.UpdatedAt)
	st.UpdatedAt = st.UpdatedAt.UTC()
	return st, err
}

// List returns the rows of one tenant, by provider.
func (s *PG) List(ctx context.Context, tenant uuid.UUID) ([]Setting, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+settingCols+` FROM ohs_core.tenant_providers
		WHERE tenant_id = $1 ORDER BY provider`, tenant)
	if err != nil {
		return nil, fmt.Errorf("tenantproviders: list: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Setting, error) { return scanSetting(r) })
	if err != nil {
		return nil, fmt.Errorf("tenantproviders: list: %w", err)
	}
	return out, nil
}

// Put stores one row and, when ev is set, records it for the row's tenant in
// the same transaction: a failed audit write changes nothing.
func (s *PG) Put(ctx context.Context, st Setting, ev *audit.Event) (Setting, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Setting{}, fmt.Errorf("tenantproviders: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var by *string
	if st.UpdatedBy != "" {
		by = &st.UpdatedBy
	}
	out, err := scanSetting(tx.QueryRow(ctx, `
		INSERT INTO ohs_core.tenant_providers (tenant_id, provider, enabled, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, clock_timestamp())
		ON CONFLICT (tenant_id, provider) DO UPDATE SET enabled = EXCLUDED.enabled,
			updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at
		RETURNING `+settingCols, st.Tenant, st.Provider, st.Enabled, by))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return Setting{}, ErrUnknownTenant
	}
	if err != nil {
		return Setting{}, fmt.Errorf("tenantproviders: put %s: %w", st.Provider, err)
	}
	if ev != nil {
		e := *ev
		e.Tenant = st.Tenant
		if err := audit.Record(ctx, tx, e); err != nil {
			return Setting{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Setting{}, fmt.Errorf("tenantproviders: commit: %w", err)
	}
	return out, nil
}
