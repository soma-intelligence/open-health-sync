// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// exerciseStart is the UTC start of an exercise. AccessLink gives the local
// wall clock start and its UTC offset in minutes; a start that carries its
// own offset is used as is, and one without any offset is read as UTC.
func exerciseStart(e Exercise) (time.Time, error) {
	if e.StartTime == "" {
		return time.Time{}, errors.New("missing start_time")
	}
	t, zoned, err := parseTime(e.StartTime)
	if err != nil {
		return time.Time{}, err
	}
	if !zoned && e.StartTimeUTCOffset != nil {
		t = t.Add(-time.Duration(*e.StartTimeUTCOffset) * time.Minute)
	}
	return t.UTC(), nil
}

// mapExercise turns one exercise summary into a Workout. The hash is the
// Polar exercise id alone, so the same exercise sent again (or edited in
// Polar Flow) maps to the same record and never to a second one. The workout
// is Replaceable: an edited copy replaces the stored figures, and one whose
// start moved supersedes the stored workout of the same id.
func mapExercise(line []byte, userID string) (*model.MapResult, error) {
	var e Exercise
	if err := decode(DataTypeExercise, line, &e); err != nil {
		return nil, err
	}
	if e.ID == "" {
		return nil, fmt.Errorf("polar: exercise: missing id")
	}
	start, err := exerciseStart(e)
	if err != nil {
		return nil, fmt.Errorf("polar: exercise: %w", err)
	}
	end := start
	var dur *int
	if e.Duration != "" {
		d, err := parseISODuration(e.Duration)
		if err != nil {
			return nil, fmt.Errorf("polar: exercise: %w", err)
		}
		s := int(d.Round(time.Second) / time.Second)
		dur = &s
		end = start.Add(d)
	}

	wt := polarSport(e.Sport, e.DetailedSportInfo)
	md := map[string]any{
		"activity_type": wt.activity,
		"polar_sport":   e.Sport,
	}
	if e.DetailedSportInfo != "" {
		md["polar_detailed_sport"] = e.DetailedSportInfo
	}
	if wt.indoor {
		md["indoor"] = true
	}
	if e.Device != "" {
		md["device"] = e.Device
	}
	setIf(md, "training_load", e.TrainingLoad)
	setIf(md, "running_index", e.RunningIndex)
	setIf(md, "has_route", e.HasRoute)
	if t, zoned, err := parseTime(e.StartTime); err == nil && zoned {
		md = withOffset(md, offsetMinutes(t))
	} else if e.StartTimeUTCOffset != nil {
		md = withOffset(md, *e.StartTimeUTCOffset)
	}

	out := model.Workout{
		UserID:           userID,
		Provider:         Slug,
		ExternalID:       e.ID,
		ActivityType:     wt.activity,
		StartDate:        start,
		EndDate:          end,
		DurationS:        dur,
		ActiveEnergyKcal: e.Calories,
		DistanceM:        e.Distance,
		Metadata:         md,
		Replaceable:      true,
	}
	if e.HeartRate != nil {
		out.AvgHeartRate = e.HeartRate.Average
		out.MaxHeartRate = e.HeartRate.Maximum
	}
	out.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":     userID,
		"provider":    Slug,
		"data_type":   DataTypeExercise,
		"external_id": e.ID,
	})
	return &model.MapResult{Workouts: []model.Workout{out}}, nil
}

// workoutType is the canonical activity of a Polar sport, in the vocabulary
// the Apple Health connector uses, and whether it is an indoor variant.
type workoutType struct {
	activity string
	indoor   bool
}

// polarSport maps a Polar sport to the canonical activity: the detailed
// sport first (it is the finer one), then the sport, then "other". Names
// are matched case-insensitively with "-" and spaces read as "_".
func polarSport(sport, detailed string) workoutType {
	for _, s := range []string{detailed, sport} {
		if t, ok := polarSports[normSport(s)]; ok {
			return t
		}
	}
	return workoutType{activity: "other"}
}

func normSport(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.NewReplacer("-", "_", " ", "_").Replace(s)
}

// polarSports maps Polar sport names to canonical activities. The list is
// the commonly seen subset of Polar Flow's sports; the AccessLink docs do not
// publish the full enumeration, so an unknown sport maps to "other" and its
// name stays in the metadata (polar_sport, polar_detailed_sport).
var polarSports = map[string]workoutType{
	"RUNNING":                 {activity: "running"},
	"ROAD_RUNNING":            {activity: "running"},
	"TRAIL_RUNNING":           {activity: "running"},
	"TRACK_AND_FIELD_RUNNING": {activity: "running"},
	"ULTRARUNNING_RUNNING":    {activity: "running"},
	"TREADMILL_RUNNING":       {activity: "running", indoor: true},
	"CYCLING":                 {activity: "cycling"},
	"ROAD_BIKING":             {activity: "cycling"},
	"MOUNTAIN_BIKING":         {activity: "cycling"},
	"GRAVEL_BIKING":           {activity: "cycling"},
	"INDOOR_CYCLING":          {activity: "cycling", indoor: true},
	"WALKING":                 {activity: "walking"},
	"NORDIC_WALKING":          {activity: "walking"},
	"HIKING":                  {activity: "hiking"},
	"SWIMMING":                {activity: "swimming"},
	"POOL_SWIMMING":           {activity: "swimming", indoor: true},
	"OPEN_WATER_SWIMMING":     {activity: "swimming_open_water"},
	"STRENGTH_TRAINING":       {activity: "traditional_strength_training"},
	"CIRCUIT_TRAINING":        {activity: "functional_strength_training"},
	"CORE":                    {activity: "core_training"},
	"CROSSFIT":                {activity: "high_intensity_interval_training"},
	"YOGA":                    {activity: "yoga"},
	"PILATES":                 {activity: "pilates"},
	"ROWING":                  {activity: "rowing"},
	"INDOOR_ROWING":           {activity: "rowing", indoor: true},
	"CROSS_COUNTRY_SKIING":    {activity: "cross_country_skiing"},
	"DOWNHILL_SKIING":         {activity: "downhill_skiing"},
	"SNOWBOARDING":            {activity: "snowboarding"},
	"ELLIPTICAL":              {activity: "elliptical"},
	"CROSS_TRAINER":           {activity: "elliptical"},
	"FOOTBALL":                {activity: "soccer"},
	"SOCCER":                  {activity: "soccer"},
	"BASKETBALL":              {activity: "basketball"},
	"VOLLEYBALL":              {activity: "volleyball"},
	"TENNIS":                  {activity: "tennis"},
	"BADMINTON":               {activity: "badminton"},
	"SQUASH":                  {activity: "squash"},
	"TABLE_TENNIS":            {activity: "table_tennis"},
	"GOLF":                    {activity: "golf"},
	"DANCING":                 {activity: "dance"},
	"ICE_SKATING":             {activity: "skating_sports"},
	"INLINE_SKATING":          {activity: "skating_sports"},
	"KAYAKING":                {activity: "paddle_sports"},
	"CANOEING":                {activity: "paddle_sports"},
	"CLIMBING":                {activity: "climbing"},
	"BOXING":                  {activity: "boxing"},
	"MARTIAL_ARTS":            {activity: "martial_arts"},
	"TRIATHLON":               {activity: "mixed_cardio"},
	"OTHER_INDOOR":            {activity: "other", indoor: true},
	"OTHER_OUTDOOR":           {activity: "other"},
	"OTHER":                   {activity: "other"},
}
