// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapWorkout transforms a raw Apple Health workout record into a canonical Workout.
func (m *mapper) mapWorkout(record map[string]any, userID string) (*model.MapResult, error) {
	// Extract workout activity type (integer)
	activityTypeRaw, _ := record["workoutActivityType"].(float64)
	activityTypeInt := int(activityTypeRaw)

	activityType, ok := workoutActivityTypes[activityTypeInt]
	if !ok {
		activityType = "other"
	}

	startDate, endDate, tz, err := ParseTimestamp(record)
	if err != nil {
		return nil, fmt.Errorf("workout: %w", err)
	}

	// Extract optional numeric fields. Two exporter generations exist and
	// both shapes stay replayable from the raw store: the SDK spells
	// duration "durationS", an older exporter spells it "duration".
	// Accepting only the latter would leave duration_s NULL.
	duration := optionalFloat(record, "durationS", "duration")
	totalEnergy := optionalFloat(record, "totalEnergyBurned", "activeEnergyKcal")
	totalDistance := optionalFloat(record, "totalDistance", "distanceM")
	avgHR := optionalFloat(record, "averageHeartRate", "avgHeartRate")
	maxHR := optionalFloat(record, "maxHeartRate")

	// Speed and elevation arrive either as typed fields from the exporter or,
	// in older raw objects, only as HKQuantity descriptions
	// ("1.25 m/s") inside the HealthKit metadata dict. Promote them to their
	// own columns so reports stop parsing the JSON blob.
	avgSpeed := optionalFloat(record, "avgSpeedMps")
	if avgSpeed == nil {
		avgSpeed = optionalHKQuantity(record, "HKAverageSpeed")
	}
	elevationGain := optionalFloat(record, "elevationGainM")
	if elevationGain == nil {
		elevationGain = optionalHKQuantity(record, "HKElevationAscended")
	}

	// Convert duration to int seconds
	var durationS *int
	if duration != nil {
		d := int(*duration)
		durationS = &d
	}

	// Build metadata preserving raw fields
	metadata := buildMetadata(record, map[string]any{
		"workout_activity_type_raw": activityTypeInt,
	})
	if tz != "" {
		metadata["timezone"] = tz
	}
	// Preserve route path for the ETL
	if routePath, ok := record["routePath"].(string); ok {
		metadata["routePath"] = routePath
	}
	// Preserve energy and distance units
	if u, ok := record["totalEnergyBurnedUnit"].(string); ok {
		metadata["totalEnergyBurnedUnit"] = u
	}
	if u, ok := record["totalDistanceUnit"].(string); ok {
		metadata["totalDistanceUnit"] = u
	}
	// Preserve min heart rate
	if minHR, ok := record["minHeartRate"].(float64); ok {
		metadata["minHeartRate"] = minHR
	}

	workout := model.Workout{
		UserID:           userID,
		Provider:         "apple_health",
		ExternalID:       sampleExternalID(record),
		ActivityType:     activityType,
		StartDate:        startDate,
		EndDate:          endDate,
		DurationS:        durationS,
		ActiveEnergyKcal: totalEnergy,
		DistanceM:        totalDistance,
		AvgHeartRate:     avgHR,
		MaxHeartRate:     maxHR,
		AvgSpeedMps:      avgSpeed,
		ElevationGainM:   elevationGain,
		Metadata:         metadata,
	}

	workout.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       userID,
		"provider":      "apple_health",
		"activity_type": activityType,
		"start_date":    startDate.UTC().Format(time.RFC3339Nano),
		"end_date":      endDate.UTC().Format(time.RFC3339Nano),
	})

	return &model.MapResult{
		Workouts: []model.Workout{workout},
	}, nil
}

// optionalFloat extracts a float64 pointer from the first of keys that holds a
// number. Returns nil if none of them is present or numeric. Several key
// spellings are accepted because the raw store is the replay source of truth
// and holds files from more than one generation of the iOS exporter.
func optionalFloat(record map[string]any, keys ...string) *float64 {
	for _, key := range keys {
		v, ok := record[key]
		if !ok {
			continue
		}
		switch val := v.(type) {
		case float64:
			return &val
		case int:
			f := float64(val)
			return &f
		}
	}
	return nil
}

// optionalHKQuantity reads key out of the record's HealthKit metadata dict and
// parses its HKQuantity description ("1.25 m/s", "10.5 m") into an SI value.
func optionalHKQuantity(record map[string]any, key string) *float64 {
	md, ok := record["metadata"].(map[string]any)
	if !ok {
		return nil
	}
	s, ok := md[key].(string)
	if !ok {
		return nil
	}
	v, ok := parseHKQuantityString(s)
	if !ok {
		return nil
	}
	return &v
}

// hkUnitToSI converts a HealthKit unit suffix to its SI base: metres for
// lengths, metres per second for speeds. A quantity carrying any other unit is
// rejected rather than stored under the wrong one.
var hkUnitToSI = map[string]float64{
	"":     1,
	"m":    1,
	"m/s":  1,
	"km":   1000,
	"cm":   0.01,
	"mi":   1609.344,
	"ft":   0.3048,
	"yd":   0.9144,
	"km/h": 1.0 / 3.6,
	"mph":  0.44704,
}

// parseHKQuantityString parses a HealthKit quantity description of the form
// "<number> <unit>" and returns the value converted to its SI base unit.
// HealthKit formats these with a plain ASCII decimal point regardless of the
// device locale, so a plain ParseFloat is correct here.
func parseHKQuantityString(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}

	numPart, unitPart := s, ""
	if i := strings.IndexByte(s, ' '); i >= 0 {
		numPart, unitPart = s[:i], strings.TrimSpace(s[i+1:])
	}

	value, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, false
	}
	factor, ok := hkUnitToSI[unitPart]
	if !ok {
		return 0, false
	}
	return value * factor, true
}
