// SPDX-License-Identifier: MIT

import Foundation

// Upload lines in the format the server's Apple Health connector reads
// (docs/connectors/apple-health.md#line-formats). These types hold plain values, so they
// work and are tested on every platform; the HealthKit target only converts HealthKit
// samples into them.

/// One record in the server's line format.
public protocol AppleHealthLine: Sendable {
    /// The `X-OHS-Type` of the part the line goes in.
    var typeIdentifier: String { get }
    /// The record's start; with the time zone it decides the part's day.
    var startDate: Date { get }
    /// The `HKTimeZone` metadata value, when present.
    var timeZoneIdentifier: String? { get }
    /// The line as a JSON object.
    func jsonObject() -> JSONObject
}

/// Where a record came from. Every field is optional and left out of the line when nil.
public struct LineProvenance: Sendable, Equatable {
    public var sourceName: String?
    public var sourceBundleId: String?
    public var device: String?
    public var deviceModel: String?
    public var productType: String?
    public var deviceManufacturer: String?
    public var deviceHardwareVersion: String?
    public var deviceSoftwareVersion: String?
    public var osVersion: String?
    public var wasUserEntered: Bool?
    /// The HealthKit sample UUID.
    public var uuid: String?

    public init(
        sourceName: String? = nil,
        sourceBundleId: String? = nil,
        device: String? = nil,
        deviceModel: String? = nil,
        productType: String? = nil,
        deviceManufacturer: String? = nil,
        deviceHardwareVersion: String? = nil,
        deviceSoftwareVersion: String? = nil,
        osVersion: String? = nil,
        wasUserEntered: Bool? = nil,
        uuid: String? = nil
    ) {
        self.sourceName = sourceName
        self.sourceBundleId = sourceBundleId
        self.device = device
        self.deviceModel = deviceModel
        self.productType = productType
        self.deviceManufacturer = deviceManufacturer
        self.deviceHardwareVersion = deviceHardwareVersion
        self.deviceSoftwareVersion = deviceSoftwareVersion
        self.osVersion = osVersion
        self.wasUserEntered = wasUserEntered
        self.uuid = uuid
    }

    func write(into line: inout JSONObject) {
        func put(_ key: String, _ value: String?) {
            if let value = value {
                line[key] = .string(value)
            }
        }
        put("sourceName", sourceName)
        put("sourceBundleId", sourceBundleId)
        put("device", device)
        put("deviceModel", deviceModel)
        put("productType", productType)
        put("deviceManufacturer", deviceManufacturer)
        put("deviceHardwareVersion", deviceHardwareVersion)
        put("deviceSoftwareVersion", deviceSoftwareVersion)
        put("osVersion", osVersion)
        put("uuid", uuid)
        if let wasUserEntered = wasUserEntered {
            line["wasUserEntered"] = .bool(wasUserEntered)
        }
    }
}

private func commonFields(
    startDate: Date, endDate: Date, provenance: LineProvenance, metadata: JSONObject
) -> JSONObject {
    var line: JSONObject = [:]
    line["startDate"] = .string(RFC3339.format(startDate))
    line["endDate"] = .string(RFC3339.format(endDate))
    line["metadata"] = .object(metadata)
    provenance.write(into: &line)
    return line
}

/// A quantity sample: `quantityType`, `value` and `unit` (dietary types become nutrition
/// logs on the server).
public struct QuantityLine: AppleHealthLine, Equatable {
    public var quantityType: String
    public var startDate: Date
    public var endDate: Date
    public var value: Double
    /// The unit `value` is in; see `AppleHealthUnits.wireUnit(forQuantityType:)`.
    public var unit: String
    public var provenance: LineProvenance
    /// HealthKit metadata; `HKTimeZone` decides the local day.
    public var metadata: JSONObject

    public init(
        quantityType: String, startDate: Date, endDate: Date, value: Double, unit: String,
        provenance: LineProvenance = LineProvenance(), metadata: JSONObject = [:]
    ) {
        self.quantityType = quantityType
        self.startDate = startDate
        self.endDate = endDate
        self.value = value
        self.unit = unit
        self.provenance = provenance
        self.metadata = metadata
    }

    public var typeIdentifier: String { quantityType }
    public var timeZoneIdentifier: String? { metadata["HKTimeZone"]?.stringValue }

    public func jsonObject() -> JSONObject {
        var line = commonFields(startDate: startDate, endDate: endDate, provenance: provenance, metadata: metadata)
        line["quantityType"] = .string(quantityType)
        line["value"] = .number(value)
        line["unit"] = .string(unit)
        return line
    }
}

/// A category sample: `categoryType` and its integer `value` (sleep stages included).
public struct CategoryLine: AppleHealthLine, Equatable {
    public var categoryType: String
    public var startDate: Date
    public var endDate: Date
    public var value: Int
    public var provenance: LineProvenance
    public var metadata: JSONObject

    public init(
        categoryType: String, startDate: Date, endDate: Date, value: Int,
        provenance: LineProvenance = LineProvenance(), metadata: JSONObject = [:]
    ) {
        self.categoryType = categoryType
        self.startDate = startDate
        self.endDate = endDate
        self.value = value
        self.provenance = provenance
        self.metadata = metadata
    }

    public var typeIdentifier: String { categoryType }
    public var timeZoneIdentifier: String? { metadata["HKTimeZone"]?.stringValue }

    public func jsonObject() -> JSONObject {
        var line = commonFields(startDate: startDate, endDate: endDate, provenance: provenance, metadata: metadata)
        line["categoryType"] = .string(categoryType)
        line["value"] = .number(Double(value))
        return line
    }
}

/// A workout: the `HKWorkoutActivityType` raw value, the duration in seconds, the distance
/// in metres and the energy in kilocalories.
public struct WorkoutLine: AppleHealthLine, Equatable {
    public var workoutActivityType: Int
    public var startDate: Date
    public var endDate: Date
    public var durationS: Double
    /// Metres.
    public var totalDistance: Double?
    /// Kilocalories.
    public var totalEnergyBurned: Double?
    /// Beats per minute.
    public var averageHeartRate: Double?
    public var minHeartRate: Double?
    public var maxHeartRate: Double?
    public var provenance: LineProvenance
    public var metadata: JSONObject

    public init(
        workoutActivityType: Int, startDate: Date, endDate: Date, durationS: Double,
        totalDistance: Double? = nil, totalEnergyBurned: Double? = nil,
        averageHeartRate: Double? = nil, minHeartRate: Double? = nil, maxHeartRate: Double? = nil,
        provenance: LineProvenance = LineProvenance(), metadata: JSONObject = [:]
    ) {
        self.workoutActivityType = workoutActivityType
        self.startDate = startDate
        self.endDate = endDate
        self.durationS = durationS
        self.totalDistance = totalDistance
        self.totalEnergyBurned = totalEnergyBurned
        self.averageHeartRate = averageHeartRate
        self.minHeartRate = minHeartRate
        self.maxHeartRate = maxHeartRate
        self.provenance = provenance
        self.metadata = metadata
    }

    public var typeIdentifier: String { AppleHealthUnits.workoutTypeIdentifier }
    public var timeZoneIdentifier: String? { metadata["HKTimeZone"]?.stringValue }

    public func jsonObject() -> JSONObject {
        var line = commonFields(startDate: startDate, endDate: endDate, provenance: provenance, metadata: metadata)
        line["workoutActivityType"] = .number(Double(workoutActivityType))
        line["durationS"] = .number(durationS)
        if let d = totalDistance {
            line["totalDistance"] = .number(d)
            line["totalDistanceUnit"] = "m"
        }
        if let e = totalEnergyBurned {
            line["totalEnergyBurned"] = .number(e)
            line["totalEnergyBurnedUnit"] = "kcal"
        }
        if let v = averageHeartRate { line["averageHeartRate"] = .number(v) }
        if let v = minHeartRate { line["minHeartRate"] = .number(v) }
        if let v = maxHeartRate { line["maxHeartRate"] = .number(v) }
        return line
    }
}

/// One member of a correlation.
public struct CorrelationObject: Sendable, Equatable {
    public var sampleType: String
    public var startDate: Date
    public var endDate: Date
    public var value: Double
    public var unit: String

    public init(sampleType: String, startDate: Date, endDate: Date, value: Double, unit: String) {
        self.sampleType = sampleType
        self.startDate = startDate
        self.endDate = endDate
        self.value = value
        self.unit = unit
    }

    func jsonObject() -> JSONObject {
        return [
            "sampleType": .string(sampleType),
            "startDate": .string(RFC3339.format(startDate)),
            "endDate": .string(RFC3339.format(endDate)),
            "value": .number(value),
            "unit": .string(unit),
        ]
    }
}

/// A correlation: blood pressure (systolic and diastolic) or food.
public struct CorrelationLine: AppleHealthLine, Equatable {
    public var correlationType: String
    public var startDate: Date
    public var endDate: Date
    public var objects: [CorrelationObject]
    public var provenance: LineProvenance
    public var metadata: JSONObject

    public init(
        correlationType: String, startDate: Date, endDate: Date, objects: [CorrelationObject],
        provenance: LineProvenance = LineProvenance(), metadata: JSONObject = [:]
    ) {
        self.correlationType = correlationType
        self.startDate = startDate
        self.endDate = endDate
        self.objects = objects
        self.provenance = provenance
        self.metadata = metadata
    }

    public var typeIdentifier: String { correlationType }
    public var timeZoneIdentifier: String? { metadata["HKTimeZone"]?.stringValue }

    public func jsonObject() -> JSONObject {
        var line = commonFields(startDate: startDate, endDate: endDate, provenance: provenance, metadata: metadata)
        line["correlationType"] = .string(correlationType)
        line["objects"] = .array(objects.map { JSONValue.object($0.jsonObject()) })
        return line
    }
}

/// The units the SDK writes values in, one per HealthKit quantity type the server stores.
/// Each is a spelling the server converts to its storage unit (connectors/applehealth/units.go
/// and internal/model/units.go).
public enum AppleHealthUnits {
    /// The `X-OHS-Type` of workout parts.
    public static let workoutTypeIdentifier = "HKWorkoutTypeIdentifier"
    /// The blood pressure correlation type.
    public static let bloodPressureCorrelationIdentifier = "HKCorrelationTypeIdentifierBloodPressure"

    /// The wire unit for a HealthKit quantity type identifier, or nil when the server does
    /// not store the type (its lines would be skipped).
    public static func wireUnit(forQuantityType identifier: String) -> String? {
        if let unit = quantityUnits[identifier] {
            return unit
        }
        let prefix = "HKQuantityTypeIdentifierDietary"
        if identifier.hasPrefix(prefix) {
            return dietaryUnits[String(identifier.dropFirst(prefix.count))]
        }
        return nil
    }

    /// Every quantity type identifier with a wire unit, dietary types included.
    public static var quantityTypeIdentifiers: [String] {
        let dietary = dietaryUnits.keys.map { "HKQuantityTypeIdentifierDietary" + $0 }
        return (Array(quantityUnits.keys) + dietary).sorted()
    }

    static let quantityUnits: [String: String] = [
        // Heart and lungs.
        "HKQuantityTypeIdentifierHeartRate": "count/min",
        "HKQuantityTypeIdentifierRestingHeartRate": "count/min",
        "HKQuantityTypeIdentifierWalkingHeartRateAverage": "count/min",
        "HKQuantityTypeIdentifierHeartRateVariabilitySDNN": "ms",
        "HKQuantityTypeIdentifierOxygenSaturation": "%",
        "HKQuantityTypeIdentifierBloodPressureSystolic": "mmHg",
        "HKQuantityTypeIdentifierBloodPressureDiastolic": "mmHg",
        "HKQuantityTypeIdentifierRespiratoryRate": "count/min",
        "HKQuantityTypeIdentifierVO2Max": "mL/kg/min",
        "HKQuantityTypeIdentifierForcedExpiratoryVolume1": "L",
        "HKQuantityTypeIdentifierForcedVitalCapacity": "L",
        "HKQuantityTypeIdentifierPeakExpiratoryFlowRate": "L/min",
        "HKQuantityTypeIdentifierInhalerUsage": "count",
        // Temperatures.
        "HKQuantityTypeIdentifierBodyTemperature": "degC",
        "HKQuantityTypeIdentifierBasalBodyTemperature": "degC",
        "HKQuantityTypeIdentifierAppleSleepingWristTemperature": "degC",
        "HKQuantityTypeIdentifierWaterTemperature": "degC",
        // Body.
        "HKQuantityTypeIdentifierBodyMass": "kg",
        "HKQuantityTypeIdentifierLeanBodyMass": "kg",
        "HKQuantityTypeIdentifierHeight": "cm",
        "HKQuantityTypeIdentifierWaistCircumference": "cm",
        "HKQuantityTypeIdentifierBodyMassIndex": "count",
        "HKQuantityTypeIdentifierBodyFatPercentage": "%",
        "HKQuantityTypeIdentifierBloodGlucose": "mg/dL",
        "HKQuantityTypeIdentifierBloodAlcoholContent": "%",
        "HKQuantityTypeIdentifierElectrodermalActivity": "mcS",
        "HKQuantityTypeIdentifierInsulinDelivery": "IU",
        "HKQuantityTypeIdentifierNumberOfAlcoholicBeverages": "count",
        "HKQuantityTypeIdentifierNumberOfTimesFallen": "count",
        "HKQuantityTypeIdentifierUVExposure": "count",
        // Activity.
        "HKQuantityTypeIdentifierStepCount": "count",
        "HKQuantityTypeIdentifierFlightsClimbed": "count",
        "HKQuantityTypeIdentifierPushCount": "count",
        "HKQuantityTypeIdentifierNikeFuel": "count",
        "HKQuantityTypeIdentifierDistanceWalkingRunning": "m",
        "HKQuantityTypeIdentifierDistanceCycling": "m",
        "HKQuantityTypeIdentifierDistanceSwimming": "m",
        "HKQuantityTypeIdentifierDistanceWheelchair": "m",
        "HKQuantityTypeIdentifierDistanceDownhillSnowSports": "m",
        "HKQuantityTypeIdentifierUnderwaterDepth": "m",
        "HKQuantityTypeIdentifierBasalEnergyBurned": "kcal",
        "HKQuantityTypeIdentifierActiveEnergyBurned": "kcal",
        "HKQuantityTypeIdentifierAppleExerciseTime": "min",
        "HKQuantityTypeIdentifierAppleMoveTime": "min",
        "HKQuantityTypeIdentifierAppleStandTime": "min",
        "HKQuantityTypeIdentifierPhysicalEffort": "kcal/kg/h",
        "HKQuantityTypeIdentifierRunningStrideLength": "m",
        "HKQuantityTypeIdentifierRunningVerticalOscillation": "cm",
        "HKQuantityTypeIdentifierRunningGroundContactTime": "ms",
        "HKQuantityTypeIdentifierRunningPower": "W",
        "HKQuantityTypeIdentifierCyclingPower": "W",
        "HKQuantityTypeIdentifierRunningSpeed": "m/s",
        "HKQuantityTypeIdentifierWalkingSpeed": "m/s",
        // Hearing.
        "HKQuantityTypeIdentifierEnvironmentalAudioExposure": "dBASPL",
        "HKQuantityTypeIdentifierHeadphoneAudioExposure": "dBASPL",
        "HKQuantityTypeIdentifierEnvironmentalSoundReduction": "dBASPL",
    ]

    /// Dietary types, keyed by the part after `HKQuantityTypeIdentifierDietary`. Masses of
    /// the core nutrition columns are in grams (sodium in milligrams); the others keep
    /// their unit next to the value on the server.
    static let dietaryUnits: [String: String] = [
        "EnergyConsumed": "kcal",
        "Water": "mL",
        "Protein": "g",
        "Carbohydrates": "g",
        "FatTotal": "g",
        "FatSaturated": "g",
        "FatMonounsaturated": "g",
        "FatPolyunsaturated": "g",
        "Fiber": "g",
        "Sugar": "g",
        "Sodium": "mg",
        "Caffeine": "mg",
        "Cholesterol": "mg",
        "Calcium": "mg",
        "Chloride": "mg",
        "Copper": "mg",
        "Iron": "mg",
        "Magnesium": "mg",
        "Manganese": "mg",
        "Phosphorus": "mg",
        "Potassium": "mg",
        "Zinc": "mg",
        "Niacin": "mg",
        "PantothenicAcid": "mg",
        "Riboflavin": "mg",
        "Thiamin": "mg",
        "VitaminB6": "mg",
        "VitaminC": "mg",
        "VitaminE": "mg",
        "Biotin": "mcg",
        "Chromium": "mcg",
        "Folate": "mcg",
        "Iodine": "mcg",
        "Molybdenum": "mcg",
        "Selenium": "mcg",
        "VitaminA": "mcg",
        "VitaminB12": "mcg",
        "VitaminD": "mcg",
        "VitaminK": "mcg",
    ]
}
