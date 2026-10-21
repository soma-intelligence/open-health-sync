// SPDX-License-Identifier: AGPL-3.0-only

package fhir

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
)

// Invented records of every kind, as the read layer returns them.

func bpPair() (read.CodedSample, read.CodedSample) {
	sys := sample(1003, "blood_pressure_systolic", 118, "mmHg", "8480-6", "mm[Hg]")
	dia := sample(1013, "blood_pressure_diastolic", 76, "mmHg", "8462-4", "mm[Hg]")
	sys.ExternalID = ptr("bp-reading-0001-sys")
	dia.ExternalID = ptr("bp-reading-0001-dia")
	dia.IngestedAt = ingested.Add(2 * time.Second)
	return sys, dia
}

func sleepSession() read.SleepSession {
	return read.SleepSession{
		ID:                7007,
		BedtimeStart:      time.Date(2026, 2, 28, 22, 40, 0, 0, time.UTC),
		BedtimeEnd:        ptr(time.Date(2026, 3, 1, 6, 55, 0, 0, time.UTC)),
		TotalSleepSeconds: ptr(26400),
		DeepSleepSeconds:  ptr(5400),
		REMSleepSeconds:   ptr(6300),
		LightSleepSeconds: ptr(14700),
		AwakeSeconds:      ptr(3300),
		SleepScore:        ptr(81.0),
		HRVAvgMs:          ptr(48.5),
		RestingHR:         ptr(51.0),
		RespiratoryRate:   ptr(14.2),
		SpO2Avg:           ptr(96.0),
		ExternalID:        ptr("sleep-invented-42"),
		Provenance:        read.Provenance{Provider: "oura", Source: ptr("oura"), Device: ptr("oura_ring"), IngestedAt: ingested},
	}
}

func workout() read.Workout {
	return read.Workout{
		ID:               8008,
		ActivityType:     "trail_running",
		StartDate:        t0,
		EndDate:          ptr(t0.Add(52 * time.Minute)),
		DurationS:        ptr(3120),
		ActiveEnergyKcal: ptr(512.5),
		DistanceM:        ptr(8420.0),
		AvgHeartRate:     ptr(148.0),
		MaxHeartRate:     ptr(176.0),
		AvgSpeedMps:      ptr(2.7),
		ElevationGainM:   ptr(310.0),
		VO2Max:           ptr(47.5),
		Provenance:       read.Provenance{Provider: "strava", Source: ptr("garmin"), Device: ptr("garmin_watch"), IngestedAt: ingested},
	}
}

// dailySummary is a day of a daylight saving change in its zone: 23 hours.
func dailySummary() read.DailySummary {
	return read.DailySummary{
		ID:             9009,
		Date:           "2026-03-29",
		ReadinessScore: ptr(77.0),
		ActivityScore:  ptr(64.0),
		SleepScore:     ptr(81.0),
		StepCount:      ptr(10432),
		ActiveKcal:     ptr(431.0),
		TotalKcal:      ptr(2310.0),
		DistanceM:      ptr(7650.0),
		FloorsClimbed:  ptr(9),
		RestingHR:      ptr(52.0),
		HRVAvgMs:       ptr(47.0),
		Metadata:       json.RawMessage(`{"timezone":"Europe/Paris"}`),
		Provenance:     read.Provenance{Provider: "oura", Source: ptr("oura"), IngestedAt: ingested},
	}
}

// nutritionLog is an invented Health Connect lunch: the seven columns, the
// extended nutrients of nutrients_json, a name the table does not know (left
// out) and a meal type.
func nutritionLog() read.NutritionLog {
	return read.NutritionLog{
		ID:         12012,
		StartDate:  time.Date(2026, 3, 1, 11, 30, 0, 0, time.UTC),
		EndDate:    ptr(time.Date(2026, 3, 1, 11, 50, 0, 0, time.UTC)),
		MealType:   ptr("lunch"),
		EnergyKcal: ptr(420.0),
		ProteinG:   ptr(24.0),
		CarbsG:     ptr(52.5),
		FatG:       ptr(11.0),
		FiberG:     ptr(13.0),
		SugarG:     ptr(6.5),
		SodiumMg:   ptr(800.0),
		Nutrients:  json.RawMessage(`{"water":250,"fat_saturated":2.1,"caffeine":50,"vitamin_b6":0.4,"vitamin_d":2.5,"lutein":3}`),
		ExternalID: ptr("hc-nutrition-invented-7"),
		Metadata:   json.RawMessage(`{"food_name":"Lentil soup"}`),
		Provenance: read.Provenance{Provider: "health_connect", Source: ptr("health_connect"), IngestedAt: ingested},
	}
}

func recordCases() map[string]read.ObservationRecord {
	sys, dia := bpPair()
	sl, w, d, e, n := sleepSession(), workout(), dailySummary(), ecgRecording(), nutritionLog()
	return map[string]read.ObservationRecord{
		"observation-vital-blood-pressure-panel": {Kind: read.KindQuantity, Sample: &sys, Partner: &dia},
		"observation-sleep-session":              {Kind: read.KindSleep, Sleep: &sl},
		"observation-workout":                    {Kind: read.KindWorkout, Workout: &w},
		"observation-daily-summary":              {Kind: read.KindDaily, Daily: &d},
		"observation-ecg-recording":              {Kind: read.KindECG, ECG: &e},
		"observation-nutrition-log":              {Kind: read.KindNutrition, Nutrition: &n},
	}
}

func TestGoldenRecords(t *testing.T) {
	for name, r := range recordCases() {
		t.Run(name, func(t *testing.T) {
			golden(t, name, FromRecord(testUser, r))
		})
	}
}

// TestGoldenMixedSearchSet is a page holding one Observation of each kind,
// in the order the merged list gives them.
func TestGoldenMixedSearchSet(t *testing.T) {
	sys, dia := bpPair()
	single := sample(1020, "blood_pressure_diastolic", 79, "mmHg", "8462-4", "mm[Hg]")
	sl, w, d, e, n := sleepSession(), workout(), dailySummary(), ecgRecording(), nutritionLog()
	recs := []read.ObservationRecord{
		{Kind: read.KindSleep, Sleep: &sl},
		{Kind: read.KindQuantity, Sample: &sys, Partner: &dia},
		{Kind: read.KindQuantity, Sample: &single},
		{Kind: read.KindWorkout, Workout: &w},
		{Kind: read.KindECG, ECG: &e},
		{Kind: read.KindNutrition, Nutrition: &n},
		{Kind: read.KindDaily, Daily: &d},
	}
	var obs []Observation
	for _, r := range recs {
		obs = append(obs, FromRecord(testUser, r))
	}
	base := "http://localhost:8080/v1/users/" + testUser.String() + "/fhir"
	golden(t, "bundle-searchset-mixed", SearchSet(base, base+"/Observation?limit=7", base+"/Observation?limit=7&cursor=abc", obs))
}

func TestBloodPressurePanel(t *testing.T) {
	sys, dia := bpPair()
	o := FromBloodPressure(testUser, sys, dia)
	assert.Equal(t, "bp-1003", o.ID)
	assert.Equal(t, []string{profileBase + "vitalsigns", profileBase + "bp"}, o.Meta.Profile)
	assert.Equal(t, Coding{System: LOINC, Code: "85354-9"}, o.Code.Coding[0])
	assert.Nil(t, o.ValueQuantity, "the panel has no value of its own")
	require.Len(t, o.Component, 2)
	assert.Equal(t, "8480-6", o.Component[0].Code.Coding[0].Code)
	assert.Equal(t, 118.0, *o.Component[0].ValueQuantity.Value)
	assert.Equal(t, "8462-4", o.Component[1].Code.Coding[0].Code)
	assert.Equal(t, 76.0, *o.Component[1].ValueQuantity.Value)
	assert.Equal(t, "mm[Hg]", o.Component[1].ValueQuantity.Code)
	assert.Equal(t, "2026-03-01T09:15:32.25Z", o.Issued, "the later of the two")
	assert.Len(t, o.Identifier, 2)

	// A NaN half keeps the panel valid with a data absent reason.
	dia.Value = math.NaN()
	o = FromBloodPressure(testUser, sys, dia)
	assert.Nil(t, o.Component[1].ValueQuantity)
	assert.Equal(t, "not-a-number", o.Component[1].DataAbsentReason.Coding[0].Code)
	assert.NotEmpty(t, o.Meta.Profile)

	// A half in a unit the profile does not accept: the values stay, the
	// panel claims neither the LOINC panel code nor a profile.
	dia.Value, dia.Unit = 10.1, ptr("kPa")
	o = FromBloodPressure(testUser, sys, dia)
	assert.Empty(t, o.Meta.Profile)
	assert.Equal(t, []Coding{{System: RecordTypeSystem, Code: RecordBloodPressure}}, o.Code.Coding)
	assert.Equal(t, 10.1, *o.Component[1].ValueQuantity.Value)
}

func TestRecordsWithoutValues(t *testing.T) {
	sl := read.SleepSession{ID: 1, BedtimeStart: t0, Provenance: read.Provenance{Provider: "oura", IngestedAt: ingested}}
	o := FromSleepSession(testUser, sl)
	assert.Nil(t, o.ValueQuantity)
	assert.Equal(t, "unknown", o.DataAbsentReason.Coding[0].Code)
	assert.Empty(t, o.Component)
	assert.Equal(t, "2026-03-01T08:00:00Z", o.EffectiveDateTime, "no end: an instant")
	assert.Empty(t, o.Category, "no HL7 category describes sleep")

	w := read.Workout{ID: 2, StartDate: t0, Provenance: read.Provenance{Provider: "strava", IngestedAt: ingested}}
	o = FromWorkout(testUser, w)
	assert.Equal(t, "workout", o.Code.Text)
	assert.Equal(t, "unknown", o.DataAbsentReason.Coding[0].Code)
	assert.Empty(t, o.Component, "no activity type, no measurement")

	d := read.DailySummary{ID: 3, Date: "2026-03-01", Metadata: json.RawMessage(`{"timezone":"Not/AZone"}`),
		Provenance: read.Provenance{Provider: "oura", IngestedAt: ingested}}
	o = FromDailySummary(testUser, d)
	assert.Equal(t, &Period{Start: "2026-03-01T00:00:00Z", End: "2026-03-02T00:00:00Z"}, o.EffectivePeriod, "an unknown zone is UTC")
	assert.Equal(t, "unknown", o.DataAbsentReason.Coding[0].Code)

	// A meal logged with no figure keeps its meal type.
	n := read.NutritionLog{ID: 4, StartDate: t0, EndDate: ptr(t0), MealType: ptr("snack"), Nutrients: json.RawMessage("null"),
		Provenance: read.Provenance{Provider: "health_connect", IngestedAt: ingested}}
	o = FromNutritionLog(testUser, n)
	assert.Equal(t, "unknown", o.DataAbsentReason.Coding[0].Code)
	require.Len(t, o.Component, 1)
	assert.Equal(t, "snack", o.Component[0].ValueCodeableConcept.Coding[0].Code)
	assert.Equal(t, "2026-03-01T08:00:00Z", o.EffectiveDateTime, "start and end equal: an instant")
	assert.Empty(t, o.Category, "no HL7 category describes intake")
}

func TestDailySummaryLocalDay(t *testing.T) {
	o := FromDailySummary(testUser, dailySummary())
	// Midnight in Paris is 23:00 UTC the day before; the clocks go forward
	// that night, so the day lasts 23 hours.
	assert.Equal(t, &Period{Start: "2026-03-28T23:00:00Z", End: "2026-03-29T22:00:00Z"}, o.EffectivePeriod)

	d := dailySummary()
	d.Metadata = nil
	o = FromDailySummary(testUser, d)
	assert.Equal(t, &Period{Start: "2026-03-29T00:00:00Z", End: "2026-03-30T00:00:00Z"}, o.EffectivePeriod)
}

func TestRecordIDs(t *testing.T) {
	sys, dia := bpPair()
	sl, w, d, e, n := sleepSession(), workout(), dailySummary(), ecgRecording(), nutritionLog()
	for want, r := range map[string]read.ObservationRecord{
		"qs-1003":         {Kind: read.KindQuantity, Sample: &sys},
		"bp-1003":         {Kind: read.KindQuantity, Sample: &sys, Partner: &dia},
		"sleep-7007":      {Kind: read.KindSleep, Sleep: &sl},
		"workout-8008":    {Kind: read.KindWorkout, Workout: &w},
		"daily-9009":      {Kind: read.KindDaily, Daily: &d},
		"ecg-11011":       {Kind: read.KindECG, ECG: &e},
		"nutrition-12012": {Kind: read.KindNutrition, Nutrition: &n},
	} {
		assert.Equal(t, want, RecordID(r))
		assert.Equal(t, want, FromRecord(testUser, r).ID)
		kind, panel, id, ok := ParseID(want)
		require.True(t, ok, want)
		assert.Equal(t, r.Kind, kind, want)
		assert.Equal(t, r.Partner != nil, panel, want)
		assert.Positive(t, id)
	}
	for _, bad := range []string{"", "1", "bp-", "bp-x", "sleep-0", "ecg-", "ecg-0", "ecg--1", "workout--2", "daily-+3", "qs-1-2", "xx-1", "sleep-99999999999999999999", "nutrition-", "nutrition-0", "nutrition-x"} {
		_, _, _, ok := ParseID(bad)
		assert.False(t, ok, bad)
	}
}

func TestTypesWithCategory(t *testing.T) {
	vs := TypesWithCategory(CategoryVitalSigns)
	assert.Contains(t, vs, "heart_rate")
	assert.Contains(t, vs, "blood_pressure_systolic")
	assert.NotContains(t, vs, "step_count")
	assert.Equal(t, []string{"blood_alcohol_content", "blood_glucose"}, TypesWithCategory(CategoryLaboratory))
	assert.Contains(t, TypesWithCategory(CategoryActivity), "step_count")
	assert.Equal(t, []string{"reported_energy", "reported_exertion", "reported_feeling", "reported_mood",
		"reported_muscle_soreness", "reported_pain", "reported_sleep_quality", "reported_stress"},
		TypesWithCategory(CategorySurvey))
	assert.Empty(t, TypesWithCategory("exam"))

	assert.Equal(t, []string{"resting_heart_rate"}, TypesWithExtraLOINC("8867-4"))
	assert.Equal(t, []string{"oxygen_saturation"}, TypesWithExtraLOINC("2708-6"))
	assert.Empty(t, TypesWithExtraLOINC(""))
}
