// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package automation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

func TestOrphanRawObjectGetsJob(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	std := e.stored("withings", "2026-10-01")
	hf := e.stored("apple_health", "2026-10-01")
	parked := e.stored("withings", "2026-10-02")
	done := e.stored("withings", "2026-10-03")
	pending := e.stored("withings", "2026-10-04")
	e.job(parked, "parked")
	e.job(done, "done")
	e.job(pending, "pending")
	ancient := e.file("withings", "2026-01-05")
	e.register("withings", ancient, 30*24*time.Hour) // older than the scan window
	fresh := e.file("withings", "2026-10-05")
	e.register("withings", fresh, time.Second) // inside the grace period

	m := &Metrics{}
	s := &OrphanScanner{Pool: e.pool, Metrics: m}
	res, err := s.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, res.Enqueued)

	require.Equal(t, 1, e.jobs(std))
	require.Equal(t, 1, e.jobs(hf))
	require.Equal(t, 1, e.jobs(parked), "a parked job is not retried")
	require.Equal(t, 1, e.jobs(done))
	require.Equal(t, 1, e.jobs(pending))
	require.Equal(t, 0, e.jobs(fresh))
	require.Equal(t, 0, e.jobs(ancient))
	require.Equal(t, "standard", tier(t, e, std))
	require.Equal(t, "high_freq", tier(t, e, hf))
	require.EqualValues(t, 2, m.OrphansEnqueued.Load())

	// A second pass finds nothing new.
	res, err = s.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, res.Enqueued)
	require.Equal(t, 1, e.jobs(std))
}

func tier(t *testing.T, e *env, key string) string {
	var s string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT tier FROM ohs_sync.etl_jobs WHERE key = $1`, key).Scan(&s))
	return s
}

func TestOrphanScannerManyRowsPaged(t *testing.T) {
	e := newEnv(t)
	for i := range orphanBatch + 20 {
		e.exec(`INSERT INTO ohs_sync.raw_objects
			(tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, created_at)
			VALUES ($1, $2, 'withings', $3, $3, 1, 'b', 0, now() - interval '1 hour')`,
			e.tenant, e.user, fmt.Sprintf("raw/k/%d", i))
	}
	res, err := (&OrphanScanner{Pool: e.pool}).Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, orphanBatch+20, res.Enqueued)
}

func TestUnregisteredFileIsCountedNotQueued(t *testing.T) {
	e := newEnv(t)
	loose := e.file("withings", "2026-10-01")
	e.stored("withings", "2026-10-01")

	m := &Metrics{}
	n, err := (&FileScanner{Pool: e.pool, Store: e.rs, Metrics: m}).Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.EqualValues(t, 1, m.UnregisteredFiles.Load())
	require.Equal(t, 0, e.jobs(loose))
	require.Equal(t, 0, e.scalar(`SELECT count(*) FROM ohs_sync.raw_objects WHERE key = $1`, loose))
}

func TestReconcilerCounts(t *testing.T) {
	e := newEnv(t)
	// Two hours old, well past StaleAfter (one hour). At exactly one hour
	// the row's created_at comes from the database clock and the bound
	// from the Go clock: with the database 60 ms ahead (colima) the row
	// fell just inside the bound and was not counted.
	old := e.file("withings", "2026-10-01")
	e.register("withings", old, StaleAfter+time.Hour)
	e.job(old, "parked")
	finished := e.stored("withings", "2026-10-01")
	e.job(finished, "done")
	e.failed(old, "mapping", time.Hour)
	e.failed(old, "mapping", time.Hour)
	e.exec(`UPDATE ohs_data.failed_records SET status = 'recovered' WHERE id = (SELECT min(id) FROM ohs_data.failed_records)`)
	// Outside the window: not counted.
	ancient := e.file("withings", "2026-01-01")
	e.register("withings", ancient, 30*24*time.Hour)

	m := &Metrics{}
	rep, err := (&Reconciler{Pool: e.pool, Metrics: m}).Run(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, rep.PendingFailedRecords)
	require.EqualValues(t, 1, rep.ParkedJobs)
	require.EqualValues(t, 1, rep.UnprocessedObjects)
	require.EqualValues(t, 1, m.Snapshot()["parked_jobs"])
}

func TestDrainerReplaysOnceAndToleratesDuplicates(t *testing.T) {
	e := newEnv(t)
	// Since is read from the database clock: the rows below are stamped by
	// it, and a host clock a few milliseconds ahead of the database (colima)
	// would put a "fresh" row before Since.
	var since time.Time
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&since))
	key := e.stored("withings", "2026-10-01")
	for range 3 { // duplicates from earlier replays
		e.failed(key, "mapping", time.Hour)
	}
	e.failed(key, "parse", time.Hour)
	e.failed(key, "validation", time.Hour)

	m := &Metrics{}
	d := &Drainer{Pool: e.pool, Metrics: m, Since: since}
	res, err := d.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, res.Replayed)
	require.Equal(t, 1, e.jobs(key), "one job however many duplicate rows")
	require.Equal(t, 3, e.failedStatus(key, "recovered"))
	require.Equal(t, 2, e.failedStatus(key, "permanent"))
	require.Equal(t, 0, e.failedStatus(key, "pending"))
	require.EqualValues(t, 1, m.DrainerReplays.Load())

	// The replay fails again and writes a fresh row, after Since: it waits.
	e.failed(key, "mapping", 0)
	res, err = d.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, res.Replayed)
	require.Equal(t, 1, res.AwaitingRestart)
	require.Equal(t, 1, e.jobs(key))
	require.Equal(t, 1, e.failedStatus(key, "pending"))

	// The first replay finished; a restart (later Since) replays it again.
	e.exec(`UPDATE ohs_sync.etl_jobs SET status = 'done' WHERE key = $1`, key)
	d.Since = time.Now().Add(time.Minute)
	res, err = d.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, res.Replayed)
	require.Equal(t, 2, e.jobs(key))
}

func TestDrainerPermanentOnlyNeverReplays(t *testing.T) {
	e := newEnv(t)
	key := e.stored("withings", "2026-10-01")
	e.failed(key, "parse", time.Hour)
	e.failed(key, "validation", 0) // fresh, still terminal

	res, err := (&Drainer{Pool: e.pool, Since: time.Now().Add(time.Minute)}).Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, res.Replayed)
	require.Equal(t, 0, e.jobs(key))
	require.Equal(t, 2, e.failedStatus(key, "permanent"))
}

func TestDrainerFreshPermanentRowDoesNotHoldBackOldRecoverable(t *testing.T) {
	e := newEnv(t)
	key := e.stored("withings", "2026-10-01")
	e.failed(key, "mapping", time.Hour)
	e.failed(key, "validation", 0) // newer than Since, but terminal

	res, err := (&Drainer{Pool: e.pool, Since: time.Now().Add(-time.Minute)}).Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, res.Replayed)
	require.Equal(t, 0, res.AwaitingRestart)
	require.Equal(t, 1, e.jobs(key))
	require.Equal(t, 1, e.failedStatus(key, "recovered"))
	require.Equal(t, 1, e.failedStatus(key, "permanent"))
}

func TestDrainerSkipsNewJobWhenOneIsLive(t *testing.T) {
	e := newEnv(t)
	key := e.stored("withings", "2026-10-01")
	e.job(key, "pending")
	e.failed(key, "unknown", time.Hour)

	_, err := (&Drainer{Pool: e.pool, Since: time.Now()}).Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, e.jobs(key))
	require.Equal(t, 1, e.failedStatus(key, "recovered"))
}

func TestDrainerGoneRawObjectIsPermanent(t *testing.T) {
	e := newEnv(t)
	key := e.file("withings", "2026-10-01") // never registered
	e.failed(key, "mapping", time.Hour)

	_, err := (&Drainer{Pool: e.pool, Since: time.Now()}).Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, e.jobs(key))
	require.Equal(t, 1, e.failedStatus(key, "permanent"))
}

func TestRetentionDeletesOldDataAndFilesPerUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	oldKey := e.stored("withings", "2026-06-01")
	newKey := e.stored("withings", "2026-10-09")
	e.job(oldKey, "done")
	e.job(newKey, "done")
	insertSample := func(tenant, user any, ts time.Time, h string) {
		e.exec(`INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, quantity_type, value, start_date, content_hash)
			VALUES ($1, $2, 'withings', 'heart_rate', 60, $3, $4)`, tenant, user, ts, h)
	}
	insertSample(e.tenant, e.user, now.AddDate(0, 0, -200), "old")
	insertSample(e.tenant, e.user, now.AddDate(0, 0, -1), "new")
	e.exec(`INSERT INTO ohs_data.daily_summaries (tenant_id, user_id, provider, summary_date, content_hash)
		VALUES ($1, $2, 'withings', '2026-01-01', 'd-old'), ($1, $2, 'withings', '2026-10-09', 'd-new')`, e.tenant, e.user)

	e.exec(`INSERT INTO ohs_data.workout_tracks (tenant_id, user_id, workout_id, length_m, point_count, started_at, ended_at)
		VALUES ($1, $2, 1, 1, 1, $3, $3), ($1, $2, 2, 1, 1, $4, $4)`,
		e.tenant, e.user, now.AddDate(0, 0, -200), now.AddDate(0, 0, -1))
	// Series blobs: one whose last point is past the cutoff goes whole; one
	// that started before the cutoff and ends after it stays whole.
	e.exec(`INSERT INTO ohs_data.series_blobs (tenant_id, user_id, kind, provider, parent, format, start_at, end_at, point_count, points)
		VALUES ($1, $2, 3, 'withings', 'old', 1, $3, $3, 1, '\x01'), ($1, $2, 3, 'withings', 'across', 1, $3, $4, 2, '\x01')`,
		e.tenant, e.user, now.AddDate(0, 0, -200), now.AddDate(0, 0, -1))

	// A second user (other tenant): old data and old files go, recent data and files stay.
	t2 := e.newTenant()
	u2 := e.newUser(t2)
	insertSample(t2, u2, now.AddDate(0, 0, -200), "old2")
	insertSample(t2, u2, now.AddDate(0, 0, -2), "new2")
	oldKey2 := rawstore.Key(t2, u2, "withings", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), "b", 0)
	newKey2 := rawstore.Key(t2, u2, "withings", time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), "b", 0)
	for i, k := range []string{oldKey2, newKey2} {
		require.NoError(t, e.rs.Put(ctx, k, strings.NewReader("x")))
		e.exec(`INSERT INTO ohs_sync.raw_objects (tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part)
			VALUES ($1, $2, 'withings', $3, $4, 1, 'b', 0)`, t2, u2, k, fmt.Sprintf("u2-%d", i))
	}
	// A job whose raw_objects row is already gone (an earlier run died half way).
	e.job(rawstore.Key(e.tenant, e.user, "withings", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), "gone", 0), "done")

	// Zero days is a no-op.
	res, err := (&Retention{Pool: e.pool, Store: e.rs, Days: 0, Now: func() time.Time { return now }}).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, RetentionResult{}, res)
	require.Equal(t, 4, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples`))
	require.Equal(t, 2, e.scalar(`SELECT count(*) FROM ohs_data.workout_tracks`))

	m := &Metrics{}
	res, err = (&Retention{Pool: e.pool, Store: e.rs, Days: 90, Metrics: m, Now: func() time.Time { return now }}).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, res.Prefixes)
	require.EqualValues(t, 5, res.Rows) // 2 samples + 1 summary + 1 track + 1 series
	require.Equal(t, 2, res.Chunks, "the old samples' chunk and the old summary's chunk are dropped whole")
	require.Equal(t, 2, res.Users)

	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples WHERE content_hash = 'new'`))
	require.Equal(t, 2, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples`))
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples WHERE content_hash = 'new2'`))
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.workout_tracks`))
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.series_blobs WHERE parent = 'across'`))
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.series_blobs`))
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.daily_summaries`))
	require.Equal(t, 0, e.scalar(`SELECT count(*) FROM ohs_sync.raw_objects WHERE key = $1`, oldKey))
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_sync.raw_objects WHERE key = $1`, newKey))
	require.Equal(t, 0, e.jobs(oldKey))
	require.Equal(t, 1, e.jobs(newKey))
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_sync.etl_jobs`))
	require.Equal(t, 2, e.scalar(`SELECT count(*) FROM ohs_sync.raw_objects`))
	keys, err := e.rs.List(ctx, "raw/")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{newKey, newKey2}, keys)
	require.EqualValues(t, 5, m.RetentionRows.Load())

	// Deleting row by row reports the same rows as dropping chunks.
	insertSample(e.tenant, e.user, now.AddDate(0, 0, -200), "old-again")
	insertSample(t2, u2, now.AddDate(0, 0, -300), "older-again")
	res, err = (&Retention{Pool: e.pool, Store: e.rs, Days: 90, KeepChunks: true, Now: func() time.Time { return now }}).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, RetentionResult{Rows: 2}, res)
	insertSample(e.tenant, e.user, now.AddDate(0, 0, -200), "old-again")
	insertSample(t2, u2, now.AddDate(0, 0, -300), "older-again")
	res, err = (&Retention{Pool: e.pool, Store: e.rs, Days: 90, Now: func() time.Time { return now }}).Run(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, res.Rows)
	require.Equal(t, 2, res.Chunks)
	require.Equal(t, 2, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples`))
}

// The rows are deleted in batches, each its own transaction: one chunk of a
// hypertable at a time, a bounded number of rows of a plain table at a time.
// A delete that fails half way keeps what the earlier batches removed, and
// the count says so.
func TestRetentionDeletesRowsInBatches(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	bound := now.AddDate(0, 0, -90)
	old := retentionDeleteBatch
	retentionDeleteBatch = 2
	t.Cleanup(func() { retentionDeleteBatch = old })

	// A plain table: five old rows go in three batches.
	for i, days := range []int{-300, -250, -200, -150, -100, -1} {
		e.exec(`INSERT INTO ohs_data.workout_tracks (tenant_id, user_id, workout_id, length_m, point_count, started_at, ended_at)
			VALUES ($1, $2, $3, 1, 1, $4, $4)`, e.tenant, e.user, i+1, now.AddDate(0, 0, days))
	}
	n, err := pruneTable(ctx, e.pool, "workout_tracks", "started_at", false, bound, false)
	require.NoError(t, err)
	require.EqualValues(t, 5, n)
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.workout_tracks`))

	// A hypertable: three old chunks, and the delete of the newest of them fails.
	for hash, days := range map[string]int{"a": -300, "b": -200, "boom": -100, "new": -1} {
		e.exec(`INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, quantity_type, value, start_date, content_hash)
			VALUES ($1, $2, 'withings', 'heart_rate', 60, $3, $4)`, e.tenant, e.user, now.AddDate(0, 0, days), hash)
	}
	e.exec(`CREATE FUNCTION ohs_data.test_refuse_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$`)
	e.exec(`CREATE TRIGGER test_refuse BEFORE DELETE ON ohs_data.quantity_samples
		FOR EACH ROW WHEN (OLD.content_hash = 'boom') EXECUTE FUNCTION ohs_data.test_refuse_delete()`)
	n, err = pruneTable(ctx, e.pool, "quantity_samples", "start_date", false, bound, true)
	require.ErrorContains(t, err, "refused")
	require.EqualValues(t, 2, n, "the chunks deleted before the failure are counted")
	require.Equal(t, 2, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples`), "and stay deleted")

	e.exec(`DROP TRIGGER test_refuse ON ohs_data.quantity_samples`)
	n, err = pruneTable(ctx, e.pool, "quantity_samples", "start_date", false, bound, true)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples WHERE content_hash = 'new'`))

	// A date column: whole days before the cutoff day, chunk by chunk.
	e.exec(`INSERT INTO ohs_data.daily_summaries (tenant_id, user_id, provider, summary_date, content_hash)
		VALUES ($1, $2, 'withings', '2026-01-01', 'd1'), ($1, $2, 'withings', '2026-07-11', 'd2'),
		       ($1, $2, 'withings', '2026-07-12', 'd3')`, e.tenant, e.user)
	n, err = pruneTable(ctx, e.pool, "daily_summaries", "summary_date", true, time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC), true)
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.daily_summaries WHERE content_hash = 'd3'`))
}

// The record aliases of HealthKit samples (one per sample) used to stay after
// retention removed their rows: on an instance with OHS_RETENTION_DAYS set the
// table only ever grew.
func TestRetentionPrunesRecordAliases(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, a := range []struct {
		id string
		at time.Time
	}{{"OLD-UUID", now.AddDate(0, 0, -200)}, {"NEW-UUID", now.AddDate(0, 0, -1)}} {
		e.exec(`INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, quantity_type, value, start_date, content_hash, external_id)
			VALUES ($1, $2, 'apple_health', 'heart_rate', 60, $3, $4, $4)`, e.tenant, e.user, a.at, a.id)
		e.exec(`INSERT INTO ohs_data.record_aliases (tenant_id, user_id, provider, external_id, record_table, content_hash, start_time)
			VALUES ($1, $2, 'apple_health', $3, 'quantity_samples', $3, $4)`, e.tenant, e.user, a.id, a.at)
	}
	_, err := (&Retention{Pool: e.pool, Store: e.rs, Days: 90, Now: func() time.Time { return now }}).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, e.scalar(`SELECT count(*) FROM ohs_data.record_aliases WHERE external_id = 'OLD-UUID'`))
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.record_aliases WHERE external_id = 'NEW-UUID'`))
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples`))
}

// The sealed hourly buckets go by their hour, cut on the same whole hour as
// the samples they count: a bucket goes exactly when all of its samples do.
func TestRetentionPrunesHourlyBuckets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 40, 0, 0, time.UTC)
	cutHour := now.AddDate(0, 0, -90).Truncate(time.Hour)
	for i, at := range []time.Time{cutHour.Add(-time.Hour), cutHour} {
		e.exec(`INSERT INTO ohs_data.quantity_hourly_sealed (tenant_id, user_id, quantity_type, provider, source_key, slug, worn, unit, bucket, sealed)
			VALUES ($1, $2, 'heart_rate', 'withings', '\x01', 'withings', false, 'bpm', $3, '\x02')`, e.tenant, e.user, at)
		// One sample at the end of each hour, after the cutoff minute of
		// the day.
		e.exec(`INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, quantity_type, value, start_date, content_hash)
			VALUES ($1, $2, 'withings', 'heart_rate', 60, $3, $4)`, e.tenant, e.user, at.Add(50*time.Minute), fmt.Sprintf("h%d", i))
	}
	_, err := (&Retention{Pool: e.pool, Store: e.rs, Days: 90, Now: func() time.Time { return now }}).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.quantity_hourly_sealed WHERE bucket = $1`, cutHour))
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.quantity_hourly_sealed`))
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples WHERE content_hash = 'h1'`), "the samples of the kept hour stay")
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples`))
}

// Every ohs_data table that holds a user's rows is pruned by retention or
// kept on purpose (retentionKept): a new table cannot be forgotten.
func TestRetentionCoversEveryUserTable(t *testing.T) {
	e := newEnv(t)
	rows, err := e.pool.Query(context.Background(), `
		SELECT table_name FROM information_schema.columns
		WHERE table_schema = 'ohs_data' AND column_name = 'user_id'
		  AND table_name IN (SELECT table_name FROM information_schema.tables WHERE table_schema = 'ohs_data' AND table_type = 'BASE TABLE')`)
	require.NoError(t, err)
	var tables []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		tables = append(tables, n)
	}
	require.NoError(t, rows.Err())
	known := map[string]bool{}
	for _, t := range retentionTables {
		known[t.table] = true
	}
	for _, k := range retentionKept {
		known[k] = true
	}
	for _, n := range tables {
		require.True(t, known[n], "ohs_data.%s has a user_id but is neither in retentionTables nor in retentionKept", n)
	}
}

func TestStartRegistersRetentionOnlyWhenEnabled(t *testing.T) {
	e := newEnv(t)
	names := func(days int) []string {
		s, err := newScheduler(Deps{Pool: e.pool, Store: e.rs, RetentionDays: days})
		require.NoError(t, err)
		var out []string
		for _, tk := range s.tasks {
			out = append(out, tk.name)
		}
		return out
	}
	require.Equal(t, []string{"reconciler", "orphans", "drainer", "idempotency", "shares", "files", "tracking"}, names(0))
	require.Equal(t, []string{"reconciler", "orphans", "drainer", "idempotency", "shares", "files", "tracking", "retention"}, names(30))
	withHB, err := newScheduler(Deps{Pool: e.pool, Store: e.rs, RetentionDays: 30,
		PruneHeartbeats: func(context.Context) (int64, error) { return 0, nil }})
	require.NoError(t, err)
	var hbNames []string
	hbEvery := time.Duration(0)
	for _, tk := range withHB.tasks {
		hbNames = append(hbNames, tk.name)
		if tk.name == "heartbeats" {
			hbEvery = tk.interval
		}
	}
	require.Equal(t, []string{"reconciler", "orphans", "drainer", "idempotency", "shares", "files", "tracking", "heartbeats", "retention"}, hbNames)
	require.Equal(t, 24*time.Hour, hbEvery)
	off, err := newScheduler(Deps{Pool: e.pool, Store: e.rs, TrackingRetentionDays: -1})
	require.NoError(t, err)
	for _, tk := range off.tasks {
		require.NotEqual(t, "tracking", tk.name, "a negative TrackingRetentionDays keeps the tracking rows")
	}

	s, err := newScheduler(Deps{Pool: e.pool, Store: e.rs, RetentionDays: 30})
	require.NoError(t, err)
	got := map[string]time.Duration{}
	for _, tk := range s.tasks {
		got[tk.name] = tk.interval
	}
	require.Equal(t, 5*time.Minute, got["reconciler"])
	require.Equal(t, 10*time.Minute, got["orphans"])
	require.Equal(t, 15*time.Minute, got["drainer"])
	require.Equal(t, 24*time.Hour, got["files"])
	require.Equal(t, 24*time.Hour, got["retention"])
	require.Equal(t, time.Hour, got["idempotency"])
	require.Equal(t, time.Hour, got["shares"])
}

func TestStartRunsJobsAndStopsOnCancel(t *testing.T) {
	e := newEnv(t)
	key := e.stored("withings", "2026-10-01")
	m := &Metrics{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Start(ctx, Deps{
			Pool: e.pool, Store: e.rs, Metrics: m, NoJitter: true,
			Intervals: Intervals{Reconcile: 20 * time.Millisecond, Orphans: 20 * time.Millisecond, Drain: 20 * time.Millisecond},
		})
	}()
	require.Eventually(t, func() bool { return e.jobs(key) == 1 && m.LastReconcileUnix.Load() > 0 },
		10*time.Second, 20*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
	require.Equal(t, 1, e.jobs(key), "repeated passes never duplicate the job")
	require.EqualValues(t, 0, m.RunFailures.Load())
}

func TestStartNeedsDeps(t *testing.T) {
	require.Error(t, Start(context.Background(), Deps{}))
}

// A user being deleted gets no new ETL job from the orphan scanner or the
// drainer: the deletion worker would have nothing left to sweep it with.
func TestOrphansAndDrainerSkipUserBeingDeleted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	orphan := e.stored("withings", "2026-10-01")
	failedKey := e.stored("withings", "2026-10-02")
	e.job(failedKey, "done")
	e.failed(failedKey, "mapping", time.Hour)
	e.exec(`UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, e.user)

	orph, err := (&OrphanScanner{Pool: e.pool}).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, orph.Enqueued)
	require.Equal(t, 0, e.jobs(orphan))

	res, err := (&Drainer{Pool: e.pool, Since: time.Now().Add(time.Minute)}).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, res.Replayed)
	require.Equal(t, 1, e.jobs(failedKey), "no replay job beside the finished one")
}
