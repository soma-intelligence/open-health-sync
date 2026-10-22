// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
)

// The SDK's own lines, through the real Apple Health connector, land in the
// canonical tables in their storage units and with their time zone.
func TestAppleHealthUploadLinesLand(t *testing.T) {
	e := newEnv(t)
	e.reg.Register(applehealth.New())
	raw, err := os.ReadFile("../../connectors/applehealth/testdata/ios_upload_lines.ndjson")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")

	job := e.upload(e.tenant, e.user, applehealth.Slug, "mixed", lines...)
	e.process(job)

	assert.Equal(t, 5, e.count("quantity_samples"), "heart rate, spo2, steps, systolic, diastolic")
	assert.Equal(t, 1, e.count("sleep_sessions"))
	assert.Equal(t, 1, e.count("workouts"))
	var failed int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ohs_data.failed_records WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user).Scan(&failed))
	assert.Equal(t, 0, failed)

	var unit, tz string
	var value float64
	require.NoError(t, e.pool.QueryRow(context.Background(), `
		SELECT unit, value, metadata->>'timezone' FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2 AND quantity_type = 'oxygen_saturation'`,
		e.tenant, e.user).Scan(&unit, &value, &tz))
	assert.Equal(t, "percent", unit)
	assert.InDelta(t, 97, value, 1e-9, "the 0..1 HealthKit fraction is stored as a percentage")
	assert.Equal(t, "Europe/Paris", tz)
}

// Unknown HealthKit types are skipped by the real connector: nothing stored,
// nothing in coverage, no failed record. The known line beside them lands.
func TestAppleHealthUnknownTypesAreSkipped(t *testing.T) {
	e := newEnv(t)
	e.reg.Register(applehealth.New())
	lines := []string{
		`{"quantityType":"HKQuantityTypeIdentifierFutureMetric","value":42,"unit":"count","startDate":"2026-03-01T10:00:00Z","endDate":"2026-03-01T10:00:00Z"}`,
		`{"categoryType":"HKCategoryTypeIdentifierFutureCategory","value":1,"startDate":"2026-03-01T10:00:00Z","endDate":"2026-03-01T10:05:00Z"}`,
		`{"quantityType":"HKQuantityTypeIdentifierHeartRate","value":72,"unit":"count/min","startDate":"2026-03-01T10:00:00Z","endDate":"2026-03-01T10:00:00Z"}`,
	}
	e.process(e.upload(e.tenant, e.user, applehealth.Slug, "mixed", lines...))

	assert.Equal(t, 1, e.count("quantity_samples"))
	assert.Equal(t, 0, e.count("observations"))
	assert.Equal(t, 0, e.count("category_samples"))
	assert.Equal(t, 0, e.count("failed_records"))
	var metrics []string
	rows, err := e.pool.Query(context.Background(),
		`SELECT DISTINCT metric FROM ohs_sync.pull_coverage WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user)
	require.NoError(t, err)
	for rows.Next() {
		var m string
		require.NoError(t, rows.Scan(&m))
		metrics = append(metrics, m)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"heart_rate"}, metrics)
}

// reprocessUserSQL is the "Re-processing a user from the raw files" recipe of
// docs/self-hosting.md, with the two ids as parameters. Keep them in step.
const reprocessUserSQL = `INSERT INTO ohs_sync.etl_jobs (tenant_id, user_id, provider, key, tier)
SELECT o.tenant_id, o.user_id, o.provider, o.key,
       CASE WHEN o.provider = 'apple_health' THEN 'high_freq' ELSE 'standard' END
FROM ohs_sync.raw_objects o
WHERE o.tenant_id = $1 AND o.user_id = $2
  AND NOT EXISTS (SELECT 1 FROM ohs_sync.etl_jobs j
                  WHERE j.key = o.key AND j.status IN ('pending', 'running'))`

// The documented requeue queues one job per raw object of the user, and
// processing it again stores nothing twice.
func TestReprocessUserRecipe(t *testing.T) {
	e := newEnv(t)
	e.reg.Register(applehealth.New())
	job := e.upload(e.tenant, e.user, applehealth.Slug, "HKQuantityTypeIdentifierHeartRate",
		`{"quantityType":"HKQuantityTypeIdentifierHeartRate","value":72,"unit":"count/min","startDate":"2026-03-01T10:00:00Z","endDate":"2026-03-01T10:00:00Z"}`)
	e.process(job)
	require.Equal(t, 1, e.count("quantity_samples"))

	tag, err := e.pool.Exec(context.Background(), reprocessUserSQL, e.tenant, e.user)
	require.NoError(t, err)
	assert.EqualValues(t, 1, tag.RowsAffected())
	var key, tier string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT key, tier FROM ohs_sync.etl_jobs WHERE tenant_id = $1 AND user_id = $2 AND status = 'pending'`,
		e.tenant, e.user).Scan(&key, &tier))
	assert.Equal(t, job.Key, key)
	assert.Equal(t, "high_freq", tier)

	// Run again while that job is pending: nothing more is queued.
	tag, err = e.pool.Exec(context.Background(), reprocessUserSQL, e.tenant, e.user)
	require.NoError(t, err)
	assert.EqualValues(t, 0, tag.RowsAffected())

	e.process(job)
	assert.Equal(t, 1, e.count("quantity_samples"), "re-processing stores nothing twice")
}

// A NUL in a source name or a metadata value, which PostgreSQL text and
// jsonb refuse, used to fail the whole object: the job retried until parked
// and every line beside it was lost. It is now removed and the lines land.
func TestAppleHealthNULCharactersLand(t *testing.T) {
	e := newEnv(t)
	e.reg.Register(applehealth.New())
	lines := []string{
		`{"quantityType":"HKQuantityTypeIdentifierHeartRate","value":72,"unit":"count/min","startDate":"2026-03-01T10:00:00Z","endDate":"2026-03-01T10:00:00Z","sourceName":"Watch\u0000"}`,
		`{"quantityType":"HKQuantityTypeIdentifierHeartRate","value":74,"unit":"count/min","startDate":"2026-03-01T10:01:00Z","endDate":"2026-03-01T10:01:00Z","metadata":{"HKMetadataKeyDeviceName":"a\u0000b"}}`,
		`{"quantityType":"HKQuantityTypeIdentifierHeartRate","value":76,"unit":"count/min","startDate":"2026-03-01T10:02:00Z","endDate":"2026-03-01T10:02:00Z"}`,
		`{"quantityType":"HKQuantityTypeIdentifierHeartRate","value":-5,"unit":"count/min","startDate":"2026-03-01T10:03:00Z","endDate":"2026-03-01T10:03:00Z","sourceName":"\u0000refused"}`,
	}
	e.process(e.upload(e.tenant, e.user, applehealth.Slug, "mixed", lines...))

	assert.Equal(t, 3, e.count("quantity_samples"))
	var name string
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT source_name FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2 AND value = 72`, e.tenant, e.user).Scan(&name))
	assert.Equal(t, "Watch", name)
	var failed int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_data.failed_records
		WHERE tenant_id = $1 AND user_id = $2 AND reason_code = 'validation'`, e.tenant, e.user).Scan(&failed))
	assert.Equal(t, 1, failed, "the out of range line is recorded, its payload stripped of the NUL")
}
