// SPDX-License-Identifier: AGPL-3.0-only

package providerset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
)

// Settings is what the console saved for one provider
// (ohs_core.provider_settings).
type Settings struct {
	Slug string
	// Keys are the operator keys entered in the console, by FIELD. Nil when
	// none was saved.
	Keys config.ProviderKeys
	// Enabled is false when the console turned the provider off.
	Enabled   bool
	UpdatedBy string
	UpdatedAt time.Time
	// OpenErr is set by List when the saved keys cannot be read (sealed
	// under a key no longer configured). Keys is nil then.
	OpenErr error
}

// Store is the settings table. The interface lets the Set be tested without
// a database.
type Store interface {
	List(ctx context.Context) ([]Settings, error)
	Put(ctx context.Context, s Settings) error
	Delete(ctx context.Context, slug string) (bool, error)
}

// KeysAAD binds the sealed keys of a row to its provider: a value moved to
// another row does not open.
func KeysAAD(slug string) []byte { return []byte("ohs_core.provider_settings|" + slug) }

// PG is the Postgres Store. The keys are sealed with OHS_ENCRYPTION_KEY.
type PG struct {
	pool *pgxpool.Pool
	box  *crypto.Box
}

// NewPG returns the Postgres Store.
func NewPG(pool *pgxpool.Pool, box *crypto.Box) *PG { return &PG{pool: pool, box: box} }

// List returns every row, by slug. A row whose keys no configured key opens
// comes back with OpenErr set, so only that provider is affected.
func (s *PG) List(ctx context.Context) ([]Settings, error) {
	rows, err := s.pool.Query(ctx, `SELECT slug, keys_sealed, enabled, updated_by, updated_at
		FROM ohs_core.provider_settings ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("providerset: list: %w", err)
	}
	type row struct {
		Settings
		sealed []byte
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.Slug, &x.sealed, &x.Enabled, &x.UpdatedBy, &x.UpdatedAt)
		return x, err
	})
	if err != nil {
		return nil, fmt.Errorf("providerset: list: %w", err)
	}
	out := make([]Settings, 0, len(got))
	for _, x := range got {
		if x.sealed != nil {
			plain, err := s.box.OpenFor(x.sealed, KeysAAD(x.Slug))
			if err == nil {
				err = json.Unmarshal(plain, &x.Keys)
			}
			if err != nil {
				x.Keys = nil
				x.OpenErr = errors.New("the keys saved in the console do not open with OHS_ENCRYPTION_KEY or " +
					"OHS_PREVIOUS_ENCRYPTION_KEYS: add the key that sealed them, or save them again")
			}
		}
		x.UpdatedAt = x.UpdatedAt.UTC()
		out = append(out, x.Settings)
	}
	return out, nil
}

// Put stores one row, replacing the provider's previous one. Empty Keys are
// stored as none.
func (s *PG) Put(ctx context.Context, st Settings) error {
	var sealed []byte
	if len(st.Keys) > 0 {
		plain, err := json.Marshal(st.Keys)
		if err != nil {
			return fmt.Errorf("providerset: encode keys: %w", err)
		}
		if sealed, err = s.box.SealFor(plain, KeysAAD(st.Slug)); err != nil {
			return fmt.Errorf("providerset: seal keys: %w", err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO ohs_core.provider_settings (slug, keys_sealed, enabled, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, clock_timestamp())
		ON CONFLICT (slug) DO UPDATE SET keys_sealed = EXCLUDED.keys_sealed, enabled = EXCLUDED.enabled,
			updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at`,
		st.Slug, sealed, st.Enabled, st.UpdatedBy); err != nil {
		return fmt.Errorf("providerset: put %s: %w", st.Slug, err)
	}
	return nil
}

// Delete removes the provider's row and reports whether there was one.
func (s *PG) Delete(ctx context.Context, slug string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ohs_core.provider_settings WHERE slug = $1`, slug)
	if err != nil {
		return false, fmt.Errorf("providerset: delete %s: %w", slug, err)
	}
	return tag.RowsAffected() > 0, nil
}

// errNoStore is returned by the writes of a Set built without a Store.
var errNoStore = errors.New("providerset: no settings store")
