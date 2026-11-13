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
	"github.com/soma-intelligence/open-health-sync/internal/consent"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
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
	// ConsentRequired (OHS_CONSENT_REQUIRED) stores nothing from a source
	// the user has recorded no consent for.
	ConsentRequired bool
	// Vault, when set, gives the job the user's keys before anything is
	// read. A user being deleted whose keys are already shredded ends like
	// any job of a user who is not active; an active user with no keys
	// fails the job, which the queue retries and then parks.
	Vault *vault.Vault
}

// Process normalizes the raw object of job into the canonical store and
// returns the per-table counts. Records that fail to parse, map, convert or
// validate are kept in ohs_data.failed_records and do not fail the job; an
// error means the job should be retried (raw store, database, unknown
// provider). A user who is not active (deleting or gone), and a raw object
// erased while it was being read, get nothing written and no error, so the
// job completes.
func Process(ctx context.Context, pool *pgxpool.Pool, rs rawstore.Store, job queue.ETLJob) (Counts, error) {
	return ProcessWith(ctx, pool, rs, job, Options{})
}

// metricDetail counts one metric of one object for the sync log.
type metricDetail struct {
	written, skipped, failed, suspect int
}

// ProcessWith is Process with explicit options.
//
// The object is read once, one record at a time, into the batch the writer
// takes (objectPlan). An object with more bulk records than one write chunk
// (WriterConfig.ChunkRecords) is not held: the first read keeps only what the
// write needs as a whole, and the write transaction reads the object a second
// time and writes its bulk records chunk by chunk (stream.go), so the memory
// of a job does not grow with the size of its object.
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

	// The keys first: from the shred of a deletion on, the user has none,
	// and the status check below tells that case from a user never given
	// keys.
	var (
		keys    *vault.UserVault
		keysErr error
	)
	if opts.Vault != nil {
		if keys, keysErr = opts.Vault.ForUser(ctx, job.TenantID, job.UserID); keysErr != nil && !errors.Is(keysErr, vault.ErrNoKeys) {
			return nil, fmt.Errorf("user keys: %w", keysErr)
		}
	}
	active, zone, err := userState(ctx, pool, sc, false)
	if err != nil {
		return nil, err
	}
	if !active {
		log.InfoContext(ctx, "etl job skipped, user not active")
		return Counts{}, nil
	}
	if keysErr != nil {
		return nil, fmt.Errorf("user keys: %w", keysErr)
	}
	// The same keys open the raw object and seal every row the job writes.
	sc.uv = keys
	// The user's zone, as it is when the job starts, is the zone of the local
	// day of every record that names none.
	sc.zone = zone

	// Consent choke point: categories the user has not granted for this
	// source are dropped before validation, so nothing of them is stored.
	policy, err := consent.Load(ctx, pool, job.TenantID, job.UserID, job.Provider, opts.ConsentRequired)
	if err != nil {
		return nil, err
	}

	// The object's capture time orders the copies of a replaceable row, so a
	// retried or replayed job for an older object cannot overwrite what a
	// newer object stored (see latestWins).
	dataType, captured, err := rawObjectInfo(ctx, pool, sc, job.Key)
	if err != nil {
		return nil, err
	}
	sc.captured = captured
	conn, ok := lookup(job.Provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", job.Provider)
	}

	// Stream the object, mapping and validating one record at a time. The raw
	// map is released as soon as its MapResult is folded into the plan.
	lines := &lineMapper{conn: conn, job: job, sc: sc, dataType: dataType, policy: policy, validatedAt: validatedAt}
	writer := NewWriter(opts.Writer)
	plan := newObjectPlan(sc, job.Key, writer.cfg.ChunkRecords)
	st := newPassState()
	cov := newCoverageBuilder(validatedAt)
	cov.zone = sc.zone
	// A device upload that carries a zone teaches the user's zone.
	var learnt zoneSeen
	_, device := conn.(connector.DeviceConnector)

	reader := NewReader(rs)
	if keys != nil {
		// The job's keys open its sealed raw object.
		reader = reader.WithKeys(keys.Keys)
	}
	rr, err := reader.StreamNDJSON(ctx, job.Key, func(i int, record map[string]any) error {
		mr := lines.mapLine(st, i, record)
		if mr == nil {
			return nil
		}
		cov.add(mr)
		if device {
			learnt.add(mr, validatedAt)
		}
		if plan.add(mr) {
			// The object is larger than one chunk: its record failures are
			// written as the second read meets them, not held.
			st.release()
		}
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
	if st.suspects > 0 {
		log.WarnContext(ctx, "implausible values flagged", "count", st.suspects)
	}
	if st.withheld > 0 {
		log.InfoContext(ctx, "records withheld, no consent for their category", "count", st.withheld)
	}
	plan.seal()
	recordsIn := rr.RecordCount + len(rr.ParseErrors) + rr.ParseErrorsDropped

	// Parse errors first, then the records that parsed but did not survive.
	parseFailed := make([]FailedRecord, 0, len(rr.ParseErrors))
	for _, pe := range rr.ParseErrors {
		parseFailed = append(parseFailed, FailedRecord{
			LineNum:    pe.LineNum,
			RawPayload: map[string]any{"raw_line": pe.RawLine},
			Error:      fmt.Sprintf("parse error: %v", pe.Err),
			ReasonCode: "parse",
		})
	}
	recordsFailed := len(parseFailed) + st.failed + rr.ParseErrorsDropped

	// One write transaction per object. Late data for an old period can land
	// in a chunk TimescaleDB's compression job is compressing at that moment,
	// and the two may deadlock: retry the transaction a few times before
	// leaving it to the job queue's own retries.
	writeObject := func() (Counts, string, bool, error) {
		dt := cloneDetails(st.details)
		tx, err := pool.Begin(ctx)
		if err != nil {
			return nil, "", false, fmt.Errorf("begin transaction: %w", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		// Re-check under a share lock: a deletion that flips the user to deleting
		// waits for this transaction, and one that already did stops the write.
		if active, _, err := userState(ctx, tx, sc, true); err != nil {
			return nil, "", false, err
		} else if !active {
			return Counts{}, "", true, nil
		}
		// The erasure of the user's data from this provider deletes the raw
		// object's row under the user row lock: an object erased while it was
		// read writes nothing, so the erased data cannot come back.
		if registered, err := rawRegistered(ctx, tx, sc, job.Key); err != nil {
			return nil, "", false, err
		} else if !registered {
			return Counts{}, "", true, nil
		}

		counts := Counts{}
		// The failures written at the end of the transaction: all of them,
		// unless the object is streamed, which writes them as it goes.
		failed := parseFailed
		if plan.spilled || !plan.acc.IsEmpty() {
			bw, err := writer.begin(ctx, tx, sc, plan.acc, plan.spilled && plan.n.quantity > 0)
			if err != nil {
				return nil, "", false, fmt.Errorf("write records: %w", err)
			}
			rest := plan.acc
			if plan.spilled {
				if rest, err = streamChunks(ctx, tx, bw, reader, lines, plan, rr, st, parseFailed); err != nil {
					return nil, "", false, err
				}
				failed = nil
			}
			c, err := bw.finish(ctx, tx, rest)
			if err != nil {
				return nil, "", false, fmt.Errorf("write records: %w", err)
			}
			counts = c
			if days := bw.touched(rest); len(days) > 0 {
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

		if !plan.spilled {
			failed = append(failed[:len(failed):len(failed)], st.failures...)
		}
		if len(failed) > 0 {
			if err := writeFailedRecords(ctx, tx, sc, job.Key, failed); err != nil {
				return nil, "", false, err
			}
		}
		// All-failed object (nothing written or already stored): the per-line rows alone do not make it visible as one
		// unit, so add a job-level row in the same transaction.
		// Only when a code fix may rescue it: an object whose every line
		// failed parse or validation would fail the same way again, and a job
		// row made the drainer replay it, and write its line failures again,
		// at every process start.
		if written+counts.TotalSkipped() == 0 && recordsFailed > 0 && st.recoverable {
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
		log.InfoContext(ctx, "etl job skipped, user not active or raw object erased")
		return Counts{}, nil
	}
	written := counts.TotalWritten()

	// Best effort, after the commit: the zone is a hint for later records, and
	// failing to note it must not fail a job whose data is stored.
	if learnt.name != "" {
		if err := learnUserZone(ctx, pool, sc, learnt); err != nil {
			log.WarnContext(ctx, "user time zone not recorded", "error", err)
		}
	}

	log.InfoContext(ctx, "raw object processed",
		"status", status, "records_in", recordsIn, "records_out", written,
		"duplicates", counts.TotalSkipped(), "failed", recordsFailed,
		"chunked", plan.spilled,
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

// userState reports whether the job's user exists in its tenant and is
// active, and the user's time zone (nil when the user has none, or one this
// host cannot load). forShare locks the row until the transaction ends.
func userState(ctx context.Context, db queryRower, sc scope, forShare bool) (bool, *time.Location, error) {
	q := `SELECT status, coalesce(timezone, '') FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2`
	if forShare {
		q += ` FOR SHARE`
	}
	var status, zone string
	err := db.QueryRow(ctx, q, sc.tenant, sc.user).Scan(&status, &zone)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("look up user: %w", err)
	}
	loc, _ := loadZone(zone)
	return status == "active", loc, nil
}

// userActive reports whether the job's user exists in its tenant and is
// active. forShare locks the row until the transaction ends.
func userActive(ctx context.Context, db queryRower, sc scope, forShare bool) (bool, error) {
	active, _, err := userState(ctx, db, sc, forShare)
	return active, err
}

// rawObjectInfo loads the data type registered for the raw object and when
// the object was captured: when the pull that stored it started fetching, or,
// for an upload or a notification body, when it was received. One raw object
// holds one data type.
func rawObjectInfo(ctx context.Context, db queryRower, sc scope, key string) (dataType string, captured time.Time, err error) {
	err = db.QueryRow(ctx, `SELECT data_type, coalesce(captured_at, created_at) FROM ohs_sync.raw_objects
		WHERE tenant_id = $1 AND user_id = $2 AND key = $3`, sc.tenant, sc.user, key).Scan(&dataType, &captured)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, fmt.Errorf("raw object %s is not registered", key)
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("look up raw object: %w", err)
	}
	return dataType, captured, nil
}

// rawRegistered reports whether the raw object is still registered.
func rawRegistered(ctx context.Context, db queryRower, sc scope, key string) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ohs_sync.raw_objects
		WHERE tenant_id = $1 AND user_id = $2 AND key = $3)`, sc.tenant, sc.user, key).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("look up raw object: %w", err)
	}
	return ok, nil
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
// The day is the record's own day in its "timezone" metadata, else in the
// user's zone, else in UTC (scope.location).
type coverageBuilder struct {
	today  time.Time
	counts map[string]map[time.Time]int
	// zone is the user's zone, nil when the user has none.
	zone *time.Location
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
		day = dateOnly(ts.In(scope{zone: b.zone}.location(md)))
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
