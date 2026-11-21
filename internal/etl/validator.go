// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// ValidationError represents a single data quality validation failure.
type ValidationError struct {
	RecordType string // "quantity_sample", "workout", "sleep_session", etc.
	Field      string // "value", "start_date", "bedtime_start", etc.
	Value      any
	Message    string
	// Severity decides what the caller does with the record: SeverityReject
	// parks it, SeveritySuspect writes it with a flag. The zero value is
	// SeverityReject, so a check that predates the severity split (timestamps,
	// orderings, GPS bounds) keeps rejecting exactly as it did.
	Severity Severity
	// Index is the position of the offending record within its slice on the
	// MapResult, so the caller can flag that specific sample without rerunning
	// the rules.
	Index int
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s.%s: %s (value: %v)", e.RecordType, e.Field, e.Message, e.Value)
}

// Rejections returns the errors that must stop a record from being written.
func Rejections(errs []ValidationError) []ValidationError {
	return filterSeverity(errs, SeverityReject)
}

// Suspicions returns the errors that flag a value without blocking the write.
func Suspicions(errs []ValidationError) []ValidationError {
	return filterSeverity(errs, SeveritySuspect)
}

func filterSeverity(errs []ValidationError, want Severity) []ValidationError {
	var out []ValidationError
	for _, e := range errs {
		sev := e.Severity
		if sev == "" {
			sev = SeverityReject
		}
		if sev == want {
			out = append(out, e)
		}
	}
	return out
}

// FlagSuspects annotates the records named by errs so the implausible value is
// visible after the write. Metadata is already persisted as JSONB on every
// record kind flagged here, so this needs no migration and stays queryable.
//
// Quantity samples are flagged for their value; sleep sessions, daily
// summaries and workouts for one of their figures. Errors for other record
// types are ignored here.
func FlagSuspects(mr *model.MapResult, errs []ValidationError) {
	if mr == nil {
		return
	}
	for _, e := range errs {
		md := recordMetadata(mr, e)
		if md == nil {
			continue
		}
		md["quality_flag"] = string(SeveritySuspect)
		md["quality_note"] = e.Message
	}
}

// recordMetadata returns the metadata of the record e names, creating it when
// the record has none, or nil when e names no record that carries flags.
func recordMetadata(mr *model.MapResult, e ValidationError) map[string]any {
	var md *map[string]any
	switch e.RecordType {
	case "quantity_sample":
		if e.Index >= 0 && e.Index < len(mr.QuantitySamples) {
			md = &mr.QuantitySamples[e.Index].Metadata
		}
	case "sleep_session":
		if e.Index >= 0 && e.Index < len(mr.SleepSessions) {
			md = &mr.SleepSessions[e.Index].Metadata
		}
	case "daily_summary":
		if e.Index >= 0 && e.Index < len(mr.DailySummaries) {
			md = &mr.DailySummaries[e.Index].Metadata
		}
	case "workout":
		if e.Index >= 0 && e.Index < len(mr.Workouts) {
			md = &mr.Workouts[e.Index].Metadata
		}
	}
	if md == nil {
		return nil
	}
	if *md == nil {
		*md = map[string]any{}
	}
	return *md
}

// ValidateMapResult validates all records in a MapResult against type-specific rules.
// Returns all validation errors found (not just the first).
func ValidateMapResult(mr *model.MapResult) []ValidationError {
	return ValidateMapResultAt(mr, time.Now())
}

// ValidateMapResultAt is ValidateMapResult with an explicit "now" for the
// future-timestamp check.
func ValidateMapResultAt(mr *model.MapResult, now time.Time) []ValidationError {
	var errs []ValidationError

	for i := range mr.QuantitySamples {
		errs = append(errs, at(validateQuantitySample(&mr.QuantitySamples[i], now), i)...)
	}
	for i := range mr.CategorySamples {
		errs = append(errs, at(validateCategorySample(&mr.CategorySamples[i], now), i)...)
	}
	for i := range mr.SleepSessions {
		errs = append(errs, at(validateSleepSession(&mr.SleepSessions[i], now), i)...)
	}
	for i := range mr.SleepPhases {
		errs = append(errs, at(validateSleepPhase(&mr.SleepPhases[i], now), i)...)
	}
	for i := range mr.Workouts {
		errs = append(errs, at(validateWorkout(&mr.Workouts[i], now), i)...)
	}
	for i := range mr.RoutePoints {
		errs = append(errs, at(validateRoutePoint(&mr.RoutePoints[i], now), i)...)
	}
	for i := range mr.NutritionLogs {
		errs = append(errs, at(validateNutritionLog(&mr.NutritionLogs[i], now), i)...)
	}
	for i := range mr.DailySummaries {
		errs = append(errs, at(validateDailySummary(&mr.DailySummaries[i], now), i)...)
	}
	for i := range mr.Observations {
		errs = append(errs, at(validateObservation(&mr.Observations[i], now), i)...)
	}
	for i := range mr.ECGRecordings {
		errs = append(errs, at(validateECGRecording(&mr.ECGRecordings[i], now), i)...)
	}
	for i := range mr.ECGSamples {
		errs = append(errs, at(validateECGVoltageSample(&mr.ECGSamples[i], now), i)...)
	}
	for i := range mr.HeartbeatSamples {
		errs = append(errs, at(validateHeartbeatSample(&mr.HeartbeatSamples[i], now), i)...)
	}

	return errs
}

// at stamps the slice position onto every error a per-record check produced, so
// the caller can find the record again without rerunning the rules.
func at(errs []ValidationError, i int) []ValidationError {
	for j := range errs {
		errs[j].Index = i
	}
	return errs
}

// futureTimestampBuffer is how far ahead of now a timestamp may be before it is
// rejected as invalid. Fix 6 widened this from 24h to 48h so legitimate edge
// cases (a UTC+14 timezone plus modest device clock skew can put a "now" reading
// up to ~38h ahead of server UTC) are not dropped. Combined with Fix 1, a reading
// that still trips this is recoverable rather than silently lost.
const futureTimestampBuffer = 48 * time.Hour

// earliestTimestamp is the oldest time a record may carry. Before it a time is
// a zero or epoch default (0001-01-01, 1970-01-01, the FIT epoch of
// 1989-12-31) rather than a measurement: no wearable or app this pipeline
// reads recorded anything before 1990.
var earliestTimestamp = time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)

// checkFloor rejects a time before earliestTimestamp. A zero time is
// rejected too: on a start it is a date the source never filled in.
func checkFloor(recordType, field string, t time.Time) []ValidationError {
	if !t.Before(earliestTimestamp) {
		return nil
	}
	return []ValidationError{{
		RecordType: recordType,
		Field:      field,
		Value:      t,
		Message:    "timestamp is before 1990-01-01",
		Severity:   SeverityReject,
	}}
}

// isFutureTimestamp returns true if t is more than futureTimestampBuffer after now.
func isFutureTimestamp(t, now time.Time) bool {
	return t.After(now.Add(futureTimestampBuffer))
}

// validateQuantitySample applies the type's range rule plus the timestamp check.
//
// A quantity type with no rule gets the timestamp check only. That is deliberate:
// a connector may emit a metric no rule declares yet, and it must land rather
// than fail every record of that type. The coverage guard in
// validator_rules_test.go is what stops a DECLARED metric from ending up here.
func validateQuantitySample(s *model.QuantitySample, now time.Time) []ValidationError {
	var errs []ValidationError

	// NaN fails every comparison and +Inf passes an unbounded ceiling, so the
	// range rule cannot catch them, and a type with no rule has no range rule
	// at all. Neither is a measurement: reject before the rule runs.
	if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
		errs = append(errs, ValidationError{
			RecordType: "quantity_sample",
			Field:      "value",
			Value:      s.Value,
			Message:    fmt.Sprintf("%s value is not a finite number: %g", s.QuantityType, s.Value),
			Severity:   SeverityReject,
		})
	} else if rule, ok := rangeRules[s.QuantityType]; ok {
		if e := checkRange(s, rule); e != nil {
			errs = append(errs, *e)
		}
	}

	// Timestamp validation: applies to all quantity types
	if isFutureTimestamp(s.StartDate, now) {
		errs = append(errs, ValidationError{
			RecordType: "quantity_sample",
			Field:      "start_date",
			Value:      s.StartDate,
			Message:    "timestamp is in the future (more than 48h ahead)",
			Severity:   SeverityReject,
		})
	}
	errs = append(errs, checkFloor("quantity_sample", "start_date", s.StartDate)...)
	errs = append(errs, checkEnd("quantity_sample", s.StartDate, s.EndDate, now)...)

	return errs
}

// checkEnd rejects an end that precedes the start or lies in the future. A
// zero end means an instant and passes. A start already in the future is
// reported by the caller, so its end is not reported twice.
func checkEnd(recordType string, start, end, now time.Time) []ValidationError {
	if end.IsZero() {
		return nil
	}
	var errs []ValidationError
	if end.Before(start) {
		errs = append(errs, ValidationError{
			RecordType: recordType,
			Field:      "end_date",
			Value:      end,
			Message:    "end_date must not be before start_date",
			Severity:   SeverityReject,
		})
	}
	if isFutureTimestamp(end, now) && !isFutureTimestamp(start, now) {
		errs = append(errs, ValidationError{
			RecordType: recordType,
			Field:      "end_date",
			Value:      end,
			Message:    "timestamp is in the future (more than 48h ahead)",
			Severity:   SeverityReject,
		})
	}
	return errs
}

// checkRange returns at most one error for a sample: the hard band wins over the
// soft one, so a single bad value never produces two findings.
func checkRange(s *model.QuantitySample, r rangeRule) *ValidationError {
	// Unit contract. By the time a sample reaches the validator, the processor
	// has already run model.CanonicalizeUnits, so a surviving foreign unit means
	// the sample took a path that skipped the choke point. The magnitude is then
	// unknown and range-checking it would be theatre, so reject: the record parks
	// as recoverable and a replay lands it once the conversion exists.
	// An empty unit is treated as already canonical, matching ToStorageUnit.
	if r.Unit != "" && s.Unit != "" && !strings.EqualFold(s.Unit, r.Unit) {
		return &ValidationError{
			RecordType: "quantity_sample",
			Field:      "unit",
			Value:      s.Unit,
			Message: fmt.Sprintf("%s carries unit %q but the storage unit is %q, so the value cannot be range-checked",
				s.QuantityType, s.Unit, r.Unit),
			Severity: SeverityReject,
		}
	}

	v := s.Value
	belowHard := v < r.HardMin || (r.HardMinExclusive && v == r.HardMin)
	if belowHard || v > r.HardMax {
		return &ValidationError{
			RecordType: "quantity_sample",
			Field:      "value",
			Value:      v,
			Message:    hardRangeMessage(s.QuantityType, r, v),
			Severity:   SeverityReject,
		}
	}

	if v < r.SoftMin || v > r.SoftMax {
		return &ValidationError{
			RecordType: "quantity_sample",
			Field:      "value",
			Value:      v,
			Message: fmt.Sprintf("%s is implausible, outside [%g,%g]%s: %g",
				s.QuantityType, r.SoftMin, r.SoftMax, unitSuffix(r.Unit), v),
			Severity: SeveritySuspect,
		}
	}

	return nil
}

// hardRangeMessage phrases a hard-band violation. A value at or under an
// exclusive zero, and any value of a metric with no physical ceiling, keep the
// "must be positive" / "must be non-negative" wording the pre-table validator
// used, because the recovery tooling and the existing failed_records rows
// carry it.
func hardRangeMessage(qt string, r rangeRule, v float64) string {
	if r.HardMinExclusive && r.HardMin == 0 && v <= 0 {
		return fmt.Sprintf("%s must be positive: %g", qt, v)
	}
	if math.IsInf(r.HardMax, 1) {
		return fmt.Sprintf("%s must be non-negative: %g", qt, v)
	}
	return fmt.Sprintf("%s out of range [%g,%g]%s: %g", qt, r.HardMin, r.HardMax, unitSuffix(r.Unit), v)
}

func unitSuffix(unit string) string {
	if unit == "" {
		return ""
	}
	return " " + unit
}

// validateCategorySample checks timestamp validity.
func validateCategorySample(s *model.CategorySample, now time.Time) []ValidationError {
	var errs []ValidationError

	if isFutureTimestamp(s.StartDate, now) {
		errs = append(errs, ValidationError{
			RecordType: "category_sample",
			Field:      "start_date",
			Value:      s.StartDate,
			Message:    "timestamp is in the future (more than 48h ahead)",
		})
	}
	errs = append(errs, checkFloor("category_sample", "start_date", s.StartDate)...)
	errs = append(errs, checkEnd("category_sample", s.StartDate, s.EndDate, now)...)

	return errs
}

// validateSleepPhase checks a stage interval like any other sample: not in
// the future and not ending before it starts. A rejected phase parks its
// whole record, since a night with a stage cut out would misreport it.
func validateSleepPhase(p *model.SleepPhase, now time.Time) []ValidationError {
	var errs []ValidationError

	if isFutureTimestamp(p.StartDate, now) {
		errs = append(errs, ValidationError{
			RecordType: "sleep_phase",
			Field:      "start_date",
			Value:      p.StartDate,
			Message:    "timestamp is in the future (more than 48h ahead)",
		})
	}
	errs = append(errs, checkFloor("sleep_phase", "start_date", p.StartDate)...)
	errs = append(errs, checkEnd("sleep_phase", p.StartDate, p.EndDate, now)...)

	return errs
}

// validateSleepSession checks ordering, timestamps and the night's figures.
func validateSleepSession(s *model.SleepSession, now time.Time) []ValidationError {
	var errs []ValidationError

	if isFutureTimestamp(s.BedtimeStart, now) {
		errs = append(errs, ValidationError{
			RecordType: "sleep_session",
			Field:      "bedtime_start",
			Value:      s.BedtimeStart,
			Message:    "timestamp is in the future (more than 48h ahead)",
		})
	}
	errs = append(errs, checkFloor("sleep_session", "bedtime_start", s.BedtimeStart)...)

	if isFutureTimestamp(s.BedtimeEnd, now) {
		errs = append(errs, ValidationError{
			RecordType: "sleep_session",
			Field:      "bedtime_end",
			Value:      s.BedtimeEnd,
			Message:    "timestamp is in the future (more than 48h ahead)",
		})
	}

	if s.BedtimeStart.After(s.BedtimeEnd) {
		errs = append(errs, ValidationError{
			RecordType: "sleep_session",
			Field:      "bedtime_start",
			Value:      s.BedtimeStart,
			Message:    "bedtime_start must be before bedtime_end",
		})
	}

	errs = append(errs, checkFigures("sleep_session", []figure{
		intFigure("total_sleep_seconds", s.TotalSleepSeconds, sleepSeconds),
		intFigure("deep_sleep_seconds", s.DeepSleepSeconds, sleepSeconds),
		intFigure("rem_sleep_seconds", s.REMSleepSeconds, sleepSeconds),
		intFigure("light_sleep_seconds", s.LightSleepSeconds, sleepSeconds),
		intFigure("awake_seconds", s.AwakeSeconds, sleepSeconds),
		{"sleep_score", s.SleepScore, rangeRules[model.QTSleepScore]},
		{"hrv_avg_ms", s.HRVAvgMs, rangeRules[model.QTHRVRMSSD]},
		{"resting_hr", s.RestingHR, rangeRules[model.QTRestingHeartRate]},
		{"respiratory_rate", s.RespiratoryRate, rangeRules[model.QTRespiratoryRate]},
		{"spo2_avg", s.SpO2Avg, rangeRules[model.QTOxygenSaturation]},
	})...)

	return errs
}

// validateWorkout checks ordering, duration, and timestamps.
func validateWorkout(w *model.Workout, now time.Time) []ValidationError {
	var errs []ValidationError

	if isFutureTimestamp(w.StartDate, now) {
		errs = append(errs, ValidationError{
			RecordType: "workout",
			Field:      "start_date",
			Value:      w.StartDate,
			Message:    "timestamp is in the future (more than 48h ahead)",
		})
	}
	errs = append(errs, checkFloor("workout", "start_date", w.StartDate)...)

	if w.StartDate.After(w.EndDate) {
		errs = append(errs, ValidationError{
			RecordType: "workout",
			Field:      "start_date",
			Value:      w.StartDate,
			Message:    "start_date must be before or equal to end_date",
		})
	}

	if isFutureTimestamp(w.EndDate, now) && !isFutureTimestamp(w.StartDate, now) {
		errs = append(errs, ValidationError{
			RecordType: "workout",
			Field:      "end_date",
			Value:      w.EndDate,
			Message:    "timestamp is in the future (more than 48h ahead)",
		})
	}

	if w.DurationS != nil && *w.DurationS < 0 {
		errs = append(errs, ValidationError{
			RecordType: "workout",
			Field:      "duration_s",
			Value:      *w.DurationS,
			Message:    fmt.Sprintf("duration must be non-negative: %d", *w.DurationS),
		})
	}

	errs = append(errs, checkFigures("workout", []figure{
		{"avg_heart_rate", w.AvgHeartRate, workoutHeartRate},
		{"max_heart_rate", w.MaxHeartRate, workoutHeartRate},
		{"active_energy_kcal", w.ActiveEnergyKcal, rangeRules[model.QTActiveEnergyBurned]},
		{"distance_m", w.DistanceM, rangeRules[model.QTDistanceWalkingRunning]},
		{"avg_speed_mps", w.AvgSpeedMps, workoutSpeed},
		{"elevation_gain_m", w.ElevationGainM, workoutElevationGain},
		{"vo2_max", w.VO2Max, rangeRules["vo2_max"]},
	})...)

	return errs
}

// The bands of the workout summary fields that have no quantity type of
// their own. A heart rate of zero is a device that recorded none, not a
// measurement, so it is flagged instead of parking the whole workout.
var (
	workoutHeartRate     = rangeRule{HardMin: 0, HardMax: 300, SoftMin: 25, SoftMax: 250}
	workoutSpeed         = rangeRule{HardMin: 0, HardMax: 150, SoftMin: 0, SoftMax: 40}
	workoutElevationGain = rangeRule{HardMin: 0, HardMax: 20000, SoftMin: 0, SoftMax: 10000}
)

// sleepSeconds bounds a stage duration of one night: a stage longer than a
// day is a unit error, not a sleep.
var sleepSeconds = rangeRule{HardMin: 0, HardMax: 86400, SoftMin: 0, SoftMax: 86400}

// figure is one optional figure of a sleep session, a workout or a daily
// summary, with the band it must fall in.
type figure struct {
	field string
	v     *float64
	rule  rangeRule
}

func intFigure(field string, v *int, r rangeRule) figure {
	f := figure{field: field, rule: r}
	if v != nil {
		x := float64(*v)
		f.v = &x
	}
	return f
}

// checkFigures range-checks the figures of one record. A figure outside its
// hard band is dropped from the record by pruneRejected, not the record.
func checkFigures(recordType string, figures []figure) []ValidationError {
	var errs []ValidationError
	for _, f := range figures {
		if e := checkValue(recordType, f.field, f.v, f.rule); e != nil {
			errs = append(errs, *e)
		}
	}
	return errs
}

// checkValue range-checks one optional figure of a record: outside the hard
// band parks the record, outside the soft band flags it.
func checkValue(recordType, field string, v *float64, r rangeRule) *ValidationError {
	if v == nil {
		return nil
	}
	if math.IsNaN(*v) || math.IsInf(*v, 0) || *v < r.HardMin || *v > r.HardMax {
		return &ValidationError{
			RecordType: recordType,
			Field:      field,
			Value:      *v,
			Message:    fmt.Sprintf("%s out of range [%g, %g]: %g", field, r.HardMin, r.HardMax, *v),
			Severity:   SeverityReject,
		}
	}
	if *v < r.SoftMin || *v > r.SoftMax {
		return &ValidationError{
			RecordType: recordType,
			Field:      field,
			Value:      *v,
			Message:    fmt.Sprintf("%s outside the expected range [%g, %g]: %g", field, r.SoftMin, r.SoftMax, *v),
			Severity:   SeveritySuspect,
		}
	}
	return nil
}

// validateRoutePoint checks GPS coordinate ranges and timestamps.
func validateRoutePoint(p *model.RoutePoint, now time.Time) []ValidationError {
	var errs []ValidationError

	if p.Latitude < -90 || p.Latitude > 90 {
		errs = append(errs, ValidationError{
			RecordType: "route_point",
			Field:      "latitude",
			Value:      p.Latitude,
			Message:    fmt.Sprintf("latitude out of range [-90,90]: %g", p.Latitude),
		})
	}

	if p.Longitude < -180 || p.Longitude > 180 {
		errs = append(errs, ValidationError{
			RecordType: "route_point",
			Field:      "longitude",
			Value:      p.Longitude,
			Message:    fmt.Sprintf("longitude out of range [-180,180]: %g", p.Longitude),
		})
	}

	if isFutureTimestamp(p.Timestamp, now) {
		errs = append(errs, ValidationError{
			RecordType: "route_point",
			Field:      "timestamp",
			Value:      p.Timestamp,
			Message:    "timestamp is in the future (more than 48h ahead)",
		})
	}
	errs = append(errs, checkFloor("route_point", "timestamp", p.Timestamp)...)

	return errs
}

// validateNutritionLog checks timestamp validity.
func validateNutritionLog(n *model.NutritionLog, now time.Time) []ValidationError {
	var errs []ValidationError

	if isFutureTimestamp(n.StartDate, now) {
		errs = append(errs, ValidationError{
			RecordType: "nutrition_log",
			Field:      "start_date",
			Value:      n.StartDate,
			Message:    "timestamp is in the future (more than 48h ahead)",
		})
	}
	errs = append(errs, checkFloor("nutrition_log", "start_date", n.StartDate)...)
	if n.EndDate != nil {
		errs = append(errs, checkEnd("nutrition_log", n.StartDate, *n.EndDate, now)...)
	}

	for _, f := range []struct {
		field string
		v     *float64
		rule  rangeRule
	}{
		{"energy_kcal", n.EnergyKcal, mealEnergy},
		{"protein_g", n.ProteinG, mealGrams},
		{"carbs_g", n.CarbsG, mealGrams},
		{"fat_g", n.FatG, mealGrams},
		{"fiber_g", n.FiberG, mealGrams},
		{"sugar_g", n.SugarG, mealGrams},
		{"sodium_mg", n.SodiumMg, mealSodium},
	} {
		if e := checkValue("nutrition_log", f.field, f.v, f.rule); e != nil {
			errs = append(errs, *e)
		}
	}
	// The extended nutrients are amounts too: none can be negative.
	for _, k := range sortedKeys(n.NutrientsJSON) {
		v, ok := n.NutrientsJSON[k].(float64)
		if !ok {
			continue
		}
		if e := checkValue("nutrition_log", "nutrients."+k, &v, nonNegative); e != nil {
			errs = append(errs, *e)
		}
	}

	return errs
}

// The bands of a logged meal or drink. A day's intake rarely passes 10000
// kcal, so one log above 20000 is flagged, not refused.
var (
	mealEnergy  = rangeRule{HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 20000}
	mealGrams   = rangeRule{HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 5000}
	mealSodium  = rangeRule{HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 100000}
	nonNegative = rangeRule{HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: unbounded}
)

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// validateDailySummary rejects a summary of a day that has not begun and
// range-checks its figures.
func validateDailySummary(d *model.DailySummary, now time.Time) []ValidationError {
	var errs []ValidationError
	if isFutureTimestamp(d.SummaryDate, now) {
		errs = append(errs, ValidationError{
			RecordType: "daily_summary",
			Field:      "summary_date",
			Value:      d.SummaryDate,
			Message:    "timestamp is in the future (more than 48h ahead)",
		})
	}
	errs = append(errs, checkFloor("daily_summary", "summary_date", d.SummaryDate)...)
	errs = append(errs, checkFigures("daily_summary", []figure{
		intFigure("step_count", d.StepCount, rangeRules[model.QTStepCount]),
		{"active_kcal", d.ActiveKcal, rangeRules[model.QTActiveEnergyBurned]},
		{"total_kcal", d.TotalKcal, rangeRules[model.QTTotalEnergyBurned]},
		{"distance_m", d.DistanceM, rangeRules[model.QTDistanceWalkingRunning]},
		intFigure("floors_climbed", d.FloorsClimbed, rangeRules[model.QTFlightsClimbed]),
		{"resting_hr", d.RestingHR, rangeRules[model.QTRestingHeartRate]},
		{"hrv_avg_ms", d.HRVAvgMs, rangeRules[model.QTHRVRMSSD]},
		{"readiness_score", d.ReadinessScore, dayScore},
		{"activity_score", d.ActivityScore, dayScore},
		{"sleep_score", d.SleepScore, dayScore},
		{"recovery_score", d.RecoveryScore, dayScore},
		{"strain_score", d.StrainScore, rangeRules[model.QTStrain]},
	})...)
	return errs
}

// dayScore bounds the 0 to 100 scores of a day. A score of 0 is possible
// (a day with no activity), so unlike the readiness and sleep score samples
// nothing below 1 is flagged.
var dayScore = rangeRule{HardMin: 0, HardMax: 100, SoftMin: 0, SoftMax: 100}

// validateObservation checks an observation's instant and interval.
func validateObservation(o *model.Observation, now time.Time) []ValidationError {
	var errs []ValidationError

	if isFutureTimestamp(o.TS, now) {
		errs = append(errs, ValidationError{
			RecordType: "observation",
			Field:      "ts",
			Value:      o.TS,
			Message:    "timestamp is in the future (more than 48h ahead)",
		})
	}
	errs = append(errs, checkFloor("observation", "ts", o.TS)...)
	errs = append(errs, checkEnd("observation", o.TS, o.EndTS, now)...)

	return errs
}

// validateECGRecording checks a recording's interval like a sample's.
func validateECGRecording(r *model.ECGRecording, now time.Time) []ValidationError {
	var errs []ValidationError

	if isFutureTimestamp(r.StartDate, now) {
		errs = append(errs, ValidationError{
			RecordType: "ecg_recording",
			Field:      "start_date",
			Value:      r.StartDate,
			Message:    "timestamp is in the future (more than 48h ahead)",
		})
	}
	errs = append(errs, checkFloor("ecg_recording", "start_date", r.StartDate)...)
	errs = append(errs, checkEnd("ecg_recording", r.StartDate, r.EndDate, now)...)

	return errs
}

// validateECGVoltageSample checks basic range validity.
func validateECGVoltageSample(s *model.ECGVoltageSample, now time.Time) []ValidationError {
	var errs []ValidationError

	if s.TimeSinceSampleStart < 0 {
		errs = append(errs, ValidationError{
			RecordType: "ecg_voltage_sample",
			Field:      "time_since_sample_start",
			Value:      s.TimeSinceSampleStart,
			Message:    "time offset must be non-negative",
		})
	}

	return errs
}

// validateHeartbeatSample checks timestamp validity.
func validateHeartbeatSample(s *model.HeartbeatSample, now time.Time) []ValidationError {
	var errs []ValidationError

	if isFutureTimestamp(s.Timestamp, now) {
		errs = append(errs, ValidationError{
			RecordType: "heartbeat_sample",
			Field:      "timestamp",
			Value:      s.Timestamp,
			Message:    "timestamp is in the future (more than 48h ahead)",
		})
	}
	errs = append(errs, checkFloor("heartbeat_sample", "timestamp", s.Timestamp)...)

	if s.RRMS < 0 {
		errs = append(errs, ValidationError{
			RecordType: "heartbeat_sample",
			Field:      "rr_ms",
			Value:      s.RRMS,
			Message:    "RR interval must be non-negative",
		})
	}

	return errs
}
