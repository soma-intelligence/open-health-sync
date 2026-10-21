// SPDX-License-Identifier: AGPL-3.0-only

package fhir

import (
	"bytes"
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata")

var (
	testUser = uuid.MustParse("3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11")
	t0       = time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	ingested = time.Date(2026, 3, 1, 9, 15, 30, 250000000, time.UTC)
)

func ptr[T any](v T) *T { return &v }

// sample is an invented quantity sample with the catalog codes of its type,
// as seeded in internal/db/migrations.
func sample(id int64, qt string, value float64, unit string, loinc, ucum string) read.CodedSample {
	s := read.CodedSample{Sample: read.Sample{
		ID: id, Type: qt, Value: value, Unit: ptr(unit), StartDate: t0, EndDate: ptr(t0),
		Provenance: read.Provenance{Provider: "apple_health", Source: ptr("apple"), Device: ptr("apple_watch"), IngestedAt: ingested},
	}}
	if loinc != "" {
		s.LOINCCode = ptr(loinc)
	}
	if ucum != "" {
		s.UCUMUnit = ptr(ucum)
	}
	s.StorageUnit = ptr(unit)
	return s
}

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')
	path := filepath.Join("testdata", name+".json")
	if *update {
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test ./internal/fhir -update to write it")
	assert.Equal(t, string(want), string(got), "%s differs; check the change, then run go test ./internal/fhir -update", path)
}

// The golden cases: one per mapping category. The HL7 validator checks the
// same files (scripts/fhir-validate.sh).
func goldenCases() map[string]read.CodedSample {
	period := sample(2002, "step_count", 1234, "count", "55423-8", "{steps}")
	period.EndDate = ptr(t0.Add(time.Hour))
	period.ExternalID = ptr("8d7c6b5a-0000-4000-8000-00000000c0de")
	period.Source = nil

	glucose := sample(3003, "blood_glucose", 97, "mg/dL", "2339-0", "mg/dL")
	glucose.Provider, glucose.Source, glucose.Device, glucose.DeviceModel = "withings", nil, nil, ptr("Invented Meter 1")

	// A CGM reading (Dexcom): interstitial fluid, not blood.
	cgm := sample(3004, "blood_glucose", 112, "mg/dL", "2339-0", "mg/dL")
	cgm.Provider, cgm.Source, cgm.Device, cgm.DeviceModel = "dexcom", ptr("dexcom"), nil, ptr("g7")
	cgm.Metadata = json.RawMessage(`{"specimen":"interstitial_fluid","trend":"flat"}`)

	noLOINC := sample(4004, "body_fat_mass", 18.4, "kg", "", "kg")

	unknown := sample(5005, "undeclared_metric", 7.5, "furlongs", "", "")
	unknown.LOINCCode, unknown.UCUMUnit, unknown.StorageUnit = nil, nil, nil
	unknown.Device = nil

	nan := sample(6006, "hrv_sdnn", math.NaN(), "ms", "80404-7", "ms")
	nan.EndDate = nil

	// Withings' AFib classification of an ECG: a class, not an amount.
	afib := sample(7007, "ecg_afib_classification", 0, "classification", "", "")
	afib.Provider, afib.Source = "withings", ptr("withings")

	// Self reports: a rating the person gave, with and without a LOINC code.
	pain := sample(8008, "reported_pain", 3, "score", "72514-3", "{score}")
	pain.Provider, pain.Source, pain.Device = "self_report", ptr("self_report"), nil
	pain.ExternalID = ptr("0192f1a2-0000-7000-8000-00000000beef")
	feeling := sample(8009, "reported_feeling", 4, "score", "", "{score}")
	feeling.Provider, feeling.Source, feeling.Device = "self_report", ptr("self_report"), nil

	// The day's minutes at an intensity (Oura, Withings): Physical Activity
	// IG measures, over the provider's local day.
	dayStart := time.Date(2026, 2, 28, 23, 0, 0, 0, time.UTC)
	moderate := sample(9010, "moderate_activity_time", 42, "min", "77592-4", "min")
	moderate.Provider, moderate.Source, moderate.Device = "oura", ptr("oura"), nil
	moderate.StartDate, moderate.EndDate = dayStart, ptr(dayStart.Add(24*time.Hour-time.Second))
	moderate.ExternalID = ptr("2026-03-01_moderate_activity_time")
	vigorous := sample(9011, "vigorous_activity_time", 15, "min", "77593-2", "min")
	vigorous.Provider, vigorous.Source, vigorous.Device = "withings", ptr("withings"), ptr("fake-watch-0001")
	vigorous.StartDate, vigorous.EndDate = dayStart, ptr(dayStart.Add(24*time.Hour-time.Second))
	vigorous.ExternalID = ptr("2026-03-01_vigorous_activity_time_fake-watch-0001")

	return map[string]read.CodedSample{
		"observation-activity-moderate-time":    moderate,
		"observation-activity-vigorous-time":    vigorous,
		"observation-survey-reported-pain":      pain,
		"observation-survey-reported-feeling":   feeling,
		"observation-ecg-afib-classification":   afib,
		"observation-vital-heart-rate":          sample(1001, "heart_rate", 61, "bpm", "8867-4", "/min"),
		"observation-vital-oxygen-saturation":   sample(1002, "oxygen_saturation", 97, "percent", "59408-5", "%"),
		"observation-vital-blood-pressure-sys":  sample(1003, "blood_pressure_systolic", 118, "mmHg", "8480-6", "mm[Hg]"),
		"observation-vital-resting-heart-rate":  sample(1004, "resting_heart_rate", 52, "bpm", "40443-4", "/min"),
		"observation-activity-period":           period,
		"observation-laboratory-blood-glucose":  glucose,
		"observation-laboratory-cgm-glucose":    cgm,
		"observation-no-loinc-no-category":      noLOINC,
		"observation-unknown-type-unknown-unit": unknown,
		"observation-value-not-a-number":        nan,
	}
}

func TestGoldenObservations(t *testing.T) {
	for name, s := range goldenCases() {
		t.Run(name, func(t *testing.T) {
			golden(t, name, FromQuantitySample(testUser, s))
		})
	}
}

// TestGoldenSearchSet is a page of every profiled vital sign, with a next link.
func TestGoldenSearchSet(t *testing.T) {
	vitals := []read.CodedSample{
		sample(101, "heart_rate", 61, "bpm", "8867-4", "/min"),
		sample(102, "respiratory_rate", 14, "bpm", "9279-1", "/min"),
		sample(103, "oxygen_saturation", 97, "percent", "59408-5", "%"),
		sample(104, "body_temperature", 36.8, "celsius", "8310-5", "Cel"),
		sample(105, "body_mass", 72.5, "kg", "29463-7", "kg"),
		sample(106, "height", 178, "cm", "8302-2", "cm"),
		sample(107, "body_mass_index", 22.9, "kg/m2", "39156-5", "kg/m2"),
		sample(108, "blood_pressure_systolic", 118, "mmHg", "8480-6", "mm[Hg]"),
		sample(109, "blood_pressure_diastolic", 76, "mmHg", "8462-4", "mm[Hg]"),
	}
	var obs []Observation
	for _, s := range vitals {
		obs = append(obs, FromQuantitySample(testUser, s))
	}
	// The default OHS_PUBLIC_URL: the validator refuses example.org links.
	base := "http://localhost:8080/v1/users/" + testUser.String() + "/fhir"
	b := SearchSet(base, base+"/Observation?limit=9", base+"/Observation?limit=9&cursor=abc", obs)
	golden(t, "bundle-searchset-vital-signs", b)

	empty := SearchSet(base, base+"/Observation", "", nil)
	golden(t, "bundle-searchset-empty", empty)
}

// FHIR JSON has no empty arrays, objects or strings, and no nulls; the JSON
// schema does not check it, so this walks every golden file.
func TestGoldenFilesHaveNoEmptyValues(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case nil:
			t.Errorf("%s is null", path)
		case string:
			assert.NotEmpty(t, x, path)
		case []any:
			assert.NotEmpty(t, x, path)
			for i, e := range x {
				walk(path+"["+strconv.Itoa(i)+"]", e)
			}
		case map[string]any:
			assert.NotEmpty(t, x, path)
			for k, e := range x {
				walk(path+"."+k, e)
			}
		}
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		var v any
		require.NoError(t, json.Unmarshal(raw, &v), f)
		walk(filepath.Base(f), v)
	}
}

func TestVitalSignProfiles(t *testing.T) {
	o := FromQuantitySample(testUser, sample(1, "heart_rate", 61, "bpm", "8867-4", "/min"))
	assert.Equal(t, []string{profileBase + "vitalsigns", profileBase + "heartrate"}, o.Meta.Profile)
	assert.Equal(t, CategoryVitalSigns, o.Category[0].Coding[0].Code)

	// A catalog that codes the type differently claims no profile.
	o = FromQuantitySample(testUser, sample(1, "heart_rate", 61, "bpm", "0000-0", "/min"))
	assert.Empty(t, o.Meta.Profile)
	assert.Equal(t, CategoryVitalSigns, o.Category[0].Coding[0].Code)

	// A unit the profile does not accept claims no profile either.
	s := sample(1, "body_mass", 72.5, "stone", "29463-7", "kg")
	s.StorageUnit = ptr("kg")
	o = FromQuantitySample(testUser, s)
	assert.Empty(t, o.Meta.Profile)
	assert.Empty(t, o.ValueQuantity.Code, "no UCUM code for an unknown unit")

	// Resting heart rate also carries 8867-4 and meets the heart rate profile.
	o = FromQuantitySample(testUser, sample(1, "resting_heart_rate", 52, "bpm", "40443-4", "/min"))
	assert.Equal(t, []string{"8867-4", "40443-4", "resting_heart_rate"},
		[]string{o.Code.Coding[0].Code, o.Code.Coding[1].Code, o.Code.Coding[2].Code})
	assert.Equal(t, []string{profileBase + "vitalsigns", profileBase + "heartrate"}, o.Meta.Profile)

	// Systolic pressure alone: R4 allows 8480-6 only inside the panel.
	o = FromQuantitySample(testUser, sample(1, "blood_pressure_systolic", 118, "mmHg", "8480-6", "mm[Hg]"))
	assert.Equal(t, []Coding{{System: QuantityTypeSystem, Code: "blood_pressure_systolic"}}, o.Code.Coding)
	assert.Empty(t, o.Meta.Profile)
	assert.Equal(t, CategoryVitalSigns, o.Category[0].Coding[0].Code)
	assert.True(t, ComponentOnlyLOINC("8480-6"))
	assert.True(t, ComponentOnlyLOINC("8462-4"))
	assert.False(t, ComponentOnlyLOINC("8867-4"))

	// SpO2 carries the code the R4 profile requires, then the catalog's.
	o = FromQuantitySample(testUser, sample(1, "oxygen_saturation", 97, "percent", "59408-5", "%"))
	assert.Equal(t, "2708-6", o.Code.Coding[0].Code)
	assert.Equal(t, "59408-5", o.Code.Coding[1].Code)
	assert.Equal(t, QuantityTypeSystem, o.Code.Coding[2].System)
}

func TestEffective(t *testing.T) {
	s := sample(1, "step_count", 10, "count", "", "")
	s.EndDate = nil
	o := FromQuantitySample(testUser, s)
	assert.Equal(t, "2026-03-01T08:00:00Z", o.EffectiveDateTime)
	assert.Nil(t, o.EffectivePeriod)

	s.EndDate = ptr(t0.Add(-time.Minute)) // before start: not a valid Period
	o = FromQuantitySample(testUser, s)
	assert.Equal(t, "2026-03-01T08:00:00Z", o.EffectiveDateTime)

	s.EndDate = ptr(t0.Add(30 * time.Second))
	o = FromQuantitySample(testUser, s)
	assert.Empty(t, o.EffectiveDateTime)
	assert.Equal(t, &Period{Start: "2026-03-01T08:00:00Z", End: "2026-03-01T08:00:30Z"}, o.EffectivePeriod)
}

func TestInfiniteValues(t *testing.T) {
	for v, code := range map[float64]string{math.Inf(1): "positive-infinity", math.Inf(-1): "negative-infinity"} {
		o := FromQuantitySample(testUser, sample(1, "heart_rate", v, "bpm", "8867-4", "/min"))
		assert.Nil(t, o.ValueQuantity)
		assert.Equal(t, code, o.DataAbsentReason.Coding[0].Code)
		_, err := json.Marshal(o)
		assert.NoError(t, err)
	}
}

func TestZeroValueIsKept(t *testing.T) {
	o := FromQuantitySample(testUser, sample(1, "step_count", 0, "count", "55423-8", "{steps}"))
	raw, err := json.Marshal(o)
	require.NoError(t, err)
	assert.True(t, bytes.Contains(raw, []byte(`"value":0`)), "%s", raw)
}

func TestObservationID(t *testing.T) {
	assert.Equal(t, "qs-42", ObservationID(42))
	id, ok := ParseObservationID("qs-42")
	assert.True(t, ok)
	assert.Equal(t, int64(42), id)
	for _, bad := range []string{"", "42", "qs-", "qs-x", "qs--1", "qs-+1", "qs-0", "ws-1", "qs-99999999999999999999"} {
		_, ok := ParseObservationID(bad)
		assert.False(t, ok, bad)
	}
}

// The tables name declared quantity types only, and every storage unit has a
// UCUM code, so a renamed type or a new unit fails here first.
func TestTablesMatchTheModel(t *testing.T) {
	units := model.StorageUnits()
	var names []string
	for qt := range vitalSigns {
		names = append(names, qt)
	}
	for qt := range laboratory {
		names = append(names, qt)
	}
	for qt := range activity {
		names = append(names, qt)
	}
	for _, qt := range names {
		_, ok := units[qt]
		assert.True(t, ok, "%s is not a declared quantity type", qt)
	}
	for qt, u := range units {
		assert.NotEmpty(t, ucumUnits[u], "storage unit %q of %s has no UCUM code", u, qt)
	}
	for qt, vs := range vitalSigns {
		if vs.profile != "" {
			assert.True(t, slices.Contains(vs.units, ucumUnits[units[qt]]), "%s: storage unit not allowed by its profile", qt)
		}
	}
}
