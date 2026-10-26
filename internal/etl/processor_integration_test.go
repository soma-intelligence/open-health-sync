// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/consent"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

func recent() time.Time { return time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second) }

func TestProcess_HappyPathWritesAndDedups(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	at := recent()
	job := e.upload(e.tenant, e.user, "apple_health", "heart_rate",
		q("heart_rate", 62, "bpm", at),
		q("heart_rate", 64, "count/min", at.Add(time.Minute)),
		q("heart_rate", 66, "bpm", at.Add(2*time.Minute)),
	)

	c := e.process(job)
	assert.Equal(t, TableCount{Written: 3}, c["quantity_samples"])
	assert.Equal(t, 3, e.count("quantity_samples"))

	var tenantRows int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND unit = 'bpm' AND source_slug = 'apple_health'`,
		e.tenant).Scan(&tenantRows))
	assert.Equal(t, 3, tenantRows, "tenant_id stamped, units canonicalized, attribution filled")

	// Reprocessing the same object is idempotent.
	c = e.process(job)
	assert.Equal(t, TableCount{Skipped: 3}, c["quantity_samples"])
	assert.Equal(t, 3, e.count("quantity_samples"))
}

func TestProcess_MappingAndParseFailuresGoToFailedRecords(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	at := recent()
	job := e.upload(e.tenant, e.user, "apple_health", "heart_rate",
		q("heart_rate", 62, "bpm", at),
		`{not json`,
		line(map[string]any{"fail": "bad record"}),
	)

	c := e.process(job)
	assert.Equal(t, 1, c["quantity_samples"].Written)

	rows, err := e.pool.Query(context.Background(), `
		SELECT reason_code, line_number, status, raw_key FROM ohs_data.failed_records
		WHERE tenant_id = $1 AND user_id = $2 ORDER BY id`, e.tenant, e.user)
	require.NoError(t, err)
	defer rows.Close()
	var reasons []string
	for rows.Next() {
		var reason, status, key string
		var ln *int
		require.NoError(t, rows.Scan(&reason, &ln, &status, &key))
		reasons = append(reasons, reason)
		assert.Equal(t, "pending", status)
		assert.Equal(t, job.Key, key)
		require.NotNil(t, ln)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"parse", "mapping"}, reasons)
}

func TestProcess_AllFailedWritesAJobLevelRow(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	job := e.upload(e.tenant, e.user, "apple_health", "heart_rate",
		line(map[string]any{"fail": "x"}), line(map[string]any{"fail": "y"}))

	e.process(job)

	var jobRows int
	require.NoError(t, e.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM ohs_data.failed_records
		WHERE tenant_id = $1 AND user_id = $2 AND reason_code = 'job' AND line_number IS NULL`,
		e.tenant, e.user).Scan(&jobRows))
	assert.Equal(t, 1, jobRows)
	assert.Equal(t, 3, e.count("failed_records"))
}

// An object whose every line fails validation gets no job-level row: the
// drainer replays objects with one at every process start, and replaying
// these only wrote their line failures again (392 k more rows for 11 k such
// objects after one restart in a stress run).
func TestProcess_AllInvalidWritesNoJobRow(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	future := time.Now().Add(30 * 24 * time.Hour)
	job := e.upload(e.tenant, e.user, "apple_health", "heart_rate",
		q("heart_rate", 60, "bpm", future), q("heart_rate", 61, "bpm", future.Add(time.Minute)))

	e.process(job)

	var jobRows, lineRows int
	require.NoError(t, e.pool.QueryRow(context.Background(), `
		SELECT count(*) FILTER (WHERE reason_code = 'job'), count(*) FILTER (WHERE reason_code = 'validation')
		FROM ohs_data.failed_records WHERE tenant_id = $1 AND user_id = $2`,
		e.tenant, e.user).Scan(&jobRows, &lineRows))
	assert.Equal(t, 0, jobRows)
	assert.Equal(t, 2, lineRows)
}

func TestProcess_UnknownProviderIsAnError(t *testing.T) {
	e := newEnv(t)
	job := e.upload(e.tenant, e.user, "nobody", "x", q("heart_rate", 60, "bpm", recent()))
	_, err := ProcessWith(context.Background(), e.pool, e.rs, job, e.opts())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}

func TestProcess_MissingRawObjectIsAnError(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	job := e.upload(e.tenant, e.user, "apple_health", "x", q("heart_rate", 60, "bpm", recent()))
	require.NoError(t, e.rs.DeletePrefix(context.Background(), "raw/"+e.tenant.String()+"/"+e.user.String()+"/"))
	_, err := ProcessWith(context.Background(), e.pool, e.rs, job, e.opts())
	require.Error(t, err)
}

func TestProcess_UserNotActiveCompletesWithoutWriting(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	job := e.upload(e.tenant, e.user, "apple_health", "heart_rate", q("heart_rate", 60, "bpm", recent()))
	e.exec(`UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, e.user)

	c := e.process(job)
	assert.Empty(t, c)
	assert.Equal(t, 0, e.count("quantity_samples"))
	assert.Equal(t, 0, e.count("failed_records"))

	// A user that does not exist at all (or belongs to another tenant) is the same.
	other := e.newTenant()
	job.TenantID = other
	c, err := ProcessWith(context.Background(), e.pool, e.rs, job, e.opts())
	require.NoError(t, err)
	assert.Empty(t, c)
}

func TestProcess_ConnectorCannotWriteForAnotherUser(t *testing.T) {
	e := newEnv(t)
	victim := e.newUser(e.tenant)
	e.register("apple_health", func(r connector.RawRecord) (model.MapResult, error) {
		mr, err := quantityMap(r)
		mr.QuantitySamples[0].UserID = victim.String()
		mr.QuantitySamples[0].TenantID = "00000000-0000-0000-0000-000000000000"
		return mr, err
	})
	e.process(e.upload(e.tenant, e.user, "apple_health", "heart_rate", q("heart_rate", 60, "bpm", recent())))
	assert.Equal(t, 1, e.count("quantity_samples"))
	assert.Equal(t, 0, e.countFor("quantity_samples", e.tenant, victim))
}

func TestProcess_SameContentInTwoTenantsIsTwoRows(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	at := recent()
	t2 := e.newTenant()
	u2 := e.newUser(t2)
	e.process(e.upload(e.tenant, e.user, "apple_health", "heart_rate", q("heart_rate", 60, "bpm", at)))
	c := e.process(e.upload(t2, u2, "apple_health", "heart_rate", q("heart_rate", 60, "bpm", at)))
	assert.Equal(t, 1, c["quantity_samples"].Written)
	assert.Equal(t, 1, e.count("quantity_samples"))
	assert.Equal(t, 1, e.countFor("quantity_samples", t2, u2))
}

// One raw record is not one sample: one out-of-band value must not discard the
// rest of what the record mapped to.
func TestProcess_OneRejectedSampleDoesNotDiscardTheDay(t *testing.T) {
	e := newEnv(t)
	day := recent()
	e.register("dev", func(r connector.RawRecord) (model.MapResult, error) {
		mk := func(h string, v float64) model.QuantitySample {
			return model.QuantitySample{UserID: r.UserID, Provider: "dev", QuantityType: model.QTHeartRate,
				Value: v, Unit: "bpm", StartDate: day, ContentHash: h}
		}
		return model.MapResult{
			QuantitySamples: []model.QuantitySample{mk("a", 72), mk("b", 310), mk("c", 65)},
			DailySummaries:  []model.DailySummary{{UserID: r.UserID, Provider: "dev", SummaryDate: day.Truncate(24 * time.Hour), ContentHash: "d"}},
		}, nil
	})
	c := e.process(e.upload(e.tenant, e.user, "dev", "daily", `{"type":"daily"}`))
	assert.Equal(t, 2, c["quantity_samples"].Written, "only the out-of-band sample is dropped")
	assert.Equal(t, 1, c["daily_summaries"].Written, "the day's summary survives one bad sample")

	var reason string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT reason_code FROM ohs_data.failed_records WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user).Scan(&reason))
	assert.Equal(t, "validation", reason)
}

// A reject on the parent itself cannot be scoped: the whole record is parked.
func TestProcess_StructuralRejectStillDiscardsTheRecord(t *testing.T) {
	e := newEnv(t)
	start := recent()
	e.register("dev", func(r connector.RawRecord) (model.MapResult, error) {
		return model.MapResult{
			SleepSessions: []model.SleepSession{{UserID: r.UserID, Provider: "dev", BedtimeStart: start, BedtimeEnd: start.Add(-time.Hour), ContentHash: "s1"}},
			SleepPhases:   []model.SleepPhase{{UserID: r.UserID, Phase: "deep", StartDate: start, EndDate: start.Add(time.Hour), DurationS: 3600, ParentContentHash: "s1"}},
		}, nil
	})
	e.process(e.upload(e.tenant, e.user, "dev", "sleep", `{"type":"sleep"}`))
	assert.Equal(t, 0, e.count("sleep_sessions"))
	assert.Equal(t, 0, e.count("sleep_phases"))
	assert.Equal(t, 1, e.count("failed_records"), "the validation row; no job row, a replay would fail the same way")
}

func TestProcess_SuspectValuesAreWrittenAndFlagged(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	e.process(e.upload(e.tenant, e.user, "apple_health", "heart_rate", q("heart_rate", 250, "bpm", recent())))
	var flag string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT metadata->>'quality_flag' FROM ohs_data.quantity_samples WHERE tenant_id = $1`, e.tenant).Scan(&flag))
	assert.Equal(t, "suspect", flag)
}

func TestProcess_UnconvertibleUnitFailsAsRecoverable(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	e.process(e.upload(e.tenant, e.user, "apple_health", "body_mass", q("body_mass", 70, "furlongs", recent())))
	assert.Equal(t, 0, e.count("quantity_samples"))
	var reason string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT reason_code FROM ohs_data.failed_records WHERE tenant_id = $1 AND line_number = 1`, e.tenant).Scan(&reason))
	assert.Equal(t, "unit", reason)
}

// One sample with a unit that cannot be converted fails on its own: the rest
// of the record is written, and the record is kept for a replay.
func TestProcess_UnconvertibleUnitDropsOnlyItsSample(t *testing.T) {
	e := newEnv(t)
	at := recent()
	e.register("dev", func(r connector.RawRecord) (model.MapResult, error) {
		mk := func(h, qt string, v float64, unit string) model.QuantitySample {
			return model.QuantitySample{UserID: r.UserID, Provider: "dev", QuantityType: qt,
				Value: v, Unit: unit, StartDate: at, ContentHash: h}
		}
		return model.MapResult{QuantitySamples: []model.QuantitySample{
			mk("a", model.QTHeartRate, 61, "bpm"), mk("b", "body_mass", 70, "furlongs"), mk("c", model.QTHeartRate, 64, "bpm")}}, nil
	})
	c := e.process(e.upload(e.tenant, e.user, "dev", "day", `{"type":"day"}`))
	assert.Equal(t, 2, c["quantity_samples"].Written)
	var reason string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT reason_code FROM ohs_data.failed_records WHERE tenant_id = $1 AND line_number = 1`, e.tenant).Scan(&reason))
	assert.Equal(t, "unit", reason)
}

// A bad figure of a night is dropped from the session, which is written with
// its other figures and its phases, and says what it lost.
func TestProcess_BadSleepFigureDropsOnlyTheFigure(t *testing.T) {
	e := newEnv(t)
	start := recent().Add(-9 * time.Hour)
	spo2, hr := 0.95, 52.0
	e.register("dev", func(r connector.RawRecord) (model.MapResult, error) {
		return model.MapResult{
			SleepSessions: []model.SleepSession{{UserID: r.UserID, Provider: "dev", BedtimeStart: start, BedtimeEnd: start.Add(8 * time.Hour),
				SpO2Avg: &spo2, RestingHR: &hr, ContentHash: "s1"}},
			SleepPhases: []model.SleepPhase{{UserID: r.UserID, Phase: "deep", StartDate: start, EndDate: start.Add(time.Hour), DurationS: 3600, ParentContentHash: "s1"}},
		}, nil
	})
	e.process(e.upload(e.tenant, e.user, "dev", "sleep", `{"type":"sleep"}`))
	assert.Equal(t, 1, e.count("sleep_phases"))
	var gotSpO2, gotHR *float64
	var dropped string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT spo2_avg, resting_hr, metadata->'quality_dropped'->>0 FROM ohs_data.sleep_sessions WHERE tenant_id = $1`, e.tenant).
		Scan(&gotSpO2, &gotHR, &dropped))
	assert.Nil(t, gotSpO2)
	require.NotNil(t, gotHR)
	assert.Equal(t, 52.0, *gotHR)
	assert.Equal(t, "spo2_avg", dropped)
	var reason string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT reason_code FROM ohs_data.failed_records WHERE tenant_id = $1 AND line_number = 1`, e.tenant).Scan(&reason))
	assert.Equal(t, "validation", reason)
}

// A record the connector maps to nothing (an unknown HealthKit type) is
// skipped: not stored anywhere, and not a failure either.
func TestProcess_UnmappedRecordIsSkipped(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	at := recent()
	c := e.process(e.upload(e.tenant, e.user, "apple_health", "x", line(map[string]any{
		"empty": true, "quantity_type": "someNewMetric", "value": 3.5, "unit": "u",
		"start_date": at.Format(time.RFC3339),
	})))
	assert.Equal(t, 0, c["observations"].Written)
	assert.Equal(t, 0, e.count("observations"))
	assert.Equal(t, 0, e.count("failed_records"))
}

func TestProcess_HeartbeatSeriesTakesTheDataType(t *testing.T) {
	e := newEnv(t)
	at := recent()
	e.register("apple_health", func(r connector.RawRecord) (model.MapResult, error) {
		return model.MapResult{HeartbeatSamples: []model.HeartbeatSample{{UserID: r.UserID, Timestamp: at, RRMS: 812}}}, nil
	})
	e.process(e.upload(e.tenant, e.user, "apple_health", "heartbeat_series", `{"x":1}`))
	var series, provider string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT series_id, provider FROM ohs_data.heartbeat_samples WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user).Scan(&series, &provider))
	assert.Equal(t, "heartbeat_series", series)
	assert.Equal(t, "apple_health", provider)
}

func TestProcess_ConnectorSeesTheRawRecord(t *testing.T) {
	e := newEnv(t)
	var got []connector.RawRecord
	e.register("apple_health", func(r connector.RawRecord) (model.MapResult, error) {
		got = append(got, r)
		return model.MapResult{}, nil
	})
	job := e.upload(e.tenant, e.user, "apple_health", "step_count", `{"a":1}`, `{"a":2}`)
	e.process(job)
	require.Len(t, got, 2)
	assert.Equal(t, connector.RawRecord{
		TenantID: e.tenant.String(), UserID: e.user.String(), Provider: "apple_health",
		DataType: "step_count", SourceKey: job.Key, Fields: map[string]any{"a": 1.0},
	}, got[0])
}

func TestProcess_SyncLogAndCoverage(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	job := e.upload(e.tenant, e.user, "apple_health", "heart_rate",
		q("heart_rate", 62, "bpm", at), q("heart_rate", 63, "bpm", at.Add(time.Minute)),
		line(map[string]any{"fail": "x", "type": "heart_rate"}))
	e.process(job)

	var status string
	var written, failed int
	require.NoError(t, e.pool.QueryRow(context.Background(), `
		SELECT status, records_written, records_failed FROM ohs_sync.sync_log
		WHERE tenant_id = $1 AND user_id = $2 AND stage = 'etl' AND raw_key = $3 AND metric_type = 'all'`,
		e.tenant, e.user, job.Key).Scan(&status, &written, &failed))
	assert.Equal(t, "partial", status)
	assert.Equal(t, 2, written)
	assert.Equal(t, 1, failed)

	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), `
		SELECT records_count FROM ohs_sync.pull_coverage
		WHERE tenant_id = $1 AND user_id = $2 AND provider = 'apple_health' AND metric = 'heart_rate' AND date = '2026-09-30'`,
		e.tenant, e.user).Scan(&n))
	assert.Equal(t, 2, n)
}

func TestProcess_StreamErrorIsRetried(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	job := e.upload(e.tenant, e.user, "apple_health", "x", q("heart_rate", 60, "bpm", recent()))
	_, err := ProcessWith(context.Background(), e.pool, brokenStore{e.rs}, job, e.opts())
	require.Error(t, err, "a stream that died mid-read is retried, not kept as a partial")
	assert.Contains(t, err.Error(), "truncated")
	assert.Equal(t, 0, e.count("quantity_samples"))
}

var errBroken = errors.New("connection reset")

// brokenStore serves every object as a stream that fails after its first bytes.
type brokenStore struct{ rawstore.Store }

func (b brokenStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(io.MultiReader(strings.NewReader("{\"empty\":1}\n{\"b\""), errReader{})), nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errBroken }

// A user's consent decides which categories of a source are stored: the
// others are withheld before anything is written. Without a recorded consent
// everything is stored, unless consent is required.
func TestProcess_ConsentWithholdsCategories(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	at := recent()
	upload := func(tag string) queue.ETLJob {
		return e.upload(e.tenant, e.user, "apple_health", tag,
			q("heart_rate", 62, "bpm", at),
			q("step_count", 100, "count", at),
		)
	}

	required := e.opts()
	required.ConsentRequired = true
	c, err := ProcessWith(context.Background(), e.pool, e.rs, upload("required"), required)
	require.NoError(t, err)
	assert.Zero(t, c["quantity_samples"].Written, "consent required and none recorded: nothing stored")
	assert.Equal(t, 0, e.count("failed_records"), "withheld records are not failures")

	tx, err := e.pool.Begin(context.Background())
	require.NoError(t, err)
	_, err = consent.Set(context.Background(), tx, e.tenant, e.user, "apple_health", []string{consent.Activity}, "k", "")
	require.NoError(t, err)
	require.NoError(t, tx.Commit(context.Background()))

	c = e.process(upload("granted"))
	assert.Equal(t, 1, c["quantity_samples"].Written)
	var typ string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT quantity_type FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user).Scan(&typ))
	assert.Equal(t, "step_count", typ, "heart rate withheld, activity stored")
}
