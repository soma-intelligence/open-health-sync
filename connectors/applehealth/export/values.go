// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"math"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // HKTimeZone names must resolve on a host without a zone database
)

// exportLayout is how export.xml writes a date: local wall time and the UTC
// offset the phone applied when it wrote the export.
const exportLayout = "2006-01-02 15:04:05 -0700"

// parseExportTime parses an export.xml date. The instant is exact; the
// offset is the one of the export, not necessarily the zone the sample was
// recorded in (that is HKTimeZone, when HealthKit kept it).
func parseExportTime(s string) (time.Time, error) {
	return time.Parse(exportLayout, strings.TrimSpace(s))
}

// wireTime formats an instant the way the upload contract expects: RFC 3339
// in UTC.
func wireTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// categoryValues maps the HKCategoryValue names export.xml writes to the raw
// integers HealthKit uses, which is what the upload contract carries. The
// names carry their enum's name, so one table serves every category type.
var categoryValues = map[string]int{
	"HKCategoryValueNotApplicable": 0,

	"HKCategoryValueSleepAnalysisInBed":             0,
	"HKCategoryValueSleepAnalysisAsleep":            1, // before iOS 16
	"HKCategoryValueSleepAnalysisAsleepUnspecified": 1,
	"HKCategoryValueSleepAnalysisAwake":             2,
	"HKCategoryValueSleepAnalysisAsleepCore":        3,
	"HKCategoryValueSleepAnalysisAsleepDeep":        4,
	"HKCategoryValueSleepAnalysisAsleepREM":         5,

	"HKCategoryValueAppleStandHourStood": 0,
	"HKCategoryValueAppleStandHourIdle":  1,

	"HKCategoryValueMenstrualFlowUnspecified": 1,
	"HKCategoryValueMenstrualFlowLight":       2,
	"HKCategoryValueMenstrualFlowMedium":      3,
	"HKCategoryValueMenstrualFlowHeavy":       4,
	"HKCategoryValueMenstrualFlowNone":        5,

	"HKCategoryValueCervicalMucusQualityDry":      1,
	"HKCategoryValueCervicalMucusQualitySticky":   2,
	"HKCategoryValueCervicalMucusQualityCreamy":   3,
	"HKCategoryValueCervicalMucusQualityWatery":   4,
	"HKCategoryValueCervicalMucusQualityEggWhite": 5,

	"HKCategoryValueOvulationTestResultNegative":                1,
	"HKCategoryValueOvulationTestResultPositive":                2, // before iOS 13.1
	"HKCategoryValueOvulationTestResultLuteinizingHormoneSurge": 2,
	"HKCategoryValueOvulationTestResultIndeterminate":           3,
	"HKCategoryValueOvulationTestResultEstrogenSurge":           4,

	"HKCategoryValuePregnancyTestResultNegative":      1,
	"HKCategoryValuePregnancyTestResultPositive":      2,
	"HKCategoryValuePregnancyTestResultIndeterminate": 3,

	"HKCategoryValueProgesteroneTestResultNegative":      1,
	"HKCategoryValueProgesteroneTestResultPositive":      2,
	"HKCategoryValueProgesteroneTestResultIndeterminate": 3,

	"HKCategoryValueSeverityUnspecified": 0,
	"HKCategoryValueSeverityNotPresent":  1,
	"HKCategoryValueSeverityMild":        2,
	"HKCategoryValueSeverityModerate":    3,
	"HKCategoryValueSeveritySevere":      4,

	"HKCategoryValuePresencePresent":    0,
	"HKCategoryValuePresenceNotPresent": 1,

	"HKCategoryValueAppetiteChangesUnspecified": 0,
	"HKCategoryValueAppetiteChangesNoChange":    1,
	"HKCategoryValueAppetiteChangesDecreased":   2,
	"HKCategoryValueAppetiteChangesIncreased":   3,

	"HKCategoryValueAudioExposureEventLoudEnvironment":             1,
	"HKCategoryValueEnvironmentalAudioExposureEventMomentaryLimit": 1,
	"HKCategoryValueLowCardioFitnessEventLowFitness":               1,
}

// categoryValue returns the raw integer of a category value as export.xml
// writes it: an HKCategoryValue name, or already a number.
func categoryValue(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if v, ok := categoryValues[s]; ok {
		return v, true
	}
	v, err := strconv.Atoi(s)
	return v, err == nil
}

// workoutActivityTypes maps the HKWorkoutActivityType names export.xml writes
// to HealthKit's raw values. An unknown name becomes HKWorkoutActivityTypeOther.
var workoutActivityTypes = map[string]int{
	"AmericanFootball": 1, "Archery": 2, "AustralianFootball": 3, "Badminton": 4,
	"Baseball": 5, "Basketball": 6, "Bowling": 7, "Boxing": 8, "Climbing": 9,
	"Cricket": 10, "CrossTraining": 11, "Curling": 12, "Cycling": 13, "Dance": 14,
	"DanceInspiredTraining": 15, "Elliptical": 16, "EquestrianSports": 17,
	"Fencing": 18, "Fishing": 19, "FunctionalStrengthTraining": 20, "Golf": 21,
	"Gymnastics": 22, "Handball": 23, "Hiking": 24, "Hockey": 25, "Hunting": 26,
	"Lacrosse": 27, "MartialArts": 28, "MindAndBody": 29,
	"MixedMetabolicCardioTraining": 30, "PaddleSports": 31, "Play": 32,
	"PreparationAndRecovery": 33, "Racquetball": 34, "Rowing": 35, "Rugby": 36,
	"Running": 37, "Sailing": 38, "SkatingSports": 39, "SnowSports": 40,
	"Soccer": 41, "Softball": 42, "Squash": 43, "StairClimbing": 44,
	"SurfingSports": 45, "Swimming": 46, "TableTennis": 47, "Tennis": 48,
	"TrackAndField": 49, "TraditionalStrengthTraining": 50, "Volleyball": 51,
	"Walking": 52, "WaterFitness": 53, "WaterPolo": 54, "WaterSports": 55,
	"Wrestling": 56, "Yoga": 57, "Barre": 58, "CoreTraining": 59,
	"CrossCountrySkiing": 60, "DownhillSkiing": 61, "Flexibility": 62,
	"HighIntensityIntervalTraining": 63, "JumpRope": 64, "Kickboxing": 65,
	"Pilates": 66, "Snowboarding": 67, "Stairs": 68, "StepTraining": 69,
	"WheelchairWalkPace": 70, "WheelchairRunPace": 71, "TaiChi": 72,
	"MixedCardio": 73, "HandCycling": 74, "DiscSports": 75, "FitnessGaming": 76,
	"CardioDance": 77, "SocialDance": 78, "Pickleball": 79, "Cooldown": 80,
	"SwimBikeRun": 82, "Transition": 83, "UnderwaterDiving": 84, "Other": 3000,
}

// workoutActivityType returns the raw HKWorkoutActivityType of a name
// ("HKWorkoutActivityTypeRunning") or of a number.
func workoutActivityType(s string) int {
	s = strings.TrimSpace(s)
	if v, ok := workoutActivityTypes[strings.TrimPrefix(s, "HKWorkoutActivityType")]; ok {
		return v
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return workoutActivityTypes["Other"]
}

// Unit factors to the units the workout line carries: metres, kilocalories
// and seconds.
var (
	metresPer = map[string]float64{
		"m": 1, "km": 1000, "cm": 0.01, "mm": 0.001,
		"mi": 1609.344, "yd": 0.9144, "ft": 0.3048, "in": 0.0254,
	}
	kcalPer = map[string]float64{
		"kcal": 1, "Cal": 1, "cal": 0.001, "kJ": 1 / 4.184, "J": 1 / 4184.0,
	}
	secondsPer = map[string]float64{
		"s": 1, "ms": 0.001, "min": 60, "hr": 3600, "h": 3600, "d": 86400,
	}
)

// convert parses a number and converts it with factors. ok is false for an
// empty or unparseable value or an unknown unit.
func convert(value, unit string, factors map[string]float64) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, false
	}
	f, ok := factors[strings.TrimSpace(unit)]
	if !ok {
		return 0, false
	}
	// Rounded to a millionth: 418.4 kJ is 100 kcal, not 99.99999999999999.
	return math.Round(v*f*1e6) / 1e6, true
}

// deviceFields parses the HKDevice description export.xml writes:
//
//	<<HKDevice: 0x6000...>, name:Apple Watch, manufacturer:Apple Inc., model:Watch, hardware:Watch7,1, software:10.0>
//
// into its named parts. A part that does not start with a known key belongs
// to the previous one (a name may hold ", ").
func deviceFields(s string) map[string]string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if i := strings.Index(s, ">, "); strings.HasPrefix(s, "<<") && i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimSuffix(s, ">")
	keys := []string{"name", "manufacturer", "model", "hardware", "software", "localIdentifier", "UDIDeviceIdentifier", "creation date"}
	out := map[string]string{}
	last := ""
	for _, part := range strings.Split(s, ", ") {
		matched := false
		for _, k := range keys {
			if strings.HasPrefix(part, k+":") {
				last = k
				out[k] = strings.TrimSpace(part[len(k)+1:])
				matched = true
				break
			}
		}
		if !matched && last != "" {
			out[last] += ", " + part
		}
	}
	return out
}

// parseClock parses the time of day a heartbeat series beat carries
// ("11:59:59.50 PM", or "23:59:59,50" in a 24-hour locale) into the time
// since midnight.
func parseClock(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	upper := strings.ToUpper(s)
	half := ""
	for _, suffix := range []string{"AM", "PM"} {
		if strings.HasSuffix(upper, suffix) {
			half = suffix
			s = strings.TrimSpace(s[:len(s)-2])
			break
		}
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(strings.Replace(parts[2], ",", ".", 1), 64)
	if err1 != nil || err2 != nil || err3 != nil || h < 0 || h > 23 || m < 0 || m > 59 || sec < 0 || sec >= 61 {
		return 0, false
	}
	switch half {
	case "AM":
		if h == 12 {
			h = 0
		}
	case "PM":
		if h < 12 {
			h += 12
		}
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute +
		time.Duration(math.Round(sec*1e6))*time.Microsecond, true
}
