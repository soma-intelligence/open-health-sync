// SPDX-License-Identifier: AGPL-3.0-only

package consent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultPurgeEvery is how often the purger looks for queued purges when the
// last look found none.
const DefaultPurgeEvery = 10 * time.Second

// Purger deletes the stored records of revoked categories, one queued purge
// per transaction. It runs with the etl role.
type Purger struct {
	pool  *pgxpool.Pool
	every time.Duration
}

// NewPurger returns a Purger polling every DefaultPurgeEvery.
func NewPurger(pool *pgxpool.Pool) *Purger { return &Purger{pool: pool, every: DefaultPurgeEvery} }

// Run purges until ctx is done. A failed purge is logged and retried on the
// next pass.
func (p *Purger) Run(ctx context.Context) error {
	for {
		did, err := p.Once(ctx)
		if err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "consent: purge failed", "error", err)
		}
		if did && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(p.every):
		}
	}
}

// Once carries out the oldest queued purge, if any, and reports whether it
// found one.
func (p *Purger) Once(ctx context.Context) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("consent: purge: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var (
		id            int64
		tenant, user  uuid.UUID
		provider, cat string
	)
	err = tx.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, provider, category FROM ohs_sync.consent_purges
		WHERE finished_at IS NULL ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`).
		Scan(&id, &tenant, &user, &provider, &cat)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("consent: purge: claim: %w", err)
	}
	// Granted again since the purge was queued: keep the data.
	var granted bool
	err = tx.QueryRow(ctx, `
		SELECT granted FROM ohs_sync.consents
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND category = $4`,
		tenant, user, provider, cat).Scan(&granted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("consent: purge: %w", err)
	}
	n := int64(0)
	if !granted {
		if n, err = purge(ctx, tx, tenant, user, provider, cat); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ohs_sync.consent_purges SET finished_at = now(), rows_deleted = $2 WHERE id = $1`, id, n); err != nil {
		return false, fmt.Errorf("consent: purge: finish: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("consent: purge: commit: %w", err)
	}
	if n > 0 {
		slog.InfoContext(ctx, "consent: revoked category purged", "tenant_id", tenant, "user_id", user,
			"provider", provider, "category", cat, "rows", n)
	}
	return true, nil
}

// typedTables hold a type name per row: the rows of the category are those
// whose type Of files under it.
var typedTables = []struct{ table, col string }{
	{"ohs_data.quantity_samples", "quantity_type"},
	{"ohs_data.category_samples", "category_type"},
	{"ohs_data.observations", "canonical_code"},
}

// purge deletes the user's records of category cat that came from provider,
// and returns how many rows went.
func purge(ctx context.Context, tx pgx.Tx, tenant, user uuid.UUID, provider, cat string) (int64, error) {
	// A user's rows in a compressed chunk may need decompressing.
	if _, err := tx.Exec(ctx, `SET LOCAL timescaledb.max_tuples_decompressed_per_dml_transaction = 0`); err != nil {
		return 0, fmt.Errorf("consent: purge: %w", err)
	}
	var total int64
	exec := func(sql string, args ...any) error {
		tag, err := tx.Exec(ctx, sql, append([]any{tenant, user, provider}, args...)...)
		if err != nil {
			return fmt.Errorf("consent: purge %s: %w", cat, err)
		}
		total += tag.RowsAffected()
		return nil
	}
	for _, t := range typedTables {
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT DISTINCT %s FROM %s
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`, t.col, t.table), tenant, user, provider)
		if err != nil {
			return 0, fmt.Errorf("consent: purge: types of %s: %w", t.table, err)
		}
		types, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return 0, fmt.Errorf("consent: purge: types of %s: %w", t.table, err)
		}
		var match []string
		for _, typ := range types {
			if Of(typ) == cat {
				match = append(match, typ)
			}
		}
		if len(match) == 0 {
			continue
		}
		if err := exec(fmt.Sprintf(`DELETE FROM %s
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND %s = ANY($4)`, t.table, t.col), match); err != nil {
			return 0, err
		}
	}

	const ofWorkouts = `workout_id IN (SELECT id FROM ohs_data.workouts
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3)`
	var stmts []string
	switch cat {
	case Sleep:
		stmts = []string{
			`DELETE FROM ohs_data.sleep_phases WHERE tenant_id = $1 AND user_id = $2 AND session_id IN
				(SELECT id FROM ohs_data.sleep_sessions WHERE tenant_id = $1 AND user_id = $2 AND provider = $3)`,
			`DELETE FROM ohs_data.sleep_sessions WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
		}
	case Activity:
		stmts = []string{
			`DELETE FROM ohs_data.route_points WHERE tenant_id = $1 AND user_id = $2 AND ` + ofWorkouts,
			`DELETE FROM ohs_data.workout_tracks WHERE tenant_id = $1 AND user_id = $2 AND ` + ofWorkouts,
			`DELETE FROM ohs_data.workouts WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
			`DELETE FROM ohs_data.daily_summaries WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
		}
	case Location:
		stmts = []string{
			`DELETE FROM ohs_data.route_points WHERE tenant_id = $1 AND user_id = $2 AND ` + ofWorkouts,
			`DELETE FROM ohs_data.workout_tracks WHERE tenant_id = $1 AND user_id = $2 AND ` + ofWorkouts,
		}
	case ECG:
		stmts = []string{
			`DELETE FROM ohs_data.ecg_samples WHERE tenant_id = $1 AND user_id = $2 AND ecg_id IN
				(SELECT id FROM ohs_data.ecg_recordings WHERE tenant_id = $1 AND user_id = $2 AND provider = $3)`,
			`DELETE FROM ohs_data.ecg_recordings WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
		}
	case Heart:
		stmts = []string{`DELETE FROM ohs_data.heartbeat_samples WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`}
	case Nutrition:
		stmts = []string{`DELETE FROM ohs_data.nutrition_logs WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`}
	}
	for _, q := range stmts {
		if err := exec(q); err != nil {
			return 0, err
		}
	}
	return total, nil
}
