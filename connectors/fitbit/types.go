// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Wire types of the Google Health API v4 (field names from the API's
// discovery document). The API writes int64 values as JSON strings
// ("count": "1234") and doubles as numbers; num reads both.

// num is a number sent as a JSON number or a decimal string. null and ""
// leave it unset (use *num).
type num float64

// UnmarshalJSON reads 12, 12.5, "12" or "12.5".
func (n *num) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		b = []byte(strings.TrimSpace(s))
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("not a number: %s", clip(string(b), 40))
	}
	*n = num(f)
	return nil
}

// val returns the value of an optional number and whether it is set.
func val(n *num) (float64, bool) {
	if n == nil {
		return 0, false
	}
	return float64(*n), true
}

// interval is an ObservationTimeInterval or SessionTimeInterval.
type interval struct {
	StartTime      string `json:"startTime"`
	StartUTCOffset string `json:"startUtcOffset"`
	EndTime        string `json:"endTime"`
	EndUTCOffset   string `json:"endUtcOffset"`
}

// sampleTime is an ObservationSampleTime.
type sampleTime struct {
	PhysicalTime string `json:"physicalTime"`
	UTCOffset    string `json:"utcOffset"`
}

// dataSource is where a data point came from.
type dataSource struct {
	RecordingMethod string `json:"recordingMethod"`
	Platform        string `json:"platform"`
	Device          *struct {
		FormFactor   string `json:"formFactor"`
		DisplayName  string `json:"displayName"`
		Manufacturer string `json:"manufacturer"`
	} `json:"device"`
	Application *struct {
		PackageName string `json:"packageName"`
	} `json:"application"`
}

// dataPoint is the envelope of one point: its resource name (only for
// identifiable types such as exercise and sleep), its source and the union
// member holding the values (decoded by each mapper).
type dataPoint struct {
	Name       string      `json:"name"`
	DataSource *dataSource `json:"dataSource"`
}

type intervalCount struct {
	Interval interval `json:"interval"`
	Count    *num     `json:"count"`
}

type distanceValue struct {
	Interval    interval `json:"interval"`
	Millimeters *num     `json:"millimeters"`
}

type energyValue struct {
	Interval interval `json:"interval"`
	Kcal     *num     `json:"kcal"`
}

type heartRateValue struct {
	SampleTime     sampleTime `json:"sampleTime"`
	BeatsPerMinute *num       `json:"beatsPerMinute"`
	Metadata       *struct {
		MotionContext  string `json:"motionContext"`
		SensorLocation string `json:"sensorLocation"`
	} `json:"metadata"`
}

type hrvValue struct {
	SampleTime                                        sampleTime `json:"sampleTime"`
	RootMeanSquareOfSuccessiveDifferencesMilliseconds *num       `json:"rootMeanSquareOfSuccessiveDifferencesMilliseconds"`
	StandardDeviationMilliseconds                     *num       `json:"standardDeviationMilliseconds"`
}

type percentageSample struct {
	SampleTime sampleTime `json:"sampleTime"`
	Percentage *num       `json:"percentage"`
}

type weightValue struct {
	SampleTime  sampleTime `json:"sampleTime"`
	WeightGrams *num       `json:"weightGrams"`
}

type heightValue struct {
	SampleTime        sampleTime `json:"sampleTime"`
	HeightMillimeters *num       `json:"heightMillimeters"`
}

type vo2MaxValue struct {
	SampleTime        sampleTime `json:"sampleTime"`
	VO2Max            *num       `json:"vo2Max"`
	MeasurementMethod string     `json:"measurementMethod"`
}

type bloodGlucoseValue struct {
	SampleTime                         sampleTime `json:"sampleTime"`
	BloodGlucoseMilligramsPerDeciliter *num       `json:"bloodGlucoseMilligramsPerDeciliter"`
	Specimen                           string     `json:"specimen"`
	MealType                           string     `json:"mealType"`
	MeasurementSource                  string     `json:"measurementSource"`
	MeasurementTiming                  string     `json:"measurementTiming"`
}

type coreTemperatureValue struct {
	SampleTime          sampleTime `json:"sampleTime"`
	TemperatureCelsius  *num       `json:"temperatureCelsius"`
	MeasurementLocation string     `json:"measurementLocation"`
}

type dailyRestingHR struct {
	Date           civilDate `json:"date"`
	BeatsPerMinute *num      `json:"beatsPerMinute"`
}

type dailyHRV struct {
	Date                                                       civilDate `json:"date"`
	AverageHeartRateVariabilityMilliseconds                    *num      `json:"averageHeartRateVariabilityMilliseconds"`
	DeepSleepRootMeanSquareOfSuccessiveDifferencesMilliseconds *num      `json:"deepSleepRootMeanSquareOfSuccessiveDifferencesMilliseconds"`
	NonRemHeartRateBeatsPerMinute                              *num      `json:"nonRemHeartRateBeatsPerMinute"`
	Entropy                                                    *num      `json:"entropy"`
}

type dailyOxygen struct {
	Date                 civilDate `json:"date"`
	AveragePercentage    *num      `json:"averagePercentage"`
	LowerBoundPercentage *num      `json:"lowerBoundPercentage"`
	UpperBoundPercentage *num      `json:"upperBoundPercentage"`
}

type dailyRespiratory struct {
	Date             civilDate `json:"date"`
	BreathsPerMinute *num      `json:"breathsPerMinute"`
}

type dailyVO2Max struct {
	Date               civilDate `json:"date"`
	VO2Max             *num      `json:"vo2Max"`
	CardioFitnessLevel string    `json:"cardioFitnessLevel"`
	Estimated          *bool     `json:"estimated"`
}

type sleepTemperature struct {
	Date                       civilDate `json:"date"`
	NightlyTemperatureCelsius  *num      `json:"nightlyTemperatureCelsius"`
	BaselineTemperatureCelsius *num      `json:"baselineTemperatureCelsius"`
}

type sleepStage struct {
	Type      string `json:"type"`
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
}

type sleepValue struct {
	Interval interval     `json:"interval"`
	Type     string       `json:"type"`
	Stages   []sleepStage `json:"stages"`
	Metadata *struct {
		MainSleep      *bool  `json:"mainSleep"`
		Nap            *bool  `json:"nap"`
		ManuallyEdited *bool  `json:"manuallyEdited"`
		Processed      *bool  `json:"processed"`
		StagesStatus   string `json:"stagesStatus"`
		ExternalID     string `json:"externalId"`
	} `json:"metadata"`
	Summary *struct {
		MinutesInSleepPeriod *num `json:"minutesInSleepPeriod"`
		MinutesAsleep        *num `json:"minutesAsleep"`
		MinutesAwake         *num `json:"minutesAwake"`
		MinutesToFallAsleep  *num `json:"minutesToFallAsleep"`
		MinutesAfterWakeUp   *num `json:"minutesAfterWakeUp"`
		StagesSummary        []struct {
			Type    string `json:"type"`
			Minutes *num   `json:"minutes"`
			Count   *num   `json:"count"`
		} `json:"stagesSummary"`
	} `json:"summary"`
}

type exerciseValue struct {
	Interval       interval `json:"interval"`
	ExerciseType   string   `json:"exerciseType"`
	DisplayName    string   `json:"displayName"`
	ActiveDuration string   `json:"activeDuration"`
	MetricsSummary *struct {
		CaloriesKcal                     *num `json:"caloriesKcal"`
		DistanceMillimeters              *num `json:"distanceMillimeters"`
		Steps                            *num `json:"steps"`
		AverageSpeedMillimetersPerSecond *num `json:"averageSpeedMillimetersPerSecond"`
		AverageHeartRateBeatsPerMinute   *num `json:"averageHeartRateBeatsPerMinute"`
		ElevationGainMillimeters         *num `json:"elevationGainMillimeters"`
		ActiveZoneMinutes                *num `json:"activeZoneMinutes"`
		RunVO2Max                        *num `json:"runVo2Max"`
		TotalSwimLengths                 *num `json:"totalSwimLengths"`
	} `json:"metricsSummary"`
	ExerciseMetadata *struct {
		HasGPS *bool `json:"hasGps"`
	} `json:"exerciseMetadata"`
}
