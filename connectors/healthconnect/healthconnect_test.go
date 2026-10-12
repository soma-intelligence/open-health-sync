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

func TestMapDeletionAndUnknown(t *testing.T) {
	res := mapOne(t, lines(t)["st-0"])
	require.Equal(t, []model.Deletion{{ExternalID: "st-0"}}, res.Deletions)

	res = mapOne(t, lines(t)["mf-1"])
	require.Empty(t, res.QuantitySamples)

	_, err := New().Map(connector.RawRecord{Fields: map[string]any{"recordType": "Deletion"}})
	require.Error(t, err)
	_, err = New().Map(connector.RawRecord{Fields: map[string]any{"recordType": "Steps", "count": 1}})
	require.ErrorContains(t, err, "metadata.id")
	_, err = New().Map(connector.RawRecord{Fields: map[string]any{"recordType": "Steps", "metadata": map[string]any{"id": "x"},
		"startTime": "2026-03-02T09:00:00Z", "endTime": "2026-03-02T08:00:00Z", "count": 1}})
	require.ErrorContains(t, err, "before")
}

func TestSourceSlug(t *testing.T) {
	require.Equal(t, "dexcom", SourceSlug("com.dexcom.g7"))
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
