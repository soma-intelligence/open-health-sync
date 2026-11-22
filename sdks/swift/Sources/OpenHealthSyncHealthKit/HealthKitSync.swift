// SPDX-License-Identifier: AGPL-3.0-only

#if canImport(HealthKit)
import CoreLocation
import Foundation
import HealthKit
import OpenHealthSync

/// Errors of the HealthKit side.
public enum HealthKitSyncError: Error, Sendable, Equatable {
    /// HealthKit answered `false` without an error.
    case refused(String)
}

/// Reads HealthKit with anchored queries and uploads what changed since the last run.
///
/// For each type, the samples added since the stored anchor are converted into upload lines,
/// grouped into parts by type and local day (in the sample's `HKTimeZone`), and uploaded
/// under a fresh batch id; the UUIDs of the samples deleted since that anchor follow as
/// deletion lines, once the server has said it applies them (see `AnchoredSync`). The new
/// anchor is saved only once every part of the round is uploaded, so an interrupted sync
/// sends the same samples and deletions again next time, and the server applies them once.
///
/// Workout routes, ECG recordings and heartbeat series follow the same rules. Their data is
/// read with its own query (`HKWorkoutRouteQuery`, `HKElectrocardiogramQuery`,
/// `HKHeartbeatSeriesQuery`); a route and an ECG are read a batch at a time and uploaded in
/// parts of at most `seriesPartBytes`, so a long recording is never held in memory whole.
///
/// `HealthKitSync.allSupportedTypes` is every type the server stores.
@available(macOS 13.0, *)
public final class HealthKitSync: @unchecked Sendable {
    public let store: HKHealthStore
    public let client: OHSClient
    public let anchors: AnchorStore
    /// The prefix of every batch id.
    public let batchPrefix: String
    /// The most samples one anchored query returns; a type with more is read in rounds.
    public let queryLimit: Int
    /// The most workout routes, ECG recordings or heartbeat series one round reads; the
    /// anchor is saved after each round.
    public let seriesQueryLimit: Int
    /// The most NDJSON bytes of one part of a route or an ECG, before compression.
    public let seriesPartBytes: Int

    /// What the server applies, learnt from its upload answers.
    public let features = ServerFeatures()

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
        queryLimit: Int = 5000,
        seriesQueryLimit: Int = 25,
        seriesPartBytes: Int = AnchoredSync.defaultSeriesPartBytes
    ) {
        self.store = store
        self.client = client
        self.anchors = anchors
        self.batchPrefix = batchPrefix
        self.queryLimit = max(1, queryLimit)
        self.seriesQueryLimit = max(1, seriesQueryLimit)
        self.seriesPartBytes = max(1, seriesPartBytes)
    }

    // MARK: - Types

    /// Every HealthKit type the server stores that this OS version has: quantities
    /// (nutrition included), categories (sleep included), workouts and their routes, ECG
    /// recordings, heartbeat series, audiograms and states of mind. Built from
    /// `AppleHealthTypes`, the table the sync reads samples by. Pass it to
    /// `requestAuthorization`, `sync` and `enableBackgroundDelivery`, or a subset of it.
    ///
    /// Correlations (blood pressure, food) are left out on purpose: their members are
    /// quantity samples of types that are in the set, and HealthKit refuses an authorization
    /// request for a correlation type.
    public static var allSupportedTypes: Set<HKSampleType> {
        return types(AppleHealthTypes.allIdentifiers)
    }

    /// Heart, body, activity and hearing quantities (no nutrition).
    public static var quantityTypes: Set<HKSampleType> {
        return types(AppleHealthTypes.quantityIdentifiers)
    }

    /// Dietary quantities, stored as nutrition logs.
    public static var nutritionTypes: Set<HKSampleType> {
        return types(AppleHealthTypes.nutritionIdentifiers)
    }

    /// Category types, sleep analysis included.
    public static var categoryTypes: Set<HKSampleType> {
        return types(AppleHealthTypes.categoryIdentifiers)
    }

    /// Workouts and workout routes. A route is only readable when workouts are too.
    public static var workoutTypes: Set<HKSampleType> {
        return types(AppleHealthTypes.workoutIdentifiers)
    }

    /// ECG recordings and heartbeat series.
    public static var heartRhythmTypes: Set<HKSampleType> {
        return types(AppleHealthTypes.heartRhythmIdentifiers)
    }

    /// The HealthKit type of an identifier of `AppleHealthTypes`; nil when the server does
    /// not store the type or this OS version does not have it.
    public static func sampleType(forIdentifier identifier: String) -> HKSampleType? {
        guard let kind = AppleHealthTypes.kind(of: identifier) else {
            return nil
        }
        switch kind {
        case .quantity:
            return HKObjectType.quantityType(forIdentifier: HKQuantityTypeIdentifier(rawValue: identifier))
        case .category:
            return HKObjectType.categoryType(forIdentifier: HKCategoryTypeIdentifier(rawValue: identifier))
        case .correlation:
            return HKObjectType.correlationType(forIdentifier: HKCorrelationTypeIdentifier(rawValue: identifier))
        case .workout:
            return HKObjectType.workoutType()
        case .workoutRoute:
            return HKSeriesType.workoutRoute()
        case .electrocardiogram:
            return HKObjectType.electrocardiogramType()
        case .heartbeatSeries:
            return HKSeriesType.heartbeat()
        case .audiogram:
            return HKObjectType.audiogramSampleType()
        case .stateOfMind:
            if #available(iOS 18.0, macOS 15.0, watchOS 11.0, visionOS 2.0, *) {
                return HKObjectType.stateOfMindType()
            }
            return nil
        }
    }

    private static func types(_ identifiers: [String]) -> Set<HKSampleType> {
        var out = Set<HKSampleType>()
        for identifier in identifiers {
            if let type = sampleType(forIdentifier: identifier) {
                out.insert(type)
            }
        }
        return out
    }

    /// The types to ask read access for in order to sync `types`: a correlation type is
    /// replaced by the quantity types of its members (HealthKit raises an exception when
    /// asked for a correlation type), and a series type comes with its parent type
    /// (HealthKit raises an exception when the parent is missing): heart rate variability
    /// for heartbeat series, workouts for workout routes.
    public static func authorizationTypes(for types: Set<HKSampleType>) -> Set<HKObjectType> {
        var read = Set<HKObjectType>()
        for type in types {
            if type.identifier == AppleHealthTypes.heartbeatSeries,
               let parent = HKObjectType.quantityType(forIdentifier: .heartRateVariabilitySDNN) {
                read.insert(parent)
            }
            if type == HKSeriesType.workoutRoute() {
                read.insert(HKObjectType.workoutType())
            }
            guard type is HKCorrelationType else {
                read.insert(type)
                continue
            }
            let members = type.identifier == AppleHealthTypes.foodCorrelation
                ? AppleHealthTypes.nutritionIdentifiers
                : ["HKQuantityTypeIdentifierBloodPressureSystolic", "HKQuantityTypeIdentifierBloodPressureDiastolic"]
            for member in members {
                if let memberType = sampleType(forIdentifier: member) {
                    read.insert(memberType)
                }
            }
        }
        return read
    }

    /// Asks the user for read access to `types`. HealthKit does not tell an app whether
    /// read access was granted: a type the user declined just returns no samples.
    public func requestAuthorization(for types: Set<HKSampleType>) async throws {
        let read = HealthKitSync.authorizationTypes(for: types)
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

    /// Uploads the samples of `types` added or deleted since the last sync. Calls are
    /// serialized: a sync started while another runs waits for it.
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
    /// keeps each record once, and records each sample's UUID on the row it stands for, so the
    /// history stored by an older version becomes deletable). Pending deletions are kept.
    public func resetAnchor(for type: HKSampleType) {
        anchors.setAnchorData(nil, for: type.identifier)
    }

    // MARK: - One type

    private func syncType(_ type: HKSampleType, report: inout SyncReport) async throws {
        let limit = HealthKitChangeSource.readsSeries(type) ? min(queryLimit, seriesQueryLimit) : queryLimit
        let sync = AnchoredSync(
            source: HealthKitChangeSource(store: store, type: type),
            uploader: client, anchors: anchors, features: features, batchPrefix: batchPrefix,
            queryLimit: limit, seriesPartBytes: seriesPartBytes)
        try await sync.sync(type.identifier, report: &report)
    }
}

/// Runs `HKAnchoredObjectQuery` for one type and converts what it returns: the new samples
/// into upload lines, the deleted objects into their UUIDs.
@available(macOS 13.0, *)
struct HealthKitChangeSource: AnchoredChangeSource {
    let store: HKHealthStore
    let type: HKSampleType

    func changes(of typeIdentifier: String, after anchor: Data?, limit: Int) async throws -> AnchoredChanges {
        let (samples, deleted, newAnchor) = try await query(after: unarchive(anchor), limit: limit)
        var changes = AnchoredChanges(
            deletedUUIDs: deleted.map { $0.uuid.uuidString },
            limitReached: samples.count + deleted.count >= limit,
            newAnchor: archive(newAnchor))
        for sample in samples {
            if let route = sample as? HKWorkoutRoute {
                changes.series.append(try await routeUpload(route))
            } else if let ecg = sample as? HKElectrocardiogram {
                changes.series.append(ecgUpload(ecg))
            } else if let series = sample as? HKHeartbeatSeriesSample {
                let line = HealthKitLines.heartbeatSeriesLine(for: series, beats: try await beats(of: series))
                if line.beats.count < 2 {
                    // One beat gives no interval: nothing the server would store.
                    changes.skippedSamples += 1
                } else {
                    for part in line.split(maxBeats: HealthKitChangeSource.maxBeatsPerLine) {
                        changes.lines.append(part)
                    }
                }
            } else if let line = HealthKitLines.line(for: sample) {
                changes.lines.append(line)
            } else {
                changes.skippedSamples += 1
            }
        }
        return changes
    }

    /// Whether the samples of `type` have data read with a query of its own.
    static func readsSeries(_ type: HKSampleType) -> Bool {
        switch AppleHealthTypes.kind(of: type.identifier) {
        case .workoutRoute?, .electrocardiogram?, .heartbeatSeries?:
            return true
        default:
            return false
        }
    }

    /// The most beats of one heartbeat line; a longer series is cut (`split(maxBeats:)`).
    static let maxBeatsPerLine = 5000
    /// How many GPS points or ECG voltages go from the query to the upload at a time.
    static let seriesBatch = 1024

    // MARK: Workout routes

    /// The upload of one route: its points, read in batches, under the line of the workout
    /// it belongs to. Without that workout (not readable, or not saved yet) the points go
    /// alone and the server attaches them to a stored workout that overlaps them in time.
    private func routeUpload(_ route: HKWorkoutRoute) async throws -> SeriesUpload {
        var header: JSONObject?
        var zone = HealthKitLines.convertMetadata(route.metadata)["HKTimeZone"]?.stringValue
        if let workout = try await parentWorkout(of: route),
           let line = HealthKitLines.line(for: workout) {
            header = line.jsonObject()
            zone = line.timeZoneIdentifier ?? zone
        }
        let store = self.store
        let pipe = SeriesLinePipe { pipe in
            let query = HKWorkoutRouteQuery(route: route) { query, locations, done, error in
                if let error = error {
                    pipe.finish(throwing: error)
                    return
                }
                let lines = (locations ?? []).map { HealthKitLines.routePointLine(for: $0).jsonObject() }
                if !pipe.send(lines) {
                    store.stop(query)
                    return
                }
                if done {
                    pipe.finish()
                }
            }
            pipe.setOnCancel { store.stop(query) }
            store.execute(query)
        }
        return SeriesUpload(startDate: route.startDate, timeZoneIdentifier: zone, header: header, reader: pipe)
    }

    /// The workout `route` is attached to: among the workouts that overlap it in time, the
    /// one whose routes include it. nil when workouts are not readable.
    private func parentWorkout(of route: HKWorkoutRoute) async throws -> HKWorkout? {
        let slack: TimeInterval = 300
        let window = HKQuery.predicateForSamples(
            withStart: route.startDate.addingTimeInterval(-slack),
            end: route.endDate.addingTimeInterval(slack), options: [])
        let candidates: [HKSample]
        do {
            candidates = try await samples(of: HKObjectType.workoutType(), matching: window)
        } catch let error as HKError
            where error.code == .errorAuthorizationNotDetermined || error.code == .errorAuthorizationDenied {
            return nil
        }
        for case let workout as HKWorkout in candidates.sorted(by: { $0.startDate > $1.startDate }) {
            let routes = try await samples(
                of: HKSeriesType.workoutRoute(), matching: HKQuery.predicateForObjects(from: workout))
            if routes.contains(where: { $0.uuid == route.uuid }) {
                return workout
            }
        }
        return nil
    }

    private func samples(of type: HKSampleType, matching predicate: NSPredicate) async throws -> [HKSample] {
        let store = self.store
        return try await withCheckedThrowingContinuation {
            (continuation: CheckedContinuation<[HKSample], Error>) in
            let query = HKSampleQuery(
                sampleType: type, predicate: predicate, limit: HKObjectQueryNoLimit, sortDescriptors: nil
            ) { _, samples, error in
                if let error = error {
                    continuation.resume(throwing: error)
                } else {
                    continuation.resume(returning: samples ?? [])
                }
            }
            store.execute(query)
        }
    }

    // MARK: ECG

    /// The upload of one recording: its line, then its voltages read in batches.
    private func ecgUpload(_ ecg: HKElectrocardiogram) -> SeriesUpload {
        let line = HealthKitLines.electrocardiogramLine(for: ecg)
        let store = self.store
        let pipe = SeriesLinePipe { pipe in
            let pending = LineBatch()
            let query = HKElectrocardiogramQuery(ecg) { query, result in
                switch result {
                case .measurement(let measurement):
                    guard let volts = measurement.quantity(for: .appleWatchSimilarToLeadI)?
                        .doubleValue(for: HKUnit.volt())
                    else { return }
                    pending.lines.append(ECGVoltageLine(
                        timeSinceSampleStart: measurement.timeSinceSampleStart, volts: volts).jsonObject())
                    if pending.lines.count >= HealthKitChangeSource.seriesBatch {
                        if !pipe.send(pending.take()) {
                            store.stop(query)
                        }
                    }
                case .done:
                    pipe.send(pending.take())
                    pipe.finish()
                case .error(let error):
                    pipe.finish(throwing: error)
                @unknown default:
                    break
                }
            }
            pipe.setOnCancel { store.stop(query) }
            store.execute(query)
        }
        return SeriesUpload(
            startDate: line.startDate, timeZoneIdentifier: line.timeZoneIdentifier,
            header: line.jsonObject(), headerIsOwnRecord: true, reader: pipe)
    }

    // MARK: Heartbeat series

    /// The beats of one series. A series is short (a minute of beats for an HRV reading), so
    /// it is read whole.
    private func beats(of series: HKHeartbeatSeriesSample) async throws -> [Heartbeat] {
        let store = self.store
        return try await withCheckedThrowingContinuation {
            (continuation: CheckedContinuation<[Heartbeat], Error>) in
            let collected = BeatBatch()
            let query = HKHeartbeatSeriesQuery(heartbeatSeries: series) { _, time, precededByGap, done, error in
                guard !collected.finished else { return }
                if let error = error {
                    collected.finished = true
                    continuation.resume(throwing: error)
                    return
                }
                collected.beats.append(Heartbeat(timeSinceSeriesStart: time, precededByGap: precededByGap))
                if done {
                    collected.finished = true
                    continuation.resume(returning: collected.beats)
                }
            }
            store.execute(query)
        }
    }

    private func query(after anchor: HKQueryAnchor?, limit: Int) async throws
        -> ([HKSample], [HKDeletedObject], HKQueryAnchor?)
    {
        let store = self.store
        return try await withCheckedThrowingContinuation {
            (continuation: CheckedContinuation<([HKSample], [HKDeletedObject], HKQueryAnchor?), Error>) in
            let query = HKAnchoredObjectQuery(
                type: type, predicate: nil, anchor: anchor, limit: limit
            ) { _, samples, deleted, newAnchor, error in
                if let error = error {
                    continuation.resume(throwing: error)
                } else {
                    continuation.resume(returning: (samples ?? [], deleted ?? [], newAnchor))
                }
            }
            store.execute(query)
        }
    }

    private func unarchive(_ data: Data?) -> HKQueryAnchor? {
        guard let data = data else {
            return nil
        }
        return try? NSKeyedUnarchiver.unarchivedObject(ofClass: HKQueryAnchor.self, from: data)
    }

    private func archive(_ anchor: HKQueryAnchor?) -> Data? {
        guard let anchor = anchor else {
            return nil
        }
        return try? NSKeyedArchiver.archivedData(withRootObject: anchor, requiringSecureCoding: true)
    }
}

/// The lines a query handler gathers before it hands them over. HealthKit calls the data
/// handler of one query serially, so the box needs no lock.
private final class LineBatch: @unchecked Sendable {
    var lines: [JSONObject] = []

    func take() -> [JSONObject] {
        let out = lines
        lines = []
        return out
    }
}

/// The beats a heartbeat query gathers; `finished` guards the continuation.
private final class BeatBatch: @unchecked Sendable {
    var beats: [Heartbeat] = []
    var finished = false
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
    /// The line for `sample`, or nil when the server does not store it (a type that is not
    /// in `AppleHealthTypes`, a unit that does not fit) or when its data is read with a query
    /// of its own (a workout route, an ECG, a heartbeat series: `HealthKitSync` reads those).
    public static func line(for sample: HKSample) -> AppleHealthLine? {
        guard AppleHealthTypes.kind(of: sample.sampleType.identifier) != nil else {
            return nil
        }
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

        if let audiogram = sample as? HKAudiogramSample {
            let hertz = HKUnit.hertz()
            let dBHL = HKUnit.decibelHearingLevel()
            let points = audiogram.sensitivityPoints.map { point in
                AudiogramPoint(
                    frequency: point.frequency.doubleValue(for: hertz),
                    leftEarSensitivity: point.leftEarSensitivity?.doubleValue(for: dBHL),
                    rightEarSensitivity: point.rightEarSensitivity?.doubleValue(for: dBHL))
            }
            return AudiogramLine(
                startDate: audiogram.startDate, endDate: audiogram.endDate, sensitivityPoints: points,
                provenance: provenance, metadata: metadata)
        }

        if #available(iOS 18.0, macOS 15.0, watchOS 11.0, visionOS 2.0, *) {
            if let mind = sample as? HKStateOfMind {
                return StateOfMindLine(
                    startDate: mind.startDate, endDate: mind.endDate, kind: mind.kind.rawValue,
                    valence: mind.valence, labels: mind.labels.map { $0.rawValue },
                    associations: mind.associations.map { $0.rawValue },
                    provenance: provenance, metadata: metadata)
            }
        }

        return nil
    }

    /// The line of an ECG recording; its voltages are read with `HKElectrocardiogramQuery`.
    public static func electrocardiogramLine(for ecg: HKElectrocardiogram) -> ECGLine {
        let bpm = HKUnit.count().unitDivided(by: HKUnit.minute())
        return ECGLine(
            startDate: ecg.startDate, endDate: ecg.endDate,
            classification: ecg.classification.rawValue, symptomsStatus: ecg.symptomsStatus.rawValue,
            averageHeartRate: ecg.averageHeartRate?.doubleValue(for: bpm),
            samplingFrequency: ecg.samplingFrequency?.doubleValue(for: HKUnit.hertz()),
            numberOfVoltageMeasurements: ecg.numberOfVoltageMeasurements,
            provenance: provenanceOf(ecg), metadata: convertMetadata(ecg.metadata))
    }

    /// The line of a heartbeat series whose beats were read with `HKHeartbeatSeriesQuery`.
    public static func heartbeatSeriesLine(for series: HKHeartbeatSeriesSample, beats: [Heartbeat]) -> HeartbeatSeriesLine {
        return HeartbeatSeriesLine(
            startDate: series.startDate, endDate: series.endDate, beats: beats,
            provenance: provenanceOf(series), metadata: convertMetadata(series.metadata))
    }

    /// One GPS point of a workout route. An altitude, speed or course Core Location marks
    /// as invalid (a negative accuracy or value) is left out.
    public static func routePointLine(for location: CLLocation) -> RoutePointLine {
        return RoutePointLine(
            timestamp: location.timestamp,
            latitude: location.coordinate.latitude, longitude: location.coordinate.longitude,
            altitude: location.verticalAccuracy >= 0 ? location.altitude : nil,
            speed: location.speed >= 0 ? location.speed : nil,
            course: location.course >= 0 ? location.course : nil)
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
