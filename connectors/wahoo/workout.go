// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

import (
	"fmt"
	"math"
	"strings"
	"time"

	// Embed the zone database so LoadLocation works in minimal images.
	_ "time/tzdata"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapWorkoutLine turns one pulled workout, its summary embedded, into a
// Workout.
func mapWorkoutLine(line []byte, userID string) (*model.MapResult, error) {
	var w Workout
	if err := decode(DataTypeWorkout, line, &w); err != nil {
		return nil, err
	}
	return mapWorkout(w, w.Summary, userID)
}

// mapWebhook turns one stored workout_summary webhook, where the summary
// carries its workout, into the same Workout a pull gives.
func mapWebhook(line []byte, userID string) (*model.MapResult, error) {
	var b webhookBody
	if err := decode(DataTypeWebhook, line, &b); err != nil {
		return nil, err
	}
	if b.WorkoutSummary == nil || b.WorkoutSummary.Workout == nil {
		return nil, fmt.Errorf("wahoo: webhook: no workout_summary.workout")
	}
	return mapWorkout(*b.WorkoutSummary.Workout, b.WorkoutSummary, userID)
}

// mapWorkout builds the Workout of w and its summary s (nil when Wahoo has
// none yet: the workout then carries only its planned minutes).
//
// The workout is Replaceable and hashed on the Wahoo workout id and start: a
// summary that arrives or changes later (an edit, a webhook after a pull)
// replaces the stored figures instead of adding a second workout. The name is
// the user's free text and is not copied; neither is the FIT file link.
func mapWorkout(w Workout, s *Summary, userID string) (*model.MapResult, error) {
	externalID := w.ID.String()
	if externalID == "" {
		return nil, fmt.Errorf("wahoo: workout: missing id")
	}
	start, err := time.Parse(time.RFC3339, w.Starts)
	if err != nil {
		return nil, fmt.Errorf("wahoo: workout %s: starts is not RFC 3339", externalID)
	}
	start = start.UTC()

	activity, indoor, typeName := "other", false, ""
	md := map[string]any{}
	if w.WorkoutTypeID != nil {
		md["wahoo_workout_type_id"] = *w.WorkoutTypeID
		if t, ok := workoutTypes[*w.WorkoutTypeID]; ok {
			activity, indoor, typeName = t.activity, t.indoor, t.name
		}
	}
	md["activity_type"] = activity
	if typeName != "" {
		md["wahoo_workout_type"] = typeName
	}
	if indoor {
		md["indoor"] = true
	}
	if w.PlanID.String() != "" {
		md["wahoo_plan_id"] = w.PlanID.String()
	}

	end := start
	var dur *int
	if m := w.Minutes.val(); m != nil && *m > 0 && *m < maxMinutes {
		d := int(math.Round(*m * 60))
		dur = &d
		end = start.Add(time.Duration(d) * time.Second)
	}
	out := model.Workout{
		UserID:       userID,
		Provider:     Slug,
		SourceSlug:   Slug,
		ExternalID:   externalID,
		ActivityType: activity,
		StartDate:    start,
		Metadata:     md,
		Replaceable:  true,
	}
	if s != nil {
		if sid := s.ID.String(); sid != "" {
			md["wahoo_summary_id"] = sid
		}
		if total := seconds(s.DurationTotalAccum); total != nil {
			end = start.Add(time.Duration(*total) * time.Second)
			md["elapsed_time_s"] = *total
			dur = total
		}
		if active := seconds(s.DurationActiveAccum); active != nil {
			md["moving_time_s"] = *active
			if s.DurationTotalAccum == nil {
				end = start.Add(time.Duration(*active) * time.Second)
			}
			dur = active
		}
		if paused := seconds(s.DurationPausedAccum); paused != nil {
			md["paused_time_s"] = *paused
		}
		out.ActiveEnergyKcal = positive(s.CaloriesAccum)
		out.DistanceM = positive(s.DistanceAccum)
		out.AvgHeartRate = inRange(s.HeartRateAvg, 20, 250)
		out.AvgSpeedMps = positive(s.SpeedAvg)
		out.ElevationGainM = positive(s.AscentAccum)
		putFloat(md, "average_watts", positive(s.PowerAvg))
		putFloat(md, "normalized_power_w", positive(s.PowerBikeNPLast))
		putFloat(md, "training_stress_score", positive(s.PowerBikeTSSLast))
		putFloat(md, "average_cadence_rpm", positive(s.CadenceAvg))
		putFloat(md, "work_j", positive(s.WorkAccum))
		if tz := zone(s.TimeZone); tz != "" {
			md["timezone"] = tz
		}
		if s.Manual != nil {
			md["manual"] = *s.Manual
		}
		if s.Edited != nil {
			md["edited"] = *s.Edited
		}
		if s.FitnessAppID != nil {
			md["wahoo_fitness_app_id"] = *s.FitnessAppID
		}
	}
	out.EndDate = end
	out.DurationS = dur
	out.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":     userID,
		"provider":    Slug,
		"start_date":  start.Format(time.RFC3339),
		"external_id": externalID,
	})
	return &model.MapResult{Workouts: []model.Workout{out}}, nil
}

// maxMinutes bounds a workout's minutes (a week): beyond it the figure is
// not a workout length.
const maxMinutes = 7 * 24 * 60

// seconds returns a duration figure as whole seconds, nil when it is missing,
// negative or longer than a week.
func seconds(f *flexFloat) *int {
	v := f.val()
	if v == nil || *v < 0 || *v > maxMinutes*60 {
		return nil
	}
	s := int(math.Round(*v))
	return &s
}

// positive drops a missing, zero or negative figure: a workout that did not
// move has no distance, and a missing strap gives no heart rate.
func positive(f *flexFloat) *float64 {
	v := f.val()
	if v == nil || *v <= 0 {
		return nil
	}
	return v
}

// inRange keeps a figure within (lo, hi].
func inRange(f *flexFloat, lo, hi float64) *float64 {
	v := f.val()
	if v == nil || *v <= lo || *v > hi {
		return nil
	}
	return v
}

func putFloat(md map[string]any, key string, v *float64) {
	if v != nil {
		md[key] = *v
	}
}

// zone returns an IANA zone name Go knows, or "".
func zone(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ""
	}
	return name
}

// workoutType is one row of Wahoo's workout type table.
type workoutType struct {
	name     string
	activity string
	indoor   bool
}

// workoutTypes maps workout_type_id (Wahoo's "Workout Types" table) to the
// canonical activity and whether its location is INDOOR. 255 (UNKNOWN) and
// any id not listed give "other".
var workoutTypes = map[int]workoutType{
	0:  {"BIKING", "cycling", false},
	1:  {"RUNNING", "running", false},
	2:  {"FE", "other", true},
	3:  {"RUNNING_TRACK", "running", false},
	4:  {"RUNNING_TRAIL", "running", false},
	5:  {"RUNNING_TREADMILL", "running", true},
	6:  {"WALKING", "walking", false},
	7:  {"WALKING_SPEED", "walking", false},
	8:  {"WALKING_NORDIC", "walking", false},
	9:  {"HIKING", "hiking", false},
	10: {"MOUNTAINEERING", "climbing", false},
	11: {"BIKING_CYCLECROSS", "cycling", false},
	12: {"BIKING_INDOOR", "cycling", true},
	13: {"BIKING_MOUNTAIN", "cycling", false},
	14: {"BIKING_RECUMBENT", "cycling", false},
	15: {"BIKING_ROAD", "cycling", false},
	16: {"BIKING_TRACK", "cycling", false},
	17: {"BIKING_MOTOCYCLING", "other", false},
	18: {"FE_GENERAL", "other", true},
	19: {"FE_TREADMILL", "running", true},
	20: {"FE_ELLIPTICAL", "elliptical", true},
	21: {"FE_BIKE", "cycling", true},
	22: {"FE_ROWER", "rowing", true},
	23: {"FE_CLIMBER", "stair_climbing", true},
	25: {"SWIMMING_LAP", "swimming", true},
	26: {"SWIMMING_OPEN_WATER", "swimming", false},
	27: {"SNOWBOARDING", "snowboarding", false},
	28: {"SKIING", "snow_sports", false},
	29: {"SKIING_DOWNHILL", "downhill_skiing", false},
	30: {"SKIINGCROSS_COUNTRY", "cross_country_skiing", false},
	31: {"SKATING", "skating_sports", false},
	32: {"SKATING_ICE", "skating_sports", true},
	33: {"SKATING_INLINE", "skating_sports", true},
	34: {"LONG_BOARDING", "skating_sports", false},
	35: {"SAILING", "sailing", false},
	36: {"WINDSURFING", "surfing_sports", false},
	37: {"CANOEING", "paddle_sports", false},
	38: {"KAYAKING", "paddle_sports", false},
	39: {"ROWING", "rowing", false},
	40: {"KITEBOARDING", "surfing_sports", false},
	41: {"STAND_UP_PADDLE_BOARD", "paddle_sports", false},
	42: {"WORKOUT", "other", true},
	43: {"CARDIO_CLASS", "mixed_cardio", true},
	44: {"STAIR_CLIMBER", "stair_climbing", true},
	45: {"WHEELCHAIR", "other", false},
	46: {"GOLFING", "golf", false},
	47: {"OTHER", "other", false},
	49: {"BIKING_INDOOR_CYCLING_CLASS", "cycling", true},
	56: {"WALKING_TREADMILL", "walking", true},
	61: {"BIKING_INDOOR_TRAINER", "cycling", true},
	62: {"MULTISPORT", "other", false},
	63: {"TRANSITION", "other", false},
	64: {"EBIKING", "cycling", false},
	65: {"TICKR_OFFLINE", "other", false},
	66: {"YOGA", "yoga", true},
	67: {"RUNNING_RACE", "running", false},
	68: {"BIKING_INDOOR_VIRTUAL", "cycling", true},
	69: {"MENTAL_STRENGTH", "mind_and_body", true},
	70: {"HANDCYCLING", "hand_cycling", false},
	71: {"RUNNING_INDOOR_VIRTUAL", "running", true},
}
