// SPDX-License-Identifier: AGPL-3.0-only

// Package etl turns raw provider payloads into canonical records: it reads a
// raw object, maps every line through the provider's connector, canonicalizes
// units and attribution, validates, and writes the result into ohs_data in one
// transaction, together with the failed records, the sync log and coverage.
package etl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// Lookup finds the connector registered under a provider slug.
type Lookup func(slug string) (connector.Connector, bool)

// Options tunes ProcessWith. The zero value uses the default connector
// registry and writer settings.
type Options struct {
	Lookup Lookup
	Writer WriterConfig
	// Now is the clock the future-timestamp check reads (default time.Now).
	Now func() time.Time
}

// Process normalizes the raw object of job into the canonical store and
// returns the per-table counts. Records that fail to parse, map, convert or
// validate are kept in ohs_data.failed_records and do not fail the job; an
// error means the job should be retried (raw store, database, unknown
// provider). A user who is not active (deleting or gone) gets nothing written
// and no error, so the job completes.
func Process(ctx context.Context, pool *pgxpool.Pool, rs rawstore.Store, job queue.ETLJob) (Counts, error) {
	return ProcessWith(ctx, pool, rs, job, Options{})
}

// metricDetail counts one metric of one object for the sync log.
type metricDetail struct {
	written, skipped, failed, suspect int
}

// ProcessWith is Process with explicit options.
func ProcessWith(ctx context.Context, pool *pgxpool.Pool, rs rawstore.Store, job queue.ETLJob, opts Options) (Counts, error) {
	start := time.Now()
	lookup := opts.Lookup
	if lookup == nil {
		lookup = connector.Get
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	validatedAt := now()
	sc := scope{tenant: job.TenantID, user: job.UserID, provider: job.Provider}
	log := slog.With("job_id", job.ID, "tenant_id", job.TenantID, "user_id", job.UserID, "provider", job.Provider)

	if active, err := userActive(ctx, pool, sc, false); err != nil {
		return nil, err
	} else if !active {
		log.InfoContext(ctx, "etl job skipped, user not active")
		return Counts{}, nil
	}

	dataType, err := rawDataType(ctx, pool, sc, job.Key)
	if err != nil {
		return nil, err
	}
	conn, ok := lookup(job.Provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", job.Provider)
	}

	// Stream the object, mapping and validating one record at a time. The raw
	// map is released as soon as its MapResult is folded into acc.
	acc := &model.MapResult{SourceKey: job.Key}
	cov := newCoverageBuilder(validatedAt)
	details := map[string]*metricDetail{}
	suspects := 0
	// Record failures are kept apart from parse errors so the final slice puts
	// parse errors first, then the records that parsed but did not survive.
	var recordFailures []FailedRecord
	fail := func(i int, record map[string]any, reason, msg string) {
		recordFailures = append(recordFailures, FailedRecord{LineNum: i + 1, RawPayload: record, Error: msg, ReasonCode: reason})
		detail(details, metricTypeFromRecord(record)).failed++
	}

	reader := NewReader(rs)
	rr, err := reader.StreamNDJSON(ctx, job.Key, func(i int, record map[string]any) error {
		mr, mapErr := mapRecord(conn, connector.RawRecord{
			TenantID: job.TenantID.String(), UserID: job.UserID.String(), Provider: job.Provider,
			DataType: dataType, SourceKey: job.Key, Fields: record,
		})
		if mapErr != nil {
			fail(i, record, "mapping", fmt.Sprintf("mapping error: %v", mapErr))
			return nil
		}
		if mr.IsEmpty() {
			// The connector skipped the record on purpose (an unknown type, a
			// line without a value): nothing to store, and not a failure. The
			// connector counts and logs what it skips.
			return nil
		}
		stampOwner(mr, sc)

		// Attribution choke point: which upstream produced each sample, not only
		// which integration delivered it.
		model.CanonicalizeSourceAttribution(mr, record, job.Provider)

		// Unit choke point, before validation, so the range checks can assume
		// the storage unit. An unconvertible unit is a recoverable failure.
		if unitErrs := model.CanonicalizeUnits(mr); len(unitErrs) > 0 {
			fail(i, record, "unit", fmt.Sprintf("unit errors: %v", unitErrs))
			return nil
		}

		// A reject is a value the metric cannot physically take: parked, never
		// written. A suspect is implausible but possible: written and flagged.
		valErrors := ValidateMapResultAt(mr, validatedAt)
		if rejects := Rejections(valErrors); len(rejects) > 0 {
			// Scope the rejection to the offending entries: one record can map to
			// a whole day's series, and dropping all of it over one out-of-band
			// sample would erase the day.
			structural := pruneRejected(mr, rejects)
			fail(i, record, "validation", fmt.Sprintf("validation errors: %v", rejects))
			if len(structural) > 0 || mr.IsEmpty() {
				return nil
			}
			// The indices in valErrors point at the pre-prune slices.
			valErrors = ValidateMapResultAt(mr, validatedAt)
		}
		if s := Suspicions(valErrors); len(s) > 0 {
			FlagSuspects(mr, s)
			suspects += len(s)
			detail(details, metricTypeFromRecord(record)).suspect += len(s)
		}

		// Heartbeat series identity: the raw object's data type, unless the
		// connector named the series itself.
		if dataType != "" {
			for j := range mr.HeartbeatSamples {
				if mr.HeartbeatSamples[j].SeriesID == "" {
					mr.HeartbeatSamples[j].SeriesID = dataType
				}
			}
		}

		cov.add(mr)
		Accumulate(acc, mr)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read raw object: %w", err)
	}
	// A stream that died mid-read is a job failure, not a partial: the object is
	// usually intact and the next read gets all of it.
	if rr.ScanTruncated && rr.Truncation != TruncationLimit {
		return nil, fmt.Errorf("read raw object: stream truncated after %d record(s)", rr.RecordCount)
	}
	// A cap this reader imposes is deterministic: the head is written and the
	// tail is recorded as a parse failure.
	if rr.ScanTruncated {
		log.WarnContext(ctx, "reader cap hit, processing partial",
			"truncation", string(rr.Truncation), "parsed_records", rr.RecordCount)
	}
	if suspects > 0 {
		log.WarnContext(ctx, "implausible values flagged", "count", suspects)
	}
	recordsIn := rr.RecordCount + len(rr.ParseErrors) + rr.ParseErrorsDropped

	failed := make([]FailedRecord, 0, len(rr.ParseErrors)+len(recordFailures))
	for _, pe := range rr.ParseErrors {
		failed = append(failed, FailedRecord{
			LineNum:    pe.LineNum,
			RawPayload: map[string]any{"raw_line": pe.RawLine},
			Error:      fmt.Sprintf("parse error: %v", pe.Err),
			ReasonCode: "parse",
		})
	}
	failed = append(failed, recordFailures...)
	recordsFailed := len(failed) + rr.ParseErrorsDropped

	// One write transaction per object. Late data for an old period can land
	// in a chunk TimescaleDB's compression job is compressing at that moment,
	// and the two may deadlock: retry the transaction a few times before
	// leaving it to the job queue's own retries.
	writeObject := func() (Counts, string, bool, error) {
		dt := cloneDetails(details)
		tx, err := pool.Begin(ctx)
		if err != nil {
			return nil, "", false, fmt.Errorf("begin transaction: %w", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		// Re-check under a share lock: a deletion that flips the user to deleting
		// waits for this transaction, and one that already did stops the write.
		if active, err := userActive(ctx, tx, sc, true); err != nil {
			return nil, "", false, err
		} else if !active {
			return Counts{}, "", true, nil
		}

		counts := Counts{}
		if !acc.IsEmpty() {
			c, err := NewWriter(opts.Writer).WriteAccumulated(ctx, tx, sc, acc)
			if err != nil {
				return nil, "", false, fmt.Errorf("write records: %w", err)
			}
			counts = c
			if days := touchedDays(sc, acc); len(days) > 0 {
				n, k, err := deriveDailySummaries(ctx, tx, sc, days)
				if err != nil {
					return nil, "", false, fmt.Errorf("derive daily summaries: %w", err)
				}
				if n+k > 0 {
					counts.add(tDaily, n, k)
				}
			}
			for table, tc := range counts {
				d := detail(dt, table)
				d.written += tc.Written
				d.skipped += tc.Skipped
			}
		}
		written := counts.TotalWritten()

		if len(failed) > 0 {
			if err := writeFailedRecords(ctx, tx, sc, job.Key, failed); err != nil {
				return nil, "", false, err
			}
		}
		// All-failed object (nothing written or already stored): the per-line rows alone do not make it visible as one
		// unit, so add a job-level row in the same transaction.
		if written+counts.TotalSkipped() == 0 && recordsFailed > 0 {
			if err := writeJobFailure(ctx, tx, sc, job.Key,
				fmt.Sprintf("all %d record(s) failed (0 written)", recordsFailed)); err != nil {
				return nil, "", false, err
			}
		}

		status := "success"
		if recordsIn > 0 && recordsFailed > 0 {
			status = "partial"
		}
		entries := []syncLogEntry{{
			MetricType: metricAll, Status: status, Written: written, Skipped: counts.TotalSkipped(),
			Failed: recordsFailed, DurationMS: time.Since(start).Milliseconds(),
		}}
		entries = append(entries, metricEntries(dt)...)
		if err := writeSyncLog(ctx, tx, sc, job.Key, entries); err != nil {
			return nil, "", false, err
		}
		covEntries := cov.entries()
		if err := writeCoverage(ctx, tx, sc, covEntries); err != nil {
			return nil, "", false, err
		}
		if err := enqueueWebhooks(ctx, tx, sc, counts, covEntries); err != nil {
			return nil, "", false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, "", false, fmt.Errorf("commit transaction: %w", err)
		}
		return counts, status, false, nil
	}
	var (
		counts      Counts
		status      string
		userSkipped bool
	)
	for attempt := 1; ; attempt++ {
		counts, status, userSkipped, err = writeObject()
		if err == nil || attempt == maxWriteAttempts || !retryableWrite(err) {
			break
		}
		log.WarnContext(ctx, "write transaction retried", "attempt", attempt, "error", err)
		if err := sleepCtx(ctx, time.Duration(attempt)*100*time.Millisecond); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	if userSkipped {
		log.InfoContext(ctx, "etl job skipped, user not active")
		return Counts{}, nil
	}
	written := counts.TotalWritten()

	log.InfoContext(ctx, "raw object processed",
		"status", status, "records_in", recordsIn, "records_out", written,
		"duplicates", counts.TotalSkipped(), "failed", recordsFailed,
		"duration_ms", time.Since(start).Milliseconds())
	return counts, nil
}

// mapRecord calls the connector, turning a panic into a mapping error so one
// bad line cannot take the worker down.
func mapRecord(c connector.Connector, raw connector.RawRecord) (mr *model.MapResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			mr, err = nil, fmt.Errorf("connector panic: %v", r)
		}
	}()
	res, err := c.Map(raw)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// queryRower is QueryRow of a pool or a transaction.
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// userActive reports whether the job's user exists in its tenant and is
// active. forShare locks the row until the transaction ends.
func userActive(ctx context.Context, db queryRower, sc scope, forShare bool) (bool, error) {
	q := `SELECT status FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2`
	if forShare {
		q += ` FOR SHARE`
	}
	var status string
	err := db.QueryRow(ctx, q, sc.tenant, sc.user).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("look up user: %w", err)
	}
	return status == "active", nil
}

// rawDataType loads the data type registered for the raw object. One raw
// object holds one data type.
func rawDataType(ctx context.Context, db queryRower, sc scope, key string) (string, error) {
	var dt string
	err := db.QueryRow(ctx, `SELECT data_type FROM ohs_sync.raw_objects
		WHERE tenant_id = $1 AND user_id = $2 AND key = $3`, sc.tenant, sc.user, key).Scan(&dt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("raw object %s is not registered", key)
	}
	if err != nil {
		return "", fmt.Errorf("look up raw object: %w", err)
	}
	return dt, nil
}

// stampOwner sets the job's tenant and user on every record, whatever the
// connector put there. The writer takes the row owner from the job too; this
// keeps the in-memory records (and anything hashing them) consistent with it.
func stampOwner(mr *model.MapResult, sc scope) {
	t, u := sc.tenant.String(), sc.user.String()
	for i := range mr.QuantitySamples {
		mr.QuantitySamples[i].TenantID, mr.QuantitySamples[i].UserID = t, u
	}
	for i := range mr.CategorySamples {
		mr.CategorySamples[i].TenantID, mr.CategorySamples[i].UserID = t, u
	}
	for i := range mr.SleepSessions {
		mr.SleepSessions[i].TenantID, mr.SleepSessions[i].UserID = t, u
	}
	for i := range mr.SleepPhases {
		mr.SleepPhases[i].TenantID, mr.SleepPhases[i].UserID = t, u
	}
	for i := range mr.Workouts {
		mr.Workouts[i].TenantID, mr.Workouts[i].UserID = t, u
	}
	for i := range mr.RoutePoints {
		mr.RoutePoints[i].TenantID, mr.RoutePoints[i].UserID = t, u
	}
	for i := range mr.NutritionLogs {
		mr.NutritionLogs[i].TenantID, mr.NutritionLogs[i].UserID = t, u
	}
	for i := range mr.ECGRecordings {
		mr.ECGRecordings[i].TenantID, mr.ECGRecordings[i].UserID = t, u
	}
	for i := range mr.ECGSamples {
		mr.ECGSamples[i].TenantID, mr.ECGSamples[i].UserID = t, u
	}
	for i := range mr.HeartbeatSamples {
		mr.HeartbeatSamples[i].TenantID, mr.HeartbeatSamples[i].UserID = t, u
	}
	for i := range mr.DailySummaries {
		mr.DailySummaries[i].TenantID, mr.DailySummaries[i].UserID = t, u
	}
	for i := range mr.Observations {
		mr.Observations[i].TenantID, mr.Observations[i].UserID = t, u
	}
}

func metricTypeFromRecord(record map[string]any) string {
	for _, key := range []string{"type", "quantity_type", "category_type", "activity_type"} {
		if v, ok := record[key].(string); ok {
			return v
		}
	}
	return "unknown"
}

func detail(details map[string]*metricDetail, key string) *metricDetail {
	d, ok := details[key]
	if !ok {
		d = &metricDetail{}
		details[key] = d
	}
	return d
}

// metricEntries turns per-metric counters into sync_log rows. Written counts
// are keyed by table, failures by the raw record's type.
func metricEntries(details map[string]*metricDetail) []syncLogEntry {
	out := make([]syncLogEntry, 0, len(details))
	for metric, d := range details {
		status := "success"
		switch {
		case d.failed > 0 && d.written > 0:
			status = "partial"
		case d.failed > 0:
			status = "failed"
		}
		out = append(out, syncLogEntry{MetricType: metric, Written: d.written, Skipped: d.skipped, Failed: d.failed, Status: status})
	}
	return out
}

// Coverage keys for the non-quantity record families.
const (
	coverageKeySleep     = "sleep_analysis"
	coverageKeyWorkout   = "workout"
	coverageKeyNutrition = "nutrition"
	coverageKeyECG       = "ecg"
	coverageKeyHeartbeat = "heartbeat"
	coverageKeyDaily     = "daily_summary"
)

// coverageBuilder counts mapped records per (canonical metric, local data
// day) as they stream past. It counts mapped records, not written rows: a
// replayed object whose rows already exist still proves the data is present.
// The day is the record's own day in its "timezone" metadata, or UTC.
type coverageBuilder struct {
	today  time.Time
	counts map[string]map[time.Time]int
}

func newCoverageBuilder(now time.Time) *coverageBuilder {
	return &coverageBuilder{
		today:  dateOnly(now.UTC()),
		counts: map[string]map[time.Time]int{},
	}
}

func (b *coverageBuilder) addAt(metric string, ts time.Time, md map[string]any) {
	if metric == "" {
		return
	}
	day := b.today // undated record: the processing day
	if !ts.IsZero() {
		day = localDate(ts, md)
	}
	byDay, ok := b.counts[metric]
	if !ok {
		byDay = map[time.Time]int{}
		b.counts[metric] = byDay
	}
	byDay[day]++
}

func (b *coverageBuilder) add(mr *model.MapResult) {
	for _, s := range mr.QuantitySamples {
		b.addAt(s.QuantityType, s.StartDate, s.Metadata)
	}
	for _, s := range mr.CategorySamples {
		b.addAt(s.CategoryType, s.StartDate, s.Metadata)
	}
	for _, s := range mr.SleepSessions {
		b.addAt(coverageKeySleep, s.BedtimeStart, s.Metadata)
	}
	for _, w := range mr.Workouts {
		b.addAt(coverageKeyWorkout, w.StartDate, w.Metadata)
	}
	for _, n := range mr.NutritionLogs {
		b.addAt(coverageKeyNutrition, n.StartDate, n.Metadata)
	}
	for _, e := range mr.ECGRecordings {
		b.addAt(coverageKeyECG, e.StartDate, e.Metadata)
	}
	for _, h := range mr.HeartbeatSamples {
		b.addAt(coverageKeyHeartbeat, h.Timestamp, nil)
	}
	for _, d := range mr.DailySummaries {
		// Already a calendar date.
		b.addAt(coverageKeyDaily, dateOnly(d.SummaryDate), nil)
	}
	for _, o := range mr.Observations {
		b.addAt(o.CanonicalCode, o.TS, o.Metadata)
	}
}

func (b *coverageBuilder) entries() []coverageEntry {
	var out []coverageEntry
	for metric, byDay := range b.counts {
		for day, n := range byDay {
			out = append(out, coverageEntry{Metric: metric, Date: day, Count: n})
		}
	}
	return out
}

// maxWriteAttempts bounds the in-process retries of one object's write
// transaction.
const maxWriteAttempts = 3

// retryableWrite reports whether a write transaction failed on a deadlock or a
// serialization failure, which a fresh attempt can clear.
func retryableWrite(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40P01" || pgErr.Code == "40001")
}

// cloneDetails copies the per-metric counts so a retried transaction starts
// from the counts gathered before the write.
func cloneDetails(in map[string]*metricDetail) map[string]*metricDetail {
	out := make(map[string]*metricDetail, len(in))
	for k, v := range in {
		d := *v
		out[k] = &d
	}
	return out
}

// sleepCtx waits for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
