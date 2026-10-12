// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import "encoding/json"

// Data types this connector stores. Each raw object holds one of them, and Map
// dispatches on it.
const (
	// DataTypeWorkout: one workout summary of GET /v3/workouts per line,
	// verbatim. Mapped to a model.Workout.
	DataTypeWorkout = "workout"
	// DataTypeSleep: GET /247samples/sleep, stored raw only. Suunto does not
	// publish the answer's fields, so nothing is mapped yet.
	DataTypeSleep = "sleep"
	// DataTypeRecovery: GET /247samples/recovery, stored raw only.
	DataTypeRecovery = "recovery"
	// DataTypeDailyActivity: GET /247/daily-activity-statistics (daily
	// steps and energy, energy in joules), stored raw only.
	DataTypeDailyActivity = "daily_activity"
)

// AllDataTypes is every data type Fetch pulls.
var AllDataTypes = []string{DataTypeWorkout, DataTypeSleep, DataTypeRecovery, DataTypeDailyActivity}

// rawOnlyTypes are stored for later mapping; Map gives nothing for them.
var rawOnlyTypes = []string{DataTypeSleep, DataTypeRecovery, DataTypeDailyActivity}

// tokenResponse is the answer of POST /oauth/token. The access token is a JWT
// whose "user" claim is the Suunto username. Some answers are reported to
// carry the username as a top-level "user" field too; it is the fallback.
type tokenResponse struct {
	TokenType    string      `json:"token_type"`
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresIn    json.Number `json:"expires_in"`
	Scope        string      `json:"scope"`
	User         string      `json:"user"`
}

// Workout is the part of a workout summary the mapper reads. Every figure is
// a pointer: Suunto leaves out what the watch did not measure.
type Workout struct {
	WorkoutKey          string       `json:"workoutKey"`
	WorkoutID           json.Number  `json:"workoutId"`
	ActivityID          *int         `json:"activityId"`
	StartTime           *int64       `json:"startTime"` // epoch ms
	StopTime            *int64       `json:"stopTime"`  // epoch ms
	TotalTime           *float64     `json:"totalTime"` // s, timer time without pauses
	TotalDistance       *float64     `json:"totalDistance"`
	TotalAscent         *float64     `json:"totalAscent"`
	TotalDescent        *float64     `json:"totalDescent"`
	EnergyConsumption   *float64     `json:"energyConsumption"` // kcal
	StepCount           *float64     `json:"stepCount"`
	AvgSpeed            *float64     `json:"avgSpeed"` // m/s
	MaxSpeed            *float64     `json:"maxSpeed"`
	AvgPower            *float64     `json:"avgPower"`
	MaxPower            *float64     `json:"maxPower"`
	AvgCadence          *float64     `json:"avgCadence"`
	MaxAltitude         *float64     `json:"maxAltitude"`
	MinAltitude         *float64     `json:"minAltitude"`
	RecoveryTime        *float64     `json:"recoveryTime"`
	PeakTrainingEffect  *float64     `json:"peakTrainingEffect"`
	TimeOffsetInMinutes *int         `json:"timeOffsetInMinutes"`
	HRData              *hrData      `json:"hrdata"`
	Gear                *gear        `json:"gear"`
	LastModified        *json.Number `json:"lastModified"`
}

// hrData holds the heart rate figures of a workout. avg and hrmax are the
// workout's own; max is reported to be the user's maximum heart rate setting,
// so it is never read as the workout maximum.
type hrData struct {
	WorkoutAvgHR *float64 `json:"workoutAvgHR"`
	WorkoutMaxHR *float64 `json:"workoutMaxHR"`
	Avg          *float64 `json:"avg"`
	HRMax        *float64 `json:"hrmax"`
	Min          *float64 `json:"min"`
}

// gear is the device that recorded the workout.
type gear struct {
	DisplayName  string `json:"displayName"`
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer"`
	SWVersion    string `json:"swVersion"`
}

// notification is one Suunto notification. Two shapes are accepted: the
// current one names the event in "type" and nests the workout, and the older
// one documented in the API Zone FAQ carries only username and workoutid.
type notification struct {
	Type       string               `json:"type"`
	Username   string               `json:"username"`
	WorkoutKey string               `json:"workoutKey"`
	WorkoutID  json.RawMessage      `json:"workoutid"`
	Workout    *notificationWorkout `json:"workout"`
}

type notificationWorkout struct {
	WorkoutKey string          `json:"workoutKey"`
	WorkoutID  json.RawMessage `json:"workoutId"`
	StartTime  *json.Number    `json:"startTime"`
}
