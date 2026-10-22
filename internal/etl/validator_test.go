// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- QuantitySample validation tests ---

func TestValidateMapResult_HeartRateValid(t *testing.T) {
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "heart_rate",
				Value:        72,
				StartDate:    time.Now().Add(-1 * time.Hour),
				EndDate:      time.Now().Add(-59 * time.Minute),
			},
		},
	}
	errs := ValidateMapResult(mr)
	assert.Empty(t, errs)
}

func TestValidateMapResult_HeartRateTooLow(t *testing.T) {
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "heart_rate",
				Value:        0,
				StartDate:    time.Now().Add(-1 * time.Hour),
				EndDate:      time.Now().Add(-59 * time.Minute),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "value", errs[0].Field)
	assert.Contains(t, errs[0].Message, "20")
	assert.Contains(t, errs[0].Message, "300")
}

func TestValidateMapResult_HeartRateTooHigh(t *testing.T) {
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "heart_rate",
				Value:        350,
				StartDate:    time.Now().Add(-1 * time.Hour),
				EndDate:      time.Now().Add(-59 * time.Minute),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "value", errs[0].Field)
	assert.Contains(t, errs[0].Message, "300")
}

func TestValidateMapResult_SpO2Valid(t *testing.T) {
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "oxygen_saturation",
				Value:        99.5,
				StartDate:    time.Now().Add(-1 * time.Hour),
				EndDate:      time.Now().Add(-59 * time.Minute),
			},
		},
	}
	errs := ValidateMapResult(mr)
	assert.Empty(t, errs)
}

func TestValidateMapResult_SpO2TooHigh(t *testing.T) {
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "oxygen_saturation",
				Value:        105,
				StartDate:    time.Now().Add(-1 * time.Hour),
				EndDate:      time.Now().Add(-59 * time.Minute),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "value", errs[0].Field)
	assert.Contains(t, errs[0].Message, "100")
}

func TestValidateMapResult_SpO2AltName(t *testing.T) {
	// spo2 is an alias for oxygen_saturation
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "spo2",
				Value:        -1,
				StartDate:    time.Now().Add(-1 * time.Hour),
				EndDate:      time.Now().Add(-59 * time.Minute),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "value", errs[0].Field)
}

func TestValidateMapResult_FutureTimestamp(t *testing.T) {
	// A timestamp more than the 48h buffer in the future is invalid (Fix 6).
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "heart_rate",
				Value:        72,
				StartDate:    time.Now().Add(72 * time.Hour),
				EndDate:      time.Now().Add(72*time.Hour + time.Minute),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "start_date", errs[0].Field)
	assert.Contains(t, errs[0].Message, "future")
}

func TestValidateMapResult_RecentPastValid(t *testing.T) {
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "heart_rate",
				Value:        72,
				StartDate:    time.Now().Add(-1 * time.Hour),
				EndDate:      time.Now().Add(-59 * time.Minute),
			},
		},
	}
	errs := ValidateMapResult(mr)
	assert.Empty(t, errs)
}

func TestValidateMapResult_WithinFortyEightHoursValid(t *testing.T) {
	// Fix 6: a reading 36h ahead (UTC+14 timezone + device clock skew) is now valid.
	// Under the old 24h buffer it would have been rejected and (pre-Fix-1) lost.
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "heart_rate",
				Value:        72,
				StartDate:    time.Now().Add(36 * time.Hour),
				EndDate:      time.Now().Add(36*time.Hour + time.Minute),
			},
		},
	}
	errs := ValidateMapResult(mr)
	assert.Empty(t, errs, "a timestamp within the 48h buffer must be accepted")
}

func TestValidateMapResult_BodyMassPositive(t *testing.T) {
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "body_mass",
				Value:        0,
				StartDate:    time.Now().Add(-1 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "value", errs[0].Field)
}

func TestValidateMapResult_BodyTemperatureRange(t *testing.T) {
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "body_temperature",
				Value:        50, // above 45C
				StartDate:    time.Now().Add(-1 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "value", errs[0].Field)
}

func TestValidateMapResult_RespiratoryRateRange(t *testing.T) {
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "respiratory_rate",
				Value:        2, // below 4
				StartDate:    time.Now().Add(-1 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
}

func TestValidateMapResult_BloodPressureSystolicRange(t *testing.T) {
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "blood_pressure_systolic",
				Value:        400, // above 300
				StartDate:    time.Now().Add(-1 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
}

func TestValidateMapResult_BloodPressureDiastolicRange(t *testing.T) {
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "blood_pressure_diastolic",
				Value:        10, // below 20
				StartDate:    time.Now().Add(-1 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
}

func TestValidateMapResult_BloodGlucoseRange(t *testing.T) {
	// 60 mg/dL (no unit defaults to mg/dL) is a normal reading and must pass.
	// (The old rule wrongly assumed mmol/L [0,50] and rejected it.)
	ok := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "blood_glucose",
				Value:        60,
				StartDate:    time.Now().Add(-1 * time.Hour),
			},
		},
	}
	require.Empty(t, ValidateMapResult(ok))

	// An absurd mg/dL value is still rejected.
	bad := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "blood_glucose",
				Value:        5000,
				Unit:         "mg/dL",
				StartDate:    time.Now().Add(-1 * time.Hour),
			},
		},
	}
	require.Len(t, ValidateMapResult(bad), 1)
}

func TestValidateMapResult_UnknownTypeTimestampOnly(t *testing.T) {
	// Unknown quantity types only get timestamp validation
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "step_count",
				Value:        10000,
				StartDate:    time.Now().Add(-1 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	assert.Empty(t, errs)
}

// --- SleepSession validation tests ---

func TestValidateMapResult_SleepSessionValid(t *testing.T) {
	mr := &model.MapResult{
		SleepSessions: []model.SleepSession{
			{
				BedtimeStart: time.Now().Add(-8 * time.Hour),
				BedtimeEnd:   time.Now(),
			},
		},
	}
	errs := ValidateMapResult(mr)
	assert.Empty(t, errs)
}

func TestValidateMapResult_SleepStartAfterEnd(t *testing.T) {
	mr := &model.MapResult{
		SleepSessions: []model.SleepSession{
			{
				BedtimeStart: time.Now(),
				BedtimeEnd:   time.Now().Add(-8 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "bedtime_start", errs[0].Field)
}

func TestValidateMapResult_SleepFutureTimestamp(t *testing.T) {
	mr := &model.MapResult{
		SleepSessions: []model.SleepSession{
			{
				BedtimeStart: time.Now().Add(72 * time.Hour),
				BedtimeEnd:   time.Now().Add(80 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "future")
}

// --- Workout validation tests ---

func TestValidateMapResult_WorkoutValid(t *testing.T) {
	dur := 3600
	mr := &model.MapResult{
		Workouts: []model.Workout{
			{
				StartDate: time.Now().Add(-1 * time.Hour),
				EndDate:   time.Now(),
				DurationS: &dur,
			},
		},
	}
	errs := ValidateMapResult(mr)
	assert.Empty(t, errs)
}

func TestValidateMapResult_WorkoutNegativeDuration(t *testing.T) {
	dur := -100
	mr := &model.MapResult{
		Workouts: []model.Workout{
			{
				StartDate: time.Now().Add(-1 * time.Hour),
				EndDate:   time.Now(),
				DurationS: &dur,
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "duration_s", errs[0].Field)
}

func TestValidateMapResult_WorkoutStartAfterEnd(t *testing.T) {
	mr := &model.MapResult{
		Workouts: []model.Workout{
			{
				StartDate: time.Now(),
				EndDate:   time.Now().Add(-1 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "start_date", errs[0].Field)
}

// --- CategorySample validation tests ---

func TestValidateMapResult_CategorySampleValid(t *testing.T) {
	mr := &model.MapResult{
		CategorySamples: []model.CategorySample{
			{
				CategoryType: "sleep_analysis",
				StartDate:    time.Now().Add(-1 * time.Hour),
				EndDate:      time.Now(),
			},
		},
	}
	errs := ValidateMapResult(mr)
	assert.Empty(t, errs)
}

func TestValidateMapResult_CategorySampleFutureTimestamp(t *testing.T) {
	mr := &model.MapResult{
		CategorySamples: []model.CategorySample{
			{
				CategoryType: "sleep_analysis",
				StartDate:    time.Now().Add(72 * time.Hour),
				EndDate:      time.Now().Add(73 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.NotEmpty(t, errs)
}

// --- RoutePoint validation tests ---

func TestValidateMapResult_RoutePointValid(t *testing.T) {
	mr := &model.MapResult{
		RoutePoints: []model.RoutePoint{
			{
				Latitude:  48.8566,
				Longitude: 2.3522,
				Timestamp: time.Now().Add(-1 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	assert.Empty(t, errs)
}

func TestValidateMapResult_RoutePointInvalidLatitude(t *testing.T) {
	mr := &model.MapResult{
		RoutePoints: []model.RoutePoint{
			{
				Latitude:  91.0,
				Longitude: 2.3522,
				Timestamp: time.Now().Add(-1 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "latitude", errs[0].Field)
}

func TestValidateMapResult_RoutePointInvalidLongitude(t *testing.T) {
	mr := &model.MapResult{
		RoutePoints: []model.RoutePoint{
			{
				Latitude:  48.8566,
				Longitude: -181.0,
				Timestamp: time.Now().Add(-1 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1)
	assert.Equal(t, "longitude", errs[0].Field)
}

// --- Multiple errors collected ---

func TestValidateMapResult_CollectsAllErrors(t *testing.T) {
	// Multiple invalid records return multiple errors
	dur := -10
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{
				QuantityType: "heart_rate",
				Value:        0,
				StartDate:    time.Now().Add(-1 * time.Hour),
			},
			{
				QuantityType: "oxygen_saturation",
				Value:        105,
				StartDate:    time.Now().Add(-1 * time.Hour),
			},
		},
		Workouts: []model.Workout{
			{
				StartDate: time.Now().Add(-1 * time.Hour),
				EndDate:   time.Now(),
				DurationS: &dur,
			},
		},
	}
	errs := ValidateMapResult(mr)
	assert.Len(t, errs, 3, "should collect all validation errors, not just the first")
}

// --- NutritionLog validation ---

func TestValidateMapResult_NutritionLogValid(t *testing.T) {
	mr := &model.MapResult{
		NutritionLogs: []model.NutritionLog{
			{
				StartDate: time.Now().Add(-1 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	assert.Empty(t, errs)
}

func TestValidateMapResult_NutritionLogFutureTimestamp(t *testing.T) {
	mr := &model.MapResult{
		NutritionLogs: []model.NutritionLog{
			{
				StartDate: time.Now().Add(72 * time.Hour),
			},
		},
	}
	errs := ValidateMapResult(mr)
	require.NotEmpty(t, errs)
}

// --- ValidationError.Error() ---

func TestValidationError_ErrorString(t *testing.T) {
	ve := &ValidationError{
		RecordType: "quantity_sample",
		Field:      "value",
		Value:      350,
		Message:    "heart_rate out of range [20,300]: 350",
	}
	errStr := ve.Error()
	assert.Contains(t, errStr, "quantity_sample")
	assert.Contains(t, errStr, "value")
	assert.Contains(t, errStr, "350")
}

func TestValidateMapResult_EndBeforeStartAndFutureEnd(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	start := now.Add(-time.Hour)
	end := start.Add(-time.Minute)
	far := now.AddDate(70, 0, 0)
	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{{QuantityType: "heart_rate", Value: 60, StartDate: start, EndDate: end}},
		CategorySamples: []model.CategorySample{{StartDate: start, EndDate: far}},
		SleepPhases:     []model.SleepPhase{{StartDate: start, EndDate: end}},
		NutritionLogs:   []model.NutritionLog{{StartDate: start, EndDate: &far}},
		Workouts:        []model.Workout{{StartDate: start, EndDate: far}},
		ECGRecordings:   []model.ECGRecording{{StartDate: start, EndDate: end}},
		DailySummaries:  []model.DailySummary{{SummaryDate: far}},
		Observations:    []model.Observation{{TS: start, EndTS: end}},
	}
	got := map[string]string{}
	for _, e := range Rejections(ValidateMapResultAt(mr, now)) {
		got[e.RecordType] = e.Field
	}
	assert.Equal(t, map[string]string{
		"quantity_sample": "end_date", "category_sample": "end_date",
		"sleep_phase": "end_date", "nutrition_log": "end_date",
		"workout": "end_date", "ecg_recording": "end_date",
		"daily_summary": "summary_date", "observation": "end_date",
	}, got)
}
