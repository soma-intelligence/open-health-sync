// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package read

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/fusion"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// statsFixture holds the same samples twice: under a user whose rows are
// clear and under a user whose rows are sealed.
type statsFixture struct {
	pool          *pgxpool.Pool
	v             *vault.Vault
	tenant        uuid.UUID
	clear, sealed uuid.UUID
}

// statsSamples are heart rates of three streams (a ring, and a watch and a
// phone behind one provider) every seven minutes over the given windows.
// The values are multiples of a quarter, so a sum does not depend on the
// order of its terms.
func statsSamples(windows ...[2]time.Time) map[string]*model.MapResult {
	out := map[string]*model.MapResult{"oura": {}, "apple_health": {}}
	i := 0
	for _, w := range windows {
		for at := w[0]; at.Before(w[1]); at = at.Add(7 * time.Minute) {
			i++
			add := func(provider, name, bundle, product string, v float64) {
				s := model.QuantitySample{Provider: provider, SourceName: name, SourceBundleID: bundle, ProductType: product,
					QuantityType: "heart_rate", Value: v, Unit: "bpm", IsPrimarySource: true, StartDate: at, EndDate: at,
					Metadata: map[string]any{"timezone": "Europe/Paris"}}
				s.ContentHash = model.ComputeContentHash(map[string]string{"p": provider, "n": name, "at": at.Format(time.RFC3339Nano)})
				out[provider].QuantitySamples = append(out[provider].QuantitySamples, s)
			}
			add("oura", "Ring", "", "", 50+float64(i%37)*0.25)
			if i%2 == 0 {
				add("apple_health", "Apple Watch", "com.apple.health.W", "Watch7,1", 60+float64(i%53)*0.5)
			}
			if i%3 == 0 {
				add("apple_health", "iPhone", "com.apple.health.P", "iPhone16,1", 70+float64(i%11)*0.75)
			}
		}
	}
	return out
}

func newStatsFixture(t *testing.T, windows ...[2]time.Time) statsFixture {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	p, err := vault.OpenProvider(ctx, config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{4}, 32)})
	require.NoError(t, err)
	f := statsFixture{pool: pool, v: vault.New(pool, p, vault.Options{CanOpen: true})}
	st := tenant.New(pool).WithVault(f.v)
	tn, _, err := st.CreateTenant(ctx, "stats")
	require.NoError(t, err)
	f.tenant = tn.ID
	for i, dst := range []*uuid.UUID{&f.clear, &f.sealed} {
		u, err := st.CreateUser(ctx, tn.ID, fmt.Sprintf("stats-%d", i))
		require.NoError(t, err)
		*dst = u.ID
		var write *vault.Vault
		if dst == &f.sealed {
			write = f.v
		}
		for provider, mr := range statsSamples(windows...) {
			_, err := etl.WriteSynthetic(ctx, pool, write, tn.ID, u.ID, provider, mr)
			require.NoError(t, err)
		}
	}
	_, err = pool.Exec(ctx, `CALL refresh_continuous_aggregate('ohs_data.quantity_hourly', NULL, NULL)`)
	require.NoError(t, err)

	var clearValues, sealedValues, buckets int
	require.NoError(t, pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM ohs_data.quantity_samples WHERE user_id = $1 AND value IS NOT NULL AND sealed IS NULL),
			(SELECT count(*) FROM ohs_data.quantity_samples WHERE user_id = $2 AND value IS NOT NULL),
			(SELECT count(*) FROM ohs_data.quantity_hourly_sealed WHERE user_id = $2)`, f.clear, f.sealed).
		Scan(&clearValues, &sealedValues, &buckets))
	require.NotZero(t, clearValues, "the clear user's rows are clear")
	require.Zero(t, sealedValues, "the sealed user's values are sealed")
	require.NotZero(t, buckets, "the sealed user has hourly buckets")
	return f
}

// asJSON is the answer as the API serves it.
func asJSON(t *testing.T, s Stats) string {
	t.Helper()
	b, err := json.MarshalIndent(s, "", " ")
	require.NoError(t, err)
	return string(b)
}

// Plan Task 14, review focus 1: the statistics computed in Go from sealed
// rows are the ones SQL computes from clear rows. The same samples, clear
// under one user and sealed under another, answer every query alike: hourly
// and daily, in UTC, in zones with a whole hour offset across a change of
// clocks (the sealed hourly buckets) and in zones at a half hour offset (the
// samples), merged and not, with and without a provider filter. A vault
// reading a user whose rows are still clear (an upgrade not converted yet)
// answers the same again.
func TestSealedStatsMatchSQL(t *testing.T) {
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }
	// Around the end of daylight saving in Los Angeles (2026-11-01) and its
	// start in Adelaide (2026-10-04).
	f := newStatsFixture(t, [2]time.Time{day(10, 3), day(10, 5)}, [2]time.Time{day(10, 31), day(11, 3)})
	ctx := context.Background()
	sealedCtx := WithVault(ctx, f.v)
	order := fusion.NewPolicy(fusion.Priorities{fusion.All: {"apple_health", "oura"}}, nil)

	n := 0
	for _, zoneName := range []string{"UTC", "America/Los_Angeles", "Europe/Paris", "Asia/Kolkata", "Australia/Adelaide"} {
		loc := zone(t, zoneName)
		for _, interval := range []string{IntervalHour, IntervalDay} {
			for _, view := range []View{ViewMerged, ViewAll} {
				for _, provider := range []string{"", "oura"} {
					for _, pol := range []*fusion.Policy{nil, &order} {
						q := StatsQuery{Type: "heart_rate", Interval: interval, Location: loc, View: view, Provider: provider,
							Policy: pol, Start: day(10, 2), End: day(11, 4)}
						name := fmt.Sprintf("%s %s %s provider=%q policy=%v", zoneName, interval, view, provider, pol != nil)
						want, err := SampleStats(ctx, f.pool, f.tenant, f.clear, q)
						require.NoError(t, err, name)
						require.NotEmpty(t, want.Buckets, name)

						got, err := SampleStats(sealedCtx, f.pool, f.tenant, f.sealed, q)
						require.NoError(t, err, name)
						require.Equal(t, asJSON(t, want), asJSON(t, got), "sealed rows: "+name)

						mixed, err := SampleStats(sealedCtx, f.pool, f.tenant, f.clear, q)
						require.NoError(t, err, name)
						require.Equal(t, asJSON(t, want), asJSON(t, mixed), "clear rows read under a vault: "+name)
						n++
					}
				}
			}
		}
	}
	assert.Equal(t, 80, n)

	// An empty range has no bucket, and a truncated answer says so.
	empty, err := SampleStats(sealedCtx, f.pool, f.tenant, f.sealed, StatsQuery{Type: "heart_rate", Interval: IntervalDay,
		Start: day(1, 1), End: day(1, 3)})
	require.NoError(t, err)
	assert.Empty(t, empty.Buckets)
	assert.NotNil(t, empty.Buckets)

	was := MaxStatsRows
	MaxStatsRows = 5
	q := StatsQuery{Type: "heart_rate", Interval: IntervalHour, View: ViewAll, Start: day(10, 31), End: day(11, 1)}
	want, err := SampleStats(ctx, f.pool, f.tenant, f.clear, q)
	require.NoError(t, err)
	got, err := SampleStats(sealedCtx, f.pool, f.tenant, f.sealed, q)
	MaxStatsRows = was
	require.NoError(t, err)
	assert.True(t, got.Truncated)
	assert.Equal(t, asJSON(t, want), asJSON(t, got))

	// Hours that hold sealed samples and have lost their buckets are not
	// answered from the continuous aggregate, which counts values it cannot
	// see: the samples answer.
	_, err = f.pool.Exec(ctx, `DELETE FROM ohs_data.quantity_hourly_sealed WHERE user_id = $1 AND bucket >= $2`, f.sealed, day(10, 31))
	require.NoError(t, err)
	for _, view := range []View{ViewMerged, ViewAll} {
		q := StatsQuery{Type: "heart_rate", Interval: IntervalDay, View: view, Location: zone(t, "America/Los_Angeles"),
			Start: day(10, 2), End: day(11, 4)}
		want, err := SampleStats(ctx, f.pool, f.tenant, f.clear, q)
		require.NoError(t, err)
		got, err := SampleStats(sealedCtx, f.pool, f.tenant, f.sealed, q)
		require.NoError(t, err)
		assert.Equal(t, asJSON(t, want), asJSON(t, got), "without buckets, %s", view)
	}
}

// A sealed bucket or sample that does not open fails the statistics: the
// answer never comes back with the bucket left out or a zero in its place.
func TestSealedStatsFailOnARowThatDoesNotOpen(t *testing.T) {
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	f := newStatsFixture(t, [2]time.Time{day, day.Add(6 * time.Hour)})
	ctx := WithVault(context.Background(), f.v)
	q := StatsQuery{Type: "heart_rate", Interval: IntervalHour, Start: day, End: day.AddDate(0, 0, 1)}
	_, err := SampleStats(ctx, f.pool, f.tenant, f.sealed, q)
	require.NoError(t, err)

	// One bit of one hourly bucket.
	_, err = f.pool.Exec(ctx, `UPDATE ohs_data.quantity_hourly_sealed SET sealed = set_byte(sealed, length(sealed) - 1,
			get_byte(sealed, length(sealed) - 1) # 1)
		WHERE (tenant_id, user_id, quantity_type, provider, source_key, slug, worn, unit, bucket) IN (
			SELECT tenant_id, user_id, quantity_type, provider, source_key, slug, worn, unit, bucket
			FROM ohs_data.quantity_hourly_sealed WHERE user_id = $1 ORDER BY bucket DESC LIMIT 1)`, f.sealed)
	require.NoError(t, err)
	_, err = SampleStats(ctx, f.pool, f.tenant, f.sealed, q)
	var open *vault.OpenError
	require.ErrorAs(t, err, &open)
	assert.Equal(t, "quantity_hourly_sealed", open.Table)

	// The samples path (a half hour zone) meets a sample that does not open.
	_, err = f.pool.Exec(ctx, `UPDATE ohs_data.quantity_samples SET sealed = set_byte(sealed, 9, get_byte(sealed, 9) # 1)
		WHERE id = (SELECT max(id) FROM ohs_data.quantity_samples WHERE user_id = $1)`, f.sealed)
	require.NoError(t, err)
	q.Location = zone(t, "Asia/Kolkata")
	_, err = SampleStats(ctx, f.pool, f.tenant, f.sealed, q)
	require.ErrorAs(t, err, &open)
	assert.Equal(t, "quantity_samples", open.Table)

	// Without a vault the sealed rows are not readable at all.
	q.Location = time.UTC
	_, err = Samples(context.Background(), f.pool, f.tenant, f.sealed, Query{Type: "heart_rate"})
	assert.ErrorIs(t, err, vault.ErrNoVault)
}
