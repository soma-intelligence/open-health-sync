// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImported(t *testing.T) {
	cases := []struct {
		name, dataType, line string
		want                 bool
	}{
		{"activity external, model unknown", DataTypeActivity, `{"date":"2026-03-01","brand":18,"modelid":-1,"model":null,"is_tracker":false}`, true},
		{"activity from Google Fit", DataTypeActivity, `{"date":"2026-03-01","brand":18,"modelid":1055,"model":"GoogleFit tracker","is_tracker":false}`, true},
		{"activity from a Withings tracker", DataTypeActivity, `{"date":"2026-03-01","brand":1,"modelid":53,"model":"Activite (Pop, Steel)","is_tracker":true}`, false},
		{"activity from the Withings app", DataTypeActivity, `{"date":"2026-03-01","brand":1,"modelid":null,"model":null,"is_tracker":false}`, false},
		{"activity without brand", DataTypeActivity, `{"date":"2026-03-01","steps":10}`, false},
		{"intraday from Health Connect", DataTypeIntraday, `{"timestamp":1772359200,"heart_rate":60,"duration":2,"model":"Google Health Connect","model_id":1060,"deviceid":null}`, true},
		{"intraday from a ScanWatch", DataTypeIntraday, `{"timestamp":1772359200,"heart_rate":60,"duration":2,"model":"ScanWatch","model_id":93}`, false},
		{"intraday without model", DataTypeIntraday, `{"timestamp":1772359200,"steps":100}`, false},
		{"workout from Health Connect", DataTypeWorkout, `{"id":1,"model":1060,"attrib":0,"startdate":1772470800}`, true},
		{"workout from a ScanWatch", DataTypeWorkout, `{"id":1,"model":93,"attrib":7,"startdate":1772470800}`, false},
		{"sleep from Apple Watch", DataTypeSleep, `{"id":1,"model":16,"model_id":1058,"startdate":1772402400}`, true},
		{"sleep from a Sleep Analyzer", DataTypeSleep, `{"id":1,"model":32,"model_id":63,"startdate":1772402400}`, false},
		{"synthetic night", DataTypeSleep, `{"id":1,"startdate":1772402400,"synthetic":true}`, false},
		{"measure from a Body+", DataTypeMeasure, `{"grpid":1,"attrib":0,"model":"Body+","modelid":5}`, false},
		{"measure entered by hand", DataTypeMeasure, `{"grpid":1,"attrib":2,"model":null,"modelid":null}`, false},
		{"measure from Health Connect", DataTypeMeasure, `{"grpid":1,"attrib":0,"model":"Google Health Connect","modelid":1060}`, true},
		{"measure model id as a string", DataTypeMeasure, `{"grpid":1,"modelid":"1057"}`, true},
		{"partner device is not an import", DataTypeMeasure, `{"grpid":1,"attrib":0,"model":"iGlucose Plus","modelid":1061}`, false},
		{"ECG is never dropped", DataTypeECG, `{"signalid":1,"model":1060}`, false},
		{"unreadable line is kept", DataTypeIntraday, `not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, imported(tc.dataType, []byte(tc.line)))
		})
	}
}

// importedBodies answer each data endpoint with one imported record and one
// a Withings device or the Withings app recorded, on 2026-03-01.
func importedBodies() map[string]string {
	return map[string]string{
		"/measure getmeas": `{"status":0,"body":{"timezone":"Europe/Paris","more":0,"offset":0,"measuregrps":[
			{"grpid":11,"attrib":0,"date":1772359200,"category":1,"model":"Body+","modelid":5,"measures":[{"value":70000,"type":1,"unit":-3}]},
			{"grpid":12,"attrib":2,"date":1772359300,"category":1,"model":null,"modelid":null,"measures":[{"value":70100,"type":1,"unit":-3}]},
			{"grpid":13,"attrib":0,"date":1772359400,"category":1,"model":"Google Health Connect","modelid":1060,"measures":[{"value":71000,"type":1,"unit":-3}]}]}}`,
		"/v2/measure getactivity": `{"status":0,"body":{"more":false,"offset":0,"activities":[
			{"date":"2026-03-01","timezone":"Europe/Paris","brand":18,"modelid":1055,"model":"GoogleFit tracker","is_tracker":false,"steps":8000},
			{"date":"2026-03-02","timezone":"Europe/Paris","brand":1,"modelid":null,"model":null,"is_tracker":false,"steps":4000}]}}`,
		"/v2/measure getintradayactivity": `{"status":0,"body":{"series":{
			"1772359200":{"heart_rate":60,"duration":2,"model":"Google Health Connect","model_id":1060,"deviceid":null},
			"1772359260":{"steps":30,"duration":60,"model":"Activite (Pop, Steel)","model_id":53,"deviceid":"fake-watch-0001"}}}}`,
		"/v2/measure getworkouts": `{"status":0,"body":{"more":false,"offset":0,"series":[
			{"id":3101,"category":1,"model":1060,"attrib":0,"startdate":1772359200,"enddate":1772362800,"date":"2026-03-01","timezone":"Europe/Paris","data":{}},
			{"id":3102,"category":1,"model":93,"attrib":7,"startdate":1772366400,"enddate":1772370000,"date":"2026-03-01","timezone":"Europe/Paris","data":{}}]}}`,
		"/v2/sleep getsummary": `{"status":0,"body":{"more":false,"offset":0,"series":[
			{"id":2101,"model":16,"model_id":1058,"startdate":1772402400,"enddate":1772406000,"date":"2026-03-02","timezone":"Europe/Paris","data":{}}]}}`,
		"/v2/sleep get": `{"status":0,"body":{"series":[
			{"startdate":1772402400,"enddate":1772404200,"state":1,"model":"HealthKit step Apple Watch tracker","model_id":1058}]}}`,
	}
}

func TestFetch_SkipImported(t *testing.T) {
	count := func(t *testing.T, skip bool) map[string]int {
		t.Helper()
		f := newFakeWithings(t)
		f.Bodies = importedBodies()
		c := f.connector()
		c.SetSkipImported(func() bool { return skip })
		sink := &recordingSink{}
		to := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
		types := []string{DataTypeMeasure, DataTypeActivity, DataTypeIntraday, DataTypeWorkout, DataTypeSleep}
		require.NoError(t, c.FetchTypes(context.Background(), testConn(), fetchFrom, to, types, sink))
		got := map[string]int{}
		for _, dt := range types {
			got[dt] = len(sink.linesOf(dt))
		}
		return got
	}

	assert.Equal(t, map[string]int{
		DataTypeMeasure: 3, DataTypeActivity: 2, DataTypeIntraday: 2, DataTypeWorkout: 2, DataTypeSleep: 1,
	}, count(t, false), "off: everything Withings returns is kept")
	assert.Equal(t, map[string]int{
		DataTypeMeasure: 2, DataTypeActivity: 1, DataTypeIntraday: 1, DataTypeWorkout: 1, DataTypeSleep: 0,
	}, count(t, true), "on: imported records are left out")
}

// Segments an import recorded become neither phases nor a synthetic night.
func TestFetchSleep_SkipImportedSegments(t *testing.T) {
	f := newFakeWithings(t)
	f.NoSleepSummary = true
	f.Bodies = map[string]string{"/v2/sleep get": `{"status":0,"body":{"series":[
		{"startdate":1772402400,"enddate":1772404200,"state":1,"model":"Google Health Connect","model_id":1060},
		{"startdate":1772404200,"enddate":1772406000,"state":2,"model":"Aura Sensor V2","model_id":63}]}}`}
	c := f.connector()
	c.SetSkipImported(func() bool { return true })
	sink := &recordingSink{}
	require.NoError(t, c.FetchTypes(context.Background(), testConn(), fetchFrom, fetchFrom.Add(24*time.Hour), []string{DataTypeSleep}, sink))
	lines := sink.linesOf(DataTypeSleep)
	require.NotEmpty(t, lines)
	for _, l := range lines {
		var s SleepSeries
		require.NoError(t, json.Unmarshal(l, &s))
		for _, ph := range s.SleepPhases {
			assert.NotEqual(t, int64(1772402400), ph.Startdate, "the imported segment is left out")
		}
		assert.NotContains(t, string(l), "model_id", "stored phases keep their shape")
	}
}

// Instances made for tenant keys share the switch, set before or after.
func TestSkipImported_SharedWithInstances(t *testing.T) {
	c := New(Options{})
	before := c.NewInstance().(*Connector)
	assert.False(t, before.skipImported())
	c.SetSkipImported(func() bool { return true })
	after := c.NewInstance().(*Connector)
	assert.True(t, before.skipImported())
	assert.True(t, after.skipImported())
}
