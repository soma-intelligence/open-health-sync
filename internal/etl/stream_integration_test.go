// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/vault/series"
)

// mixMap is a provider whose object holds every kind of line: the records of
// recordsMap (a "kind"), a heartbeat ("rr"), an observation ("code"), and the
// quantity samples of quantityMap, which may name a provider record ("ext")
// whose start can move ("moved"). With "aliased" the result is an aliased
// one.
func mixMap(r connector.RawRecord) (model.MapResult, error) {
	str := func(k string) string { s, _ := r.Fields[k].(string); return s }
	var (
		mr  model.MapResult
		err error
	)
	switch {
	case str("kind") != "":
		mr, err = recordsMap(r)
	case r.Fields["rr"] != nil:
		at, perr := time.Parse(time.RFC3339, str("start"))
		if perr != nil {
			return model.MapResult{}, perr
		}
		mr = model.MapResult{HeartbeatSamples: []model.HeartbeatSample{{Timestamp: at, RRMS: r.Fields["rr"].(float64)}}}
	case str("code") != "":
		at, perr := time.Parse(time.RFC3339, str("start"))
		if perr != nil {
			return model.MapResult{}, perr
		}
		v := r.Fields["value"].(float64)
		mr = model.MapResult{Observations: []model.Observation{{Provider: r.Provider, TS: at, CanonicalCode: str("code"), ValueNum: &v, UnitUCUM: "1"}}}
	default:
		if mr, err = quantityMap(r); err == nil && len(mr.QuantitySamples) == 1 && str("ext") != "" {
			s := &mr.QuantitySamples[0]
			s.ExternalID = str("ext")
			if moved, _ := r.Fields["moved"].(bool); moved {
				// A figure of a provider record: hashed on the record, not
				// on its start, which may move.
				s.Replaceable, s.SupersedeMoved = true, true
				s.ContentHash = model.ComputeContentHash(map[string]string{"user_id": r.UserID, "ext": s.ExternalID, "type": s.QuantityType})
			}
		}
	}
	if aliased, _ := r.Fields["aliased"].(bool); aliased || r.DataType == "aliased" {
		mr.AliasExternalIDs = true
	}
	return mr, err
}

// userDump is everything a job leaves for one user, as sorted lines: the
// rows of every table without what differs between two users (ids, keyed
// hashes, capture and ingestion times).
func (e *env) userDump(tenant, user uuid.UUID) map[string][]string {
	e.t.Helper()
	ctx := context.Background()
	out := map[string][]string{}
	sealed := map[string]string{
		tQuantity:        `quantity_type, start_date, end_date, value, unit, external_id, provider, source_name, tz, summary_date, derived, synthesized, worn, metadata::text`,
		tCategory:        `category_type, start_date, end_date, value_int, external_id, tz`,
		tObservation:     `canonical_code, ts, value_num, unit_ucum`,
		tSleep:           `external_id, bedtime_start, bedtime_end, total_sleep_seconds, resting_hr`,
		tPhases:          `phase, start_date, end_date, duration_s`,
		tWorkouts:        `external_id, activity_type, start_date, end_date`,
		tNutrition:       `external_id, start_date, energy_kcal`,
		tDaily:           `provider, summary_date, derived, step_count, active_kcal, resting_hr, hrv_avg_ms, recovery_score, metadata::text`,
		"failed_records": `line_number, reason_code, error_message, raw_payload::text`,
	}
	for table, cols := range sealed {
		rows, err := e.read().Query(ctx, `SELECT `+cols+` FROM ohs_data.`+table+` WHERE tenant_id = $1 AND user_id = $2`, tenant, user)
		require.NoError(e.t, err, table)
		for rows.Next() {
			vals, err := rows.Values()
			require.NoError(e.t, err, table)
			out[table] = append(out[table], fmt.Sprint(vals...))
		}
		require.NoError(e.t, rows.Err(), table)
	}
	// The series, decoded: a route or an ECG by its provider (the parent
	// ids differ between users), a heartbeat series by its id too.
	for _, k := range []series.Kind{series.ECG, series.Route, series.Heartbeat} {
		for parent, pts := range e.blobPointsOf(tenant, user, k) {
			if k != series.Heartbeat {
				parent = ""
			}
			for _, p := range pts {
				// Each upload has its own capture time, as captured_at
				// differs on the rows of the other tables.
				p.Capture = 0
				out[tSeries] = append(out[tSeries], fmt.Sprint(k, parent, p))
			}
		}
	}
	slices.Sort(out[tSeries])
	plain := map[string]string{
		"deleted_records": `SELECT provider, external_id FROM ohs_data.deleted_records WHERE tenant_id = $1 AND user_id = $2`,
		"record_aliases":  `SELECT provider, external_id, record_table, start_time FROM ohs_data.record_aliases WHERE tenant_id = $1 AND user_id = $2`,
		"sync_log": `SELECT metric_type, status, records_written, duplicates_skipped, records_failed FROM ohs_sync.sync_log
			WHERE tenant_id = $1 AND user_id = $2`,
		"pull_coverage": `SELECT metric, date, records_count FROM ohs_sync.pull_coverage WHERE tenant_id = $1 AND user_id = $2`,
	}
	for name, sql := range plain {
		rows, err := e.pool.Query(ctx, sql, tenant, user)
		require.NoError(e.t, err, name)
		for rows.Next() {
			vals, err := rows.Values()
			require.NoError(e.t, err, name)
			out[name] = append(out[name], fmt.Sprint(vals...))
		}
		require.NoError(e.t, rows.Err(), name)
	}
	// The hourly buckets, opened, without the keyed source.
	if uv, err := e.v.ForUser(ctx, tenant, user); err == nil {
		tx, err := e.pool.Begin(ctx)
		require.NoError(e.t, err)
		buckets, err := collectRows(openRows(ctx, tx, uv, "quantity_hourly_sealed",
			`SELECT * FROM ohs_data.quantity_hourly_sealed WHERE tenant_id = $1 AND user_id = $2`, []any{tenant, user}))
		_ = tx.Rollback(ctx)
		require.NoError(e.t, err)
		for _, r := range buckets {
			out["quantity_hourly_sealed"] = append(out["quantity_hourly_sealed"], fmt.Sprint(
				r["quantity_type"], r["provider"], r["slug"], r["worn"], r["unit"], r["bucket"], r["n"], r["sum"], r["min"], r["max"]))
		}
	}
	for _, lines := range out {
		sort.Strings(lines)
	}
	return out
}

// An object written chunk by chunk leaves what the same object written as one
// batch leaves, row for row and count for count: in-object duplicates on both
// sides of a chunk boundary, copies of one Replaceable sample, a provider
// record whose start moves inside the object, a deletion that comes after the
// record it deletes, parents with their children, failures, and a second
// object over the rows of the first. Sealed and clear, aliased or not.
func TestStreamedWriteMatchesBatch(t *testing.T) {
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) string {
		return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).Format(time.RFC3339)
	}
	units := map[string]string{"step_count": "count", "heart_rate": "bpm", "resting_heart_rate": "bpm", "active_energy_burned": "kcal"}
	qty := func(qt string, v float64, start string, extra ...any) string {
		f := map[string]any{"type": qt, "value": v, "unit": units[qt], "start": start}
		for i := 0; i+1 < len(extra); i += 2 {
			f[extra[i].(string)] = extra[i+1]
		}
		return line(f)
	}
	first := []string{
		qty("step_count", 100, at(10, 0), "tz", "Europe/Paris", "ext", "a1"),
		qty("heart_rate", 60, at(10, 5), "ext", "a2"),
		qty("step_count", 100, at(10, 0), "tz", "Europe/Paris", "ext", "a1-again"), // the first line again
		line(map[string]any{"kind": "sleep", "id": "night-1", "start": at(22, 0)}),
		qty("step_count", 500, at(0, 0), "replaceable", true), // a running total
		line(map[string]any{"rr": 812.0, "start": at(10, 1)}),
		line(map[string]any{"code": "body_temperature", "value": 36.6, "start": at(10, 2)}),
		qty("step_count", 700, at(0, 0), "replaceable", true), // its newer copy
		qty("resting_heart_rate", 48, at(1, 0), "ext", "cycle-1", "moved", true),
		line(map[string]any{"kind": "samples", "id": "rec-1", "start": at(12, 0)}),
		`{"not json`,
		line(map[string]any{"kind": "delete", "id": "rec-1"}), // after the record it deletes
		line(map[string]any{"kind": "delete", "id": "never-seen"}),
		qty("step_count", 30, at(11, 0), "ext", "a3"),
		qty("resting_heart_rate", 49, at(2, 0), "ext", "cycle-1", "moved", true), // the same record, start moved
		line(map[string]any{"fail": "boom", "type": "step_count"}),
		qty("active_energy_burned", 12.5, at(10, 10), "ext", "a4"),
		line(map[string]any{"kind": "workout", "id": "run-1", "start": at(15, 0)}),
		qty("step_count", 10, at(24, 0), "replaceable", true), // one copy only
		qty("heart_rate", 61, at(10, 6), "ext", "a5"),
		line(map[string]any{"rr": 815.0, "start": at(10, 3)}),
		line(map[string]any{"rr": 812.0, "start": at(10, 1)}), // the first beat again
		qty("step_count", 44, at(23, 30), "ext", "a6"),
	}
	second := []string{
		qty("step_count", 20, at(24, 0), "replaceable", true),                // replaces the stored figure
		qty("step_count", 100, at(10, 0), "tz", "Europe/Paris", "ext", "b1"), // stored already
		line(map[string]any{"kind": "delete", "id": "night-1"}),
		qty("resting_heart_rate", 47, at(3, 0), "ext", "cycle-1", "moved", true),
		qty("step_count", 55, at(11, 30), "ext", "b2"),
		line(map[string]any{"kind": "delete", "id": "a3"}),
		qty("heart_rate", 62, at(10, 7), "ext", "b3"),
		qty("step_count", 9, at(11, 45), "ext", "a3"), // tombstoned by this very object
		line(map[string]any{"code": "body_temperature", "value": 36.6, "start": at(10, 2)}),
	}
	for _, mode := range []struct {
		name  string
		clear bool
	}{{"sealed", false}, {"clear", true}} {
		for _, dataType := range []string{"plain", "aliased"} {
			t.Run(mode.name+"/"+dataType, func(t *testing.T) {
				e := newEnv(t)
				e.now = func() time.Time { return day.AddDate(0, 0, 3) }
				e.register("mix", mixMap)
				whole, chunked := e.user, e.newUser(e.tenant)
				run := func(user uuid.UUID, chunk int, lines []string) Counts {
					opts := e.opts()
					if mode.clear {
						opts = e.clearOpts()
					}
					opts.Writer.ChunkRecords = chunk
					c, err := ProcessWith(context.Background(), e.pool, e.rs, e.upload(e.tenant, user, "mix", dataType, lines...), opts)
					require.NoError(t, err)
					return c
				}
				for i, lines := range [][]string{first, second, first} {
					want := run(whole, 1000, lines)
					got := run(chunked, 3, lines)
					assert.Equal(t, want, got, "counts of object %d", i+1)
					wantRows, gotRows := e.userDump(e.tenant, whole), e.userDump(e.tenant, chunked)
					for table, rows := range wantRows {
						assert.Equal(t, rows, gotRows[table], "%s after object %d", table, i+1)
					}
					assert.Equal(t, len(wantRows), len(gotRows), "tables with rows after object %d", i+1)
					require.NotEmpty(t, wantRows[tQuantity])
				}
				// The fixture did what it is there for.
				rows := e.userDump(e.tenant, chunked)
				assert.NotEmpty(t, rows["deleted_records"])
				assert.NotEmpty(t, rows["failed_records"])
				assert.NotEmpty(t, rows[tSeries])
				assert.NotEmpty(t, rows[tObservation])
				assert.NotEmpty(t, rows[tDaily])
				if !mode.clear {
					assert.NotEmpty(t, rows["quantity_hourly_sealed"])
				}
				if dataType == "aliased" {
					assert.NotEmpty(t, rows["record_aliases"])
				}
				moved := 0
				for _, r := range rows[tQuantity] {
					if strings.Contains(r, "cycle-1") {
						moved++
					}
				}
				assert.Equal(t, 1, moved, "one copy of the record whose start moved")
			})
		}
	}
}

// A streamed object processed again reads the same and writes nothing: its
// held records and its chunks meet the rows of the first run.
func TestStreamedWriteIsRepeatable(t *testing.T) {
	e := newEnv(t)
	e.register("mix", mixMap)
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	var lines []string
	for i := range 10 {
		lines = append(lines, q("heart_rate", float64(60+i), "bpm", day.Add(time.Duration(i)*time.Minute)))
	}
	lines = append(lines, line(map[string]any{"kind": "sleep", "id": "night-1", "start": day.Format(time.RFC3339)}))
	job := e.upload(e.tenant, e.user, "mix", "plain", lines...)
	opts := e.opts()
	opts.Writer.ChunkRecords = 4
	c, err := ProcessWith(context.Background(), e.pool, e.rs, job, opts)
	require.NoError(t, err)
	assert.Equal(t, 11, c[tQuantity].Written, "ten heart rates and the night's resting heart rate")
	assert.Equal(t, 1, c[tSleep].Written)

	again, err := ProcessWith(context.Background(), e.pool, e.rs, job, opts)
	require.NoError(t, err)
	assert.Equal(t, 0, again.TotalWritten(), "the object again writes nothing")
	assert.Equal(t, 11, e.count(tQuantity))
	assert.Equal(t, 1, e.count(tSleep))
	assert.Equal(t, 2, e.count(tPhases))
}
