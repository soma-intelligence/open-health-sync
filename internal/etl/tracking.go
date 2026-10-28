// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Execer is the Exec method of pgx.Tx and pgxpool.Pool.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// FailedRecord is one raw line that failed parsing, mapping, unit conversion
// or validation.
type FailedRecord struct {
	LineNum    int
	RawPayload map[string]any
	Error      string
	// ReasonCode classifies why the record failed so the recovery loop can
	// route it: "parse" (malformed line, never replayable as-is), "mapping"
	// (the connector rejected it), "unit" (no conversion yet, replayable once
	// one exists), "validation" (data quality rule). Empty becomes "unknown".
	ReasonCode string
}

// failedRecordBatchSize bounds the multi-VALUES insert: a provider format
// change fails every record of a part, and one statement over the 65535
// parameter ceiling would fail the job and lose the rows that make it
// recoverable.
const failedRecordBatchSize = 100

// failedShape is the write form of failed_records. Its identity is the row
// id, which a multi-row INSERT does not give back in order, so a sealed write
// reserves the ids first (reserveFailedIDs).
var failedShape = newShape("failed_records",
	[]string{"id", "tenant_id", "user_id", "provider", "raw_key", "line_number", "raw_payload", "reason_code", "error_message", "status"},
	nil, nil)

// writeFailedRecords inserts record failures into ohs_data.failed_records,
// status pending.
func writeFailedRecords(ctx context.Context, tx TxLike, sc scope, rawKey string, records []FailedRecord) error {
	for i := 0; i < len(records); i += failedRecordBatchSize {
		batch := records[i:min(i+failedRecordBatchSize, len(records))]
		rows := make([][]any, len(batch))
		for j, rec := range batch {
			reason := rec.ReasonCode
			if reason == "" {
				reason = "unknown"
			}
			// The payload and the message can quote a NUL from the source,
			// which jsonb and text refuse: the failure would fail too.
			var payload any
			if rec.RawPayload != nil {
				payload = model.ScrubJSON(rec.RawPayload)
			}
			rows[j] = []any{nil, sc.tenant, sc.user, sc.provider, rawKey, rec.LineNum, payload, reason, model.ScrubJSON(rec.Error), "pending"}
		}
		if err := insertFailed(ctx, tx, sc, rows); err != nil {
			return fmt.Errorf("insert failed_records: %w", err)
		}
	}
	return nil
}

// insertFailed inserts failed_records rows (failedShape base rows, id unset).
// Without keys the id is the column's default; with keys it is reserved
// first, since it is the row's identity, and given OVERRIDING SYSTEM VALUE.
func insertFailed(ctx context.Context, tx TxLike, sc scope, rows [][]any) error {
	columns := failedShape.columns(sc)[1:]
	override := ""
	if sc.uv != nil {
		columns, override = failedShape.columns(sc), " OVERRIDING SYSTEM VALUE"
		ids, err := reserveFailedIDs(ctx, tx, len(rows))
		if err != nil {
			return err
		}
		for i := range rows {
			rows[i][0] = ids[i]
		}
	}
	args := make([]any, 0, len(rows)*len(columns))
	phs := make([]string, 0, len(rows))
	for j, r := range rows {
		f, err := failedShape.finish(sc, r)
		if err != nil {
			return err
		}
		if sc.uv == nil {
			f = f[1:]
		}
		phs = append(phs, placeholders(j*len(columns)+1, len(columns)))
		args = append(args, f...)
	}
	_, err := tx.Exec(ctx, `INSERT INTO ohs_data.failed_records (`+quoteCols(columns)+`)`+override+` VALUES `+
		strings.Join(phs, ", "), args...)
	return err
}

// reserveFailedIDs takes n ids from the failed_records sequence.
func reserveFailedIDs(ctx context.Context, tx TxLike, n int) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT nextval(pg_get_serial_sequence('ohs_data.failed_records', 'id'))
		FROM generate_series(1, $1)`, n)
	if err != nil {
		return nil, fmt.Errorf("reserve failed_records ids: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// permanentReason says whether a failure reason is deterministic: the same
// bytes through any later binary fail the same way. It matches the drainer's
// permanent reasons (internal/etl/automation).
func permanentReason(code string) bool { return code == "parse" || code == "validation" }

// anyRecoverable says whether a code fix may rescue one of the failures.
func anyRecoverable(failed []FailedRecord) bool {
	for _, f := range failed {
		if !permanentReason(f.ReasonCode) {
			return true
		}
	}
	return false
}

// writeJobFailure records a whole raw object the ETL could not land: a row
// with no line number and reason "job", so an all-failed object is visible
// and reprocessable as one unit, not only as its per-line rows.
func writeJobFailure(ctx context.Context, tx TxLike, sc scope, rawKey, msg string) error {
	row := []any{nil, sc.tenant, sc.user, sc.provider, rawKey, nil, nil, "job", model.ScrubJSON(msg), "pending"}
	if err := insertFailed(ctx, tx, sc, [][]any{row}); err != nil {
		return fmt.Errorf("insert job failure: %w", err)
	}
	return nil
}

// syncLogEntry is one ohs_sync.sync_log row of stage etl: either the whole
// object (metric_type "all") or one metric of it.
type syncLogEntry struct {
	MetricType string
	Written    int
	Skipped    int
	Failed     int
	Status     string // success | partial | failed
	DurationMS int64  // set on the object row only
}

// writeSyncLog inserts the etl rows of one processed raw object.
func writeSyncLog(ctx context.Context, tx Execer, sc scope, rawKey string, entries []syncLogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	const colCount = 11
	args := make([]any, 0, len(entries)*colCount)
	phs := make([]string, 0, len(entries))
	for i, e := range entries {
		phs = append(phs, placeholders(i*colCount+1, colCount))
		args = append(args, sc.tenant, sc.user, sc.provider, "etl", rawKey, e.MetricType, e.Status,
			e.Written, e.Skipped, e.Failed, e.DurationMS)
	}
	q := `INSERT INTO ohs_sync.sync_log
		(tenant_id, user_id, provider, stage, raw_key, metric_type, status,
		 records_written, duplicates_skipped, records_failed, duration_ms)
		VALUES ` + strings.Join(phs, ", ")
	if _, err := tx.Exec(ctx, q, args...); err != nil {
		return fmt.Errorf("insert sync_log: %w", err)
	}
	return nil
}

// metricAll is the metric_type of the object-level sync_log row.
const metricAll = "all"

// coverageEntry is one (metric, local data day) count of one processed object.
type coverageEntry struct {
	Metric string
	Date   time.Time // the data day, midnight UTC representing a calendar date
	Count  int
}

// coverageBatchSize bounds the multi-VALUES upsert: coverage cardinality is
// metrics x data days, so a multi-year import produces tens of thousands of
// rows.
const coverageBatchSize = 500

// writeCoverage upserts coverage rows into ohs_sync.pull_coverage. On conflict
// it keeps GREATEST(existing, new): accumulating would break replay
// idempotence, and replacing would let a small object clobber the count a
// larger one contributed for the same day. Known trade-off: the count is a
// high-water mark, so a corrected re-sync with fewer records is not reflected.
func writeCoverage(ctx context.Context, tx Execer, sc scope, entries []coverageEntry) error {
	const colCount = 6
	for i := 0; i < len(entries); i += coverageBatchSize {
		batch := entries[i:min(i+coverageBatchSize, len(entries))]
		args := make([]any, 0, len(batch)*colCount)
		phs := make([]string, 0, len(batch))
		for j, e := range batch {
			phs = append(phs, placeholders(j*colCount+1, colCount))
			args = append(args, sc.tenant, sc.user, strings.ToLower(sc.provider), e.Metric, e.Date, e.Count)
		}
		q := `INSERT INTO ohs_sync.pull_coverage (tenant_id, user_id, provider, metric, date, records_count) VALUES ` +
			strings.Join(phs, ", ") + `
			ON CONFLICT (tenant_id, user_id, provider, metric, date) DO UPDATE SET
				records_count = GREATEST(pull_coverage.records_count, EXCLUDED.records_count),
				last_synced_at = now()`
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			return fmt.Errorf("upsert pull_coverage: %w", err)
		}
	}
	return nil
}
