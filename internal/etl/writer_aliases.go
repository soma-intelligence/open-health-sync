// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Aliased deletions (model.MapResult.AliasExternalIDs).
//
// Content de-duplication folds two source objects of one reading into one
// stored row: two apps writing the same weight, or an app that saves a new
// copy of a sample before it deletes the old one. The row keeps the first
// copy's external_id, so deleting by external_id would remove a row another
// live object still stands for. For such a provider the writer keeps, in
// ohs_data.record_aliases, one alias per (external id, stored row): every
// incoming record with an ExternalID gets one, stored or de-duplicated into a
// row already there (a row stored before aliases existed included). A
// deletion then, in the job's transaction:
//
//  1. inserts the tombstones of its ids (as an unaliased deletion does);
//  2. deletes the aliases of those ids, in one statement;
//  3. deletes, per table, the rows those aliases named that no alias holds
//     any more, matched on content hash and start, bounded by the earliest
//     and latest start so the hypertable scans only those chunks;
//  4. deletes the route points and track summaries of removed workouts, and
//     returns the local days of removed additive samples for the daily
//     recompute.
//
// Every aliased job of a user takes the user's alias lock first, so a
// de-duplicated record's new alias and a deletion of the row's other alias
// cannot pass each other.

// tAliases is the alias table.
const tAliases = "record_aliases"

// aliasTables are the tables a record alias may name, with the RETURNING
// clause of their aliased delete. The table name of an alias row is checked
// against this list before it reaches SQL text.
var aliasTables = map[string]string{
	tQuantity:  "quantity_type, start_date, metadata->>'" + MetadataTimezone + "', NULL::bigint",
	tCategory:  "NULL::text, start_date, NULL::text, NULL::bigint",
	tNutrition: "NULL::text, start_date, NULL::text, NULL::bigint",
	tWorkouts:  "NULL::text, start_date, NULL::text, id",
}

var (
	aliasInsert = `INSERT INTO ohs_data.record_aliases
		(tenant_id, user_id, provider, external_id, record_table, content_hash, start_time)
		SELECT $1, $2, a.provider, a.external_id, a.record_table, a.content_hash, a.start_time
		FROM unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::timestamptz[])
		     AS a(provider, external_id, record_table, content_hash, start_time)
		ON CONFLICT DO NOTHING`
	aliasTombstones = `INSERT INTO ohs_data.deleted_records (tenant_id, user_id, provider, external_id)
		SELECT $1, $2, d.provider, d.external_id FROM unnest($3::text[], $4::text[]) AS d(provider, external_id)
		ON CONFLICT DO NOTHING`
	aliasDelete = `DELETE FROM ohs_data.record_aliases a
		USING unnest($3::text[], $4::text[]) AS d(provider, external_id)
		WHERE a.tenant_id = $1 AND a.user_id = $2 AND a.provider = d.provider AND a.external_id = d.external_id
		RETURNING a.provider, a.record_table, a.content_hash, a.start_time`
)

// aliasRowDelete is the delete of the rows of table that no alias holds any
// more. $3..$5 are the provider, content hash and start of each candidate,
// $6 and $7 the earliest and latest start.
func aliasRowDelete(table string) string {
	return `DELETE FROM ` + qualified(table) + ` r
		USING unnest($3::text[], $4::text[], $5::timestamptz[]) AS g(provider, content_hash, start_time)
		WHERE r.tenant_id = $1 AND r.user_id = $2
		  AND r.start_date >= $6 AND r.start_date <= $7
		  AND r.provider = g.provider AND r.content_hash = g.content_hash AND r.start_date = g.start_time
		  AND NOT EXISTS (SELECT 1 FROM ohs_data.record_aliases a
		      WHERE a.tenant_id = $1 AND a.user_id = $2 AND a.record_table = '` + table + `'
		        AND a.content_hash = g.content_hash AND a.start_time = g.start_time)
		RETURNING ` + aliasTables[table]
}

// lockAliases takes the user's alias lock for the transaction.
func lockAliases(ctx context.Context, tx TxLike, sc scope) error {
	return lockUserTable(ctx, tx, tAliases, sc)
}

// aliasRow is one alias, as written or as a deletion returns it.
type aliasRow struct {
	provider, externalID, table, hash string
	start                             time.Time
}

// recordAliases lists the aliases of acc's records: every quantity sample,
// category sample, nutrition log and workout with an ExternalID.
func recordAliases(sc scope, acc *model.MapResult) []aliasRow {
	var out []aliasRow
	add := func(provider, id, table, hash string, start time.Time) {
		if id != "" && hash != "" {
			out = append(out, aliasRow{sc.providerOr(provider), id, table, hash, start})
		}
	}
	for _, s := range acc.QuantitySamples {
		add(s.Provider, s.ExternalID, tQuantity, s.ContentHash, s.StartDate)
	}
	for _, s := range acc.CategorySamples {
		add(s.Provider, s.ExternalID, tCategory, s.ContentHash, s.StartDate)
	}
	for _, n := range acc.NutritionLogs {
		add(n.Provider, n.ExternalID, tNutrition, n.ContentHash, n.StartDate)
	}
	for _, w := range acc.Workouts {
		add(w.Provider, w.ExternalID, tWorkouts, w.ContentHash, w.StartDate)
	}
	return out
}

// writeAliases records the aliases of acc's records, the ones de-duplicated
// into a stored row included.
func writeAliases(ctx context.Context, tx TxLike, sc scope, acc *model.MapResult) error {
	rows := recordAliases(sc, acc)
	if len(rows) == 0 {
		return nil
	}
	providers, ids, tables, hashes := make([]string, len(rows)), make([]string, len(rows)), make([]string, len(rows)), make([]string, len(rows))
	starts := make([]time.Time, len(rows))
	for i, r := range rows {
		providers[i], ids[i], tables[i], hashes[i], starts[i] = r.provider, r.externalID, r.table, r.hash, r.start
	}
	if _, err := tx.Exec(ctx, aliasInsert, sc.tenant, sc.user, providers, ids, tables, hashes, starts); err != nil {
		return fmt.Errorf("write record aliases: %w", err)
	}
	return nil
}

// applyAliasDeletions applies the deletions of an aliased batch (see the top
// of this file) and returns the local days of the additive samples removed.
func applyAliasDeletions(ctx context.Context, tx TxLike, sc scope, dels []model.Deletion, c Counts) (map[dayKey]*time.Location, error) {
	seen := map[recordKey]bool{}
	var providers, ids []string
	for _, d := range dels {
		if d.ExternalID == "" {
			continue
		}
		k := recordKey{provider: sc.providerOr(d.Provider), id: d.ExternalID}
		if seen[k] {
			c.add(tDeleted, 0, 1)
			continue
		}
		seen[k] = true
		providers, ids = append(providers, k.provider), append(ids, k.id)
	}
	days := map[dayKey]*time.Location{}
	if len(ids) == 0 {
		return days, nil
	}
	tag, err := tx.Exec(ctx, aliasTombstones, sc.tenant, sc.user, providers, ids)
	if err != nil {
		return nil, fmt.Errorf("insert tombstones: %w", err)
	}
	fresh := int(tag.RowsAffected())
	c.add(tDeleted, fresh, len(ids)-fresh)

	rows, err := tx.Query(ctx, aliasDelete, sc.tenant, sc.user, providers, ids)
	if err != nil {
		return nil, fmt.Errorf("delete record aliases: %w", err)
	}
	gone, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (aliasRow, error) {
		var a aliasRow
		err := r.Scan(&a.provider, &a.table, &a.hash, &a.start)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("delete record aliases: %w", err)
	}
	byTable := map[string][]aliasRow{}
	for _, a := range gone {
		if _, ok := aliasTables[a.table]; !ok {
			slog.WarnContext(ctx, "etl: record alias names an unknown table, ignored", "table", a.table)
			continue
		}
		byTable[a.table] = append(byTable[a.table], a)
	}

	removed := 0
	for _, table := range []string{tQuantity, tCategory, tNutrition, tWorkouts} {
		cands := byTable[table]
		if len(cands) == 0 {
			continue
		}
		ps, hs := make([]string, len(cands)), make([]string, len(cands))
		ss := make([]time.Time, len(cands))
		lo, hi := cands[0].start, cands[0].start
		for i, a := range cands {
			ps[i], hs[i], ss[i] = a.provider, a.hash, a.start
			if a.start.Before(lo) {
				lo = a.start
			}
			if a.start.After(hi) {
				hi = a.start
			}
		}
		rows, err := tx.Query(ctx, aliasRowDelete(table), sc.tenant, sc.user, ps, hs, ss, lo, hi)
		if err != nil {
			return nil, fmt.Errorf("delete %s: %w", table, err)
		}
		type removedRow struct {
			QuantityType *string
			StartDate    time.Time
			Timezone     *string
			WorkoutID    *int64
		}
		out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[removedRow])
		if err != nil {
			return nil, fmt.Errorf("delete %s: %w", table, err)
		}
		removed += len(out)
		// An aliased batch comes from one raw object: its candidates share
		// one provider.
		provider := cands[0].provider
		var workouts []int64
		for _, r := range out {
			if r.WorkoutID != nil {
				workouts = append(workouts, *r.WorkoutID)
			}
			if r.QuantityType != nil && isAdditive(*r.QuantityType) {
				md := map[string]any{}
				if r.Timezone != nil {
					md[MetadataTimezone] = *r.Timezone
				}
				loc := sampleLocation(md)
				dk := dayKey{provider: provider, day: dateOnly(r.StartDate.In(loc))}
				if _, ok := days[dk]; !ok {
					days[dk] = loc
				}
			}
		}
		if len(workouts) > 0 {
			for _, q := range []string{deletionRoutes, deletionTracks} {
				tag, err := tx.Exec(ctx, q, sc.tenant, sc.user, workouts)
				if err != nil {
					return nil, fmt.Errorf("delete workout children: %w", err)
				}
				removed += int(tag.RowsAffected())
			}
		}
	}
	if removed > 0 || len(gone) > 0 {
		slog.InfoContext(ctx, "etl: aliased deletions applied", "deletions", len(ids),
			"aliases_removed", len(gone), "rows_removed", removed)
	}
	return days, nil
}
