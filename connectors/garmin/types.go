// SPDX-License-Identifier: AGPL-3.0-only

package garmin

// Data types this connector stores. Each is the name of a Health API summary
// type, as its pull endpoint (/wellness-api/rest/<type>) and its
// notification key spell it. A raw line of each type is one summary object
// as Garmin sent it, with any "userAccessToken" field (sent by the older
// OAuth 1.0a integrations) removed.
//
// Field names come from payload examples published by third parties and
// older public copies of the Health API specification; the current
// specification is behind Garmin's partner login (see
// docs/connectors/garmin.md).
const (
	DataTypeDailies        = "dailies"
	DataTypeEpochs         = "epochs"
	DataTypeSleeps         = "sleeps"
	DataTypeActivities     = "activities"
	DataTypeBodyComps      = "bodyComps"
	DataTypeHRV            = "hrv"
	DataTypePulseOx        = "pulseox"
	DataTypeRespiration    = "respiration"
	DataTypeUserMetrics    = "userMetrics"
	DataTypeBloodPressures = "bloodPressures"
	DataTypeSkinTemp       = "skinTemp"
	// DataTypeStressDetails is stored raw only: stress and Body Battery have
	// no canonical name.
	DataTypeStressDetails = "stressDetails"
)

// AllDataTypes is every summary type Fetch pulls, in the order it pulls them.
var AllDataTypes = []string{
	DataTypeDailies,
	DataTypeEpochs,
	DataTypeSleeps,
	DataTypeActivities,
	DataTypeBodyComps,
	DataTypeHRV,
	DataTypePulseOx,
	DataTypeRespiration,
	DataTypeUserMetrics,
	DataTypeBloodPressures,
	DataTypeSkinTemp,
	DataTypeStressDetails,
}

// notificationKeys maps a notification body key to the data type. Besides
// the endpoint names, third parties show "allDayRespiration" and "pulseOx"
// as notification keys (unverified against Garmin's specification).
var notificationKeys = map[string]string{
	"allDayRespiration": DataTypeRespiration,
	"pulseOx":           DataTypePulseOx,
	"bodyComposition":   DataTypeBodyComps,
	"bloodPressure":     DataTypeBloodPressures,
}

func init() {
	for _, dt := range AllDataTypes {
		notificationKeys[dt] = dt
	}
}

// Notification keys that are not summaries.
const (
	keyDeregistrations       = "deregistrations"
	keyUserPermissionsChange = "userPermissionsChange"
)

// Daily is one daily summary.
type Daily struct {
	SummaryID                  string             `json:"summaryId"`
	CalendarDate               string             `json:"calendarDate"`
	StartTimeInSeconds         *int64             `json:"startTimeInSeconds"`
	StartTimeOffsetInSeconds   *int64             `json:"startTimeOffsetInSeconds"`
	DurationInSeconds          *int64             `json:"durationInSeconds"`
	Steps                      *float64           `json:"steps"`
	DistanceInMeters           *float64           `json:"distanceInMeters"`
	ActiveKilocalories         *float64           `json:"activeKilocalories"`
	BMRKilocalories            *float64           `json:"bmrKilocalories"`
	FloorsClimbed              *float64           `json:"floorsClimbed"`
	RestingHeartRate           *float64           `json:"restingHeartRateInBeatsPerMinute"`
	MinHeartRate               *float64           `json:"minHeartRateInBeatsPerMinute"`
	AverageHeartRate           *float64           `json:"averageHeartRateInBeatsPerMinute"`
	MaxHeartRate               *float64           `json:"maxHeartRateInBeatsPerMinute"`
	AverageStressLevel         *float64           `json:"averageStressLevel"`
	MaxStressLevel             *float64           `json:"maxStressLevel"`
	ActiveTimeInSeconds        *float64           `json:"activeTimeInSeconds"`
	ModerateIntensityDuration  *float64           `json:"moderateIntensityDurationInSeconds"`
	VigorousIntensityDuration  *float64           `json:"vigorousIntensityDurationInSeconds"`
	StepsGoal                  *float64           `json:"stepsGoal"`
	TimeOffsetHeartRateSamples map[string]float64 `json:"timeOffsetHeartRateSamples"`
}

// Epoch is one 15 minute slice of one activity type.
type Epoch struct {
	SummaryID                string   `json:"summaryId"`
	ActivityType             string   `json:"activityType"`
	StartTimeInSeconds       *int64   `json:"startTimeInSeconds"`
	StartTimeOffsetInSeconds *int64   `json:"startTimeOffsetInSeconds"`
	DurationInSeconds        *int64   `json:"durationInSeconds"`
	ActiveTimeInSeconds      *float64 `json:"activeTimeInSeconds"`
	Steps                    *float64 `json:"steps"`
	DistanceInMeters         *float64 `json:"distanceInMeters"`
	ActiveKilocalories       *float64 `json:"activeKilocalories"`
	MET                      *float64 `json:"met"`
	Intensity                string   `json:"intensity"`
}

// SleepInterval is one span of sleepLevelsMap.
type SleepInterval struct {
	StartTimeInSeconds *int64 `json:"startTimeInSeconds"`
	EndTimeInSeconds   *int64 `json:"endTimeInSeconds"`
}

// Sleep is one sleep summary.
type Sleep struct {
	SummaryID                  string                     `json:"summaryId"`
	CalendarDate               string                     `json:"calendarDate"`
	StartTimeInSeconds         *int64                     `json:"startTimeInSeconds"`
	StartTimeOffsetInSeconds   *int64                     `json:"startTimeOffsetInSeconds"`
	DurationInSeconds          *int64                     `json:"durationInSeconds"`
	UnmeasurableSleepInSeconds *int64                     `json:"unmeasurableSleepInSeconds"`
	DeepSleepDurationInSeconds *int64                     `json:"deepSleepDurationInSeconds"`
	LightSleepDurationInSecs   *int64                     `json:"lightSleepDurationInSeconds"`
	REMSleepInSeconds          *int64                     `json:"remSleepInSeconds"`
	AwakeDurationInSeconds     *int64                     `json:"awakeDurationInSeconds"`
	Validation                 string                     `json:"validation"`
	SleepLevelsMap             map[string][]SleepInterval `json:"sleepLevelsMap"`
	TimeOffsetSleepSpo2        map[string]float64         `json:"timeOffsetSleepSpo2"`
	TimeOffsetSleepRespiration map[string]float64         `json:"timeOffsetSleepRespiration"`
	OverallSleepScore          *struct {
		Value        *float64 `json:"value"`
		QualifierKey string   `json:"qualifierKey"`
	} `json:"overallSleepScore"`
}

// Activity is one activity (workout) summary.
type Activity struct {
	SummaryID                string   `json:"summaryId"`
	ActivityID               *int64   `json:"activityId"`
	ActivityName             string   `json:"activityName"`
	ActivityType             string   `json:"activityType"`
	DeviceName               string   `json:"deviceName"`
	StartTimeInSeconds       *int64   `json:"startTimeInSeconds"`
	StartTimeOffsetInSeconds *int64   `json:"startTimeOffsetInSeconds"`
	DurationInSeconds        *int64   `json:"durationInSeconds"`
	DistanceInMeters         *float64 `json:"distanceInMeters"`
	ActiveKilocalories       *float64 `json:"activeKilocalories"`
	AverageHeartRate         *float64 `json:"averageHeartRateInBeatsPerMinute"`
	MaxHeartRate             *float64 `json:"maxHeartRateInBeatsPerMinute"`
	AverageSpeed             *float64 `json:"averageSpeedInMetersPerSecond"`
	MaxSpeed                 *float64 `json:"maxSpeedInMetersPerSecond"`
	TotalElevationGain       *float64 `json:"totalElevationGainInMeters"`
	TotalElevationLoss       *float64 `json:"totalElevationLossInMeters"`
	Steps                    *float64 `json:"steps"`
	Manual                   *bool    `json:"manual"`
}

// BodyComp is one body composition measurement.
type BodyComp struct {
	SummaryID                      string   `json:"summaryId"`
	MeasurementTimeInSeconds       *int64   `json:"measurementTimeInSeconds"`
	MeasurementTimeOffsetInSeconds *int64   `json:"measurementTimeOffsetInSeconds"`
	WeightInGrams                  *float64 `json:"weightInGrams"`
	BodyFatInPercent               *float64 `json:"bodyFatInPercent"`
	MuscleMassInGrams              *float64 `json:"muscleMassInGrams"`
	BoneMassInGrams                *float64 `json:"boneMassInGrams"`
	BodyWaterInPercent             *float64 `json:"bodyWaterInPercent"`
	BodyMassIndex                  *float64 `json:"bodyMassIndex"`
}

// HRV is one nightly HRV summary.
type HRV struct {
	SummaryID                string             `json:"summaryId"`
	CalendarDate             string             `json:"calendarDate"`
	StartTimeInSeconds       *int64             `json:"startTimeInSeconds"`
	StartTimeOffsetInSeconds *int64             `json:"startTimeOffsetInSeconds"`
	DurationInSeconds        *int64             `json:"durationInSeconds"`
	LastNightAvg             *float64           `json:"lastNightAvg"`
	LastNight5MinHigh        *float64           `json:"lastNight5MinHigh"`
	HRVValues                map[string]float64 `json:"hrvValues"`
}

// PulseOx is one pulse ox summary.
type PulseOx struct {
	SummaryID                string             `json:"summaryId"`
	CalendarDate             string             `json:"calendarDate"`
	StartTimeInSeconds       *int64             `json:"startTimeInSeconds"`
	StartTimeOffsetInSeconds *int64             `json:"startTimeOffsetInSeconds"`
	DurationInSeconds        *int64             `json:"durationInSeconds"`
	TimeOffsetSpo2Values     map[string]float64 `json:"timeOffsetSpo2Values"`
	OnDemand                 *bool              `json:"onDemand"`
}

// Respiration is one all-day respiration summary.
type Respiration struct {
	SummaryID                string             `json:"summaryId"`
	StartTimeInSeconds       *int64             `json:"startTimeInSeconds"`
	StartTimeOffsetInSeconds *int64             `json:"startTimeOffsetInSeconds"`
	DurationInSeconds        *int64             `json:"durationInSeconds"`
	TimeOffsetEpochToBreaths map[string]float64 `json:"timeOffsetEpochToBreaths"`
}

// UserMetrics is one day's user metrics.
type UserMetrics struct {
	SummaryID    string   `json:"summaryId"`
	CalendarDate string   `json:"calendarDate"`
	VO2Max       *float64 `json:"vo2Max"`
	VO2MaxCycle  *float64 `json:"vo2MaxCycling"`
	FitnessAge   *float64 `json:"fitnessAge"`
	Enhanced     *bool    `json:"enhanced"`
}

// BloodPressure is one blood pressure reading.
type BloodPressure struct {
	SummaryID                      string   `json:"summaryId"`
	MeasurementTimeInSeconds       *int64   `json:"measurementTimeInSeconds"`
	MeasurementTimeOffsetInSeconds *int64   `json:"measurementTimeOffsetInSeconds"`
	Systolic                       *float64 `json:"systolic"`
	Diastolic                      *float64 `json:"diastolic"`
	Pulse                          *float64 `json:"pulse"`
	SourceType                     string   `json:"sourceType"`
}

// SkinTemp is one nightly skin temperature summary.
type SkinTemp struct {
	SummaryID                string   `json:"summaryId"`
	CalendarDate             string   `json:"calendarDate"`
	StartTimeInSeconds       *int64   `json:"startTimeInSeconds"`
	StartTimeOffsetInSeconds *int64   `json:"startTimeOffsetInSeconds"`
	DurationInSeconds        *int64   `json:"durationInSeconds"`
	AvgDeviationCelsius      *float64 `json:"avgDeviationCelsius"`
}
