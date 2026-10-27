// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"math"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qs builds a single-sample MapResult with a safe (recent-past) timestamp so
// only the value-range rule under test can fire.
func qs(qt string, v float64) *model.MapResult {
	return &model.MapResult{
		QuantitySamples: []model.QuantitySample{{
			QuantityType: qt,
			Value:        v,
			StartDate:    time.Now().Add(-1 * time.Hour),
			EndDate:      time.Now().Add(-59 * time.Minute),
		}},
	}
}

func TestValidate_NonNegativeMetrics(t *testing.T) {
	// hrv_sdnn used to sit in this list. It moved out when the soft band landed:
	// an SDNN of exactly 0 ms means no beat-to-beat variability at all and 1234 ms
	// is off any physiological scale, so both are now suspect rather than clean.
	// See TestValidate_HRVSDNNBands.
	for _, qt := range []string{"step_count", "active_energy_burned", "distance_walking_running", "intraday_activity"} {
		t.Run(qt+"/valid-zero", func(t *testing.T) {
			assert.Empty(t, ValidateMapResult(qs(qt, 0)))
		})
		t.Run(qt+"/valid-positive", func(t *testing.T) {
			assert.Empty(t, ValidateMapResult(qs(qt, 1234.5)))
		})
		t.Run(qt+"/invalid-negative", func(t *testing.T) {
			errs := ValidateMapResult(qs(qt, -1))
			require.Len(t, errs, 1)
			assert.Equal(t, "value", errs[0].Field)
			assert.Contains(t, errs[0].Message, "non-negative")
		})
	}
}

// TestValidate_HRVSDNNBands: the hard rule stays "non-negative", the soft band
// is what catches the two garbage shapes a real feed produces.
func TestValidate_HRVSDNNBands(t *testing.T) {
	assert.Empty(t, ValidateMapResult(qs("hrv_sdnn", 42)))
	assert.Empty(t, ValidateMapResult(qs("hrv_sdnn", 180)))

	for _, v := range []float64{0, 1234.5} {
		errs := ValidateMapResult(qs("hrv_sdnn", v))
		require.Lenf(t, errs, 1, "hrv_sdnn=%v", v)
		assert.Equal(t, SeveritySuspect, errs[0].Severity, "kept and flagged, not dropped")
	}

	errs := ValidateMapResult(qs("hrv_sdnn", -1))
	require.Len(t, errs, 1)
	assert.Equal(t, SeverityReject, errs[0].Severity)
	assert.Contains(t, errs[0].Message, "non-negative")
}

func TestValidate_BodyFatPercentageRange(t *testing.T) {
	assert.Empty(t, ValidateMapResult(qs("body_fat_percentage", 22.5)))

	// 0 and 100 are a scale that failed to measure (essential fat is about 3
	// percent, and 0.18 is a fraction sent as percent): refused.
	for _, v := range []float64{0, 0.18, 100} {
		errs := ValidateMapResult(qs("body_fat_percentage", v))
		require.Lenf(t, errs, 1, "body_fat_percentage=%v", v)
		assert.Equal(t, SeverityReject, errs[0].Severity)
	}
	// 72 to 80 is possible in severe obesity but rare, so it comes back
	// flagged; only above 80 is refused.
	for _, v := range []float64{72, 78, 80} {
		errs := ValidateMapResult(qs("body_fat_percentage", v))
		require.Lenf(t, errs, 1, "body_fat_percentage=%v", v)
		assert.Equal(t, SeveritySuspect, errs[0].Severity)
	}
	require.Len(t, Rejections(ValidateMapResult(qs("body_fat_percentage", 80.5))), 1)

	require.Len(t, ValidateMapResult(qs("body_fat_percentage", 150)), 1)
	require.Len(t, ValidateMapResult(qs("body_fat_percentage", -5)), 1)
}

func TestValidate_PositiveMasses(t *testing.T) {
	// One realistic value per metric. The old single 12.3 kg stood in for all
	// five, which is nonsense for bone_mass (an adult skeleton is 2 to 4 kg) and
	// now trips its soft band.
	valid := map[string]float64{
		"lean_body_mass": 55.2,
		"body_fat_mass":  18.4,
		"bone_mass":      3.1,
		"muscle_mass":    32.7,
		"water_mass":     40.5,
	}
	for qt, v := range valid {
		t.Run(qt+"/valid", func(t *testing.T) {
			assert.Empty(t, ValidateMapResult(qs(qt, v)))
		})
		t.Run(qt+"/invalid-zero", func(t *testing.T) {
			errs := ValidateMapResult(qs(qt, 0))
			require.Len(t, errs, 1)
			assert.Contains(t, errs[0].Message, "positive")
			assert.Equal(t, SeverityReject, errs[0].Severity)
		})
		t.Run(qt+"/invalid-negative", func(t *testing.T) {
			require.Len(t, ValidateMapResult(qs(qt, -1)), 1)
		})
	}
}

// Real-data guard: the values actually present in the live dataset must all
// pass the new rules (these are the observed extremes from the audit).
func TestValidate_RealDataExtremesPass(t *testing.T) {
	samples := []struct {
		qt string
		v  float64
	}{
		{"step_count", 68302},
		{"active_energy_burned", 17536.07},
		{"distance_walking_running", 70961.63},
		{"body_fat_percentage", 41.109},
		{"body_fat_percentage", 16.179},
		{"lean_body_mass", 130.244},
		{"bone_mass", 4.38},
		{"hrv_sdnn", 42},
	}
	for _, s := range samples {
		assert.Emptyf(t, ValidateMapResult(qs(s.qt, s.v)), "real %s=%v should pass", s.qt, s.v)
	}
}

// qsUnit is qs with an explicit unit so unit-aware rules can be exercised.
func qsUnit(qt string, v float64, unit string) *model.MapResult {
	mr := qs(qt, v)
	mr.QuantitySamples[0].Unit = unit
	return mr
}

// TestValidate_BloodGlucoseUnitAware guards the regression where the validator
// assumed mmol/L [0,50] unconditionally and dropped every mg/dL reading (the
// values 88-161 seen in the live dataset are normal mg/dL glucose).
//
// The validator no longer branches on the unit itself. mg/dL is the storage unit
// and model.ToStorageUnit converts mmol/L before validation runs, so the
// mmol/L half of this test moved to TestValidate_UnitContractPairsWithConverter,
// where it exercises the converter and the range check together.
func TestValidate_BloodGlucoseUnitAware(t *testing.T) {
	// mg/dL (Apple Health default): normal-to-high readings must pass.
	for _, v := range []float64{72, 88, 117, 159, 240, 400} {
		assert.Emptyf(t, ValidateMapResult(qsUnit("blood_glucose", v, "mg/dL")), "mg/dL %v should pass", v)
	}
	// Empty unit defaults to mg/dL.
	assert.Empty(t, ValidateMapResult(qsUnit("blood_glucose", 117, "")))
	// Absurd mg/dL still rejected.
	require.Len(t, ValidateMapResult(qsUnit("blood_glucose", 5000, "mg/dL")), 1)
	require.Len(t, ValidateMapResult(qsUnit("blood_glucose", -1, "mg/dL")), 1)
	// An unconverted mmol/L value reaching the validator means the sample skipped
	// the choke point, so it parks instead of being silently mis-scaled.
	unconverted := ValidateMapResult(qsUnit("blood_glucose", 6.5, "mmol/L"))
	require.Len(t, unconverted, 1)
	assert.Equal(t, "unit", unconverted[0].Field)
	assert.Equal(t, SeverityReject, unconverted[0].Severity)
}

// A fraction sent with a percent unit (0.97 for 97 %) is not a reading: the
// hard floors of SpO2 and body fat refuse it instead of flagging it.
func TestValidate_FractionUnderPercentIsRejected(t *testing.T) {
	for _, c := range []struct {
		qt string
		v  float64
	}{{"oxygen_saturation", 0.97}, {"oxygen_saturation", 49}, {"body_fat_percentage", 0.18}, {"body_fat_percentage", 1.5}, {"body_fat_percentage", 85}} {
		errs := ValidateMapResult(qs(c.qt, c.v))
		require.Lenf(t, Rejections(errs), 1, "%s=%v", c.qt, c.v)
	}
	assert.Empty(t, Rejections(ValidateMapResult(qs("oxygen_saturation", 62))), "a severe desaturation is kept")
	assert.Empty(t, ValidateMapResult(qs("body_fat_percentage", 18.5)))
}

// Sleep sessions and daily summaries have figures of their own, checked with
// the bands of the matching quantity types.
func TestValidate_SleepAndDailyFigures(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	n := func(v int) *int { return &v }
	sleep := func(s model.SleepSession) []ValidationError {
		s.BedtimeStart, s.BedtimeEnd = now.Add(-9*time.Hour), now.Add(-time.Hour)
		return ValidateMapResultAt(&model.MapResult{SleepSessions: []model.SleepSession{s}}, now)
	}
	daily := func(d model.DailySummary) []ValidationError {
		d.SummaryDate = time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
		return ValidateMapResultAt(&model.MapResult{DailySummaries: []model.DailySummary{d}}, now)
	}

	assert.Empty(t, sleep(model.SleepSession{TotalSleepSeconds: n(26400), DeepSleepSeconds: n(5400), SleepScore: f(81),
		HRVAvgMs: f(48), RestingHR: f(51), RespiratoryRate: f(14), SpO2Avg: f(96)}))
	assert.Empty(t, daily(model.DailySummary{StepCount: n(10432), ActiveKcal: f(431), TotalKcal: f(2310), DistanceM: f(7650),
		FloorsClimbed: n(9), RestingHR: f(52), HRVAvgMs: f(47), ReadinessScore: f(77), StrainScore: f(12)}))

	for name, errs := range map[string][]ValidationError{
		"sleep spo2 fraction":    sleep(model.SleepSession{SpO2Avg: f(0.95)}),
		"sleep resting hr":       sleep(model.SleepSession{RestingHR: f(400)}),
		"sleep negative hrv":     sleep(model.SleepSession{HRVAvgMs: f(-1)}),
		"sleep respiratory rate": sleep(model.SleepSession{RespiratoryRate: f(90)}),
		"sleep score":            sleep(model.SleepSession{SleepScore: f(810)}),
		"sleep negative stage":   sleep(model.SleepSession{DeepSleepSeconds: n(-60)}),
		"daily negative steps":   daily(model.DailySummary{StepCount: n(-5)}),
		"daily resting hr":       daily(model.DailySummary{RestingHR: f(5)}),
		"daily negative energy":  daily(model.DailySummary{TotalKcal: f(-100)}),
		"daily score":            daily(model.DailySummary{RecoveryScore: f(140)}),
	} {
		rejects := Rejections(errs)
		require.Len(t, rejects, 1, name)
		assert.NotEqual(t, "bedtime_start", rejects[0].Field, name)
	}
	s := Suspicions(sleep(model.SleepSession{SpO2Avg: f(65)}))
	require.Len(t, s, 1, "a low but possible night average is flagged")
	assert.Equal(t, "spo2_avg", s[0].Field)
}

// TestValidate_NonFiniteValues: NaN fails every comparison, so a range check
// alone lets it through, and +Inf passes a rule whose ceiling is unbounded.
// Neither is a measurement, whether or not the type has a rule.
func TestValidate_NonFiniteValues(t *testing.T) {
	for _, qt := range []string{"heart_rate", "step_count", "no_rule_for_this_type"} {
		for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			errs := ValidateMapResult(qs(qt, v))
			require.NotEmptyf(t, errs, "%s=%v", qt, v)
			assert.Equal(t, "value", errs[0].Field)
			assert.Equal(t, SeverityReject, errs[0].Severity)
		}
	}
}

func TestCheckValue_RefusesInfOnUnboundedRule(t *testing.T) {
	inf := math.Inf(1)
	e := checkValue("nutrition_log", "sodium_mg", &inf, rangeRule{HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: unbounded})
	require.NotNil(t, e)
	assert.Equal(t, SeverityReject, e.Severity)
}
