// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// sealTables are the tables `ohs vault seal` converts that these tests fill.
var sealTables = append(append([]string{}, etlWritten...), tTimeTags)

// opened is every row of the env's user, by table, as the tests read it
// (opened): everything but the columns the seal command is meant to change.
func (e *env) opened() map[string]string {
	e.t.Helper()
	out := map[string]string{}
	for _, table := range etlWritten {
		var rows string
		require.NoError(e.t, e.read().QueryRow(context.Background(), `
			SELECT coalesce(jsonb_agg(j ORDER BY j::text), '[]')::text FROM (
			    SELECT to_jsonb(t) - '{sealed,content_hash,content_mac,source_key}'::text[] AS j
			    FROM ohs_data.`+table+` t WHERE t.tenant_id = $1 AND t.user_id = $2) x`, e.tenant, e.user).Scan(&rows), table)
		out[table] = rows
	}
	out[tTimeTags] = e.openedTags()
	return out
}

// openedTags is the time tags of the env's user, opened. The ETL does not
// read this table, so the test opens it itself: a tag is sealed under its id.
func (e *env) openedTags() string {
	e.t.Helper()
	ctx := context.Background()
	uv := e.keys(e.tenant, e.user)
	t := vault.Schema[tTimeTags]
	rows, err := e.pool.Query(ctx, `SELECT id, start_at, sealed, kind, label, timezone, note, source FROM ohs_data.time_tags
		WHERE tenant_id = $1 AND user_id = $2 ORDER BY id`, e.tenant, e.user)
	require.NoError(e.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var (
			id     uuid.UUID
			at     time.Time
			sealed []byte
			clear  [5]*string
		)
		require.NoError(e.t, rows.Scan(&id, &at, &sealed, &clear[0], &clear[1], &clear[2], &clear[3], &clear[4]))
		vals := make([]any, len(clear))
		for i, c := range clear {
			if c != nil {
				vals[i] = *c
			}
		}
		if sealed != nil {
			vals, err = uv.OpenRow(t, id[:], sealed)
			require.NoError(e.t, err)
		}
		out = append(out, fmt.Sprint(id, at.UTC(), vals))
	}
	require.NoError(e.t, rows.Err())
	return strings.Join(out, "\n")
}

// stored is a count read from the tables as they are stored, not opened.
func (e *env) stored(sql string, args ...any) int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

// clearLeft counts, by table, the rows of the env's user with no sealed value
// or with a sensitive column still set.
func (e *env) clearLeft() map[string]int {
	e.t.Helper()
	out := map[string]int{}
	for _, table := range sealTables {
		cond := []string{"sealed IS NULL"}
		for _, c := range vault.Schema[table].Sealed {
			cond = append(cond, c.Name+" IS NOT NULL")
		}
		n := e.stored(`SELECT count(*) FROM ohs_data.`+table+` WHERE tenant_id = $1 AND user_id = $2 AND (`+
			strings.Join(cond, " OR ")+`)`, e.tenant, e.user)
		if n > 0 {
			out[table] = n
		}
	}
	return out
}

// writeClear writes acc as a release without the vault stored it.
func (e *env) writeClear(acc *model.MapResult) {
	e.t.Helper()
	was := e.clear
	e.clear = true
	e.writeAt(time.Time{}, acc)
	e.clear = was
}

// legacyUpload is upload for an object stored before the user had keys.
func (e *env) legacyUpload(provider, dataType string, lines ...string) queue.ETLJob {
	e.t.Helper()
	job := e.upload(e.tenant, e.user, provider, dataType, lines...)
	e.exec(`UPDATE ohs_sync.raw_objects SET created_at = now() - INTERVAL '30 days' WHERE key = $1`, job.Key)
	return job
}

func (e *env) seal(ctx context.Context, o vault.SealOptions) (vault.SealReport, error) {
	o.Rows = Sealer{}
	if o.Users == nil {
		o.Users = []uuid.UUID{e.user}
	}
	return vault.Seal(ctx, e.v, e.pool, e.rs, o)
}

// legacyData fills the env's user with what a release without the vault
// left: clear rows of every family, a failed record, a time tag, samples over
// many hours, and a raw object in plain gzip.
func (e *env) legacyData(at time.Time) queue.ETLJob {
	e.t.Helper()
	e.writeClear(everyFamily(at))
	var samples []model.QuantitySample
	for i := range 60 {
		samples = append(samples, model.QuantitySample{Provider: "dev", QuantityType: "heart_rate", Value: 50 + float64(i), Unit: "bpm",
			StartDate: at.Add(time.Duration(i) * 17 * time.Minute), EndDate: at.Add(time.Duration(i) * 17 * time.Minute),
			SourceName: "Apple Watch", ExternalID: fmt.Sprintf("hr-%d", i), ContentHash: fmt.Sprintf("hr-%d", i)})
	}
	e.writeClear(&model.MapResult{QuantitySamples: samples, AliasExternalIDs: true})
	e.exec(`INSERT INTO ohs_data.failed_records (tenant_id, user_id, provider, raw_key, line_number, raw_payload, reason_code, error_message)
		VALUES ($1, $2, 'dev', 'raw/k', 3, '{"hr": 61}', 'mapping', 'no such field')`, e.tenant, e.user)
	e.exec(`INSERT INTO ohs_data.time_tags (tenant_id, user_id, kind, label, start_at, note)
		VALUES ($1, $2, 'illness', 'flu', $3, 'three days in bed')`, e.tenant, e.user, at)
	return e.legacyUpload("prov", "q", rq("step_count", 12, at.Add(-24*time.Hour)))
}

// A database filled by a release without the vault is sealed whole: nothing
// is left clear, every value reads back as it did, the records are the ones
// the writer would have stored (writing them again adds nothing), the hourly
// buckets are there and the raw object is sealed and still processed.
func TestSealConvertsWhatWasStoredClear(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Hour)
	e.register("prov", quantityMap)
	job := e.legacyData(at)
	before := e.opened()
	require.NotEmpty(t, e.clearLeft())
	st, err := vault.Status(ctx, e.pool)
	require.NoError(t, err)
	require.False(t, st.Sealed())
	require.EqualValues(t, 62, st.Tables[tQuantity])
	require.EqualValues(t, 1, st.Tables[tTimeTags])
	require.EqualValues(t, 1, st.RawObjects)

	dry, err := e.seal(ctx, vault.SealOptions{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, 62, dry.Rows[tQuantity])
	require.NotEmpty(t, e.clearLeft(), "a dry run changes nothing")

	report, err := e.seal(ctx, vault.SealOptions{Batch: 7})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Users)
	assert.Equal(t, 62, report.Rows[tQuantity])
	assert.Equal(t, 1, report.Rows[tPhases], "a parent takes its children along")
	assert.Equal(t, 2, report.Rows[tECGSamples])
	assert.Equal(t, 1, report.Rows[tFailed])
	assert.Equal(t, 1, report.Rows[tTimeTags])
	assert.Equal(t, 1, report.RawObjects)
	assert.Zero(t, report.Duplicates)
	assert.Positive(t, report.Hours)

	require.Empty(t, e.clearLeft())
	st, err = vault.Status(ctx, e.pool)
	require.NoError(t, err)
	require.True(t, st.Sealed(), "%+v", st)
	require.Equal(t, before, e.opened(), "every value reads back as before")
	assert.EqualValues(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples WHERE content_hash = $1`, e.hash("q1")),
		"the content hash is the keyed one")
	assert.EqualValues(t, 60, e.scalar(`SELECT count(*) FROM ohs_data.record_aliases a JOIN ohs_data.quantity_samples q
		ON q.tenant_id = a.tenant_id AND q.user_id = a.user_id AND q.content_hash = a.content_hash AND q.start_date = a.start_time
		WHERE a.user_id = $1`, e.user), "the aliases follow their rows")
	assert.Equal(t, e.expectedBuckets(), e.storedBuckets("Apple Watch", "w1"))
	assert.Positive(t, e.scalar(`SELECT count(*) FROM ohs_core.audit_events WHERE user_id = $1 AND action = 'vault.seal_progress'
		AND detail->>'done' = 'true'`, e.user))

	// The writer finds its own records in what the command sealed.
	counts := map[string]int{}
	for _, table := range sealTables {
		counts[table] = e.count(table)
	}
	e.write(WriterConfig{}, everyFamily(at))
	for _, table := range sealTables {
		assert.Equal(t, counts[table], e.count(table), "%s: a record written again is not stored twice", table)
	}

	// The raw object: sealed under a new key, the old one gone, the job of
	// the old key now names the new one and reads it.
	var key string
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT key FROM ohs_sync.raw_objects WHERE user_id = $1`, e.user).Scan(&key))
	require.NotEqual(t, job.Key, key)
	_, err = e.rs.Get(ctx, job.Key)
	require.ErrorIs(t, err, rawstore.ErrNotFound)
	rc, err := e.rs.Get(ctx, key)
	require.NoError(t, err)
	stored, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(stored, []byte("OHSV")))
	job.Key = key
	c := e.process(job)
	assert.Equal(t, 1, c[tQuantity].Written)

	again, err := e.seal(ctx, vault.SealOptions{})
	require.NoError(t, err)
	assert.Zero(t, again.TotalRows()+again.RawObjects+again.Hours+again.Users, "a second run finds nothing to do")
}

// The command stopped at random points and started again ends where an
// uninterrupted run ends.
func TestSealStoppedAndRestartedConverges(t *testing.T) {
	e := newEnv(t)
	at := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Hour)
	for run := range 20 {
		e.user = e.newUser(e.tenant)
		e.legacyData(at)
		before := e.opened()
		stops := 0
		for {
			ctx, cancel := context.WithCancel(context.Background())
			left := 1 + rand.IntN(12)
			_, err := e.seal(ctx, vault.SealOptions{Batch: 1 + rand.IntN(9), Progress: func(vault.SealReport) {
				if left--; left == 0 {
					cancel()
				}
			}})
			cancel()
			if err == nil {
				break
			}
			require.ErrorIs(t, err, context.Canceled, "run %d", run)
			stops++
			require.Less(t, stops, 500, "the command makes progress between two stops")
		}
		require.Empty(t, e.clearLeft(), "run %d", run)
		require.Equal(t, before, e.opened(), "run %d, stopped %d times", run, stops)
		require.Equal(t, e.expectedBuckets(), e.storedBuckets("Apple Watch", "w1"), "run %d", run)
		require.Zero(t, e.stored(`SELECT count(*) FROM ohs_sync.raw_objects r WHERE r.user_id = $1 AND r.key !~ '/v[0-9]+\.[^/]*$'`, e.user))
	}
}

// A clear row whose keyed hash an older sealed row already carries is the
// same record stored twice: the newer copy goes, and the report says so.
func TestSealRemovesARecordStoredTwice(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	sample := model.QuantitySample{Provider: "dev", QuantityType: "heart_rate", Value: 61, Unit: "bpm", StartDate: at, EndDate: at, ContentHash: "twice"}
	e.write(WriterConfig{}, &model.MapResult{QuantitySamples: []model.QuantitySample{sample}})
	// The clear twin, stored later, as a writer without keys would have.
	e.exec(`INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, quantity_type, value, unit, start_date, end_date, content_hash, metadata)
		VALUES ($1, $2, 'dev', 'heart_rate', 61, 'bpm', $3, $3, 'twice', '{}')`, e.tenant, e.user, at)
	// And the other way round: the clear row is the older one.
	older := sample
	older.ContentHash, older.StartDate, older.EndDate = "older", at.Add(time.Hour), at.Add(time.Hour)
	e.writeClear(&model.MapResult{QuantitySamples: []model.QuantitySample{older}})
	var clearID int64
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT id FROM ohs_data.quantity_samples WHERE content_hash = 'older'`).Scan(&clearID))
	e.exec(`INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, quantity_type, unit, start_date, end_date, content_hash, sealed, ingested_at)
		SELECT tenant_id, user_id, provider, quantity_type, unit, $3, $3, $2, sealed, now() + INTERVAL '1 hour'
		FROM ohs_data.quantity_samples WHERE content_hash = $1`, e.hash("twice"), e.hash("older"), at.Add(time.Hour))

	report, err := e.seal(ctx, vault.SealOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, report.Duplicates)
	assert.Equal(t, 1, report.Rows[tQuantity])
	assert.Equal(t, 2, e.count(tQuantity))
	assert.Empty(t, e.clearLeft())
	var id int64
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT id FROM ohs_data.quantity_samples WHERE content_hash = $1`, e.hash("older")).Scan(&id))
	assert.Equal(t, clearID, id, "the older copy is the one that stays")
}

// oldVersionRows counts, by table, the rows of the env's user sealed under
// another key version than newest.
func (e *env) oldVersionRows(newest uint32) map[string]int {
	e.t.Helper()
	out := map[string]int{}
	for _, table := range append(append([]string{}, sealTables...), vault.HourlyTable) {
		n := e.stored(`SELECT count(*) FROM ohs_data.`+table+` WHERE tenant_id = $1 AND user_id = $2
			AND substring(sealed from 2 for 4) <> $3`, e.tenant, e.user, binary.BigEndian.AppendUint32(nil, newest))
		if n > 0 {
			out[table] = n
		}
	}
	return out
}

// After a user key rotation the rows of both versions read, a record ingested
// again is still recognized, and Reseal leaves only the new version and
// retires the old one (plan review focus 2, below the read layer).
func TestRotateUserThenReseal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Hour)
	e.register("prov", quantityMap)
	e.write(WriterConfig{}, everyFamily(at))
	e.exec(`INSERT INTO ohs_data.time_tags (tenant_id, user_id, kind, start_at) VALUES ($1, $2, 'travel', $3)`, e.tenant, e.user, at)
	_, err := e.seal(ctx, vault.SealOptions{})
	require.NoError(t, err)
	job := e.uploadSealed(e.v, e.user, "prov", "q", rq("step_count", 12, at.Add(-24*time.Hour)))
	e.process(job)
	before := e.opened()
	buckets := e.storedBuckets("Apple Watch", "w1")
	require.Empty(t, e.oldVersionRows(1))

	version, err := vault.RotateUser(ctx, e.v, e.pool, e.tenant, e.user)
	require.NoError(t, err)
	require.EqualValues(t, 2, version)
	require.EqualValues(t, 2, e.keys(e.tenant, e.user).Version(), "the next seal takes the new version")

	// A dry run names the user; a record written again is recognized under
	// the new version; a new one is sealed under it.
	dry, err := vault.Reseal(ctx, e.v, e.pool, e.rs, vault.SealOptions{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, 1, dry.Users)
	n := e.count(tQuantity)
	e.write(WriterConfig{}, everyFamily(at))
	require.Equal(t, n, e.count(tQuantity), "the keyed hash of a record is the same under both versions")
	require.Equal(t, before, e.opened(), "rows of version 1 still open")
	fresh := model.QuantitySample{Provider: "dev", QuantityType: "heart_rate", Value: 77, Unit: "bpm",
		StartDate: at.Add(5 * time.Hour), EndDate: at.Add(5 * time.Hour), SourceName: "Apple Watch", DeviceID: "w1", ContentHash: "after"}
	e.write(WriterConfig{}, &model.MapResult{QuantitySamples: []model.QuantitySample{fresh}})
	assert.EqualValues(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.quantity_samples WHERE content_hash = $1
		AND substring(sealed from 2 for 4) = '\x00000002'`, e.hash("after")), "a new row carries version 2")
	old := e.oldVersionRows(2)
	require.NotEmpty(t, old[tQuantity], "the rows written before stay under version 1")
	mixed := e.opened()

	report, err := vault.Reseal(ctx, e.v, e.pool, e.rs, vault.SealOptions{Rows: Sealer{}, Batch: 5})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Users)
	assert.Equal(t, old[tQuantity], report.Rows[tQuantity])
	assert.Equal(t, 1, report.RawObjects)
	assert.Equal(t, 1, report.KeysRetired)
	require.Empty(t, e.oldVersionRows(2), "only version 2 is left")
	require.Equal(t, mixed, e.opened(), "every value reads back as before")
	stored := e.storedBuckets("Apple Watch", "w1")
	for b, s := range buckets {
		if b.at.Equal(at.Add(5 * time.Hour)) {
			continue // the hour of the sample written after the rotation
		}
		assert.Equal(t, s, stored[b], "bucket %+v", b)
	}
	assert.EqualValues(t, 1, e.scalar(`SELECT count(*) FROM ohs_core.vault_keys WHERE user_id = $1 AND version = 1 AND retired_at IS NOT NULL`, e.user))
	assert.EqualValues(t, 1, e.scalar(`SELECT count(*) FROM ohs_core.vault_keys WHERE user_id = $1 AND version = 2 AND retired_at IS NULL`, e.user))
	assert.Positive(t, e.scalar(`SELECT count(*) FROM ohs_core.audit_events WHERE user_id = $1 AND action = 'vault.key_rotated'`, e.user))

	// The raw object is sealed to version 2 and still processed.
	var key string
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT key FROM ohs_sync.raw_objects WHERE user_id = $1`, e.user).Scan(&key))
	require.NotEqual(t, job.Key, key)
	rc, err := e.rs.Get(ctx, key)
	require.NoError(t, err)
	h, sealed, err := vault.ReadObjectHeader(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	require.True(t, sealed)
	require.EqualValues(t, 2, h.Version)
	job.Key = key
	e.process(job)

	again, err := vault.Reseal(ctx, e.v, e.pool, e.rs, vault.SealOptions{Rows: Sealer{}})
	require.NoError(t, err)
	assert.Zero(t, again.Users, "a user whose old versions are retired is done")
}
