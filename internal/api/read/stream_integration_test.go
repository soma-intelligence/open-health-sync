// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package read

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/fusion"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// EachSample decides the merged view in one pass; the paged SQL read is the
// reference. Random records of every kind of rule (readings with a
// tolerance, cumulative counts that only overlap, daily figures, a session
// metric with a minimum share, a metric of no group), from sources and
// streams that tie on rank, wear and key in every way, on a minute grid so
// that records touch, coincide and start together, must come out the same
// record for record, for every type and for a range.
func TestEachSampleMatchesThePagedRead(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := tenant.New(pool)
	tn, _, err := store.CreateTenant(ctx, "stream")
	require.NoError(t, err)

	types := []string{"heart_rate", "step_count", "resting_heart_rate", "body_mass", "sleep_analysis", "zz_custom",
		"distance_cycling"}
	providers := []struct{ provider, slug string }{
		{"apple_health", "apple_health"}, {"apple_health", "oura"}, {"withings", "withings"},
		{"health_connect", "garmin"}, {"health_connect", ""}, {"activity_files", "zz_unranked"},
	}
	names := []string{"", "Apple Watch", "iPhone", "Ring", "WATCH ultra"}
	bundles := []string{"", "com.apple.health.A", "com.apple.health.B"}
	devices := []string{"", "dev-1", "Dev-2"}
	t0 := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

	for seed := range uint64(4) {
		u, err := store.CreateUser(ctx, tn.ID, uuid.NewString())
		require.NoError(t, err)
		r := rand.New(rand.NewPCG(seed, 7))
		pick := func(xs []string) *string {
			s := xs[r.IntN(len(xs))]
			if s == "" && r.IntN(2) == 0 {
				return nil
			}
			return &s
		}
		for range 1500 {
			typ := types[r.IntN(len(types))]
			pv := providers[r.IntN(len(providers))]
			start := t0.Add(time.Duration(r.IntN(4*24*60)) * time.Minute)
			if typ == "heart_rate" || r.IntN(3) == 0 {
				start = t0.Add(time.Duration(r.IntN(6*60)) * time.Minute) // dense
			}
			// No record lasts longer than its metric's span (see EachSample).
			var end *time.Time
			span := fusion.RuleFor(typ).MaxSpan
			switch r.IntN(4) {
			case 0:
			case 1:
				end = &start
			default:
				e := start.Add(time.Duration(r.Int64N(int64(span/time.Minute)+1)) * time.Minute)
				end = &e
			}
			if typ == "distance_cycling" {
				// Sparse ten minute blocks, so records that only touch decide.
				start = t0.Add(time.Duration(r.IntN(2*24*6)) * 10 * time.Minute)
				e := start.Add(time.Duration(r.IntN(3)) * 10 * time.Minute)
				end = &e
			}
			var slug *string
			if pv.slug != "" {
				slug = &pv.slug
			}
			_, err := pool.Exec(ctx, `INSERT INTO ohs_data.quantity_samples
				(tenant_id, user_id, provider, source_slug, source_name, source_bundle_id, device_id, product_type,
				 quantity_type, value, unit, start_date, end_date, content_hash)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, 'x', $10, $11, $12)`,
				tn.ID, u.ID, pv.provider, slug, pick(names), pick(bundles), pick(devices), pick(names),
				typ, start, end, uuid.NewString())
			require.NoError(t, err)
		}
		if seed%2 == 1 {
			require.NoError(t, fusion.SavePriorities(ctx, pool, tn.ID, u.ID, fusion.Priorities{
				"heart_rate": {"withings", "garmin"}, "zz_custom": {"zz_unranked"},
			}))
		}
		queries := []Query{{}, {Start: t0.Add(2 * time.Hour), End: t0.Add(30 * time.Hour)}, {Start: t0.Add(3 * time.Hour)}}
		for _, typ := range types {
			queries = append(queries, Query{Type: typ})
		}
		if seed == 2 {
			// Scores rank a metric outside every group only when read alone.
			p := fusion.NewPolicy(nil, map[string]map[string]float64{"zz_custom": {"withings": 2}, "heart_rate": {"oura": 1}})
			queries = append(queries, Query{Policy: &p}, Query{Policy: &p, Type: "zz_custom"})
		}
		for _, q := range queries {
			var want []Sample
			pq := q
			pq.Limit = 97 // pages that end inside runs of one start time
			for {
				pg, err := AllSamples(ctx, pool, tn.ID, u.ID, pq)
				require.NoError(t, err)
				want = append(want, pg.Records...)
				if pg.NextCursor == "" {
					break
				}
				pq.Cursor = pg.NextCursor
			}
			var got []Sample
			batches := 0
			require.NoError(t, EachSample(ctx, pool, tn.ID, u.ID, q, func(b []Sample) error {
				require.LessOrEqual(t, len(b), MaxLimit)
				batches++
				got = append(got, b...)
				return nil
			}))
			require.Positive(t, batches)
			require.Equal(t, ids(want), ids(got), "seed %d, query %+v", seed, q)
			require.Equal(t, want, got)
			var all int
			require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM ohs_data.quantity_samples WHERE user_id = $1", u.ID).Scan(&all))
			if q == (Query{}) {
				require.Less(t, len(got), all, "the merged view leaves records out")
				require.NotEmpty(t, got)
			}
		}
	}

	err = EachSample(ctx, pool, tn.ID, uuid.New(), Query{}, func([]Sample) error { return nil })
	require.ErrorIs(t, err, ErrNotFound)
	var qe *QueryError
	require.ErrorAs(t, EachSample(ctx, pool, tn.ID, uuid.New(), Query{Limit: 5}, nil), &qe)
}

func ids(s []Sample) []int64 {
	out := make([]int64, len(s))
	for i, x := range s {
		out[i] = x.ID
	}
	return out
}
