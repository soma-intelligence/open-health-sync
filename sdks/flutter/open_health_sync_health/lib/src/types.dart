// SPDX-License-Identifier: AGPL-3.0-only

/// The health stores the plugin reads.
enum HealthPlatform {
  /// HealthKit, on iOS.
  healthKit,

  /// Health Connect, on Android.
  healthConnect,
}

/// Whether the health store of the device can be used.
enum HealthStoreAvailability {
  /// The store is there and can be asked for access.
  available,

  /// Android 13 and lower: the Health Connect app must be installed or
  /// updated first.
  updateRequired,

  /// No health store: an unsupported device, the web or a desktop.
  unavailable,
}

/// The data types the plugin can sync, named once for both platforms.
///
/// [healthKit] and [healthConnect] are the support map: the native types a
/// value stands for on each platform, empty where the platform has none. They
/// are the identifiers of the Swift SDK (`AppleHealthTypes`) and the wire names
/// of the Kotlin SDK (`HealthConnectRecordType`), which do the reading.
///
/// A HealthKit entry that starts with `@` is a group the Swift SDK resolves
/// from its own table: `@quantities`, `@nutrition` and `@categories`.
enum HealthDataType {
  steps(
    healthKit: ['HKQuantityTypeIdentifierStepCount'],
    healthConnect: ['Steps'],
  ),
  heartRate(
    healthKit: ['HKQuantityTypeIdentifierHeartRate'],
    healthConnect: ['HeartRate'],
  ),
  restingHeartRate(
    healthKit: ['HKQuantityTypeIdentifierRestingHeartRate'],
    healthConnect: ['RestingHeartRate'],
  ),

  /// Not the same measure on the two platforms: HealthKit records SDNN,
  /// Health Connect RMSSD. The server stores each under its own type.
  heartRateVariability(
    healthKit: ['HKQuantityTypeIdentifierHeartRateVariabilitySDNN'],
    healthConnect: ['HeartRateVariabilityRmssd'],
  ),
  oxygenSaturation(
    healthKit: ['HKQuantityTypeIdentifierOxygenSaturation'],
    healthConnect: ['OxygenSaturation'],
  ),
  respiratoryRate(
    healthKit: ['HKQuantityTypeIdentifierRespiratoryRate'],
    healthConnect: ['RespiratoryRate'],
  ),
  bodyTemperature(
    healthKit: ['HKQuantityTypeIdentifierBodyTemperature'],
    healthConnect: ['BodyTemperature'],
  ),

  /// Two quantity types on HealthKit, one record on Health Connect.
  bloodPressure(
    healthKit: [
      'HKQuantityTypeIdentifierBloodPressureSystolic',
      'HKQuantityTypeIdentifierBloodPressureDiastolic',
    ],
    healthConnect: ['BloodPressure'],
  ),
  bloodGlucose(
    healthKit: ['HKQuantityTypeIdentifierBloodGlucose'],
    healthConnect: ['BloodGlucose'],
  ),
  weight(
    healthKit: ['HKQuantityTypeIdentifierBodyMass'],
    healthConnect: ['Weight'],
  ),
  height(
    healthKit: ['HKQuantityTypeIdentifierHeight'],
    healthConnect: ['Height'],
  ),
  bodyFat(
    healthKit: ['HKQuantityTypeIdentifierBodyFatPercentage'],
    healthConnect: ['BodyFat'],
  ),
  activeEnergy(
    healthKit: ['HKQuantityTypeIdentifierActiveEnergyBurned'],
    healthConnect: ['ActiveCaloriesBurned'],
  ),

  /// Energy burned at rest on HealthKit, the basal metabolic rate on Health
  /// Connect.
  basalEnergy(
    healthKit: ['HKQuantityTypeIdentifierBasalEnergyBurned'],
    healthConnect: ['BasalMetabolicRate'],
  ),

  /// Health Connect only: HealthKit has no total, it is active plus basal.
  totalEnergy(healthConnect: ['TotalCaloriesBurned']),

  /// Walking and running distance on HealthKit; every distance on Health
  /// Connect.
  distance(
    healthKit: ['HKQuantityTypeIdentifierDistanceWalkingRunning'],
    healthConnect: ['Distance'],
  ),
  sleep(
    healthKit: ['HKCategoryTypeIdentifierSleepAnalysis'],
    healthConnect: ['SleepSession'],
  ),

  /// Workouts, with their GPS routes on HealthKit (a route is only readable
  /// when workouts are). The server does not store Health Connect routes.
  workouts(
    healthKit: ['HKWorkoutTypeIdentifier', 'HKWorkoutRouteTypeIdentifier'],
    healthConnect: ['ExerciseSession'],
  ),
  menstruation(
    healthKit: ['HKCategoryTypeIdentifierMenstrualFlow'],
    healthConnect: ['MenstruationFlow'],
  ),
  intermenstrualBleeding(
    healthKit: ['HKCategoryTypeIdentifierIntermenstrualBleeding'],
    healthConnect: ['IntermenstrualBleeding'],
  ),

  /// Every dietary quantity type on HealthKit (water included), the
  /// nutrition record on Health Connect.
  nutrition(healthKit: ['@nutrition'], healthConnect: ['Nutrition']),
  hydration(
    healthKit: ['HKQuantityTypeIdentifierDietaryWater'],
    healthConnect: ['Hydration'],
  ),

  /// HealthKit only: ECG recordings with their voltages.
  electrocardiogram(healthKit: ['HKDataTypeIdentifierElectrocardiogram']),

  /// HealthKit only: beat to beat series, stored as RR intervals.
  heartbeatSeries(healthKit: ['HKDataTypeIdentifierHeartbeatSeries']),

  /// HealthKit only.
  audiogram(healthKit: ['HKDataTypeIdentifierAudiogram']),

  /// HealthKit only, iOS 18 and later.
  stateOfMind(healthKit: ['HKDataTypeStateOfMind']),

  /// HealthKit only: every quantity type the server stores that is not
  /// dietary, the ones named above included (VO2 max, cycling distance,
  /// running power, audio exposure and so on).
  healthKitQuantities(healthKit: ['@quantities']),

  /// HealthKit only: every category type the server stores, sleep and the
  /// ones named above included (symptoms, mindful sessions, heart events
  /// and so on).
  healthKitCategories(healthKit: ['@categories']);

  const HealthDataType({
    this.healthKit = const [],
    this.healthConnect = const [],
  });

  /// The HealthKit identifiers this type stands for; empty when iOS has none.
  final List<String> healthKit;

  /// The Health Connect record types this type stands for; empty when
  /// Android has none.
  final List<String> healthConnect;

  /// The native types of this type on [platform].
  List<String> identifiersOn(HealthPlatform platform) =>
      platform == HealthPlatform.healthKit ? healthKit : healthConnect;

  /// Whether [platform] has this type.
  bool isSupportedOn(HealthPlatform platform) =>
      identifiersOn(platform).isNotEmpty;

  /// The types [platform] has.
  static Set<HealthDataType> supportedOn(HealthPlatform platform) =>
      values.where((type) => type.isSupportedOn(platform)).toSet();

  /// The types that stand for the native type [identifier] on [platform].
  /// Groups (`@quantities` and the like) are not looked into.
  static Set<HealthDataType> fromNative(
    HealthPlatform platform,
    String identifier,
  ) {
    bool matches(HealthDataType type) =>
        type.identifiersOn(platform).contains(identifier);
    return values.where(matches).toSet();
  }
}
