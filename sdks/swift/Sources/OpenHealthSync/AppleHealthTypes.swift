// SPDX-License-Identifier: AGPL-3.0-only

import Foundation

/// How the server reads the lines of a HealthKit type.
public enum AppleHealthTypeKind: String, Sendable, CaseIterable {
    /// A quantity sample; dietary types become nutrition logs.
    case quantity
    /// A category sample; sleep analysis becomes sleep sessions and phases.
    case category
    case workout
    /// The GPS points of a workout.
    case workoutRoute
    /// Blood pressure or food. Its members are also quantity samples of their own types.
    case correlation
    /// An ECG recording and its voltage samples.
    case electrocardiogram
    /// A beat to beat series, stored as RR intervals.
    case heartbeatSeries
    case audiogram
    case stateOfMind
}

/// The HealthKit types the server's Apple Health connector stores, by identifier
/// (connectors/applehealth/types.go and mapper.go). This is the one table of the SDK:
/// `HealthKitSync.allSupportedTypes` is built from it and the sync reads a sample only when
/// its type is in it, so the default type set and the read path cannot drift apart.
public enum AppleHealthTypes {
    public static let workout = AppleHealthUnits.workoutTypeIdentifier
    public static let workoutRoute = "HKWorkoutRouteTypeIdentifier"
    public static let electrocardiogram = "HKDataTypeIdentifierElectrocardiogram"
    public static let heartbeatSeries = "HKDataTypeIdentifierHeartbeatSeries"
    /// The identifier of `HKAudiogramSampleType` (the lines carry another name, see
    /// `AudiogramLine.discriminator`).
    public static let audiogram = "HKDataTypeIdentifierAudiogram"
    public static let stateOfMind = "HKDataTypeStateOfMind"
    public static let sleepAnalysis = "HKCategoryTypeIdentifierSleepAnalysis"
    public static let bloodPressureCorrelation = AppleHealthUnits.bloodPressureCorrelationIdentifier
    public static let foodCorrelation = "HKCorrelationTypeIdentifierFood"

    /// How the server reads `identifier`, or nil when it does not store the type.
    public static func kind(of identifier: String) -> AppleHealthTypeKind? {
        if AppleHealthUnits.wireUnit(forQuantityType: identifier) != nil {
            return .quantity
        }
        return otherKinds[identifier]
    }

    /// Quantity types that are not dietary: heart, body, activity, hearing.
    public static var quantityIdentifiers: [String] {
        return AppleHealthUnits.quantityUnits.keys.sorted()
    }

    /// Dietary quantity types, stored as nutrition logs.
    public static var nutritionIdentifiers: [String] {
        return AppleHealthUnits.dietaryUnits.keys.map { dietaryPrefix + $0 }.sorted()
    }

    /// Category types, sleep analysis included.
    public static var categoryIdentifiers: [String] {
        return categories.sorted()
    }

    /// The workout type and the workout route type.
    public static let workoutIdentifiers: [String] = [workoutRoute, workout]

    /// ECG recordings and heartbeat series.
    public static let heartRhythmIdentifiers: [String] = [electrocardiogram, heartbeatSeries]

    /// The correlation types the server reads. They are not in `allIdentifiers`: the members
    /// of a correlation are quantity samples of types that are, and sending both would store
    /// a food entry twice (once merged, once per nutrient).
    public static let correlationIdentifiers: [String] = [bloodPressureCorrelation, foodCorrelation]

    /// Every type to sync so that nothing the server stores is left out and nothing is sent
    /// twice, sorted. Correlations are left out (see `correlationIdentifiers`).
    public static var allIdentifiers: [String] {
        let other = otherKinds.filter { $0.value != .correlation }.map { $0.key }
        return (quantityIdentifiers + nutritionIdentifiers + other).sorted()
    }

    static let dietaryPrefix = "HKQuantityTypeIdentifierDietary"

    /// Every type that is not a quantity type, with its kind.
    static let otherKinds: [String: AppleHealthTypeKind] = {
        var kinds: [String: AppleHealthTypeKind] = [
            workout: .workout,
            workoutRoute: .workoutRoute,
            electrocardiogram: .electrocardiogram,
            heartbeatSeries: .heartbeatSeries,
            audiogram: .audiogram,
            stateOfMind: .stateOfMind,
            bloodPressureCorrelation: .correlation,
            foodCorrelation: .correlation,
        ]
        for identifier in categories {
            kinds[identifier] = .category
        }
        return kinds
    }()

    /// The keys of categoryTypeCanonical in connectors/applehealth/types.go. One of them,
    /// `HKCategoryTypeIdentifierEnvironmentalAudioExposureEvent`, is not a name HealthKit
    /// uses (its event is `HKCategoryTypeIdentifierAudioExposureEvent`): it resolves to no
    /// HealthKit type and is only kept so the list equals the server's.
    static let categories: [String] = [
        "HKCategoryTypeIdentifierAbdominalCramps",
        "HKCategoryTypeIdentifierAcne",
        "HKCategoryTypeIdentifierAppetiteChanges",
        "HKCategoryTypeIdentifierAppleStandHour",
        "HKCategoryTypeIdentifierAudioExposureEvent",
        "HKCategoryTypeIdentifierBladderIncontinence",
        "HKCategoryTypeIdentifierBloating",
        "HKCategoryTypeIdentifierBreastPain",
        "HKCategoryTypeIdentifierCervicalMucusQuality",
        "HKCategoryTypeIdentifierChestTightnessOrPain",
        "HKCategoryTypeIdentifierChills",
        "HKCategoryTypeIdentifierConstipation",
        "HKCategoryTypeIdentifierCoughing",
        "HKCategoryTypeIdentifierDiarrhea",
        "HKCategoryTypeIdentifierDizziness",
        "HKCategoryTypeIdentifierDrySkin",
        "HKCategoryTypeIdentifierEnvironmentalAudioExposureEvent",
        "HKCategoryTypeIdentifierFainting",
        "HKCategoryTypeIdentifierFatigue",
        "HKCategoryTypeIdentifierFever",
        "HKCategoryTypeIdentifierGeneralizedBodyAche",
        "HKCategoryTypeIdentifierHairLoss",
        "HKCategoryTypeIdentifierHandwashingEvent",
        "HKCategoryTypeIdentifierHeadache",
        "HKCategoryTypeIdentifierHeartburn",
        "HKCategoryTypeIdentifierHighHeartRateEvent",
        "HKCategoryTypeIdentifierHotFlashes",
        "HKCategoryTypeIdentifierInfrequentMenstrualCycles",
        "HKCategoryTypeIdentifierIntermenstrualBleeding",
        "HKCategoryTypeIdentifierIrregularHeartRhythmEvent",
        "HKCategoryTypeIdentifierIrregularMenstrualCycles",
        "HKCategoryTypeIdentifierLossOfSmell",
        "HKCategoryTypeIdentifierLossOfTaste",
        "HKCategoryTypeIdentifierLowCardioFitnessEvent",
        "HKCategoryTypeIdentifierLowHeartRateEvent",
        "HKCategoryTypeIdentifierLowerBackPain",
        "HKCategoryTypeIdentifierMemoryLapse",
        "HKCategoryTypeIdentifierMenstrualFlow",
        "HKCategoryTypeIdentifierMindfulSession",
        "HKCategoryTypeIdentifierMoodChanges",
        "HKCategoryTypeIdentifierNausea",
        "HKCategoryTypeIdentifierNightSweats",
        "HKCategoryTypeIdentifierOvulationTestResult",
        "HKCategoryTypeIdentifierPelvicPain",
        "HKCategoryTypeIdentifierPersistentIntermenstrualBleeding",
        "HKCategoryTypeIdentifierPregnancyTestResult",
        "HKCategoryTypeIdentifierProgesteroneTestResult",
        "HKCategoryTypeIdentifierProlongedMenstrualPeriods",
        "HKCategoryTypeIdentifierRapidPoundingOrFlutteringHeartbeat",
        "HKCategoryTypeIdentifierRunnyNose",
        "HKCategoryTypeIdentifierSexualActivity",
        "HKCategoryTypeIdentifierShortnessOfBreath",
        "HKCategoryTypeIdentifierSinusCongestion",
        "HKCategoryTypeIdentifierSkippedHeartbeat",
        "HKCategoryTypeIdentifierSleepAnalysis",
        "HKCategoryTypeIdentifierSleepChanges",
        "HKCategoryTypeIdentifierSoreThroat",
        "HKCategoryTypeIdentifierToothbrushingEvent",
        "HKCategoryTypeIdentifierVaginalDryness",
        "HKCategoryTypeIdentifierVomiting",
        "HKCategoryTypeIdentifierWheezing",
    ]
}
