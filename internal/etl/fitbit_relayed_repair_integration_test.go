// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The repair script of docs/how-to/fitbit-relayed-points.sql rewrites what
// the Fitbit connector stored before it told relayed Apple Health and Health
// Connect points apart, on compressed chunks too, and running it twice
// changes nothing more.
func TestFitbitRelayedRepairScript(t *testing.T) {
	script, err := os.ReadFile("../../docs/how-to/fitbit-relayed-points.sql")
	require.NoError(t, err)

	e := newEnv(t)
	at := time.Date(2026, 1, 9, 2, 0, 0, 0, time.UTC)
	day := time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)
	ins := func(hash, qt string, v float64, start time.Time, md string) {
		e.exec(`INSERT INTO ohs_data.quantity_samples
			(tenant_id, user_id, provider, source_slug, source_name, quantity_type, value, unit, start_date, content_hash, metadata)
			VALUES ($1, $2, 'fitbit', 'fitbit', 'Fitbit', $3, $4, 'ms', $5, $6, $7::jsonb)`,
			e.tenant, e.user, qt, v, start, hash, md)
	}
	// Apple's SDNN stored as RMSSD: a daily figure, with its zero deep sleep
	// RMSSD, and a sample that also carried a standard deviation.
	ins("hk-day", "hrv_rmssd", 40, day, `{"platform":"HEALTH_KIT","hrv_method":"rmssd","deep_sleep_rmssd_ms":0}`)
	ins("hk-sample", "hrv_rmssd", 38, at, `{"platform":"HEALTH_KIT","hrv_method":"rmssd","standard_deviation_ms":36}`)
	// The same night already stored again by the fixed connector: the old
	// copy goes instead of becoming a twin.
	ins("hk-old-twin", "hrv_rmssd", 41, at.Add(time.Hour), `{"platform":"HEALTH_KIT","hrv_method":"rmssd"}`)
	e.exec(`INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, source_slug, source_name, quantity_type, value, unit, start_date, content_hash, metadata)
		VALUES ($1, $2, 'fitbit', 'apple_health', 'Apple Health', 'hrv_sdnn', 41, 'ms', $3, 'hk-new', '{"platform":"HEALTH_KIT","hrv_method":"sdnn"}')`,
		e.tenant, e.user, at.Add(time.Hour))
	ins("hc-steps", "step_count_intraday", 80, at, `{"platform":"HEALTH_CONNECT"}`)
	ins("fb-hrv", "hrv_rmssd", 55, day, `{"platform":"FITBIT","hrv_method":"rmssd","deep_sleep_rmssd_ms":0}`)
	ins("fb-hrv-deep", "hrv_rmssd", 56, day.AddDate(0, 0, 1), `{"platform":"FITBIT","hrv_method":"rmssd","deep_sleep_rmssd_ms":48}`)
	e.compressQuantity()

	for range 2 {
		_, err := e.pool.Exec(context.Background(), string(script))
		require.NoError(t, err)
	}

	type row struct {
		qt, slug, name string
		v              float64
		md             map[string]any
	}
	get := func(hash string) (row, bool) {
		var r row
		err := e.pool.QueryRow(context.Background(), `SELECT quantity_type, source_slug, source_name, value, metadata
			FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND content_hash = $2`, e.tenant, hash).
			Scan(&r.qt, &r.slug, &r.name, &r.v, &r.md)
		return r, err == nil
	}

	r, ok := get("hk-day")
	require.True(t, ok)
	assert.Equal(t, row{"hrv_sdnn", "apple_health", "Apple Health", 40, r.md}, r)
	assert.Equal(t, "sdnn", r.md["hrv_method"])
	assert.NotContains(t, r.md, "deep_sleep_rmssd_ms")

	r, ok = get("hk-sample")
	require.True(t, ok)
	assert.Equal(t, "hrv_sdnn", r.qt)
	assert.Equal(t, 36.0, r.v, "the standard deviation is the SDNN, as the connector now reads it")
	assert.NotContains(t, r.md, "standard_deviation_ms")

	_, ok = get("hk-old-twin")
	assert.False(t, ok, "a copy the fixed connector already stored again is removed")
	_, ok = get("hk-new")
	assert.True(t, ok)

	r, _ = get("hc-steps")
	assert.Equal(t, "health_connect", r.slug)
	assert.Equal(t, "Health Connect", r.name)

	r, _ = get("fb-hrv")
	assert.Equal(t, row{"hrv_rmssd", "fitbit", "Fitbit", 55, r.md}, r, "Fitbit's own HRV stays RMSSD")
	assert.NotContains(t, r.md, "deep_sleep_rmssd_ms", "a zero deep sleep RMSSD is no measurement")
	r, _ = get("fb-hrv-deep")
	assert.EqualValues(t, 48, r.md["deep_sleep_rmssd_ms"])
}
