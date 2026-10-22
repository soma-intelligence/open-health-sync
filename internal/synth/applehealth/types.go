// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

// The HealthKit vocabulary below follows Apple's documentation:
//
//   - quantity types and their unit dimension and aggregation style:
//     https://developer.apple.com/documentation/healthkit/hkquantitytypeidentifier
//   - unit strings: https://developer.apple.com/documentation/healthkit/hkunit/init(from:)-9qont
//   - category types and values:
//     https://developer.apple.com/documentation/healthkit/hkcategorytypeidentifier,
//     https://developer.apple.com/documentation/healthkit/hkcategoryvaluesleepanalysis
//   - workout activity types: https://developer.apple.com/documentation/healthkit/hkworkoutactivitytype
//   - the export file itself: the DTD Apple writes at the top of every
//     export.xml (HealthKit Export Version 17).

// quantity describes one HealthKit quantity type.
type quantity struct {
	// ID is the identifier without its HKQuantityTypeIdentifier prefix.
	ID string
	// Unit is the unit string a metric locale's export writes; Alt are other
	// units HealthKit allows for the same dimension, each with the factor that
	// turns a value in Unit into it.
	Unit string
	Alt  []altUnit
	// Cumulative types are sums over their interval (steps), the others
	// discrete readings (heart rate).
	Cumulative bool
	// Lo and Hi bound a plausible value, in Unit, for types the timeline does
	// not drive.
	Lo, Hi float64
	// Manual says the Health app or a third-party app writes the type, not a
	// sensor: a few entries a week at most.
	Manual bool
}

type altUnit struct {
	Unit   string
	Factor float64
	// Offset is added after Factor (temperatures).
	Offset float64
}

const (
	prefixQuantity    = "HKQuantityTypeIdentifier"
	prefixCategory    = "HKCategoryTypeIdentifier"
	prefixCorrelation = "HKCorrelationTypeIdentifier"
	prefixWorkout     = "HKWorkoutActivityType"
)

var (
	lb      = altUnit{Unit: "lb", Factor: 2.2046226218}
	st      = altUnit{Unit: "st", Factor: 0.1574730444}
	gToOz   = altUnit{Unit: "oz", Factor: 0.0352739619}
	kmToMi  = altUnit{Unit: "mi", Factor: 0.6213711922}
	kmToM   = altUnit{Unit: "m", Factor: 1000}
	mToFt   = altUnit{Unit: "ft", Factor: 3.280839895}
	mToYd   = altUnit{Unit: "yd", Factor: 1.0936132983}
	cmToIn  = altUnit{Unit: "in", Factor: 0.3937007874}
	cmToM   = altUnit{Unit: "m", Factor: 0.01}
	kcalKJ  = altUnit{Unit: "kJ", Factor: 4.184}
	kcalCal = altUnit{Unit: "Cal", Factor: 1}
	degF    = altUnit{Unit: "degF", Factor: 1.8, Offset: 32}
	mlFlOz  = altUnit{Unit: "fl_oz_us", Factor: 0.0338140227}
	mlL     = altUnit{Unit: "L", Factor: 0.001}
	mgMmol  = altUnit{Unit: "mmol<180.15588000005408>/L", Factor: 1 / 18.015588000005408}
	kmhMph  = altUnit{Unit: "mi/hr", Factor: 0.6213711922}
	kmhMs   = altUnit{Unit: "m/s", Factor: 1 / 3.6}
	msS     = altUnit{Unit: "s", Factor: 0.001}
	mgG     = altUnit{Unit: "g", Factor: 0.001}
	mcgMg   = altUnit{Unit: "mg", Factor: 0.001}
	minHr   = altUnit{Unit: "hr", Factor: 1.0 / 60}
	minS    = altUnit{Unit: "s", Factor: 60}
	mmHgKPa = altUnit{Unit: "kPa", Factor: 0.1333223684}
)

// quantities lists every quantity type the generator writes. A type the
// documentation lists and the generator does not drive from the timeline gets
// a few plausible entries, so an export carries the whole vocabulary.
var quantities = []quantity{
	// Activity.
	{ID: "StepCount", Unit: "count", Cumulative: true},
	{ID: "DistanceWalkingRunning", Unit: "km", Alt: []altUnit{kmToMi, kmToM}, Cumulative: true},
	{ID: "DistanceCycling", Unit: "km", Alt: []altUnit{kmToMi}, Cumulative: true},
	{ID: "DistanceSwimming", Unit: "m", Alt: []altUnit{mToYd}, Cumulative: true},
	{ID: "DistanceRowing", Unit: "m", Cumulative: true},
	{ID: "DistanceWheelchair", Unit: "km", Cumulative: true, Lo: 0.1, Hi: 3, Manual: true},
	{ID: "DistanceDownhillSnowSports", Unit: "km", Cumulative: true, Lo: 0.5, Hi: 4, Manual: true},
	{ID: "DistanceCrossCountrySkiing", Unit: "km", Cumulative: true, Lo: 0.5, Hi: 4, Manual: true},
	{ID: "DistancePaddleSports", Unit: "km", Cumulative: true, Lo: 0.5, Hi: 4, Manual: true},
	{ID: "DistanceSkatingSports", Unit: "km", Cumulative: true, Lo: 0.5, Hi: 4, Manual: true},
	{ID: "PushCount", Unit: "count", Cumulative: true, Lo: 50, Hi: 400, Manual: true},
	{ID: "SwimmingStrokeCount", Unit: "count", Cumulative: true},
	{ID: "FlightsClimbed", Unit: "count", Cumulative: true},
	{ID: "ActiveEnergyBurned", Unit: "kcal", Alt: []altUnit{kcalKJ, kcalCal}, Cumulative: true},
	{ID: "BasalEnergyBurned", Unit: "kcal", Alt: []altUnit{kcalKJ, kcalCal}, Cumulative: true},
	{ID: "AppleExerciseTime", Unit: "min", Alt: []altUnit{minS}, Cumulative: true},
	{ID: "AppleMoveTime", Unit: "min", Cumulative: true, Lo: 1, Hi: 1, Manual: true},
	{ID: "AppleStandTime", Unit: "min", Cumulative: true},
	{ID: "TimeInDaylight", Unit: "min", Cumulative: true},
	{ID: "NikeFuel", Unit: "count", Cumulative: true, Lo: 100, Hi: 900, Manual: true},
	{ID: "PhysicalEffort", Unit: "kcal/hr·kg", Lo: 1, Hi: 12},
	{ID: "VO2Max", Unit: "mL/min·kg", Alt: []altUnit{{Unit: "ml/kg/min", Factor: 1}}},
	{ID: "WorkoutEffortScore", Unit: "appleEffortScore", Lo: 1, Hi: 10},
	{ID: "EstimatedWorkoutEffortScore", Unit: "appleEffortScore", Lo: 1, Hi: 10},
	{ID: "CyclingCadence", Unit: "count/min"},
	{ID: "CyclingPower", Unit: "W"},
	{ID: "CyclingSpeed", Unit: "km/hr", Alt: []altUnit{kmhMph, kmhMs}},
	{ID: "CyclingFunctionalThresholdPower", Unit: "W", Lo: 180, Hi: 300, Manual: true},
	{ID: "RunningSpeed", Unit: "km/hr", Alt: []altUnit{kmhMph, kmhMs}},
	{ID: "RunningPower", Unit: "W"},
	{ID: "RunningStrideLength", Unit: "m", Alt: []altUnit{mToFt}},
	{ID: "RunningVerticalOscillation", Unit: "cm", Alt: []altUnit{cmToIn}},
	{ID: "RunningGroundContactTime", Unit: "ms", Alt: []altUnit{msS}},
	{ID: "RowingSpeed", Unit: "m/s", Lo: 2.5, Hi: 4.5, Manual: true},
	{ID: "PaddleSportsSpeed", Unit: "km/hr", Lo: 4, Hi: 9, Manual: true},
	{ID: "CrossCountrySkiingSpeed", Unit: "km/hr", Lo: 6, Hi: 15, Manual: true},
	{ID: "UnderwaterDepth", Unit: "m", Lo: 0.5, Hi: 12, Manual: true},
	{ID: "WaterTemperature", Unit: "degC", Alt: []altUnit{degF}, Lo: 18, Hi: 29, Manual: true},
	// Mobility.
	{ID: "WalkingSpeed", Unit: "km/hr", Alt: []altUnit{kmhMph}},
	{ID: "WalkingStepLength", Unit: "cm", Alt: []altUnit{cmToIn}},
	{ID: "WalkingDoubleSupportPercentage", Unit: "%", Lo: 0.24, Hi: 0.36},
	{ID: "WalkingAsymmetryPercentage", Unit: "%", Lo: 0, Hi: 0.08},
	{ID: "AppleWalkingSteadiness", Unit: "%", Lo: 0.8, Hi: 1},
	{ID: "StairAscentSpeed", Unit: "m/s", Alt: []altUnit{{Unit: "ft/s", Factor: 3.280839895}}, Lo: 0.2, Hi: 0.6},
	{ID: "StairDescentSpeed", Unit: "m/s", Lo: 0.2, Hi: 0.7},
	{ID: "SixMinuteWalkTestDistance", Unit: "m", Alt: []altUnit{mToYd}, Lo: 420, Hi: 620},
	// Heart.
	{ID: "HeartRate", Unit: "count/min"},
	{ID: "RestingHeartRate", Unit: "count/min"},
	{ID: "WalkingHeartRateAverage", Unit: "count/min"},
	{ID: "HeartRateVariabilitySDNN", Unit: "ms", Alt: []altUnit{msS}},
	{ID: "HeartRateVariabilityRMSSD", Unit: "ms"},
	{ID: "HeartRateRecoveryOneMinute", Unit: "count/min", Lo: 15, Hi: 45},
	{ID: "AtrialFibrillationBurden", Unit: "%", Lo: 0.01, Hi: 0.02, Manual: true},
	{ID: "PeripheralPerfusionIndex", Unit: "%", Lo: 0.02, Hi: 0.08, Manual: true},
	// Respiratory and vitals.
	{ID: "OxygenSaturation", Unit: "%"},
	{ID: "RespiratoryRate", Unit: "count/min"},
	{ID: "AppleSleepingBreathingDisturbances", Unit: "count", Lo: 0, Hi: 5},
	{ID: "AppleSleepingWristTemperature", Unit: "degC", Alt: []altUnit{degF}},
	{ID: "BodyTemperature", Unit: "degC", Alt: []altUnit{degF}, Lo: 36.3, Hi: 37.4, Manual: true},
	{ID: "BasalBodyTemperature", Unit: "degC", Alt: []altUnit{degF}, Lo: 36.2, Hi: 36.9, Manual: true},
	{ID: "BloodPressureSystolic", Unit: "mmHg", Alt: []altUnit{mmHgKPa}},
	{ID: "BloodPressureDiastolic", Unit: "mmHg", Alt: []altUnit{mmHgKPa}},
	{ID: "BloodGlucose", Unit: "mg/dL", Alt: []altUnit{mgMmol}},
	{ID: "InsulinDelivery", Unit: "IU", Cumulative: true, Lo: 1, Hi: 12, Manual: true},
	{ID: "BloodAlcoholContent", Unit: "%", Lo: 0.0001, Hi: 0.0006, Manual: true},
	{ID: "ElectrodermalActivity", Unit: "mcS", Lo: 1, Hi: 20, Manual: true},
	{ID: "ForcedVitalCapacity", Unit: "L", Lo: 3.5, Hi: 5.5, Manual: true},
	{ID: "ForcedExpiratoryVolume1", Unit: "L", Lo: 2.8, Hi: 4.5, Manual: true},
	{ID: "PeakExpiratoryFlowRate", Unit: "L/min", Lo: 380, Hi: 600, Manual: true},
	{ID: "InhalerUsage", Unit: "count", Cumulative: true, Lo: 1, Hi: 2, Manual: true},
	{ID: "NumberOfTimesFallen", Unit: "count", Cumulative: true, Lo: 1, Hi: 1, Manual: true},
	{ID: "NumberOfAlcoholicBeverages", Unit: "count", Cumulative: true, Lo: 1, Hi: 3, Manual: true},
	{ID: "UVExposure", Unit: "count", Lo: 1, Hi: 8, Manual: true},
	// Hearing.
	{ID: "EnvironmentalAudioExposure", Unit: "dBASPL"},
	{ID: "HeadphoneAudioExposure", Unit: "dBASPL"},
	{ID: "EnvironmentalSoundReduction", Unit: "dBASPL", Lo: 5, Hi: 29},
	// Body measurements.
	{ID: "BodyMass", Unit: "kg", Alt: []altUnit{lb, st, {Unit: "g", Factor: 1000}}},
	{ID: "BodyMassIndex", Unit: "count"},
	{ID: "BodyFatPercentage", Unit: "%"},
	{ID: "LeanBodyMass", Unit: "kg", Alt: []altUnit{lb}},
	{ID: "Height", Unit: "cm", Alt: []altUnit{cmToIn, cmToM, {Unit: "ft", Factor: 0.0328083990}}},
	{ID: "WaistCircumference", Unit: "cm", Alt: []altUnit{cmToIn}, Lo: 70, Hi: 110, Manual: true},
	// Nutrition: driven by the meals.
	{ID: "DietaryEnergyConsumed", Unit: "kcal", Alt: []altUnit{kcalKJ, kcalCal}, Cumulative: true},
	{ID: "DietaryProtein", Unit: "g", Alt: []altUnit{gToOz}, Cumulative: true},
	{ID: "DietaryCarbohydrates", Unit: "g", Cumulative: true},
	{ID: "DietaryFatTotal", Unit: "g", Cumulative: true},
	{ID: "DietaryFatSaturated", Unit: "g", Cumulative: true},
	{ID: "DietaryFatMonounsaturated", Unit: "g", Cumulative: true, Lo: 2, Hi: 20},
	{ID: "DietaryFatPolyunsaturated", Unit: "g", Cumulative: true, Lo: 1, Hi: 12},
	{ID: "DietaryCholesterol", Unit: "mg", Alt: []altUnit{mgG}, Cumulative: true, Lo: 10, Hi: 300},
	{ID: "DietaryFiber", Unit: "g", Cumulative: true},
	{ID: "DietarySugar", Unit: "g", Cumulative: true},
	{ID: "DietarySodium", Unit: "mg", Alt: []altUnit{mgG}, Cumulative: true},
	{ID: "DietaryCaffeine", Unit: "mg", Cumulative: true},
	{ID: "DietaryWater", Unit: "mL", Alt: []altUnit{mlFlOz, mlL}, Cumulative: true},
	{ID: "DietaryPotassium", Unit: "mg", Cumulative: true, Lo: 100, Hi: 1200},
	{ID: "DietaryCalcium", Unit: "mg", Cumulative: true, Lo: 50, Hi: 500},
	{ID: "DietaryIron", Unit: "mg", Cumulative: true, Lo: 0.5, Hi: 6},
	{ID: "DietaryMagnesium", Unit: "mg", Cumulative: true, Lo: 15, Hi: 140},
	{ID: "DietaryPhosphorus", Unit: "mg", Cumulative: true, Lo: 30, Hi: 450},
	{ID: "DietaryZinc", Unit: "mg", Cumulative: true, Lo: 0.2, Hi: 4},
	{ID: "DietaryCopper", Unit: "mg", Cumulative: true, Lo: 0.05, Hi: 0.4},
	{ID: "DietaryManganese", Unit: "mg", Cumulative: true, Lo: 0.1, Hi: 1.5},
	{ID: "DietarySelenium", Unit: "mcg", Alt: []altUnit{mcgMg}, Cumulative: true, Lo: 2, Hi: 50},
	{ID: "DietaryChromium", Unit: "mcg", Cumulative: true, Lo: 1, Hi: 15},
	{ID: "DietaryMolybdenum", Unit: "mcg", Cumulative: true, Lo: 5, Hi: 30},
	{ID: "DietaryChloride", Unit: "mg", Cumulative: true, Lo: 100, Hi: 900},
	{ID: "DietaryIodine", Unit: "mcg", Cumulative: true, Lo: 10, Hi: 60},
	{ID: "DietaryVitaminA", Unit: "mcg", Cumulative: true, Lo: 20, Hi: 400},
	{ID: "DietaryVitaminB6", Unit: "mg", Cumulative: true, Lo: 0.05, Hi: 0.6},
	{ID: "DietaryVitaminB12", Unit: "mcg", Cumulative: true, Lo: 0.2, Hi: 2},
	{ID: "DietaryVitaminC", Unit: "mg", Cumulative: true, Lo: 5, Hi: 90},
	{ID: "DietaryVitaminD", Unit: "mcg", Cumulative: true, Lo: 0.2, Hi: 5},
	{ID: "DietaryVitaminE", Unit: "mg", Cumulative: true, Lo: 0.2, Hi: 6},
	{ID: "DietaryVitaminK", Unit: "mcg", Cumulative: true, Lo: 1, Hi: 60},
	{ID: "DietaryThiamin", Unit: "mg", Cumulative: true, Lo: 0.05, Hi: 0.7},
	{ID: "DietaryRiboflavin", Unit: "mg", Cumulative: true, Lo: 0.05, Hi: 0.8},
	{ID: "DietaryNiacin", Unit: "mg", Cumulative: true, Lo: 0.5, Hi: 9},
	{ID: "DietaryFolate", Unit: "mcg", Cumulative: true, Lo: 10, Hi: 120},
	{ID: "DietaryBiotin", Unit: "mcg", Cumulative: true, Lo: 0.5, Hi: 10},
	{ID: "DietaryPantothenicAcid", Unit: "mg", Cumulative: true, Lo: 0.3, Hi: 2},
}

// workoutTypes maps the generator's sports to HKWorkoutActivityType names.
var workoutTypes = map[string]string{
	"run":      "Running",
	"walk":     "Walking",
	"cycle":    "Cycling",
	"swim":     "Swimming",
	"strength": "TraditionalStrengthTraining",
	"yoga":     "Yoga",
	"hiit":     "HighIntensityIntervalTraining",
	"row":      "Rowing",
	"hike":     "Hiking",
	"other":    "Other",
}

// rareWorkoutTypes are activity types the documentation lists that the
// timeline does not drive; the Unknown scenario writes one of each, short,
// from a third-party app.
var rareWorkoutTypes = []string{"AmericanFootball", "Archery", "AustralianFootball", "Badminton", "Barre", "Baseball",
	"Basketball", "Bowling", "Boxing", "CardioDance", "Climbing", "Cooldown", "CoreTraining", "Cricket", "CrossTraining",
	"Curling", "Dance", "DanceInspiredTraining", "DiscSports", "DownhillSkiing", "Elliptical", "EquestrianSports",
	"Fencing", "Fishing", "FitnessGaming", "Flexibility", "FunctionalStrengthTraining", "Golf", "Gymnastics",
	"HandCycling", "Handball", "Hockey", "Hunting", "JumpRope", "Kickboxing", "Lacrosse", "MartialArts", "MindAndBody",
	"MixedCardio", "MixedMetabolicCardioTraining", "PaddleSports", "Pickleball", "Pilates", "Play",
	"PreparationAndRecovery", "Racquetball", "Rugby", "Sailing", "SkatingSports", "SnowSports", "Snowboarding",
	"Soccer", "SocialDance", "Softball", "Squash", "StairClimbing", "Stairs", "StepTraining", "SurfingSports",
	"SwimBikeRun", "TableTennis", "TaiChi", "Tennis", "TrackAndField", "Transition", "UnderwaterDiving", "Volleyball",
	"WaterFitness", "WaterPolo", "WaterSports", "WheelchairRunPace", "WheelchairWalkPace", "Wrestling"}

// sleepValues are HKCategoryValueSleepAnalysis cases: the raw integer an SDK
// line carries and the name an export writes.
var sleepValues = map[string]struct {
	Raw  int
	Name string
}{
	"in_bed":      {0, "HKCategoryValueSleepAnalysisInBed"},
	"unspecified": {1, "HKCategoryValueSleepAnalysisAsleepUnspecified"},
	"awake":       {2, "HKCategoryValueSleepAnalysisAwake"},
	"light":       {3, "HKCategoryValueSleepAnalysisAsleepCore"},
	"deep":        {4, "HKCategoryValueSleepAnalysisAsleepDeep"},
	"rem":         {5, "HKCategoryValueSleepAnalysisAsleepREM"},
}

// symptomTypes take HKCategoryValueSeverity values.
var symptomTypes = []string{"Headache", "Fatigue", "Nausea", "AbdominalCramps", "Bloating", "LowerBackPain", "MoodChanges", "SleepChanges"}

// severity lists HKCategoryValueSeverity cases: raw value and export name.
var severity = []struct {
	Raw  int
	Name string
}{{0, "HKCategoryValueSeverityUnspecified"}, {1, "HKCategoryValueSeverityNotPresent"}, {2, "HKCategoryValueSeverityMild"},
	{3, "HKCategoryValueSeverityModerate"}, {4, "HKCategoryValueSeveritySevere"}}

// menstrualFlow lists HKCategoryValueMenstrualFlow cases.
var menstrualFlow = map[string]struct {
	Raw  int
	Name string
}{
	"unspecified": {1, "HKCategoryValueMenstrualFlowUnspecified"},
	"light":       {2, "HKCategoryValueMenstrualFlowLight"},
	"medium":      {3, "HKCategoryValueMenstrualFlowMedium"},
	"heavy":       {4, "HKCategoryValueMenstrualFlowHeavy"},
	"none":        {5, "HKCategoryValueMenstrualFlowNone"},
}

// ecgClassifications are HKElectrocardiogram.Classification cases, with the
// label the Health app writes in an exported ECG file.
var ecgClassifications = map[string]string{
	"notSet":                    "Not Set",
	"sinusRhythm":               "Sinus Rhythm",
	"atrialFibrillation":        "Atrial Fibrillation",
	"inconclusiveLowHeartRate":  "Heart Rate Under 50",
	"inconclusiveHighHeartRate": "Heart Rate Over 150",
	"inconclusivePoorReading":   "Poor Recording",
	"inconclusiveOther":         "Inconclusive",
	"unrecognized":              "Unrecognized",
}

// workoutRaw are the raw values of HKWorkoutActivityType, as the HealthKit
// SDK header (HKWorkout.h) declares them; an iOS client sends the number.
var workoutRaw = map[string]int{
	"AmericanFootball": 1, "Archery": 2, "AustralianFootball": 3, "Badminton": 4, "Baseball": 5, "Basketball": 6,
	"Bowling": 7, "Boxing": 8, "Climbing": 9, "Cricket": 10, "CrossTraining": 11, "Curling": 12, "Cycling": 13,
	"Dance": 14, "DanceInspiredTraining": 15, "Elliptical": 16, "EquestrianSports": 17, "Fencing": 18, "Fishing": 19,
	"FunctionalStrengthTraining": 20, "Golf": 21, "Gymnastics": 22, "Handball": 23, "Hiking": 24, "Hockey": 25,
	"Hunting": 26, "Lacrosse": 27, "MartialArts": 28, "MindAndBody": 29, "MixedMetabolicCardioTraining": 30,
	"PaddleSports": 31, "Play": 32, "PreparationAndRecovery": 33, "Racquetball": 34, "Rowing": 35, "Rugby": 36,
	"Running": 37, "Sailing": 38, "SkatingSports": 39, "SnowSports": 40, "Soccer": 41, "Softball": 42, "Squash": 43,
	"StairClimbing": 44, "SurfingSports": 45, "Swimming": 46, "TableTennis": 47, "Tennis": 48, "TrackAndField": 49,
	"TraditionalStrengthTraining": 50, "Volleyball": 51, "Walking": 52, "WaterFitness": 53, "WaterPolo": 54,
	"WaterSports": 55, "Wrestling": 56, "Yoga": 57, "Barre": 58, "CoreTraining": 59, "CrossCountrySkiing": 60,
	"DownhillSkiing": 61, "Flexibility": 62, "HighIntensityIntervalTraining": 63, "JumpRope": 64, "Kickboxing": 65,
	"Pilates": 66, "Snowboarding": 67, "Stairs": 68, "StepTraining": 69, "WheelchairWalkPace": 70,
	"WheelchairRunPace": 71, "TaiChi": 72, "MixedCardio": 73, "HandCycling": 74, "DiscSports": 75, "FitnessGaming": 76,
	"CardioDance": 77, "SocialDance": 78, "Pickleball": 79, "Cooldown": 80, "SwimBikeRun": 82, "Transition": 83,
	"UnderwaterDiving": 84, "Other": 3000,
}

// ecgRaw are the raw values of HKElectrocardiogram.Classification
// (HKElectrocardiogram.h).
var ecgRaw = map[string]int{"notSet": 0, "sinusRhythm": 1, "atrialFibrillation": 2, "inconclusiveLowHeartRate": 3,
	"inconclusiveHighHeartRate": 4, "inconclusivePoorReading": 5, "inconclusiveOther": 6, "unrecognized": 100}
