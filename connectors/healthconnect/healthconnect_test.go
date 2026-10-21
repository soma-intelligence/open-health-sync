// SPDX-License-Identifier: AGPL-3.0-only

package healthconnect

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const testUser = "00000000-0000-0000-0000-000000000001"

// lines reads testdata/upload.ndjson, keyed by metadata.id (recordId for the
// deletion, recordType for a line without either).
func lines(t *testing.T) map[string]map[string]any {
	t.Helper()
	f, err := os.Open("testdata/upload.ndjson")
	require.NoError(t, err)
	defer f.Close()
	out := map[string]map[string]any{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m))
		key, _ := m["recordId"].(string)
		if md, ok := m["metadata"].(map[string]any); ok {
			key, _ = md["id"].(string)
		}
		out[key] = m
	}
	require.NoError(t, sc.Err())
	return out
}

func mapOne(t *testing.T, fields map[string]any) model.MapResult {
	t.Helper()
	res, err := New().Map(connector.RawRecord{UserID: testUser, Provider: Slug, Fields: fields})
	require.NoError(t, err)
	return res
}

func TestParseUpload(t *testing.T) {
	c := New()
	f, err := os.Open("testdata/upload.ndjson")
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, c.ParseUpload(connector.UploadMeta{}, f, nil))

	for name, body := range map[string]string{
		"not json":       "{\"recordType\":\"Steps\"}\nnope\n",
		"array":          "[1,2]\n",
		"no record type": "{\"metadata\":{\"id\":\"x\"}}\n",
		"empty type":     "{\"recordType\":\"\"}\n",
	} {
		err := c.ParseUpload(connector.UploadMeta{}, strings.NewReader(body), nil)
		require.ErrorIs(t, err, ErrInvalidLine, name)
	}
	require.ErrorIs(t, c.ParseUpload(connector.UploadMeta{}, strings.NewReader("\n  \n"), nil), ErrNoLines)
}

func TestMapSteps(t *testing.T) {
	res := mapOne(t, lines(t)["st-1"])
	require.Len(t, res.QuantitySamples, 1)
	s := res.QuantitySamples[0]
	require.Equal(t, model.QTStepCount, s.QuantityType)
	require.Equal(t, 1234.0, s.Value)
	require.Equal(t, time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC), s.StartDate)
	require.Equal(t, time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC), s.EndDate)
	require.Equal(t, Slug, s.Provider)
	require.Equal(t, "google_fit", s.SourceSlug)
	require.Equal(t, "com.google.android.apps.fitness", s.SourceBundleID)
	require.Equal(t, "Google Pixel 9", s.DeviceID)
	require.Equal(t, "Etc/GMT-1", s.Metadata["timezone"])
	require.True(t, s.Replaceable)
	require.True(t, s.SupersedeMoved)
	require.Equal(t, "st-1", s.ExternalID)

	edited := lines(t)["st-1"]
	edited["count"] = 2000.0
	again := mapOne(t, edited)
	require.Equal(t, s.ContentHash, again.QuantitySamples[0].ContentHash, "an edit keeps the identity so it replaces the value")
}

func TestMapHeartRateSeries(t *testing.T) {
	res := mapOne(t, lines(t)["hr-1"])
	require.Len(t, res.QuantitySamples, 2)
	require.Equal(t, "hr-1_0", res.QuantitySamples[0].ExternalID)
	require.Equal(t, "hr-1_1", res.QuantitySamples[1].ExternalID)
	require.Equal(t, 63.0, res.QuantitySamples[1].Value)
	require.Equal(t, "com_samsung_android_wear", res.QuantitySamples[0].SourceSlug, "an unlisted app keeps its own slug")
	require.False(t, res.QuantitySamples[0].Replaceable)
	require.NotEqual(t, res.QuantitySamples[0].ContentHash, res.QuantitySamples[1].ContentHash)
}

func TestMapBloodGlucose(t *testing.T) {
	res := mapOne(t, lines(t)["bg-1"])
	require.Len(t, res.QuantitySamples, 1)
	s := res.QuantitySamples[0]
	require.Equal(t, "blood_glucose", s.QuantityType)
	require.Equal(t, 5.6, s.Value)
	require.Equal(t, "mmol/L", s.Unit, "the pipeline converts to mg/dL")
	require.Equal(t, "freestyle_libre", s.SourceSlug)
	require.Equal(t, "interstitial_fluid", s.Metadata[MetadataSpecimen])
	require.Equal(t, "fasting", s.Metadata["relation_to_meal"])
	require.NotContains(t, s.Metadata, "meal_type", "unknown is not stored")

	v, unit, err := model.ToStorageUnit(s.QuantityType, s.Value, s.Unit)
	require.NoError(t, err)
	require.Equal(t, "mg/dL", unit)
	require.InDelta(t, 100.9, v, 0.1)
}

func TestMapBloodPressurePairs(t *testing.T) {
	res := mapOne(t, lines(t)["bp-1"])
	require.Len(t, res.QuantitySamples, 2)
	sys, dia := res.QuantitySamples[0], res.QuantitySamples[1]
	require.Equal(t, "blood_pressure_systolic", sys.QuantityType)
	require.Equal(t, 121.0, sys.Value)
	require.Equal(t, "blood_pressure_diastolic", dia.QuantityType)
	require.Equal(t, 79.0, dia.Value)
	// The read API pairs the two halves on start, end, source and device.
	require.Equal(t, sys.StartDate, dia.StartDate)
	require.Equal(t, sys.EndDate, dia.EndDate)
	require.Equal(t, sys.SourceSlug, dia.SourceSlug)
	require.Equal(t, sys.DeviceID, dia.DeviceID)
	require.Equal(t, "omron", sys.SourceSlug)
	require.Equal(t, "sitting_down", sys.Metadata["body_position"])
	require.Equal(t, "bp-1_systolic", sys.ExternalID)

	bad := lines(t)["bp-1"]
	delete(bad, "diastolicMillimetersOfMercury")
	_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: bad})
	require.Error(t, err)
}

func TestMapBodyAndManualEntry(t *testing.T) {
	w := mapOne(t, lines(t)["w-1"]).QuantitySamples[0]
	require.Equal(t, "body_mass", w.QuantityType)
	require.Equal(t, "kg", w.Unit)
	require.NotNil(t, w.WasUserEntered)
	require.True(t, *w.WasUserEntered)
	require.Equal(t, "withings", w.SourceSlug)

	h := mapOne(t, lines(t)["h-1"]).QuantitySamples[0]
	v, unit, err := model.ToStorageUnit(h.QuantityType, h.Value, h.Unit)
	require.NoError(t, err)
	require.Equal(t, "cm", unit)
	require.InDelta(t, 181, v, 0.001)
}

func TestMapSleep(t *testing.T) {
	res := mapOne(t, lines(t)["sl-1"])
	require.Len(t, res.SleepSessions, 1)
	s := res.SleepSessions[0]
	require.Equal(t, "sl-1", s.ExternalID)
	require.True(t, s.Replaceable)
	require.Equal(t, 3*3600, *s.LightSleepSeconds)
	require.Equal(t, 90*60, *s.DeepSleepSeconds)
	require.Equal(t, 90*60, *s.REMSleepSeconds)
	require.Equal(t, 30*60, *s.AwakeSeconds)
	require.Equal(t, 6*3600, *s.TotalSleepSeconds)
	require.Len(t, res.SleepPhases, 4, "out_of_bed is not a phase")
	for _, p := range res.SleepPhases {
		require.Equal(t, s.ContentHash, p.ParentContentHash)
	}
}

func TestMapExercise(t *testing.T) {
	res := mapOne(t, lines(t)["ex-1"])
	require.Len(t, res.Workouts, 1)
	w := res.Workouts[0]
	require.Equal(t, "cycling", w.ActivityType)
	require.Equal(t, 45*60, *w.DurationS)
	require.Equal(t, "Evening ride", w.Metadata["title"])

	yoga := lines(t)["ex-1"]
	yoga["exerciseType"] = "yoga"
	require.Equal(t, "yoga", mapOne(t, yoga).Workouts[0].ActivityType)
}

// Menstrual flow and intermenstrual bleeding land in the canonical category
// types Apple Health uses, with the same labels and value numbers.
func TestMapMenstruation(t *testing.T) {
	res := mapOne(t, lines(t)["mf-1"])
	require.Len(t, res.CategorySamples, 1)
	require.Empty(t, res.QuantitySamples)
	s := res.CategorySamples[0]
	require.Equal(t, "menstrual_flow", s.CategoryType)
	require.Equal(t, "light", s.ValueLabel)
	require.Equal(t, 2, s.ValueInt)
	require.Equal(t, Slug, s.Provider)
	require.Equal(t, "mf-1", s.ExternalID)
	require.Equal(t, "com.example.cycles", s.SourceName)
	require.Equal(t, time.Date(2026, 3, 1, 23, 30, 0, 0, time.UTC), s.StartDate)
	require.Equal(t, s.StartDate, s.EndDate)
	require.Equal(t, "Etc/GMT-1", s.Metadata["timezone"], "the local day is 2 March")
	require.Equal(t, "manual", s.Metadata["recording_method"])

	edited := lines(t)["mf-1"]
	edited["flow"] = "heavy"
	require.Equal(t, s.ContentHash, mapOne(t, edited).CategorySamples[0].ContentHash, "hashed on the record id and time")

	u := mapOne(t, lines(t)["mf-2"]).CategorySamples[0]
	require.Equal(t, "unspecified", u.ValueLabel, "flow of unknown amount")
	require.Equal(t, 1, u.ValueInt)
	require.NotContains(t, u.Metadata, "timezone")
	for flow, want := range map[string]string{"": "unspecified", "medium": "medium", "heavy": "heavy", "odd": "unspecified"} {
		l := lines(t)["mf-2"]
		l["flow"] = flow
		require.Equal(t, want, mapOne(t, l).CategorySamples[0].ValueLabel, flow)
	}

	b := mapOne(t, lines(t)["ib-1"]).CategorySamples
	require.Len(t, b, 1)
	require.Equal(t, "intermenstrual_bleeding", b[0].CategoryType)
	require.Empty(t, b[0].ValueLabel)
	require.NotEqual(t, s.ContentHash, b[0].ContentHash)

	bad := lines(t)["mf-1"]
	bad["time"] = "soon"
	_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: bad})
	require.Error(t, err)
}

func TestMapDeletionAndUnknown(t *testing.T) {
	res := mapOne(t, lines(t)["st-0"])
	require.Equal(t, []model.Deletion{{ExternalID: "st-0"}}, res.Deletions)

	res = mapOne(t, lines(t)["vo-1"])
	require.Equal(t, model.MapResult{}, res)

	_, err := New().Map(connector.RawRecord{Fields: map[string]any{"recordType": "Deletion"}})
	require.Error(t, err)
	_, err = New().Map(connector.RawRecord{Fields: map[string]any{"recordType": "Steps", "count": 1}})
	require.ErrorContains(t, err, "metadata.id")
	_, err = New().Map(connector.RawRecord{Fields: map[string]any{"recordType": "Steps", "metadata": map[string]any{"id": "x"},
		"startTime": "2026-03-02T09:00:00Z", "endTime": "2026-03-02T08:00:00Z", "count": 1}})
	require.ErrorContains(t, err, "before")
}

func TestMapNutrition(t *testing.T) {
	res := mapOne(t, lines(t)["nu-1"])
	require.Empty(t, res.QuantitySamples)
	require.Len(t, res.NutritionLogs, 1)
	nl := res.NutritionLogs[0]
	require.Equal(t, Slug, nl.Provider)
	require.Equal(t, "nu-1", nl.ExternalID)
	require.Equal(t, time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC), nl.StartDate)
	require.Equal(t, time.Date(2026, 3, 2, 12, 30, 0, 0, time.UTC), *nl.EndDate)
	require.Equal(t, "lunch", nl.MealType)
	require.Equal(t, "Lentil soup", nl.Metadata["food_name"])
	require.Equal(t, "com.example.food", nl.Metadata["data_origin"])
	require.Equal(t, "Etc/GMT-1", nl.Metadata["timezone"])
	require.True(t, nl.Replaceable)
	require.True(t, nl.SupersedeMoved)

	// The seven columns, in their units: grams, sodium in milligrams.
	for name, got := range map[string]*float64{"energy": nl.EnergyKcal, "protein": nl.ProteinG, "carbs": nl.CarbsG,
		"fat": nl.FatG, "fiber": nl.FiberG, "sugar": nl.SugarG, "sodium": nl.SodiumMg} {
		require.NotNil(t, got, name)
	}
	require.Equal(t, 420.0, *nl.EnergyKcal)
	require.Equal(t, 24.0, *nl.ProteinG)
	require.Equal(t, 55.0, *nl.CarbsG)
	require.Equal(t, 10.0, *nl.FatG)
	require.Equal(t, 16.0, *nl.FiberG)
	require.Equal(t, 6.0, *nl.SugarG)
	require.InDelta(t, 800, *nl.SodiumMg, 1e-9)
	// The others, each in its unit (model.Nutrients).
	require.InDelta(t, 90, nl.NutrientsJSON["energy_from_fat"], 1e-9)
	require.InDelta(t, 1.5, nl.NutrientsJSON["fat_saturated"], 1e-9)
	require.InDelta(t, 6.6, nl.NutrientsJSON["iron"], 1e-9, "mg")
	require.InDelta(t, 12, nl.NutrientsJSON["vitamin_c"], 1e-9, "mg")
	require.InDelta(t, 2, nl.NutrientsJSON["vitamin_d"], 1e-9, "mcg")
	require.Equal(t, 0.0, nl.NutrientsJSON["caffeine"], "a zero is kept")
	require.NotContains(t, nl.NutrientsJSON, "zinc", "a figure left out gives nothing")

	edited := lines(t)["nu-1"]
	edited["proteinGrams"] = 30.0
	again := mapOne(t, edited).NutritionLogs[0]
	require.Equal(t, nl.ContentHash, again.ContentHash, "an edit keeps the identity so it replaces the figures")
	require.Equal(t, 30.0, *again.ProteinG)

	for meal, want := range map[string]string{"breakfast": "breakfast", "snack": "snack", "unknown": "", "": "", "brunch": ""} {
		l := lines(t)["nu-1"]
		l["mealType"] = meal
		require.Equal(t, want, mapOne(t, l).NutritionLogs[0].MealType, meal)
	}

	bare := map[string]any{"recordType": "Nutrition", "metadata": map[string]any{"id": "nu-2"},
		"startTime": "2026-03-02T19:00:00Z", "endTime": "2026-03-02T19:10:00Z", "name": "Dinner"}
	logs := mapOne(t, bare).NutritionLogs
	require.Len(t, logs, 1, "a meal logged with no figure is kept")
	require.Nil(t, logs[0].EnergyKcal)
	require.Empty(t, logs[0].NutrientsJSON)

	bad := lines(t)["nu-1"]
	bad["endTime"] = "2026-03-02T11:00:00Z"
	_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: bad})
	require.ErrorContains(t, err, "before")
}

func TestMapHydration(t *testing.T) {
	res := mapOne(t, lines(t)["hy-1"])
	require.Len(t, res.NutritionLogs, 1)
	nl := res.NutritionLogs[0]
	require.Equal(t, map[string]any{"water": 250.0}, nl.NutrientsJSON, "litres to millilitres")
	require.Nil(t, nl.EnergyKcal)
	require.Empty(t, nl.MealType)
	require.Equal(t, "hy-1", nl.ExternalID)
	require.NotEqual(t, mapOne(t, lines(t)["nu-1"]).NutritionLogs[0].ContentHash, nl.ContentHash)

	bad := lines(t)["hy-1"]
	delete(bad, "volumeLiters")
	_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: bad})
	require.ErrorContains(t, err, "no value")
}

func TestMapEnergy(t *testing.T) {
	tc := mapOne(t, lines(t)["tc-1"]).QuantitySamples
	require.Len(t, tc, 1)
	require.Equal(t, model.QTTotalEnergyBurned, tc[0].QuantityType)
	require.Equal(t, 85.5, tc[0].Value)
	require.Equal(t, "kcal", tc[0].Unit)
	require.Equal(t, "samsung_health", tc[0].SourceSlug)
	require.Equal(t, time.Date(2026, 3, 2, 1, 0, 0, 0, time.UTC), tc[0].EndDate)

	bm := mapOne(t, lines(t)["bm-1"]).QuantitySamples
	require.Len(t, bm, 1)
	require.Equal(t, model.QTBasalMetabolicRate, bm[0].QuantityType)
	require.Equal(t, 1650.0, bm[0].Value)
	require.Equal(t, bm[0].StartDate, bm[0].EndDate, "an instant")
	v, unit, err := model.ToStorageUnit(bm[0].QuantityType, bm[0].Value, bm[0].Unit)
	require.NoError(t, err)
	require.Equal(t, "kcal/d", unit)
	require.Equal(t, 1650.0, v)
}

func TestSourceSlug(t *testing.T) {
	require.Equal(t, "dexcom", SourceSlug("com.dexcom.g7"))
	require.Equal(t, "xiaomi", SourceSlug("com.xiaomi.wearable"), "Mi Fitness")
	require.Equal(t, "xiaomi", SourceSlug("com.mi.health"), "Mi Health")
	require.Equal(t, "xiaomi", SourceSlug("com.xiaomi.hm.health"), "Zepp Life")
	require.Equal(t, "zepp", SourceSlug("com.huami.watch.hmwatchmanager"), "Zepp, for Amazfit")
	require.Equal(t, "ringconn", SourceSlug("com.gdjztech.ringconn"))
	require.Equal(t, "health_connect", SourceSlug(""))
	require.Equal(t, "com_example_app", SourceSlug("com.example.app"))
}

func TestParseOffset(t *testing.T) {
	for in, want := range map[string]int{"Z": 0, "+02:00": 120, "-05:30": -330} {
		got, ok := parseOffset(in)
		require.True(t, ok, in)
		require.Equal(t, want, got)
	}
	_, ok := parseOffset("Europe/Paris")
	require.False(t, ok)
}
