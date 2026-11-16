// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/vault/series"
)

// A route delivered in parts merges into one blob: a point it already holds
// is replaced only by a copy captured later, a point it does not hold is
// added whatever the capture, and a copy of unknown capture never replaces.
func TestSeriesMergesPartsUnderTheCaptureGuard(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(map[bool]string{false: "sealed", true: "clear"}[clear], func(t *testing.T) {
			e := newEnv(t)
			e.clear = clear
			start := time.Date(2026, 9, 4, 7, 0, 0, 0, time.UTC)
			t1, t2, t3 := start.Add(time.Hour), start.Add(2*time.Hour), start.Add(3*time.Hour)
			point := func(k int, speed float64) model.RoutePoint {
				return model.RoutePoint{Timestamp: start.Add(time.Duration(k) * time.Second), Latitude: 48 + float64(k)/1000,
					Longitude: 2 + float64(k)/1000, SpeedMps: &speed, ParentContentHash: "w1"}
			}
			points := func(from, to int, speed float64) []model.RoutePoint {
				var out []model.RoutePoint
				for k := from; k <= to; k++ {
					out = append(out, point(k, speed))
				}
				return out
			}
			wk := model.Workout{Provider: "dev", ActivityType: "running", StartDate: start, EndDate: start.Add(time.Hour), ContentHash: "w1"}

			c := e.writeAt(t2, &model.MapResult{Workouts: []model.Workout{wk}, RoutePoints: points(0, 4, 1)})
			assert.Equal(t, TableCount{Written: 5}, c[tRoutes])
			// An older part: the two new points land, the three it shares stay.
			c = e.writeAt(t1, &model.MapResult{RoutePoints: points(2, 6, 9)})
			assert.Equal(t, TableCount{Written: 2, Skipped: 3}, c[tRoutes])
			// A later copy of one point replaces it; the same copy again does not.
			c = e.writeAt(t3, &model.MapResult{RoutePoints: points(3, 3, 5)})
			assert.Equal(t, TableCount{Written: 1}, c[tRoutes])
			c = e.writeAt(t3, &model.MapResult{RoutePoints: points(3, 3, 5)})
			assert.Equal(t, TableCount{Skipped: 1}, c[tRoutes])
			// A copy whose capture is unknown never replaces a known one.
			c = e.writeAt(time.Time{}, &model.MapResult{RoutePoints: points(1, 1, 7)})
			assert.Equal(t, TableCount{Skipped: 1}, c[tRoutes])

			blobs := e.blobPoints(series.Route)
			require.Len(t, blobs, 1, "one blob for the workout")
			var speeds []float64
			for _, pts := range blobs {
				for _, p := range pts {
					speeds = append(speeds, p.V[series.RouteSpeed])
				}
			}
			assert.Equal(t, []float64{1, 1, 1, 5, 1, 9, 9}, speeds)
			assert.Equal(t, 7, e.count(tRoutes))
			var from, to time.Time
			var n int
			require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT start_at, end_at, point_count
				FROM ohs_data.series_blobs WHERE tenant_id = $1 AND kind = 2`, e.tenant).Scan(&from, &to, &n))
			assert.Equal(t, start, from.UTC())
			assert.Equal(t, start.Add(6*time.Second), to.UTC())
			assert.Equal(t, 7, n)
			assert.Zero(t, e.scalar(`SELECT count(*) FROM ohs_data.route_points`), "nothing is written per point")
		})
	}
}

// A heartbeat series uploaded in three objects (the second overlapping the
// first) is one blob holding each beat once, whether the job writes it in
// one batch or in chunks (OHS_ETL_TEST_CHUNK).
func TestSeriesHeartbeatAcrossObjects(t *testing.T) {
	e := newEnv(t)
	start := recent().Truncate(time.Second)
	e.register("hb", func(r connector.RawRecord) (model.MapResult, error) {
		k := int(r.Fields["k"].(float64))
		return model.MapResult{HeartbeatSamples: []model.HeartbeatSample{{SeriesID: "s1",
			Timestamp: start.Add(time.Duration(k) * 800 * time.Millisecond), RRMS: float64(780 + k)}}}, nil
	})
	lines := func(from, to int) []string {
		var out []string
		for k := from; k <= to; k++ {
			out = append(out, fmt.Sprintf(`{"k": %d}`, k))
		}
		return out
	}
	c := e.process(e.upload(e.tenant, e.user, "hb", "beats", lines(0, 9)...))
	assert.Equal(t, TableCount{Written: 10}, c[tHeartbeat])
	c = e.process(e.upload(e.tenant, e.user, "hb", "beats", lines(5, 14)...))
	assert.Equal(t, TableCount{Written: 5, Skipped: 5}, c[tHeartbeat])
	c = e.process(e.upload(e.tenant, e.user, "hb", "beats", lines(15, 19)...))
	assert.Equal(t, TableCount{Written: 5}, c[tHeartbeat])

	blobs := e.blobPoints(series.Heartbeat)
	require.Len(t, blobs, 1)
	pts := blobs["hb/s1"]
	require.Len(t, pts, 20)
	for k, p := range pts {
		assert.Equal(t, start.Add(time.Duration(k)*800*time.Millisecond), p.Time())
		assert.EqualValues(t, 780+k, p.V[series.HeartbeatRR])
	}
	assert.Equal(t, 20, e.count(tHeartbeat))
}
