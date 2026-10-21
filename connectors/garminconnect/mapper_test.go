// SPDX-License-Identifier: AGPL-3.0-only

package garminconnect

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/garminconnect/garminconnectfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const testUserID = "00000000-0000-0000-0000-000000000001"

// mapFields maps one parsed line of dataType as the ETL would.
func mapFields(t *testing.T, dataType string, line []byte) model.MapResult {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal(line, &fields))
	res, err := New(Options{}).Map(connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: dataType, Fields: fields})
	require.NoError(t, err)
	return res
}

// firstDaily returns the first day object of the dailies fixture.
func firstDaily(t *testing.T) []byte {
	t.Helper()
	var wrap struct {
		Data []json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(garminconnectfake.Fixture(t, "dailies.json"), &wrap))
	require.NotEmpty(t, wrap.Data)
	return wrap.Data[0]
}

func TestMapDaily(t *testing.T) {
	res := mapFields(t, DataTypeDailies, firstDaily(t))

	require.Len(t, res.DailySummaries, 1)
	sum := res.DailySummaries[0]
	require.NotNil(t, sum.StepCount)
	assert.Equal(t, 10000, *sum.StepCount)
	require.NotNil(t, sum.RestingHR)
	assert.InDelta(t, 52, *sum.RestingHR, 0.001)
	require.NotNil(t, sum.TotalKcal)
	assert.InDelta(t, 2100, *sum.TotalKcal, 0.001)

	got := map[string]float64{}
	for _, s := range res.QuantitySamples {
		got[s.QuantityType] = s.Value
	}
	assert.InDelta(t, 10000, got[model.QTStepCount], 0.001)
	assert.InDelta(t, 52, got[model.QTRestingHeartRate], 0.001)
	assert.InDelta(t, 4800, got[model.QTStressHighTime], 0.001)
}

func TestMapSleep(t *testing.T) {
	res := mapFields(t, DataTypeSleeps, garminconnectfake.Fixture(t, "sleep.json"))

	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	require.NotNil(t, s.DeepSleepSeconds)
	assert.Equal(t, 6000, *s.DeepSleepSeconds)
	require.NotNil(t, s.SleepScore)
	assert.InDelta(t, 82, *s.SleepScore, 0.001)
	require.NotNil(t, s.HRVAvgMs)
	assert.InDelta(t, 55, *s.HRVAvgMs, 0.001)
	assert.True(t, s.BedtimeEnd.After(s.BedtimeStart))

	// One phase per stage interval, each tied to the session.
	assert.Len(t, res.SleepPhases, 5)
	for _, p := range res.SleepPhases {
		assert.Equal(t, s.ContentHash, p.ParentContentHash)
		assert.True(t, p.EndDate.After(p.StartDate))
	}

	// A nightly HRV sample is emitted from the sleep HRV.
	var hrv int
	for _, q := range res.QuantitySamples {
		if q.QuantityType == model.QTHRVRMSSD {
			hrv++
		}
	}
	assert.Equal(t, 1, hrv)
}

func TestMapHRV(t *testing.T) {
	res := mapFields(t, DataTypeHRV, garminconnectfake.Fixture(t, "hrv.json"))
	require.Len(t, res.QuantitySamples, 1)
	assert.Equal(t, model.QTHRVRMSSD, res.QuantitySamples[0].QuantityType)
	assert.InDelta(t, 58, res.QuantitySamples[0].Value, 0.001)
}

func TestMapActivity(t *testing.T) {
	var list []json.RawMessage
	require.NoError(t, json.Unmarshal(garminconnectfake.Fixture(t, "activities.json"), &list))
	require.NotEmpty(t, list)

	res := mapFields(t, DataTypeActivities, list[0])
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	assert.Equal(t, "running", w.ActivityType)
	assert.Equal(t, "123456", w.ExternalID)
	require.NotNil(t, w.DistanceM)
	assert.InDelta(t, 5000, *w.DistanceM, 0.001)
	require.NotNil(t, w.DurationS)
	assert.Equal(t, 1800, *w.DurationS)
	assert.Equal(t, "Garmin 3113944", w.DeviceID)
}

// TestUnitsConvertToStorage runs CanonicalizeUnits over every fixture's
// mapped result: no sample carries a unit the pipeline cannot store.
func TestUnitsConvertToStorage(t *testing.T) {
	results := []model.MapResult{
		mapFields(t, DataTypeDailies, firstDaily(t)),
		mapFields(t, DataTypeSleeps, garminconnectfake.Fixture(t, "sleep.json")),
		mapFields(t, DataTypeHRV, garminconnectfake.Fixture(t, "hrv.json")),
	}
	for _, res := range results {
		res := res
		errs := model.CanonicalizeUnits(&res)
		assert.Empty(t, errs)
	}
}

func TestMapUnknownType(t *testing.T) {
	res, err := New(Options{}).Map(connector.RawRecord{UserID: testUserID, DataType: "nope", Fields: map[string]any{}})
	require.NoError(t, err)
	assert.True(t, res.IsEmpty())
}
