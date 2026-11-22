// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package read

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// The reads of series held by blobs, and of series still held by per-point
// rows, page together: every beat, route point and voltage once, in order,
// whatever the page size.
func TestSeriesReadsPageAcrossBlobsAndRows(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	p, err := vault.OpenProvider(ctx, config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{6}, 32)})
	require.NoError(t, err)
	v := vault.New(pool, p, vault.Options{CanOpen: true})
	st := tenant.New(pool).WithVault(v)
	tn, _, err := st.CreateTenant(ctx, "series")
	require.NoError(t, err)
	u, err := st.CreateUser(ctx, tn.ID, "series")
	require.NoError(t, err)
	ctx = WithVault(ctx, v)

	t0 := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	sec := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

	// A heartbeat series, a workout route and an ECG written by the ETL:
	// sealed blobs.
	mr := &model.MapResult{}
	for k, rr := range []float64{800, 810, 805, 790, 820} {
		mr.HeartbeatSamples = append(mr.HeartbeatSamples, model.HeartbeatSample{SeriesID: "blob-a", Timestamp: sec(float64(k)), RRMS: rr})
	}
	wk := model.Workout{Provider: "apple_health", ActivityType: "running", StartDate: t0, EndDate: t0.Add(time.Hour), ContentHash: "wk"}
	mr.Workouts = []model.Workout{wk}
	for k := range 7 {
		at := sec(float64(k / 2)) // two fixes share each second
		alt := float64(30 + k)
		mr.RoutePoints = append(mr.RoutePoints, model.RoutePoint{Timestamp: at, Latitude: 48 + float64(k)/100,
			Longitude: 2 + float64(k)/100, AltitudeM: &alt, ParentContentHash: "wk"})
	}
	ecg := model.ECGRecording{StartDate: t0, EndDate: t0.Add(30 * time.Second), Classification: 1, SamplingFrequency: 512,
		NumberOfMeasurements: 4, ContentHash: "ecg"}
	mr.ECGRecordings = []model.ECGRecording{ecg}
	for k, uv := range []float64{-12.5, 40.125, 7, 3.25} {
		mr.ECGSamples = append(mr.ECGSamples, model.ECGVoltageSample{TimeSinceSampleStart: float64(k) / 512, VoltageUV: uv,
			ParentContentHash: "ecg"})
	}
	_, err = etl.WriteSynthetic(ctx, pool, v, tn.ID, u.ID, "apple_health", mr)
	require.NoError(t, err)

	// A heartbeat series a release before stored as rows: beats at the
	// times of the blob's, two of them at one time.
	for i, b := range []struct {
		at float64
		rr float64
	}{{1, 900}, {1, 880}, {2, 870}, {4.5, 860}} {
		_, err := pool.Exec(ctx, `INSERT INTO ohs_data.heartbeat_samples (tenant_id, user_id, provider, series_id, "timestamp", rr_ms, content_hash)
			VALUES ($1, $2, 'polar', 'rows-b', $3, $4, $5)`, tn.ID, u.ID, sec(b.at), b.rr, string(rune('a'+i)))
		require.NoError(t, err)
	}

	// The RR intervals: the whole list, then pages of every size.
	all, err := RRIntervals(ctx, pool, tn.ID, u.ID, RRQuery{Limit: 100})
	require.NoError(t, err)
	require.Len(t, all.Records, 9)
	assert.Empty(t, all.NextCursor)
	assert.True(t, slices.IsSortedFunc(all.Records, rrCompare))
	assert.Equal(t, RRInterval{Timestamp: sec(1), RRMs: 810, SeriesID: "blob-a", Provider: "apple_health",
		IngestedAt: all.Records[1].IngestedAt}, all.Records[1])
	for size := 1; size <= 4; size++ {
		var got []RRInterval
		q := RRQuery{Limit: size}
		for range 20 {
			page, err := RRIntervals(ctx, pool, tn.ID, u.ID, q)
			require.NoError(t, err)
			got = append(got, page.Records...)
			if page.NextCursor == "" {
				break
			}
			q.Cursor = page.NextCursor
		}
		assert.Equal(t, all.Records, got, "pages of %d", size)
	}
	window, err := RRIntervals(ctx, pool, tn.ID, u.ID, RRQuery{Start: sec(1), End: sec(3), Limit: 100})
	require.NoError(t, err)
	assert.Len(t, window.Records, 5, "the beats in [1 s, 3 s) of both series")
	one, err := RRIntervals(ctx, pool, tn.ID, u.ID, RRQuery{SeriesID: "blob-a", Limit: 100})
	require.NoError(t, err)
	assert.Len(t, one.Records, 5)

	// HRV: one series per page, ordered by first beat.
	var series []string
	q := RRQuery{Limit: 1}
	for range 5 {
		page, err := HRV(ctx, pool, tn.ID, u.ID, q)
		require.NoError(t, err)
		for _, s := range page.Records {
			series = append(series, s.SeriesID)
		}
		if page.NextCursor == "" {
			break
		}
		q.Cursor = page.NextCursor
	}
	assert.Equal(t, []string{"blob-a", "rows-b"}, series)

	// The route: a point's id is its position, pages continue on (time, id).
	var workoutID int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM ohs_data.workouts WHERE user_id = $1`, u.ID).Scan(&workoutID))
	route, err := WorkoutRoute(ctx, pool, tn.ID, u.ID, workoutID, Query{Limit: 100})
	require.NoError(t, err)
	require.Len(t, route.Records, 7)
	for k, pt := range route.Records {
		assert.Equal(t, int64(k+1), pt.ID)
		assert.Equal(t, sec(float64(k/2)), pt.Timestamp)
		assert.Equal(t, 48+float64(k)/100, pt.Latitude)
		require.NotNil(t, pt.AltitudeM)
		assert.Equal(t, float64(30+k), *pt.AltitudeM)
		assert.Nil(t, pt.SpeedMps)
		assert.Equal(t, "apple_health", pt.Provider)
	}
	for size := 1; size <= 3; size++ {
		var got []RoutePoint
		q := Query{Limit: size}
		for range 20 {
			page, err := WorkoutRoute(ctx, pool, tn.ID, u.ID, workoutID, q)
			require.NoError(t, err)
			got = append(got, page.Records...)
			if page.NextCursor == "" {
				break
			}
			q.Cursor = page.NextCursor
		}
		assert.Equal(t, route.Records, got, "pages of %d", size)
	}
	within, err := WorkoutRoute(ctx, pool, tn.ID, u.ID, workoutID, Query{Start: sec(1), End: sec(2), Limit: 100})
	require.NoError(t, err)
	assert.Len(t, within.Records, 2)

	// The ECG: samples in offset order, a summary, and the cap.
	var ecgID int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM ohs_data.ecg_recordings WHERE user_id = $1`, u.ID).Scan(&ecgID))
	detail, err := ECGRecordingByID(ctx, pool, tn.ID, u.ID, ecgID)
	require.NoError(t, err)
	assert.Equal(t, ECGSamples{Count: 4, OffsetsS: []float64{0, 1.0 / 512, 2.0 / 512, 3.0 / 512},
		VoltagesUV: []float64{-12.5, 40.125, 7, 3.25}}, detail.Samples)
	sum, err := ECGRecordingSummary(ctx, pool, tn.ID, u.ID, ecgID)
	require.NoError(t, err)
	assert.Equal(t, 4, sum.StoredSamples)
	assert.Equal(t, -12.5, *sum.MinVoltageUV)
	assert.Equal(t, 40.125, *sum.MaxVoltageUV)
	assert.Equal(t, 3.0/512, *sum.LastOffsetS)
	was := MaxECGSamples
	MaxECGSamples = 2
	t.Cleanup(func() { MaxECGSamples = was })
	capped, err := ECGRecordingByID(ctx, pool, tn.ID, u.ID, ecgID)
	require.NoError(t, err)
	assert.Equal(t, ECGSamples{Count: 2, Truncated: true, OffsetsS: []float64{0, 1.0 / 512}, VoltagesUV: []float64{-12.5, 40.125}},
		capped.Samples)
}
