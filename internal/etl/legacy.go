// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Legacy clear rows.
//
// Rows written before the vault (or by a process without one) are clear, with
// the plain content_hash. A job with keys must not store a second copy of a
// record such a row already holds, nor sum it twice into a day: before a
// stored row is looked up or a dedup conflict decides, the clear rows the
// batch could meet are converted in place, inside the job's transaction. The
// content_hash becomes the keyed hash of the stored one (scope.hash), the
// sensitive columns are sealed and set to NULL, the promoted columns filled,
// the record aliases rekeyed; a parent takes its children along, their hashes
// computed again over its keyed hash. Every lookup and conflict after that
// compares keyed hashes only. The seal command converts the rest the same way.

// shapeFor is the write shape of a sealed table the ETL writes.
func shapeFor(table string) *shape {
	switch table {
	case tQuantity:
		return quantityShape
	case tCategory:
		return categoryShape
	case tNutrition:
		return nutritionShape
	case tHeartbeat:
		return heartbeatShape
	case tObservation:
		return observationShape
	case tDaily:
		return dailyShape
	case tSleep:
		return sleepShape
	case tPhases:
		return sleepPhaseShape
	case tWorkouts:
		return workoutShape
	case tRoutes:
		return routePointShape
	case tECG:
		return ecgShape
	case tECGSamples:
		return ecgSampleShape
	}
	return nil
}

// aliasedTables are the tables a record alias may name (aliasTables).
func aliased(table string) bool {
	_, ok := aliasTables[table]
	return ok
}

// convertLegacy converts the clear rows of table that where selects (an SQL
// condition on alias r; its placeholders start at $3, args fill them). It
// does nothing without keys.
func convertLegacy(ctx context.Context, tx TxLike, sc scope, table, where string, args ...any) error {
	if sc.uv == nil {
		return nil
	}
	rows, err := collectRows(openRows(ctx, tx, nil, table,
		`SELECT * FROM `+qualified(table)+` r WHERE r.tenant_id = $1 AND r.user_id = $2 AND r.sealed IS NULL AND (`+where+`)`,
		append([]any{sc.tenant, sc.user}, args...)))
	if err != nil {
		return fmt.Errorf("convert clear %s rows: %w", table, err)
	}
	for _, r := range rows {
		if err := convertRow(ctx, tx, sc, table, r, sc.hash(r.text("content_hash"))); err != nil {
			return err
		}
	}
	return nil
}

// convertRow seals the clear row r of table in place under hash, its new
// content_hash, with the values r holds (opened, possibly changed by the
// caller), and converts its children and aliases.
func convertRow(ctx context.Context, tx TxLike, sc scope, table string, r Row, hash string) error {
	s := shapeFor(table)
	t := s.t
	old := r.text("content_hash")
	vals, err := sealedValues(t, r)
	if err != nil {
		return err
	}
	id, err := rowIdentity(t, func(c string) any {
		if c == "content_hash" {
			return hash
		}
		return r[c]
	})
	if err != nil {
		return err
	}
	sealed, err := sc.uv.SealRow(t, id, vals)
	if err != nil {
		return fmt.Errorf("seal %s row: %w", table, err)
	}
	args := []any{sc.tenant, sc.user, hash, sealed}
	set := []string{"content_hash = $3", "sealed = $4"}
	if t.MAC {
		args = append(args, sc.uv.ContentMAC(t, vals))
		set = append(set, fmt.Sprintf("content_mac = $%d", len(args)))
	}
	if s.derive != nil {
		for i, v := range s.derive(sc, func(c string) any { return r[c] }) {
			args = append(args, v)
			set = append(set, fmt.Sprintf("%s = $%d", s.promoted[i], len(args)))
		}
	}
	for _, c := range t.Sealed {
		set = append(set, c.Name+" = NULL")
	}
	// The row is found by its id, or by its old identity on a table without
	// one; still clear, so a job that converted it first wins.
	var locate []string
	if rid, ok := r["id"]; ok {
		args = append(args, rid)
		locate = append(locate, fmt.Sprintf("id = $%d", len(args)))
	} else {
		for _, c := range t.Identity {
			args = append(args, r[c])
			locate = append(locate, fmt.Sprintf("%s = $%d", quoteCols([]string{c}), len(args)))
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE `+qualified(table)+` SET `+strings.Join(set, ", ")+`
		WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NULL AND `+strings.Join(locate, " AND "), args...); err != nil {
		return fmt.Errorf("convert %s row: %w", table, err)
	}
	if aliased(table) && old != "" {
		start, _ := r["start_date"].(time.Time)
		if err := rekeyAliases(ctx, tx, sc, table, old, hash, start); err != nil {
			return err
		}
	}
	return convertChildren(ctx, tx, sc, table, r["id"], hash)
}

// rekeyAliases points the aliases of a converted row at its keyed hash. An
// alias the job already wrote under the keyed hash makes the old one a twin,
// which goes.
func rekeyAliases(ctx context.Context, tx TxLike, sc scope, table, old, hash string, start time.Time) error {
	if _, err := tx.Exec(ctx, `DELETE FROM ohs_data.record_aliases a
		WHERE a.tenant_id = $1 AND a.user_id = $2 AND a.record_table = $3 AND a.content_hash = $4 AND a.start_time = $6
		  AND EXISTS (SELECT 1 FROM ohs_data.record_aliases k WHERE k.tenant_id = $1 AND k.user_id = $2
		      AND k.provider = a.provider AND k.external_id = a.external_id AND k.record_table = $3 AND k.content_hash = $5)`,
		sc.tenant, sc.user, table, old, hash, start); err != nil {
		return fmt.Errorf("rekey record aliases: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ohs_data.record_aliases SET content_hash = $5
		WHERE tenant_id = $1 AND user_id = $2 AND record_table = $3 AND content_hash = $4 AND start_time = $6`,
		sc.tenant, sc.user, table, old, hash, start); err != nil {
		return fmt.Errorf("rekey record aliases: %w", err)
	}
	return nil
}

// convertChildren converts the clear children of a converted parent: their
// hashes are computed over the parent's keyed hash, as the writer computes
// them for a child it writes (writeSleepPhases, writeRoutePoints,
// writeECGVoltageSamples).
func convertChildren(ctx context.Context, tx TxLike, sc scope, table string, parentID any, parentHash string) error {
	var child, link string
	var rehash func(Row) string
	switch table {
	case tSleep:
		child, link = tPhases, "session_id"
		rehash = func(r Row) string {
			d, _ := r.int("duration_s")
			p := model.SleepPhase{Phase: r.text("phase"), DurationS: int(d), ParentContentHash: parentHash}
			p.StartDate, _ = r["start_date"].(time.Time)
			p.EndDate, _ = r["end_date"].(time.Time)
			return sc.hash(model.SleepPhaseContentHash(p))
		}
	case tWorkouts:
		child, link = tRoutes, "workout_id"
		rehash = func(r Row) string {
			p := model.RoutePoint{ParentContentHash: parentHash}
			p.Timestamp, _ = r["timestamp"].(time.Time)
			p.Latitude, _ = r.float("latitude")
			p.Longitude, _ = r.float("longitude")
			if alt, ok := r.float("altitude_m"); ok {
				p.AltitudeM = &alt
			}
			return sc.hash(model.RoutePointContentHash(p))
		}
	case tECG:
		child, link = tECGSamples, "ecg_id"
		rehash = func(r Row) string {
			s := model.ECGVoltageSample{ParentContentHash: parentHash}
			s.TimeSinceSampleStart, _ = r.float("time_since_sample_start")
			s.VoltageUV, _ = r.float("voltage_uv")
			return sc.hash(model.ECGVoltageSampleContentHash(s))
		}
	default:
		return nil
	}
	rows, err := collectRows(openRows(ctx, tx, nil, child,
		`SELECT * FROM `+qualified(child)+` WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NULL AND `+link+` = $3`,
		[]any{sc.tenant, sc.user, parentID}))
	if err != nil {
		return fmt.Errorf("convert clear %s rows: %w", child, err)
	}
	for _, r := range rows {
		if err := convertRow(ctx, tx, sc, child, r, rehash(r)); err != nil {
			return err
		}
	}
	return nil
}

// legacyKeys are the (plain hash, time) pairs of one table in a batch.
type legacyKeys struct {
	hashes []string
	times  []time.Time
	wd     window
}

func (k *legacyKeys) add(hash string, at time.Time) {
	if hash == "" {
		return
	}
	k.hashes, k.times = append(k.hashes, hash), append(k.times, at)
	k.wd.extend(at)
}

// convertBatchLegacy converts the clear rows the batch acc (plain hashes, not
// keyed yet) can meet: the stored copies of its records, by plain hash and
// time, and the stored parents its children name. Without keys it does
// nothing.
func convertBatchLegacy(ctx context.Context, tx TxLike, sc scope, acc *model.MapResult) error {
	if sc.uv == nil {
		return nil
	}
	type keyed struct {
		table, timeCol, timeType string
		keys                     legacyKeys
	}
	tables := []*keyed{
		{table: tQuantity, timeCol: "start_date", timeType: "timestamptz"},
		{table: tCategory, timeCol: "start_date", timeType: "timestamptz"},
		{table: tNutrition, timeCol: "start_date", timeType: "timestamptz"},
		{table: tSleep, timeCol: "bedtime_start", timeType: "timestamptz"},
		{table: tWorkouts, timeCol: "start_date", timeType: "timestamptz"},
		{table: tECG, timeCol: "start_date", timeType: "timestamptz"},
		{table: tDaily, timeCol: "summary_date", timeType: "date"},
		{table: tHeartbeat, timeCol: "timestamp", timeType: "timestamptz"},
		{table: tObservation, timeCol: "ts", timeType: "timestamptz"},
	}
	for _, r := range acc.QuantitySamples {
		tables[0].keys.add(r.ContentHash, r.StartDate)
	}
	for _, r := range acc.CategorySamples {
		tables[1].keys.add(r.ContentHash, r.StartDate)
	}
	for _, r := range acc.NutritionLogs {
		tables[2].keys.add(r.ContentHash, r.StartDate)
	}
	for _, r := range acc.SleepSessions {
		tables[3].keys.add(r.ContentHash, r.BedtimeStart)
	}
	for _, r := range acc.Workouts {
		tables[4].keys.add(r.ContentHash, r.StartDate)
	}
	for _, r := range acc.ECGRecordings {
		tables[5].keys.add(r.ContentHash, r.StartDate)
	}
	for _, r := range acc.DailySummaries {
		tables[6].keys.add(r.ContentHash, dateOnly(r.SummaryDate))
	}
	for _, r := range acc.HeartbeatSamples {
		tables[7].keys.add(model.HeartbeatSampleContentHash(r), r.Timestamp)
	}
	for _, r := range acc.Observations {
		tables[8].keys.add(observationHash(scope{}, r), r.TS)
	}
	for _, k := range tables {
		if len(k.keys.hashes) == 0 {
			continue
		}
		col := quoteCols([]string{k.timeCol})
		if err := convertLegacy(ctx, tx, sc, k.table, fmt.Sprintf(`r.%[1]s BETWEEN $5 AND $6
			AND (r.content_hash, r.%[1]s) IN (SELECT * FROM unnest($3::text[], $4::%[2]s[]))`, col, k.timeType),
			k.keys.hashes, k.keys.times, k.keys.wd.lo, k.keys.wd.hi); err != nil {
			return err
		}
	}
	// The stored parents the children name: a child that arrives without its
	// parent is attached to the stored one by its keyed hash.
	parents := map[string][]string{}
	for _, p := range acc.SleepPhases {
		if p.ParentContentHash != "" {
			parents[tSleep] = append(parents[tSleep], p.ParentContentHash)
		}
	}
	for _, p := range acc.RoutePoints {
		if p.ParentContentHash != "" {
			parents[tWorkouts] = append(parents[tWorkouts], p.ParentContentHash)
		}
	}
	for _, s := range acc.ECGSamples {
		if s.ParentContentHash != "" {
			parents[tECG] = append(parents[tECG], s.ParentContentHash)
		}
	}
	for _, table := range []string{tSleep, tWorkouts, tECG} {
		if hs := parents[table]; len(hs) > 0 {
			if err := convertLegacy(ctx, tx, sc, table, `r.content_hash = ANY($3)`, uniq(hs)); err != nil {
				return err
			}
		}
	}
	return nil
}

func uniq(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
