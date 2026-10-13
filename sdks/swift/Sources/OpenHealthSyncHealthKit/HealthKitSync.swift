// SPDX-License-Identifier: AGPL-3.0-only

#if canImport(HealthKit)
import Foundation
import HealthKit
import OpenHealthSync

/// What one `sync` call sent.
public struct SyncReport: Sendable, Equatable {
    /// The batch ids used, one per query round that had new samples.
    public var batchIds: [String] = []
    /// Parts uploaded (duplicates included).
    public var partsUploaded = 0
    /// Parts the server already had (`duplicate: true`).
    public var duplicateParts = 0
    /// Lines uploaded, by HealthKit type identifier.
    public var linesByType: [String: Int] = [:]
    /// New samples left out because the server does not store their type or unit.
    public var skippedSamples = 0

    public init() {}

    /// Lines uploaded, all types together.
    public var linesUploaded: Int { linesByType.values.reduce(0, +) }
}

/// Errors of the HealthKit side.
public enum HealthKitSyncError: Error, Sendable, Equatable {
    /// HealthKit answered `false` without an error.
    case refused(String)
}

/// Reads HealthKit with anchored queries and uploads what changed since the last run.
///
/// For each type, the samples added since the stored anchor are converted into upload lines,
/// grouped into parts by type and local day (in the sample's `HKTimeZone`), and uploaded
/// under a fresh batch id. The new anchor is saved only once every part of that type is
/// uploaded, so an interrupted sync sends the same samples again next time, and the server
/// stores them once.
@available(macOS 13.0, *)
public final class HealthKitSync: @unchecked Sendable {
    public let store: HKHealthStore
    public let client: OHSClient
    public let anchors: AnchorStore
    /// The prefix of every batch id.
    public let batchPrefix: String
    /// The most samples one anchored query returns; a type with more is read in rounds.
    public let queryLimit: Int

    private let queue = SyncQueue()
    private let lock = NSLock()
    private var observers: [HKObserverQuery] = []

    /// - Parameters:
    ///   - store: the app's health store.
    ///   - client: a client whose token provider returns a user token with the `upload` scope.
    ///   - anchors: where anchors are kept between runs.
    public init(
        store: HKHealthStore,
        client: OHSClient,
        anchors: AnchorStore = UserDefaultsAnchorStore(),
        batchPrefix: String = "ios",
        queryLimit: Int = 5000
    ) {
        self.store = store
        self.client = client
        self.anchors = anchors
        self.batchPrefix = batchPrefix
        self.queryLimit = max(1, queryLimit)
    }

    /// Asks the user for read access to `types`. HealthKit does not tell an app whether
    /// read access was granted: a type the user declined just returns no samples.
    public func requestAuthorization(for types: Set<HKSampleType>) async throws {
        var read = Set<HKObjectType>()
        for type in types {
            read.insert(type)
        }
        let store = self.store
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
            store.requestAuthorization(toShare: nil, read: read) { success, error in
                if let error = error {
                    continuation.resume(throwing: error)
                } else if !success {
                    continuation.resume(throwing: HealthKitSyncError.refused("authorization request"))
                } else {
                    continuation.resume()
                }
            }
        }
    }

    /// Uploads the samples of `types` added since the last sync. Calls are serialized: a sync
    /// started while another runs waits for it.
    public func sync(types: Set<HKSampleType>) async throws -> SyncReport {
        let sorted = types.sorted { $0.identifier < $1.identifier }
        return try await queue.run { [self] in
            var report = SyncReport()
            for type in sorted {
                try await self.syncType(type, report: &report)
            }
            return report
        }
    }

    /// Turns on background delivery for `types` and runs an observer query per type that
    /// calls `sync` when HealthKit reports new samples. Call it once at every app launch
    /// (in `application(_:didFinishLaunchingWithOptions:)`), after authorization.
    public func enableBackgroundDelivery(
        for types: Set<HKSampleType>,
        frequency: HKUpdateFrequency = .immediate,
        onSync: (@Sendable (Result<SyncReport, Error>) -> Void)? = nil
    ) async throws {
        let store = self.store
        for type in types.sorted(by: { $0.identifier < $1.identifier }) {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
                store.enableBackgroundDelivery(for: type, frequency: frequency) { success, error in
                    if let error = error {
                        continuation.resume(throwing: error)
                    } else if !success {
                        continuation.resume(throwing: HealthKitSyncError.refused("background delivery"))
                    } else {
                        continuation.resume()
                    }
                }
            }
            let query = HKObserverQuery(sampleType: type, predicate: nil) { [weak self] _, completionHandler, error in
                guard let self = self, error == nil else {
                    completionHandler()
                    return
                }
                Task {
                    do {
                        let report = try await self.sync(types: [type])
                        onSync?(.success(report))
                    } catch {
                        onSync?(.failure(error))
                    }
                    completionHandler()
                }
            }
            store.execute(query)
            addObserver(query)
        }
    }

    /// NSLock may not be taken directly in an async function (an error in Swift 6), so the
    /// append goes through this synchronous helper.
    private func addObserver(_ query: HKObserverQuery) {
        lock.lock()
        defer { lock.unlock() }
        observers.append(query)
    }

    /// Stops the observer queries started by `enableBackgroundDelivery`.
    public func stopObserving() {
        lock.lock()
        let running = observers
        observers = []
        lock.unlock()
        for query in running {
            store.stop(query)
        }
    }

    /// Forgets the anchor of `type`: the next sync uploads all its samples again (the server
    /// keeps each record once).
    public func resetAnchor(for type: HKSampleType) {
        anchors.setAnchorData(nil, for: type.identifier)
    }

    // MARK: - One type

    private func syncType(_ type: HKSampleType, report: inout SyncReport) async throws {
        var anchor = loadAnchor(type.identifier)
        while true {
            let (samples, newAnchor) = try await anchoredQuery(type: type, anchor: anchor)
            if samples.isEmpty {
                saveAnchor(newAnchor, for: type.identifier)
                return
            }
            let batch = AppleHealthPartBuilder.newBatchId(prefix: batchPrefix)
            var builder = try AppleHealthPartBuilder(batch: batch)
            var lines = 0
            for sample in samples {
                guard let line = HealthKitLines.line(for: sample) else {
                    report.skippedSamples += 1
                    continue
                }
                let zone = line.timeZoneIdentifier.flatMap { TimeZone(identifier: $0) } ?? TimeZone.current
                try builder.add(
                    type: line.typeIdentifier,
                    day: AppleHealthPartBuilder.localDay(line.startDate, timeZone: zone),
                    line: line.jsonObject())
                lines += 1
            }
            if !builder.isEmpty {
                let parts = builder.build()
                let results = try await client.uploadParts(parts)
                report.batchIds.append(batch)
                report.partsUploaded += results.count
                report.duplicateParts += results.filter { $0.duplicate }.count
                report.linesByType[type.identifier, default: 0] += lines
            }
            // Every part of this round is on the server: the anchor can move.
            saveAnchor(newAnchor, for: type.identifier)
            if samples.count < queryLimit {
                return
            }
            anchor = newAnchor
        }
    }

    private func anchoredQuery(type: HKSampleType, anchor: HKQueryAnchor?) async throws -> ([HKSample], HKQueryAnchor?) {
        let store = self.store
        let limit = queryLimit
        return try await withCheckedThrowingContinuation {
            (continuation: CheckedContinuation<([HKSample], HKQueryAnchor?), Error>) in
            let query = HKAnchoredObjectQuery(
                type: type, predicate: nil, anchor: anchor, limit: limit
            ) { _, samples, _, newAnchor, error in
                if let error = error {
                    continuation.resume(throwing: error)
                } else {
                    continuation.resume(returning: (samples ?? [], newAnchor))
                }
            }
            store.execute(query)
        }
    }

    private func loadAnchor(_ identifier: String) -> HKQueryAnchor? {
        guard let data = anchors.anchorData(for: identifier) else {
            return nil
        }
        return try? NSKeyedUnarchiver.unarchivedObject(ofClass: HKQueryAnchor.self, from: data)
    }

    private func saveAnchor(_ anchor: HKQueryAnchor?, for identifier: String) {
        guard let anchor = anchor,
              let data = try? NSKeyedArchiver.archivedData(withRootObject: anchor, requiringSecureCoding: true)
        else {
            return
        }
        anchors.setAnchorData(data, for: identifier)
    }
}

/// Runs operations one after the other.
actor SyncQueue {
    private var tail: Task<Void, Never>?

    func run<T: Sendable>(_ operation: @escaping @Sendable () async throws -> T) async throws -> T {
        let previous = tail
        let task = Task<T, Error> {
            _ = await previous?.value
            return try await operation()
        }
        tail = Task<Void, Never> {
            _ = try? await task.value
        }
        return try await task.value
    }
}

// MARK: - Conversion

/// Converts HealthKit samples into upload lines.
@available(macOS 13.0, *)
public enum HealthKitLines {
    /// The line for `sample`, or nil when the server does not store it (unknown quantity
    /// type, a unit that does not fit, another kind of sample).
    public static func line(for sample: HKSample) -> AppleHealthLine? {
        let metadata = convertMetadata(sample.metadata)
        let provenance = provenanceOf(sample)

        if let q = sample as? HKQuantitySample {
            let identifier = q.quantityType.identifier
            guard let wire = AppleHealthUnits.wireUnit(forQuantityType: identifier),
                  let unit = HealthKitUnits.unit(forWire: wire),
                  q.quantity.is(compatibleWith: unit)
            else {
                return nil
            }
            return QuantityLine(
                quantityType: identifier, startDate: q.startDate, endDate: q.endDate,
                value: q.quantity.doubleValue(for: unit), unit: wire,
                provenance: provenance, metadata: metadata)
        }

        if let c = sample as? HKCategorySample {
            return CategoryLine(
                categoryType: c.categoryType.identifier, startDate: c.startDate, endDate: c.endDate,
                value: c.value, provenance: provenance, metadata: metadata)
        }

        if let w = sample as? HKWorkout {
            var line = WorkoutLine(
                workoutActivityType: Int(w.workoutActivityType.rawValue),
                startDate: w.startDate, endDate: w.endDate, durationS: w.duration,
                totalDistance: w.totalDistance?.doubleValue(for: HKUnit.meter()),
                totalEnergyBurned: w.totalEnergyBurned?.doubleValue(for: HKUnit.kilocalorie()),
                provenance: provenance, metadata: metadata)
            if #available(iOS 16.0, macOS 13.0, watchOS 9.0, *) {
                if let heartRate = HKQuantityType.quantityType(forIdentifier: .heartRate),
                   let stats = w.statistics(for: heartRate) {
                    let bpm = HKUnit.count().unitDivided(by: HKUnit.minute())
                    line.averageHeartRate = stats.averageQuantity()?.doubleValue(for: bpm)
                    line.minHeartRate = stats.minimumQuantity()?.doubleValue(for: bpm)
                    line.maxHeartRate = stats.maximumQuantity()?.doubleValue(for: bpm)
                }
            }
            return line
        }

        if let correlation = sample as? HKCorrelation {
            var objects: [CorrelationObject] = []
            for member in correlation.objects {
                guard let q = member as? HKQuantitySample else { continue }
                let identifier = q.quantityType.identifier
                guard let wire = AppleHealthUnits.wireUnit(forQuantityType: identifier),
                      let unit = HealthKitUnits.unit(forWire: wire),
                      q.quantity.is(compatibleWith: unit)
                else { continue }
                objects.append(CorrelationObject(
                    sampleType: identifier, startDate: q.startDate, endDate: q.endDate,
                    value: q.quantity.doubleValue(for: unit), unit: wire))
            }
            if objects.isEmpty {
                return nil
            }
            objects.sort { $0.sampleType < $1.sampleType }
            return CorrelationLine(
                correlationType: correlation.correlationType.identifier,
                startDate: correlation.startDate, endDate: correlation.endDate,
                objects: objects, provenance: provenance, metadata: metadata)
        }

        return nil
    }

    static func provenanceOf(_ sample: HKSample) -> LineProvenance {
        let revision = sample.sourceRevision
        let os = revision.operatingSystemVersion
        let wasUserEntered = (sample.metadata?[HKMetadataKeyWasUserEntered] as? Bool) ?? false
        return LineProvenance(
            sourceName: revision.source.name,
            sourceBundleId: revision.source.bundleIdentifier,
            device: sample.device?.name,
            deviceModel: sample.device?.model,
            productType: revision.productType,
            deviceManufacturer: sample.device?.manufacturer,
            deviceHardwareVersion: sample.device?.hardwareVersion,
            deviceSoftwareVersion: sample.device?.softwareVersion,
            osVersion: "\(os.majorVersion).\(os.minorVersion).\(os.patchVersion)",
            wasUserEntered: wasUserEntered,
            uuid: sample.uuid.uuidString)
    }

    /// HealthKit metadata as JSON: strings stay strings, numbers numbers, booleans booleans,
    /// dates RFC 3339, quantities their description (`1.25 m/s`). `HKTimeZone` is set to the
    /// current zone when HealthKit recorded none.
    public static func convertMetadata(_ metadata: [String: Any]?) -> JSONObject {
        var out: JSONObject = [:]
        for (key, value) in metadata ?? [:] {
            if let converted = jsonValue(value) {
                out[key] = converted
            }
        }
        if out["HKTimeZone"] == nil {
            out["HKTimeZone"] = .string(TimeZone.current.identifier)
        }
        return out
    }

    static func jsonValue(_ value: Any) -> JSONValue? {
        if let s = value as? String {
            return .string(s)
        }
        if let n = value as? NSNumber {
            if CFGetTypeID(n) == CFBooleanGetTypeID() {
                return .bool(n.boolValue)
            }
            let d = n.doubleValue
            return d.isFinite ? .number(d) : nil
        }
        if let d = value as? Date {
            return .string(RFC3339.format(d))
        }
        if let q = value as? HKQuantity {
            return .string(q.description)
        }
        return .string(String(describing: value))
    }
}

/// The `HKUnit` for each wire unit of `AppleHealthUnits`, built with HealthKit's own
/// constructors rather than parsed from strings.
@available(macOS 13.0, *)
public enum HealthKitUnits {
    public static func unit(forWire wire: String) -> HKUnit? {
        switch wire {
        case "count/min":
            return HKUnit.count().unitDivided(by: HKUnit.minute())
        case "count":
            return HKUnit.count()
        case "%":
            return HKUnit.percent()
        case "m":
            return HKUnit.meter()
        case "cm":
            return HKUnit.meterUnit(with: .centi)
        case "kg":
            return HKUnit.gramUnit(with: .kilo)
        case "g":
            return HKUnit.gram()
        case "mg":
            return HKUnit.gramUnit(with: .milli)
        case "mcg":
            return HKUnit.gramUnit(with: .micro)
        case "kcal":
            return HKUnit.kilocalorie()
        case "degC":
            return HKUnit.degreeCelsius()
        case "mmHg":
            return HKUnit.millimeterOfMercury()
        case "ms":
            return HKUnit.secondUnit(with: .milli)
        case "min":
            return HKUnit.minute()
        case "mg/dL":
            return HKUnit.gramUnit(with: .milli).unitDivided(by: HKUnit.literUnit(with: .deci))
        case "mL/kg/min":
            return HKUnit.literUnit(with: .milli)
                .unitDivided(by: HKUnit.gramUnit(with: .kilo).unitMultiplied(by: HKUnit.minute()))
        case "kcal/kg/h":
            return HKUnit.kilocalorie()
                .unitDivided(by: HKUnit.gramUnit(with: .kilo).unitMultiplied(by: HKUnit.hour()))
        case "m/s":
            return HKUnit.meter().unitDivided(by: HKUnit.second())
        case "W":
            if #available(iOS 16.0, macOS 13.0, watchOS 9.0, *) {
                return HKUnit.watt()
            }
            return nil
        case "dBASPL":
            return HKUnit.decibelAWeightedSoundPressureLevel()
        case "mcS":
            return HKUnit.siemenUnit(with: .micro)
        case "L":
            return HKUnit.liter()
        case "mL":
            return HKUnit.literUnit(with: .milli)
        case "L/min":
            return HKUnit.liter().unitDivided(by: HKUnit.minute())
        case "IU":
            return HKUnit.internationalUnit()
        default:
            return nil
        }
    }
}
#endif
