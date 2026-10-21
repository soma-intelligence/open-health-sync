// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/healthconnect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func healthConnectMap(r connector.RawRecord) (model.MapResult, error) {
	return healthconnect.New().Map(r)
}

func hcMeal(id string, start time.Time, protein float64) string {
	return line(map[string]any{"recordType": "Nutrition", "metadata": map[string]any{"id": id, "dataOrigin": "com.example.food"},
		"startTime": start.Format(time.RFC3339), "endTime": start.Add(20 * time.Minute).Format(time.RFC3339),
		"mealType": "lunch", "energyKilocalories": 420.0, "proteinGrams": protein, "sodiumGrams": 0.8, "caffeineGrams": 0.05})
}

type storedLog struct {
	ExternalID string
	Start      time.Time
	MealType   string
	ProteinG   float64
	SodiumMg   float64
	Caffeine   float64
}

func (e *env) storedLogs() []storedLog {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT external_id, start_date, coalesce(meal_type, ''),
		coalesce(protein_g, 0), coalesce(sodium_mg, 0), coalesce((nutrients_json->>'caffeine')::float8, 0)
		FROM ohs_data.nutrition_logs WHERE tenant_id = $1 AND user_id = $2 ORDER BY external_id, start_date`,
		e.tenant, e.user)
	require.NoError(e.t, err)
	defer rows.Close()
	var out []storedLog
	for rows.Next() {
		var l storedLog
		require.NoError(e.t, rows.Scan(&l.ExternalID, &l.Start, &l.MealType, &l.ProteinG, &l.SodiumMg, &l.Caffeine))
		l.Start = l.Start.UTC()
		out = append(out, l)
	}
	require.NoError(e.t, rows.Err())
	return out
}

// A Health Connect meal is identified by its record id: a re-sent record
// replaces its figures, a moved start replaces its older copy, and a
// Deletion removes it.
func TestHealthConnectNutritionLog(t *testing.T) {
	e := newEnv(t)
	e.register(healthconnect.Slug, healthConnectMap)
	t0 := time.Date(2026, 9, 2, 11, 0, 0, 0, time.UTC)

	e.process(e.upload(e.tenant, e.user, healthconnect.Slug, "Nutrition", hcMeal("nu-1", t0, 24)))
	other := e.newUser(e.tenant)
	e.process(e.upload(e.tenant, other, healthconnect.Slug, "Nutrition", hcMeal("nu-1", t0, 24)))
	assert.Equal(t, []storedLog{{"nu-1", t0, "lunch", 24, 800, 50}}, e.storedLogs())

	e.process(e.upload(e.tenant, e.user, healthconnect.Slug, "Nutrition", hcMeal("nu-1", t0, 30)))
	assert.Equal(t, []storedLog{{"nu-1", t0, "lunch", 30, 800, 50}}, e.storedLogs(), "an edit replaces the figures")

	c := e.process(e.upload(e.tenant, e.user, healthconnect.Slug, "Nutrition", hcMeal("nu-1", t0.Add(time.Hour), 31)))
	assert.Equal(t, 1, c[tNutrition].Written)
	assert.Equal(t, []storedLog{{"nu-1", t0.Add(time.Hour), "lunch", 31, 800, 50}}, e.storedLogs(), "a moved start replaces the older copy")
	assert.Equal(t, 1, e.countFor("nutrition_logs", e.tenant, other), "another user's copy stays")

	// Two copies of one record in one object: the last one is kept.
	c = e.process(e.upload(e.tenant, e.user, healthconnect.Slug, "Nutrition",
		hcMeal("nu-1", t0.Add(2*time.Hour), 32), hcMeal("nu-1", t0.Add(3*time.Hour), 33)))
	assert.Equal(t, TableCount{Written: 1, Skipped: 1}, c[tNutrition])
	assert.Equal(t, []storedLog{{"nu-1", t0.Add(3 * time.Hour), "lunch", 33, 800, 50}}, e.storedLogs())

	e.process(e.upload(e.tenant, e.user, healthconnect.Slug, "Hydration", line(map[string]any{
		"recordType": "Hydration", "metadata": map[string]any{"id": "hy-1"},
		"startTime": "2026-09-02T08:00:00Z", "endTime": "2026-09-02T08:01:00Z", "volumeLiters": 0.33})))
	var water float64
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT (nutrients_json->>'water')::float8
		FROM ohs_data.nutrition_logs WHERE tenant_id = $1 AND user_id = $2 AND external_id = 'hy-1'`,
		e.tenant, e.user).Scan(&water))
	assert.InDelta(t, 330, water, 1e-9)

	e.process(e.upload(e.tenant, e.user, healthconnect.Slug, "Deletion",
		line(map[string]any{"recordType": "Deletion", "recordId": "nu-1"})))
	for _, l := range e.storedLogs() {
		assert.NotEqual(t, "nu-1", l.ExternalID, "the deleted meal is gone")
	}
	assert.Equal(t, 1, e.countFor("nutrition_logs", e.tenant, other))
}

// A source's own total energy is the day's total_kcal; without it the day
// keeps basal plus active energy.
func TestDerivedTotalKcal(t *testing.T) {
	e := newEnv(t)
	e.register(healthconnect.Slug, healthConnectMap)
	energy := func(typ, id string, start time.Time, kcal float64) string {
		return line(map[string]any{"recordType": typ, "metadata": map[string]any{"id": id, "dataOrigin": "com.sec.android.app.shealth"},
			"startTime": start.Format(time.RFC3339), "endTime": start.Add(time.Hour).Format(time.RFC3339),
			"startZoneOffset": "+02:00", "energyKilocalories": kcal})
	}
	day := time.Date(2026, 9, 2, 6, 0, 0, 0, time.UTC)
	e.process(e.upload(e.tenant, e.user, healthconnect.Slug, "TotalCaloriesBurned",
		energy("TotalCaloriesBurned", "tc-1", day, 900.5),
		energy("TotalCaloriesBurned", "tc-2", day.Add(time.Hour), 1100),
		energy("ActiveCaloriesBurned", "ac-1", day, 300),
	))
	var active, total float64
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT active_kcal, total_kcal FROM ohs_data.daily_summaries
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND summary_date = '2026-09-02'`,
		e.tenant, e.user, healthconnect.Slug).Scan(&active, &total))
	assert.Equal(t, 300.0, active)
	assert.Equal(t, 2000.5, total, "the source's own total, not basal plus active")

	e.register("apple_health", quantityMap)
	e.process(e.upload(e.tenant, e.user, "apple_health", "energy",
		line(map[string]any{"type": model.QTBasalEnergyBurned, "value": 1600.0, "unit": "kcal", "start": "2026-09-02T10:00:00Z", "source": "w"}),
		line(map[string]any{"type": model.QTActiveEnergyBurned, "value": 450.0, "unit": "kcal", "start": "2026-09-02T10:00:00Z", "source": "w"}),
	))
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT total_kcal FROM ohs_data.daily_summaries
		WHERE tenant_id = $1 AND user_id = $2 AND provider = 'apple_health' AND summary_date = '2026-09-02'`,
		e.tenant, e.user).Scan(&total))
	assert.Equal(t, 2050.0, total, "basal plus active")
}
