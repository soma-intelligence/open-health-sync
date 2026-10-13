// SPDX-License-Identifier: AGPL-3.0-only

package deletion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// Erasing one provider's data of one user.
//
// The tables are read from pg_catalog, like a user deletion's: every table of
// ohs_sync and ohs_data with a user_id column is either
//
//   - a provider table, with a provider column: its rows of the user and
//     provider are deleted (normalized records, daily summaries, provider
//     tombstones, failed records, raw object rows, ETL and pull jobs, pull
//     coverage, the sync log, push subscriptions, OAuth states, and the
//     connection once it is disconnected);
//   - a child table, listed in childTables: its rows hang off a parent row of
//     a provider table (sleep phases, route points, track summaries, ECG
//     voltages) and are deleted with the parent's rows;
//   - a kept table, listed in keptTables: its rows are the user's own, not
//     any provider's (time tags, the order of sources), and stay;
//   - anything else is an error, so a new table cannot be left out silently.
//
// Daily summaries are per provider: a derived row sums one provider's own
// samples (internal/etl/daily.go), so removing that provider's rows leaves the
// other providers' summaries exact and nothing needs recomputing.

// childOf names the parent of a child table's rows: fk holds the parent's id.
type childOf struct{ fk, parent string }

// childTables are the user-scoped tables without a provider column.
var childTables = map[string]childOf{
	"ohs_data.ecg_samples":    {fk: "ecg_id", parent: "ohs_data.ecg_recordings"},
	"ohs_data.route_points":   {fk: "workout_id", parent: "ohs_data.workouts"},
	"ohs_data.sleep_phases":   {fk: "session_id", parent: "ohs_data.sleep_sessions"},
	"ohs_data.workout_tracks": {fk: "workout_id", parent: "ohs_data.workouts"},
}

// keptTables are the user-scoped tables whose rows no provider wrote. A
// provider erasure leaves them; a user deletion still erases them.
var keptTables = map[string]bool{
	"ohs_data.time_tags":         true,
	"ohs_sync.source_priorities": true,
}

// providerFilter narrows the delete of some provider tables. A connection is
// removed only once disconnected: one that is live again is never deleted
// from under its user (a reconnect is refused while the job is pending).
var providerFilter = map[string]string{
	"ohs_sync.connections": ` AND status = 'disconnected'`,
}

// providerPlan is what one provider erasure deletes, children first.
type providerPlan struct {
	children []childDelete
	tables   []string // schema.table
}

type childDelete struct {
	table string
	childOf
}

// planProvider classifies the user-scoped tables. It fails on a table that is
// neither a provider table nor a known child, and on a child whose parent has
// no provider column.
func planProvider(ctx context.Context, tx pgx.Tx) (providerPlan, error) {
	rows, err := tx.Query(ctx, `
		SELECT n.nspname || '.' || c.relname, bool_or(a.attname = 'provider')
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IN ('ohs_sync', 'ohs_data')
		  AND a.attnum > 0 AND NOT a.attisdropped
		  AND c.relkind IN ('r', 'p')
		GROUP BY 1
		HAVING bool_or(a.attname = 'user_id')
		ORDER BY 1`)
	if err != nil {
		return providerPlan{}, fmt.Errorf("list user tables: %w", err)
	}
	type table struct {
		name        string
		hasProvider bool
	}
	tables, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (table, error) {
		var t table
		err := r.Scan(&t.name, &t.hasProvider)
		return t, err
	})
	if err != nil {
		return providerPlan{}, fmt.Errorf("list user tables: %w", err)
	}
	var p providerPlan
	withProvider := map[string]bool{}
	var unclassified []string
	for _, t := range tables {
		switch c, child := childTables[t.name]; {
		case keptTables[t.name] && !t.hasProvider:
		case t.hasProvider:
			p.tables = append(p.tables, t.name)
			withProvider[t.name] = true
		case child:
			p.children = append(p.children, childDelete{table: t.name, childOf: c})
		default:
			unclassified = append(unclassified, t.name)
		}
	}
	if len(unclassified) > 0 {
		return providerPlan{}, fmt.Errorf("tables %s hold user rows without a provider column and are not in deletion.childTables or deletion.keptTables",
			strings.Join(unclassified, ", "))
	}
	for _, c := range p.children {
		if !withProvider[c.parent] {
			return providerPlan{}, fmt.Errorf("parent %s of %s has no provider column", c.parent, c.table)
		}
	}
	if len(p.tables) == 0 {
		return providerPlan{}, errors.New("no provider-scoped tables found in ohs_sync or ohs_data")
	}
	return p, nil
}

// ident quotes a schema.table name.
func ident(name string) string {
	schema, table, _ := strings.Cut(name, ".")
	return pgx.Identifier{schema, table}.Sanitize()
}

// pullRunning reports whether a pull of the provider is running for the user
// (claimed within the visibility timeout). The job waits for it: a pull that
// started before the disconnect could otherwise store data after the erasure.
func (w *Worker) pullRunning(ctx context.Context, j job, provider string) (bool, error) {
	var running bool
	err := w.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ohs_sync.pull_jobs
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status = 'running'
		  AND claimed_at > now() - make_interval(secs => $4::double precision))`,
		j.tenant, j.user, provider, queue.VisibilityTimeout.Seconds()).Scan(&running)
	if err != nil {
		return false, fmt.Errorf("look up running pulls: %w", err)
	}
	return running, nil
}

// runProvider erases what the user holds from provider: the raw files first
// (outside any transaction, idempotent), then every row in one transaction
// that also marks the job done. While the job is unfinished the collect side
// stores nothing new of that provider for the user, so no file lands behind
// the sweep.
func (w *Worker) runProvider(ctx context.Context, j job, provider string) (bool, error) {
	log := slog.With("job_id", j.id, "tenant_id", j.tenant, "user_id", j.user, "provider", provider)
	if !ValidProvider(provider) {
		return false, fmt.Errorf("job names an invalid provider slug")
	}
	busy, err := w.pullRunning(ctx, j, provider)
	if err != nil {
		return false, err
	}
	if busy {
		log.DebugContext(ctx, "deletion: a pull of the provider is running, the erasure waits for it")
		return false, nil
	}
	if err := w.raw.DeletePrefix(ctx, fmt.Sprintf("raw/%s/%s/%s/", j.tenant, j.user, provider)); err != nil {
		return false, fmt.Errorf("delete raw files: %w", err)
	}

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var id string
	err = tx.QueryRow(ctx, `
		SELECT id FROM ohs_core.deletion_jobs
		WHERE id = $1 AND status IN ('pending', 'running')
		FOR UPDATE SKIP LOCKED`, j.id).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // done or being finished by another worker
	}
	if err != nil {
		return false, fmt.Errorf("claim: %w", err)
	}
	// Lock the user row like a user deletion: an ETL write holding it FOR
	// SHARE finishes first (its rows are deleted below), and one arriving
	// later finds its raw object gone and writes nothing.
	var status string
	err = tx.QueryRow(ctx,
		`SELECT status FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, j.tenant, j.user).Scan(&status)
	userGone := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !userGone {
		return false, fmt.Errorf("lock user: %w", err)
	}
	rows := int64(0)
	if !userGone {
		// The user's rows of compressed chunks are not whole batches here (the
		// provider is not a segment column): lift the per-transaction
		// decompression limit so the erasure never stops half way.
		if _, err := tx.Exec(ctx, `SET LOCAL timescaledb.max_tuples_decompressed_per_dml_transaction = 0`); err != nil {
			return false, fmt.Errorf("allow decompression: %w", err)
		}
		plan, err := planProvider(ctx, tx)
		if err != nil {
			return false, err
		}
		for _, c := range plan.children {
			tag, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s
				WHERE tenant_id = $1 AND user_id = $2 AND %s IN (
					SELECT id FROM %s WHERE tenant_id = $1 AND user_id = $2 AND provider = $3)`,
				ident(c.table), pgx.Identifier{c.fk}.Sanitize(), ident(c.parent)), j.tenant, j.user, provider)
			if err != nil {
				return false, fmt.Errorf("delete from %s: %w", c.table, err)
			}
			rows += tag.RowsAffected()
		}
		for _, t := range plan.tables {
			tag, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE tenant_id = $1 AND user_id = $2 AND provider = $3%s`,
				ident(t), providerFilter[t]), j.tenant, j.user, provider)
			if err != nil {
				return false, fmt.Errorf("delete from %s: %w", t, err)
			}
			rows += tag.RowsAffected()
		}
		user := j.user
		if _, err := webhook.Enqueue(ctx, tx, webhook.Event{
			Tenant: j.tenant, User: &user, Type: webhook.DataErased,
			Data: map[string]any{"provider": provider, "job_id": j.id.String()},
		}); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE ohs_core.deletion_jobs SET status = 'done', finished_at = now() WHERE id = $1`, j.id); err != nil {
		return false, fmt.Errorf("finish job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	log.InfoContext(ctx, "deletion: provider data deleted", "rows", rows, "user_gone", userGone)
	return true, nil
}
