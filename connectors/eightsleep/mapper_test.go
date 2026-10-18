// SPDX-License-Identifier: AGPL-3.0-only

package eightsleep

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/eightsleep/eightsleepfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func byType(res model.MapResult) map[string][]model.QuantitySample {
	out := map[string][]model.QuantitySample{}
	for _, s := range res.QuantitySamples {
		out[s.QuantityType] = append(out[s.QuantityType], s)
	}
	return out
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestMapNight(t *testing.T) {
	res := mapLine(t, eightsleepfake.Days()[0])

	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	assert.Equal(t, "eight_sleep", s.Provider)
	assert.Equal(t, "session_1772317800", s.ExternalID)
	assert.Equal(t, at("2026-02-28T22:30:00Z"), s.BedtimeStart)
	assert.Equal(t, at("2026-03-01T06:45:00Z"), s.BedtimeEnd, "the trailing out of bed is not in the night")
	assert.Equal(t, 26100, *s.TotalSleepSeconds)
	assert.Equal(t, 13500, *s.LightSleepSeconds)
	assert.Equal(t, 5400, *s.DeepSleepSeconds)
	assert.Equal(t, 7200, *s.REMSleepSeconds)
	assert.Equal(t, 3600, *s.AwakeSeconds)
	assert.Equal(t, 82.0, *s.SleepScore)
	assert.Equal(t, 48.5, *s.HRVAvgMs)
	assert.Equal(t, 14.2, *s.RespiratoryRate, "Eight Sleep's own figure wins over the readings' average")
	assert.Nil(t, s.RestingHR)
	assert.True(t, s.Replaceable)
	assert.NotEmpty(t, s.ContentHash)

	md := s.Metadata
	assert.Equal(t, "2026-03-01", md["date"])
	assert.Equal(t, 85.0, md["sleep_quality_score"])
	assert.Equal(t, 76.0, md["sleep_routine_score"])
	assert.Equal(t, 14.0, md["toss_and_turns"])
	assert.Equal(t, 29.67, md["bed_temperature_avg_c"])
	assert.Equal(t, 29.0, md["bed_temperature_min_c"])
	assert.Equal(t, 30.5, md["bed_temperature_max_c"])
	assert.Equal(t, 19.0, md["room_temperature_avg_c"])
	assert.Equal(t, 18.5, md["room_temperature_min_c"])
	assert.Equal(t, 19.5, md["room_temperature_max_c"])
	assert.NotContains(t, md, "processing")

	require.Len(t, res.SleepPhases, 10)
	want := []struct {
		phase      string
		start, end string
	}{
		{model.SleepStageAwake, "2026-02-28T22:30:00Z", "2026-02-28T22:45:00Z"},
		{model.SleepStageLight, "2026-02-28T22:45:00Z", "2026-03-01T00:15:00Z"},
		{model.SleepStageDeep, "2026-03-01T00:15:00Z", "2026-03-01T01:15:00Z"},
		{model.SleepStageREM, "2026-03-01T01:15:00Z", "2026-03-01T02:15:00Z"},
	}
	for i, w := range want {
		p := res.SleepPhases[i]
		assert.Equal(t, w.phase, p.Phase, i)
		assert.Equal(t, at(w.start), p.StartDate, i)
		assert.Equal(t, at(w.end), p.EndDate, i)
		assert.Equal(t, int(p.EndDate.Sub(p.StartDate)/time.Second), p.DurationS)
		assert.Equal(t, s.ContentHash, p.ParentContentHash)
	}
	assert.Equal(t, at("2026-03-01T06:45:00Z"), res.SleepPhases[9].EndDate)

	q := byType(res)
	hr := q[model.QTHeartRate]
	require.Len(t, hr, 3, "the zero reading is dropped")
	assert.Equal(t, 62.0, hr[0].Value)
	assert.Equal(t, "bpm", hr[0].Unit)
	assert.Equal(t, at("2026-02-28T22:35:00Z"), hr[0].StartDate)
	assert.Equal(t, hr[0].StartDate, hr[0].EndDate)
	assert.Equal(t, "Eight Sleep", hr[0].SourceName)
	assert.Equal(t, "eight_sleep", hr[0].SourceSlug)
	assert.Equal(t, "1772317800", hr[0].Metadata["session_id"])
	assert.False(t, hr[0].Replaceable)

	rr := q[model.QTRespiratoryRate]
	require.Len(t, rr, 3)
	assert.Equal(t, 13.5, rr[1].Value)
	assert.Equal(t, "bpm", rr[1].Unit)

	hrv := q[model.QTHRVRMSSD]
	require.Len(t, hrv, 1, "a nightly HRV sample when the night has no HRV readings")
	assert.Equal(t, 48.5, hrv[0].Value)
	assert.Equal(t, "ms", hrv[0].Unit)
	assert.Equal(t, s.BedtimeStart, hrv[0].StartDate)
	assert.Equal(t, s.BedtimeEnd, hrv[0].EndDate)
	assert.Equal(t, "2026-03-01", hrv[0].Metadata[model.MetadataSummaryDate])
	assert.True(t, hrv[0].Replaceable)
}

func TestMapNapAndProcessingNight(t *testing.T) {
	res := mapLine(t, eightsleepfake.Days()[1])
	require.Len(t, res.SleepSessions, 2)

	nap, night := res.SleepSessions[0], res.SleepSessions[1]
	assert.Equal(t, "session_1772380800", nap.ExternalID, "a numeric id")
	assert.Equal(t, 1800, *nap.TotalSleepSeconds)
	assert.Equal(t, at("2026-03-01T16:35:00Z"), nap.BedtimeEnd)
	assert.Nil(t, nap.SleepScore)
	assert.Nil(t, nap.RespiratoryRate, "the night's figures go on the longest session only")
	assert.NotContains(t, nap.Metadata, "toss_and_turns")

	assert.Equal(t, "session_2026-03-01T23:00:00Z", night.ExternalID, "no id: the start")
	assert.Equal(t, 18000, *night.TotalSleepSeconds, "the stage without a duration is skipped")
	assert.Nil(t, night.REMSleepSeconds)
	assert.Nil(t, night.AwakeSeconds)
	assert.Nil(t, night.SleepScore, `"None" is no score`)
	assert.Equal(t, 45.0, *night.HRVAvgMs, "no nightly figure: the readings' average")
	assert.Equal(t, 13.8, *night.RespiratoryRate)
	assert.Equal(t, true, night.Metadata["processing"])
	assert.Equal(t, 9.0, night.Metadata["toss_and_turns"])
	assert.NotEqual(t, nap.ContentHash, night.ContentHash)

	q := byType(res)
	assert.Len(t, q[model.QTHRVRMSSD], 2, `"None" readings dropped, no nightly sample next to readings`)
	assert.Empty(t, q[model.QTHeartRate], "a reading without a usable time is dropped")
}

func TestMapHashIgnoresFiguresAndKeepsSessions(t *testing.T) {
	line := eightsleepfake.Days()[0]
	a := mapLine(t, line)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(line, &obj))
	obj["score"] = 90
	sess := obj["sessions"].([]any)[0].(map[string]any)
	sess["timeseries"].(map[string]any)["heartRate"] = []any{[]any{"2026-02-28T22:35:00.000Z", 63}}
	changed, err := json.Marshal(obj)
	require.NoError(t, err)
	b := mapLine(t, changed)
	assert.Equal(t, a.SleepSessions[0].ContentHash, b.SleepSessions[0].ContentHash, "a re-scored night replaces the stored one")
	assert.Equal(t, 90.0, *b.SleepSessions[0].SleepScore)
	assert.Equal(t, byType(a)[model.QTHeartRate][0].ContentHash, byType(b)[model.QTHeartRate][0].ContentHash,
		"a reading is hashed on its time, not its value")

	other := New(Options{})
	res, err := other.Map(connector.RawRecord{UserID: "00000000-0000-0000-0000-000000000002", DataType: DataTypeTrends, Fields: obj})
	require.NoError(t, err)
	assert.NotEqual(t, a.SleepSessions[0].ContentHash, res.SleepSessions[0].ContentHash, "another user, another hash")
}

func TestMapUnitsAreStorageUnits(t *testing.T) {
	for _, line := range eightsleepfake.Days() {
		for _, s := range mapLine(t, line).QuantitySamples {
			want, ok := model.StorageUnit(s.QuantityType)
			require.True(t, ok, s.QuantityType)
			assert.Equal(t, want, s.Unit, s.QuantityType)
		}
	}
}

func TestMapRefusals(t *testing.T) {
	c := New(Options{})
	_, err := c.Map(connector.RawRecord{UserID: testUserID, DataType: DataTypeTrends, Fields: map[string]any{"day": "yesterday"}})
	require.Error(t, err)

	res, err := c.Map(connector.RawRecord{UserID: testUserID, DataType: DataTypeTrends, Fields: map[string]any{"day": "2026-03-01"}})
	require.NoError(t, err)
	assert.Empty(t, res.SleepSessions)

	res, err = c.Map(connector.RawRecord{UserID: testUserID, DataType: "something_else", Fields: map[string]any{"day": "2026-03-01"}})
	require.NoError(t, err)
	assert.Empty(t, res.SleepSessions)

	// Odd shapes carry nothing rather than failing the night.
	res, err = c.Map(connector.RawRecord{UserID: testUserID, DataType: DataTypeTrends, Fields: map[string]any{
		"day": "2026-03-01",
		"sessions": []any{map[string]any{
			"ts":         "2026-03-01T00:00:00Z",
			"stages":     []any{map[string]any{"stage": "light", "duration": -5}, map[string]any{"stage": "out", "duration": 600}},
			"timeseries": map[string]any{"heartRate": "not a series"},
		}},
	}})
	require.NoError(t, err)
	assert.Empty(t, res.SleepSessions)
}
