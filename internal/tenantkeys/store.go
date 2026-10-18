// SPDX-License-Identifier: AGPL-3.0-only

package tenantkeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
)

// Keys is one stored row of ohs_core.tenant_provider_keys.
type Keys struct {
	Tenant   uuid.UUID
	Provider string
	// ID names the OAuth application the keys belong to: kept when a
	// secret changes, new when the client id does (SameApp).
	ID uuid.UUID
	// Keys are the keys by FIELD, secrets included: never serve their
	// values (connector.SecretKey says which may be shown).
	Keys                 config.ProviderKeys
	CreatedAt, UpdatedAt time.Time
	// CreatedBy and UpdatedBy are ids of the admin keys that saved the row.
	CreatedBy, UpdatedBy string
	// OpenErr is set when the keys do not open with the configured
	// encryption keys. Keys is nil then.
	OpenErr error
}

// Retire runs in the transaction that removes a tenant's keys, or replaces
// them with another application's: it marks the connections made with keysID
// as needing a new consent and returns how many it marked. The app wires the
// collect role's (collect.RetireTenantKeys).
type Retire func(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, provider string, keysID uuid.UUID) (int, error)

// Store is the table. The interface lets the Set be tested without a
// database.
type Store interface {
	// List returns every row, of every tenant. A row whose keys do not
	// open comes back with OpenErr set.
	List(ctx context.Context) ([]Keys, error)
	// Get returns the row of one tenant and provider.
	Get(ctx context.Context, tenant uuid.UUID, provider string) (Keys, bool, error)
	// Put stores k.Keys for k.Tenant and k.Provider, by k.UpdatedBy. It
	// keeps the ID of the row it replaces when same(old keys) holds, else
	// gives the row a new ID and retires the old one. It returns the row as
	// stored and how many connections were retired. ev, when set, is
	// recorded on the tenant's audit log in the same transaction. An
	// unknown tenant is ErrUnknownTenant.
	Put(ctx context.Context, k Keys, same func(old config.ProviderKeys) bool, ev *audit.Event) (Keys, int, error)
	// Delete removes the row and retires its connections, with ev in the
	// same transaction. It returns the row removed, how many connections
	// were retired, and false when there was no row.
	Delete(ctx context.Context, tenant uuid.UUID, provider string, ev *audit.Event) (Keys, int, bool, error)
}

// KeysAAD binds the sealed keys of a row to its tenant and provider: a value
// moved to another row does not open.
func KeysAAD(tenant uuid.UUID, provider string) []byte {
	return []byte("ohs_core.tenant_provider_keys|" + tenant.String() + "|" + provider)
}

// errNotOpen is the OpenErr of keys sealed under a key no longer configured.
var errNotOpen = errors.New("the tenant's keys do not open with OHS_ENCRYPTION_KEY or " +
	"OHS_PREVIOUS_ENCRYPTION_KEYS: add the key that sealed them, or set them again")

// PG is the Postgres Store. The keys are sealed with OHS_ENCRYPTION_KEY.
type PG struct {
	pool   *pgxpool.Pool
	box    *crypto.Box
	retire Retire
}

// NewPG returns the Postgres Store. retire may be nil (tools and tests):
// the connections are then left as they are.
func NewPG(pool *pgxpool.Pool, box *crypto.Box, retire Retire) *PG {
	return &PG{pool: pool, box: box, retire: retire}
}

const keysCols = `tenant_id, provider, keys_id, keys_sealed, created_at, created_by, updated_at, updated_by`

func (s *PG) scan(r pgx.Row) (Keys, error) {
	var k Keys
	var sealed []byte
	if err := r.Scan(&k.Tenant, &k.Provider, &k.ID, &sealed, &k.CreatedAt, &k.CreatedBy, &k.UpdatedAt, &k.UpdatedBy); err != nil {
		return Keys{}, err
	}
	k.CreatedAt, k.UpdatedAt = k.CreatedAt.UTC(), k.UpdatedAt.UTC()
	plain, err := s.box.OpenFor(sealed, KeysAAD(k.Tenant, k.Provider))
	if err == nil {
		err = json.Unmarshal(plain, &k.Keys)
	}
	if err != nil {
		k.Keys, k.OpenErr = nil, errNotOpen
	}
	return k, nil
}

// List returns every row, by tenant and provider.
func (s *PG) List(ctx context.Context) ([]Keys, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+keysCols+` FROM ohs_core.tenant_provider_keys ORDER BY tenant_id, provider`)
	if err != nil {
		return nil, fmt.Errorf("tenantkeys: list: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Keys, error) { return s.scan(r) })
	if err != nil {
		return nil, fmt.Errorf("tenantkeys: list: %w", err)
	}
	return out, nil
}

// Get returns one row.
func (s *PG) Get(ctx context.Context, tenant uuid.UUID, provider string) (Keys, bool, error) {
	k, err := s.scan(s.pool.QueryRow(ctx, `SELECT `+keysCols+` FROM ohs_core.tenant_provider_keys
		WHERE tenant_id = $1 AND provider = $2`, tenant, provider))
	if errors.Is(err, pgx.ErrNoRows) {
		return Keys{}, false, nil
	}
	if err != nil {
		return Keys{}, false, fmt.Errorf("tenantkeys: get: %w", err)
	}
	return k, true, nil
}

// Put stores one row; see Store.
func (s *PG) Put(ctx context.Context, k Keys, same func(old config.ProviderKeys) bool, ev *audit.Event) (Keys, int, error) {
	plain, err := json.Marshal(k.Keys)
	if err != nil {
		return Keys{}, 0, fmt.Errorf("tenantkeys: encode keys: %w", err)
	}
	sealed, err := s.box.SealFor(plain, KeysAAD(k.Tenant, k.Provider))
	if err != nil {
		return Keys{}, 0, fmt.Errorf("tenantkeys: seal keys: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Keys{}, 0, fmt.Errorf("tenantkeys: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	prev, err := s.scan(tx.QueryRow(ctx, `SELECT `+keysCols+` FROM ohs_core.tenant_provider_keys
		WHERE tenant_id = $1 AND provider = $2 FOR UPDATE`, k.Tenant, k.Provider))
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Keys{}, 0, fmt.Errorf("tenantkeys: lock: %w", err)
	}
	id := uuid.New()
	retired := 0
	if found {
		if prev.OpenErr == nil && same(prev.Keys) {
			id = prev.ID
		} else if s.retire != nil {
			if retired, err = s.retire(ctx, tx, k.Tenant, k.Provider, prev.ID); err != nil {
				return Keys{}, 0, err
			}
		}
	}
	out, err := s.scan(tx.QueryRow(ctx, `
		INSERT INTO ohs_core.tenant_provider_keys (tenant_id, provider, keys_id, keys_sealed,
			created_at, created_by, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, clock_timestamp(), $5, clock_timestamp(), $5)
		ON CONFLICT (tenant_id, provider) DO UPDATE SET keys_id = EXCLUDED.keys_id,
			keys_sealed = EXCLUDED.keys_sealed, updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by
		RETURNING `+keysCols, k.Tenant, k.Provider, id, sealed, k.UpdatedBy))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return Keys{}, 0, ErrUnknownTenant
	}
	if err != nil {
		return Keys{}, 0, fmt.Errorf("tenantkeys: put %s: %w", k.Provider, err)
	}
	if err := record(ctx, tx, k.Tenant, ev, retired); err != nil {
		return Keys{}, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Keys{}, 0, fmt.Errorf("tenantkeys: commit: %w", err)
	}
	return out, retired, nil
}

// Delete removes one row; see Store.
func (s *PG) Delete(ctx context.Context, tenant uuid.UUID, provider string, ev *audit.Event) (Keys, int, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Keys{}, 0, false, fmt.Errorf("tenantkeys: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	prev, err := s.scan(tx.QueryRow(ctx, `DELETE FROM ohs_core.tenant_provider_keys
		WHERE tenant_id = $1 AND provider = $2 RETURNING `+keysCols, tenant, provider))
	if errors.Is(err, pgx.ErrNoRows) {
		return Keys{}, 0, false, nil
	}
	if err != nil {
		return Keys{}, 0, false, fmt.Errorf("tenantkeys: delete %s: %w", provider, err)
	}
	retired := 0
	if s.retire != nil {
		if retired, err = s.retire(ctx, tx, tenant, provider, prev.ID); err != nil {
			return Keys{}, 0, false, err
		}
	}
	if err := record(ctx, tx, tenant, ev, retired); err != nil {
		return Keys{}, 0, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Keys{}, 0, false, fmt.Errorf("tenantkeys: commit: %w", err)
	}
	return prev, retired, true, nil
}

// record writes ev, when set, on the tenant's audit log with the number of
// connections retired.
func record(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, ev *audit.Event, retired int) error {
	if ev == nil {
		return nil
	}
	e := *ev
	e.Tenant = tenant
	d := make(map[string]any, len(e.Detail)+1)
	maps.Copy(d, e.Detail)
	d["reauth_required"] = retired
	e.Detail = d
	return audit.Record(ctx, tx, e)
}
