// SPDX-License-Identifier: AGPL-3.0-only

package oura

import "encoding/json"

// Data types this connector stores. Each raw object holds one of them, and
// Map dispatches on it. A line is one Oura document, verbatim (compacted to
// one line): the data type is the endpoint it came from under
// /v2/usercollection.
const (
	// DataTypeDailyActivity: one daily_activity document per line.
	DataTypeDailyActivity = "daily_activity"
	// DataTypeSleep: one sleep period per line (a night, a nap or a rest
	// period), with its 5 minute hypnogram.
	DataTypeSleep = "sleep"
	// DataTypeDailyReadiness: one daily_readiness document per line.
	DataTypeDailyReadiness = "daily_readiness"
	// DataTypeDailySpO2: one daily_spo2 document per line.
	DataTypeDailySpO2 = "daily_spo2"
	// DataTypeHeartRate: one heartrate reading per line.
	DataTypeHeartRate = "heartrate"
	// DataTypeWorkout: one workout document per line.
	DataTypeWorkout = "workout"
)

// AllDataTypes is every data type Fetch pulls, in the order it pulls them.
var AllDataTypes = []string{
	DataTypeDailyActivity,
	DataTypeSleep,
	DataTypeDailyReadiness,
	DataTypeDailySpO2,
	DataTypeWorkout,
	DataTypeHeartRate,
}

// page is the shape of every Oura list answer: the documents, and a token for
// the next page (absent or null on the last one).
type page struct {
	Data      []json.RawMessage `json:"data"`
	NextToken *string           `json:"next_token"`
}

// tokenBody is the answer of the token endpoint.
type tokenBody struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
}

// personalInfo is the part of /v2/usercollection/personal_info the connector
// reads: the Oura user id. Nothing else of it is kept.
type personalInfo struct {
	ID string `json:"id"`
}

// DailyActivity is a daily_activity document. Only the fields the mapper
// reads are declared; the raw line keeps the rest.
type DailyActivity struct {
	ID                        string   `json:"id"`
	Day                       string   `json:"day"`
	Timestamp                 string   `json:"timestamp"`
	Score                     *float64 `json:"score"`
	Steps                     *float64 `json:"steps"`
	ActiveCalories            *float64 `json:"active_calories"`
	TotalCalories             *float64 `json:"total_calories"`
	EquivalentWalkingDistance *float64 `json:"equivalent_walking_distance"`
	HighActivityTime          *float64 `json:"high_activity_time"`
	MediumActivityTime        *float64 `json:"medium_activity_time"`
	LowActivityTime           *float64 `json:"low_activity_time"`
	SedentaryTime             *float64 `json:"sedentary_time"`
	NonWearTime               *float64 `json:"non_wear_time"`
}

// Sleep is a sleep period document. Durations are in seconds.
type Sleep struct {
	ID                 string   `json:"id"`
	Day                string   `json:"day"`
	Type               string   `json:"type"`
	BedtimeStart       string   `json:"bedtime_start"`
	BedtimeEnd         string   `json:"bedtime_end"`
	TotalSleepDuration *int     `json:"total_sleep_duration"`
	DeepSleepDuration  *int     `json:"deep_sleep_duration"`
	REMSleepDuration   *int     `json:"rem_sleep_duration"`
	LightSleepDuration *int     `json:"light_sleep_duration"`
	AwakeTime          *int     `json:"awake_time"`
	TimeInBed          *int     `json:"time_in_bed"`
	Latency            *int     `json:"latency"`
	Efficiency         *float64 `json:"efficiency"`
	AverageHRV         *float64 `json:"average_hrv"`
	AverageHeartRate   *float64 `json:"average_heart_rate"`
	LowestHeartRate    *float64 `json:"lowest_heart_rate"`
	AverageBreath      *float64 `json:"average_breath"`
	RestlessPeriods    *int     `json:"restless_periods"`
	Period             *int     `json:"period"`
	SleepPhase5Min     string   `json:"sleep_phase_5_min"`
}

// DailyReadiness is a daily_readiness document.
type DailyReadiness struct {
	ID                        string             `json:"id"`
	Day                       string             `json:"day"`
	Timestamp                 string             `json:"timestamp"`
	Score                     *float64           `json:"score"`
	TemperatureDeviation      *float64           `json:"temperature_deviation"`
	TemperatureTrendDeviation *float64           `json:"temperature_trend_deviation"`
	Contributors              map[string]float64 `json:"contributors"`
}

// DailySpO2 is a daily_spo2 document.
type DailySpO2 struct {
	ID             string `json:"id"`
	Day            string `json:"day"`
	SpO2Percentage *struct {
		Average *float64 `json:"average"`
	} `json:"spo2_percentage"`
	BreathingDisturbanceIndex *float64 `json:"breathing_disturbance_index"`
}

// HeartRate is one heartrate reading.
type HeartRate struct {
	BPM       *float64 `json:"bpm"`
	Source    string   `json:"source"`
	Timestamp string   `json:"timestamp"`
}

// Workout is a workout document. Distance is in metres, calories in kcal.
type Workout struct {
	ID            string   `json:"id"`
	Activity      string   `json:"activity"`
	Calories      *float64 `json:"calories"`
	Day           string   `json:"day"`
	Distance      *float64 `json:"distance"`
	StartDatetime string   `json:"start_datetime"`
	EndDatetime   string   `json:"end_datetime"`
	Intensity     string   `json:"intensity"`
	Label         *string  `json:"label"`
	Source        string   `json:"source"`
}
