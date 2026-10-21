// SPDX-License-Identifier: AGPL-3.0-only

package garminconnect

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// sourceName is the source_name of every record: the vendor.
const sourceName = "Garmin"

// recordHandler maps one raw line to canonical records. A nil result with a
// nil error means the line carries nothing this connector stores.
type recordHandler func(line []byte, userID string) (*model.MapResult, error)

// mapper dispatches stored raw lines by data type.
type mapper struct {
	handlers map[string]recordHandler

	mu           sync.Mutex
	unknownTypes map[string]int
}

// maxUnknownTypes bounds the unknown type counter.
const maxUnknownTypes = 64

func newMapper() *mapper {
	return &mapper{
		handlers: map[string]recordHandler{
			DataTypeDailies:    mapDaily,
			DataTypeSleeps:     mapSleep,
			DataTypeHRV:        mapHRV,
			DataTypeActivities: mapActivity,
		},
		unknownTypes: map[string]int{},
	}
}

// mapRecord re-encodes fields (one parsed raw line) and hands it to the
// handler of dataType.
func (m *mapper) mapRecord(dataType string, fields map[string]any, userID string) (*model.MapResult, error) {
	h, ok := m.handlers[dataType]
	if !ok {
		m.trackUnknown(dataType)
		return nil, nil
	}
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("garmin_connect: %s: re-encode line: %w", dataType, err)
	}
	return h(line, userID)
}

func (m *mapper) trackUnknown(dataType string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, seen := m.unknownTypes[dataType]; !seen && len(m.unknownTypes) >= maxUnknownTypes {
		return
	}
	m.unknownTypes[dataType]++
	if m.unknownTypes[dataType] == 1 {
		slog.Warn("garmin_connect: unknown data type skipped", "provider", Slug, "data_type", dataType)
	}
}

// Plausibility bounds of daily figures; a value outside is not stored.
const (
	maxDailySteps    = 1e6
	maxDailyDistance = 1e6 // m
	maxDailyKcal     = 5e4
	maxDailyFloors   = 1e4
)

// mapDaily turns one Connect daily summary into a DailySummary, day total
// samples (step_count, distance_walking_running, active and basal energy,
// flights_climbed, resting_heart_rate, stress_high_time).
//
// Garmin grows a day's totals while the day is open, so the summary and the
// totals are Replaceable and hashed on the calendar date, not the value.
func mapDaily(line []byte, userID string) (*model.MapResult, error) {
	var d DailySummary
	if err := decode(DataTypeDailies, line, &d); err != nil {
		return nil, err
	}
	day, err := parseDate(d.CalendarDate)
	if err != nil {
		return nil, fmt.Errorf("garmin_connect: dailies: %w", err)
	}
	date := d.CalendarDate
	start := day
	end := day.Add(24 * time.Hour)

	md := map[string]any{"date": date}
	setIf(md, "average_stress_level", d.AverageStressLevel)
	setIf(md, "max_stress_level", d.MaxStressLevel)
	setIf(md, "min_heart_rate", d.MinHeartRate)
	setIf(md, "max_heart_rate", d.MaxHeartRate)
	setIf(md, "moderate_intensity_minutes", d.ModerateIntensityMinutes)
	setIf(md, "vigorous_intensity_minutes", d.VigorousIntensityMinutes)

	res := &model.MapResult{}
	sum := model.DailySummary{
		UserID:      userID,
		Provider:    Slug,
		SummaryDate: day,
		Metadata:    copyMap(md),
		Replaceable: true,
	}
	figures := 0
	if inRange(d.TotalSteps, maxDailySteps) {
		n := int(math.Round(*d.TotalSteps))
		sum.StepCount = &n
		figures++
	}
	if inRange(d.TotalDistanceMeters, maxDailyDistance) {
		sum.DistanceM = d.TotalDistanceMeters
		figures++
	}
	if inRange(d.ActiveKilocalories, maxDailyKcal) {
		sum.ActiveKcal = d.ActiveKilocalories
		figures++
		if inRange(d.BMRKilocalories, maxDailyKcal) {
			total := *d.ActiveKilocalories + *d.BMRKilocalories
			sum.TotalKcal = &total
		}
	}
	if inRange(d.FloorsAscended, maxDailyFloors) {
		n := int(math.Round(*d.FloorsAscended))
		sum.FloorsClimbed = &n
		figures++
	}
	if inBounds(d.RestingHeartRate, 20, 250) {
		sum.RestingHR = d.RestingHeartRate
		figures++
	}
	if figures > 0 {
		sum.ContentHash = hash(userID, DataTypeDailies, "date", date)
		res.DailySummaries = append(res.DailySummaries, sum)
	}

	for _, m := range []struct {
		qt, unit string
		v        *float64
		ok       bool
	}{
		{model.QTStepCount, "count", d.TotalSteps, inRange(d.TotalSteps, maxDailySteps)},
		{model.QTDistanceWalkingRunning, "m", d.TotalDistanceMeters, inRange(d.TotalDistanceMeters, maxDailyDistance)},
		{model.QTActiveEnergyBurned, "kcal", d.ActiveKilocalories, inRange(d.ActiveKilocalories, maxDailyKcal)},
		{model.QTBasalEnergyBurned, "kcal", d.BMRKilocalories, inRange(d.BMRKilocalories, maxDailyKcal)},
		{model.QTFlightsClimbed, "count", d.FloorsAscended, inRange(d.FloorsAscended, maxDailyFloors)},
		{model.QTRestingHeartRate, "bpm", d.RestingHeartRate, inBounds(d.RestingHeartRate, 20, 250)},
		{model.QTStressHighTime, "s", d.HighStressDuration, inRange(d.HighStressDuration, 86400)},
	} {
		if !m.ok {
			continue
		}
		smd := copyMap(md)
		smd[model.MetadataSummaryDate] = date
		s := sample(userID, "daily_"+date+"_"+m.qt, m.qt, m.unit, *m.v, start, end, smd)
		s.Replaceable = true
		s.ContentHash = hash(userID, DataTypeDailies, "quantity_type", m.qt, "date", date)
		res.QuantitySamples = append(res.QuantitySamples, s)
	}
	if res.IsEmpty() {
		return nil, nil
	}
	return res, nil
}

// sleepStages maps the sleepLevels activityLevel to a canonical stage. This
// follows the interpretation the community clients use (0 deep, 1 light, 2
// REM, 3 awake); Garmin publishes no specification for it, so a value outside
// the set is not stored as a phase. The stage-second totals of the summary
// are authoritative regardless.
var sleepStages = map[int]string{
	0: model.SleepStageDeep,
	1: model.SleepStageLight,
	2: model.SleepStageREM,
	3: model.SleepStageAwake,
}

// mapSleep turns one Connect sleep response into a SleepSession, its stage
// phases, and a nightly hrv_rmssd sample when the night carries sleep HRV.
//
// The night is keyed by its calendar date, so the session is hashed on it and
// Replaceable: a night Garmin re-scores replaces the stored figures and phases.
func mapSleep(line []byte, userID string) (*model.MapResult, error) {
	var s SleepLine
	if err := decode(DataTypeSleeps, line, &s); err != nil {
		return nil, err
	}
	dto := s.DailySleepDTO
	if dto.CalendarDate == "" {
		return nil, nil
	}
	if _, err := parseDate(dto.CalendarDate); err != nil {
		return nil, fmt.Errorf("garmin_connect: sleeps: %w", err)
	}
	date := dto.CalendarDate
	if dto.SleepStartTimestampGMT == nil || *dto.SleepStartTimestampGMT <= 0 {
		return nil, nil
	}
	start := time.UnixMilli(*dto.SleepStartTimestampGMT).UTC()
	var end time.Time
	switch {
	case dto.SleepEndTimestampGMT != nil && *dto.SleepEndTimestampGMT > *dto.SleepStartTimestampGMT:
		end = time.UnixMilli(*dto.SleepEndTimestampGMT).UTC()
	case dto.SleepTimeSeconds != nil && *dto.SleepTimeSeconds > 0:
		end = start.Add(time.Duration(*dto.SleepTimeSeconds) * time.Second)
	default:
		return nil, nil
	}
	if !end.After(start) || end.Sub(start) > 24*time.Hour {
		return nil, fmt.Errorf("garmin_connect: sleeps: implausible night length")
	}

	md := map[string]any{"date": date}
	session := model.SleepSession{
		UserID:            userID,
		Provider:          Slug,
		ExternalID:        "sleep_" + date,
		BedtimeStart:      start,
		BedtimeEnd:        end,
		TotalSleepSeconds: sec(dto.SleepTimeSeconds),
		DeepSleepSeconds:  sec(dto.DeepSleepSeconds),
		LightSleepSeconds: sec(dto.LightSleepSeconds),
		REMSleepSeconds:   sec(dto.REMSleepSeconds),
		AwakeSeconds:      sec(dto.AwakeSleepSeconds),
		HRVAvgMs:          bounded(dto.AvgSleepHRV, 1, 500),
		SpO2Avg:           bounded(dto.AvgSpO2, 50, 100),
		RespiratoryRate:   bounded(dto.AverageRespirationValue, 1, 80),
		SleepScore:        bounded(dto.SleepScores.Overall.Value, 0, 100),
		Metadata:          md,
		Replaceable:       true,
	}
	session.ContentHash = hash(userID, DataTypeSleeps, "date", date)

	res := &model.MapResult{SleepSessions: []model.SleepSession{session}}
	for _, lv := range s.SleepLevels {
		stage, ok := sleepStages[int(lv.ActivityLevel)]
		if !ok || float64(int(lv.ActivityLevel)) != lv.ActivityLevel {
			continue
		}
		ps, err1 := parseGMT(lv.StartGMT)
		pe, err2 := parseGMT(lv.EndGMT)
		if err1 != nil || err2 != nil || !pe.After(ps) || pe.Sub(ps) > 24*time.Hour {
			continue
		}
		res.SleepPhases = append(res.SleepPhases, model.SleepPhase{
			UserID:            userID,
			Phase:             stage,
			StartDate:         ps,
			EndDate:           pe,
			DurationS:         int(pe.Sub(ps) / time.Second),
			ParentContentHash: session.ContentHash,
		})
	}
	if nightly := session.HRVAvgMs; nightly != nil {
		s := sample(userID, "hrv_"+date, model.QTHRVRMSSD, "ms", *nightly, start, end,
			map[string]any{model.MetadataSummaryDate: date})
		s.ContentHash = hash(userID, "sleep_hrv", "date", date)
		s.Replaceable = true
		res.QuantitySamples = append(res.QuantitySamples, s)
	}
	return res, nil
}

// mapHRV turns one Connect HRV summary into a nightly hrv_rmssd sample
// (lastNightAvg, milliseconds).
func mapHRV(line []byte, userID string) (*model.MapResult, error) {
	var h HRVLine
	if err := decode(DataTypeHRV, line, &h); err != nil {
		return nil, err
	}
	sum := h.HRVSummary
	if sum.CalendarDate == "" {
		return nil, nil
	}
	day, err := parseDate(sum.CalendarDate)
	if err != nil {
		return nil, fmt.Errorf("garmin_connect: hrv: %w", err)
	}
	if !inBounds(sum.LastNightAvg, 1, 500) {
		return nil, nil
	}
	date := sum.CalendarDate
	md := map[string]any{model.MetadataSummaryDate: date}
	setIf(md, "last_night_5min_high", sum.LastNight5MinHi)
	setIf(md, "weekly_avg", sum.WeeklyAvg)
	if sum.Status != "" {
		md["status"] = sum.Status
	}
	s := sample(userID, "hrv_night_"+date, model.QTHRVRMSSD, "ms", *sum.LastNightAvg, day, day.Add(24*time.Hour), md)
	s.ContentHash = hash(userID, DataTypeHRV, "date", date)
	s.Replaceable = true
	return &model.MapResult{QuantitySamples: []model.QuantitySample{s}}, nil
}

// mapActivity turns one Connect activity into a Workout. The workout is
// Replaceable and hashed on the Garmin activity id.
func mapActivity(line []byte, userID string) (*model.MapResult, error) {
	var a ActivityLine
	if err := decode(DataTypeActivities, line, &a); err != nil {
		return nil, err
	}
	if a.ActivityID == nil || *a.ActivityID <= 0 {
		return nil, fmt.Errorf("garmin_connect: activities: missing activityId")
	}
	id := strconv.FormatInt(*a.ActivityID, 10)
	start, err := parseGMT(a.StartTimeGMT)
	if err != nil {
		return nil, fmt.Errorf("garmin_connect: activities: %w", err)
	}
	act := activityType(a.ActivityType.TypeKey)
	md := map[string]any{"activity_type": act}
	if a.ActivityType.TypeKey != "" {
		md["garmin_activity_type"] = a.ActivityType.TypeKey
	}
	if a.ActivityName != "" {
		md["activity_name"] = a.ActivityName
	}
	w := model.Workout{
		UserID:       userID,
		Provider:     Slug,
		SourceSlug:   Slug,
		ExternalID:   id,
		ActivityType: act,
		StartDate:    start,
		EndDate:      start,
		Metadata:     md,
		Replaceable:  true,
		DeviceID:     deviceID(a.DeviceID),
	}
	if a.Duration != nil && *a.Duration >= 0 && *a.Duration < 1e7 {
		n := int(*a.Duration)
		w.DurationS = &n
		w.EndDate = start.Add(time.Duration(n) * time.Second)
	}
	if inRange(a.Calories, 5e4) {
		w.ActiveEnergyKcal = a.Calories
	}
	if inRange(a.Distance, 1e7) {
		w.DistanceM = a.Distance
	}
	if inBounds(a.AverageHR, 20, 250) {
		w.AvgHeartRate = a.AverageHR
	}
	if inBounds(a.MaxHR, 20, 250) {
		w.MaxHeartRate = a.MaxHR
	}
	if inRange(a.AverageSpeed, 1e3) {
		w.AvgSpeedMps = a.AverageSpeed
	}
	if inRange(a.ElevationGain, 1e5) {
		w.ElevationGainM = a.ElevationGain
	}
	w.ContentHash = hash(userID, DataTypeActivities, "external_id", id)
	return &model.MapResult{Workouts: []model.Workout{w}}, nil
}

// deviceID names the device of an activity ("Garmin 123456") from the numeric
// deviceId, or "" when Garmin names none.
func deviceID(id *int64) string {
	if id == nil || *id <= 0 {
		return ""
	}
	return "Garmin " + strconv.FormatInt(*id, 10)
}

// activityType maps a Connect activity typeKey ("running", "lap_swimming") to
// the canonical activity, "other" for an unknown one.
func activityType(key string) string {
	if a, ok := activityTypes[key]; ok {
		return a
	}
	return "other"
}

var activityTypes = map[string]string{
	"running":             "running",
	"treadmill_running":   "running",
	"trail_running":       "running",
	"indoor_running":      "running",
	"cycling":             "cycling",
	"road_biking":         "cycling",
	"mountain_biking":     "cycling",
	"gravel_cycling":      "cycling",
	"indoor_cycling":      "cycling",
	"virtual_ride":        "cycling",
	"walking":             "walking",
	"indoor_walking":      "walking",
	"hiking":              "hiking",
	"lap_swimming":        "swimming",
	"open_water_swimming": "swimming_open_water",
	"strength_training":   "traditional_strength_training",
	"indoor_cardio":       "mixed_cardio",
	"hiit":                "high_intensity_interval_training",
	"elliptical":          "elliptical",
	"stair_climbing":      "stair_climbing",
	"indoor_rowing":       "rowing",
	"rowing":              "rowing",
	"yoga":                "yoga",
	"pilates":             "pilates",
	"breathwork":          "mind_and_body",
	"tennis":              "tennis",
	"pickleball":          "pickleball",
}

// decode unmarshals one line into v, naming the data type on failure.
func decode(dataType string, line []byte, v any) error {
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("garmin_connect: %s: %w", dataType, err)
	}
	return nil
}

// setIf copies *v into md under key when v is set.
func setIf[T any](md map[string]any, key string, v *T) {
	if v != nil {
		md[key] = *v
	}
}

// sec returns a copy of a non-negative, plausible seconds value as *int.
func sec(v *int64) *int {
	if v == nil || *v < 0 || *v > 86400 {
		return nil
	}
	n := int(*v)
	return &n
}

// bounded returns v when it is set and in [lo, hi], else nil.
func bounded(v *float64, lo, hi float64) *float64 {
	if v == nil || math.IsNaN(*v) || *v < lo || *v > hi {
		return nil
	}
	x := *v
	return &x
}

// valid reports whether v is set and a finite number not below zero.
func valid(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0
}

// inRange reports whether v is set, finite and in [0, max].
func inRange(v *float64, max float64) bool { return valid(v) && *v <= max }

// inBounds reports whether v is set, finite and in [lo, hi].
func inBounds(v *float64, lo, hi float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= lo && *v <= hi
}

// sample builds one quantity sample of this connector; the caller sets the
// content hash and Replaceable.
func sample(userID, externalID, qt, unit string, v float64, start, end time.Time, md map[string]any) model.QuantitySample {
	return model.QuantitySample{
		UserID:          userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      sourceName,
		ExternalID:      externalID,
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       start,
		EndDate:         end,
		Metadata:        md,
	}
}

// hash is the content hash of a record of this connector.
func hash(userID, dataType string, kv ...string) string {
	m := map[string]string{"user_id": userID, "provider": Slug, "data_type": dataType}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return model.ComputeContentHash(m)
}

// copyMap returns a shallow copy of md.
func copyMap(md map[string]any) map[string]any {
	out := make(map[string]any, len(md))
	for k, v := range md {
		out[k] = v
	}
	return out
}

// parseDate parses a calendar date (YYYY-MM-DD) as a UTC midnight.
func parseDate(s string) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a date", s)
	}
	return t, nil
}

// gmtLayouts are the timestamp shapes Garmin's Connect endpoints use, all in
// GMT (no zone suffix), newest first.
var gmtLayouts = []string{
	"2006-01-02T15:04:05.0",
	"2006-01-02T15:04:05.000",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.0",
	"2006-01-02 15:04:05",
}

// parseGMT parses one of the Connect GMT timestamp shapes as UTC.
func parseGMT(s string) (time.Time, error) {
	for _, l := range gmtLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("garmin_connect: %q is not a GMT timestamp", s)
}

// utcDay is the start of t's UTC day.
func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// sortDays returns the keys of byDay in day order.
func sortDays(byDay map[time.Time][][]byte) []time.Time {
	days := make([]time.Time, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	return days
}
