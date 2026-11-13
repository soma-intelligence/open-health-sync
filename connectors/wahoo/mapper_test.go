// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/wahoo/wahoofake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func fields(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func mapLine(t *testing.T, dataType string, raw []byte) model.MapResult {
	t.Helper()
	res, err := New(Options{}).Map(connector.RawRecord{UserID: "u1", Provider: Slug, DataType: dataType, Fields: fields(t, raw)})
	require.NoError(t, err)
	return res
}

// The workout of Wahoo's reference example (GET /v1/workouts/:id).
func TestMapWorkout_DocumentedExample(t *testing.T) {
	res := mapLine(t, DataTypeWorkout, wahoofake.File("workout.json"))
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	start := time.Date(2015, 8, 12, 9, 0, 0, 0, time.UTC)
	assert.Equal(t, "u1", w.UserID)
	assert.Equal(t, Slug, w.Provider)
	assert.Equal(t, "56519", w.ExternalID)
	assert.Equal(t, "surfing_sports", w.ActivityType, "type 40 is KITEBOARDING")
	assert.True(t, w.StartDate.Equal(start))
	assert.True(t, w.EndDate.Equal(start.Add(275*time.Second)), "end = start + duration_total_accum")
	require.NotNil(t, w.DurationS)
	assert.Equal(t, 179, *w.DurationS, "duration = duration_active_accum")
	assert.Equal(t, 24909.71, *w.DistanceM)
	assert.Equal(t, 1500.0, *w.ActiveEnergyKcal)
	assert.Equal(t, 100.0, *w.AvgHeartRate)
	assert.Equal(t, 10.75, *w.AvgSpeedMps)
	assert.Equal(t, 450.0, *w.ElevationGainM)
	assert.Nil(t, w.MaxHeartRate, "Wahoo gives no maximum")
	assert.True(t, w.Replaceable)
	assert.NotEmpty(t, w.ContentHash)

	md := w.Metadata
	assert.Equal(t, "America/Denver", md["timezone"])
	assert.Equal(t, "KITEBOARDING", md["wahoo_workout_type"])
	assert.Equal(t, 40, md["wahoo_workout_type_id"])
	assert.Equal(t, 94.59, md["average_watts"])
	assert.Equal(t, 150.0, md["normalized_power_w"])
	assert.Equal(t, 304.9, md["training_stress_score"])
	assert.Equal(t, 50.0, md["average_cadence_rpm"])
	assert.Equal(t, 1041480.0, md["work_j"])
	assert.Equal(t, 95, md["paused_time_s"])
	assert.Equal(t, 1002, md["wahoo_fitness_app_id"])
	assert.Equal(t, false, md["manual"])
	assert.NotContains(t, md, "name", "the user's free text is not copied")
	assert.NotContains(t, md, "file", "the FIT link is not copied")
}

// A listed workout without a summary carries its minutes only.
func TestMapWorkout_WithoutSummary(t *testing.T) {
	res := mapLine(t, DataTypeWorkout, wahoofake.Workouts()[0])
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	require.NotNil(t, w.DurationS)
	assert.Equal(t, 720, *w.DurationS, "12 minutes")
	assert.True(t, w.EndDate.Equal(w.StartDate.Add(12*time.Minute)))
	assert.Nil(t, w.DistanceM)
	assert.Nil(t, w.AvgHeartRate)
}

// The webhook of Wahoo's reference gives the same workout (same hash) as a
// pull, so one replaces the other.
func TestMapWebhook_SameWorkoutAsAPull(t *testing.T) {
	pulled := mapLine(t, DataTypeWorkout, wahoofake.File("workout.json")).Workouts[0]
	res := mapLine(t, DataTypeWebhook, wahoofake.File("webhook.json"))
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	assert.Equal(t, pulled.ContentHash, w.ContentHash)
	assert.Equal(t, "56519", w.ExternalID)
	assert.Equal(t, 124.23, *w.AvgHeartRate)
	assert.Equal(t, 179, *w.DurationS)
	assert.NotContains(t, w.Metadata, "timezone", "the webhook summary has no time_zone")
}

func TestMapUser(t *testing.T) {
	res := mapLine(t, DataTypeUser, wahoofake.File("user.json"))
	require.Len(t, res.QuantitySamples, 2)
	at := time.Date(2018, 10, 24, 20, 46, 40, 0, time.UTC)
	byType := map[string]model.QuantitySample{}
	for _, s := range res.QuantitySamples {
		byType[s.QuantityType] = s
		assert.True(t, s.StartDate.Equal(at))
		assert.Equal(t, Slug, s.Provider)
	}
	assert.Equal(t, 80.0, byType["body_mass"].Value)
	assert.Equal(t, "kg", byType["body_mass"].Unit)
	assert.Equal(t, 2.0, byType["height"].Value)
	assert.Equal(t, "m", byType["height"].Unit)
	// Metres, like WHOOP's: the unit pass stores height in centimetres.
	u, ok := model.StorageUnit("body_mass")
	require.True(t, ok)
	assert.Equal(t, u, byType["body_mass"].Unit)

	empty := mapLine(t, DataTypeUser, []byte(`{"id":1,"weight":"0.0","updated_at":"2018-10-24T20:46:40.000Z"}`))
	assert.Empty(t, empty.QuantitySamples, "a zero weight is no weight")
	undated := mapLine(t, DataTypeUser, []byte(`{"id":1,"weight":"80.0"}`))
	assert.Empty(t, undated.QuantitySamples)
}

func TestMap_RefusesWhatItCannotPlace(t *testing.T) {
	c := New(Options{})
	for name, raw := range map[string]string{
		"no id":           `{"starts":"2026-03-01T10:00:00.000Z"}`,
		"bad start":       `{"id":1,"starts":"yesterday"}`,
		"bad decimal":     `{"id":1,"starts":"2026-03-01T10:00:00.000Z","workout_summary":{"distance_accum":"far"}}`,
		"not a decimal":   `{"id":1,"starts":"2026-03-01T10:00:00.000Z","minutes":"NaN"}`,
		"webhook no work": `{"event_type":"workout_summary","workout_summary":{"id":1}}`,
	} {
		dt := DataTypeWorkout
		if name == "webhook no work" {
			dt = DataTypeWebhook
		}
		_, err := c.Map(connector.RawRecord{UserID: "u1", DataType: dt, Fields: fields(t, []byte(raw))})
		assert.Error(t, err, name)
	}
	res, err := c.Map(connector.RawRecord{UserID: "u1", DataType: "plans", Fields: map[string]any{"id": 1}})
	require.NoError(t, err)
	assert.Empty(t, res.Workouts, "an unknown data type gives nothing")
}

func TestMapWorkout_UnknownTypeAndBounds(t *testing.T) {
	raw := wahoofake.Edit(wahoofake.File("workout.json"), map[string]any{"workout_type_id": 255, "minutes": 99999999})
	w := mapLine(t, DataTypeWorkout, raw).Workouts[0]
	assert.Equal(t, "other", w.ActivityType)
	assert.Equal(t, 255, w.Metadata["wahoo_workout_type_id"])

	indoor := wahoofake.Edit(wahoofake.File("workout.json"), map[string]any{"workout_type_id": 61})
	w = mapLine(t, DataTypeWorkout, indoor).Workouts[0]
	assert.Equal(t, "cycling", w.ActivityType)
	assert.Equal(t, true, w.Metadata["indoor"])
}
