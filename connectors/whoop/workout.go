// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"fmt"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapWorkout turns one workout into a Workout plus a workout_strain sample.
// WHOOP's kilojoules become active_energy_kcal, the field every connector
// fills with a workout's energy. Like a sleep, the workout is hashed on its
// WHOOP id alone and is Replaceable: a PENDING_SCORE workout is stored with
// its times and sport, and its scored (or re-scored) copy replaces the
// figures.
func mapWorkout(line []byte, userID string) (*model.MapResult, error) {
	var w Workout
	if err := decode(DataTypeWorkout, line, &w); err != nil {
		return nil, err
	}
	if w.ID == "" {
		return nil, fmt.Errorf("whoop: workout: missing id")
	}
	start, err := requireTime(DataTypeWorkout, "start", w.Start)
	if err != nil {
		return nil, err
	}
	end, err := requireTime(DataTypeWorkout, "end", w.End)
	if err != nil {
		return nil, err
	}
	id := string(w.ID)
	dur := int(end.Sub(start).Seconds())

	sport := whoopSport(w.SportName)
	md := map[string]any{
		"activity_type": sport.activity,
		"sport_name":    w.SportName,
		"score_state":   w.ScoreState,
	}
	if sport.indoor {
		md["indoor"] = true
	}
	setIf(md, "sport_id", w.SportID)

	out := model.Workout{
		UserID:       userID,
		Provider:     Slug,
		ExternalID:   id,
		ActivityType: sport.activity,
		StartDate:    start,
		EndDate:      end,
		DurationS:    &dur,
		Replaceable:  true,
	}
	var strain *float64
	if sc := w.Score; sc != nil && w.ScoreState == scoreScored {
		strain = sc.Strain
		if sc.Kilojoule != nil {
			out.ActiveEnergyKcal = ptrFloat(*sc.Kilojoule * kcalPerKilojoule)
		}
		out.DistanceM = sc.DistanceMeter
		out.AvgHeartRate = sc.AverageHeartRate
		out.MaxHeartRate = sc.MaxHeartRate
		out.ElevationGainM = sc.AltitudeGainMeter
		setIf(md, "strain", sc.Strain)
		setIf(md, "kilojoule", sc.Kilojoule)
		setIf(md, "percent_recorded", sc.PercentRecorded)
		setIf(md, "altitude_change_meter", sc.AltitudeChangeMeter)
		if sc.ZoneDurations != nil {
			md["zone_durations"] = sc.ZoneDurations
		}
	}
	out.Metadata = withTimezone(md, w.TimezoneOffset)
	out.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":     userID,
		"provider":    Slug,
		"external_id": id,
	})

	res := &model.MapResult{Workouts: []model.Workout{out}}
	if strain != nil {
		m := withTimezone(map[string]any{"whoop_workout_id": id, "activity_type": sport.activity}, w.TimezoneOffset)
		res.QuantitySamples = append(res.QuantitySamples,
			quantity(userID, id, model.QTWorkoutStrain, *strain, "index", start, end, m))
	}
	return res, nil
}

// workoutType is the canonical activity of a WHOOP sport, in the vocabulary
// the Apple Health connector uses, and whether it is done indoors.
type workoutType struct {
	activity string
	indoor   bool
}

// whoopSport maps a WHOOP sport_name to the canonical activity name ("other"
// for an unknown sport). The name is matched lowercased, with spaces,
// hyphens and slashes read as underscores. sport_name and sport_id stay in
// the metadata either way.
func whoopSport(name string) workoutType {
	key := strings.NewReplacer(" ", "_", "-", "_", "/", "_").Replace(strings.ToLower(strings.TrimSpace(name)))
	if t, ok := sports[key]; ok {
		return t
	}
	return workoutType{activity: "other"}
}

var sports = map[string]workoutType{
	"running": {activity: "running"}, "track_&_field": {activity: "running"},
	"cycling": {activity: "cycling"}, "mountain_biking": {activity: "cycling"},
	"spin": {activity: "cycling", indoor: true}, "assault_bike": {activity: "cycling", indoor: true},
	"swimming": {activity: "swimming"}, "walking": {activity: "walking"},
	"hiking": {activity: "hiking"}, "hiking_rucking": {activity: "hiking"}, "rucking": {activity: "hiking"},
	"rowing": {activity: "rowing"}, "yoga": {activity: "yoga"}, "hot_yoga": {activity: "yoga"},
	"pilates":       {activity: "pilates"},
	"weightlifting": {activity: "traditional_strength_training"}, "powerlifting": {activity: "traditional_strength_training"},
	"strength_trainer":   {activity: "traditional_strength_training"},
	"functional_fitness": {activity: "functional_strength_training"},
	"hiit":               {activity: "high_intensity_interval_training"}, "crossfit": {activity: "cross_training"},
	"elliptical": {activity: "elliptical"}, "stairmaster": {activity: "stair_climbing"},
	"rock_climbing": {activity: "climbing"}, "climbing": {activity: "climbing"},
	"tennis": {activity: "tennis"}, "squash": {activity: "squash"}, "badminton": {activity: "badminton"},
	"table_tennis": {activity: "table_tennis"}, "pickleball": {activity: "pickleball"},
	"golf": {activity: "golf"}, "boxing": {activity: "boxing"}, "kickboxing": {activity: "kickboxing"},
	"martial_arts": {activity: "martial_arts"}, "jiu_jitsu": {activity: "martial_arts"},
	"wrestling": {activity: "wrestling"}, "fencing": {activity: "fencing"},
	"basketball": {activity: "basketball"}, "soccer": {activity: "soccer"},
	"football": {activity: "american_football"}, "rugby": {activity: "rugby"},
	"australian_football": {activity: "australian_football"},
	"volleyball":          {activity: "volleyball"}, "baseball": {activity: "baseball"}, "softball": {activity: "softball"},
	"ice_hockey": {activity: "hockey"}, "field_hockey": {activity: "hockey"}, "lacrosse": {activity: "lacrosse"},
	"cricket": {activity: "cricket"}, "water_polo": {activity: "water_polo"},
	"skiing": {activity: "downhill_skiing"}, "cross_country_skiing": {activity: "cross_country_skiing"},
	"snowboarding": {activity: "snowboarding"}, "ice_skating": {activity: "skating_sports"},
	"inline_skating": {activity: "skating_sports"}, "skateboarding": {activity: "skating_sports"},
	"surfing": {activity: "surfing_sports"}, "kayaking": {activity: "paddle_sports"},
	"canoeing": {activity: "paddle_sports"}, "paddleboarding": {activity: "paddle_sports"},
	"dance": {activity: "dance"}, "barre": {activity: "dance"}, "jumping_rope": {activity: "jump_rope"},
	"meditation": {activity: "mind_and_body"}, "stretching": {activity: "flexibility"},
	"activity": {activity: "other"},
}
