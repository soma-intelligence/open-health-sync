// SPDX-License-Identifier: AGPL-3.0-only

package consent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/soma-intelligence/open-health-sync/internal/logging"
	"github.com/soma-intelligence/open-health-sync/internal/logutil"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
	"github.com/soma-intelligence/open-health-sync/internal/vault/series"
)

// DefaultPurgeEvery is how often the purger looks for queued purges when the
// last look found none.
const DefaultPurgeEvery = 10 * time.Second

// Purger deletes the stored records of revoked categories, one queued purge
// per transaction. It runs with the etl role.
type Purger struct {
	pool  *pgxpool.Pool
	every time.Duration
	// vault opens and reseals the sealed rows a purge takes a figure out of
	// (WithVault). Without it a purge that meets such a row fails, and is
	// retried, rather than leave the figure in the sealed value.
	vault *vault.Vault
}

// NewPurger returns a Purger polling every DefaultPurgeEvery.
func NewPurger(pool *pgxpool.Pool) *Purger { return &Purger{pool: pool, every: DefaultPurgeEvery} }

// WithVault returns the purger with the vault of the instance, which it
// needs to take a revoked figure out of a sealed row (see sealedFigures).
func (p *Purger) WithVault(v *vault.Vault) *Purger {
	p.vault = v
	return p
}

// Run purges until ctx is done. A failed purge is logged and retried on the
// next pass.
func (p *Purger) Run(ctx context.Context) error {
	for {
		did, err := p.Once(ctx)
		if err != nil && ctx.Err() == nil {
			logutil.ErrCtx(ctx, *logging.Ctx(ctx), err, zerolog.ErrorLevel).Msg("purge_failed")
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
	// Lock the user row like a provider erasure: an ETL job holding it FOR
	// SHARE commits first (its rows, and the hourly buckets it recomputed,
	// are purged below), and one arriving later waits for this purge and then
	// finds the category withheld. A user already gone has no row to lock;
	// the purge then removes whatever is left.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		tenant, user); err != nil {
		return false, fmt.Errorf("consent: purge: lock user: %w", err)
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
		if n, err = purge(ctx, tx, vault.NewReader(p.vault, tenant, user), tenant, user, provider, cat); err != nil {
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
		logging.Ctx(ctx).Info().Stringer("tenant_id", tenant).Stringer("user_id", user).Str("provider", provider).Str("category", cat).Int64("rows", n).Msg("revoked_category_purged")
	}
	return true, nil
}

// typedTables hold a type name per row: the rows of the category are those
// whose type Of files under it. aliased names the table as record aliases
// name it, when its rows can have aliases. A derived table holds statistics
// of the records, not records: its rows are deleted but not counted in
// rows_deleted, which counts the user's records the purge removed.
var typedTables = []struct {
	table, col, aliased string
	derived             bool
}{
	{"ohs_data.quantity_samples", "quantity_type", "quantity_samples", false},
	{"ohs_data.category_samples", "category_type", "category_samples", false},
	{"ohs_data.observations", "canonical_code", "", false},
	// The sealed hourly buckets of the samples (migration 0050) are per
	// provider and type too: they go with the samples they count.
	{"ohs_data.quantity_hourly_sealed", "quantity_type", "", true},
}

// aliasesOf is the delete of the record aliases (migration 0043) of the rows
// of table that cond, a condition on r, picks. It runs before those rows go:
// an alias keeps the row's content hash and start, which with the user and
// provider can be enough to work out the value it stood for.
func aliasesOf(table, cond string) string {
	return `DELETE FROM ohs_data.record_aliases a USING ohs_data.` + table + ` r
		WHERE a.tenant_id = $1 AND a.user_id = $2 AND a.provider = $3 AND a.record_table = '` + table + `'
		  AND r.tenant_id = $1 AND r.user_id = $2 AND r.provider = $3
		  AND r.content_hash = a.content_hash AND r.start_date = a.start_time` + cond
}

// allAliases is the delete of every record alias of table for the user and
// provider. It serves the tables whose purge takes all of their rows: with no
// join on a row, it also takes the aliases whose row is already gone.
func allAliases(table string) string {
	return `DELETE FROM ohs_data.record_aliases
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND record_table = '` + table + `'`
}

// purge deletes the user's records of category cat that came from provider,
// and the statistics derived from them, and returns how many records went.
func purge(ctx context.Context, tx pgx.Tx, keys *vault.Reader, tenant, user uuid.UUID, provider, cat string) (int64, error) {
	// A user's rows in a compressed chunk may need decompressing.
	if _, err := tx.Exec(ctx, `SET LOCAL timescaledb.max_tuples_decompressed_per_dml_transaction = 0`); err != nil {
		return 0, fmt.Errorf("consent: purge: %w", err)
	}
	var total int64
	// del runs one delete and returns the rows it removed; exec counts them
	// in total.
	del := func(sql string, args ...any) (int64, error) {
		tag, err := tx.Exec(ctx, sql, append([]any{tenant, user, provider}, args...)...)
		if err != nil {
			return 0, fmt.Errorf("consent: purge %s: %w", cat, err)
		}
		return tag.RowsAffected(), nil
	}
	exec := func(sql string, args ...any) error {
		n, err := del(sql, args...)
		total += n
		return err
	}
	// Aliases are ids of records, not records: they are not counted.
	dropAliases := func(sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, append([]any{tenant, user, provider}, args...)...); err != nil {
			return fmt.Errorf("consent: purge %s aliases: %w", cat, err)
		}
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
		if t.aliased != "" {
			if err := dropAliases(aliasesOf(t.aliased, ` AND r.`+t.col+` = ANY($4)`), match); err != nil {
				return 0, err
			}
		}
		n, err := del(fmt.Sprintf(`DELETE FROM %s
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND %s = ANY($4)`, t.table, t.col), match)
		if err != nil {
			return 0, err
		}
		if !t.derived {
			total += n
		}
	}

	const ofWorkouts = `workout_id IN (SELECT id FROM ohs_data.workouts
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3)`
	var aliased, stmts []string
	// blobs are the kinds of series blob (series.Kind) cat takes: a blob
	// carries its parent's provider, so it goes by provider or by parent.
	var blobs []series.Kind
	switch cat {
	case Sleep:
		stmts = []string{
			`DELETE FROM ohs_data.sleep_phases WHERE tenant_id = $1 AND user_id = $2 AND session_id IN
				(SELECT id FROM ohs_data.sleep_sessions WHERE tenant_id = $1 AND user_id = $2 AND provider = $3)`,
			`DELETE FROM ohs_data.sleep_sessions WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
		}
	case Activity:
		aliased = []string{allAliases("workouts")}
		blobs = []series.Kind{series.Route}
		stmts = []string{
			`DELETE FROM ohs_data.route_points WHERE tenant_id = $1 AND user_id = $2 AND ` + ofWorkouts,
			`DELETE FROM ohs_data.workout_tracks WHERE tenant_id = $1 AND user_id = $2 AND ` + ofWorkouts,
			`DELETE FROM ohs_data.workouts WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
			`DELETE FROM ohs_data.daily_summaries WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
		}
	case Location:
		blobs = []series.Kind{series.Route}
		stmts = []string{
			`DELETE FROM ohs_data.route_points WHERE tenant_id = $1 AND user_id = $2 AND ` + ofWorkouts,
			`DELETE FROM ohs_data.workout_tracks WHERE tenant_id = $1 AND user_id = $2 AND ` + ofWorkouts,
		}
	case ECG:
		blobs = []series.Kind{series.ECG}
		stmts = []string{
			`DELETE FROM ohs_data.ecg_samples WHERE tenant_id = $1 AND user_id = $2 AND ecg_id IN
				(SELECT id FROM ohs_data.ecg_recordings WHERE tenant_id = $1 AND user_id = $2 AND provider = $3)`,
			`DELETE FROM ohs_data.ecg_recordings WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
		}
	case Heart:
		blobs = []series.Kind{series.Heartbeat}
		stmts = []string{`DELETE FROM ohs_data.heartbeat_samples WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`}
	case Nutrition:
		aliased = []string{allAliases("nutrition_logs")}
		stmts = []string{`DELETE FROM ohs_data.nutrition_logs WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`}
	}
	for _, q := range aliased {
		if err := dropAliases(q); err != nil {
			return 0, err
		}
	}
	// A blob counts its points: they are the records the per-point rows
	// were.
	for _, k := range blobs {
		parents := map[series.Kind]string{series.Route: "workouts", series.ECG: "ecg_recordings"}[k]
		cond := "provider = $3"
		if parents != "" {
			cond = `(provider = $3 OR parent IN (SELECT id::text FROM ohs_data.` + parents + `
				WHERE tenant_id = $1 AND user_id = $2 AND provider = $3))`
		}
		var n int64
		if err := tx.QueryRow(ctx, `WITH d AS (DELETE FROM ohs_data.series_blobs
				WHERE tenant_id = $1 AND user_id = $2 AND kind = $4 AND `+cond+` RETURNING point_count)
			SELECT coalesce(sum(point_count), 0)::bigint FROM d`, tenant, user, provider, int16(k)).Scan(&n); err != nil {
			return 0, fmt.Errorf("consent: purge %s series: %w", cat, err)
		}
		total += n
	}
	for _, q := range stmts {
		if err := exec(q); err != nil {
			return 0, err
		}
	}
	// The figures of cat that records of other categories carry go too; the
	// records stay, so they are not counted as deleted. A clear row loses
	// the column; a sealed row holds the figure inside its sealed value, where
	// an UPDATE of the (NULL) column changes nothing, so it is opened, the
	// figure dropped, and the row sealed again (sealedFigures).
	var tables []string
	cols := map[string][]string{}
	for _, f := range figures {
		if f.category != cat {
			continue
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = NULL
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND %s IS NOT NULL`, f.table, f.column, f.column),
			tenant, user, provider); err != nil {
			return 0, fmt.Errorf("consent: purge %s figure %s.%s: %w", cat, f.table, f.column, err)
		}
		if _, ok := cols[f.table]; !ok {
			tables = append(tables, f.table)
		}
		cols[f.table] = append(cols[f.table], f.column)
	}
	for _, table := range tables {
		if err := sealedFigures(ctx, tx, keys, tenant, user, provider, table, cols[table]); err != nil {
			return 0, fmt.Errorf("consent: purge %s figures of %s: %w", cat, table, err)
		}
	}
	return total, nil
}

// figureBatch is how many sealed rows one pass of sealedFigures reads and
// rewrites.
const figureBatch = 500

// sealedFigures takes the figures cols out of the sealed rows of table (a
// qualified name of ohs_data) that came from provider: each row is opened,
// the figures it holds are set to NULL, and the row is sealed again under
// the same identity, with its content MAC recomputed, so nothing of the
// revoked category is left inside the sealed value. A row that holds none
// of the figures is not rewritten. Rows are taken by id, figureBatch at a
// time.
//
// The purge fails, and is retried, when a sealed row cannot be read: the
// purger was given no vault, or the row does not open. The one exception is
// a user whose keys are gone: an erasure shredded them, nobody can open the
// rows any more, and the deletion removes them.
func sealedFigures(ctx context.Context, tx pgx.Tx, keys *vault.Reader, tenant, user uuid.UUID, provider, table string, cols []string) error {
	t, ok := vault.Schema[strings.TrimPrefix(table, "ohs_data.")]
	if !ok {
		return fmt.Errorf("%s is not a sealed table", table)
	}
	drop := make([]int, 0, len(cols))
	for _, c := range cols {
		i := slices.IndexFunc(t.Sealed, func(s vault.Column) bool { return s.Name == c })
		if i < 0 {
			return fmt.Errorf("%s.%s is not a sealed column", table, c)
		}
		drop = append(drop, i)
	}
	identity := make([]string, len(t.Identity))
	for i, c := range t.Identity {
		identity[i] = `"` + c + `"`
	}
	after := int64(0)
	for {
		rows, err := tx.Query(ctx, `SELECT id, sealed, `+strings.Join(identity, ", ")+` FROM `+table+`
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND sealed IS NOT NULL AND id > $4
			ORDER BY id LIMIT $5`, tenant, user, provider, after, figureBatch)
		if err != nil {
			return err
		}
		type stored struct {
			id     int64
			sealed []byte
			ident  []any
		}
		batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (stored, error) {
			s := stored{ident: make([]any, len(t.Identity))}
			dest := []any{&s.id, &s.sealed}
			for i := range s.ident {
				dest = append(dest, &s.ident[i])
			}
			return s, r.Scan(dest...)
		})
		if err != nil {
			return err
		}
		var ids []int64
		var sealed, macs [][]byte
		for _, s := range batch {
			id, err := vault.RowIdentity(t, func(c string) any { return s.ident[slices.Index(t.Identity, c)] })
			if err != nil {
				return err
			}
			vals, err := keys.Open(ctx, t, id, s.sealed)
			if errors.Is(err, vault.ErrNoKeys) {
				return nil
			}
			if err != nil {
				return err
			}
			changed := false
			for _, i := range drop {
				if vals[i] != nil {
					vals[i], changed = nil, true
				}
			}
			if !changed {
				continue
			}
			uv, err := keys.Keys(ctx)
			if err != nil {
				return err
			}
			again, err := uv.SealRow(t, id, vals)
			if err != nil {
				return err
			}
			ids, sealed = append(ids, s.id), append(sealed, again)
			if t.MAC {
				macs = append(macs, uv.ContentMAC(t, vals))
			}
		}
		if len(ids) > 0 {
			set, args := "sealed = u.sealed", []any{tenant, user, ids, sealed}
			from := "unnest($3::bigint[], $4::bytea[]) AS u(id, sealed)"
			if t.MAC {
				set, args = set+", content_mac = u.mac", append(args, macs)
				from = "unnest($3::bigint[], $4::bytea[], $5::bytea[]) AS u(id, sealed, mac)"
			}
			if _, err := tx.Exec(ctx, `UPDATE `+table+` r SET `+set+` FROM `+from+`
				WHERE r.tenant_id = $1 AND r.user_id = $2 AND r.id = u.id`, args...); err != nil {
				return err
			}
		}
		if len(batch) < figureBatch {
			return nil
		}
		after = batch[len(batch)-1].id
	}
}
