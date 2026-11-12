// SPDX-License-Identifier: AGPL-3.0-only

/** The platform a health store lives on. */
export type HealthPlatform = "ios" | "android";

/**
 * Where a data type comes from on each platform; `null` where the platform has no such type
 * or the server does not store it.
 *
 * - `ios`: HealthKit type identifiers, or a group the Swift SDK expands (`@nutrition`,
 *   `@workouts`, `@heartRhythm`).
 * - `android`: the Health Connect record type (the record class name without `Record`).
 *
 * This table only names the types. How a sample becomes an upload line is the work of the
 * Swift and Kotlin SDKs, which this package wraps.
 */
export interface PlatformSupport {
  readonly ios: readonly string[] | null;
  readonly android: string | null;
}

const hk = (name: string): string => `HKQuantityTypeIdentifier${name}`;
const hkCategory = (name: string): string => `HKCategoryTypeIdentifier${name}`;

/** Every data type this package names, with what backs it on each platform. */
export const HEALTH_DATA_TYPES = {
  // On both platforms.
  steps: { ios: [hk("StepCount")], android: "Steps" },
  heartRate: { ios: [hk("HeartRate")], android: "HeartRate" },
  restingHeartRate: { ios: [hk("RestingHeartRate")], android: "RestingHeartRate" },
  /** SDNN on iOS, RMSSD on Android: two measures, stored under their own names. */
  heartRateVariability: { ios: [hk("HeartRateVariabilitySDNN")], android: "HeartRateVariabilityRmssd" },
  oxygenSaturation: { ios: [hk("OxygenSaturation")], android: "OxygenSaturation" },
  respiratoryRate: { ios: [hk("RespiratoryRate")], android: "RespiratoryRate" },
  bodyTemperature: { ios: [hk("BodyTemperature")], android: "BodyTemperature" },
  bloodPressure: { ios: [hk("BloodPressureSystolic"), hk("BloodPressureDiastolic")], android: "BloodPressure" },
  bloodGlucose: { ios: [hk("BloodGlucose")], android: "BloodGlucose" },
  weight: { ios: [hk("BodyMass")], android: "Weight" },
  height: { ios: [hk("Height")], android: "Height" },
  bodyFat: { ios: [hk("BodyFatPercentage")], android: "BodyFat" },
  activeEnergy: { ios: [hk("ActiveEnergyBurned")], android: "ActiveCaloriesBurned" },
  /** Walking and running distance. */
  distance: { ios: [hk("DistanceWalkingRunning")], android: "Distance" },
  sleep: { ios: [hkCategory("SleepAnalysis")], android: "SleepSession" },
  /** Workouts; on iOS their GPS routes too. */
  workouts: { ios: ["@workouts"], android: "ExerciseSession" },
  menstrualFlow: { ios: [hkCategory("MenstrualFlow")], android: "MenstruationFlow" },
  intermenstrualBleeding: { ios: [hkCategory("IntermenstrualBleeding")], android: "IntermenstrualBleeding" },
  /** Every dietary quantity on iOS (water included), the nutrition record on Android. */
  nutrition: { ios: ["@nutrition"], android: "Nutrition" },
  hydration: { ios: [hk("DietaryWater")], android: "Hydration" },

  // Health Connect only.
  totalEnergy: { ios: null, android: "TotalCaloriesBurned" },
  basalMetabolicRate: { ios: null, android: "BasalMetabolicRate" },

  // HealthKit only.
  basalEnergy: { ios: [hk("BasalEnergyBurned")], android: null },
  walkingHeartRateAverage: { ios: [hk("WalkingHeartRateAverage")], android: null },
  vo2Max: { ios: [hk("VO2Max")], android: null },
  flightsClimbed: { ios: [hk("FlightsClimbed")], android: null },
  distanceCycling: { ios: [hk("DistanceCycling")], android: null },
  distanceSwimming: { ios: [hk("DistanceSwimming")], android: null },
  exerciseTime: { ios: [hk("AppleExerciseTime")], android: null },
  standTime: { ios: [hk("AppleStandTime")], android: null },
  bodyMassIndex: { ios: [hk("BodyMassIndex")], android: null },
  leanBodyMass: { ios: [hk("LeanBodyMass")], android: null },
  waistCircumference: { ios: [hk("WaistCircumference")], android: null },
  basalBodyTemperature: { ios: [hk("BasalBodyTemperature")], android: null },
  wristTemperature: { ios: [hk("AppleSleepingWristTemperature")], android: null },
  environmentalAudioExposure: { ios: [hk("EnvironmentalAudioExposure")], android: null },
  headphoneAudioExposure: { ios: [hk("HeadphoneAudioExposure")], android: null },
  mindfulSession: { ios: [hkCategory("MindfulSession")], android: null },
  /** ECG recordings and beat to beat series. */
  heartRhythm: { ios: ["@heartRhythm"], android: null },
  audiogram: { ios: ["HKDataTypeIdentifierAudiogram"], android: null },
  /** iOS 18 and later. */
  stateOfMind: { ios: ["HKDataTypeStateOfMind"], android: null },
} as const satisfies Record<string, PlatformSupport>;

/** The name of a data type, the same on both platforms. */
export type HealthDataType = keyof typeof HEALTH_DATA_TYPES;

/** Every name of {@link HEALTH_DATA_TYPES}. */
export const ALL_HEALTH_DATA_TYPES = Object.keys(HEALTH_DATA_TYPES) as HealthDataType[];

/**
 * What the native side is given in place of a list to mean "every type the server stores".
 * That is more than {@link ALL_HEALTH_DATA_TYPES} on iOS, where the Swift SDK knows every
 * quantity and category type of the server's table.
 */
export const EVERY_TYPE = "*";

/** Whether `type` can be read on `platform`. */
export function isTypeSupported(type: HealthDataType, platform: HealthPlatform): boolean {
  return support(type)[platform] !== null;
}

/** The types of {@link HEALTH_DATA_TYPES} that can be read on `platform`. */
export function supportedTypes(platform: HealthPlatform): HealthDataType[] {
  return ALL_HEALTH_DATA_TYPES.filter((t) => isTypeSupported(t, platform));
}

function support(type: HealthDataType): PlatformSupport {
  const s: PlatformSupport | undefined = HEALTH_DATA_TYPES[type];
  if (s === undefined) throw new RangeError(`unknown health data type "${String(type)}"`);
  return s;
}

/**
 * The names the native module of `platform` takes for `types`, without repeats and in the
 * order given. A type the platform does not have is left out, so one list can be passed on
 * both platforms. A name that is not in {@link HEALTH_DATA_TYPES} throws a `RangeError`.
 */
export function nativeTypeNames(types: readonly HealthDataType[], platform: HealthPlatform): string[] {
  const out: string[] = [];
  for (const type of types) {
    const s = support(type);
    const names = platform === "ios" ? s.ios : s.android === null ? null : [s.android];
    for (const name of names ?? []) {
      if (!out.includes(name)) out.push(name);
    }
  }
  return out;
}

/**
 * The data types a native name stands for on `platform` (the reverse of
 * {@link nativeTypeNames}); empty for a name this package has no word for.
 */
export function dataTypesOfNativeName(name: string, platform: HealthPlatform): HealthDataType[] {
  return ALL_HEALTH_DATA_TYPES.filter((type) => {
    const s = support(type);
    return platform === "ios" ? (s.ios ?? []).includes(name) : s.android === name;
  });
}
