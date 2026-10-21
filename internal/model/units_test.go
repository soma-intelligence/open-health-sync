// SPDX-License-Identifier: AGPL-3.0-only

package model

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToStorageUnit_AliasRenamesWithoutTouchingValue(t *testing.T) {
	// whoop wrote heart_rate as "count/min" while oura and withings wrote "bpm".
	// Same physical quantity, so only the spelling changes.
	v, u, err := ToStorageUnit(QTHeartRate, 62, "count/min")
	require.NoError(t, err)
	assert.Equal(t, 62.0, v)
	assert.Equal(t, "bpm", u)
}

func TestToStorageUnit_CaseInsensitive(t *testing.T) {
	v, u, err := ToStorageUnit("body_mass", 80, "KG")
	require.NoError(t, err)
	assert.Equal(t, 80.0, v)
	assert.Equal(t, "kg", u)
}

func TestToStorageUnit_ScalarConversion(t *testing.T) {
	v, u, err := ToStorageUnit("body_mass", 165, "lb")
	require.NoError(t, err)
	assert.InDelta(t, 74.8427, v, 0.001)
	assert.Equal(t, "kg", u)
}

func TestToStorageUnit_Speed(t *testing.T) {
	// Apple Health spells walking and running speed "km/hr": 3.6 km/hr is 1 m/s.
	v, u, err := ToStorageUnit("walking_speed", 3.6, "km/hr")
	require.NoError(t, err)
	assert.InDelta(t, 1.0, v, 1e-9)
	assert.Equal(t, "m/s", u)
	v, _, err = ToStorageUnit("running_speed", 10, "mph")
	require.NoError(t, err)
	assert.InDelta(t, 4.4704, v, 1e-9)
}

func TestToStorageUnit_AffineConversion(t *testing.T) {
	// Temperature is not a pure scaling: 98.6 F is 37 C.
	v, u, err := ToStorageUnit(QTBodyTemperature, 98.6, "degF")
	require.NoError(t, err)
	assert.InDelta(t, 37.0, v, 0.0001)
	assert.Equal(t, "celsius", u)
}

func TestToStorageUnit_MetricKeyedConversion_Height(t *testing.T) {
	// withings meastype 4 reports height in metres; storage is centimetres. The
	// reverse rule (cm -> m) holds for distance, which is why this cannot be a
	// qt-agnostic entry.
	v, u, err := ToStorageUnit("height", 1.83, "m")
	require.NoError(t, err)
	assert.InDelta(t, 183.0, v, 0.0001)
	assert.Equal(t, "cm", u)

	dv, du, err := ToStorageUnit(QTDistanceWalkingRunning, 250000, "cm")
	require.NoError(t, err)
	assert.InDelta(t, 2500.0, dv, 0.0001)
	assert.Equal(t, "m", du)
}

func TestToStorageUnit_MolarConversionIsMetricScoped(t *testing.T) {
	// mmol/L to mg/dL depends on the analyte's molar mass, so it is never a global
	// rule: 5.5 mmol/L glucose is ~99 mg/dL.
	v, u, err := ToStorageUnit("blood_glucose", 5.5, "mmol/L")
	require.NoError(t, err)
	assert.InDelta(t, 99.1, v, 0.1)
	assert.Equal(t, "mg/dL", u)

	// The same unit on a different metric has no declared meaning and must fail
	// rather than silently borrow glucose's factor.
	_, _, err = ToStorageUnit("body_mass", 5.5, "mmol/L")
	require.Error(t, err)
}

func TestToStorageUnit_KilopascalIsBloodPressureOnly(t *testing.T) {
	for _, qt := range []string{"blood_pressure_systolic", "blood_pressure_diastolic"} {
		v, u, err := ToStorageUnit(qt, 16, "kPa")
		require.NoError(t, err)
		assert.InDelta(t, 120.0, v, 0.01)
		assert.Equal(t, "mmHg", u)
	}
	_, _, err := ToStorageUnit("body_mass", 16, "kPa")
	require.Error(t, err)
}

func TestToStorageUnit_FractionalSaturation(t *testing.T) {
	v, u, err := ToStorageUnit(QTOxygenSaturation, 0.97, "fraction")
	require.NoError(t, err)
	assert.InDelta(t, 97.0, v, 0.0001)
	assert.Equal(t, "percent", u)
}

// TestToStorageUnit_ApplePercentIsAFraction pins the end of the mixed-scale bug:
// HealthKit reports these two through HKUnit.percent(), a 0..1 fraction that
// spells itself "%", and the apple_health mapper now hands them over as
// "fraction" so the choke point can rescale them. An oura row carrying the same
// 97 with a real "%" must come out unchanged, which is what makes the two
// providers finally agree on one scale.
func TestToStorageUnit_ApplePercentIsAFraction(t *testing.T) {
	for _, qt := range []string{QTOxygenSaturation, "body_fat_percentage"} {
		v, u, err := ToStorageUnit(qt, 0.223, "fraction")
		require.NoErrorf(t, err, "%s", qt)
		assert.InDeltaf(t, 22.3, v, 0.0001, "%s", qt)
		assert.Equalf(t, "percent", u, "%s", qt)

		v, u, err = ToStorageUnit(qt, 22.3, "%")
		require.NoErrorf(t, err, "%s", qt)
		assert.InDeltaf(t, 22.3, v, 0.0001, "%s", qt)
		assert.Equalf(t, "percent", u, "%s", qt)
	}
}

// TestToStorageUnit_AppleRingTimesAreMinutes pins the other half of the same
// class of bug: the Apple Health client's unit map lists "ms" before "min", so every ring
// time arrived in milliseconds against readers that treat it as minutes.
func TestToStorageUnit_AppleRingTimesAreMinutes(t *testing.T) {
	for _, qt := range []string{"apple_exercise_time", "apple_move_time", "apple_stand_time"} {
		v, u, err := ToStorageUnit(qt, 300000, "ms")
		require.NoErrorf(t, err, "%s", qt)
		assert.InDeltaf(t, 5.0, v, 0.0001, "%s", qt)
		assert.Equalf(t, "min", u, "%s", qt)

		// Already-minutes rows must not be rescaled, so a replay after the
		// backfill is a no-op rather than a second division.
		v, u, err = ToStorageUnit(qt, 5, "min")
		require.NoErrorf(t, err, "%s", qt)
		assert.InDeltaf(t, 5.0, v, 0.0001, "%s", qt)
		assert.Equalf(t, "min", u, "%s", qt)
	}

	// ms stays ms where it is the storage unit: the conversion is metric-scoped
	// for exactly this reason.
	v, u, err := ToStorageUnit(QTHRVSDNN, 42, "ms")
	require.NoError(t, err)
	assert.InDelta(t, 42.0, v, 0.0001)
	assert.Equal(t, "ms", u)
}

func TestToStorageUnit_UndeclaredTypePassesThrough(t *testing.T) {
	// The long-tail promise: an undeclared metric still lands, unconverted, rather
	// than failing every record of a type nobody has curated yet.
	v, u, err := ToStorageUnit("some_new_provider_metric", 12, "widgets")
	require.NoError(t, err)
	assert.Equal(t, 12.0, v)
	assert.Equal(t, "widgets", u)
}

func TestToStorageUnit_EmptyUnitAssumesStorageUnit(t *testing.T) {
	v, u, err := ToStorageUnit("blood_glucose", 100, "")
	require.NoError(t, err)
	assert.Equal(t, 100.0, v)
	assert.Equal(t, "mg/dL", u)
}

func TestToStorageUnit_UnknownUnitIsAnError(t *testing.T) {
	v, u, err := ToStorageUnit(QTHeartRate, 62, "furlongs/fortnight")
	require.Error(t, err)
	// Value and unit are returned untouched so the caller can record the raw sample.
	assert.Equal(t, 62.0, v)
	assert.Equal(t, "furlongs/fortnight", u)
}

func TestToStorageUnit_StrainSentAsScoreIsItsIndex(t *testing.T) {
	// Whoop (workout and cycle strain) and some aggregators (strain_level) both send strain
	// as "score", while its storage unit is "index": the same dimensionless number
	// under two names. Unconverted, it was a unit error, and a unit error parks the
	// WHOLE record, so the Whoop workout itself or the entire aggregated day went with it.
	for _, qt := range []string{QTStrain, QTWorkoutStrain} {
		v, u, err := ToStorageUnit(qt, 14.2, "score")
		require.NoError(t, err, qt)
		assert.Equal(t, 14.2, v, qt)
		assert.Equal(t, "index", u, qt)
	}
}

func TestToStorageUnit_WithingsVisceralFatCountIsItsIndex(t *testing.T) {
	// The Withings feed sends visceral fat as "count" and it is stored as "index":
	// one dimensionless rating under two names. Unconverted, every reading was a
	// unit error that parked the record, so no Withings visceral fat was ever stored.
	v, u, err := ToStorageUnit("visceral_fat_rating", 12, "count")
	require.NoError(t, err)
	assert.Equal(t, 12.0, v)
	assert.Equal(t, "index", u)
}

func TestCanonicalizeUnits_MutatesConvertibleAndReportsRest(t *testing.T) {
	mr := &MapResult{QuantitySamples: []QuantitySample{
		{QuantityType: QTHeartRate, Value: 62, Unit: "count/min"},
		{QuantityType: "body_mass", Value: 165, Unit: "lb"},
		{QuantityType: QTHeartRate, Value: 70, Unit: "nonsense"},
	}}

	errs := CanonicalizeUnits(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "nonsense", errs[0].Unit)

	assert.Equal(t, "bpm", mr.QuantitySamples[0].Unit)
	assert.Equal(t, 62.0, mr.QuantitySamples[0].Value)
	assert.Equal(t, "kg", mr.QuantitySamples[1].Unit)
	assert.InDelta(t, 74.8427, mr.QuantitySamples[1].Value, 0.001)
	// The unconvertible sample is left untouched for the caller to record and drop.
	assert.Equal(t, "nonsense", mr.QuantitySamples[2].Unit)
}

func TestCanonicalizeUnits_IsIdempotent(t *testing.T) {
	// A replayed file runs the same pass again; converting twice must not scale twice.
	mr := &MapResult{QuantitySamples: []QuantitySample{
		{QuantityType: "height", Value: 1.83, Unit: "m"},
	}}
	require.Empty(t, CanonicalizeUnits(mr))
	first := mr.QuantitySamples[0].Value

	require.Empty(t, CanonicalizeUnits(mr))
	assert.Equal(t, first, mr.QuantitySamples[0].Value)
}

func TestCanonicalizeUnits_NilResult(t *testing.T) {
	assert.Nil(t, CanonicalizeUnits(nil))
}

// TestCanonicalizeUnits_LeavesContentHash is the invariant that keeps a backfill
// safe. The hash is the dedup identity of the SOURCE record, computed in the
// mapper from the pre-canonicalization value, so replaying a raw file written
// before a conversion existed still hashes to the row already stored and dedups
// against it. If this pass ever recomputed the hash, every such replay would
// write a second copy of the sample on the old scale, straight past the
// migration that had just repaired it.
func TestCanonicalizeUnits_LeavesContentHash(t *testing.T) {
	mr := &MapResult{QuantitySamples: []QuantitySample{
		{QuantityType: QTOxygenSaturation, Value: 0.97, Unit: "fraction", ContentHash: "spo2-hash"},
		{QuantityType: "apple_exercise_time", Value: 300000, Unit: "ms", ContentHash: "exercise-hash"},
	}}

	require.Empty(t, CanonicalizeUnits(mr))

	assert.InDelta(t, 97.0, mr.QuantitySamples[0].Value, 0.0001)
	assert.Equal(t, "spo2-hash", mr.QuantitySamples[0].ContentHash)
	assert.InDelta(t, 5.0, mr.QuantitySamples[1].Value, 0.0001)
	assert.Equal(t, "exercise-hash", mr.QuantitySamples[1].ContentHash)
}

// Every alias must resolve to a unit that is actually the storage unit of some
// canonical type. An alias pointing at a spelling nothing stores is dead config
// that will silently fail to match at conversion time.
func TestUnitAliasTargetsAreRealStorageUnits(t *testing.T) {
	valid := make(map[string]struct{})
	for _, u := range storageUnits {
		valid[u] = struct{}{}
	}
	for src, target := range unitAliases {
		_, ok := valid[target]
		assert.True(t, ok, "alias %q targets %q, which is no type's storage unit", src, target)
	}
}

// Same for the conversion tables: a transform whose target nothing stores can never fire.
func TestConversionTargetsAreRealStorageUnits(t *testing.T) {
	valid := make(map[string]struct{})
	for _, u := range storageUnits {
		valid[u] = struct{}{}
	}
	for src, c := range unitConversions {
		_, ok := valid[c.To]
		assert.True(t, ok, "conversion from %q targets %q, which is no type's storage unit", src, c.To)
	}
	for key, c := range metricUnitConversions {
		_, ok := valid[c.To]
		assert.True(t, ok, "conversion %q targets %q, which is no type's storage unit", key, c.To)
	}
}

// unitExemptQuantityTypes are quantity types deliberately left without a declared
// storage unit. Coded or waveform-shaped values whose `unit` carries a
// classification rather than a physical unit, plus the test-only dummy metric.
var unitExemptQuantityTypes = map[string]struct{}{
	"audiogram":               {}, // per-ear frequency/threshold payload, not one scalar
	"ecg_afib_classification": {}, // coded classification, not a measurement
	"dummy_metric":            {}, // dummy mapper, tests only
}

// The forms the mappers use to name a quantity type: a string literal in a struct
// or table field, a QT constant (bare or package-qualified), a literal passed to
// CanonicalQuantityType, and the Apple Health identifier table whose values are
// canonical keys.
var (
	qtLiteralRe   = regexp.MustCompile(`(?i)quantitytype:\s*"([^"]+)"`)
	qtConstUseRe  = regexp.MustCompile(`(?i)quantitytype:\s*(?:\w+\.)?(QT\w+)`)
	qtCanonCallRe = regexp.MustCompile(`CanonicalQuantityType\(\s*"([^"]+)"\s*\)`)
	qtHKTableRe   = regexp.MustCompile(`"HKQuantityTypeIdentifier\w+":\s*"([^"]+)"`)
	qtConstDeclRe = regexp.MustCompile(`\b(QT\w+)\s*=\s*"([^"]+)"`)
)

// Guard: every quantity type a connector actually emits must have a declared
// storage unit (or be explicitly exempt). This is what stops a new provider from
// reintroducing the drift this package exists to remove: a mapper shipping
// heart_rate in an undeclared unit fails the build instead of writing a second
// unit spelling into the column.
//
// Connectors live at the repository root under connectors/<slug>/. Test files are
// skipped: fixtures there may use values that never reach the writer. The guard
// skips only while no connector source exists; once one does, finding no quantity
// type at all is a failure, because it means the extraction no longer matches how
// the connectors name them.
func TestEveryEmittedQuantityTypeHasAStorageUnit(t *testing.T) {
	all, err := filepath.Glob(filepath.Join("..", "..", "connectors", "*", "*.go"))
	require.NoError(t, err)
	var files []string
	for _, f := range all {
		// connectors/all only holds the blank imports, no mapper.
		if !strings.HasSuffix(f, "_test.go") && filepath.Base(filepath.Dir(f)) != "all" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		t.Skip("no connector sources under connectors/ yet; this guard goes live with the first connector")
	}

	quantitySrc, err := os.ReadFile("quantity.go")
	require.NoError(t, err)
	consts := map[string]string{}
	for _, m := range qtConstDeclRe.FindAllStringSubmatch(string(quantitySrc), -1) {
		consts[m[1]] = m[2]
	}
	require.NotEmpty(t, consts, "could not read the QT constants from quantity.go")

	seen := map[string]string{}
	add := func(qt, file string) {
		if _, dup := seen[qt]; !dup {
			seen[qt] = file
		}
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		src := string(raw)
		for _, m := range qtLiteralRe.FindAllStringSubmatch(src, -1) {
			add(m[1], f)
		}
		for _, m := range qtCanonCallRe.FindAllStringSubmatch(src, -1) {
			add(CanonicalQuantityType(m[1]), f)
		}
		for _, m := range qtHKTableRe.FindAllStringSubmatch(src, -1) {
			add(m[1], f)
		}
		for _, m := range qtConstUseRe.FindAllStringSubmatch(src, -1) {
			v, ok := consts[m[1]]
			require.Truef(t, ok, "%s uses %s, which is not a constant in quantity.go", f, m[1])
			add(v, f)
		}
	}
	require.NotEmpty(t, seen, "connector sources exist but no quantity type was found in them: update the extraction patterns in this test")

	for qt, file := range seen {
		if _, exempt := unitExemptQuantityTypes[qt]; exempt {
			continue
		}
		_, declared := storageUnits[qt]
		assert.True(t, declared,
			"%s emits quantity_type %q with no storage unit declared in units.go: add it there, or to unitExemptQuantityTypes with a reason",
			file, qt)
	}
}
