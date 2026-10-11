// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/whoop/whoopfake"
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

// fixture returns record i of a whoopfake collection fixture as one line.
func fixture(t *testing.T, file string, i int) string {
	t.Helper()
	var all struct {
		Records []json.RawMessage `json:"records"`
	}
	require.NoError(t, json.Unmarshal(whoopfake.File(file), &all))
	return string(all.Records[i])
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// --- Registration and dispatch ---

func TestRegistered(t *testing.T) {
	c, ok := connector.Get(Slug)
	require.True(t, ok, "whoop registers itself in init()")
	assert.Equal(t, "whoop", c.Slug())
	_, isOAuth := connector.AsOAuth(c)
	_, isPush := connector.AsPush(c)
	assert.True(t, isOAuth)
	assert.True(t, isPush)
	_, isSub := c.(connector.Subscriber)
	assert.False(t, isSub, "WHOOP webhooks are set in the dashboard, not per user")
}

func TestMap_UnknownDataTypeIsEmpty(t *testing.T) {
	res := mustMap(t, "unknown_xyz", `{"a":1}`)
	assert.True(t, res.IsEmpty())
	res = mustMap(t, "", `{}`)
	assert.True(t, res.IsEmpty())
}

// A field of the wrong JSON type is an error, whatever the data type.
func TestMap_MalformedErrors(t *testing.T) {
	for dt, line := range map[string]string{
		DataTypeCycle:    `{"id":1,"start":42}`,
		DataTypeRecovery: `{"cycle_id":1,"created_at":"2026-03-01T07:00:00Z","score":"high"}`,
		DataTypeSleep:    `{"id":"s","start":"2026-03-01T00:00:00Z","end":"2026-03-01T07:00:00Z","nap":"no"}`,
		DataTypeWorkout:  `{"id":"w","start":"2026-03-01T00:00:00Z","end":"2026-03-01T01:00:00Z","score":[1]}`,
		DataTypeBody:     `{"weight_kilogram":"heavy","snapshot_date":"2026-03-03"}`,
	} {
		_, err := mapLine(t, dt, line)
		assert.Error(t, err, dt)
	}
}

// Every quantity this connector emits reaches its declared storage unit.
func TestEmittedUnitsReachStorage(t *testing.T) {
	lines := map[string]string{
		DataTypeCycle:    fixture(t, "cycles.json", 1),
		DataTypeRecovery: fixture(t, "recoveries.json", 1),
		DataTypeSleep:    fixture(t, "sleeps.json", 0),
		DataTypeWorkout:  fixture(t, "workouts.json", 0),
		DataTypeBody:     `{"height_meter":1.8,"weight_kilogram":75.5,"max_heart_rate":190,"snapshot_date":"2026-03-03"}`,
	}
	seen := map[string]bool{}
	for dt, line := range lines {
		res := mustMap(t, dt, line)
		for _, s := range res.QuantitySamples {
			seen[s.QuantityType] = true
			want, declared := model.StorageUnit(s.QuantityType)
			require.True(t, declared, "%s has no declared storage unit", s.QuantityType)
			_, unit, err := model.ToStorageUnit(s.QuantityType, s.Value, s.Unit)
			require.NoError(t, err, s.QuantityType)
			assert.Equal(t, want, unit, s.QuantityType)
		}
	}
	for _, qt := range []string{
		model.QTStrain, model.QTHeartRate, model.QTRestingHeartRate, model.QTHRVRMSSD,
		model.QTOxygenSaturation, model.QTSkinTemperature, model.QTRespiratoryRate,
		model.QTWorkoutStrain, "body_mass", "height",
	} {
		assert.True(t, seen[qt], "%s is emitted", qt)
	}
}

// --- Cycle ---

func TestMapCycle(t *testing.T) {
	res := mustMap(t, DataTypeCycle, fixture(t, "cycles.json", 2))
	m := byType(res.QuantitySamples)
	require.Len(t, res.QuantitySamples, 2)

	st := m[model.QTStrain]
	assert.InDelta(t, 8.25, st.Value, 1e-9)
	assert.Equal(t, "index", st.Unit)
	assert.Equal(t, "whoop", st.Provider)
	assert.Equal(t, "whoop", st.SourceSlug)
	assert.Equal(t, testUserID, st.UserID)
	assert.Equal(t, "93002_strain", st.ExternalID)
	assert.True(t, st.StartDate.Equal(ts("2026-03-01T22:30:00Z")))
	assert.True(t, st.EndDate.Equal(ts("2026-03-02T23:00:00Z")))
	assert.True(t, st.Replaceable, "a re-scored cycle replaces its strain")
	assert.Equal(t, "Etc/GMT-1", st.Metadata["timezone"], "+01:00 as an IANA name")
	assert.Equal(t, "+01:00", st.Metadata["timezone_offset"])
	assert.InDelta(t, 8400.0, st.Metadata["kilojoule"], 1e-9)

	hr := m[model.QTHeartRate]
	assert.InDelta(t, 62.0, hr.Value, 1e-9)
	assert.Equal(t, "count/min", hr.Unit)
	assert.Equal(t, "cycle_average", hr.Metadata["aggregate"])

	_, active := m[model.QTActiveEnergyBurned]
	assert.False(t, active, "cycle energy includes resting metabolism: never active energy")

	require.Len(t, res.DailySummaries, 1)
	d := res.DailySummaries[0]
	assert.Equal(t, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), d.SummaryDate, "a cycle starting at 23:30 local is the next day's")
	assert.InDelta(t, 8.25, *d.StrainScore, 1e-9)
	assert.InDelta(t, 8400/4.184, *d.TotalKcal, 1e-6)
	assert.Nil(t, d.ActiveKcal)
	assert.Nil(t, d.RecoveryScore, "left for the recovery to fill")
	assert.True(t, d.Replaceable)
}

func TestMapCycle_OpenCycle(t *testing.T) {
	res := mustMap(t, DataTypeCycle, fixture(t, "cycles.json", 3))
	st := byType(res.QuantitySamples)[model.QTStrain]
	assert.True(t, st.EndDate.Equal(st.StartDate), "no end yet")
	assert.Equal(t, true, st.Metadata["cycle_open"])
}

// The current cycle's strain grows: a later copy must hit the same identity.
func TestMapCycle_HashIgnoresTheValue(t *testing.T) {
	a := mustMap(t, DataTypeCycle, `{"id":7,"start":"2026-03-02T23:00:00.000Z","end":null,"timezone_offset":"+01:00","score_state":"SCORED","score":{"strain":2.5}}`)
	b := mustMap(t, DataTypeCycle, `{"id":7,"start":"2026-03-02T23:00:00.000Z","end":null,"timezone_offset":"+01:00","score_state":"SCORED","score":{"strain":9.75}}`)
	assert.Equal(t, a.QuantitySamples[0].ContentHash, b.QuantitySamples[0].ContentHash)
	assert.Equal(t, a.DailySummaries[0].ContentHash, b.DailySummaries[0].ContentHash)
	c := mustMap(t, DataTypeCycle, `{"id":8,"start":"2026-03-02T23:00:00.000Z","score_state":"SCORED","score":{"strain":2.5}}`)
	assert.NotEqual(t, a.QuantitySamples[0].ContentHash, c.QuantitySamples[0].ContentHash, "another cycle")
}

func TestMapCycle_NotScored(t *testing.T) {
	for _, state := range []string{"PENDING_SCORE", "UNSCORABLE"} {
		res := mustMap(t, DataTypeCycle, `{"id":7,"start":"2026-03-02T23:00:00.000Z","score_state":"`+state+`"}`)
		assert.True(t, res.IsEmpty(), state)
	}
}

func TestMapCycle_MissingFieldsError(t *testing.T) {
	_, err := mapLine(t, DataTypeCycle, `{"start":"2026-03-02T23:00:00.000Z","score_state":"SCORED"}`)
	assert.Error(t, err, "no id")
	_, err = mapLine(t, DataTypeCycle, `{"id":7,"score_state":"SCORED"}`)
	assert.Error(t, err, "no start")
	_, err = mapLine(t, DataTypeCycle, `{"id":7,"start":"yesterday","score_state":"SCORED"}`)
	assert.Error(t, err, "bad start")
}

// A missing figure is skipped, not stored as zero.
func TestMapCycle_MissingValueSkipped(t *testing.T) {
	res := mustMap(t, DataTypeCycle, `{"id":7,"start":"2026-03-02T23:00:00.000Z","end":"2026-03-03T23:00:00.000Z","score_state":"SCORED","score":{"strain":5}}`)
	require.Len(t, res.QuantitySamples, 1)
	assert.Equal(t, model.QTStrain, res.QuantitySamples[0].QuantityType)
	assert.Nil(t, res.DailySummaries[0].TotalKcal)
}

// --- Recovery ---

func TestMapRecovery(t *testing.T) {
	line := `{"cycle_id":93002,"sleep_id":"aaaaaaaa-0000-4000-8000-000000000003","user_id":10001,
	  "created_at":"2026-03-02T07:05:00.000Z","score_state":"SCORED",
	  "score":{"user_calibrating":true,"recovery_score":48,"resting_heart_rate":55,
	           "hrv_rmssd_milli":41.25,"spo2_percentage":95.75,"skin_temp_celsius":33.875},
	  "cycle":{"start":"2026-03-01T22:30:00.000Z","end":"2026-03-02T23:00:00.000Z","timezone_offset":"+01:00"}}`
	res := mustMap(t, DataTypeRecovery, line)
	m := byType(res.QuantitySamples)
	require.Len(t, res.QuantitySamples, 4)

	at := ts("2026-03-02T07:05:00Z")
	for qt, want := range map[string]struct {
		v    float64
		unit string
	}{
		model.QTRestingHeartRate: {55, "count/min"},
		model.QTHRVRMSSD:         {41.25, "ms"},
		model.QTOxygenSaturation: {95.75, "percent"},
		model.QTSkinTemperature:  {33.875, "celsius"},
	} {
		s := m[qt]
		assert.InDelta(t, want.v, s.Value, 1e-9, qt)
		assert.Equal(t, want.unit, s.Unit, qt)
		assert.True(t, s.StartDate.Equal(at), qt)
		assert.True(t, s.EndDate.Equal(at), qt)
		assert.True(t, s.Replaceable, qt)
		assert.Equal(t, "Etc/GMT-1", s.Metadata["timezone"], qt)
		assert.Equal(t, "93002", s.Metadata["whoop_cycle_id"], qt)
		assert.Equal(t, true, s.Metadata["user_calibrating"], qt)
	}
	_, sdnn := m[model.QTHRVSDNN]
	assert.False(t, sdnn, "WHOOP's HRV is RMSSD, never hrv_sdnn")

	require.Len(t, res.DailySummaries, 1)
	d := res.DailySummaries[0]
	assert.Equal(t, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), d.SummaryDate)
	assert.InDelta(t, 48.0, *d.RecoveryScore, 1e-9)
	assert.InDelta(t, 55.0, *d.RestingHR, 1e-9)
	assert.Nil(t, d.HRVAvgMs, "RMSSD is not written into the generic HRV column")
	assert.Nil(t, d.StrainScore, "left for the cycle to fill")

	cyc := mustMap(t, DataTypeCycle, fixture(t, "cycles.json", 2))
	assert.Equal(t, cyc.DailySummaries[0].ContentHash, d.ContentHash, "cycle and recovery of a day share the summary row")
}

// A re-scored recovery replaces the stored figures.
func TestMapRecovery_RescoreKeepsIdentity(t *testing.T) {
	a := mustMap(t, DataTypeRecovery, `{"cycle_id":1,"created_at":"2026-03-02T07:05:00.000Z","score_state":"SCORED","score":{"hrv_rmssd_milli":40}}`)
	b := mustMap(t, DataTypeRecovery, `{"cycle_id":1,"created_at":"2026-03-02T07:05:00.000Z","updated_at":"2026-03-02T09:00:00.000Z","score_state":"SCORED","score":{"hrv_rmssd_milli":44}}`)
	assert.Equal(t, a.QuantitySamples[0].ContentHash, b.QuantitySamples[0].ContentHash)
	assert.True(t, a.QuantitySamples[0].StartDate.Equal(b.QuantitySamples[0].StartDate))
}

// Without its cycle, the summary falls back to the UTC day of created_at.
func TestMapRecovery_WithoutCycle(t *testing.T) {
	res := mustMap(t, DataTypeRecovery, `{"cycle_id":1,"created_at":"2026-03-02T07:05:00.000Z","score_state":"SCORED","score":{"recovery_score":80}}`)
	require.Len(t, res.DailySummaries, 1)
	assert.Equal(t, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), res.DailySummaries[0].SummaryDate)
	assert.Empty(t, res.QuantitySamples)
	_, tz := res.DailySummaries[0].Metadata["timezone"]
	assert.False(t, tz)
}

func TestMapRecovery_PendingIsEmpty(t *testing.T) {
	res := mustMap(t, DataTypeRecovery, fixture(t, "recoveries.json", 3))
	assert.True(t, res.IsEmpty())
}

func TestMapRecovery_MissingFieldsError(t *testing.T) {
	_, err := mapLine(t, DataTypeRecovery, `{"created_at":"2026-03-02T07:05:00.000Z","score_state":"SCORED","score":{}}`)
	assert.Error(t, err, "no cycle_id")
	_, err = mapLine(t, DataTypeRecovery, `{"cycle_id":1,"score_state":"SCORED","score":{}}`)
	assert.Error(t, err, "no created_at")
}

// --- Sleep ---

func TestMapSleep(t *testing.T) {
	res := mustMap(t, DataTypeSleep, fixture(t, "sleeps.json", 0))
	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	assert.Equal(t, testUserID, s.UserID)
	assert.Equal(t, "whoop", s.Provider)
	assert.Equal(t, "aaaaaaaa-0000-4000-8000-000000000001", s.ExternalID)
	assert.True(t, s.BedtimeStart.Equal(ts("2026-03-01T00:15:00Z")))
	assert.True(t, s.BedtimeEnd.Equal(ts("2026-03-01T07:00:00Z")))
	assert.Equal(t, 5400, *s.DeepSleepSeconds, "slow wave sleep is deep")
	assert.Equal(t, 4500, *s.REMSleepSeconds)
	assert.Equal(t, 12600, *s.LightSleepSeconds)
	assert.Equal(t, 1800, *s.AwakeSeconds)
	assert.Equal(t, 22500, *s.TotalSleepSeconds, "light + deep + REM")
	assert.InDelta(t, 88.0, *s.SleepScore, 1e-9)
	assert.InDelta(t, 14.5, *s.RespiratoryRate, 1e-9)
	assert.Nil(t, s.HRVAvgMs)
	assert.Equal(t, "Etc/GMT-1", s.Metadata["timezone"])
	assert.Equal(t, false, s.Metadata["nap"])
	assert.Equal(t, 24300, s.Metadata["in_bed_seconds"])
	assert.EqualValues(t, 9, s.Metadata["disturbance_count"])
	assert.InDelta(t, 92.5, s.Metadata["sleep_efficiency_percentage"], 1e-9)
	assert.NotEmpty(t, s.ContentHash)
	assert.Empty(t, res.SleepPhases, "the public API has no hypnogram")

	require.Len(t, res.QuantitySamples, 1)
	rr := res.QuantitySamples[0]
	assert.Equal(t, model.QTRespiratoryRate, rr.QuantityType)
	assert.InDelta(t, 14.5, rr.Value, 1e-9)
	assert.True(t, rr.StartDate.Equal(s.BedtimeStart))
	assert.True(t, rr.EndDate.Equal(s.BedtimeEnd))
}

func TestMapSleep_ContentHashDeterministic(t *testing.T) {
	a := mustMap(t, DataTypeSleep, fixture(t, "sleeps.json", 0))
	b := mustMap(t, DataTypeSleep, fixture(t, "sleeps.json", 0))
	assert.Equal(t, a.SleepSessions[0].ContentHash, b.SleepSessions[0].ContentHash)
	assert.Equal(t, a.QuantitySamples[0].ContentHash, b.QuantitySamples[0].ContentHash)
}

func TestMapSleep_UnscorableNap(t *testing.T) {
	res := mustMap(t, DataTypeSleep, fixture(t, "sleeps.json", 1))
	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	assert.Equal(t, true, s.Metadata["nap"])
	assert.Nil(t, s.TotalSleepSeconds)
	assert.Nil(t, s.SleepScore)
	assert.Empty(t, res.QuantitySamples)
}

// A pending sleep would freeze unscored: it waits for its scored copy.
func TestMapSleep_PendingIsEmpty(t *testing.T) {
	res := mustMap(t, DataTypeSleep, fixture(t, "sleeps.json", 2))
	assert.True(t, res.IsEmpty())
}

func TestMapSleep_MissingFieldsError(t *testing.T) {
	_, err := mapLine(t, DataTypeSleep, `{"start":"2026-03-01T00:00:00Z","end":"2026-03-01T07:00:00Z"}`)
	assert.Error(t, err, "no id")
	_, err = mapLine(t, DataTypeSleep, `{"id":"s","start":"2026-03-01T00:00:00Z"}`)
	assert.Error(t, err, "no end")
}

// --- Workout ---

func TestMapWorkout(t *testing.T) {
	res := mustMap(t, DataTypeWorkout, fixture(t, "workouts.json", 0))
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	assert.Equal(t, testUserID, w.UserID)
	assert.Equal(t, "whoop", w.Provider)
	assert.Equal(t, "bbbbbbbb-0000-4000-8000-000000000001", w.ExternalID)
	assert.Equal(t, "running", w.ActivityType)
	assert.Equal(t, 2700, *w.DurationS)
	assert.InDelta(t, 500.0, *w.ActiveEnergyKcal, 1e-9, "2092 kJ")
	assert.InDelta(t, 7500.0, *w.DistanceM, 1e-9)
	assert.InDelta(t, 148.0, *w.AvgHeartRate, 1e-9)
	assert.InDelta(t, 176.0, *w.MaxHeartRate, 1e-9)
	assert.InDelta(t, 45.0, *w.ElevationGainM, 1e-9)
	assert.Equal(t, "Etc/GMT-1", w.Metadata["timezone"])
	assert.Equal(t, "running", w.Metadata["sport_name"])
	assert.EqualValues(t, 0, w.Metadata["sport_id"])
	assert.InDelta(t, 12.25, w.Metadata["strain"], 1e-9)
	assert.NotNil(t, w.Metadata["zone_durations"])

	require.Len(t, res.QuantitySamples, 1)
	s := res.QuantitySamples[0]
	assert.Equal(t, model.QTWorkoutStrain, s.QuantityType, "never the daily strain key")
	assert.InDelta(t, 12.25, s.Value, 1e-9)
	assert.Equal(t, "index", s.Unit)
	assert.True(t, s.StartDate.Equal(w.StartDate))
}

func TestMapWorkout_NoDistance(t *testing.T) {
	res := mustMap(t, DataTypeWorkout, fixture(t, "workouts.json", 1))
	w := res.Workouts[0]
	assert.Equal(t, "traditional_strength_training", w.ActivityType)
	assert.Nil(t, w.DistanceM)
	assert.Nil(t, w.ElevationGainM)
	assert.InDelta(t, 300.0, *w.ActiveEnergyKcal, 1e-9)
}

func TestMapWorkout_PendingIsEmpty(t *testing.T) {
	res := mustMap(t, DataTypeWorkout, `{"id":"w","start":"2026-03-01T00:00:00Z","end":"2026-03-01T01:00:00Z","sport_name":"running","score_state":"PENDING_SCORE"}`)
	assert.True(t, res.IsEmpty())
}

func TestMapWorkout_UnknownSport(t *testing.T) {
	res := mustMap(t, DataTypeWorkout, `{"id":"w","start":"2026-03-01T00:00:00Z","end":"2026-03-01T01:00:00Z","sport_name":"Underwater Origami","sport_id":999,"score_state":"UNSCORABLE"}`)
	require.Len(t, res.Workouts, 1)
	assert.Equal(t, "other", res.Workouts[0].ActivityType)
	assert.Equal(t, "Underwater Origami", res.Workouts[0].Metadata["sport_name"])
	assert.Nil(t, res.Workouts[0].ActiveEnergyKcal)
	assert.Empty(t, res.QuantitySamples)
}

func TestMapWorkout_MissingDatesError(t *testing.T) {
	_, err := mapLine(t, DataTypeWorkout, `{"id":"w","start":"2026-03-01T00:00:00Z"}`)
	assert.Error(t, err)
}

func TestWhoopSport(t *testing.T) {
	for name, want := range map[string]workoutType{
		"running":            {activity: "running"},
		"Running":            {activity: "running"},
		"functional-fitness": {activity: "functional_strength_training"},
		"Hiking/Rucking":     {activity: "hiking"},
		"spin":               {activity: "cycling", indoor: true},
		"weightlifting":      {activity: "traditional_strength_training"},
	} {
		assert.Equal(t, want, whoopSport(name), name)
	}
	assert.Equal(t, workoutType{activity: "other"}, whoopSport("no such sport"))
}

// Every activity name is one the Apple Health connector also uses (or other).
func TestSportsUseTheSharedVocabulary(t *testing.T) {
	shared := map[string]bool{"other": true}
	for _, v := range []string{"walking", "running", "hiking", "skating_sports", "cycling", "swimming",
		"surfing_sports", "paddle_sports", "tennis", "table_tennis", "squash", "badminton", "pickleball",
		"traditional_strength_training", "functional_strength_training", "high_intensity_interval_training",
		"cross_training", "elliptical", "stair_climbing", "pilates", "basketball", "soccer",
		"american_football", "australian_football", "rugby", "volleyball", "water_polo", "golf", "yoga",
		"dance", "boxing", "kickboxing", "fencing", "wrestling", "martial_arts", "downhill_skiing",
		"cross_country_skiing", "snowboarding", "rowing", "baseball", "softball", "hockey", "lacrosse",
		"cricket", "climbing", "jump_rope", "mind_and_body", "flexibility"} {
		shared[v] = true
	}
	for name, wt := range sports {
		assert.True(t, shared[wt.activity], "sport %q maps to %q", name, wt.activity)
	}
}

// --- Body ---

func TestMapBody(t *testing.T) {
	res := mustMap(t, DataTypeBody, `{"height_meter":1.8,"weight_kilogram":75.5,"max_heart_rate":190,"snapshot_date":"2026-03-03"}`)
	m := byType(res.QuantitySamples)
	require.Len(t, res.QuantitySamples, 2)
	day := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)

	w := m["body_mass"]
	assert.InDelta(t, 75.5, w.Value, 1e-9)
	assert.Equal(t, "kg", w.Unit)
	assert.True(t, w.StartDate.Equal(day))
	assert.True(t, w.Replaceable)
	assert.InDelta(t, 190.0, w.Metadata["max_heart_rate"], 1e-9)

	h := m["height"]
	v, unit, err := model.ToStorageUnit(h.QuantityType, h.Value, h.Unit)
	require.NoError(t, err)
	assert.InDelta(t, 180.0, v, 1e-9, "metres reach centimetres")
	assert.Equal(t, "cm", unit)

	// A change during the day replaces; another day is its own sample.
	again := mustMap(t, DataTypeBody, `{"weight_kilogram":76,"snapshot_date":"2026-03-03"}`)
	assert.Equal(t, w.ContentHash, again.QuantitySamples[0].ContentHash)
	next := mustMap(t, DataTypeBody, `{"weight_kilogram":75.5,"snapshot_date":"2026-03-04"}`)
	assert.NotEqual(t, w.ContentHash, next.QuantitySamples[0].ContentHash)
}

func TestMapBody_MissingValuesSkipped(t *testing.T) {
	res := mustMap(t, DataTypeBody, `{"max_heart_rate":190,"snapshot_date":"2026-03-03"}`)
	assert.True(t, res.IsEmpty())
}

func TestMapBody_MissingDateErrors(t *testing.T) {
	_, err := mapLine(t, DataTypeBody, `{"weight_kilogram":75.5}`)
	assert.Error(t, err)
	_, err = mapLine(t, DataTypeBody, `{"weight_kilogram":75.5,"snapshot_date":"03/03/2026"}`)
	assert.Error(t, err)
}

// --- Time ---

func TestIANAForOffset(t *testing.T) {
	for in, want := range map[string]string{
		"+01:00": "Etc/GMT-1",
		"-05:00": "Etc/GMT+5",
		"+00:00": "Etc/UTC",
		"Z":      "Etc/UTC",
		"+14:00": "Etc/GMT-14",
		"+05:30": "",
		"bogus":  "",
		"":       "",
	} {
		assert.Equal(t, want, ianaForOffset(in), in)
	}
	loc, err := time.LoadLocation("Etc/GMT-1")
	require.NoError(t, err)
	_, off := time.Date(2026, 3, 1, 12, 0, 0, 0, loc).Zone()
	assert.Equal(t, 3600, off, "Etc/GMT-1 is UTC+1")
}

func TestCycleDay(t *testing.T) {
	assert.Equal(t, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), cycleDay(ts("2026-03-01T22:30:00Z"), "+01:00"), "23:30 local")
	assert.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), cycleDay(ts("2026-03-01T00:15:00Z"), "+01:00"), "01:15 local")
	assert.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), cycleDay(ts("2026-03-01T04:00:00Z"), "-05:00"), "23:00 the evening before, local")
}

func TestProviderIsLowercase(t *testing.T) {
	res := mustMap(t, DataTypeWorkout, fixture(t, "workouts.json", 0))
	assert.Equal(t, model.Slug(res.Workouts[0].Provider), res.Workouts[0].Provider)
}
