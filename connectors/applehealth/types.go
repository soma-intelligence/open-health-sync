// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

// quantityTypeCanonical maps HKQuantityTypeIdentifier strings to
// canonical quantity_type values stored in the DB.
var quantityTypeCanonical = map[string]string{
	"HKQuantityTypeIdentifierHeartRate":                     "heart_rate",
	"HKQuantityTypeIdentifierRestingHeartRate":              "resting_heart_rate",
	"HKQuantityTypeIdentifierWalkingHeartRateAverage":       "walking_heart_rate_average",
	"HKQuantityTypeIdentifierHeartRateVariabilitySDNN":      "hrv_sdnn",
	"HKQuantityTypeIdentifierOxygenSaturation":              "oxygen_saturation",
	"HKQuantityTypeIdentifierBloodPressureSystolic":         "blood_pressure_systolic",
	"HKQuantityTypeIdentifierBloodPressureDiastolic":        "blood_pressure_diastolic",
	"HKQuantityTypeIdentifierRespiratoryRate":               "respiratory_rate",
	"HKQuantityTypeIdentifierBodyTemperature":               "body_temperature",
	"HKQuantityTypeIdentifierBodyMass":                      "body_mass",
	"HKQuantityTypeIdentifierHeight":                        "height",
	"HKQuantityTypeIdentifierBodyMassIndex":                 "body_mass_index",
	"HKQuantityTypeIdentifierBodyFatPercentage":             "body_fat_percentage",
	"HKQuantityTypeIdentifierLeanBodyMass":                  "lean_body_mass",
	"HKQuantityTypeIdentifierWaistCircumference":            "waist_circumference",
	"HKQuantityTypeIdentifierStepCount":                     "step_count",
	"HKQuantityTypeIdentifierDistanceWalkingRunning":        "distance_walking_running",
	"HKQuantityTypeIdentifierDistanceCycling":               "distance_cycling",
	"HKQuantityTypeIdentifierDistanceSwimming":              "distance_swimming",
	"HKQuantityTypeIdentifierDistanceWheelchair":            "distance_wheelchair",
	"HKQuantityTypeIdentifierDistanceDownhillSnowSports":    "distance_downhill_snow_sports",
	"HKQuantityTypeIdentifierBasalEnergyBurned":             "basal_energy_burned",
	"HKQuantityTypeIdentifierActiveEnergyBurned":            "active_energy_burned",
	"HKQuantityTypeIdentifierFlightsClimbed":                "flights_climbed",
	"HKQuantityTypeIdentifierPushCount":                     "push_count",
	"HKQuantityTypeIdentifierNikeFuel":                      "nike_fuel",
	"HKQuantityTypeIdentifierAppleExerciseTime":             "apple_exercise_time",
	"HKQuantityTypeIdentifierAppleMoveTime":                 "apple_move_time",
	"HKQuantityTypeIdentifierAppleStandTime":                "apple_stand_time",
	"HKQuantityTypeIdentifierRunningStrideLength":           "running_stride_length",
	"HKQuantityTypeIdentifierRunningVerticalOscillation":    "running_vertical_oscillation",
	"HKQuantityTypeIdentifierRunningGroundContactTime":      "running_ground_contact_time",
	"HKQuantityTypeIdentifierRunningPower":                  "running_power",
	"HKQuantityTypeIdentifierRunningSpeed":                  "running_speed",
	"HKQuantityTypeIdentifierWalkingSpeed":                  "walking_speed",
	"HKQuantityTypeIdentifierPhysicalEffort":                "physical_effort",
	"HKQuantityTypeIdentifierVO2Max":                        "vo2_max",
	"HKQuantityTypeIdentifierCyclingPower":                  "cycling_power",
	"HKQuantityTypeIdentifierEnvironmentalAudioExposure":    "environmental_audio_exposure",
	"HKQuantityTypeIdentifierHeadphoneAudioExposure":        "headphone_audio_exposure",
	"HKQuantityTypeIdentifierEnvironmentalSoundReduction":   "environmental_sound_reduction",
	"HKQuantityTypeIdentifierBloodGlucose":                  "blood_glucose",
	"HKQuantityTypeIdentifierElectrodermalActivity":         "electrodermal_activity",
	"HKQuantityTypeIdentifierForcedExpiratoryVolume1":       "forced_expiratory_volume_1",
	"HKQuantityTypeIdentifierForcedVitalCapacity":           "forced_vital_capacity",
	"HKQuantityTypeIdentifierInhalerUsage":                  "inhaler_usage",
	"HKQuantityTypeIdentifierInsulinDelivery":               "insulin_delivery",
	"HKQuantityTypeIdentifierPeakExpiratoryFlowRate":        "peak_expiratory_flow_rate",
	"HKQuantityTypeIdentifierNumberOfTimesFallen":           "number_of_times_fallen",
	"HKQuantityTypeIdentifierUVExposure":                    "uv_exposure",
	"HKQuantityTypeIdentifierBasalBodyTemperature":          "basal_body_temperature",
	"HKQuantityTypeIdentifierBloodAlcoholContent":           "blood_alcohol_content",
	"HKQuantityTypeIdentifierNumberOfAlcoholicBeverages":    "number_of_alcoholic_beverages",
	"HKQuantityTypeIdentifierAppleSleepingWristTemperature": "apple_sleeping_wrist_temperature",
	"HKQuantityTypeIdentifierWaterTemperature":              "water_temperature",
	"HKQuantityTypeIdentifierUnderwaterDepth":               "underwater_depth",
}

// dietaryPrefix starts every dietary HKQuantityType identifier.
const dietaryPrefix = "HKQuantityTypeIdentifierDietary"

// dietaryNutrients maps the part of a dietary HKQuantityType identifier after
// dietaryPrefix to its canonical nutrient name. These lines become nutrition
// logs, not quantity samples, so the table is keyed by the suffix: a nutrient
// name is not a quantity type and must not read as one (the storage-unit guard
// in internal/model reads HKQuantityTypeIdentifier tables as quantity types).
var dietaryNutrients = map[string]string{
	"Biotin":             "biotin",
	"Caffeine":           "caffeine",
	"Calcium":            "calcium",
	"Carbohydrates":      "carbohydrates",
	"Chloride":           "chloride",
	"Cholesterol":        "cholesterol",
	"Chromium":           "chromium",
	"Copper":             "copper",
	"EnergyConsumed":     "energy_consumed",
	"FatMonounsaturated": "fat_monounsaturated",
	"FatPolyunsaturated": "fat_polyunsaturated",
	"FatSaturated":       "fat_saturated",
	"FatTotal":           "fat_total",
	"Fiber":              "fiber",
	"Folate":             "folate",
	"Iodine":             "iodine",
	"Iron":               "iron",
	"Magnesium":          "magnesium",
	"Manganese":          "manganese",
	"Molybdenum":         "molybdenum",
	"Niacin":             "niacin",
	"PantothenicAcid":    "pantothenic_acid",
	"Phosphorus":         "phosphorus",
	"Potassium":          "potassium",
	"Protein":            "protein",
	"Riboflavin":         "riboflavin",
	"Selenium":           "selenium",
	"Sodium":             "sodium",
	"Sugar":              "sugar",
	"Thiamin":            "thiamin",
	"VitaminA":           "vitamin_a",
	"VitaminB12":         "vitamin_b12",
	"VitaminB6":          "vitamin_b6",
	"VitaminC":           "vitamin_c",
	"VitaminD":           "vitamin_d",
	"VitaminE":           "vitamin_e",
	"VitaminK":           "vitamin_k",
	"Water":              "water",
	"Zinc":               "zinc",
}

// categoryTypeCanonical maps HKCategoryTypeIdentifier strings to
// canonical category_type values stored in the DB.
var categoryTypeCanonical = map[string]string{
	"HKCategoryTypeIdentifierSleepAnalysis":                      "sleep_analysis",
	"HKCategoryTypeIdentifierAppleStandHour":                     "apple_stand_hour",
	"HKCategoryTypeIdentifierCervicalMucusQuality":               "cervical_mucus_quality",
	"HKCategoryTypeIdentifierOvulationTestResult":                "ovulation_test_result",
	"HKCategoryTypeIdentifierMenstrualFlow":                      "menstrual_flow",
	"HKCategoryTypeIdentifierIntermenstrualBleeding":             "intermenstrual_bleeding",
	"HKCategoryTypeIdentifierSexualActivity":                     "sexual_activity",
	"HKCategoryTypeIdentifierMindfulSession":                     "mindful_session",
	"HKCategoryTypeIdentifierHighHeartRateEvent":                 "high_heart_rate_event",
	"HKCategoryTypeIdentifierLowHeartRateEvent":                  "low_heart_rate_event",
	"HKCategoryTypeIdentifierIrregularHeartRhythmEvent":          "irregular_heart_rhythm_event",
	"HKCategoryTypeIdentifierToothbrushingEvent":                 "toothbrushing_event",
	"HKCategoryTypeIdentifierHandwashingEvent":                   "handwashing_event",
	"HKCategoryTypeIdentifierAudioExposureEvent":                 "audio_exposure_event",
	"HKCategoryTypeIdentifierLowCardioFitnessEvent":              "low_cardio_fitness_event",
	"HKCategoryTypeIdentifierAbdominalCramps":                    "abdominal_cramps",
	"HKCategoryTypeIdentifierBloating":                           "bloating",
	"HKCategoryTypeIdentifierConstipation":                       "constipation",
	"HKCategoryTypeIdentifierDiarrhea":                           "diarrhea",
	"HKCategoryTypeIdentifierHeartburn":                          "heartburn",
	"HKCategoryTypeIdentifierNausea":                             "nausea",
	"HKCategoryTypeIdentifierVomiting":                           "vomiting",
	"HKCategoryTypeIdentifierAppetiteChanges":                    "appetite_changes",
	"HKCategoryTypeIdentifierChills":                             "chills",
	"HKCategoryTypeIdentifierDizziness":                          "dizziness",
	"HKCategoryTypeIdentifierFainting":                           "fainting",
	"HKCategoryTypeIdentifierFatigue":                            "fatigue",
	"HKCategoryTypeIdentifierFever":                              "fever",
	"HKCategoryTypeIdentifierGeneralizedBodyAche":                "generalized_body_ache",
	"HKCategoryTypeIdentifierHotFlashes":                         "hot_flashes",
	"HKCategoryTypeIdentifierChestTightnessOrPain":               "chest_tightness_or_pain",
	"HKCategoryTypeIdentifierCoughing":                           "coughing",
	"HKCategoryTypeIdentifierRapidPoundingOrFlutteringHeartbeat": "rapid_pounding_or_fluttering_heartbeat",
	"HKCategoryTypeIdentifierShortnessOfBreath":                  "shortness_of_breath",
	"HKCategoryTypeIdentifierSkippedHeartbeat":                   "skipped_heartbeat",
	"HKCategoryTypeIdentifierWheezing":                           "wheezing",
	"HKCategoryTypeIdentifierLowerBackPain":                      "lower_back_pain",
	"HKCategoryTypeIdentifierHeadache":                           "headache",
	"HKCategoryTypeIdentifierMemoryLapse":                        "memory_lapse",
	"HKCategoryTypeIdentifierMoodChanges":                        "mood_changes",
	"HKCategoryTypeIdentifierLossOfSmell":                        "loss_of_smell",
	"HKCategoryTypeIdentifierLossOfTaste":                        "loss_of_taste",
	"HKCategoryTypeIdentifierRunnyNose":                          "runny_nose",
	"HKCategoryTypeIdentifierSoreThroat":                         "sore_throat",
	"HKCategoryTypeIdentifierSinusCongestion":                    "sinus_congestion",
	"HKCategoryTypeIdentifierAcne":                               "acne",
	"HKCategoryTypeIdentifierDrySkin":                            "dry_skin",
	"HKCategoryTypeIdentifierHairLoss":                           "hair_loss",
	"HKCategoryTypeIdentifierNightSweats":                        "night_sweats",
	"HKCategoryTypeIdentifierSleepChanges":                       "sleep_changes",
	"HKCategoryTypeIdentifierBladderIncontinence":                "bladder_incontinence",
	"HKCategoryTypeIdentifierPelvicPain":                         "pelvic_pain",
	"HKCategoryTypeIdentifierVaginalDryness":                     "vaginal_dryness",
	"HKCategoryTypeIdentifierBreastPain":                         "breast_pain",
	"HKCategoryTypeIdentifierPregnancyTestResult":                "pregnancy_test_result",
	"HKCategoryTypeIdentifierProgesteroneTestResult":             "progesterone_test_result",
	"HKCategoryTypeIdentifierEnvironmentalAudioExposureEvent":    "environmental_audio_exposure_event",
	"HKCategoryTypeIdentifierPersistentIntermenstrualBleeding":   "persistent_intermenstrual_bleeding",
	"HKCategoryTypeIdentifierProlongedMenstrualPeriods":          "prolonged_menstrual_periods",
	"HKCategoryTypeIdentifierIrregularMenstrualCycles":           "irregular_menstrual_cycles",
	"HKCategoryTypeIdentifierInfrequentMenstrualCycles":          "infrequent_menstrual_cycles",
}

// sleepAnalysisValues maps Apple Health HKCategoryValueSleepAnalysis
// integers to their raw HK phase labels. These are NOT stored verbatim:
// mapSleep routes every label except in_bed through
// model.CanonicalSleepStage before emission, so the sleep phase column only
// ever carries the canonical four stages plus the in_bed session marker.
var sleepAnalysisValues = map[int]string{
	0: "in_bed",
	1: "asleep_unspecified",
	2: "awake",
	3: "asleep_core",
	4: "asleep_deep",
	5: "asleep_rem",
}

// workoutActivityTypes maps HKWorkoutActivityType raw values (integers)
// to canonical activity type names for the DB.
var workoutActivityTypes = map[int]string{
	1:  "american_football",
	2:  "archery",
	3:  "australian_football",
	4:  "badminton",
	5:  "baseball",
	6:  "basketball",
	7:  "bowling",
	8:  "boxing",
	9:  "climbing",
	10: "cricket",
	11: "cross_training",
	12: "curling",
	13: "cycling",
	16: "elliptical",
	17: "equestrian_sports",
	18: "fencing",
	19: "fishing",
	20: "functional_strength_training",
	21: "golf",
	22: "gymnastics",
	23: "handball",
	24: "hiking",
	25: "hockey",
	26: "hunting",
	27: "lacrosse",
	28: "martial_arts",
	29: "mind_and_body",
	30: "mixed_metabolic_cardio_training",
	31: "paddle_sports",
	32: "play",
	33: "preparation_and_recovery",
	34: "racquetball",
	35: "rowing",
	36: "rugby",
	37: "running",
	38: "sailing",
	39: "skating_sports",
	40: "snow_sports",
	41: "soccer",
	42: "softball",
	43: "squash",
	44: "stair_climbing",
	45: "surfing_sports",
	46: "swimming",
	47: "table_tennis",
	48: "tennis",
	49: "track_and_field",
	50: "traditional_strength_training",
	51: "volleyball",
	52: "walking",
	53: "water_fitness",
	54: "water_polo",
	55: "water_sports",
	56: "wrestling",
	57: "yoga",
	58: "barre",
	59: "core_training",
	60: "cross_country_skiing",
	61: "downhill_skiing",
	62: "flexibility",
	63: "high_intensity_interval_training",
	64: "jump_rope",
	65: "kickboxing",
	66: "pilates",
	67: "snowboarding",
	68: "stairs",
	69: "step_training",
	70: "wheelchair_walk_pace",
	71: "wheelchair_run_pace",
	72: "tai_chi",
	73: "mixed_cardio",
	74: "hand_cycling",
	75: "disc_sports",
	76: "fitness_gaming",
	77: "dance",
	78: "social_dance",
	79: "pickleball",
	80: "cooldown",
	83: "swimming_open_water",
	84: "underwater_diving",
}

// dietaryNutrient returns the nutrient name of a dietary HKQuantityType
// identifier, and false for any other identifier.
func dietaryNutrient(hkType string) (string, bool) {
	if len(hkType) <= len(dietaryPrefix) || hkType[:len(dietaryPrefix)] != dietaryPrefix {
		return "", false
	}
	n, ok := dietaryNutrients[hkType[len(dietaryPrefix):]]
	return n, ok
}

// StoresType reports whether the connector stores samples of a HealthKit
// quantity or category type identifier (dietary types included). A line of
// any other quantity or category type is skipped by Map. Clients use it to
// leave out, and count, what the server would drop anyway.
func StoresType(id string) bool {
	if _, ok := quantityTypeCanonical[id]; ok {
		return true
	}
	if _, ok := categoryTypeCanonical[id]; ok {
		return true
	}
	_, ok := dietaryNutrient(id)
	return ok
}
