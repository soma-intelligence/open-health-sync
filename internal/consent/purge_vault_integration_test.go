// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package consent_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/consent"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// sealedFigure is what the figure tables of one provider hold for a user,
// read through the vault.
type sealedFigures struct {
	sleepScore, hrv, restingHR, respiratory, spo2 *float64 // the sleep session
	total                                         *int
	avgHR, maxHR, vo2                             *float64 // the workout
	duration                                      *int
	dayRestingHR, dayHRV, daySleepScore           *float64 // the daily summary
	steps                                         *int
	sessions, workouts, days                      int
}

func readSealedFigures(t *testing.T, pool *pgxpool.Pool, v *vault.Vault, ten, user uuid.UUID, provider string) sealedFigures {
	t.Helper()
	ctx := context.Background()
	rd := vault.NewReader(v, ten, user)
	var f sealedFigures
	read := func(sel *vault.Columns, n *int, dest ...any) {
		rows, err := pool.Query(ctx, `SELECT `+sel.List("")+` FROM ohs_data.`+sel.Table().Name+`
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`, ten, user, provider)
		require.NoError(t, err)
		defer rows.Close()
		for rows.Next() {
			require.NoError(t, sel.Scan(ctx, rd, rows, dest))
			*n++
		}
		require.NoError(t, rows.Err())
	}
	read(vault.Select("sleep_sessions", `sleep_score, hrv_avg_ms, resting_hr, respiratory_rate, spo2_avg, total_sleep_seconds`),
		&f.sessions, &f.sleepScore, &f.hrv, &f.restingHR, &f.respiratory, &f.spo2, &f.total)
	read(vault.Select("workouts", `avg_heart_rate, max_heart_rate, vo2_max, duration_s`),
		&f.workouts, &f.avgHR, &f.maxHR, &f.vo2, &f.duration)
	read(vault.Select("daily_summaries", `resting_hr, hrv_avg_ms, sleep_score, step_count`),
		&f.days, &f.dayRestingHR, &f.dayHRV, &f.daySleepScore, &f.steps)
	return f
}

// A revoked figure must leave a sealed row too. The figures a record of
// another category carries (the heart rate of a workout, the sleep score of
// a day) are inside the row's sealed value, where setting the column to NULL
// changes nothing: the purge opens the row, drops the figure and seals the
// row again. The record and its other figures stay, another source is not
// touched, and no clear column is written.
func TestPurgeFiguresOfSealedRows(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	defer pool.Close()
	p, err := vault.OpenProvider(ctx, config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{8}, 32)})
	require.NoError(t, err)
	v := vault.New(pool, p, vault.Options{CanOpen: true})
	st := tenant.New(pool).WithVault(v)
	tn, _, err := st.CreateTenant(ctx, "purge")
	require.NoError(t, err)
	u, err := st.CreateUser(ctx, tn.ID, "purge-1")
	require.NoError(t, err)
	ten, user := tn.ID, u.ID

	at := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	n := func(v int) *int { return &v }
	for _, provider := range []string{"oura", "withings"} {
		_, err := etl.WriteSynthetic(ctx, pool, v, ten, user, provider, &model.MapResult{
			SleepSessions: []model.SleepSession{{Provider: provider, BedtimeStart: at.Add(-8 * time.Hour), BedtimeEnd: at,
				TotalSleepSeconds: n(27000), SleepScore: f(80), HRVAvgMs: f(50), RestingHR: f(52), RespiratoryRate: f(14),
				SpO2Avg: f(96), ContentHash: "sleep-" + provider}},
			Workouts: []model.Workout{{Provider: provider, ActivityType: "running", StartDate: at, EndDate: at.Add(time.Hour),
				DurationS: n(3600), AvgHeartRate: f(140), MaxHeartRate: f(170), VO2Max: f(48), ContentHash: "workout-" + provider}},
			DailySummaries: []model.DailySummary{{Provider: provider, SummaryDate: at.Truncate(24 * time.Hour),
				StepCount: n(9000), SleepScore: f(80), RestingHR: f(52), HRVAvgMs: f(50), ContentHash: "day-" + provider}},
		})
		require.NoError(t, err)
	}
	before := readSealedFigures(t, pool, v, ten, user, "oura")
	require.Equal(t, 1, before.sessions)
	require.Equal(t, 140.0, *before.avgHR)
	require.Equal(t, 80.0, *before.daySleepScore)
	sealedOnly := func() int {
		return count(t, pool, `SELECT (SELECT count(*) FROM ohs_data.workouts WHERE user_id = $1 AND (sealed IS NULL
				OR avg_heart_rate IS NOT NULL OR max_heart_rate IS NOT NULL OR vo2_max IS NOT NULL OR duration_s IS NOT NULL))
			+ (SELECT count(*) FROM ohs_data.daily_summaries WHERE user_id = $1 AND (sealed IS NULL
				OR resting_hr IS NOT NULL OR hrv_avg_ms IS NOT NULL OR sleep_score IS NOT NULL OR step_count IS NOT NULL))`, user)
	}
	require.Zero(t, sealedOnly(), "the rows are sealed, their columns NULL")
	macs := func() (workout, day []byte) {
		require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT content_mac FROM ohs_data.workouts WHERE user_id = $1 AND provider = 'oura'),
			(SELECT content_mac FROM ohs_data.daily_summaries WHERE user_id = $1 AND provider = 'oura')`, user).Scan(&workout, &day))
		return workout, day
	}
	workoutMAC, dayMAC := macs()

	// Activity stays granted; heart, respiratory and sleep are revoked.
	set(t, pool, ten, user, "oura", consent.Activity)

	// A purger with no vault cannot take the figures out: it fails, and the
	// purge stays queued, rather than finish with the figures left inside.
	// (The purges of categories with no figure in a sealed row pass first.)
	blind := consent.NewPurger(pool)
	for did := true; did && err == nil; {
		did, err = blind.Once(ctx)
	}
	require.ErrorIs(t, err, vault.ErrNoVault)
	assert.NotZero(t, count(t, pool, `SELECT count(*) FROM ohs_sync.consent_purges WHERE user_id = $1 AND finished_at IS NULL`, user))
	still := readSealedFigures(t, pool, v, ten, user, "oura")
	assert.Equal(t, 140.0, *still.avgHR, "a failed purge changes nothing")

	purger := consent.NewPurger(pool).WithVault(v)
	for {
		did, err := purger.Once(ctx)
		require.NoError(t, err)
		if !did {
			break
		}
	}
	assert.Zero(t, count(t, pool, `SELECT count(*) FROM ohs_sync.consent_purges WHERE user_id = $1 AND finished_at IS NULL`, user))

	got := readSealedFigures(t, pool, v, ten, user, "oura")
	assert.Zero(t, got.sessions, "sleep revoked: the session goes")
	require.Equal(t, 1, got.workouts, "the workout stays")
	assert.Nil(t, got.avgHR, "heart revoked: gone from the sealed workout")
	assert.Nil(t, got.maxHR)
	assert.Nil(t, got.vo2, "respiratory revoked")
	require.NotNil(t, got.duration)
	assert.Equal(t, 3600, *got.duration, "the figures of the category still granted stay")
	require.Equal(t, 1, got.days, "the day stays")
	assert.Nil(t, got.dayRestingHR)
	assert.Nil(t, got.dayHRV)
	assert.Nil(t, got.daySleepScore, "sleep revoked: gone from the sealed day")
	require.NotNil(t, got.steps)
	assert.Equal(t, 9000, *got.steps)
	assert.Zero(t, sealedOnly(), "the rows are still sealed, no figure written clear")
	newWorkoutMAC, newDayMAC := macs()
	assert.NotEqual(t, workoutMAC, newWorkoutMAC, "the content MAC follows the content")
	assert.NotEqual(t, dayMAC, newDayMAC)

	other := readSealedFigures(t, pool, v, ten, user, "withings")
	assert.Equal(t, 1, other.sessions, "another source is untouched")
	for _, fig := range []*float64{other.sleepScore, other.hrv, other.restingHR, other.respiratory, other.spo2,
		other.avgHR, other.maxHR, other.vo2, other.dayRestingHR, other.dayHRV, other.daySleepScore} {
		assert.NotNil(t, fig)
	}

	// A second purge of the same category finds nothing to rewrite.
	set(t, pool, ten, user, "oura", consent.Activity, consent.Heart)
	set(t, pool, ten, user, "oura", consent.Activity)
	for {
		did, err := purger.Once(ctx)
		require.NoError(t, err)
		if !did {
			break
		}
	}
	again, againDay := macs()
	assert.Equal(t, newWorkoutMAC, again, "a row that holds none of the figures is not rewritten")
	assert.Equal(t, newDayMAC, againDay)
}
