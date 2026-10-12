// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mapActivity turns one activity summary into a Workout.
//
// The workout is Replaceable and hashed on the Garmin activity id (the
// summary id when there is none): an activity the user edits comes back
// under the same hash and its figures replace the stored ones; an edit that
// moves the start supersedes the stored workout of the same id.
func mapActivity(line []byte, userID string) (*model.MapResult, error) {
	var a Activity
	if err := decode(DataTypeActivities, line, &a); err != nil {
		return nil, err
	}
	id := a.SummaryID
	if a.ActivityID != nil && *a.ActivityID > 0 {
		id = strconv.FormatInt(*a.ActivityID, 10)
	}
	if id == "" {
		return nil, fmt.Errorf("garmin: activities: missing activityId and summaryId")
	}
	start, err := unixTime("startTimeInSeconds", a.StartTimeInSeconds)
	if err != nil {
		return nil, fmt.Errorf("garmin: activities: %w", err)
	}
	wt := activityType(a.ActivityType)
	md := dayMeta(a.SummaryID, a.StartTimeOffsetInSeconds)
	md["activity_type"] = wt.activity
	if a.ActivityType != "" {
		md["garmin_activity_type"] = a.ActivityType
	}
	if wt.indoor {
		md["indoor"] = true
	}
	if a.ActivityName != "" {
		md["activity_name"] = a.ActivityName
	}
	if a.DeviceName != "" {
		md["device_name"] = a.DeviceName
	}
	setIf(md, "manual", a.Manual)
	setIf(md, "steps", a.Steps)
	setIf(md, "max_speed_mps", a.MaxSpeed)
	setIf(md, "elevation_loss_m", a.TotalElevationLoss)

	w := model.Workout{
		UserID:       userID,
		Provider:     Slug,
		ExternalID:   id,
		ActivityType: wt.activity,
		StartDate:    start,
		EndDate:      start,
		Metadata:     md,
		Replaceable:  true,
	}
	if d := seconds(a.DurationInSeconds, -1); d >= 0 {
		n := int(d)
		w.DurationS = &n
		w.EndDate = start.Add(time.Duration(d) * time.Second)
	}
	if inRange(a.ActiveKilocalories, 5e4) {
		w.ActiveEnergyKcal = a.ActiveKilocalories
	}
	if inRange(a.DistanceInMeters, 1e7) {
		w.DistanceM = a.DistanceInMeters
	}
	if inBounds(a.AverageHeartRate, 20, 250) {
		w.AvgHeartRate = a.AverageHeartRate
	}
	if inBounds(a.MaxHeartRate, 20, 250) {
		w.MaxHeartRate = a.MaxHeartRate
	}
	if inRange(a.AverageSpeed, 1e3) {
		w.AvgSpeedMps = a.AverageSpeed
	}
	if inRange(a.TotalElevationGain, 1e5) {
		w.ElevationGainM = a.TotalElevationGain
	}
	w.ContentHash = hash(userID, DataTypeActivities, "external_id", id)
	return &model.MapResult{Workouts: []model.Workout{w}}, nil
}

// workoutType is the canonical activity of a Garmin activity type.
type workoutType struct {
	activity string
	indoor   bool
}

// activityType maps a Garmin activityType ("RUNNING", "LAP_SWIMMING") to the
// canonical activity, "other" for an unknown one (the Garmin name stays in
// metadata).
func activityType(s string) workoutType {
	if t, ok := activityTypes[strings.ToUpper(strings.TrimSpace(s))]; ok {
		return t
	}
	return workoutType{activity: "other"}
}

// activityTypes covers the Garmin activity type keys seen in public
// examples and Garmin Connect's activity list (unverified against Garmin's
// current specification).
var activityTypes = map[string]workoutType{
	"RUNNING":                    {activity: "running"},
	"STREET_RUNNING":             {activity: "running"},
	"TRACK_RUNNING":              {activity: "running"},
	"TRAIL_RUNNING":              {activity: "running"},
	"ULTRA_RUN":                  {activity: "running"},
	"OBSTACLE_RUN":               {activity: "running"},
	"TREADMILL_RUNNING":          {activity: "running", indoor: true},
	"INDOOR_RUNNING":             {activity: "running", indoor: true},
	"VIRTUAL_RUN":                {activity: "running", indoor: true},
	"CYCLING":                    {activity: "cycling"},
	"ROAD_BIKING":                {activity: "cycling"},
	"MOUNTAIN_BIKING":            {activity: "cycling"},
	"GRAVEL_CYCLING":             {activity: "cycling"},
	"CYCLOCROSS":                 {activity: "cycling"},
	"DOWNHILL_BIKING":            {activity: "cycling"},
	"BMX":                        {activity: "cycling"},
	"RECUMBENT_CYCLING":          {activity: "cycling"},
	"TRACK_CYCLING":              {activity: "cycling"},
	"E_BIKE_FITNESS":             {activity: "cycling"},
	"E_BIKE_MOUNTAIN":            {activity: "cycling"},
	"INDOOR_CYCLING":             {activity: "cycling", indoor: true},
	"VIRTUAL_RIDE":               {activity: "cycling", indoor: true},
	"HAND_CYCLING":               {activity: "hand_cycling"},
	"INDOOR_HAND_CYCLING":        {activity: "hand_cycling", indoor: true},
	"WALKING":                    {activity: "walking"},
	"CASUAL_WALKING":             {activity: "walking"},
	"SPEED_WALKING":              {activity: "walking"},
	"INDOOR_WALKING":             {activity: "walking", indoor: true},
	"HIKING":                     {activity: "hiking"},
	"MOUNTAINEERING":             {activity: "hiking"},
	"SWIMMING":                   {activity: "swimming"},
	"LAP_SWIMMING":               {activity: "swimming", indoor: true},
	"OPEN_WATER_SWIMMING":        {activity: "swimming_open_water"},
	"STRENGTH_TRAINING":          {activity: "traditional_strength_training"},
	"CARDIO_TRAINING":            {activity: "mixed_cardio"},
	"FITNESS_EQUIPMENT":          {activity: "other", indoor: true},
	"HIIT":                       {activity: "high_intensity_interval_training"},
	"ELLIPTICAL":                 {activity: "elliptical", indoor: true},
	"STAIR_CLIMBING":             {activity: "stair_climbing"},
	"FLOOR_CLIMBING":             {activity: "stair_climbing"},
	"INDOOR_ROWING":              {activity: "rowing", indoor: true},
	"ROWING":                     {activity: "rowing"},
	"YOGA":                       {activity: "yoga"},
	"PILATES":                    {activity: "pilates"},
	"BREATHWORK":                 {activity: "mind_and_body"},
	"MEDITATION":                 {activity: "mind_and_body"},
	"JUMP_ROPE":                  {activity: "jump_rope"},
	"BOXING":                     {activity: "boxing"},
	"MIXED_MARTIAL_ARTS":         {activity: "martial_arts"},
	"RESORT_SKIING_SNOWBOARDING": {activity: "downhill_skiing"},
	"RESORT_SKIING":              {activity: "downhill_skiing"},
	"RESORT_SNOWBOARDING":        {activity: "snowboarding"},
	"BACKCOUNTRY_SKIING":         {activity: "snow_sports"},
	"BACKCOUNTRY_SNOWBOARDING":   {activity: "snow_sports"},
	"CROSS_COUNTRY_SKIING_WS":    {activity: "cross_country_skiing"},
	"SKATE_SKIING_WS":            {activity: "cross_country_skiing"},
	"SNOWSHOEING_WS":             {activity: "snow_sports"},
	"SKATING_WS":                 {activity: "skating_sports"},
	"INLINE_SKATING":             {activity: "skating_sports"},
	"STAND_UP_PADDLEBOARDING":    {activity: "paddle_sports"},
	"KAYAKING":                   {activity: "paddle_sports"},
	"WHITEWATER_RAFTING":         {activity: "paddle_sports"},
	"SURFING":                    {activity: "surfing_sports"},
	"KITEBOARDING":               {activity: "surfing_sports"},
	"WINDSURFING":                {activity: "surfing_sports"},
	"SAILING":                    {activity: "sailing"},
	"ROCK_CLIMBING":              {activity: "climbing"},
	"BOULDERING":                 {activity: "climbing"},
	"INDOOR_CLIMBING":            {activity: "climbing", indoor: true},
	"GOLF":                       {activity: "golf"},
	"TENNIS":                     {activity: "tennis"},
	"TABLE_TENNIS":               {activity: "table_tennis"},
	"PICKLEBALL":                 {activity: "pickleball"},
	"BADMINTON":                  {activity: "badminton"},
	"SQUASH":                     {activity: "squash"},
	"RACQUETBALL":                {activity: "racquetball"},
	"SOCCER":                     {activity: "soccer"},
	"BASKETBALL":                 {activity: "basketball"},
	"VOLLEYBALL":                 {activity: "volleyball"},
	"BASEBALL":                   {activity: "baseball"},
	"SOFTBALL":                   {activity: "softball"},
	"AMERICAN_FOOTBALL":          {activity: "american_football"},
	"RUGBY":                      {activity: "rugby"},
	"CRICKET":                    {activity: "cricket"},
	"LACROSSE":                   {activity: "lacrosse"},
	"ICE_HOCKEY":                 {activity: "hockey"},
	"FIELD_HOCKEY":               {activity: "hockey"},
	"DANCE":                      {activity: "dance"},
	"MULTI_SPORT":                {activity: "other"},
	"OTHER":                      {activity: "other"},
}
