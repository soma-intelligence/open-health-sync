// SPDX-License-Identifier: AGPL-3.0-only

package ultrahuman

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// byType groups samples by quantity type.
func byType(res model.MapResult) map[string][]model.QuantitySample {
	out := map[string][]model.QuantitySample{}
	for _, s := range res.QuantitySamples {
		out[s.QuantityType] = append(out[s.QuantityType], s)
	}
	return out
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestMap_Day(t *testing.T) {
	res, err := mapFields(t, fixtureLine(t, "2026-03-09", "metrics_2026-03-09.json"))
	require.NoError(t, err)
	got := byType(res)

	// Heart rate: three readings, the zero one skipped, local wall clock
	// 02:00 at +01:00 is 01:00 UTC.
	hr := got[model.QTHeartRate]
	require.Len(t, hr, 3)
	require.Equal(t, utc("2026-03-09T01:00:00Z"), hr[0].StartDate)
	require.Equal(t, hr[0].StartDate, hr[0].EndDate)
	require.Equal(t, 58.0, hr[0].Value)
	require.Equal(t, "bpm", hr[0].Unit)
	require.Equal(t, "Etc/GMT-1", hr[0].Metadata["timezone"])
	require.Equal(t, 60, hr[0].Metadata["utc_offset_minutes"])
	require.Equal(t, "ultrahuman", hr[0].SourceSlug)
	require.Equal(t, "Ultrahuman", hr[0].SourceName)
	require.True(t, hr[0].Replaceable)

	hrv := got[model.QTHRVRMSSD]
	require.Len(t, hrv, 2)
	require.Equal(t, "ms", hrv[0].Unit)
	require.Empty(t, got[model.QTHRVSDNN], "Ultrahuman HRV is RMSSD, never SDNN")

	temp := got[model.QTSkinTemperature]
	require.Len(t, temp, 2)
	require.Equal(t, 35.25, temp[1].Value)
	require.Equal(t, "celsius", temp[1].Unit)

	// Steps: the intervals (zero skipped) under the intraday key, the day's
	// total under step_count, covering the local day.
	intra := got[model.QTStepCountIntraday]
	require.Len(t, intra, 2)
	require.Equal(t, 120.0, intra[0].Value)
	steps := got[model.QTStepCount]
	require.Len(t, steps, 1)
	require.Equal(t, 8000.0, steps[0].Value)
	require.Equal(t, utc("2026-03-08T23:00:00Z"), steps[0].StartDate)
	require.Equal(t, utc("2026-03-09T23:00:00Z"), steps[0].EndDate)
	require.True(t, steps[0].Replaceable)

	// Resting HR: sleep_rhr (53) wins over the night_rhr average (55).
	rhr := got[model.QTRestingHeartRate]
	require.Len(t, rhr, 1)
	require.Equal(t, 53.0, rhr[0].Value)

	dev := got[model.QTBodyTemperatureDeviation]
	require.Len(t, dev, 1)
	require.Equal(t, -0.25, dev[0].Value)
	require.Equal(t, "celsius", dev[0].Unit)

	vo2 := got["vo2_max"]
	require.Len(t, vo2, 1)
	require.Equal(t, 44.5, vo2[0].Value)
	require.Equal(t, "mL/kg/min", vo2[0].Unit)

	gl := got["blood_glucose"]
	require.Len(t, gl, 2)
	require.Equal(t, 95.0, gl[0].Value)
	require.Equal(t, "mg/dL", gl[0].Unit)
	require.Equal(t, utc("2026-03-09T09:00:00Z"), gl[0].StartDate)

	// Scores have no canonical name: raw only.
	for _, qt := range []string{"recovery_index", "movement_index", "metabolic_score", model.QTReadinessScore} {
		require.Empty(t, got[qt], qt)
	}

	// Sleep.
	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	require.Equal(t, "2026-03-09", s.ExternalID)
	require.Equal(t, utc("2026-03-08T22:00:00Z"), s.BedtimeStart)
	require.Equal(t, utc("2026-03-09T06:00:00Z"), s.BedtimeEnd)
	require.Equal(t, 25200, *s.TotalSleepSeconds)
	require.Equal(t, 5400, *s.DeepSleepSeconds)
	require.Equal(t, 5400, *s.REMSleepSeconds)
	require.Equal(t, 14400, *s.LightSleepSeconds)
	require.Equal(t, 3600, *s.AwakeSeconds)
	require.Equal(t, 50.0, *s.HRVAvgMs)
	require.Equal(t, 53.0, *s.RestingHR)
	require.Equal(t, 97.0, *s.SpO2Avg)
	require.Nil(t, s.SleepScore)
	require.Equal(t, 88.0, s.Metadata["sleep_efficiency"])
	require.True(t, s.Replaceable)
	require.NotEmpty(t, s.ContentHash)

	require.Len(t, res.SleepPhases, 5)
	wantPhases := []string{model.SleepStageAwake, model.SleepStageLight, model.SleepStageDeep, model.SleepStageREM, model.SleepStageLight}
	for i, p := range res.SleepPhases {
		require.Equal(t, wantPhases[i], p.Phase)
		require.Equal(t, s.ContentHash, p.ParentContentHash)
		require.Equal(t, int(p.EndDate.Sub(p.StartDate)/time.Second), p.DurationS)
	}
	require.Equal(t, s.BedtimeStart, res.SleepPhases[0].StartDate)
	require.Equal(t, s.BedtimeEnd, res.SleepPhases[4].EndDate)

	// Every hash is set and unique.
	seen := map[string]bool{}
	for _, q := range res.QuantitySamples {
		require.NotEmpty(t, q.ContentHash)
		require.False(t, seen[q.ContentHash], "duplicate hash for %s at %s", q.QuantityType, q.StartDate)
		seen[q.ContentHash] = true
	}
}

func TestMap_GlucoseMmolConverted(t *testing.T) {
	res, err := mapFields(t, fixtureLine(t, "2026-03-08", "metrics_2026-03-08.json"))
	require.NoError(t, err)
	gl := byType(res)["blood_glucose"]
	require.Len(t, gl, 2)
	require.Equal(t, "mg/dL", gl[0].Unit)
	require.InDelta(t, 5.5*18.0182, gl[0].Value, 1e-9)
	require.InDelta(t, 6.0*18.0182, gl[1].Value, 1e-9)
}

func TestGlucoseUnits(t *testing.T) {
	v, u, ok := glucose(100, "mg/dL")
	require.True(t, ok)
	require.Equal(t, 100.0, v)
	require.Equal(t, "mg/dL", u)
	v, _, ok = glucose(100, "")
	require.True(t, ok, "no unit reads as mg/dL")
	require.Equal(t, 100.0, v)
	v, u, ok = glucose(5.5, "mmol/l")
	require.True(t, ok)
	require.Equal(t, "mg/dL", u)
	require.InDelta(t, 99.1, v, 0.01)
	_, _, ok = glucose(5, "g/L")
	require.False(t, ok, "an unknown unit is dropped, not guessed")
	_, _, ok = glucose(0, "mg/dL")
	require.False(t, ok)
}

func TestTemperatureUnits(t *testing.T) {
	v, _, ok := temperature(93.2, "°F")
	require.True(t, ok)
	require.InDelta(t, 34.0, v, 1e-9)
	_, _, ok = temperature(0.4, "°C")
	require.False(t, ok, "a deviation is not a skin temperature")
	v, _, ok = temperatureDelta(0.9, "°F")
	require.True(t, ok)
	require.InDelta(t, 0.5, v, 1e-9)
}

func TestMap_GlucoseWithoutUnitIsFlagged(t *testing.T) {
	fields := map[string]any{"date": "2026-03-09", "body": map[string]any{"data": map[string]any{"metric_data": []any{
		map[string]any{"type": "glucose", "object": map[string]any{"day_start_timestamp": 1773010800, "values": []any{
			map[string]any{"value": 110, "timestamp": 1773046800},
		}}},
	}}}}
	res, err := mapFields(t, fields)
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 1)
	require.Equal(t, true, res.QuantitySamples[0].Metadata["unit_assumed"])
}

func TestMap_NightRHRFallbackAndOffsets(t *testing.T) {
	// A UTC-05:00 user: local midnight of 2026-03-09 is 05:00 UTC. No
	// sleep_rhr: the night_rhr average is the resting heart rate.
	ds := float64(utc("2026-03-09T05:00:00Z").Unix())
	fields := map[string]any{"date": "2026-03-09", "body": map[string]any{"data": map[string]any{"metric_data": []any{
		map[string]any{"type": "night_rhr", "object": map[string]any{"day_start_timestamp": ds, "avg": 57, "values": []any{}}},
	}}}}
	res, err := mapFields(t, fields)
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 1)
	s := res.QuantitySamples[0]
	require.Equal(t, model.QTRestingHeartRate, s.QuantityType)
	require.Equal(t, 57.0, s.Value)
	require.Equal(t, utc("2026-03-09T05:00:00Z"), s.StartDate)
	require.Equal(t, "Etc/GMT+5", s.Metadata["timezone"])
	require.Equal(t, -300, s.Metadata["utc_offset_minutes"])

	// A +05:30 user keeps the offset but gets no zone.
	ds = float64(utc("2026-03-08T18:30:00Z").Unix())
	fields["body"].(map[string]any)["data"].(map[string]any)["metric_data"].([]any)[0].(map[string]any)["object"].(map[string]any)["day_start_timestamp"] = ds
	res, err = mapFields(t, fields)
	require.NoError(t, err)
	require.Equal(t, 330, res.QuantitySamples[0].Metadata["utc_offset_minutes"])
	require.NotContains(t, res.QuantitySamples[0].Metadata, "timezone")

	// No day start at all: the date is read as a UTC day and flagged.
	delete(fields["body"].(map[string]any)["data"].(map[string]any)["metric_data"].([]any)[0].(map[string]any)["object"].(map[string]any), "day_start_timestamp")
	res, err = mapFields(t, fields)
	require.NoError(t, err)
	require.Equal(t, true, res.QuantitySamples[0].Metadata["local_date_as_utc"])
	require.Equal(t, utc("2026-03-09T00:00:00Z"), res.QuantitySamples[0].StartDate)
}

func TestMap_PersonalAPIEnvelope(t *testing.T) {
	// {"data":{"metrics":{"<date>":[...]}}}, as third parties read the
	// personal token API, maps like the Partnership API shape.
	fields := map[string]any{"date": "2026-03-09", "body": map[string]any{"data": map[string]any{"metrics": map[string]any{
		"2026-03-09": []any{map[string]any{"type": "vo2_max", "object": map[string]any{"value": 41, "day_start_timestamp": 1773010800}}},
	}}}}
	res, err := mapFields(t, fields)
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 1)
	require.Equal(t, "vo2_max", res.QuantitySamples[0].QuantityType)
}

func TestMap_LenientValues(t *testing.T) {
	// A null, a string number and a non-number: only the usable readings
	// land; duplicated timestamps keep the last reading; milliseconds are
	// read as such.
	fields := map[string]any{"date": "2026-03-09", "body": map[string]any{"data": map[string]any{"metric_data": []any{
		map[string]any{"type": "hr", "object": map[string]any{"values": []any{
			map[string]any{"value": nil, "timestamp": 1773046800},
			map[string]any{"value": "64", "timestamp": 1773046860},
			map[string]any{"value": "n/a", "timestamp": 1773046920},
			map[string]any{"value": 70, "timestamp": 1773046980},
			map[string]any{"value": 71, "timestamp": 1773046980},
			map[string]any{"value": 80, "timestamp": 1773047040000.0},
			map[string]any{"value": 90, "timestamp": 12},
		}}},
		map[string]any{"type": "metabolic_score", "object": map[string]any{"value": nil}},
	}}}}
	res, err := mapFields(t, fields)
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 3)
	require.Equal(t, 64.0, res.QuantitySamples[0].Value)
	require.Equal(t, 71.0, res.QuantitySamples[1].Value)
	require.Equal(t, time.Unix(1773047040, 0).UTC(), res.QuantitySamples[2].StartDate)
}

func TestMap_SleepEdges(t *testing.T) {
	sleep := func(obj map[string]any) model.MapResult {
		fields := map[string]any{"date": "2026-03-09", "body": map[string]any{"data": map[string]any{"metric_data": []any{
			map[string]any{"type": "Sleep", "object": obj},
		}}}}
		res, err := mapFields(t, fields)
		require.NoError(t, err)
		return res
	}
	// Without bedtimes there is no session.
	require.Empty(t, sleep(map[string]any{"quick_metrics": []any{}}).SleepSessions)
	// End before start: no session.
	require.Empty(t, sleep(map[string]any{"bedtime_start": 1773010800, "bedtime_end": 1773000000}).SleepSessions)
	// Stages without total_sleep: the total is deep + light + REM; an
	// unknown graph stage is dropped; a sleep score is read.
	res := sleep(map[string]any{
		"bedtime_start": 1773010800, "bedtime_end": 1773036000,
		"sleep_stages": []any{
			map[string]any{"type": "deep_sleep", "stage_time": 3600},
			map[string]any{"type": "light_sleep", "stage_time": 7200},
			map[string]any{"type": "rem_sleep", "stage_time": 1800},
		},
		"sleep_graph": map[string]any{"data": []any{
			map[string]any{"start": 1773010800, "end": 1773014400, "type": "deep_sleep"},
			map[string]any{"start": 1773014400, "end": 1773018000, "type": "nap"},
		}},
		"sleep_score": map[string]any{"score": 81},
	})
	require.Len(t, res.SleepSessions, 1)
	require.Equal(t, 12600, *res.SleepSessions[0].TotalSleepSeconds)
	require.Equal(t, 81.0, *res.SleepSessions[0].SleepScore)
	require.Len(t, res.SleepPhases, 1)
	require.Equal(t, model.SleepStageDeep, res.SleepPhases[0].Phase)
}

func TestMap_UnknownTypeIsEmpty(t *testing.T) {
	res, err := New(Options{}).Map(connector.RawRecord{UserID: testUserID, DataType: "nope", Fields: map[string]any{"x": 1}})
	require.NoError(t, err)
	require.Empty(t, res.QuantitySamples)

	// A day holding only raw-only types maps to nothing.
	res, err = mapFields(t, map[string]any{"date": "2026-03-09", "body": map[string]any{"data": map[string]any{"metric_data": []any{
		map[string]any{"type": "recovery_index", "object": map[string]any{"value": 80}},
		map[string]any{"type": "brand_new_metric", "object": map[string]any{"value": 1}},
	}}}})
	require.NoError(t, err)
	require.Empty(t, res.QuantitySamples)
	require.Empty(t, res.SleepSessions)
}

func TestMap_MalformedIsError(t *testing.T) {
	_, err := mapFields(t, map[string]any{"date": "09/03/2026", "body": map[string]any{}})
	require.Error(t, err)
	_, err = mapFields(t, map[string]any{"date": "2026-03-09", "body": []any{1}})
	require.Error(t, err)
	_, err = mapFields(t, map[string]any{"date": 5})
	require.Error(t, err)
}

func TestMap_Deterministic(t *testing.T) {
	fields := fixtureLine(t, "2026-03-09", "metrics_2026-03-09.json")
	a, err := mapFields(t, fields)
	require.NoError(t, err)
	b, err := mapFields(t, fields)
	require.NoError(t, err)
	require.Equal(t, a, b)
}

func TestUnixTime(t *testing.T) {
	_, ok := unixTime(0)
	require.False(t, ok)
	_, ok = unixTime(math.MaxFloat64)
	require.False(t, ok)
	got, ok := unixTime(1773046800.5)
	require.True(t, ok)
	require.Equal(t, 500*time.Millisecond, time.Duration(got.Nanosecond()))
}

func TestMap_ZeroDateRefused(t *testing.T) {
	body := `{"data":{"metric_data":[{"type":"vo2_max","object":{"value":45}}]}}`
	_, err := mapFields(t, map[string]any{"date": "0001-01-01", "body": json.RawMessage(body)})
	require.Error(t, err)
}

func TestMap_NonFiniteValuesAreMissing(t *testing.T) {
	body := `{"data":{"metric_data":[` +
		`{"type":"hr","object":{"values":[{"value":"NaN","timestamp":1772931600},{"value":"Inf","timestamp":1772931900},{"value":61,"timestamp":"NaN"}]}},` +
		`{"type":"temp","object":{"values":[{"value":"nan","timestamp":1772931600}]}},` +
		`{"type":"temperature_deviation","object":{"value":"NaN"}},` +
		`{"type":"steps","object":{"total":"Inf"}},` +
		`{"type":"vo2_max","object":{"value":"+Inf"}},` +
		`{"type":"Sleep","object":{"bedtime_start":1772920800,"bedtime_end":1772949600,` +
		`"sleep_stages":[{"type":"deep_sleep","stage_time":"Inf"}],"sleep_score":{"score":"NaN"},"spo2":{"value":"Inf"}}}]}}`
	res, err := mapFields(t, map[string]any{"date": "2026-03-08", "body": json.RawMessage(body)})
	require.NoError(t, err)
	require.Empty(t, res.QuantitySamples, "NaN and Inf are not readings")
	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	require.Nil(t, s.DeepSleepSeconds)
	require.Nil(t, s.SleepScore)
	require.Nil(t, s.SpO2Avg)
}
