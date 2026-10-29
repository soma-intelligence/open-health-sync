// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package consent_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/consent"
	"github.com/soma-intelligence/open-health-sync/internal/db"
)

func set(t *testing.T, pool *pgxpool.Pool, ten, user uuid.UUID, provider string, granted ...string) []consent.Change {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	ch, err := consent.Set(ctx, tx, ten, user, provider, granted, "key-1", "form v2")
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	return ch
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func TestSetListHistory(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	defer pool.Close()
	ten, user := uuid.New(), uuid.New()

	p, err := consent.Load(ctx, pool, ten, user, "oura", false)
	require.NoError(t, err)
	assert.False(t, p.Recorded)
	assert.True(t, p.Allows(consent.Heart))

	ch := set(t, pool, ten, user, "oura", consent.Heart, consent.Sleep)
	assert.Equal(t, []consent.Change{{Category: consent.Heart, Granted: true}, {Category: consent.Sleep, Granted: true}}, ch,
		"a first consent reports grants only")
	assert.Equal(t, len(consent.Categories)-2, count(t, pool,
		`SELECT count(*) FROM ohs_sync.consent_purges WHERE user_id = $1`, user),
		"each category left out of a first consent queues a purge of what may be stored")

	p, err = consent.Load(ctx, pool, ten, user, "oura", false)
	require.NoError(t, err)
	assert.True(t, p.Recorded)
	assert.True(t, p.Allows(consent.Heart))
	assert.False(t, p.Allows(consent.Location))

	assert.Empty(t, set(t, pool, ten, user, "oura", consent.Sleep, consent.Heart), "the same consent changes nothing")
	ch = set(t, pool, ten, user, "oura", consent.Heart, consent.Glucose)
	assert.ElementsMatch(t, []consent.Change{{Category: consent.Glucose, Granted: true}, {Category: consent.Sleep, Granted: false}}, ch)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = consent.Set(ctx, tx, ten, user, "oura", []string{"genome"}, "", "")
	require.ErrorIs(t, err, consent.ErrUnknownCategory)
	require.NoError(t, tx.Rollback(ctx))

	set(t, pool, ten, user, "withings", consent.Body)
	srcs, err := consent.List(ctx, pool, ten, user)
	require.NoError(t, err)
	require.Len(t, srcs, 2)
	assert.Equal(t, "oura", srcs[0].Provider)
	require.Len(t, srcs[0].Categories, len(consent.Categories))
	assert.Equal(t, consent.Activity, srcs[0].Categories[0].Category, "categories in documentation order")
	byCat := map[string]consent.Category{}
	for _, c := range srcs[0].Categories {
		byCat[c.Category] = c
	}
	assert.True(t, byCat[consent.Heart].Granted)
	assert.NotNil(t, byCat[consent.Heart].GrantedAt)
	assert.False(t, byCat[consent.Sleep].Granted)
	assert.NotNil(t, byCat[consent.Sleep].RevokedAt)
	assert.NotNil(t, byCat[consent.Sleep].GrantedAt, "a revoked category keeps when it was granted")
	assert.Nil(t, byCat[consent.Location].GrantedAt)

	other, err := consent.List(ctx, pool, uuid.New(), user)
	require.NoError(t, err)
	assert.Empty(t, other, "another tenant sees nothing")

	evs, next, err := consent.History(ctx, pool, ten, user, 0, 0)
	require.NoError(t, err)
	assert.Zero(t, next)
	require.Len(t, evs, 5, "2 grants, 1 grant and 1 revocation, 1 grant")
	assert.Equal(t, "withings", evs[0].Provider, "newest first")
	assert.Equal(t, "key-1", evs[0].ActorID)
	assert.Equal(t, "form v2", evs[0].Evidence)

	page, next, err := consent.History(ctx, pool, ten, user, 0, 2)
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.NotZero(t, next)
	rest, last, err := consent.History(ctx, pool, ten, user, next, 10)
	require.NoError(t, err)
	assert.Len(t, rest, 3)
	assert.Zero(t, last)
}

func TestPurge(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	defer pool.Close()
	ten, user := uuid.New(), uuid.New()
	at := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, sql, args...)
		require.NoError(t, err)
	}
	id := func(sql string, args ...any) int64 {
		t.Helper()
		var n int64
		require.NoError(t, pool.QueryRow(ctx, sql, args...).Scan(&n))
		return n
	}
	seed := func(provider string) {
		h := func(s string) string { return fmt.Sprintf("%s-%s", provider, s) }
		exec(`INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, quantity_type, value, start_date, content_hash)
			VALUES ($1, $2, $3, 'heart_rate', 61, $4, $5)`, ten, user, provider, at, h("hr"))
		exec(`INSERT INTO ohs_data.quantity_samples (tenant_id, user_id, provider, quantity_type, value, start_date, content_hash)
			VALUES ($1, $2, $3, 'step_count', 100, $4, $5)`, ten, user, provider, at, h("steps"))
		exec(`INSERT INTO ohs_data.heartbeat_samples (tenant_id, user_id, provider, series_id, "timestamp", rr_ms, content_hash)
			VALUES ($1, $2, $3, 'series', $4, 900, $5)`, ten, user, provider, at, h("beat"))
		s := id(`INSERT INTO ohs_data.sleep_sessions (tenant_id, user_id, provider, bedtime_start, content_hash)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`, ten, user, provider, at, h("sleep"))
		exec(`INSERT INTO ohs_data.sleep_phases (tenant_id, user_id, session_id, phase, start_date, duration_s, content_hash)
			VALUES ($1, $2, $3, 'deep', $4, 600, $5)`, ten, user, s, at, h("phase"))
		w := id(`INSERT INTO ohs_data.workouts (tenant_id, user_id, provider, activity_type, start_date, content_hash)
			VALUES ($1, $2, $3, 'running', $4, $5) RETURNING id`, ten, user, provider, at, h("workout"))
		exec(`INSERT INTO ohs_data.route_points (tenant_id, user_id, workout_id, "timestamp", latitude, longitude, content_hash)
			VALUES ($1, $2, $3, $4, 10.5, 20.5, $5)`, ten, user, w, at, h("route"))
	}
	seed("oura")
	seed("withings")
	rows := func(provider, sql string) int {
		return count(t, pool, sql+` WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`, ten, user, provider)
	}

	set(t, pool, ten, user, "oura", consent.Heart, consent.Sleep, consent.Activity, consent.Location)
	purger := consent.NewPurger(pool)
	for {
		did, err := purger.Once(ctx)
		require.NoError(t, err)
		if !did {
			break
		}
	}
	assert.Equal(t, 2, rows("oura", `SELECT count(*) FROM ohs_data.quantity_samples`), "nothing granted was purged")

	set(t, pool, ten, user, "oura", consent.Sleep, consent.Activity)
	for {
		did, err := purger.Once(ctx)
		require.NoError(t, err)
		if !did {
			break
		}
	}
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM ohs_data.quantity_samples
		WHERE user_id = $1 AND provider = 'oura' AND quantity_type = 'step_count'`, user))
	assert.Zero(t, count(t, pool, `SELECT count(*) FROM ohs_data.quantity_samples
		WHERE user_id = $1 AND provider = 'oura' AND quantity_type = 'heart_rate'`, user), "heart revoked")
	assert.Zero(t, rows("oura", `SELECT count(*) FROM ohs_data.heartbeat_samples`))
	assert.Zero(t, count(t, pool, `SELECT count(*) FROM ohs_data.route_points WHERE user_id = $1 AND workout_id IN
		(SELECT id FROM ohs_data.workouts WHERE provider = 'oura')`, user), "location revoked")
	assert.Equal(t, 1, rows("oura", `SELECT count(*) FROM ohs_data.workouts`), "the workout stays")
	assert.Equal(t, 1, rows("oura", `SELECT count(*) FROM ohs_data.sleep_sessions`))

	assert.Equal(t, 2, rows("withings", `SELECT count(*) FROM ohs_data.quantity_samples`), "another source is untouched")
	assert.Equal(t, 1, rows("withings", `SELECT count(*) FROM ohs_data.heartbeat_samples`))
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM ohs_data.route_points WHERE user_id = $1 AND workout_id IN
		(SELECT id FROM ohs_data.workouts WHERE provider = 'withings')`, user))

	var deleted int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT rows_deleted FROM ohs_sync.consent_purges
		WHERE user_id = $1 AND provider = 'oura' AND category = 'heart' AND rows_deleted > 0`, user).Scan(&deleted))
	assert.Equal(t, int64(2), deleted, "the heart rate sample and the heartbeat")

	// A purge queued then overtaken by a new grant keeps the data.
	set(t, pool, ten, user, "oura", consent.Activity)
	set(t, pool, ten, user, "oura", consent.Activity, consent.Sleep)
	for {
		did, err := purger.Once(ctx)
		require.NoError(t, err)
		if !did {
			break
		}
	}
	assert.Equal(t, 1, rows("oura", `SELECT count(*) FROM ohs_data.sleep_sessions`))
	assert.Zero(t, count(t, pool, `SELECT count(*) FROM ohs_sync.consent_purges WHERE finished_at IS NULL`))
}

// TestPurgeFigures: revoking heart (or sleep, or respiratory) takes the
// figures of that category out of the sleep sessions, workouts and daily
// summaries that stay, from that source only.
func TestPurgeFigures(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	defer pool.Close()
	ten, user := uuid.New(), uuid.New()
	at := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	for _, provider := range []string{"oura", "withings"} {
		_, err := pool.Exec(ctx, `INSERT INTO ohs_data.sleep_sessions (tenant_id, user_id, provider, bedtime_start,
				sleep_score, hrv_avg_ms, resting_hr, respiratory_rate, spo2_avg, content_hash)
			VALUES ($1, $2, $3, $4, 80, 50, 52, 14, 96, $3)`, ten, user, provider, at)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `INSERT INTO ohs_data.workouts (tenant_id, user_id, provider, activity_type, start_date,
				avg_heart_rate, max_heart_rate, vo2_max, content_hash)
			VALUES ($1, $2, $3, 'running', $4, 140, 170, 48, $3)`, ten, user, provider, at)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `INSERT INTO ohs_data.daily_summaries (tenant_id, user_id, provider, summary_date,
				step_count, sleep_score, resting_hr, hrv_avg_ms, content_hash)
			VALUES ($1, $2, $3, '2026-09-01', 9000, 80, 52, 50, $3)`, ten, user, provider)
		require.NoError(t, err)
	}
	set(t, pool, ten, user, "oura", consent.Activity)
	purger := consent.NewPurger(pool)
	for {
		did, err := purger.Once(ctx)
		require.NoError(t, err)
		if !did {
			break
		}
	}
	figures := func(provider string) [6]*float64 {
		var f [6]*float64
		require.NoError(t, pool.QueryRow(ctx, `SELECT w.avg_heart_rate, w.max_heart_rate, w.vo2_max,
				d.resting_hr, d.hrv_avg_ms, d.sleep_score
			FROM ohs_data.workouts w JOIN ohs_data.daily_summaries d
				ON d.tenant_id = w.tenant_id AND d.user_id = w.user_id AND d.provider = w.provider
			WHERE w.tenant_id = $1 AND w.user_id = $2 AND w.provider = $3`, ten, user, provider).
			Scan(&f[0], &f[1], &f[2], &f[3], &f[4], &f[5]))
		return f
	}
	assert.Equal(t, [6]*float64{}, figures("oura"), "the heart, respiratory and sleep figures of oura are gone")
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM ohs_data.daily_summaries
		WHERE tenant_id = $1 AND user_id = $2 AND provider = 'oura' AND step_count = 9000`, ten, user), "the day stays")
	for _, f := range figures("withings") {
		assert.NotNil(t, f, "another source is untouched")
	}
	assert.Zero(t, count(t, pool, `SELECT count(*) FROM ohs_data.sleep_sessions
		WHERE tenant_id = $1 AND user_id = $2 AND provider = 'oura'`, ten, user), "sleep revoked: the session goes")
}
