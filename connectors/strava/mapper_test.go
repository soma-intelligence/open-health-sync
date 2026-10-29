// SPDX-License-Identifier: AGPL-3.0-only

package strava

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/strava/stravafake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// fixture returns the fixture activity at i as parsed fields, as the ETL
// hands them to Map.
func fixture(t *testing.T, i int) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(stravafake.Activities()[i], &m))
	return m
}

func mapOne(t *testing.T, fields map[string]any) model.Workout {
	t.Helper()
	res, err := New(Options{}).Map(connector.RawRecord{UserID: "u1", Provider: Slug, DataType: DataTypeActivity, Fields: fields})
	require.NoError(t, err)
	require.Len(t, res.Workouts, 1)
	return res.Workouts[0]
}

func TestMapActivity_Run(t *testing.T) {
	w := mapOne(t, fixture(t, 0))
	start := time.Date(2026, 3, 1, 7, 30, 0, 0, time.UTC)
	assert.Equal(t, "u1", w.UserID)
	assert.Equal(t, Slug, w.Provider)
	assert.Equal(t, "90000000001", w.ExternalID)
	assert.Equal(t, "running", w.ActivityType)
	assert.True(t, w.StartDate.Equal(start))
	assert.True(t, w.EndDate.Equal(start.Add(3120*time.Second)), "end = start + elapsed_time")
	require.NotNil(t, w.DurationS)
	assert.Equal(t, 3000, *w.DurationS, "duration = moving_time")
	assert.Equal(t, 10000.0, *w.DistanceM)
	assert.Equal(t, 150.0, *w.AvgHeartRate)
	assert.Equal(t, 175.0, *w.MaxHeartRate)
	assert.Equal(t, 3.333, *w.AvgSpeedMps)
	assert.Equal(t, 85.0, *w.ElevationGainM)
	assert.Nil(t, w.ActiveEnergyKcal, "no calories and no kilojoules")
	assert.True(t, w.Replaceable)
	assert.NotEmpty(t, w.ContentHash)

	md := w.Metadata
	assert.Equal(t, "Europe/Paris", md["timezone"])
	assert.Equal(t, "running", md["activity_type"])
	assert.Equal(t, "Run", md["strava_sport_type"])
	assert.Equal(t, 3120, md["elapsed_time_s"])
	assert.Equal(t, 3000, md["moving_time_s"])
	assert.NotContains(t, md, "indoor")
	assert.NotContains(t, md, "name", "the athlete's free text is not copied")
	assert.NotContains(t, md, "map", "the polyline is not copied")
}

func TestMapActivity_VirtualRideEnergyFromKilojoules(t *testing.T) {
	w := mapOne(t, fixture(t, 1))
	assert.Equal(t, "cycling", w.ActivityType)
	assert.Equal(t, true, w.Metadata["indoor"])
	require.NotNil(t, w.ActiveEnergyKcal)
	assert.Equal(t, 720.0, *w.ActiveEnergyKcal)
	assert.Equal(t, "kilojoules", w.Metadata["active_energy_source"])
	assert.Equal(t, 200.0, w.Metadata["average_watts"])
	assert.Nil(t, w.AvgHeartRate)
}

func TestMapActivity_YogaDropsZeroDistance(t *testing.T) {
	w := mapOne(t, fixture(t, 2))
	assert.Equal(t, "yoga", w.ActivityType)
	assert.Nil(t, w.DistanceM)
	assert.Nil(t, w.AvgSpeedMps)
	assert.Equal(t, 0.0, *w.ElevationGainM)
	assert.Equal(t, "Europe/London", w.Metadata["timezone"])
	assert.Equal(t, true, w.Metadata["manual"])
}

func TestMapActivity_LegacyTypeAndCalories(t *testing.T) {
	w := mapOne(t, fixture(t, 3))
	assert.Equal(t, "hiking", w.ActivityType, "type is used when sport_type is absent")
	assert.Equal(t, 900.0, *w.ActiveEnergyKcal)
	assert.Equal(t, "calories", w.Metadata["active_energy_source"])
	assert.Equal(t, "America/Los_Angeles", w.Metadata["timezone"])
}

// An edited activity (new type, new distance) keeps its identity, so the
// writer replaces the stored workout instead of adding one.
func TestMapActivity_EditKeepsTheHash(t *testing.T) {
	a := fixture(t, 0)
	b := fixture(t, 0)
	b["sport_type"] = "TrailRun"
	b["type"] = "Run"
	b["distance"] = 10500.0
	b["name"] = "renamed"
	wa, wb := mapOne(t, a), mapOne(t, b)
	assert.Equal(t, wa.ContentHash, wb.ContentHash)
	assert.Equal(t, 10500.0, *wb.DistanceM)
	assert.True(t, wb.Replaceable)

	c := fixture(t, 0)
	c["id"] = 90000000099
	assert.NotEqual(t, wa.ContentHash, mapOne(t, c).ContentHash, "another activity")
}

func TestMapActivity_Deterministic(t *testing.T) {
	for i := range stravafake.Activities() {
		a, b := mapOne(t, fixture(t, i)), mapOne(t, fixture(t, i))
		assert.Equal(t, a, b)
	}
}

func TestMap_UnknownDataTypeIsEmpty(t *testing.T) {
	res, err := New(Options{}).Map(connector.RawRecord{DataType: "stream", Fields: map[string]any{"x": 1}})
	require.NoError(t, err)
	assert.Equal(t, model.MapResult{}, res)
}

func TestMap_MalformedLineIsAnError(t *testing.T) {
	cases := map[string]map[string]any{
		"no id":          {"start_date": "2026-03-01T07:30:00Z"},
		"id not number":  {"id": "abc", "start_date": "2026-03-01T07:30:00Z"},
		"no start":       {"id": 1.0},
		"bad start":      {"id": 1.0, "start_date": "yesterday"},
		"distance typed": {"id": 1.0, "start_date": "2026-03-01T07:30:00Z", "distance": "far"},
	}
	for name, fields := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(Options{}).Map(connector.RawRecord{DataType: DataTypeActivity, Fields: fields})
			assert.Error(t, err)
		})
	}
}

// Figures Strava leaves out stay nil rather than zero.
func TestMapActivity_MissingFiguresSkipped(t *testing.T) {
	w := mapOne(t, map[string]any{"id": 5.0, "start_date": "2026-03-01T07:30:00Z", "sport_type": "Kitesurf"})
	assert.Equal(t, "surfing_sports", w.ActivityType)
	assert.True(t, w.EndDate.Equal(w.StartDate))
	assert.Nil(t, w.DurationS)
	assert.Nil(t, w.DistanceM)
	assert.Nil(t, w.ActiveEnergyKcal)
	assert.Nil(t, w.ElevationGainM)
	assert.NotContains(t, w.Metadata, "timezone")
}

// An elapsed or moving time too large for a time.Duration used to wrap:
// 2^55 + 3600 seconds put the end one hour after the start while the stored
// duration read 36028797018967568 s. Such a figure is not a duration.
func TestMapActivity_DurationThatOverflowsIsDropped(t *testing.T) {
	w := mapOne(t, map[string]any{"id": 5.0, "start_date": "2026-03-01T07:30:00Z", "sport_type": "Run",
		"elapsed_time": 36028797018967568.0, "moving_time": 36028797018967568.0})
	assert.True(t, w.EndDate.Equal(w.StartDate))
	assert.Nil(t, w.DurationS)
	assert.NotContains(t, w.Metadata, "elapsed_time_s")
	assert.NotContains(t, w.Metadata, "moving_time_s")
}

func TestSportType(t *testing.T) {
	assert.Equal(t, sport{activity: "cycling", indoor: true}, sportType("VirtualRide"))
	assert.Equal(t, "running", sportType("TrailRun").activity)
	assert.Equal(t, "other", sportType("Teleportation").activity)
	assert.Equal(t, "other", sportType("").activity)
	for name, s := range sportTypes {
		assert.NotEmpty(t, s.activity, name)
	}
}

func TestIANAZone(t *testing.T) {
	cases := map[string]string{
		"(GMT+01:00) Europe/Paris":        "Europe/Paris",
		"(GMT-08:00) America/Los_Angeles": "America/Los_Angeles",
		"(GMT+00:00) Etc/UTC":             "Etc/UTC",
		"Europe/Berlin":                   "Europe/Berlin",
		"(GMT+01:00) Mars/Olympus":        "",
		"(GMT+01:00)":                     "",
		"":                                "",
	}
	for in, want := range cases {
		assert.Equal(t, want, ianaZone(in), in)
	}
}
