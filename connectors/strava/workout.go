// SPDX-License-Identifier: AGPL-3.0-only

package strava

import (
	"fmt"
	"math"
	"strings"
	"time"

	// Embed the zone database so LoadLocation works in minimal images.
	_ "time/tzdata"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapActivity turns one SummaryActivity into a Workout.
//
// The workout is Replaceable and hashed on the Strava id and start: an
// activity the athlete edits (type, figures) comes back under the same hash
// and its new figures replace the stored ones instead of adding a second
// workout; an edit that moves the start supersedes the stored workout of the
// same Strava id. The summary polyline is not mapped: it has no timestamps, and a
// route point needs one.
func mapActivity(line []byte, userID string) (*model.MapResult, error) {
	var a Activity
	if err := decode(DataTypeActivity, line, &a); err != nil {
		return nil, err
	}
	if _, err := a.ID.Int64(); err != nil {
		return nil, fmt.Errorf("strava: activity: missing or invalid id")
	}
	if a.StartDate == "" {
		return nil, fmt.Errorf("strava: activity: missing start_date")
	}
	start, err := time.Parse(time.RFC3339, a.StartDate)
	if err != nil {
		return nil, fmt.Errorf("strava: activity: start_date is not RFC 3339")
	}
	start = start.UTC()

	sport := a.SportType
	if sport == "" {
		sport = a.Type // activities recorded before sport_type existed
	}
	st := sportType(sport)
	md := map[string]any{
		"activity_type":     st.activity,
		"strava_sport_type": sport,
	}
	if a.Type != "" {
		md["strava_type"] = a.Type
	}
	if st.indoor || (a.Trainer != nil && *a.Trainer) {
		md["indoor"] = true
	}

	end := start
	var dur *int
	if seconds(a.ElapsedTime) {
		end = start.Add(time.Duration(*a.ElapsedTime) * time.Second)
		md["elapsed_time_s"] = *a.ElapsedTime
		dur = a.ElapsedTime
	}
	if seconds(a.MovingTime) {
		md["moving_time_s"] = *a.MovingTime
		if a.ElapsedTime == nil {
			end = start.Add(time.Duration(*a.MovingTime) * time.Second)
		}
		dur = a.MovingTime
	}

	// Energy: calories when Strava gives them (detailed activities, some
	// uploads); otherwise kilojoules of mechanical work, which Strava itself
	// shows as calories one to one (about 25% efficiency cancels the 4.184
	// kJ/kcal factor).
	var energy *float64
	switch {
	case positive(a.Calories):
		energy = a.Calories
		md["active_energy_source"] = "calories"
	case positive(a.Kilojoules):
		kcal := *a.Kilojoules
		energy = &kcal
		md["active_energy_source"] = "kilojoules"
	}
	putFloat(md, "kilojoules", a.Kilojoules)
	putFloat(md, "calories", a.Calories)
	putFloat(md, "average_watts", a.AverageWatts)
	putFloat(md, "max_speed_mps", a.MaxSpeed)
	putFloat(md, "avg_heart_rate", a.AverageHeartrate)
	putFloat(md, "max_heart_rate", a.MaxHeartrate)
	putBool(md, "device_watts", a.DeviceWatts)
	putBool(md, "trainer", a.Trainer)
	putBool(md, "manual", a.Manual)
	if a.WorkoutType != nil {
		md["strava_workout_type"] = *a.WorkoutType
	}
	if tz := ianaZone(a.Timezone); tz != "" {
		md["timezone"] = tz
	}

	externalID := a.ID.String()
	out := model.Workout{
		UserID:           userID,
		Provider:         Slug,
		ExternalID:       externalID,
		ActivityType:     st.activity,
		StartDate:        start,
		EndDate:          end,
		DurationS:        dur,
		ActiveEnergyKcal: energy,
		DistanceM:        nonZero(a.Distance),
		AvgHeartRate:     a.AverageHeartrate,
		MaxHeartRate:     a.MaxHeartrate,
		AvgSpeedMps:      nonZero(a.AverageSpeed),
		ElevationGainM:   a.TotalElevationGain,
		Metadata:         md,
		Replaceable:      true,
	}
	out.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":     userID,
		"provider":    Slug,
		"start_date":  start.Format(time.RFC3339),
		"external_id": externalID,
	})
	return &model.MapResult{Workouts: []model.Workout{out}}, nil
}

// ianaZone extracts the IANA name of a Strava timezone, which reads
// "(GMT+01:00) Europe/Paris". It returns "" when the name is missing or not a
// zone Go knows.
func ianaZone(s string) string {
	name := strings.TrimSpace(s)
	if i := strings.LastIndex(name, ")"); i >= 0 {
		name = strings.TrimSpace(name[i+1:])
	}
	if name == "" {
		return ""
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ""
	}
	return name
}

// maxSeconds is the longest duration in seconds a time.Duration holds
// (about 292 years). A longer one would wrap when turned into a span.
const maxSeconds = math.MaxInt64 / int(time.Second)

// seconds reports whether v is a duration in seconds a workout can have: set,
// not negative and not so large it wraps.
func seconds(v *int) bool { return v != nil && *v >= 0 && *v <= maxSeconds }

func positive(v *float64) bool { return v != nil && *v > 0 }

// nonZero drops a zero figure: Strava writes 0 distance and speed for a
// workout that did not move (yoga, strength), which is not a measurement.
func nonZero(v *float64) *float64 {
	if v == nil || *v == 0 {
		return nil
	}
	return v
}

func putFloat(md map[string]any, key string, v *float64) {
	if v != nil {
		md[key] = *v
	}
}

func putBool(md map[string]any, key string, v *bool) {
	if v != nil {
		md[key] = *v
	}
}

// sport is the canonical activity of a Strava sport type, in the vocabulary
// the Apple Health and Withings connectors use, and whether Strava's type
// itself says indoors (virtual rides, runs and rows).
type sport struct {
	activity string
	indoor   bool
}

// sportType maps a Strava sport_type (or legacy type) to the canonical
// activity ("other" for an unknown one; the Strava name stays in metadata).
func sportType(s string) sport {
	if t, ok := sportTypes[s]; ok {
		return t
	}
	return sport{activity: "other"}
}

// sportTypes covers Strava's SportType enum.
// https://developers.strava.com/docs/reference/#api-models-SportType
var sportTypes = map[string]sport{
	"Run":                           {activity: "running"},
	"TrailRun":                      {activity: "running"},
	"VirtualRun":                    {activity: "running", indoor: true},
	"Walk":                          {activity: "walking"},
	"Hike":                          {activity: "hiking"},
	"Ride":                          {activity: "cycling"},
	"MountainBikeRide":              {activity: "cycling"},
	"GravelRide":                    {activity: "cycling"},
	"EBikeRide":                     {activity: "cycling"},
	"EMountainBikeRide":             {activity: "cycling"},
	"Velomobile":                    {activity: "cycling"},
	"VirtualRide":                   {activity: "cycling", indoor: true},
	"Handcycle":                     {activity: "hand_cycling"},
	"Swim":                          {activity: "swimming"},
	"Rowing":                        {activity: "rowing"},
	"VirtualRow":                    {activity: "rowing", indoor: true},
	"Canoeing":                      {activity: "paddle_sports"},
	"Kayaking":                      {activity: "paddle_sports"},
	"StandUpPaddling":               {activity: "paddle_sports"},
	"Surfing":                       {activity: "surfing_sports"},
	"Kitesurf":                      {activity: "surfing_sports"},
	"Windsurf":                      {activity: "surfing_sports"},
	"Sail":                          {activity: "sailing"},
	"AlpineSki":                     {activity: "downhill_skiing"},
	"BackcountrySki":                {activity: "snow_sports"},
	"NordicSki":                     {activity: "cross_country_skiing"},
	"RollerSki":                     {activity: "cross_country_skiing"},
	"Snowboard":                     {activity: "snowboarding"},
	"Snowshoe":                      {activity: "snow_sports"},
	"IceSkate":                      {activity: "skating_sports"},
	"InlineSkate":                   {activity: "skating_sports"},
	"Skateboard":                    {activity: "skating_sports"},
	"RockClimbing":                  {activity: "climbing"},
	"Crossfit":                      {activity: "cross_training"},
	"Elliptical":                    {activity: "elliptical"},
	"StairStepper":                  {activity: "stair_climbing"},
	"WeightTraining":                {activity: "traditional_strength_training"},
	"HighIntensityIntervalTraining": {activity: "high_intensity_interval_training"},
	"Pilates":                       {activity: "pilates"},
	"Yoga":                          {activity: "yoga"},
	"Golf":                          {activity: "golf"},
	"Soccer":                        {activity: "soccer"},
	"Tennis":                        {activity: "tennis"},
	"TableTennis":                   {activity: "table_tennis"},
	"Squash":                        {activity: "squash"},
	"Badminton":                     {activity: "badminton"},
	"Racquetball":                   {activity: "racquetball"},
	"Pickleball":                    {activity: "pickleball"},
	"Basketball":                    {activity: "basketball"},
	"Volleyball":                    {activity: "volleyball"},
	"Cricket":                       {activity: "cricket"},
	"Dance":                         {activity: "dance"},
	"Workout":                       {activity: "other"},
	"Wheelchair":                    {activity: "other"},
	"Padel":                         {activity: "other"},
}
