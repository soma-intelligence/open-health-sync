// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import OpenHealthSync

// The anchored sync of one type, with HealthKit kept behind `AnchoredChangeSource` so the
// rules on parts and anchors are tested on every platform. `HealthKitSync` runs it over
// `HKAnchoredObjectQuery`.

/// What one `sync` call sent.
public struct SyncReport: Sendable, Equatable {
    /// The batch ids used, one per query round that had new or deleted samples.
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

/// Syncs one type round by round. For each round, the new samples are grouped into parts by
/// local day, and the round's deletion lines open every one of those parts (a round with
/// deletions only sends them in one part dated today). The anchor is saved only once every
/// part of the round is uploaded, samples and deletions alike: an interrupted sync reads the
/// same round again, and the server keeps each record and each deletion once.
public struct AnchoredSync {
    public let source: AnchoredChangeSource
    public let uploader: AppleHealthPartUploader
    public let anchors: AnchorStore
    public let batchPrefix: String
    public let queryLimit: Int
    /// The zone of lines without `HKTimeZone`, and of the day of a deletions only part.
    public let timeZone: TimeZone
    /// The clock that dates a deletions only part.
    public let now: () -> Date

    public init(
        source: AnchoredChangeSource,
        uploader: AppleHealthPartUploader,
        anchors: AnchorStore,
        batchPrefix: String = "ios",
        queryLimit: Int = 5000,
        timeZone: TimeZone = .current,
        now: @escaping () -> Date = Date.init
    ) {
        self.source = source
        self.uploader = uploader
        self.anchors = anchors
        self.batchPrefix = batchPrefix
        self.queryLimit = max(1, queryLimit)
        self.timeZone = timeZone
        self.now = now
    }

    /// Uploads the changes of `typeIdentifier` since its stored anchor.
    public func sync(_ typeIdentifier: String, report: inout SyncReport) async throws {
        var anchor = anchors.anchorData(for: typeIdentifier)
        while true {
            let changes = try await source.changes(of: typeIdentifier, after: anchor, limit: queryLimit)
            report.skippedSamples += changes.skippedSamples
            if !changes.lines.isEmpty || !changes.deletedUUIDs.isEmpty {
                let batch = AppleHealthPartBuilder.newBatchId(prefix: batchPrefix)
                var builder = try AppleHealthPartBuilder(batch: batch)
                for line in changes.lines {
                    try builder.add(line, defaultTimeZone: timeZone)
                }
                for uuid in changes.deletedUUIDs {
                    let line = DeletionLine(uuid: uuid).jsonObject()
                    if changes.lines.isEmpty {
                        try builder.add(
                            type: typeIdentifier,
                            day: AppleHealthPartBuilder.localDay(now(), timeZone: timeZone),
                            line: line)
                    } else {
                        builder.addToEveryPart(line: line)
                    }
                }
                let results = try await uploader.uploadParts(builder.build())
                report.batchIds.append(batch)
                report.partsUploaded += results.count
                report.duplicateParts += results.filter { $0.duplicate }.count
                if !changes.lines.isEmpty {
                    report.linesByType[typeIdentifier, default: 0] += changes.lines.count
                }
                if !changes.deletedUUIDs.isEmpty {
                    report.deletionsByType[typeIdentifier, default: 0] += changes.deletedUUIDs.count
                }
            }
            // Every part of this round, deletions included, is on the server: the anchor can move.
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
