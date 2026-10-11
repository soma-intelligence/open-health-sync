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
	// DataTypeDailySleep: one daily_sleep document (the day's sleep score)
	// per line.
	DataTypeDailySleep = "daily_sleep"
	// DataTypeDailyStress: one daily_stress document per line.
	DataTypeDailyStress = "daily_stress"
	// DataTypeSession: one session document (a meditation, breathing or
	// other guided moment) per line.
	DataTypeSession = "session"
	// DataTypeVO2Max: one vo2_max document per line. The endpoint is spelled
	// vO2_max (see endpoints); the data type keeps Oura's webhook spelling.
	DataTypeVO2Max = "vo2_max"
	// DataTypeWebhook: one Oura webhook notification per line, verbatim,
	// stored by the inbound route for a delete event. It is never fetched;
	// Map turns it into a model.Deletion.
	DataTypeWebhook = "webhook"
)

// AllDataTypes is every data type Fetch pulls, in the order it pulls them.
var AllDataTypes = []string{
	DataTypeDailyActivity,
	DataTypeSleep,
	DataTypeDailySleep,
	DataTypeDailyReadiness,
	DataTypeDailySpO2,
	DataTypeDailyStress,
	DataTypeWorkout,
	DataTypeSession,
	DataTypeVO2Max,
	DataTypeHeartRate,
}

// endpoints names the /v2/usercollection path of a data type whose path is
// not its name.
var endpoints = map[string]string{
	DataTypeVO2Max: "vO2_max",
}

// endpoint returns the /v2/usercollection path segment of dataType.
func endpoint(dataType string) string {
	if e, ok := endpoints[dataType]; ok {
		return e
	}
	return dataType
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

// DailySleep is a daily_sleep document: the day's sleep score and its
// contributors.
type DailySleep struct {
	ID           string             `json:"id"`
	Day          string             `json:"day"`
	Timestamp    string             `json:"timestamp"`
	Score        *float64           `json:"score"`
	Contributors map[string]float64 `json:"contributors"`
}

// DailyStress is a daily_stress document. StressHigh and RecoveryHigh are
// seconds of the day; DaySummary is "restored", "normal" or "stressful".
type DailyStress struct {
	ID           string   `json:"id"`
	Day          string   `json:"day"`
	StressHigh   *float64 `json:"stress_high"`
	RecoveryHigh *float64 `json:"recovery_high"`
	DaySummary   *string  `json:"day_summary"`
}

// Session is a session document. The heart rate, HRV and motion series it
// carries stay in the raw line.
type Session struct {
	ID            string  `json:"id"`
	Day           string  `json:"day"`
	StartDatetime string  `json:"start_datetime"`
	EndDatetime   string  `json:"end_datetime"`
	Type          string  `json:"type"`
	Mood          *string `json:"mood"`
}

// VO2Max is a vo2_max document, in mL/kg/min.
type VO2Max struct {
	ID        string   `json:"id"`
	Day       string   `json:"day"`
	Timestamp string   `json:"timestamp"`
	VO2Max    *float64 `json:"vo2_max"`
}
