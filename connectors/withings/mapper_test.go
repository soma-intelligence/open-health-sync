// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const testUserID = "00000000-0000-0000-0000-0000000000aa"

// mapLine runs Map on one raw line of dataType, the way the ETL hands it over
// (a json.Unmarshal into map[string]any).
func mapLine(t *testing.T, dataType, line string) (model.MapResult, error) {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &fields))
	return New(Options{}).Map(connector.RawRecord{
		TenantID: "00000000-0000-0000-0000-0000000000bb", UserID: testUserID,
		Provider: Slug, DataType: dataType, SourceKey: "raw/test", Fields: fields,
	})
}

func mustMap(t *testing.T, dataType, line string) model.MapResult {
	t.Helper()
	res, err := mapLine(t, dataType, line)
	require.NoError(t, err)
	return res
}

func byType(samples []model.QuantitySample) map[string]model.QuantitySample {
	out := map[string]model.QuantitySample{}
	for _, s := range samples {
		out[s.QuantityType] = s
	}
	return out
}

// --- Value scaling ---

func TestMeasureFloat64(t *testing.T) {
	cases := []struct {
		name        string
		value, unit int
		want        float64
	}{
		{"body mass", 7234, -2, 72.34},
		{"blood pressure no scaling", 120, 0, 120},
		{"negative value", -234, -1, -23.4},
		{"zero value", 0, -2, 0},
		{"positive exponent", 5, 2, 500},
		{"large negative exponent", 12345, -4, 1.2345},
		{"kilograms to the gram", 75500, -3, 75.5},
		{"body fat percent", 1834, -2, 18.34},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.InDelta(t, c.want, Measure{Value: c.value, Unit: c.unit}.Float64(), 1e-9)
		})
	}
}

// --- Timestamp parsing ---

func TestParseWithingsTimestamp(t *testing.T) {
	got := ParseWithingsTimestamp(1709337600)
	assert.True(t, time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC).Equal(got))
	assert.Equal(t, time.UTC, got.Location())
	assert.True(t, time.Unix(0, 0).UTC().Equal(ParseWithingsTimestamp(0)))
}

// --- Meastype table ---

func TestMeasTypeMapping_Completeness(t *testing.T) {
	expected := []int{1, 4, 5, 6, 8, 9, 10, 11, 12, 54, 71, 73, 76, 77, 88, 91, 123, 155, 170}
	assert.Len(t, withingsMeasTypes, len(expected))
	for _, code := range expected {
		info, ok := withingsMeasTypes[code]
		assert.True(t, ok, "meastype %d should be mapped", code)
		assert.NotEmpty(t, info.QuantityType)
		assert.NotEmpty(t, info.Unit)
	}
}

func TestMeasTypeMapping_SpecificMappings(t *testing.T) {
	cases := []struct {
		code int
		qt   string
		unit string
	}{
		{1, "body_mass", "kg"},
		{9, "blood_pressure_diastolic", "mmHg"},
		{10, "blood_pressure_systolic", "mmHg"},
		{11, "heart_rate", "bpm"},
		{54, "oxygen_saturation", "percent"},
		{71, "body_temperature", "celsius"},
		{73, "skin_temperature", "celsius"},
		{123, "vo2_max", "mL/kg/min"},
		{155, "vascular_age", "years"},
	}
	for _, c := range cases {
		assert.Equal(t, c.qt, withingsMeasTypes[c.code].QuantityType, "meastype %d", c.code)
		assert.Equal(t, c.unit, withingsMeasTypes[c.code].Unit, "meastype %d", c.code)
	}
	_, ok := withingsMeasTypes[130]
	assert.False(t, ok, "meastype 130 is unidentified and must stay unmapped")
	_, ok = withingsMeasTypes[121]
	assert.False(t, ok, "meastype 121 is not in the published table")
}

// Withings' published codes: 123 is VO2 max, 135 an ECG's QRS interval, 169
// intracellular water, 170 visceral fat.
func TestMeasTypes_FollowWithingsPublishedCodes(t *testing.T) {
	assert.Equal(t, "vo2_max", withingsMeasTypes[123].QuantityType)
	assert.Equal(t, "visceral_fat_rating", withingsMeasTypes[170].QuantityType)
	assert.NotEqual(t, "vo2_max", withingsMeasTypes[135].QuantityType)
	assert.NotEqual(t, "visceral_fat_rating", withingsMeasTypes[169].QuantityType)
}

// Every storage unit the meastype table writes must be the declared storage
// unit, so the unit choke point never has to convert (height aside: metres to
// centimetres is a declared conversion).
func TestMeasTypeUnitsReachStorage(t *testing.T) {
	for code, info := range withingsMeasTypes {
		_, unit, err := model.ToStorageUnit(info.QuantityType, 1, info.Unit)
		assert.NoError(t, err, "meastype %d", code)
		want, _ := model.StorageUnit(info.QuantityType)
		assert.Equal(t, want, unit, "meastype %d", code)
	}
}

// --- Registration and dispatch ---

func TestRegistered(t *testing.T) {
	c, ok := connector.Get(Slug)
	require.True(t, ok, "withings registers itself in init()")
	assert.Equal(t, "withings", c.Slug())
	_, isOAuth := connector.AsOAuth(c)
	_, isPush := connector.AsPush(c)
	assert.True(t, isOAuth)
	assert.True(t, isPush)
}

func TestMap_UnknownDataTypeIsEmpty(t *testing.T) {
	res := mustMap(t, "unknown_xyz", `{"a":1}`)
	assert.True(t, res.IsEmpty())
	res = mustMap(t, "", `{}`)
	assert.True(t, res.IsEmpty())
}

// --- Measure ---

const bodyMassGroup = `{"grpid":1001,"attrib":0,"date":1709337600,"category":1,"timezone":"Europe/Paris",
  "measures":[{"value":7234,"type":1,"unit":-2}]}`

func TestMapMeasure_BodyMass(t *testing.T) {
	res := mustMap(t, DataTypeMeasure, bodyMassGroup)
	require.Len(t, res.QuantitySamples, 1)
	s := res.QuantitySamples[0]
	assert.Equal(t, testUserID, s.UserID)
	assert.Equal(t, "withings", s.Provider)
	assert.Equal(t, "body_mass", s.QuantityType)
	assert.InDelta(t, 72.34, s.Value, 1e-9)
	assert.Equal(t, "kg", s.Unit)
	assert.Equal(t, time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC), s.StartDate)
	assert.Equal(t, s.StartDate, s.EndDate, "point-in-time measurement")
	assert.Equal(t, "1001_body_mass_1", s.ExternalID)
	assert.NotEmpty(t, s.ContentHash)
	assert.Equal(t, "Europe/Paris", s.Metadata["timezone"], "R17: the zone Withings gave")
}

func TestMapMeasure_MultiMeasureGroup(t *testing.T) {
	res := mustMap(t, DataTypeMeasure, `{"grpid":1,"date":1709337600,"category":1,"measures":[
	  {"type":1,"value":7234,"unit":-2},{"type":6,"value":1850,"unit":-2},{"type":76,"value":3245,"unit":-2}]}`)
	require.Len(t, res.QuantitySamples, 3)
	m := byType(res.QuantitySamples)
	assert.InDelta(t, 72.34, m["body_mass"].Value, 1e-9)
	assert.InDelta(t, 18.50, m["body_fat_percentage"].Value, 1e-9)
	assert.Equal(t, "percent", m["body_fat_percentage"].Unit)
	assert.InDelta(t, 32.45, m["muscle_mass"].Value, 1e-9)
	assert.NotEqual(t, m["body_mass"].ContentHash, m["body_fat_percentage"].ContentHash)
	assert.NotEqual(t, m["body_mass"].ContentHash, m["muscle_mass"].ContentHash)
	_, hasTZ := m["body_mass"].Metadata["timezone"]
	assert.False(t, hasTZ, "no zone given, none invented")
}

func TestMapMeasure_BloodPressureAndHeartRate(t *testing.T) {
	res := mustMap(t, DataTypeMeasure, `{"grpid":2,"date":1709337600,"category":1,"measures":[
	  {"type":9,"value":80,"unit":0},{"type":10,"value":120,"unit":0},{"type":11,"value":60,"unit":0}]}`)
	m := byType(res.QuantitySamples)
	require.Len(t, m, 3)
	assert.InDelta(t, 80.0, m["blood_pressure_diastolic"].Value, 1e-9)
	assert.InDelta(t, 120.0, m["blood_pressure_systolic"].Value, 1e-9)
	assert.Equal(t, "mmHg", m["blood_pressure_systolic"].Unit)
	assert.Equal(t, "bpm", m["heart_rate"].Unit)
	assert.NotEqual(t, m["blood_pressure_diastolic"].ExternalID, m["blood_pressure_systolic"].ExternalID,
		"systolic and diastolic of one group need distinct external ids")
}

func TestMapMeasure_SpO2AndTemperatures(t *testing.T) {
	res := mustMap(t, DataTypeMeasure, `{"grpid":3,"date":1709337600,"category":1,"measures":[
	  {"type":54,"value":98,"unit":0},{"type":71,"value":3680,"unit":-2},{"type":73,"value":3520,"unit":-2}]}`)
	m := byType(res.QuantitySamples)
	assert.InDelta(t, 98.0, m["oxygen_saturation"].Value, 1e-9)
	assert.Equal(t, "percent", m["oxygen_saturation"].Unit)
	assert.InDelta(t, 36.80, m["body_temperature"].Value, 1e-9)
	assert.InDelta(t, 35.20, m["skin_temperature"].Value, 1e-9)
	assert.Equal(t, "celsius", m["skin_temperature"].Unit)
}

// 12 and 71 both map to body_temperature: one group carrying both keeps two
// external ids.
func TestMapMeasure_TemperatureCodesKeepDistinctIDs(t *testing.T) {
	res := mustMap(t, DataTypeMeasure, `{"grpid":12,"date":1709337600,"category":1,"measures":[
	  {"type":12,"value":3700,"unit":-2},{"type":71,"value":3680,"unit":-2}]}`)
	require.Len(t, res.QuantitySamples, 2)
	assert.Equal(t, "12_body_temperature_12", res.QuantitySamples[0].ExternalID)
	assert.Equal(t, "12_body_temperature_71", res.QuantitySamples[1].ExternalID)
}

func TestMapMeasure_UnknownMeastypeSkipped(t *testing.T) {
	res := mustMap(t, DataTypeMeasure, `{"grpid":4,"date":1709337600,"category":1,"measures":[{"type":9999,"value":100,"unit":0}]}`)
	assert.Empty(t, res.QuantitySamples)

	res = mustMap(t, DataTypeMeasure, `{"grpid":5,"date":1709337600,"category":1,"measures":[
	  {"type":1,"value":7234,"unit":-2},{"type":9999,"value":100,"unit":0},{"type":11,"value":72,"unit":0}]}`)
	assert.Len(t, res.QuantitySamples, 2)
}

func TestMapMeasure_MissingDateErrors(t *testing.T) {
	_, err := mapLine(t, DataTypeMeasure, `{"grpid":6,"measures":[{"type":1,"value":7234,"unit":-2}]}`)
	assert.Error(t, err)
}

func TestMapMeasure_MissingOrEmptyMeasures(t *testing.T) {
	a := mustMap(t, DataTypeMeasure, `{"grpid":7,"date":1709337600}`)
	assert.True(t, a.IsEmpty())
	b := mustMap(t, DataTypeMeasure, `{"grpid":8,"date":1709337600,"measures":[]}`)
	assert.True(t, b.IsEmpty())
}

// A household scale is shared, and Withings says on each group whether it could
// attribute the reading. The reading is stored, the attribution rides along.
func TestMapMeasure_CarriesAttribution(t *testing.T) {
	res := mustMap(t, DataTypeMeasure, `{"grpid":9,"date":1788000000,"attrib":1,"category":1,"measures":[{"value":74250,"type":1,"unit":-3}]}`)
	require.Len(t, res.QuantitySamples, 1)
	s := res.QuantitySamples[0]
	assert.InDelta(t, 74.25, s.Value, 1e-9)
	assert.EqualValues(t, 1, s.Metadata["withings_attrib"])
	assert.EqualValues(t, 1, s.Metadata["withings_category"])
	assert.EqualValues(t, 1, s.Metadata["meastype"])
}

// Category 2 is a user objective (a target weight), not a measurement.
func TestMapMeasure_ObjectiveIsNotStored(t *testing.T) {
	res := mustMap(t, DataTypeMeasure, `{"grpid":10,"date":1788000000,"category":2,"measures":[{"value":65000,"type":1,"unit":-3}]}`)
	assert.True(t, res.IsEmpty())
}

func TestMapMeasure_HeightReachesCentimetres(t *testing.T) {
	res := mustMap(t, DataTypeMeasure, `{"grpid":11,"date":1788000000,"category":1,"measures":[{"value":175,"type":4,"unit":-2}]}`)
	require.Len(t, res.QuantitySamples, 1)
	require.Empty(t, model.CanonicalizeUnits(&res))
	assert.InDelta(t, 175.0, res.QuantitySamples[0].Value, 1e-9)
	assert.Equal(t, "cm", res.QuantitySamples[0].Unit)
}

func TestContentHash_Measure(t *testing.T) {
	a := mustMap(t, DataTypeMeasure, bodyMassGroup)
	b := mustMap(t, DataTypeMeasure, bodyMassGroup)
	assert.Equal(t, a.QuantitySamples[0].ContentHash, b.QuantitySamples[0].ContentHash)
	c := mustMap(t, DataTypeMeasure, `{"grpid":1001,"date":1709424000,"category":1,"measures":[{"value":7234,"type":1,"unit":-2}]}`)
	assert.NotEqual(t, a.QuantitySamples[0].ContentHash, c.QuantitySamples[0].ContentHash)
}

// --- Sleep ---

const sleepLine = `{"id":2001,"timezone":"Europe/Paris","startdate":1709334000,"enddate":1709362800,"state":0,
  "data":{"deepsleepduration":5400,"remsleepduration":7200,"lightsleepduration":10800,"wakeupduration":1800,
  "hr_average":55,"hr_min":48,"rr_average":16,"sleep_score":85}}`

func TestMapSleep(t *testing.T) {
	res := mustMap(t, DataTypeSleep, sleepLine)
	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	assert.Equal(t, testUserID, s.UserID)
	assert.Equal(t, "withings", s.Provider)
	assert.Equal(t, "2001", s.ExternalID)
	assert.Equal(t, time.Date(2024, 3, 1, 23, 0, 0, 0, time.UTC), s.BedtimeStart)
	assert.Equal(t, time.Date(2024, 3, 2, 7, 0, 0, 0, time.UTC), s.BedtimeEnd)
	require.NotNil(t, s.DeepSleepSeconds)
	assert.Equal(t, 5400, *s.DeepSleepSeconds)
	assert.Equal(t, 7200, *s.REMSleepSeconds)
	assert.Equal(t, 10800, *s.LightSleepSeconds)
	assert.Equal(t, 1800, *s.AwakeSeconds)
	require.NotNil(t, s.TotalSleepSeconds)
	assert.Equal(t, 5400+7200+10800, *s.TotalSleepSeconds, "total = deep + rem + light")
	require.NotNil(t, s.RestingHR)
	assert.Equal(t, 55.0, *s.RestingHR)
	assert.Equal(t, 16.0, *s.RespiratoryRate)
	assert.Equal(t, 85.0, *s.SleepScore)
	assert.NotEmpty(t, s.ContentHash)
	assert.Equal(t, "Europe/Paris", s.Metadata["timezone"])
	assert.EqualValues(t, 48, s.Metadata["hr_min"], "unmapped data fields stay queryable in metadata")
	assert.Nil(t, res.SleepPhases, "no phase segments, no synthetic phases")
}

func TestMapSleep_ContentHashDeterministic(t *testing.T) {
	a := mustMap(t, DataTypeSleep, `{"id":1,"startdate":1709334000,"enddate":1709362800,"data":{}}`)
	b := mustMap(t, DataTypeSleep, `{"id":1,"startdate":1709334000,"enddate":1709362800,"data":{}}`)
	assert.Equal(t, a.SleepSessions[0].ContentHash, b.SleepSessions[0].ContentHash)
}

func TestMapSleep_WithPhases(t *testing.T) {
	before := DroppedSleepStates()
	res := mustMap(t, DataTypeSleep, `{"id":2002,"startdate":1709334000,"enddate":1709362800,"data":{"deepsleepduration":3600},
	  "sleep_phases":[{"startdate":1709334000,"enddate":1709337600,"state":2},
	                  {"startdate":1709337600,"enddate":1709341200,"state":3},
	                  {"startdate":1709341200,"enddate":1709342000,"state":0},
	                  {"startdate":1709342000,"enddate":1709343000,"state":1},
	                  {"startdate":1709343000,"enddate":1709344000,"state":9}]}`)
	require.Len(t, res.SleepSessions, 1)
	require.Len(t, res.SleepPhases, 4, "the segment of unknown state 9 is dropped")
	assert.Equal(t, "deep", res.SleepPhases[0].Phase)
	assert.Equal(t, "rem", res.SleepPhases[1].Phase)
	assert.Equal(t, "awake", res.SleepPhases[2].Phase)
	assert.Equal(t, "light", res.SleepPhases[3].Phase)
	assert.Equal(t, int64(1), DroppedSleepStates()-before, "the drop is counted")
	assert.Equal(t, 3600, res.SleepPhases[0].DurationS)
	assert.Equal(t, time.Date(2024, 3, 1, 23, 0, 0, 0, time.UTC), res.SleepPhases[0].StartDate)
	for _, p := range res.SleepPhases {
		assert.Equal(t, res.SleepSessions[0].ContentHash, p.ParentContentHash)
		assert.Equal(t, testUserID, p.UserID)
	}
	_, hasPhases := res.SleepSessions[0].Metadata["sleep_phases"]
	assert.False(t, hasPhases, "phases live in their own table, not in the session metadata")
}

func TestMapSleep_ExternalIDStringOrNumber(t *testing.T) {
	a := mustMap(t, DataTypeSleep, `{"id":"sleep-string-id","startdate":1704067200,"enddate":1704099600,"data":{}}`)
	assert.Equal(t, "sleep-string-id", a.SleepSessions[0].ExternalID)
	b := mustMap(t, DataTypeSleep, `{"id":98765432,"startdate":1704067200,"enddate":1704099600,"data":{}}`)
	assert.Equal(t, "98765432", b.SleepSessions[0].ExternalID)
}

func TestMapSleep_MissingDatesError(t *testing.T) {
	_, err := mapLine(t, DataTypeSleep, `{"id":1,"enddate":1709362800,"data":{}}`)
	assert.Error(t, err)
	_, err = mapLine(t, DataTypeSleep, `{"id":1,"startdate":1709334000,"data":{}}`)
	assert.Error(t, err)
}

// --- Activity ---

const activityLine = `{"date":"2024-03-02","timezone":"Europe/Paris","steps":8500,"distance":6200,"calories":350,
  "totalcalories":2350,"soft":3600,"moderate":1200,"intense":600}`

func TestMapActivity_StepsDistanceCalories(t *testing.T) {
	res := mustMap(t, DataTypeActivity, activityLine)
	m := byType(res.QuantitySamples)
	require.Len(t, m, 5)
	assert.InDelta(t, 8500.0, m["step_count"].Value, 1e-9)
	assert.Equal(t, "count", m["step_count"].Unit)
	assert.Equal(t, "withings", m["step_count"].Provider)
	assert.InDelta(t, 6200.0, m["distance_walking_running"].Value, 1e-9)
	assert.Equal(t, "m", m["distance_walking_running"].Unit)
	assert.InDelta(t, 350.0, m["active_energy_burned"].Value, 1e-9)
	assert.Equal(t, "kcal", m["active_energy_burned"].Unit)
	for _, s := range res.QuantitySamples {
		assert.Equal(t, "Europe/Paris", s.Metadata["timezone"])
		assert.True(t, s.Replaceable, "a day's total is re-sent while the day is open")
		assert.NotEmpty(t, s.ContentHash)
	}
}

// Withings' moderate and intense durations (seconds) are the day's moderate
// and vigorous minutes, kept apart per device like the other day totals.
func TestMapActivity_IntensityMinutes(t *testing.T) {
	res := mustMap(t, DataTypeActivity, activityLine)
	require.Empty(t, model.CanonicalizeUnits(&res), "seconds convert to the storage unit")
	m := byType(res.QuantitySamples)
	moderate, vigorous := m[model.QTModerateActivityTime], m[model.QTVigorousActivityTime]
	assert.InDelta(t, 20.0, moderate.Value, 1e-9, "1200 s of moderate activity")
	assert.InDelta(t, 10.0, vigorous.Value, 1e-9, "600 s of intense activity")
	for _, s := range []model.QuantitySample{moderate, vigorous} {
		assert.Equal(t, "min", s.Unit)
		assert.True(t, m["step_count"].StartDate.Equal(s.StartDate))
		assert.True(t, s.Replaceable)
		assert.Equal(t, 1200.0, s.Metadata["moderate_duration"], "the metadata copy stays")
	}
	assert.Equal(t, "2024-03-02_moderate_activity_time", moderate.ExternalID)

	// Zero is a value; a missing figure gives no sample.
	res = mustMap(t, DataTypeActivity, `{"date":"2024-03-02","steps":1000,"moderate":0}`)
	require.Empty(t, model.CanonicalizeUnits(&res))
	m = byType(res.QuantitySamples)
	assert.InDelta(t, 0.0, m[model.QTModerateActivityTime].Value, 1e-9)
	assert.NotContains(t, m, model.QTVigorousActivityTime)
}

// The day is the user's local day: midnight in the zone Withings gave, or UTC
// midnight without one.
func TestMapActivity_DateIsLocalMidnight(t *testing.T) {
	res := mustMap(t, DataTypeActivity, `{"date":"2024-03-02","steps":1000}`)
	require.Len(t, res.QuantitySamples, 1)
	assert.Equal(t, time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC), res.QuantitySamples[0].StartDate)

	res = mustMap(t, DataTypeActivity, `{"date":"2024-03-02","timezone":"Europe/Paris","steps":1000}`)
	paris, _ := time.LoadLocation("Europe/Paris")
	s := res.QuantitySamples[0]
	assert.True(t, time.Date(2024, 3, 2, 0, 0, 0, 0, paris).Equal(s.StartDate))
	assert.True(t, time.Date(2024, 3, 3, 0, 0, 0, 0, paris).Add(-time.Second).Equal(s.EndDate))
}

// A day's total grows during the day. The hash must not include the value, so
// the newer total replaces the stored one instead of adding a second row.
func TestMapActivity_HashIgnoresTheValue(t *testing.T) {
	a := mustMap(t, DataTypeActivity, `{"date":"2024-03-02","steps":8500}`)
	b := mustMap(t, DataTypeActivity, `{"date":"2024-03-02","steps":9100}`)
	assert.Equal(t, a.QuantitySamples[0].ContentHash, b.QuantitySamples[0].ContentHash)
	c := mustMap(t, DataTypeActivity, `{"date":"2024-03-03","steps":8500}`)
	assert.NotEqual(t, a.QuantitySamples[0].ContentHash, c.QuantitySamples[0].ContentHash)
}

// Two trackers each report their own day total: the device keeps them apart.
func TestMapActivity_DeviceInIdentity(t *testing.T) {
	a := mustMap(t, DataTypeActivity, `{"date":"2024-03-02","deviceid":"fake-watch-0001","steps":8500}`)
	b := mustMap(t, DataTypeActivity, `{"date":"2024-03-02","deviceid":"fake-watch-0002","steps":8500}`)
	assert.NotEqual(t, a.QuantitySamples[0].ContentHash, b.QuantitySamples[0].ContentHash)
	assert.Equal(t, "2024-03-02_step_count_fake-watch-0001", a.QuantitySamples[0].ExternalID)
	c := mustMap(t, DataTypeActivity, `{"date":"2024-03-02","steps":8500}`)
	assert.Equal(t, "2024-03-02_step_count", c.QuantitySamples[0].ExternalID)
}

func TestMapActivity_BadDateErrors(t *testing.T) {
	_, err := mapLine(t, DataTypeActivity, `{"date":"not-a-date","steps":1000}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "activity date")
}

// --- Workout ---

func TestMapWorkout(t *testing.T) {
	res := mustMap(t, DataTypeWorkout, `{"id":3001,"category":2,"timezone":"Europe/Paris","startdate":1709373600,"enddate":1709377200,
	  "deviceid":"fake-device-1","data":{"calories":350,"distance":6200,"steps":7000,"hr_average":140,"hr_min":100,"hr_max":175}}`)
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	assert.Equal(t, testUserID, w.UserID)
	assert.Equal(t, "withings", w.Provider)
	assert.Equal(t, "running", w.ActivityType, "the Apple Health vocabulary")
	assert.Equal(t, "3001", w.ExternalID)
	require.NotNil(t, w.DurationS)
	assert.Equal(t, 3600, *w.DurationS)
	assert.InDelta(t, 350.0, *w.ActiveEnergyKcal, 1e-9)
	assert.InDelta(t, 6200.0, *w.DistanceM, 1e-9)
	assert.InDelta(t, 140.0, *w.AvgHeartRate, 1e-9)
	assert.InDelta(t, 175.0, *w.MaxHeartRate, 1e-9)
	assert.NotEmpty(t, w.ContentHash)
	assert.Equal(t, "Europe/Paris", w.Metadata["timezone"])
	assert.Equal(t, true, w.Metadata["distance_is_step_based"], "running distance comes from steps")
	assert.EqualValues(t, 2, w.Metadata["withings_category"])
	_, indoor := w.Metadata["indoor"]
	assert.False(t, indoor)
	assert.EqualValues(t, 7000, w.Metadata["steps"])
}

func TestMapWorkout_IndoorAndNotStepBased(t *testing.T) {
	res := mustMap(t, DataTypeWorkout, `{"id":3004,"category":307,"startdate":1709373600,"enddate":1709377200,"data":{"distance":20000}}`)
	w := res.Workouts[0]
	assert.Equal(t, "cycling", w.ActivityType)
	assert.Equal(t, true, w.Metadata["indoor"])
	_, stepBased := w.Metadata["distance_is_step_based"]
	assert.False(t, stepBased, "cycling distance is not derived from steps")

	res = mustMap(t, DataTypeWorkout, `{"id":3005,"category":306,"startdate":1709373600,"enddate":1709377200}`)
	assert.Equal(t, "running", res.Workouts[0].ActivityType)
	assert.Equal(t, true, res.Workouts[0].Metadata["indoor"])
	assert.Equal(t, true, res.Workouts[0].Metadata["distance_is_step_based"])
}

func TestMapWorkout_NoDataDefaults(t *testing.T) {
	res := mustMap(t, DataTypeWorkout, `{"id":3002,"category":99999,"startdate":1709373600,"enddate":1709375400}`)
	require.Len(t, res.Workouts, 1)
	assert.Equal(t, "other", res.Workouts[0].ActivityType)
	assert.Equal(t, 1800, *res.Workouts[0].DurationS)
	assert.Nil(t, res.Workouts[0].ActiveEnergyKcal)
}

func TestMapWorkout_MissingDatesError(t *testing.T) {
	_, err := mapLine(t, DataTypeWorkout, `{"id":3003,"startdate":1709373600}`)
	assert.Error(t, err)
}

func TestWorkoutCategory(t *testing.T) {
	for cat, want := range map[int]workoutType{
		1:   {activity: "walking"},
		2:   {activity: "running"},
		6:   {activity: "cycling"},
		7:   {activity: "swimming"},
		16:  {activity: "traditional_strength_training"},
		306: {activity: "running", indoor: true},
		307: {activity: "cycling", indoor: true},
		308: {activity: "walking", indoor: true},
	} {
		assert.Equal(t, want, withingsWorkoutCategory(cat), "category %d", cat)
	}
	assert.Equal(t, workoutType{activity: "other"}, withingsWorkoutCategory(99999), "an unknown category still gets a usable label")
}

// Every activity name is one the Apple Health connector also uses (or other).
func TestWorkoutCategoriesUseTheSharedVocabulary(t *testing.T) {
	shared := map[string]bool{"other": true, "mixed_cardio": true}
	for _, v := range []string{"walking", "running", "hiking", "skating_sports", "cycling", "swimming",
		"surfing_sports", "water_sports", "tennis", "table_tennis", "squash", "badminton",
		"traditional_strength_training", "functional_strength_training", "elliptical", "pilates",
		"basketball", "soccer", "american_football", "rugby", "volleyball", "water_polo",
		"equestrian_sports", "golf", "yoga", "dance", "boxing", "fencing", "wrestling", "martial_arts",
		"downhill_skiing", "snowboarding", "rowing", "baseball", "handball", "hockey", "climbing"} {
		shared[v] = true
	}
	for cat, wt := range workoutCategories {
		assert.True(t, shared[wt.activity], "category %d maps to %q", cat, wt.activity)
		assert.NotContains(t, wt.activity, "indoor_", "indoor is metadata, not a type")
	}
}

// --- Intraday ---

func TestMapIntraday(t *testing.T) {
	res := mustMap(t, DataTypeIntraday, `{"timestamp":1709370000,"steps":500,"heart_rate":80,"calories":12.5,"distance":350,"duration":60}`)
	m := byType(res.QuantitySamples)
	for _, canonical := range []string{"step_count", "active_energy_burned", "distance_walking_running"} {
		_, present := m[canonical]
		assert.False(t, present, "%s from intraday would double count the daily total", canonical)
	}
	assert.InDelta(t, 500.0, m[model.QTStepCountIntraday].Value, 1e-9)
	assert.Equal(t, "count", m[model.QTStepCountIntraday].Unit)
	assert.InDelta(t, 12.5, m[model.QTActiveEnergyIntraday].Value, 1e-9)
	assert.Equal(t, "kcal", m[model.QTActiveEnergyIntraday].Unit)
	assert.InDelta(t, 350.0, m[model.QTDistanceWalkRunIntday].Value, 1e-9)
	assert.Equal(t, "m", m[model.QTDistanceWalkRunIntday].Unit)
	assert.InDelta(t, 80.0, m["heart_rate"].Value, 1e-9)
	assert.Equal(t, "bpm", m["heart_rate"].Unit)
	hr := m["heart_rate"]
	assert.Equal(t, time.Unix(1709370000, 0).UTC(), hr.StartDate)
	assert.Equal(t, time.Unix(1709370060, 0).UTC(), hr.EndDate)
	for _, s := range res.QuantitySamples {
		assert.NotEmpty(t, s.ContentHash)
	}
}

// An interval sent again with a corrected value replaces the stored one.
func TestMapIntraday_ReplaceableOnIntervalStart(t *testing.T) {
	a := byType(mustMap(t, DataTypeIntraday, `{"timestamp":1709370000,"steps":500,"duration":60}`).QuantitySamples)
	b := byType(mustMap(t, DataTypeIntraday, `{"timestamp":1709370000,"steps":520,"duration":60}`).QuantitySamples)
	c := byType(mustMap(t, DataTypeIntraday, `{"timestamp":1709370060,"steps":500,"duration":60}`).QuantitySamples)
	sa := a[model.QTStepCountIntraday]
	assert.True(t, sa.Replaceable)
	assert.Equal(t, sa.ContentHash, b[model.QTStepCountIntraday].ContentHash)
	assert.NotEqual(t, sa.ContentHash, c[model.QTStepCountIntraday].ContentHash)
}

// A 0 bpm reading is a missing reading; zero steps are a real zero.
func TestMapIntraday_ZeroHeartRateSkipped(t *testing.T) {
	m := byType(mustMap(t, DataTypeIntraday, `{"timestamp":1709370000,"steps":0,"heart_rate":0,"duration":60}`).QuantitySamples)
	_, hasHR := m["heart_rate"]
	assert.False(t, hasHR)
	steps, ok := m[model.QTStepCountIntraday]
	require.True(t, ok)
	assert.Equal(t, 0.0, steps.Value)
}

func TestMapIntraday_MissingTimestampErrors(t *testing.T) {
	_, err := mapLine(t, DataTypeIntraday, `{"steps":5}`)
	assert.Error(t, err)
}

// --- ECG ---

func TestMapECG_ClassificationOnly(t *testing.T) {
	res := mustMap(t, DataTypeECG, `{"deviceid":"fake-device-1","model":93,"ecg":{"signalid":4001,"afib":1},"heart_rate":62,"timestamp":1709380800,"timezone":"Europe/Paris"}`)
	require.Len(t, res.QuantitySamples, 1)
	s := res.QuantitySamples[0]
	assert.Equal(t, "ecg_afib_classification", s.QuantityType)
	assert.InDelta(t, 1.0, s.Value, 1e-9)
	assert.Equal(t, "classification", s.Unit)
	assert.Equal(t, "withings_ecg_1709380800_4001", s.ExternalID)
	assert.Equal(t, "fake-device-1", s.DeviceID)
	assert.Equal(t, "Europe/Paris", s.Metadata["timezone"])
	assert.EqualValues(t, 4001, s.Metadata["signal_id"])
	assert.NotEmpty(t, s.ContentHash)
	assert.Empty(t, res.ECGRecordings, "no waveform, no recording")
	assert.False(t, res.HighFrequency)
}

// The waveform fans out into one recording plus one voltage sample per value,
// offsets index / sampling frequency, microvolts unchanged.
func TestMapECG_Waveform(t *testing.T) {
	res := mustMap(t, DataTypeECG, `{"deviceid":"fake-device-1","model":93,"ecg":{"signalid":9001,"afib":1},"heart_rate":62,"timestamp":1709380800,
	  "signal":{"signal":[-100,0,150.5,300],"sampling_frequency":2,"wearposition":1}}`)
	require.Len(t, res.QuantitySamples, 1, "the classification is kept next to the waveform")
	require.Len(t, res.ECGRecordings, 1)
	rec := res.ECGRecordings[0]
	assert.Equal(t, testUserID, rec.UserID)
	assert.Equal(t, 1, rec.Classification)
	assert.InDelta(t, 2.0, rec.SamplingFrequency, 1e-9)
	assert.Equal(t, 4, rec.NumberOfMeasurements)
	require.NotNil(t, rec.AverageHeartRate)
	assert.InDelta(t, 62.0, *rec.AverageHeartRate, 1e-9)
	start := time.Date(2024, 3, 2, 12, 0, 0, 0, time.UTC)
	assert.True(t, rec.StartDate.Equal(start))
	assert.True(t, rec.EndDate.Equal(start.Add(2*time.Second)), "4 samples / 2 Hz = 2 s")
	_, hasSignal := rec.Metadata["signal"]
	assert.False(t, hasSignal, "the raw signal is not copied onto the recording row")
	assert.NotEmpty(t, rec.ContentHash)

	require.Len(t, res.ECGSamples, 4)
	assert.True(t, res.HighFrequency)
	for _, s := range res.ECGSamples {
		assert.Equal(t, rec.ContentHash, s.ParentContentHash)
		assert.Equal(t, testUserID, s.UserID)
	}
	assert.InDelta(t, 0.0, res.ECGSamples[0].TimeSinceSampleStart, 1e-9)
	assert.InDelta(t, 0.5, res.ECGSamples[1].TimeSinceSampleStart, 1e-9)
	assert.InDelta(t, 1.5, res.ECGSamples[3].TimeSinceSampleStart, 1e-9)
	assert.InDelta(t, -100.0, res.ECGSamples[0].VoltageUV, 1e-9)
	assert.InDelta(t, 150.5, res.ECGSamples[2].VoltageUV, 1e-9)
}

// Without a sampling frequency the samples still land, spaced at 1 Hz.
func TestMapECG_MissingSamplingFrequency(t *testing.T) {
	res := mustMap(t, DataTypeECG, `{"ecg":{"signalid":9002,"afib":0},"timestamp":1709380800,"signal":{"signal":[10,20]}}`)
	require.Len(t, res.ECGRecordings, 1)
	require.Len(t, res.ECGSamples, 2)
	assert.InDelta(t, 0.0, res.ECGSamples[0].TimeSinceSampleStart, 1e-9)
	assert.InDelta(t, 1.0, res.ECGSamples[1].TimeSinceSampleStart, 1e-9)
}

func TestMapECG_MissingTimestampErrors(t *testing.T) {
	_, err := mapLine(t, DataTypeECG, `{"ecg":{"signalid":1,"afib":0}}`)
	assert.Error(t, err)
}

// --- Provider naming ---

func TestProviderIsLowercase(t *testing.T) {
	for dt, line := range map[string]string{
		DataTypeMeasure:  bodyMassGroup,
		DataTypeActivity: activityLine,
		DataTypeSleep:    sleepLine,
	} {
		res := mustMap(t, dt, line)
		for _, s := range res.QuantitySamples {
			assert.Equal(t, "withings", s.Provider)
		}
		for _, s := range res.SleepSessions {
			assert.Equal(t, "withings", s.Provider)
		}
	}
}
