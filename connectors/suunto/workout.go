// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapWorkout turns one workout summary into a Workout.
//
// Units: startTime and stopTime are epoch milliseconds, totalTime seconds of
// timer time (pauses excluded), totalDistance and totalAscent metres, speeds
// metres per second, energyConsumption kilocalories, heart rates beats per
// minute. The workout is Replaceable and hashed on the workout key: a
// workout the user edits in the Suunto app comes back under the same hash and
// its figures replace the stored ones.
func mapWorkout(line []byte, userID string) (*model.MapResult, error) {
	var w Workout
	if err := decode(DataTypeWorkout, line, &w); err != nil {
		return nil, err
	}
	externalID := w.WorkoutKey
	if externalID == "" {
		externalID = w.WorkoutID.String()
	}
	if externalID == "" {
		return nil, errors.New("suunto: workout: missing workoutKey and workoutId")
	}
	if w.StartTime == nil || *w.StartTime <= 0 {
		return nil, errors.New("suunto: workout: missing startTime")
	}
	start := time.UnixMilli(*w.StartTime).UTC()

	md := map[string]any{}
	act := sport{activity: "other"}
	if w.ActivityID != nil {
		act = activity(*w.ActivityID)
		md["suunto_activity_id"] = *w.ActivityID
		if act.name != "" {
			md["suunto_activity"] = act.name
		}
	}
	md["activity_type"] = act.activity
	if act.indoor {
		md["indoor"] = true
	}
	if w.WorkoutKey != "" && w.WorkoutID.String() != "" {
		md["suunto_workout_id"] = w.WorkoutID.String()
	}

	var dur *int
	if w.TotalTime != nil && *w.TotalTime >= 0 && *w.TotalTime < 1e7 {
		d := int(math.Round(*w.TotalTime))
		dur = &d
		md["total_time_s"] = *w.TotalTime
	}
	end := start
	switch {
	case w.StopTime != nil && *w.StopTime >= *w.StartTime:
		end = time.UnixMilli(*w.StopTime).UTC()
	case dur != nil:
		end = start.Add(time.Duration(*dur) * time.Second)
	}

	if w.TimeOffsetInMinutes != nil {
		if off, ok := offsetString(*w.TimeOffsetInMinutes); ok {
			md["utc_offset"] = off
		}
	}

	var avgHR, maxHR *float64
	if h := w.HRData; h != nil {
		avgHR = firstHR(h.WorkoutAvgHR, h.Avg)
		maxHR = firstHR(h.WorkoutMaxHR, h.HRMax)
		if v := firstHR(h.Min); v != nil {
			md["min_heart_rate"] = *v
		}
	}

	putFloat(md, "total_descent_m", w.TotalDescent)
	putFloat(md, "step_count", w.StepCount)
	putFloat(md, "max_speed_mps", w.MaxSpeed)
	putFloat(md, "avg_power_w", w.AvgPower)
	putFloat(md, "max_power_w", w.MaxPower)
	putFloat(md, "avg_cadence", w.AvgCadence)
	putFloat(md, "max_altitude_m", w.MaxAltitude)
	putFloat(md, "min_altitude_m", w.MinAltitude)
	putFloat(md, "recovery_time_s", w.RecoveryTime)
	putFloat(md, "peak_training_effect", w.PeakTrainingEffect)
	if g := w.Gear; g != nil {
		name := g.DisplayName
		if name == "" {
			name = g.Name
		}
		if name != "" {
			md["device"] = name
		}
		if g.SWVersion != "" {
			md["device_sw_version"] = g.SWVersion
		}
	}

	out := model.Workout{
		UserID:           userID,
		Provider:         Slug,
		ExternalID:       externalID,
		ActivityType:     act.activity,
		StartDate:        start,
		EndDate:          end,
		DurationS:        dur,
		ActiveEnergyKcal: positive(w.EnergyConsumption),
		DistanceM:        positive(w.TotalDistance),
		AvgHeartRate:     avgHR,
		MaxHeartRate:     maxHR,
		AvgSpeedMps:      positive(w.AvgSpeed),
		ElevationGainM:   nonNegative(w.TotalAscent),
		Metadata:         md,
		Replaceable:      true,
	}
	out.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":     userID,
		"provider":    Slug,
		"external_id": externalID,
	})
	return &model.MapResult{Workouts: []model.Workout{out}}, nil
}

// offsetString formats an offset in minutes as "+01:00". Offsets past
// +-18 hours are not real and give false.
func offsetString(minutes int) (string, bool) {
	if minutes < -18*60 || minutes > 18*60 {
		return "", false
	}
	sign := '+'
	if minutes < 0 {
		sign = '-'
		minutes = -minutes
	}
	return fmt.Sprintf("%c%02d:%02d", sign, minutes/60, minutes%60), true
}

// firstHR returns the first plausible heart rate (1 to 300 bpm).
func firstHR(vs ...*float64) *float64 {
	for _, v := range vs {
		if v != nil && *v > 0 && *v <= 300 {
			return v
		}
	}
	return nil
}

// positive drops a missing, zero or negative figure: Suunto writes 0 for what
// a workout did not measure (distance of a gym session).
func positive(v *float64) *float64 {
	if v == nil || *v <= 0 || math.IsInf(*v, 0) || math.IsNaN(*v) {
		return nil
	}
	return v
}

func nonNegative(v *float64) *float64 {
	if v == nil || *v < 0 {
		return nil
	}
	return v
}

func putFloat(md map[string]any, key string, v *float64) {
	if v != nil {
		md[key] = *v
	}
}

// sport is the canonical activity of a Suunto activity id, in the vocabulary
// the Apple Health connector uses, with Suunto's name for it and whether the
// activity itself says indoors.
type sport struct {
	name     string
	activity string
	indoor   bool
}

// activity maps a Suunto activityId ("other" for an unknown one; the id
// always stays in metadata).
func activity(id int) sport {
	if s, ok := activities[id]; ok {
		return s
	}
	return sport{activity: "other"}
}

// activities maps Suunto activity ids. Suunto does not publish the list on a
// public page; ids and names follow a third-party integration's table
// (unverified against Suunto's own list).
var activities = map[int]sport{
	0:   {name: "Walking", activity: "walking"},
	1:   {name: "Running", activity: "running"},
	2:   {name: "Cycling", activity: "cycling"},
	3:   {name: "Cross-country skiing", activity: "cross_country_skiing"},
	4:   {name: "Sports", activity: "other"},
	5:   {name: "Sports", activity: "other"},
	6:   {name: "Sports", activity: "other"},
	7:   {name: "Sports", activity: "other"},
	8:   {name: "Sports", activity: "other"},
	9:   {name: "Sports", activity: "other"},
	10:  {name: "Mountain biking", activity: "cycling"},
	11:  {name: "Hiking", activity: "hiking"},
	12:  {name: "Roller skating", activity: "skating_sports"},
	13:  {name: "Downhill skiing", activity: "downhill_skiing"},
	14:  {name: "Paddling", activity: "paddle_sports"},
	15:  {name: "Rowing", activity: "rowing"},
	16:  {name: "Golfing", activity: "golf"},
	17:  {name: "Indoor sports", activity: "other", indoor: true},
	18:  {name: "Parkouring", activity: "other"},
	19:  {name: "Ball games", activity: "other"},
	20:  {name: "Outdoor gym", activity: "functional_strength_training"},
	21:  {name: "Swimming", activity: "swimming"},
	22:  {name: "Trail running", activity: "running"},
	23:  {name: "Gym", activity: "traditional_strength_training", indoor: true},
	24:  {name: "Nordic walking", activity: "walking"},
	25:  {name: "Horseback riding", activity: "equestrian_sports"},
	26:  {name: "Motorsports", activity: "other"},
	27:  {name: "Skateboarding", activity: "skating_sports"},
	28:  {name: "Water sports", activity: "water_sports"},
	29:  {name: "Climbing", activity: "climbing"},
	30:  {name: "Snowboarding", activity: "snowboarding"},
	31:  {name: "Ski touring", activity: "snow_sports"},
	32:  {name: "Fitness class", activity: "mixed_cardio"},
	33:  {name: "Soccer", activity: "soccer"},
	34:  {name: "Tennis", activity: "tennis"},
	35:  {name: "Basketball", activity: "basketball"},
	36:  {name: "Badminton", activity: "badminton"},
	37:  {name: "Baseball", activity: "baseball"},
	38:  {name: "Volleyball", activity: "volleyball"},
	39:  {name: "American football", activity: "american_football"},
	40:  {name: "Table tennis", activity: "table_tennis"},
	41:  {name: "Racquet ball", activity: "racquetball"},
	42:  {name: "Squash", activity: "squash"},
	43:  {name: "Floorball", activity: "hockey"},
	44:  {name: "Handball", activity: "handball"},
	45:  {name: "Softball", activity: "softball"},
	46:  {name: "Bowling", activity: "bowling"},
	47:  {name: "Cricket", activity: "cricket"},
	48:  {name: "Rugby", activity: "rugby"},
	49:  {name: "Ice skating", activity: "skating_sports"},
	50:  {name: "Ice hockey", activity: "hockey"},
	51:  {name: "Yoga/pilates", activity: "yoga"},
	52:  {name: "Indoor cycling", activity: "cycling", indoor: true},
	53:  {name: "Treadmill", activity: "running", indoor: true},
	54:  {name: "Crossfit", activity: "cross_training"},
	55:  {name: "Crosstrainer", activity: "elliptical", indoor: true},
	56:  {name: "Roller skiing", activity: "cross_country_skiing"},
	57:  {name: "Indoor rowing", activity: "rowing", indoor: true},
	58:  {name: "Stretching", activity: "flexibility"},
	59:  {name: "Track and field", activity: "track_and_field"},
	60:  {name: "Orienteering", activity: "running"},
	61:  {name: "Standup paddling", activity: "paddle_sports"},
	62:  {name: "Combat sport", activity: "martial_arts"},
	63:  {name: "Kettlebell", activity: "functional_strength_training"},
	64:  {name: "Dancing", activity: "dance"},
	65:  {name: "Snow shoeing", activity: "snow_sports"},
	66:  {name: "Frisbee golf", activity: "disc_sports"},
	67:  {name: "Futsal", activity: "soccer"},
	68:  {name: "Multisport", activity: "other"},
	69:  {name: "Aerobics", activity: "mixed_cardio"},
	70:  {name: "Trekking", activity: "hiking"},
	71:  {name: "Sailing", activity: "sailing"},
	72:  {name: "Kayaking", activity: "paddle_sports"},
	73:  {name: "Circuit training", activity: "functional_strength_training"},
	74:  {name: "Triathlon", activity: "other"},
	75:  {name: "Padel", activity: "other"},
	76:  {name: "Cheerleading", activity: "other"},
	77:  {name: "Boxing", activity: "boxing"},
	78:  {name: "Scubadiving", activity: "underwater_diving"},
	79:  {name: "Freediving", activity: "underwater_diving"},
	80:  {name: "Adventure racing", activity: "other"},
	81:  {name: "Gymnastics", activity: "gymnastics"},
	82:  {name: "Canoeing", activity: "paddle_sports"},
	83:  {name: "Mountaineering", activity: "hiking"},
	84:  {name: "Telemarkskiing", activity: "downhill_skiing"},
	85:  {name: "Openwater swimming", activity: "swimming_open_water"},
	86:  {name: "Windsurfing", activity: "surfing_sports"},
	87:  {name: "Kitesurfing", activity: "surfing_sports"},
	88:  {name: "Paragliding", activity: "other"},
	90:  {name: "Snorkeling", activity: "water_sports"},
	91:  {name: "Surfing", activity: "surfing_sports"},
	92:  {name: "Swimrun", activity: "other"},
	93:  {name: "Duathlon", activity: "other"},
	94:  {name: "Aquathlon", activity: "other"},
	95:  {name: "Obstacle racing", activity: "running"},
	96:  {name: "Fishing", activity: "fishing"},
	97:  {name: "Hunting", activity: "hunting"},
	98:  {name: "Transition", activity: "other"},
	99:  {name: "Gravel cycling", activity: "cycling"},
	100: {name: "Mermaiding", activity: "swimming"},
	101: {name: "Spearfishing", activity: "underwater_diving"},
	102: {name: "Jump rope", activity: "jump_rope"},
	103: {name: "Track running", activity: "running"},
	104: {name: "Calisthenics", activity: "functional_strength_training"},
	105: {name: "E-biking", activity: "cycling"},
	106: {name: "E-mtb", activity: "cycling"},
	107: {name: "Backcountry skiing", activity: "snow_sports"},
	108: {name: "Wheelchair sport", activity: "other"},
	109: {name: "Hand cycling", activity: "hand_cycling"},
	110: {name: "Splitboarding", activity: "snowboarding"},
	111: {name: "Biathlon", activity: "cross_country_skiing"},
	112: {name: "Meditation", activity: "mind_and_body"},
	113: {name: "Field hockey", activity: "hockey"},
	114: {name: "Cyclocross", activity: "cycling"},
	115: {name: "Vertical running", activity: "running"},
	116: {name: "Ski mountaineering", activity: "snow_sports"},
	117: {name: "Skate skiing", activity: "cross_country_skiing"},
	118: {name: "Classic skiing", activity: "cross_country_skiing"},
	119: {name: "Chores", activity: "other"},
	120: {name: "Pilates", activity: "pilates"},
	121: {name: "Yoga", activity: "yoga"},
}
