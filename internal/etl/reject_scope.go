// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"slices"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// prunableRecordTypes are the record types whose rejection can be scoped to the
// one offending entry, because the entry is a LEAF: removing it leaves the rest
// of the mapped record coherent and writable.
//
// Anything absent from this list is structural. A sleep session whose bedtimes
// are out of order, or a workout that ends before it starts, is the parent its
// phases and route points hang off: writing the rest without it would persist
// children with nothing to attach to. An unrecognised record type is treated as
// structural too, so a new validator rule fails safe (whole record parked) until
// someone decides how to scope it.
var prunableRecordTypes = map[string]bool{
	"quantity_sample":    true,
	"category_sample":    true,
	"route_point":        true,
	"nutrition_log":      true,
	"ecg_voltage_sample": true,
	"heartbeat_sample":   true,
}

// pruneRejected removes from mr exactly the entries named by rejects, leaving
// everything else writable. It returns the rejects it could NOT scope; when that
// is non-empty mr is left untouched and the caller drops the whole record.
//
// This exists because one raw record is not one sample. A provider's daily
// record can map to a summary plus the whole day's heart-rate, HRV and
// saturation series; a sleep record maps to a session plus its phases plus the
// night's HR series.
// Dropping the MapResult over a single 310 bpm optical artifact erased the entire
// day, and reason_code 'validation' is deterministic-permanent to the drainer, so
// it was never replayed: the loss was permanent and invisible.
func pruneRejected(mr *model.MapResult, rejects []ValidationError) []ValidationError {
	if mr == nil || len(rejects) == 0 {
		return nil
	}

	var structural []ValidationError
	for _, e := range rejects {
		if !prunableRecordTypes[e.RecordType] && !isFigure(mr, e) {
			structural = append(structural, e)
		}
	}
	if len(structural) > 0 {
		return structural
	}

	drop := make(map[string]map[int]bool, 2)
	for _, e := range rejects {
		if !prunableRecordTypes[e.RecordType] {
			clearFigure(mr, e)
			continue
		}
		byType, ok := drop[e.RecordType]
		if !ok {
			byType = make(map[int]bool)
			drop[e.RecordType] = byType
		}
		byType[e.Index] = true
	}

	for recordType, idx := range drop {
		switch recordType {
		case "quantity_sample":
			mr.QuantitySamples = dropIndices(mr.QuantitySamples, idx)
		case "category_sample":
			mr.CategorySamples = dropIndices(mr.CategorySamples, idx)
		case "route_point":
			mr.RoutePoints = dropIndices(mr.RoutePoints, idx)
		case "nutrition_log":
			mr.NutritionLogs = dropIndices(mr.NutritionLogs, idx)
		case "ecg_voltage_sample":
			mr.ECGSamples = dropIndices(mr.ECGSamples, idx)
		case "heartbeat_sample":
			mr.HeartbeatSamples = dropIndices(mr.HeartbeatSamples, idx)
		}
	}
	return nil
}

// dropIndices removes the positions named by idx, preserving order. The slice is
// compacted in place: the caller owns the MapResult by this point.
func dropIndices[T any](s []T, idx map[int]bool) []T {
	if len(idx) == 0 {
		return s
	}
	out := s[:0]
	for i, v := range s {
		if idx[i] {
			continue
		}
		out = append(out, v)
	}
	return out
}

// figures are the optional figures of a parent record that a reject drops on
// their own: the record keeps its other figures and its children. A figure is
// one measurement (a night's SpO2, a day's steps); a parent's times are not,
// and stay structural.
var figures = map[string][]string{
	"sleep_session": {"total_sleep_seconds", "deep_sleep_seconds", "rem_sleep_seconds", "light_sleep_seconds",
		"awake_seconds", "sleep_score", "hrv_avg_ms", "resting_hr", "respiratory_rate", "spo2_avg"},
	"daily_summary": {"step_count", "active_kcal", "total_kcal", "distance_m", "floors_climbed", "resting_hr",
		"hrv_avg_ms", "readiness_score", "activity_score", "sleep_score", "recovery_score", "strain_score"},
	"workout": {"avg_heart_rate", "max_heart_rate", "active_energy_kcal", "distance_m", "avg_speed_mps",
		"elevation_gain_m", "vo2_max"},
}

// isFigure reports whether e names a figure of a record in mr. It leaves mr
// as it is: the caller may still park the whole record.
func isFigure(mr *model.MapResult, e ValidationError) bool {
	if !slices.Contains(figures[e.RecordType], e.Field) || e.Index < 0 {
		return false
	}
	switch e.RecordType {
	case "sleep_session":
		return e.Index < len(mr.SleepSessions)
	case "daily_summary":
		return e.Index < len(mr.DailySummaries)
	case "workout":
		return e.Index < len(mr.Workouts)
	}
	return false
}

// clearFigure removes the figure e names from its record and lists it under
// quality_dropped in the record's metadata, with the reason as quality_note,
// so the stored record says it is missing a measurement it was sent with.
func clearFigure(mr *model.MapResult, e ValidationError) {
	md := recordMetadata(mr, e)
	if md == nil {
		return
	}
	switch e.RecordType {
	case "sleep_session":
		s := &mr.SleepSessions[e.Index]
		for _, f := range []struct {
			name string
			ip   **int
			fp   **float64
		}{
			{"total_sleep_seconds", &s.TotalSleepSeconds, nil}, {"deep_sleep_seconds", &s.DeepSleepSeconds, nil},
			{"rem_sleep_seconds", &s.REMSleepSeconds, nil}, {"light_sleep_seconds", &s.LightSleepSeconds, nil},
			{"awake_seconds", &s.AwakeSeconds, nil}, {"sleep_score", nil, &s.SleepScore},
			{"hrv_avg_ms", nil, &s.HRVAvgMs}, {"resting_hr", nil, &s.RestingHR},
			{"respiratory_rate", nil, &s.RespiratoryRate}, {"spo2_avg", nil, &s.SpO2Avg},
		} {
			clearNamed(e.Field, f.name, f.ip, f.fp)
		}
	case "daily_summary":
		d := &mr.DailySummaries[e.Index]
		for _, f := range []struct {
			name string
			ip   **int
			fp   **float64
		}{
			{"step_count", &d.StepCount, nil}, {"active_kcal", nil, &d.ActiveKcal}, {"total_kcal", nil, &d.TotalKcal},
			{"distance_m", nil, &d.DistanceM}, {"floors_climbed", &d.FloorsClimbed, nil},
			{"resting_hr", nil, &d.RestingHR}, {"hrv_avg_ms", nil, &d.HRVAvgMs},
			{"readiness_score", nil, &d.ReadinessScore}, {"activity_score", nil, &d.ActivityScore},
			{"sleep_score", nil, &d.SleepScore}, {"recovery_score", nil, &d.RecoveryScore},
			{"strain_score", nil, &d.StrainScore},
		} {
			clearNamed(e.Field, f.name, f.ip, f.fp)
		}
	case "workout":
		w := &mr.Workouts[e.Index]
		for _, f := range []struct {
			name string
			fp   **float64
		}{
			{"avg_heart_rate", &w.AvgHeartRate}, {"max_heart_rate", &w.MaxHeartRate},
			{"active_energy_kcal", &w.ActiveEnergyKcal}, {"distance_m", &w.DistanceM},
			{"avg_speed_mps", &w.AvgSpeedMps}, {"elevation_gain_m", &w.ElevationGainM}, {"vo2_max", &w.VO2Max},
		} {
			clearNamed(e.Field, f.name, nil, f.fp)
		}
	}
	dropped, _ := md["quality_dropped"].([]string)
	md["quality_dropped"] = append(dropped, e.Field)
	md["quality_note"] = e.Message
}

func clearNamed(field, name string, ip **int, fp **float64) {
	if field != name {
		return
	}
	if ip != nil {
		*ip = nil
	}
	if fp != nil {
		*fp = nil
	}
}

// pruneUnitErrors removes the samples whose unit could not be converted, so
// one foreign unit drops its own sample and not the whole record.
func pruneUnitErrors(mr *model.MapResult, errs []model.UnitError) {
	idx := make(map[int]bool, len(errs))
	for _, e := range errs {
		idx[e.Index] = true
	}
	mr.QuantitySamples = dropIndices(mr.QuantitySamples, idx)
}
