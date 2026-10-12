// SPDX-License-Identifier: AGPL-3.0-only

package omh

import (
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata/golden")

var (
	testUser = uuid.MustParse("3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11")
	t0       = time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	ingested = time.Date(2026, 3, 1, 9, 15, 30, 250000000, time.UTC)
)

func ptr[T any](v T) *T { return &v }

func prov() read.Provenance {
	return read.Provenance{Provider: "apple_health", Source: ptr("apple"), Device: ptr("apple_watch"), IngestedAt: ingested}
}

// sample is an invented quantity sample stored in unit.
func sample(id int64, qt string, value float64, unit string) read.CodedSample {
	return read.CodedSample{Sample: read.Sample{
		ID: id, Type: qt, Value: value, Unit: ptr(unit), StartDate: t0, EndDate: ptr(t0), Provenance: prov(),
	}}
}

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')
	path := filepath.Join("testdata", "golden", name+".json")
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test ./internal/omh -update")
	assert.Equal(t, string(want), string(got))
}

func mustMap(t *testing.T, r read.ObservationRecord) DataPoint {
	t.Helper()
	d, ok := FromRecord(testUser, r)
	require.True(t, ok)
	return d
}

func quantityRecord(s read.CodedSample) read.ObservationRecord {
	return read.ObservationRecord{Kind: read.KindQuantity, Sample: &s}
}

func TestQuantitySamples(t *testing.T) {
	hr := sample(101, "heart_rate", 62, "bpm")
	hr.ExternalID = ptr("hk-uuid-1")
	hr.WasUserEntered = ptr(false)
	golden(t, "heart-rate", mustMap(t, quantityRecord(hr)))

	resting := sample(102, "resting_heart_rate", 51, "bpm")
	golden(t, "heart-rate-resting", mustMap(t, quantityRecord(resting)))

	steps := sample(103, "step_count", 1234, "count")
	steps.EndDate = ptr(t0.Add(time.Hour))
	golden(t, "step-count", mustMap(t, quantityRecord(steps)))

	weight := sample(104, "body_mass", 72.4, "kg")
	weight.WasUserEntered = ptr(true)
	golden(t, "body-weight", mustMap(t, quantityRecord(weight)))

	for _, c := range []struct {
		name, qt, unit string
		v              float64
	}{
		{"body-height", "height", "cm", 178},
		{"body-mass-index", "body_mass_index", "kg/m2", 22.9},
		{"body-fat-percentage", "body_fat_percentage", "percent", 18.5},
		{"body-temperature", "body_temperature", "celsius", 36.8},
		{"oxygen-saturation", "oxygen_saturation", "percent", 97},
		{"respiratory-rate", "respiratory_rate", "bpm", 14},
		{"blood-glucose", "blood_glucose", "mg/dL", 95},
		{"calories-burned", "active_energy_burned", "kcal", 12.5},
	} {
		golden(t, c.name, mustMap(t, quantityRecord(sample(200, c.qt, c.v, c.unit))))
	}
}

func TestBloodPressure(t *testing.T) {
	sys := sample(301, read.Systolic, 120, "mmHg")
	dia := sample(302, read.Diastolic, 80, "mmHg")
	dia.IngestedAt = ingested.Add(time.Second)
	d := mustMap(t, read.ObservationRecord{Kind: read.KindQuantity, Sample: &sys, Partner: &dia})
	assert.Equal(t, "bp-301", d.Header.ID)
	assert.Equal(t, "2026-03-01T09:15:31.25Z", d.Header.CreationDateTime, "the later of the two halves")
	golden(t, "blood-pressure", d)

	dia.Unit = ptr("kPa")
	_, ok := FromRecord(testUser, read.ObservationRecord{Kind: read.KindQuantity, Sample: &sys, Partner: &dia})
	assert.False(t, ok, "a half in another unit")
}

func TestSleepSession(t *testing.T) {
	s := read.SleepSession{ID: 7, BedtimeStart: t0, BedtimeEnd: ptr(t0.Add(8 * time.Hour)),
		TotalSleepSeconds: ptr(25200), ExternalID: ptr("oura-sleep-1"), Provenance: prov()}
	golden(t, "sleep-episode", mustMap(t, read.ObservationRecord{Kind: read.KindSleep, Sleep: &s}))

	open := read.SleepSession{ID: 8, BedtimeStart: t0, Provenance: prov()}
	d := mustMap(t, read.ObservationRecord{Kind: read.KindSleep, Sleep: &open})
	assert.Equal(t, map[string]any{"time_interval": map[string]string{
		"start_date_time": "2026-03-01T08:00:00Z", "end_date_time": "2026-03-01T08:00:00Z"}},
		d.Body["effective_time_frame"], "sleep-episode requires an interval")
	assert.NotContains(t, d.Body, "total_sleep_time")
	golden(t, "sleep-episode-no-end", d)
}

func TestWorkout(t *testing.T) {
	w := read.Workout{ID: 9, ActivityType: "trail_running", StartDate: t0, EndDate: ptr(t0.Add(45 * time.Minute)),
		DistanceM: ptr(8200.0), ActiveEnergyKcal: ptr(610.0), Provenance: prov()}
	golden(t, "physical-activity", mustMap(t, read.ObservationRecord{Kind: read.KindWorkout, Workout: &w}))

	bare := read.Workout{ID: 10, StartDate: t0, DistanceM: ptr(math.NaN()), Provenance: prov()}
	d := mustMap(t, read.ObservationRecord{Kind: read.KindWorkout, Workout: &bare})
	assert.Equal(t, "workout", d.Body["activity_name"])
	assert.NotContains(t, d.Body, "distance")
	golden(t, "physical-activity-bare", d)
}

func TestNoDataPoint(t *testing.T) {
	for name, r := range map[string]read.ObservationRecord{
		"type without a schema": quantityRecord(sample(1, "hrv_sdnn", 40, "ms")),
		"another unit":          quantityRecord(sample(2, "body_mass", 160, "lb")),
		"no unit":               quantityRecord(read.CodedSample{Sample: read.Sample{ID: 3, Type: "heart_rate", Value: 60, StartDate: t0}}),
		"NaN":                   quantityRecord(sample(4, "heart_rate", math.NaN(), "bpm")),
		"infinite":              quantityRecord(sample(5, "heart_rate", math.Inf(1), "bpm")),
		"lone diastolic half":   quantityRecord(sample(6, read.Diastolic, 80, "mmHg")),
		"daily summary":         {Kind: read.KindDaily, Daily: &read.DailySummary{ID: 1, Date: "2026-03-01"}},
	} {
		_, ok := FromRecord(testUser, r)
		assert.False(t, ok, name)
	}
}

func TestSchemaByName(t *testing.T) {
	for in, want := range map[string]Schema{
		"heart-rate":         HeartRate,
		"omh:heart-rate":     HeartRate,
		"omh:heart-rate:2.0": HeartRate,
		"blood-pressure:3.1": BloodPressure,
		"sleep-episode":      SleepEpisode,
	} {
		got, ok := SchemaByName(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "heart_rate", "omh:heart-rate:3.0", "ieee:heart-rate", "hrv"} {
		_, ok := SchemaByName(in)
		assert.False(t, ok, in)
	}
}

func TestFilter(t *testing.T) {
	all := Filter(nil)
	assert.True(t, all.Quantities && all.Sleep && all.Workouts)
	assert.False(t, all.Daily)
	assert.Contains(t, all.Types, read.Diastolic, "brings the blood pressure panels in")
	assert.Contains(t, all.Types, "heart_rate")
	assert.NotContains(t, all.Types, "hrv_sdnn")

	hr := Filter(&HeartRate)
	assert.Equal(t, []string{"heart_rate", "resting_heart_rate"}, hr.Types)
	assert.False(t, hr.Sleep || hr.Workouts || hr.Daily)

	bp := Filter(&BloodPressure)
	assert.True(t, bp.Quantities && bp.PanelsOnly)

	sleep := Filter(&SleepEpisode)
	assert.False(t, sleep.Quantities)
	assert.True(t, sleep.Sleep)
	assert.False(t, sleep.Workouts)
}

// TestEveryQuantityTypeIsInTheCatalog: the table only names types the
// seeded catalog has, with the storage unit the catalog gives them.
func TestEveryQuantityTypeIsInTheCatalog(t *testing.T) {
	seeds, err := filepath.Glob(filepath.Join("..", "db", "migrations", "*seed*.up.sql"))
	require.NoError(t, err)
	var all string
	for _, f := range seeds {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		all += string(b)
	}
	for qt, q := range quantities {
		assert.Regexp(t, `\('`+qt+`', [^\n]*'`+q.storageUnit+`', '(num|code)'`, all, qt)
	}
}
