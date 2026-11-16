// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import OpenHealthSync

// The anchored sync of one type, with HealthKit kept behind `AnchoredChangeSource` so the
// rules on parts, deletions and anchors are tested on every platform. `HealthKitSync` runs it
// over `HKAnchoredObjectQuery`.

/// What one `sync` call sent.
public struct SyncReport: Sendable, Equatable {
    /// The batch ids used: one per upload of new samples, one per upload of deletions.
    public var batchIds: [String] = []
    /// Parts uploaded (duplicates included).
    public var partsUploaded = 0
    /// Parts the server already had (`duplicate: true`).
    public var duplicateParts = 0
    /// Lines uploaded, by HealthKit type identifier. Deletion lines are not counted here.
    public var linesByType: [String: Int] = [:]
    /// Deleted samples sent, by HealthKit type identifier, each counted once however many
    /// parts carried it.
    public var deletionsByType: [String: Int] = [:]
    /// Deleted samples kept for a later sync, by HealthKit type identifier, because no upload
    /// answer has said yet that the server applies deletion lines.
    public var deletionsPendingByType: [String: Int] = [:]
    /// New samples left out because the server does not store their type or unit.
    public var skippedSamples = 0

    public init() {}

    /// Lines uploaded, all types together.
    public var linesUploaded: Int { linesByType.values.reduce(0, +) }
    /// Deleted samples sent, all types together.
    public var deletionsUploaded: Int { deletionsByType.values.reduce(0, +) }
}

/// Reads the lines of one series (the points of a workout route, the voltages of an ECG) a
/// batch at a time, so a long series is never held in memory whole.
public protocol SeriesLineReader: AnyObject, Sendable {
    /// The next lines, in order; nil once the series is read.
    func next() async throws -> [JSONObject]?
    /// Stops reading. Called once the series is uploaded or the upload failed.
    func cancel()
}

/// A new sample whose data is read apart from it and uploaded in parts of bounded size.
public struct SeriesUpload: Sendable {
    /// The start of the sample; with the time zone it decides the day of its parts.
    public var startDate: Date
    /// The `HKTimeZone` of the sample, when it has one.
    public var timeZoneIdentifier: String?
    /// The line every part of the series starts with: the workout of a route, the recording
    /// of an ECG. The server attaches the lines of a part to the one parent line in it, so
    /// the parent is repeated at the top of each part (it is stored once).
    public var header: JSONObject?
    /// Whether `header` is a record of this type (an ECG recording): it is then counted in
    /// the report and uploaded even when the series has no line. The workout line of a route
    /// is not: the workout type uploads it.
    public var headerIsOwnRecord: Bool
    public var reader: any SeriesLineReader

    public init(
        startDate: Date, timeZoneIdentifier: String? = nil, header: JSONObject? = nil,
        headerIsOwnRecord: Bool = false, reader: any SeriesLineReader
    ) {
        self.startDate = startDate
        self.timeZoneIdentifier = timeZoneIdentifier
        self.header = header
        self.headerIsOwnRecord = headerIsOwnRecord
        self.reader = reader
    }
}

/// What one anchored query round returned for one type.
public struct AnchoredChanges: Sendable {
    /// The new samples the server stores, as upload lines.
    public var lines: [any AppleHealthLine]
    /// The new samples whose data is read and uploaded in parts (workout routes, ECG).
    public var series: [SeriesUpload]
    /// The UUIDs of the deleted samples (`HKDeletedObject.uuid`).
    public var deletedUUIDs: [String]
    /// New samples left out because the server does not store their type or unit.
    public var skippedSamples: Int
    /// Whether the round returned as many objects as the limit: more may follow.
    public var limitReached: Bool
    /// The archived anchor after this round; nil keeps the stored one.
    public var newAnchor: Data?

    public init(
        lines: [any AppleHealthLine] = [], series: [SeriesUpload] = [],
        deletedUUIDs: [String] = [], skippedSamples: Int = 0,
        limitReached: Bool = false, newAnchor: Data? = nil
    ) {
        self.lines = lines
        self.series = series
        self.deletedUUIDs = deletedUUIDs
        self.skippedSamples = skippedSamples
        self.limitReached = limitReached
        self.newAnchor = newAnchor
    }
}

/// Reads the samples added and deleted since an anchor, at most `limit` objects.
public protocol AnchoredChangeSource {
    func changes(of typeIdentifier: String, after anchor: Data?, limit: Int) async throws -> AnchoredChanges
}

/// Uploads Apple Health parts; `OHSClient` is one.
public protocol AppleHealthPartUploader {
    func uploadParts(_ parts: [AppleHealthPart]) async throws -> [UploadResult]
}

extension OHSClient: AppleHealthPartUploader {}

/// What the server is known to apply, learnt from its upload answers. Shared by the syncs of
/// every type of one `HealthKitSync`, so the first answer of a run tells them all.
public final class ServerFeatures: @unchecked Sendable {
    private let lock = NSLock()
    private var deletionLines: Bool?

    public init() {}

    /// Whether the server applies deletion lines: nil until an upload answered.
    public var acceptsDeletionLines: Bool? {
        lock.lock()
        defer { lock.unlock() }
        return deletionLines
    }

    /// Records what the answers of an upload say.
    public func learn(from results: [UploadResult]) {
        guard let last = results.last else { return }
        lock.lock()
        defer { lock.unlock() }
        deletionLines = last.acceptsDeletionLines
    }
}

/// Syncs one type round by round. For each round, the new samples are grouped into parts by
/// local day and uploaded; then the round's deletions go in one part of their own, dated
/// today, under another batch id. The server keeps one alias per HealthKit sample of a stored
/// row, so a deletion never takes a row another sample still stands for, whatever order the
/// parts are processed in.
///
/// A series (a workout route, an ECG) is read a batch of lines at a time and uploaded in parts
/// of at most `seriesPartBytes` of NDJSON, each under the series' own batch id and each starting
/// with the series' parent line, so memory stays bounded however long the series is.
///
/// Deletions are sent only to a server whose upload answers list `deletion_lines`. Until one
/// did (an older server, or no upload yet in this run), they are kept beside the anchor, under
/// `<type>.pendingDeletions` in the `AnchorStore`, and sent with a later round. The pending list
/// is saved before the anchor moves, and the anchor only moves once every part of the round is
/// uploaded: an interrupted sync reads the same round again, and the server keeps each record
/// and each deletion once.
public struct AnchoredSync {
    public let source: AnchoredChangeSource
    public let uploader: AppleHealthPartUploader
    public let anchors: AnchorStore
    public let features: ServerFeatures
    public let batchPrefix: String
    public let queryLimit: Int
    /// The most NDJSON bytes of one part of a series, before compression.
    public let seriesPartBytes: Int
    /// The zone of lines without `HKTimeZone`, and of the day of a deletion part.
    public let timeZone: TimeZone
    /// The clock that dates a deletion part.
    public let now: () -> Date

    public init(
        source: AnchoredChangeSource,
        uploader: AppleHealthPartUploader,
        anchors: AnchorStore,
        features: ServerFeatures = ServerFeatures(),
        batchPrefix: String = "ios",
        queryLimit: Int = 5000,
        seriesPartBytes: Int = AnchoredSync.defaultSeriesPartBytes,
        timeZone: TimeZone = .current,
        now: @escaping () -> Date = Date.init
    ) {
        self.source = source
        self.uploader = uploader
        self.anchors = anchors
        self.features = features
        self.batchPrefix = batchPrefix
        self.queryLimit = max(1, queryLimit)
        self.seriesPartBytes = max(1, seriesPartBytes)
        self.timeZone = timeZone
        self.now = now
    }

    /// 2 MiB: an Apple Watch ECG (about 15000 voltages) fits in one part, and an hour of GPS
    /// points in a fraction of one.
    public static let defaultSeriesPartBytes = 2 * 1024 * 1024

    /// The key of a type's pending deletions in the `AnchorStore`.
    public static func pendingKey(_ typeIdentifier: String) -> String {
        return typeIdentifier + ".pendingDeletions"
    }

    /// The deletions of `typeIdentifier` not sent yet.
    public func pendingDeletions(_ typeIdentifier: String) -> [String] {
        guard let data = anchors.anchorData(for: AnchoredSync.pendingKey(typeIdentifier)),
              let ids = try? JSONDecoder().decode([String].self, from: data)
        else {
            return []
        }
        return ids
    }

    private func setPendingDeletions(_ ids: [String], for typeIdentifier: String) {
        let key = AnchoredSync.pendingKey(typeIdentifier)
        if ids.isEmpty {
            anchors.setAnchorData(nil, for: key)
        } else {
            anchors.setAnchorData(try? JSONEncoder().encode(ids), for: key)
        }
    }

    /// Uploads the changes of `typeIdentifier` since its stored anchor, and the deletions
    /// still pending from earlier rounds.
    public func sync(_ typeIdentifier: String, report: inout SyncReport) async throws {
        var anchor = anchors.anchorData(for: typeIdentifier)
        var pending = pendingDeletions(typeIdentifier)
        while true {
            let changes = try await source.changes(of: typeIdentifier, after: anchor, limit: queryLimit)
            report.skippedSamples += changes.skippedSamples
            if !changes.lines.isEmpty {
                let batch = AppleHealthPartBuilder.newBatchId(prefix: batchPrefix)
                var builder = try AppleHealthPartBuilder(batch: batch)
                for line in changes.lines {
                    try builder.add(line, defaultTimeZone: timeZone)
                }
                let results = try await uploader.uploadParts(builder.build())
                features.learn(from: results)
                report.batchIds.append(batch)
                report.partsUploaded += results.count
                report.duplicateParts += results.filter { $0.duplicate }.count
                report.linesByType[typeIdentifier, default: 0] += changes.lines.count
            }
            for series in changes.series {
                try await upload(series, of: typeIdentifier, report: &report)
            }

            var seen = Set(pending)
            for id in changes.deletedUUIDs where seen.insert(id).inserted {
                pending.append(id)
            }
            if !pending.isEmpty && features.acceptsDeletionLines == true {
                let batch = AppleHealthPartBuilder.newBatchId(prefix: batchPrefix)
                var builder = try AppleHealthPartBuilder(batch: batch)
                let day = AppleHealthPartBuilder.localDay(now(), timeZone: timeZone)
                for id in pending {
                    try builder.add(type: typeIdentifier, day: day, line: DeletionLine(uuid: id).jsonObject())
                }
                let results = try await uploader.uploadParts(builder.build())
                features.learn(from: results)
                report.batchIds.append(batch)
                report.partsUploaded += results.count
                report.duplicateParts += results.filter { $0.duplicate }.count
                report.deletionsByType[typeIdentifier, default: 0] += pending.count
                pending = []
            }
            // The deletions not sent are kept first; then, every part of the round being on
            // the server, the anchor can move.
            setPendingDeletions(pending, for: typeIdentifier)
            if pending.isEmpty {
                report.deletionsPendingByType[typeIdentifier] = nil
            } else {
                report.deletionsPendingByType[typeIdentifier] = pending.count
            }
            if let newAnchor = changes.newAnchor {
                anchors.setAnchorData(newAnchor, for: typeIdentifier)
                anchor = newAnchor
            }
            if !changes.limitReached || changes.newAnchor == nil {
                return
            }
        }
    }

    /// Uploads one series part by part: lines are read a batch at a time and a part goes out
    /// as soon as it is full, so at most one part is held in memory.
    private func upload(_ series: SeriesUpload, of typeIdentifier: String, report: inout SyncReport) async throws {
        defer { series.reader.cancel() }
        let zone = series.timeZoneIdentifier.flatMap { TimeZone(identifier: $0) } ?? timeZone
        let day = AppleHealthPartBuilder.localDay(series.startDate, timeZone: zone)
        let batch = AppleHealthPartBuilder.newBatchId(prefix: batchPrefix)
        let header = series.header.map { object -> Data in
            var bytes = JSONValue.object(object).compactJSONData()
            bytes.append(0x0a)
            return bytes
        }

        var body = Data()
        var linesInPart = 0
        var partNumber = 0
        var uploaded = 0
        var sent = false

        func flush() async throws {
            let part = AppleHealthPart(
                type: typeIdentifier, day: day, batch: batch, partNumber: partNumber,
                body: Gzip.compress(body), lineCount: linesInPart + (header == nil ? 0 : 1),
                uncompressedBytes: body.count)
            let results = try await uploader.uploadParts([part])
            features.learn(from: results)
            report.partsUploaded += results.count
            report.duplicateParts += results.filter { $0.duplicate }.count
            uploaded += linesInPart
            partNumber += 1
            body = Data()
            linesInPart = 0
            sent = true
        }

        while let lines = try await series.reader.next() {
            for line in lines {
                var bytes = JSONValue.object(line).compactJSONData()
                bytes.append(0x0a)
                if linesInPart > 0 && body.count + bytes.count > seriesPartBytes {
                    try await flush()
                }
                if linesInPart == 0, let header = header {
                    body.append(header)
                }
                body.append(bytes)
                linesInPart += 1
            }
        }
        if linesInPart > 0 {
            try await flush()
        } else if !sent, series.headerIsOwnRecord, let header = header {
            body.append(header)
            try await flush()
        }
        guard sent else {
            return
        }
        report.batchIds.append(batch)
        report.linesByType[typeIdentifier, default: 0] += uploaded + (series.headerIsOwnRecord ? 1 : 0)
    }
}

/// A `SeriesLineReader` over lines already in memory, a batch per call; for tests and for
/// series short enough to hold.
public final class ArraySeriesLineReader: SeriesLineReader, @unchecked Sendable {
    private let lock = NSLock()
    private var batches: [[JSONObject]]
    private var cancelled = false

    public init(batches: [[JSONObject]]) {
        self.batches = batches
    }

    /// Whether `cancel` was called.
    public var isCancelled: Bool {
        lock.lock()
        defer { lock.unlock() }
        return cancelled
    }

    public func next() async throws -> [JSONObject]? {
        return take()
    }

    private func take() -> [JSONObject]? {
        lock.lock()
        defer { lock.unlock() }
        if cancelled || batches.isEmpty {
            return nil
        }
        return batches.removeFirst()
    }

    public func cancel() {
        lock.lock()
        defer { lock.unlock() }
        cancelled = true
    }
}

/// Hands batches of lines from a callback (a HealthKit query handler) to an async reader,
/// holding at most `capacity` batches: `send` blocks the calling thread while the buffer is
/// full, so a query that reads faster than the upload goes cannot fill memory.
public final class SeriesLinePipe: SeriesLineReader, @unchecked Sendable {
    private let condition = NSCondition()
    private let capacity: Int
    private var buffer: [[JSONObject]] = []
    private var finished = false
    private var failure: Error?
    private var cancelled = false
    private var waiter: CheckedContinuation<[JSONObject]?, Error>?
    private var started = false
    private let start: (SeriesLinePipe) -> Void
    private var onCancel: (() -> Void)?

    /// - Parameters:
    ///   - capacity: how many batches wait at most.
    ///   - start: starts the producer; called once, at the first `next`, so a series is only
    ///     read when its turn to upload comes.
    public init(capacity: Int = 2, start: @escaping (SeriesLinePipe) -> Void) {
        self.capacity = max(1, capacity)
        self.start = start
    }

    /// Sets what `cancel` runs (stopping the query).
    public func setOnCancel(_ action: @escaping () -> Void) {
        condition.lock()
        onCancel = action
        condition.unlock()
    }

    /// Adds a batch; blocks while `capacity` batches wait. Returns false once the reader
    /// cancelled: the producer should stop.
    @discardableResult
    public func send(_ lines: [JSONObject]) -> Bool {
        if lines.isEmpty {
            return !isCancelled
        }
        condition.lock()
        while buffer.count >= capacity && waiter == nil && !cancelled {
            condition.wait()
        }
        if cancelled {
            condition.unlock()
            return false
        }
        if let waiting = waiter {
            waiter = nil
            condition.unlock()
            waiting.resume(returning: lines)
            return true
        }
        buffer.append(lines)
        condition.unlock()
        return true
    }

    /// Ends the series: with `error`, the reader throws it once the waiting batches are read.
    public func finish(throwing error: Error? = nil) {
        condition.lock()
        if finished {
            condition.unlock()
            return
        }
        finished = true
        failure = error
        let waiting = buffer.isEmpty ? waiter : nil
        if waiting != nil {
            waiter = nil
        }
        condition.unlock()
        if let waiting = waiting {
            if let error = error {
                waiting.resume(throwing: error)
            } else {
                waiting.resume(returning: nil)
            }
        }
    }

    public var isCancelled: Bool {
        condition.lock()
        defer { condition.unlock() }
        return cancelled
    }

    public func next() async throws -> [JSONObject]? {
        if beginIfNeeded() {
            start(self)
        }
        return try await withCheckedThrowingContinuation { continuation in
            condition.lock()
            if !buffer.isEmpty {
                let lines = buffer.removeFirst()
                condition.signal()
                condition.unlock()
                continuation.resume(returning: lines)
            } else if finished || cancelled {
                let error = failure
                condition.unlock()
                if let error = error {
                    continuation.resume(throwing: error)
                } else {
                    continuation.resume(returning: nil)
                }
            } else {
                waiter = continuation
                condition.unlock()
            }
        }
    }

    private func beginIfNeeded() -> Bool {
        condition.lock()
        defer { condition.unlock() }
        if started || cancelled {
            return false
        }
        started = true
        return true
    }

    public func cancel() {
        condition.lock()
        if cancelled {
            condition.unlock()
            return
        }
        cancelled = true
        let waiting = waiter
        waiter = nil
        let action = onCancel
        onCancel = nil
        buffer = []
        condition.broadcast()
        condition.unlock()
        waiting?.resume(returning: nil)
        action?()
    }
}
