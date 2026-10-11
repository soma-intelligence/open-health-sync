// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"fmt"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapWorkout turns one workout document into a Workout. The activity is put
// in the vocabulary the Apple Health connector uses ("other" when Oura's name
// has no counterpart); Oura's own name, intensity, label and source
// (manual, autodetected, confirmed, workout_heart_rate) stay in metadata.
//
// The workout is hashed on its Oura id and start: a re-sent workout is a
// duplicate, and the writer keeps its first version.
func mapWorkout(line []byte, userID string) (*model.MapResult, error) {
	var w Workout
	if err := decode(DataTypeWorkout, line, &w); err != nil {
		return nil, err
	}
	if w.ID == "" {
		return nil, fmt.Errorf("oura: workout: missing id")
	}
	startLocal, err := parseTime(w.StartDatetime)
	if err != nil {
		return nil, fmt.Errorf("oura: workout: start_datetime: %w", err)
	}
	endLocal, err := parseTime(w.EndDatetime)
	if err != nil {
		return nil, fmt.Errorf("oura: workout: end_datetime: %w", err)
	}
	start, end := startLocal.UTC(), endLocal.UTC()
	if end.Before(start) {
		return nil, fmt.Errorf("oura: workout: end_datetime before start_datetime")
	}
	dur := int(end.Sub(start).Seconds())

	activity := workoutActivity(w.Activity)
	md := map[string]any{
		"activity_type": activity,
		"oura_activity": w.Activity,
	}
	if w.Intensity != "" {
		md["intensity"] = w.Intensity
	}
	if w.Source != "" {
		md["oura_source"] = w.Source
	}
	if w.Label != nil && *w.Label != "" {
		md["label"] = *w.Label
	}
	zoneMetadata(md, startLocal)

	out := model.Workout{
		UserID:           userID,
		Provider:         Slug,
		ExternalID:       w.ID,
		ActivityType:     activity,
		StartDate:        start,
		EndDate:          end,
		DurationS:        &dur,
		ActiveEnergyKcal: w.Calories,
		DistanceM:        w.Distance,
		Metadata:         md,
	}
	out.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":     userID,
		"provider":    Slug,
		"start_date":  start.Format(time.RFC3339),
		"external_id": w.ID,
	})
	return &model.MapResult{Workouts: []model.Workout{out}}, nil
}

// workoutActivity maps an Oura activity name to the canonical activity
// ("other" for a name without a counterpart). Oura names are lower case
// words; a camelCase or spaced spelling is normalized first.
func workoutActivity(name string) string {
	key := strings.ReplaceAll(strings.TrimSpace(name), " ", "_")
	key = model.CanonicalQuantityType(key) // camelCase -> snake_case
	key = strings.ToLower(key)
	if a, ok := workoutAliases[key]; ok {
		return a
	}
	if canonicalActivities[key] {
		return key
	}
	return "other"
}

// workoutAliases are the Oura names spelled differently from the canonical
// activity.
var workoutAliases = map[string]string{
	"strength_training": "traditional_strength_training",
	"weight_training":   "traditional_strength_training",
	"hiit":              "high_intensity_interval_training",
	"dancing":           "dance",
	"skiing":            "downhill_skiing",
	"cross_country":     "cross_country_skiing",
	"stair_exercise":    "stair_climbing",
	"indoor_cycling":    "cycling",
	"mountain_biking":   "cycling",
	"jogging":           "running",
	"trail_running":     "running",
	"open_water":        "swimming_open_water",
	"skateboarding":     "skating_sports",
	"ice_skating":       "skating_sports",
	"meditation":        "mind_and_body",
	"stretching":        "flexibility",
}

// canonicalActivities is the part of the canonical activity vocabulary (the
// Apple Health one) an Oura name can match as is.
var canonicalActivities = map[string]bool{
	"badminton": true, "baseball": true, "basketball": true, "boxing": true,
	"climbing": true, "cricket": true, "cross_country_skiing": true, "cross_training": true,
	"cycling": true, "dance": true, "downhill_skiing": true, "elliptical": true,
	"fencing": true, "flexibility": true, "functional_strength_training": true,
	"golf": true, "gymnastics": true, "handball": true, "hiking": true, "hockey": true,
	"high_intensity_interval_training": true, "jump_rope": true, "kickboxing": true,
	"martial_arts": true, "mixed_cardio": true, "paddle_sports": true, "pickleball": true,
	"pilates": true, "rowing": true, "rugby": true, "running": true, "sailing": true,
	"skating_sports": true, "snowboarding": true, "soccer": true, "softball": true,
	"squash": true, "stair_climbing": true, "surfing_sports": true, "swimming": true,
	"table_tennis": true, "tai_chi": true, "tennis": true, "traditional_strength_training": true,
	"volleyball": true, "walking": true, "water_polo": true, "wrestling": true, "yoga": true,
}
