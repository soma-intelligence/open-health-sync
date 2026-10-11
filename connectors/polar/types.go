// SPDX-License-Identifier: AGPL-3.0-only

package polar

import "encoding/json"

// Data types this connector stores. Each raw object holds one of them, and
// Map dispatches on it. A line of each type is one AccessLink JSON object,
// kept as Polar sent it except for the additions documented on each
// constant.
const (
	// DataTypeExercise: one GET /v3/exercises item per line, verbatim.
	DataTypeExercise = "exercise"
	// DataTypeSleep: one GET /v3/users/sleep night per line, verbatim.
	DataTypeSleep = "sleep"
	// DataTypeNightlyRecharge: one GET /v3/users/nightly-recharge item per
	// line, verbatim.
	DataTypeNightlyRecharge = "nightly_recharge"
	// DataTypeActivity: one GET /v3/users/activities day per line, verbatim.
	DataTypeActivity = "activity"
	// DataTypeContinuousHR: one GET /v3/users/continuous-heart-rate day per
	// line. Polar gives its sample times as local wall clock without an
	// offset; when the same pull saw a sleep or an exercise that tells the
	// user's UTC offset, it is added as "utc_offset_minutes".
	DataTypeContinuousHR = "continuous_heart_rate"
	// DataTypePhysicalInfo: the GET /v3/users/physical-info object. The pull
	// time is added as "fetched_at" so a line without "modified" or
	// "created" still has a date.
	DataTypePhysicalInfo = "physical_info"
)

// AllDataTypes is every data type Fetch pulls, in the order it pulls them.
// Sleep and exercises come before continuous heart rate, whose lines take
// their UTC offset from them.
var AllDataTypes = []string{
	DataTypeSleep,
	DataTypeNightlyRecharge,
	DataTypeExercise,
	DataTypeActivity,
	DataTypeContinuousHR,
	DataTypePhysicalInfo,
}

// Exercise is one AccessLink exercise summary.
type Exercise struct {
	ID                 string   `json:"id"`
	UploadTime         string   `json:"upload_time"`
	Device             string   `json:"device"`
	DeviceID           string   `json:"device_id"`
	StartTime          string   `json:"start_time"`
	StartTimeUTCOffset *int     `json:"start_time_utc_offset"`
	Duration           string   `json:"duration"`
	Calories           *float64 `json:"calories"`
	Distance           *float64 `json:"distance"`
	HeartRate          *struct {
		Average *float64 `json:"average"`
		Maximum *float64 `json:"maximum"`
	} `json:"heart_rate"`
	TrainingLoad      *float64 `json:"training_load"`
	Sport             string   `json:"sport"`
	DetailedSportInfo string   `json:"detailed_sport_info"`
	HasRoute          *bool    `json:"has_route"`
	RunningIndex      *float64 `json:"running-index"`
}

// Sleep is one AccessLink sleep night. Durations are seconds.
type Sleep struct {
	Date                      string             `json:"date"`
	SleepStartTime            string             `json:"sleep_start_time"`
	SleepEndTime              string             `json:"sleep_end_time"`
	DeviceID                  string             `json:"device_id"`
	Continuity                *float64           `json:"continuity"`
	ContinuityClass           *int               `json:"continuity_class"`
	LightSleep                *int               `json:"light_sleep"`
	DeepSleep                 *int               `json:"deep_sleep"`
	REMSleep                  *int               `json:"rem_sleep"`
	UnrecognizedSleepStage    *int               `json:"unrecognized_sleep_stage"`
	SleepScore                *float64           `json:"sleep_score"`
	TotalInterruptionDuration *int               `json:"total_interruption_duration"`
	SleepCharge               *int               `json:"sleep_charge"`
	SleepGoal                 *int               `json:"sleep_goal"`
	SleepRating               *int               `json:"sleep_rating"`
	ShortInterruptionDuration *int               `json:"short_interruption_duration"`
	LongInterruptionDuration  *int               `json:"long_interruption_duration"`
	SleepCycles               *int               `json:"sleep_cycles"`
	GroupDurationScore        *float64           `json:"group_duration_score"`
	GroupSolidityScore        *float64           `json:"group_solidity_score"`
	GroupRegenerationScore    *float64           `json:"group_regeneration_score"`
	Hypnogram                 map[string]int     `json:"hypnogram"`
	HeartRateSamples          map[string]float64 `json:"heart_rate_samples"`
}

// NightlyRecharge is one AccessLink Nightly Recharge result.
type NightlyRecharge struct {
	Date                  string   `json:"date"`
	HeartRateAvg          *float64 `json:"heart_rate_avg"`
	BeatToBeatAvg         *float64 `json:"beat_to_beat_avg"`
	HeartRateVariabilityA *float64 `json:"heart_rate_variability_avg"`
	BreathingRateAvg      *float64 `json:"breathing_rate_avg"`
	NightlyRechargeStatus *int     `json:"nightly_recharge_status"`
	ANSCharge             *float64 `json:"ans_charge"`
	ANSChargeStatus       *int     `json:"ans_charge_status"`
}

// Activity is one AccessLink daily activity summary.
type Activity struct {
	StartTime            string   `json:"start_time"`
	EndTime              string   `json:"end_time"`
	ActiveDuration       string   `json:"active_duration"`
	InactiveDuration     string   `json:"inactive_duration"`
	DailyActivity        *float64 `json:"daily_activity"`
	Calories             *float64 `json:"calories"`
	ActiveCalories       *float64 `json:"active_calories"`
	Steps                *float64 `json:"steps"`
	InactivityAlertCount *int     `json:"inactivity_alert_count"`
	DistanceFromSteps    *float64 `json:"distance_from_steps"`
}

// ContinuousHRDay is one day of AccessLink continuous heart rate.
type ContinuousHRDay struct {
	Date             string `json:"date"`
	UTCOffsetMinutes *int   `json:"utc_offset_minutes"`
	HeartRateSamples []struct {
		HeartRate  *float64 `json:"heart_rate"`
		SampleTime string   `json:"sample_time"`
	} `json:"heart_rate_samples"`
}

// PhysicalInfo is the AccessLink physical information of a user.
type PhysicalInfo struct {
	Created          string   `json:"created"`
	Modified         string   `json:"modified"`
	FetchedAt        string   `json:"fetched_at"`
	Weight           *float64 `json:"weight"`
	Height           *float64 `json:"height"`
	MaximumHeartRate *float64 `json:"maximum_heart_rate"`
	RestingHeartRate *float64 `json:"resting_heart_rate"`
	VO2Max           *float64 `json:"vo2_max"`
}

// sleepList, rechargeList and hrList are the list answers.
type (
	sleepList struct {
		Nights []json.RawMessage `json:"nights"`
	}
	rechargeList struct {
		Recharges []json.RawMessage `json:"recharges"`
	}
	hrList struct {
		HeartRates []json.RawMessage `json:"heart_rates"`
	}
)
