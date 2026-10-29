// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"fmt"
	"math"
	"strings"
	"time"

	// Embed the zone database so LoadLocation works in minimal images.
	_ "time/tzdata"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapActivity turns one intervals.icu activity into a Workout.
//
// The workout is Replaceable and hashed on the intervals.icu id and start: an
// activity the athlete edits (type, figures) or intervals.icu re-analyses
// comes back under the same hash and its new figures replace the stored ones.
//
// An activity synced from Strava is listed by the API as a stub without its
// figures (Strava's terms keep them from third parties); it gives nothing.
func mapActivity(line []byte, userID string) (*model.MapResult, error) {
	var a Activity
	if err := decode(DataTypeActivity, line, &a); err != nil {
		return nil, err
	}
	if stravaStub(a) {
		return nil, nil
	}
	if a.ID == "" {
		return nil, fmt.Errorf("intervals_icu: activity: missing id")
	}
	md := map[string]any{}
	tz := zone(a.Timezone)
	if tz != nil {
		md["timezone"] = tz.String()
	}
	start, err := activityStart(a, tz)
	if err != nil {
		return nil, err
	}

	activity, indoor := sportType(a.Type)
	md["activity_type"] = activity
	if a.Type != "" {
		md["intervals_type"] = a.Type
	}
	if indoor || (a.Trainer != nil && *a.Trainer) {
		md["indoor"] = true
	}
	if a.Source != "" {
		md["intervals_source"] = a.Source
		if up := upstreamSlug(a.Source); up != "" {
			md["source_provider"] = up
		}
	}
	if a.DeviceName != "" {
		md["device_name"] = a.DeviceName
	}
	if a.ExternalID != "" {
		md["intervals_external_id"] = a.ExternalID
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

	// Energy: calories when given; otherwise the mechanical work in joules,
	// as kilojoules shown one to one as kilocalories (about 25% efficiency
	// cancels the 4.184 kJ/kcal factor), the convention of power-based
	// training tools.
	var energy *float64
	switch {
	case positive(a.Calories):
		energy = a.Calories
		md["active_energy_source"] = "calories"
	case positive(a.Joules):
		kcal := *a.Joules / 1000
		energy = &kcal
		md["active_energy_source"] = "joules"
	}
	putFloat(md, "calories", a.Calories)
	putFloat(md, "joules", a.Joules)
	putFloat(md, "average_watts", a.AverageWatts)
	putFloat(md, "training_load", a.TrainingLoad)
	putFloat(md, "max_speed_mps", a.MaxSpeed)

	externalID := string(a.ID)
	out := model.Workout{
		UserID:           userID,
		Provider:         Slug,
		ExternalID:       externalID,
		ActivityType:     activity,
		StartDate:        start,
		EndDate:          end,
		DurationS:        dur,
		ActiveEnergyKcal: energy,
		DistanceM:        positiveOrNil(a.Distance),
		AvgHeartRate:     positiveOrNil(a.AverageHeartrate),
		MaxHeartRate:     positiveOrNil(a.MaxHeartrate),
		AvgSpeedMps:      positiveOrNil(a.AverageSpeed),
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

// stravaStub reports an activity intervals.icu synced from Strava and lists
// without its figures.
func stravaStub(a Activity) bool {
	return strings.EqualFold(a.Source, "STRAVA") && a.Type == "" &&
		a.MovingTime == nil && a.ElapsedTime == nil && a.Distance == nil
}

// activityStart reads start_date (UTC), or failing that start_date_local in
// the activity's time zone.
func activityStart(a Activity, tz *time.Location) (time.Time, error) {
	if a.StartDate != "" {
		t, err := time.Parse(time.RFC3339, a.StartDate)
		if err != nil {
			return time.Time{}, fmt.Errorf("intervals_icu: activity: start_date is not RFC 3339")
		}
		return t.UTC(), nil
	}
	if a.StartDateLocal == "" || tz == nil {
		return time.Time{}, fmt.Errorf("intervals_icu: activity: missing start_date")
	}
	l, err := parseLocal(a.StartDateLocal)
	if err != nil {
		return time.Time{}, fmt.Errorf("intervals_icu: activity: %w", err)
	}
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, tz).UTC(), nil
}

// zone loads an IANA zone name, or nil when it is missing or unknown.
func zone(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	return loc
}

// upstreamSlug turns intervals.icu's source of an activity
// ("GARMIN_CONNECT", "WAHOO", "ZWIFT") into the slug of the upstream that
// recorded it. A file upload, a manual entry or an unknown source gives the
// source lowercased.
func upstreamSlug(source string) string {
	s := strings.ToUpper(strings.TrimSpace(source))
	if s == "" {
		return ""
	}
	if up, ok := upstreams[s]; ok {
		return up
	}
	return strings.ToLower(s)
}

var upstreams = map[string]string{
	"GARMIN_CONNECT": "garmin",
	"GARMIN":         "garmin",
	"WAHOO":          "wahoo",
	"ZWIFT":          "zwift",
	"COROS":          "coros",
	"SUUNTO":         "suunto",
	"POLAR":          "polar",
	"STRAVA":         "strava",
}

// maxSeconds is the longest duration in seconds a time.Duration holds
// (about 292 years). A longer one would wrap when turned into a span.
const maxSeconds = math.MaxInt64 / int(time.Second)

// seconds reports whether v is a duration in seconds a workout can have: set,
// not negative and not so large it wraps.
func seconds(v *int) bool { return v != nil && *v >= 0 && *v <= maxSeconds }

func positive(v *float64) bool { return v != nil && *v > 0 }

// positiveOrNil drops a zero or negative figure: a workout that did not move
// (yoga, strength) has no distance or speed, and a missing heart rate strap
// gives no heart rate.
func positiveOrNil(v *float64) *float64 {
	if !positive(v) {
		return nil
	}
	return v
}

func putFloat(md map[string]any, key string, v *float64) {
	if v != nil {
		md[key] = *v
	}
}

// sportType maps an intervals.icu activity type to the canonical activity
// ("other" for an unknown one; the intervals.icu name stays in metadata) and
// whether the type itself says indoors. intervals.icu uses Strava's names.
func sportType(s string) (string, bool) {
	if t, ok := sportTypes[s]; ok {
		return t.activity, t.indoor
	}
	return "other", false
}

type sport struct {
	activity string
	indoor   bool
}

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
	"TrackRide":                     {activity: "cycling"},
	"Velomobile":                    {activity: "cycling"},
	"VirtualRide":                   {activity: "cycling", indoor: true},
	"Handcycle":                     {activity: "hand_cycling"},
	"Swim":                          {activity: "swimming"},
	"OpenWaterSwim":                 {activity: "swimming"},
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
	"VirtualSki":                    {activity: "cross_country_skiing", indoor: true},
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
	"Other":                         {activity: "other"},
}
