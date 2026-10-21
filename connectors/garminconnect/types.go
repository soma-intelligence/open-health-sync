// SPDX-License-Identifier: AGPL-3.0-only

package garminconnect

// Data types of the stored raw lines.
const (
	DataTypeDailies    = "dailies"
	DataTypeSleeps     = "sleeps"
	DataTypeHRV        = "hrv"
	DataTypeActivities = "activities"
)

// DailySummary is one day of the Connect daily summary
// (usersummary-service dailysummaries). Only the fields this connector maps
// are named; unknown fields are ignored.
type DailySummary struct {
	CalendarDate             string   `json:"calendarDate"`
	TotalSteps               *float64 `json:"totalSteps"`
	TotalDistanceMeters      *float64 `json:"totalDistanceMeters"`
	ActiveKilocalories       *float64 `json:"activeKilocalories"`
	BMRKilocalories          *float64 `json:"bmrKilocalories"`
	FloorsAscended           *float64 `json:"floorsAscended"`
	RestingHeartRate         *float64 `json:"restingHeartRate"`
	MinHeartRate             *float64 `json:"minHeartRate"`
	MaxHeartRate             *float64 `json:"maxHeartRate"`
	AverageStressLevel       *float64 `json:"averageStressLevel"`
	MaxStressLevel           *float64 `json:"maxStressLevel"`
	HighStressDuration       *float64 `json:"highStressDuration"`
	ModerateIntensityMinutes *float64 `json:"moderateIntensityMinutes"`
	VigorousIntensityMinutes *float64 `json:"vigorousIntensityMinutes"`
}

// SleepLine is one day of the Connect sleep response (sleep-service
// dailySleepData).
type SleepLine struct {
	DailySleepDTO struct {
		CalendarDate            string   `json:"calendarDate"`
		SleepTimeSeconds        *int64   `json:"sleepTimeSeconds"`
		DeepSleepSeconds        *int64   `json:"deepSleepSeconds"`
		LightSleepSeconds       *int64   `json:"lightSleepSeconds"`
		REMSleepSeconds         *int64   `json:"remSleepSeconds"`
		AwakeSleepSeconds       *int64   `json:"awakeSleepSeconds"`
		SleepStartTimestampGMT  *int64   `json:"sleepStartTimestampGMT"`
		SleepEndTimestampGMT    *int64   `json:"sleepEndTimestampGMT"`
		AvgSleepHRV             *float64 `json:"avgSleepHRV"`
		AvgSpO2                 *float64 `json:"averageSpO2Value"`
		AverageRespirationValue *float64 `json:"averageRespirationValue"`
		SleepScores             struct {
			Overall struct {
				Value *float64 `json:"value"`
			} `json:"overall"`
		} `json:"sleepScores"`
	} `json:"dailySleepDTO"`
	SleepLevels []SleepLevel `json:"sleepLevels"`
}

// SleepLevel is one stage interval of the night. activityLevel names the
// stage (see sleepStages).
type SleepLevel struct {
	StartGMT      string  `json:"startGMT"`
	EndGMT        string  `json:"endGMT"`
	ActivityLevel float64 `json:"activityLevel"`
}

// HRVLine is one day of the Connect HRV response (hrv-service).
type HRVLine struct {
	HRVSummary struct {
		CalendarDate    string   `json:"calendarDate"`
		LastNightAvg    *float64 `json:"lastNightAvg"`
		LastNight5MinHi *float64 `json:"lastNight5MinHigh"`
		WeeklyAvg       *float64 `json:"weeklyAvg"`
		Status          string   `json:"status"`
	} `json:"hrvSummary"`
}

// ActivityLine is one entry of the Connect activity list (activitylist-service
// search).
type ActivityLine struct {
	ActivityID   *int64 `json:"activityId"`
	ActivityName string `json:"activityName"`
	ActivityType struct {
		TypeKey string `json:"typeKey"`
	} `json:"activityType"`
	StartTimeGMT   string   `json:"startTimeGMT"`
	StartTimeLocal string   `json:"startTimeLocal"`
	Duration       *float64 `json:"duration"`
	Distance       *float64 `json:"distance"`
	Calories       *float64 `json:"calories"`
	AverageHR      *float64 `json:"averageHR"`
	MaxHR          *float64 `json:"maxHR"`
	AverageSpeed   *float64 `json:"averageSpeed"`
	ElevationGain  *float64 `json:"elevationGain"`
	DeviceID       *int64   `json:"deviceId"`
}
