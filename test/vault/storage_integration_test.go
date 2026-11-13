// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package vault

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// seriesTables are the tables a stored series can occupy: the per-point
// tables of releases up to 0.3.0 and the per-parent blobs of migration 0053.
var seriesTables = []string{"ecg_samples", "route_points", "heartbeat_samples", "series_blobs"}

// relationBytes is the bytes on disk of table (heap, indexes and TOAST): of
// every chunk for a hypertable, 0 for a table that does not exist.
func relationBytes(t *testing.T, pool *pgxpool.Pool, table string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT CASE
			WHEN to_regclass('ohs_data.' || $1) IS NULL THEN 0
			WHEN EXISTS (SELECT 1 FROM timescaledb_information.hypertables
			    WHERE hypertable_schema = 'ohs_data' AND hypertable_name = $1)
			  THEN hypertable_size(('ohs_data.' || $1)::regclass)
			ELSE pg_total_relation_size(('ohs_data.' || $1)::regclass) END`, table).Scan(&n))
	return n
}

// seriesBytes is relationBytes summed over seriesTables.
func seriesBytes(t *testing.T, pool *pgxpool.Pool) int64 {
	var n int64
	for _, table := range seriesTables {
		n += relationBytes(t, pool, table)
	}
	return n
}

// storageFixture is one ECG of 30 s at 512 Hz (15,360 voltages as an Apple
// Watch sends them: volts times a million), a workout of 2 h with a route
// at 1 Hz (7,200 fixes with altitude, speed and course), or a day of
// heartbeat series (24 series of one hour, one beat about every 0.8 s, RR in
// whole milliseconds as a chest strap sends them).
type storageFixture struct {
	name   string
	points int
	build  func(i int) *model.MapResult
}

func storageFixtures() []storageFixture {
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return []storageFixture{
		{name: "ecg", points: 15360, build: func(i int) *model.MapResult {
			start := day.Add(time.Duration(i) * 24 * time.Hour).Add(8 * time.Hour)
			rec := model.ECGRecording{StartDate: start, EndDate: start.Add(30 * time.Second), Classification: 1,
				SamplingFrequency: 512, NumberOfMeasurements: 15360, Metadata: map[string]any{}}
			rec.ContentHash = model.ComputeContentHash(map[string]string{"storage": "ecg", "i": fmt.Sprint(i)})
			mr := &model.MapResult{ECGRecordings: []model.ECGRecording{rec}}
			for k := range 15360 {
				// A waveform with noise, in volts as HealthKit gives it,
				// rounded to the nanovolt the SDK prints, then made microvolts.
				x := float64(k) / 512
				volts := math.Round((0.0004*math.Sin(2*math.Pi*1.2*x)+0.00005*math.Sin(float64(k*7919%1000)))*1e9) / 1e9
				mr.ECGSamples = append(mr.ECGSamples, model.ECGVoltageSample{TimeSinceSampleStart: x,
					VoltageUV: volts * 1_000_000, ParentContentHash: rec.ContentHash})
			}
			return mr
		}},
		{name: "route", points: 7200, build: func(i int) *model.MapResult {
			start := day.Add(time.Duration(i) * 24 * time.Hour).Add(7 * time.Hour)
			end := start.Add(2 * time.Hour)
			wk := model.Workout{ActivityType: "running", StartDate: start, EndDate: end, Metadata: map[string]any{}}
			wk.ContentHash = model.ComputeContentHash(map[string]string{"storage": "route", "i": fmt.Sprint(i)})
			mr := &model.MapResult{Workouts: []model.Workout{wk}}
			rng := rand.New(rand.NewPCG(uint64(i), 2))
			lat, lon, alt := 48.8566, 2.3522, 35.0
			for k := range 7200 {
				// A smooth path with GPS noise; Apple Health prints the
				// CLLocation fields with all their double digits.
				lat += 0.00002*math.Cos(float64(k)/300) + rng.NormFloat64()*0.000002
				lon += 0.00003*math.Sin(float64(k)/400) + rng.NormFloat64()*0.000002
				alt += 0.1*math.Sin(float64(k)/60) + rng.NormFloat64()*0.3
				a, s, c := alt, 3+math.Sin(float64(k)/90)+rng.NormFloat64()*0.2, math.Mod(float64(k)*0.7+rng.NormFloat64()*3+360, 360)
				mr.RoutePoints = append(mr.RoutePoints, model.RoutePoint{Timestamp: start.Add(time.Duration(k) * time.Second),
					Latitude: lat, Longitude: lon, AltitudeM: &a, SpeedMps: &s, CourseDeg: &c, ParentContentHash: wk.ContentHash})
			}
			return mr
		}},
		{name: "heartbeat", points: 0, build: func(i int) *model.MapResult {
			mr := &model.MapResult{}
			rng := rand.New(rand.NewPCG(uint64(i), 3))
			for h := range 24 {
				at := day.Add(time.Duration(i) * 24 * time.Hour).Add(time.Duration(h) * time.Hour)
				end := at.Add(time.Hour)
				for k := 0; at.Before(end); k++ {
					// A slow swing plus beat-to-beat variability.
					rr := math.Round(800 + 60*math.Sin(float64(k)/40) + rng.NormFloat64()*30)
					mr.HeartbeatSamples = append(mr.HeartbeatSamples, model.HeartbeatSample{
						SeriesID: fmt.Sprintf("storage-%d-%02d", i, h), Timestamp: at, RRMS: rr})
					at = at.Add(time.Duration(rr) * time.Millisecond)
				}
			}
			return mr
		}},
	}
}

// TestSeriesStorage measures the bytes on disk per stored point of ECG
// voltages, route fixes and heartbeats, in clear and sealed mode, and logs
// them (docs/release-notes/v0.3.0.md quotes the numbers). Each fixture is
// written three times so the fixed cost of an empty page weighs less.
func TestSeriesStorage(t *testing.T) {
	ctx := context.Background()
	for _, sealed := range []bool{false, true} {
		pool, v, tn, u := storageInstance(t, sealed)
		for _, f := range storageFixtures() {
			before := seriesBytes(t, pool)
			points := 0
			for i := range 3 {
				mr := f.build(i)
				points += len(mr.ECGSamples) + len(mr.RoutePoints) + len(mr.HeartbeatSamples)
				_, err := etl.WriteSynthetic(ctx, pool, v, tn, u, "apple_health", mr)
				require.NoError(t, err, f.name)
			}
			_, err := pool.Exec(ctx, `VACUUM ANALYZE`)
			require.NoError(t, err)
			after := seriesBytes(t, pool)
			mode := "clear"
			if sealed {
				mode = "sealed"
			}
			t.Logf("storage %-6s %-9s points=%d bytes=%d bytes/point=%.2f", mode, f.name, points, after-before,
				float64(after-before)/float64(points))
		}
	}
}

// storageInstance is a migrated database with one tenant and one user, with
// a vault when sealed.
func storageInstance(t *testing.T, sealed bool) (*pgxpool.Pool, *vault.Vault, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	st := tenant.New(pool)
	var v *vault.Vault
	if sealed {
		p, err := vault.OpenProvider(ctx, config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{7}, 32)})
		require.NoError(t, err)
		v = vault.New(pool, p, vault.Options{CanOpen: true})
		st = st.WithVault(v)
	}
	tn, _, err := st.CreateTenant(ctx, "Storage")
	require.NoError(t, err)
	u, err := st.CreateUser(ctx, tn.ID, "storage-1")
	require.NoError(t, err)
	return pool, v, tn.ID, u.ID
}
