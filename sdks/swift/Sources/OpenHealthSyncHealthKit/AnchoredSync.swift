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

/// What one anchored query round returned for one type.
public struct AnchoredChanges: Sendable {
    /// The new samples the server stores, as upload lines.
    public var lines: [any AppleHealthLine]
    /// The UUIDs of the deleted samples (`HKDeletedObject.uuid`).
    public var deletedUUIDs: [String]
    /// New samples left out because the server does not store their type or unit.
    public var skippedSamples: Int
    /// Whether the round returned as many objects as the limit: more may follow.
    public var limitReached: Bool
    /// The archived anchor after this round; nil keeps the stored one.
    public var newAnchor: Data?

    public init(
        lines: [any AppleHealthLine] = [], deletedUUIDs: [String] = [], skippedSamples: Int = 0,
        limitReached: Bool = false, newAnchor: Data? = nil
    ) {
        self.lines = lines
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
        timeZone: TimeZone = .current,
        now: @escaping () -> Date = Date.init
    ) {
        self.source = source
        self.uploader = uploader
        self.anchors = anchors
        self.features = features
        self.batchPrefix = batchPrefix
        self.queryLimit = max(1, queryLimit)
        self.timeZone = timeZone
        self.now = now
    }

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
}
