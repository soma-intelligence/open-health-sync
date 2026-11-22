// SPDX-License-Identifier: AGPL-3.0-only

package settings

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
)

// SealAAD binds the sealed value of a row to its setting name: a value moved
// to another row does not open.
func SealAAD(name string) []byte { return []byte("ohs_core.instance_settings|" + name) }

// Row is one saved setting.
type Row struct {
	Name string
	// Value is the clear text, or the opened secret. Empty when OpenErr is set.
	Value     string
	Sealed    bool
	UpdatedAt time.Time
	UpdatedBy uuid.UUID
	// OpenErr is set when a sealed value no configured key opens. It never
	// carries the value.
	OpenErr error
}

// State is the table at one revision.
type State struct {
	Revision, RestartRevision int64
	ChangedAt                 time.Time
	Rows                      map[string]Row
}

// Change is one write: values to save and names to remove.
type Change struct {
	Put    map[string]string
	Delete []string
	// Restart is true when a restart setting is among the names:
	// restart_revision moves with the revision.
	Restart bool
	By      uuid.UUID
}

// ErrRevisionMoved is returned by Apply when the revision is no longer the
// one the caller read.
var ErrRevisionMoved = errors.New("settings: the revision moved")

// Store is the settings table. The interface lets callers be tested without
// a database.
type Store interface {
	Revision(ctx context.Context) (int64, error)
	// Load reads the revision and the rows in one REPEATABLE READ transaction.
	Load(ctx context.Context) (State, error)
	// Apply writes ch if the revision is still ifRevision and returns the
	// revision after the call.
	Apply(ctx context.Context, ifRevision int64, ch Change) (int64, error)
}

// PG is the Postgres Store. Secrets are sealed with OHS_ENCRYPTION_KEY.
type PG struct {
	pool *pgxpool.Pool
	box  *crypto.Box
}

// NewPG returns the Postgres Store.
func NewPG(pool *pgxpool.Pool, box *crypto.Box) *PG { return &PG{pool: pool, box: box} }

// Revision returns the current revision.
func (s *PG) Revision(ctx context.Context) (int64, error) {
	var rev int64
	if err := s.pool.QueryRow(ctx, `SELECT revision FROM ohs_core.instance_settings_revision`).Scan(&rev); err != nil {
		return 0, fmt.Errorf("settings: revision: %w", err)
	}
	return rev, nil
}

// Load returns the revision counters and every row, read in one snapshot. A
// sealed row no configured key opens comes back with OpenErr set and no value.
func (s *PG) Load(ctx context.Context) (State, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return State{}, fmt.Errorf("settings: load: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read only

	st := State{Rows: map[string]Row{}}
	if err := tx.QueryRow(ctx, `SELECT revision, restart_revision, changed_at FROM ohs_core.instance_settings_revision`).
		Scan(&st.Revision, &st.RestartRevision, &st.ChangedAt); err != nil {
		return State{}, fmt.Errorf("settings: load: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT name, value, sealed, updated_at, updated_by FROM ohs_core.instance_settings`)
	if err != nil {
		return State{}, fmt.Errorf("settings: load: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r Row
		var value *string
		var sealed []byte
		if err := rows.Scan(&r.Name, &value, &sealed, &r.UpdatedAt, &r.UpdatedBy); err != nil {
			return State{}, fmt.Errorf("settings: load: %w", err)
		}
		if sealed != nil {
			r.Sealed = true
			plain, err := s.box.OpenFor(sealed, SealAAD(r.Name))
			if err != nil {
				r.OpenErr = fmt.Errorf("settings: %s: sealed value does not open with a configured key", r.Name)
			} else {
				r.Value = string(plain)
			}
		} else if value != nil {
			r.Value = *value
		}
		st.Rows[r.Name] = r
	}
	if err := rows.Err(); err != nil {
		return State{}, fmt.Errorf("settings: load: %w", err)
	}
	return st, nil
}

// Apply writes ch in one transaction that also moves the revision. It locks
// the revision row, so concurrent calls run one after the other and the
// second sees the moved revision. When no row changed the revision stays.
func (s *PG) Apply(ctx context.Context, ifRevision int64, ch Change) (int64, error) {
	// Seal before the transaction, so a failure holds no lock.
	type put struct {
		name, value string
		sealed      []byte
	}
	names := make([]string, 0, len(ch.Put))
	for n := range ch.Put {
		names = append(names, n)
	}
	sort.Strings(names)
	puts := make([]put, 0, len(names))
	for _, n := range names {
		p := put{name: n}
		if def, ok := config.LookupSetting(n); ok && def.Secret {
			sealed, err := s.box.SealFor([]byte(ch.Put[n]), SealAAD(n))
			if err != nil {
				return 0, fmt.Errorf("settings: seal %s: %w", n, err)
			}
			p.sealed = sealed
		} else {
			p.value = ch.Put[n]
		}
		puts = append(puts, p)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("settings: apply: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a committed tx ignores it

	var rev int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM ohs_core.instance_settings_revision FOR UPDATE`).Scan(&rev); err != nil {
		return 0, fmt.Errorf("settings: apply: %w", err)
	}
	if rev != ifRevision {
		return 0, ErrRevisionMoved
	}

	changed := false
	for _, p := range puts {
		var value *string
		if p.sealed == nil {
			v := p.value
			value = &v
		}
		tag, err := tx.Exec(ctx, `INSERT INTO ohs_core.instance_settings (name, value, sealed, updated_by)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (name) DO UPDATE
			SET value = EXCLUDED.value, sealed = EXCLUDED.sealed, updated_at = now(), updated_by = EXCLUDED.updated_by`,
			p.name, value, p.sealed, ch.By)
		if err != nil {
			return 0, fmt.Errorf("settings: save %s: %w", p.name, err)
		}
		changed = changed || tag.RowsAffected() > 0
	}
	if len(ch.Delete) > 0 {
		tag, err := tx.Exec(ctx, `DELETE FROM ohs_core.instance_settings WHERE name = ANY($1)`, ch.Delete)
		if err != nil {
			return 0, fmt.Errorf("settings: delete: %w", err)
		}
		changed = changed || tag.RowsAffected() > 0
	}
	if changed {
		rev++
		if ch.Restart {
			_, err = tx.Exec(ctx, `UPDATE ohs_core.instance_settings_revision
				SET revision = $1, restart_revision = $1, changed_at = now()`, rev)
		} else {
			_, err = tx.Exec(ctx, `UPDATE ohs_core.instance_settings_revision
				SET revision = $1, changed_at = now()`, rev)
		}
		if err != nil {
			return 0, fmt.Errorf("settings: apply: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("settings: apply: %w", err)
	}
	return rev, nil
}
