// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"encoding/json"
	"fmt"
)

// Data types this connector stores. Each raw object holds one of them (R16),
// and Map dispatches on it. A line of each type is one provider JSON object,
// kept as WHOOP sent it except for the additions documented on each constant.
const (
	// DataTypeCycle: one /v2/cycle record per line, verbatim. A cycle is
	// WHOOP's physiological day; the current one has no end yet.
	DataTypeCycle = "cycle"
	// DataTypeRecovery: one /v2/recovery record per line. A recovery carries
	// no time or zone of its own, so the start, end and timezone_offset of
	// its cycle are added as "cycle" when WHOOP returns the cycle.
	DataTypeRecovery = "recovery"
	// DataTypeSleep: one /v2/activity/sleep record per line, verbatim.
	DataTypeSleep = "sleep"
	// DataTypeWorkout: one /v2/activity/workout record per line, verbatim.
	DataTypeWorkout = "workout"
	// DataTypeBody: the /v2/user/measurement/body answer. WHOOP keeps one
	// current value per user with no date, so the UTC day it was read on is
	// added as "snapshot_date".
	DataTypeBody = "body"
	// DataTypeWebhook: one WHOOP webhook body per line, verbatim, stored by
	// the inbound route for a deletion (sleep.deleted, workout.deleted). It
	// is never fetched; Map turns it into a model.Deletion.
	DataTypeWebhook = "webhook"
)

// AllDataTypes is every data type Fetch pulls, in the order it pulls them.
// Cycles come before recoveries so a recovery finds its cycle without a
// request of its own.
var AllDataTypes = []string{
	DataTypeCycle,
	DataTypeRecovery,
	DataTypeSleep,
	DataTypeWorkout,
	DataTypeBody,
}

// Collection endpoints, under the API base.
const (
	pathCycle    = "/v2/cycle"
	pathRecovery = "/v2/recovery"
	pathSleep    = "/v2/activity/sleep"
	pathWorkout  = "/v2/activity/workout"
	pathBody     = "/v2/user/measurement/body"
	pathProfile  = "/v2/user/profile/basic"
)

// WHOOP score states. Only a SCORED record carries final figures: a
// PENDING_SCORE one is sent again once WHOOP has scored it, and UNSCORABLE
// stays without a score.
const (
	scoreScored      = "SCORED"
	scorePending     = "PENDING_SCORE"
	maxPageLimit     = 25
	maxPagesPerFetch = 500
)

// FlexString decodes a JSON string or number into a string. WHOOP ids are
// int64 numbers (users, cycles) or UUID strings (sleeps, workouts).
type FlexString string

// UnmarshalJSON accepts a string, a number or null.
func (f *FlexString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*f = ""
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("whoop: %s is neither a string nor a number", data)
	}
	*f = FlexString(n.String())
	return nil
}

// collectionPage is one page of a v2 collection endpoint. Records stay raw:
// they are stored verbatim.
type collectionPage struct {
	Records   []json.RawMessage `json:"records"`
	NextToken string            `json:"next_token"`
}

// ── cycle ────────────────────────────────────────────────────────────────────

// Cycle is one /v2/cycle record as Map reads it back.
type Cycle struct {
	ID             FlexString  `json:"id"`
	UserID         FlexString  `json:"user_id"`
	CreatedAt      string      `json:"created_at"`
	UpdatedAt      string      `json:"updated_at"`
	Start          string      `json:"start"`
	End            *string     `json:"end"`
	TimezoneOffset string      `json:"timezone_offset"`
	ScoreState     string      `json:"score_state"`
	Score          *CycleScore `json:"score"`
}

// CycleScore is the score of a cycle. Pointers tell a missing figure from a
// zero.
type CycleScore struct {
	Strain           *float64 `json:"strain"`
	Kilojoule        *float64 `json:"kilojoule"`
	AverageHeartRate *float64 `json:"average_heart_rate"`
	MaxHeartRate     *float64 `json:"max_heart_rate"`
}

// cycleRef is the part of a cycle added to a recovery line.
type cycleRef struct {
	Start          string  `json:"start"`
	End            *string `json:"end,omitempty"`
	TimezoneOffset string  `json:"timezone_offset,omitempty"`
}

// ── recovery ─────────────────────────────────────────────────────────────────

// Recovery is one /v2/recovery record as Map reads it back.
type Recovery struct {
	CycleID    FlexString     `json:"cycle_id"`
	SleepID    FlexString     `json:"sleep_id"`
	UserID     FlexString     `json:"user_id"`
	CreatedAt  string         `json:"created_at"`
	UpdatedAt  string         `json:"updated_at"`
	ScoreState string         `json:"score_state"`
	Score      *RecoveryScore `json:"score"`
	Cycle      *cycleRef      `json:"cycle"`
}

// RecoveryScore is the score of a recovery. hrv_rmssd_milli is the RMSSD of
// the night in milliseconds, not an SDNN.
type RecoveryScore struct {
	UserCalibrating  *bool    `json:"user_calibrating"`
	RecoveryScore    *float64 `json:"recovery_score"`
	RestingHeartRate *float64 `json:"resting_heart_rate"`
	HRVRMSSDMilli    *float64 `json:"hrv_rmssd_milli"`
	SpO2Percentage   *float64 `json:"spo2_percentage"`
	SkinTempCelsius  *float64 `json:"skin_temp_celsius"`
}

// ── sleep ────────────────────────────────────────────────────────────────────

// Sleep is one /v2/activity/sleep record as Map reads it back.
type Sleep struct {
	ID             FlexString  `json:"id"`
	CycleID        FlexString  `json:"cycle_id"`
	UserID         FlexString  `json:"user_id"`
	Start          string      `json:"start"`
	End            string      `json:"end"`
	TimezoneOffset string      `json:"timezone_offset"`
	Nap            bool        `json:"nap"`
	ScoreState     string      `json:"score_state"`
	Score          *SleepScore `json:"score"`
}

// SleepScore is the score of a sleep. WHOOP's public API has no hypnogram,
// only the time spent in each stage.
type SleepScore struct {
	StageSummary               *StageSummary  `json:"stage_summary"`
	SleepNeeded                map[string]any `json:"sleep_needed"`
	RespiratoryRate            *float64       `json:"respiratory_rate"`
	SleepPerformancePercentage *float64       `json:"sleep_performance_percentage"`
	SleepConsistencyPercentage *float64       `json:"sleep_consistency_percentage"`
	SleepEfficiencyPercentage  *float64       `json:"sleep_efficiency_percentage"`
}

// StageSummary is the time spent in each stage, in milliseconds.
type StageSummary struct {
	TotalInBedTimeMilli         *int64 `json:"total_in_bed_time_milli"`
	TotalAwakeTimeMilli         *int64 `json:"total_awake_time_milli"`
	TotalNoDataTimeMilli        *int64 `json:"total_no_data_time_milli"`
	TotalLightSleepTimeMilli    *int64 `json:"total_light_sleep_time_milli"`
	TotalSlowWaveSleepTimeMilli *int64 `json:"total_slow_wave_sleep_time_milli"`
	TotalREMSleepTimeMilli      *int64 `json:"total_rem_sleep_time_milli"`
	SleepCycleCount             *int64 `json:"sleep_cycle_count"`
	DisturbanceCount            *int64 `json:"disturbance_count"`
}

// ── workout ──────────────────────────────────────────────────────────────────

// Workout is one /v2/activity/workout record as Map reads it back.
type Workout struct {
	ID             FlexString    `json:"id"`
	UserID         FlexString    `json:"user_id"`
	Start          string        `json:"start"`
	End            string        `json:"end"`
	TimezoneOffset string        `json:"timezone_offset"`
	SportName      string        `json:"sport_name"`
	SportID        *int          `json:"sport_id"`
	ScoreState     string        `json:"score_state"`
	Score          *WorkoutScore `json:"score"`
}

// WorkoutScore is the score of a workout.
type WorkoutScore struct {
	Strain              *float64       `json:"strain"`
	AverageHeartRate    *float64       `json:"average_heart_rate"`
	MaxHeartRate        *float64       `json:"max_heart_rate"`
	Kilojoule           *float64       `json:"kilojoule"`
	PercentRecorded     *float64       `json:"percent_recorded"`
	DistanceMeter       *float64       `json:"distance_meter"`
	AltitudeGainMeter   *float64       `json:"altitude_gain_meter"`
	AltitudeChangeMeter *float64       `json:"altitude_change_meter"`
	ZoneDurations       map[string]any `json:"zone_durations"`
}

// ── body ─────────────────────────────────────────────────────────────────────

// Body is the /v2/user/measurement/body answer plus the snapshot date Fetch
// adds.
type Body struct {
	HeightMeter    *float64 `json:"height_meter"`
	WeightKilogram *float64 `json:"weight_kilogram"`
	MaxHeartRate   *float64 `json:"max_heart_rate"`
	SnapshotDate   string   `json:"snapshot_date"`
}

// ── oauth ────────────────────────────────────────────────────────────────────

// tokenAnswer is the token endpoint answer, success or OAuth error.
type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
}

// profile is the part of /v2/user/profile/basic the connector reads: the
// user id webhooks name. The name and email it also returns are not kept.
type profile struct {
	UserID FlexString `json:"user_id"`
}
