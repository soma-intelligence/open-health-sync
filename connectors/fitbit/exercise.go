// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapExercise turns one exercise session into a Workout. Distances and
// elevation come in millimeters (divided by 1000 for meters), speed in
// millimeters per second (divided by 1000 for m/s). caloriesKcal is the
// energy Google counts for the session; it fills ActiveEnergyKcal as for the
// other wearables. The duration is activeDuration (pauses excluded) when
// given, else the session's length. The exercise type is put in the
// canonical activity vocabulary ("other" without a counterpart); Google's
// type and display name stay in metadata, with the run VO2 max, steps, active
// zone minutes and swim lengths.
//
// The workout is identified by its data point id and start, never its
// values, and is Replaceable: an edited session replaces the stored one.
func mapExercise(p point, userID string) (*model.MapResult, error) {
	var v exerciseValue
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	md := p.metadata()
	start, end, err := spanOf(v.Interval, md)
	if err != nil {
		return nil, err
	}
	id := p.recordID()
	if id == "" {
		id = fmt.Sprintf("exercise_%d", start.Unix())
	}
	activity := exerciseActivity(v.ExerciseType)
	md["activity_type"] = activity
	if v.ExerciseType != "" {
		md["google_exercise_type"] = v.ExerciseType
	}
	if v.DisplayName != "" {
		md["display_name"] = v.DisplayName
	}
	dur := int(end.Sub(start).Seconds())
	if v.ActiveDuration != "" {
		if d, err := parseDuration(v.ActiveDuration); err == nil && d >= 0 {
			md["elapsed_seconds"] = dur
			dur = int(d.Seconds())
		}
	}
	w := model.Workout{
		UserID:       userID,
		Provider:     Slug,
		ExternalID:   id,
		ActivityType: activity,
		StartDate:    start,
		EndDate:      end,
		DurationS:    &dur,
		Metadata:     md,
		Replaceable:  true,
	}
	scaled := func(n *num, div float64) *float64 {
		x, ok := val(n)
		if !ok || x < 0 {
			return nil
		}
		x /= div
		return &x
	}
	if m := v.MetricsSummary; m != nil {
		w.ActiveEnergyKcal = scaled(m.CaloriesKcal, 1)
		w.DistanceM = scaled(m.DistanceMillimeters, 1000)
		w.ElevationGainM = scaled(m.ElevationGainMillimeters, 1000)
		w.AvgSpeedMps = scaled(m.AverageSpeedMillimetersPerSecond, 1000)
		if hr := scaled(m.AverageHeartRateBeatsPerMinute, 1); hr != nil && *hr > 0 {
			w.AvgHeartRate = hr
		}
		for k, n := range map[string]*num{
			"run_vo2_max":         m.RunVO2Max,
			"steps":               m.Steps,
			"active_zone_minutes": m.ActiveZoneMinutes,
			"total_swim_lengths":  m.TotalSwimLengths,
		} {
			if x, ok := val(n); ok {
				md[k] = x
			}
		}
	}
	if em := v.ExerciseMetadata; em != nil && em.HasGPS != nil {
		md["has_gps"] = *em.HasGPS
	}
	w.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":     userID,
		"provider":    Slug,
		"start_date":  start.Format(time.RFC3339),
		"external_id": id,
	})
	return &model.MapResult{Workouts: []model.Workout{w}}, nil
}

// exerciseActivity maps a Google exercise type to the canonical activity (the
// Apple Health vocabulary), "other" when it has none.
func exerciseActivity(t string) string {
	if a, ok := exerciseTypes[t]; ok {
		return a
	}
	return "other"
}

// exerciseTypes maps Exercise.ExerciseType values to canonical activities.
// Types without a fitting activity (chores, gardening, motor sports, fishing
// gear...) are left out and map to "other".
var exerciseTypes = map[string]string{
	"AEROBIC_WORKOUT":              "mixed_cardio",
	"ARCHERY":                      "archery",
	"ASSAULT_BIKE":                 "cycling",
	"BACKPACKING":                  "hiking",
	"BADMINTON":                    "badminton",
	"BALLET":                       "dance",
	"BALLROOM_DANCE":               "social_dance",
	"BARRE_CLASS":                  "barre",
	"BASEBALL":                     "baseball",
	"BASKETBALL":                   "basketball",
	"BIKING":                       "cycling",
	"BODY_WEIGHT":                  "functional_strength_training",
	"BOOTCAMP":                     "cross_training",
	"BOWLING":                      "bowling",
	"BOXING":                       "boxing",
	"BREAKDANCING":                 "dance",
	"CALISTHENICS":                 "functional_strength_training",
	"CANOEING":                     "paddle_sports",
	"CARDIO_SCULPT":                "mixed_cardio",
	"CARDIO_WORKOUT":               "mixed_cardio",
	"CIRCUIT_TRAINING":             "cross_training",
	"CLIMBING":                     "climbing",
	"CORE_TRAINING":                "core_training",
	"CRICKET":                      "cricket",
	"CROSS_COUNTRY_SKI":            "cross_country_skiing",
	"CROSS_TRAINING":               "cross_training",
	"CROSSFIT":                     "cross_training",
	"CURLING":                      "curling",
	"DANCING":                      "dance",
	"DIVING":                       "underwater_diving",
	"ELECTRIC_BIKE":                "cycling",
	"ELLIPTICAL":                   "elliptical",
	"EQUESTRIAN_SPORTS":            "equestrian_sports",
	"EXERCISE_CLASS":               "mixed_cardio",
	"FENCING":                      "fencing",
	"FIELD_HOCKEY":                 "hockey",
	"FISHING":                      "fishing",
	"FITNESS_GAMING":               "fitness_gaming",
	"FOOTBALL_AMERICAN":            "american_football",
	"FOOTBALL_AUSTRALIAN":          "australian_football",
	"FREE_WEIGHTS":                 "traditional_strength_training",
	"FRISBEE_PLAYING_GENERAL":      "disc_sports",
	"FUNCTIONAL_STRENGTH_TRAINING": "functional_strength_training",
	"GOLF":                         "golf",
	"GYMNASTICS":                   "gymnastics",
	"HANDBALL":                     "handball",
	"HAND_CYCLING":                 "hand_cycling",
	"HIIT":                         "high_intensity_interval_training",
	"HIKING":                       "hiking",
	"HIP_HOP":                      "dance",
	"HOCKEY":                       "hockey",
	"HUNTING":                      "hunting",
	"ICE_SKATING":                  "skating_sports",
	"INCLINE_RUN":                  "running",
	"INCLINE_WALK":                 "walking",
	"INDOOR_CLIMBING":              "climbing",
	"INTERVAL_WORKOUT":             "high_intensity_interval_training",
	"JAZZ_DANCE":                   "dance",
	"JIU_JITSU":                    "martial_arts",
	"JUMPING_ROPE":                 "jump_rope",
	"KARATE":                       "martial_arts",
	"KAYAKING":                     "paddle_sports",
	"KICKBOXING":                   "kickboxing",
	"KITESURFING":                  "water_sports",
	"LACROSSE":                     "lacrosse",
	"MARTIAL_ARTS":                 "martial_arts",
	"MEDITATE":                     "mind_and_body",
	"MODERN_DANCE":                 "dance",
	"MOUNTAIN_BIKE":                "cycling",
	"MUAY_THAI":                    "kickboxing",
	"NORDIC_WALKING":               "walking",
	"OUTDOOR_BIKE":                 "cycling",
	"PADDLEBOARDING":               "paddle_sports",
	"PADEL":                        "racquetball",
	"PICKELBALL":                   "pickleball", // sic, the API's spelling
	"PICKLEBALL":                   "pickleball",
	"PILATES":                      "pilates",
	"POWERLIFTING":                 "traditional_strength_training",
	"POWER_WALKING":                "walking",
	"RACQUETBALL":                  "racquetball",
	"RESISTANCE_BANDS":             "functional_strength_training",
	"ROCK_CLIMBING":                "climbing",
	"ROLLERBLADING":                "skating_sports",
	"ROLLER_SKATING":               "skating_sports",
	"ROWING":                       "rowing",
	"ROWING_MACHINE":               "rowing",
	"RUCKING":                      "walking",
	"RUGBY":                        "rugby",
	"RUNNING":                      "running",
	"SAILING":                      "sailing",
	"SCUBA_DIVING":                 "underwater_diving",
	"SKATEBOARDING":                "skating_sports",
	"SKATING":                      "skating_sports",
	"SKIING":                       "downhill_skiing",
	"SNOWBOARDING":                 "snowboarding",
	"SNOW_SPORT":                   "snow_sports",
	"SNOWSHOEING":                  "snow_sports",
	"SOCCER":                       "soccer",
	"SOFTBALL":                     "softball",
	"SPEED_SKATING":                "skating_sports",
	"SPINNING":                     "cycling",
	"SQUASH":                       "squash",
	"STAIRCLIMBER":                 "stair_climbing",
	"STATIONARY_BIKE":              "cycling",
	"STEP_TRAINING":                "step_training",
	"STRENGTH_TRAINING":            "traditional_strength_training",
	"STRETCHING":                   "flexibility",
	"STROLLER_WALK":                "walking",
	"SURFING":                      "surfing_sports",
	"SWIMMING":                     "swimming",
	"SWIMMING_OPEN_WATER":          "swimming_open_water",
	"SWIMMING_POOL":                "swimming",
	"SYNCHRONIZED_SWIMMING":        "swimming",
	"TABATA_WORKOUT":               "high_intensity_interval_training",
	"TABLE_TENNIS":                 "table_tennis",
	"TAEKWONDO":                    "martial_arts",
	"TAI_CHI":                      "tai_chi",
	"TANGO":                        "social_dance",
	"TENNIS":                       "tennis",
	"TRACK_AND_FIELD":              "track_and_field",
	"TRAIL_RUN":                    "running",
	"TREADMILL":                    "running",
	"TREADMILL_WALK":               "walking",
	"TRX":                          "functional_strength_training",
	"ULTIMATE_FRISBEE":             "disc_sports",
	"VOLLEYBALL":                   "volleyball",
	"VOLLEYBALL_BEACH":             "volleyball",
	"WAKEBOARDING":                 "water_sports",
	"WALKING":                      "walking",
	"WALK_WITH_WEIGHTS":            "walking",
	"WATER_AEROBICS":               "water_fitness",
	"WATER_JOGGING":                "water_fitness",
	"WATER_POLO":                   "water_polo",
	"WATER_SKIING":                 "water_sports",
	"WATER_SPORT":                  "water_sports",
	"WATER_VOLLEYBALL":             "volleyball",
	"WEIGHTLIFTING":                "traditional_strength_training",
	"WEIGHT_MACHINES":              "traditional_strength_training",
	"WEIGHTS":                      "traditional_strength_training",
	"WHEELCHAIR":                   "wheelchair_walk_pace",
	"WINDSURFING":                  "water_sports",
	"WRESTLING":                    "wrestling",
	"YOGA":                         "yoga",
	"YOGA_BIKRAM":                  "yoga",
	"YOGA_HATHA":                   "yoga",
	"YOGA_POWER":                   "yoga",
	"YOGA_VINYASA":                 "yoga",
	"ZUMBA":                        "dance",
}
