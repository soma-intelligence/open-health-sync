// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/withings"
	"github.com/soma-intelligence/open-health-sync/connectors/withings/withingsfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// uploadSink stores every Emit as one raw object and keeps the jobs.
type uploadSink struct {
	e    *env
	jobs []queue.ETLJob
}

func (s *uploadSink) Emit(_ context.Context, dataType string, _ time.Time, lines [][]byte) error {
	strs := make([]string, len(lines))
	for i, l := range lines {
		strs[i] = string(l)
	}
	s.jobs = append(s.jobs, s.e.upload(s.e.tenant, s.e.user, withings.Slug, dataType, strs...))
	return nil
}

// A pull through the real connector against the fake Withings API, then the
// ETL: every data type lands in the canonical tables, in storage units, with
// its time zone, with no failed record, and a second pull of the same window
// adds no row.
func TestWithingsPullLands(t *testing.T) {
	e := newEnv(t)
	e.now = func() time.Time { return time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) }
	fake := withingsfake.New(t)
	c := withings.New(withings.Options{
		ClientID: "test-client-id", ClientSecret: "test-client-secret",
		AccountBase: fake.URL(), APIBase: fake.URL(), HTTPClient: fake.Client(),
		ChunkDelay: -1, PageDelay: -1,
	})
	e.reg.Register(c)
	ctx := context.Background()
	conn := connector.Connection{TenantID: e.tenant, UserID: e.user, Provider: withings.Slug,
		Tokens: connector.Tokens{Access: "fake-access-token-1", ProviderUserID: "424242"}}
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)

	pull := func() {
		sink := &uploadSink{e: e}
		require.NoError(t, c.Fetch(ctx, conn, from, to, sink))
		require.NotEmpty(t, sink.jobs)
		for _, j := range sink.jobs {
			e.process(j)
		}
	}
	pull()

	var failed int
	require.NoError(t, e.pool.QueryRow(ctx,
		`SELECT count(*) FROM ohs_data.failed_records WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user).Scan(&failed))
	assert.Equal(t, 0, failed)

	// measure 5 (the objective is dropped), activity 2 days x 5, ECG
	// classification 1, intraday 4 + 3 (no heart rate at 0 bpm).
	counts := map[string]int{
		"quantity_samples": 23,
		"observations":     0,
		"sleep_sessions":   2,
		"sleep_phases":     7,
		"workouts":         1,
		"ecg_recordings":   1,
		"ecg_samples":      6,
	}
	for table, want := range counts {
		assert.Equal(t, want, e.count(table), table)
	}

	var unit, tz string
	var value float64
	require.NoError(t, e.pool.QueryRow(ctx, `
		SELECT unit, value, metadata->>'timezone' FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2 AND quantity_type = 'body_mass'`, e.tenant, e.user).Scan(&unit, &value, &tz))
	assert.Equal(t, "kg", unit)
	assert.InDelta(t, 70, value, 1e-9)
	assert.Equal(t, "Europe/Paris", tz)

	var sleepTZ []string
	rows, err := e.pool.Query(ctx, `SELECT metadata->>'timezone' FROM ohs_data.sleep_sessions WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user)
	require.NoError(t, err)
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		sleepTZ = append(sleepTZ, s)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"Europe/Paris", "Europe/Paris"}, sleepTZ)

	var activity string
	require.NoError(t, e.pool.QueryRow(ctx, `
		SELECT activity_type, metadata->>'timezone' FROM ohs_data.workouts WHERE tenant_id = $1 AND user_id = $2`,
		e.tenant, e.user).Scan(&activity, &tz))
	assert.Equal(t, "running", activity, "the shared activity vocabulary")
	assert.Equal(t, "Europe/Paris", tz)

	require.NoError(t, e.pool.QueryRow(ctx, `
		SELECT metadata->>'timezone' FROM ohs_data.ecg_recordings WHERE tenant_id = $1 AND user_id = $2`,
		e.tenant, e.user).Scan(&tz))
	assert.Equal(t, "Europe/Paris", tz)

	// Daily totals on the user's local day.
	assert.Equal(t, []daySteps{{"2026-03-01", 8000}, {"2026-03-02", 10000}}, e.dailySteps(withings.Slug))

	// The same window pulled again: nothing new.
	pull()
	for table, want := range counts {
		assert.Equal(t, want, e.count(table), "after a second pull: %s", table)
	}
	assert.Equal(t, []daySteps{{"2026-03-01", 8000}, {"2026-03-02", 10000}}, e.dailySteps(withings.Slug))
}

// Withings re-sends an open day with a larger total: it replaces the stored
// one instead of adding a second sample.
func TestWithingsActivityTotalReplaced(t *testing.T) {
	e := newEnv(t)
	e.now = func() time.Time { return time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) }
	e.reg.Register(withings.New(withings.Options{}))
	ctx := context.Background()

	e.process(e.upload(e.tenant, e.user, withings.Slug, withings.DataTypeActivity,
		`{"date":"2026-03-01","timezone":"Europe/Paris","deviceid":"fake-watch-0001","steps":8000,"distance":6000,"calories":300}`))
	e.process(e.upload(e.tenant, e.user, withings.Slug, withings.DataTypeActivity,
		`{"date":"2026-03-01","timezone":"Europe/Paris","deviceid":"fake-watch-0001","steps":9500,"distance":7000,"calories":350}`))
	assert.Equal(t, 3, e.count("quantity_samples"))
	var value float64
	require.NoError(t, e.pool.QueryRow(ctx, `
		SELECT value FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2 AND quantity_type = 'step_count'`, e.tenant, e.user).Scan(&value))
	assert.InDelta(t, 9500, value, 1e-9)
	assert.Equal(t, []daySteps{{"2026-03-01", 9500}}, e.dailySteps(withings.Slug))
}
