// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import XCTest
@testable import OpenHealthSync
@testable import OpenHealthSyncHealthKit

/// Answers the anchored queries from a queue and records the anchors asked for.
final class FakeChangeSource: AnchoredChangeSource {
    var rounds: [AnchoredChanges]
    var anchorsAsked: [Data?] = []

    init(_ rounds: [AnchoredChanges]) {
        self.rounds = rounds
    }

    func changes(of typeIdentifier: String, after anchor: Data?, limit: Int) async throws -> AnchoredChanges {
        anchorsAsked.append(anchor)
        return rounds.isEmpty ? AnchoredChanges() : rounds.removeFirst()
    }
}

struct UploadFailed: Error {}

/// Records the parts it is given; fails the calls whose index is in `failing`. Its answers
/// list `features` (nil: a server older than the field).
final class FakeUploader: AppleHealthPartUploader {
    var calls: [[AppleHealthPart]] = []
    var failing: Set<Int> = []
    var features: [String]? = [UploadResult.deletionLines]

    func uploadParts(_ parts: [AppleHealthPart]) async throws -> [UploadResult] {
        let index = calls.count
        calls.append(parts)
        if failing.contains(index) {
            throw UploadFailed()
        }
        return parts.map { UploadResult(key: "raw/\($0.partNumber)", duplicate: false, features: features) }
    }
}

final class AnchoredSyncTests: XCTestCase {
    let type = "HKQuantityTypeIdentifierBodyMass"
    let paris = TimeZone(identifier: "Europe/Paris")!

    func weight(_ kg: Double, at iso: String, uuid: String) -> QuantityLine {
        return QuantityLine(
            quantityType: type, startDate: RFC3339.parse(iso)!, endDate: RFC3339.parse(iso)!,
            value: kg, unit: "kg", provenance: LineProvenance(uuid: uuid),
            metadata: ["HKTimeZone": "Europe/Paris"])
    }

    func lines(of part: AppleHealthPart) throws -> [JSONValue] {
        let text = String(decoding: try TestGunzip.gunzip(part.body), as: UTF8.self)
        return try text.split(separator: "\n").map { try JSONValue.parse(Data($0.utf8)) }
    }

    func deletion(_ uuid: String) -> JSONValue {
        return .object(["deletedUuid": .string(uuid)])
    }

    func makeSync(
        _ source: FakeChangeSource, _ uploader: FakeUploader, _ anchors: AnchorStore,
        features: ServerFeatures = ServerFeatures()
    ) -> AnchoredSync {
        return AnchoredSync(
            source: source, uploader: uploader, anchors: anchors, features: features, queryLimit: 10,
            timeZone: paris, now: { RFC3339.parse("2026-03-04T23:30:00Z")! })
    }

    func knownServer() -> ServerFeatures {
        let f = ServerFeatures()
        f.learn(from: [UploadResult(key: "k", duplicate: false, features: [UploadResult.deletionLines])])
        return f
    }

    // Samples first, then the round's deletions once, in a part of their own dated today.
    func testDeletionsFollowTheSamplesInOnePart() async throws {
        let source = FakeChangeSource([AnchoredChanges(
            lines: [weight(78.0, at: "2026-03-01T07:00:00Z", uuid: "NEW-1"),
                    weight(77.5, at: "2026-03-02T07:00:00Z", uuid: "NEW-2")],
            deletedUUIDs: ["OLD-1", "OLD-2", "OLD-1"],
            newAnchor: Data("a1".utf8))])
        let uploader = FakeUploader()
        let anchors = InMemoryAnchorStore()
        var report = SyncReport()
        try await makeSync(source, uploader, anchors).sync(type, report: &report)

        XCTAssertEqual(uploader.calls.count, 2)
        XCTAssertEqual(uploader.calls[0].map { $0.day }, ["2026-03-01", "2026-03-02"])
        for part in uploader.calls[0] {
            XCTAssertEqual(try lines(of: part).count, 1, "no deletion in the sample parts")
        }
        let del = uploader.calls[1]
        XCTAssertEqual(del.count, 1)
        XCTAssertEqual(del[0].type, type)
        XCTAssertEqual(del[0].day, "2026-03-05", "today in the sync's zone")
        XCTAssertEqual(try lines(of: del[0]), [deletion("OLD-1"), deletion("OLD-2")])
        XCTAssertEqual(report.batchIds.count, 2)
        XCTAssertEqual(anchors.anchorData(for: type), Data("a1".utf8))
        XCTAssertEqual(report.linesByType, [type: 2])
        XCTAssertEqual(report.deletionsByType, [type: 2])
        XCTAssertTrue(report.deletionsPendingByType.isEmpty)
        XCTAssertTrue(makeSync(source, uploader, anchors).pendingDeletions(type).isEmpty)
    }

    func testDeletionsOnlyGoOutOnceTheServerIsKnown() async throws {
        let anchors = InMemoryAnchorStore()
        let uploader = FakeUploader()
        var report = SyncReport()
        // Nothing uploaded yet in this run: the server is not known, the deletion waits.
        try await makeSync(FakeChangeSource([AnchoredChanges(deletedUUIDs: ["OLD-1"], newAnchor: Data("a1".utf8))]),
                           uploader, anchors).sync(type, report: &report)
        XCTAssertTrue(uploader.calls.isEmpty)
        XCTAssertEqual(report.deletionsPendingByType, [type: 1])
        XCTAssertEqual(anchors.anchorData(for: type), Data("a1".utf8))

        // Known: the pending deletion goes out even with nothing new.
        report = SyncReport()
        try await makeSync(FakeChangeSource([AnchoredChanges(newAnchor: Data("a2".utf8))]), uploader, anchors,
                           features: knownServer()).sync(type, report: &report)
        XCTAssertEqual(uploader.calls.count, 1)
        XCTAssertEqual(try lines(of: uploader.calls[0][0]), [deletion("OLD-1")])
        XCTAssertEqual(report.deletionsUploaded, 1)
        XCTAssertTrue(makeSync(FakeChangeSource([]), uploader, anchors).pendingDeletions(type).isEmpty)
    }

    // A server whose answers do not list deletion_lines (older than the deletion line) gets
    // the samples only; the deletions are kept, and go out once it lists the feature.
    func testOldServerKeepsTheDeletions() async throws {
        let anchors = InMemoryAnchorStore()
        let uploader = FakeUploader()
        uploader.features = nil
        let features = ServerFeatures()
        var report = SyncReport()
        try await makeSync(FakeChangeSource([AnchoredChanges(
            lines: [weight(78.0, at: "2026-03-01T07:00:00Z", uuid: "NEW-1")],
            deletedUUIDs: ["OLD-1"], newAnchor: Data("a1".utf8))]), uploader, anchors, features: features)
            .sync(type, report: &report)
        XCTAssertEqual(uploader.calls.count, 1, "samples only")
        XCTAssertEqual(features.acceptsDeletionLines, false)
        XCTAssertEqual(report.deletionsPendingByType, [type: 1])
        XCTAssertEqual(anchors.anchorData(for: type), Data("a1".utf8))

        // The server is upgraded: the next upload answer lists the feature.
        uploader.features = [UploadResult.deletionLines]
        report = SyncReport()
        try await makeSync(FakeChangeSource([AnchoredChanges(
            lines: [weight(77.0, at: "2026-03-02T07:00:00Z", uuid: "NEW-2")],
            deletedUUIDs: ["OLD-2"], newAnchor: Data("a2".utf8))]), uploader, anchors, features: features)
            .sync(type, report: &report)
        XCTAssertEqual(uploader.calls.count, 3)
        XCTAssertEqual(try lines(of: uploader.calls[2][0]), [deletion("OLD-1"), deletion("OLD-2")])
        XCTAssertTrue(report.deletionsPendingByType.isEmpty)
    }

    // The anchor moves only after the samples and the deletions of the round are uploaded:
    // a failed upload leaves it where it was, so the next sync sends both again.
    func testFailedUploadKeepsTheAnchor() async throws {
        for (round, failing) in [
            (AnchoredChanges(deletedUUIDs: ["OLD-1"], newAnchor: Data("a1".utf8)), 0),
            (AnchoredChanges(lines: [weight(78.0, at: "2026-03-01T07:00:00Z", uuid: "NEW-1")],
                             deletedUUIDs: ["OLD-1"], newAnchor: Data("a1".utf8)), 0),
            (AnchoredChanges(lines: [weight(78.0, at: "2026-03-01T07:00:00Z", uuid: "NEW-1")],
                             deletedUUIDs: ["OLD-1"], newAnchor: Data("a1".utf8)), 1),
        ] {
            let anchors = InMemoryAnchorStore()
            anchors.setAnchorData(Data("a0".utf8), for: type)
            let uploader = FakeUploader()
            uploader.failing = [failing]
            var report = SyncReport()
            do {
                try await makeSync(FakeChangeSource([round]), uploader, anchors, features: knownServer())
                    .sync(type, report: &report)
                XCTFail("the upload error must reach the caller")
            } catch is UploadFailed {}
            XCTAssertEqual(anchors.anchorData(for: type), Data("a0".utf8))

            // Next sync: the same round is read from the same anchor and sent again.
            let source = FakeChangeSource([round])
            let again = FakeUploader()
            try await makeSync(source, again, anchors, features: knownServer()).sync(type, report: &report)
            XCTAssertEqual(source.anchorsAsked, [Data("a0".utf8)])
            XCTAssertEqual(anchors.anchorData(for: type), Data("a1".utf8))
            XCTAssertEqual(try lines(of: again.calls.last![0]), [deletion("OLD-1")])
        }
    }

    func testRoundsFollowTheirAnchors() async throws {
        let source = FakeChangeSource([
            AnchoredChanges(lines: [weight(78.0, at: "2026-03-01T07:00:00Z", uuid: "NEW-1")],
                            limitReached: true, newAnchor: Data("a1".utf8)),
            AnchoredChanges(deletedUUIDs: ["OLD-1"], limitReached: true, newAnchor: Data("a2".utf8)),
            AnchoredChanges(newAnchor: Data("a3".utf8)),
        ])
        let uploader = FakeUploader()
        uploader.failing = [1]
        let anchors = InMemoryAnchorStore()
        var report = SyncReport()
        do {
            try await makeSync(source, uploader, anchors).sync(type, report: &report)
            XCTFail("the second round's upload fails")
        } catch is UploadFailed {}
        XCTAssertEqual(source.anchorsAsked, [nil, Data("a1".utf8)])
        XCTAssertEqual(anchors.anchorData(for: type), Data("a1".utf8), "the first round is on the server")
    }

    func testEmptyRoundMovesTheAnchor() async throws {
        let uploader = FakeUploader()
        let anchors = InMemoryAnchorStore()
        var report = SyncReport()
        try await makeSync(FakeChangeSource([AnchoredChanges(skippedSamples: 2, newAnchor: Data("a1".utf8))]),
                           uploader, anchors).sync(type, report: &report)
        XCTAssertTrue(uploader.calls.isEmpty)
        XCTAssertEqual(anchors.anchorData(for: type), Data("a1".utf8))
        XCTAssertEqual(report.skippedSamples, 2)
        XCTAssertTrue(report.batchIds.isEmpty)
    }

    func testDeletionLineFormat() {
        XCTAssertEqual(DeletionLine(uuid: "6F1A9D2B").jsonObject(), ["deletedUuid": "6F1A9D2B"])
    }

    func testUploadResultReadsFeatures() throws {
        let new = try JSONDecoder().decode(UploadResult.self, from: Data(#"{"key":"k","duplicate":false,"features":["deletion_lines"]}"#.utf8))
        XCTAssertTrue(new.acceptsDeletionLines)
        let old = try JSONDecoder().decode(UploadResult.self, from: Data(#"{"key":"k","duplicate":true}"#.utf8))
        XCTAssertFalse(old.acceptsDeletionLines)
        XCTAssertNil(old.features)
    }
}
