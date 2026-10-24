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

/// Records the parts it is given; fails the calls whose index is in `failing`.
final class FakeUploader: AppleHealthPartUploader {
    var calls: [[AppleHealthPart]] = []
    var failing: Set<Int> = []

    func uploadParts(_ parts: [AppleHealthPart]) async throws -> [UploadResult] {
        let index = calls.count
        calls.append(parts)
        if failing.contains(index) {
            throw UploadFailed()
        }
        return parts.map { UploadResult(key: "raw/\($0.partNumber)", duplicate: false) }
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

    func makeSync(_ source: FakeChangeSource, _ uploader: FakeUploader, _ anchors: AnchorStore) -> AnchoredSync {
        return AnchoredSync(
            source: source, uploader: uploader, anchors: anchors, queryLimit: 10, timeZone: paris,
            now: { RFC3339.parse("2026-03-04T23:30:00Z")! })
    }

    func testDeletionLinesOpenEveryPartOfTheirRound() async throws {
        let source = FakeChangeSource([AnchoredChanges(
            lines: [weight(78.0, at: "2026-03-01T07:00:00Z", uuid: "NEW-1"),
                    weight(77.5, at: "2026-03-02T07:00:00Z", uuid: "NEW-2")],
            deletedUUIDs: ["OLD-1", "OLD-2"],
            newAnchor: Data("a1".utf8))])
        let uploader = FakeUploader()
        let anchors = InMemoryAnchorStore()
        var report = SyncReport()
        try await makeSync(source, uploader, anchors).sync(type, report: &report)

        XCTAssertEqual(uploader.calls.count, 1, "one upload call per round")
        let parts = uploader.calls[0]
        XCTAssertEqual(parts.map { $0.day }, ["2026-03-01", "2026-03-02"])
        XCTAssertEqual(Set(parts.map { $0.batch }).count, 1)
        for part in parts {
            let got = try lines(of: part)
            XCTAssertEqual(Array(got.prefix(2)), [deletion("OLD-1"), deletion("OLD-2")])
            XCTAssertEqual(got.count, 3)
            XCTAssertEqual(part.lineCount, 3)
        }
        XCTAssertEqual(anchors.anchorData(for: type), Data("a1".utf8))
        XCTAssertEqual(report.linesByType, [type: 2])
        XCTAssertEqual(report.deletionsByType, [type: 2], "each deletion counted once")
        XCTAssertEqual(report.partsUploaded, 2)
    }

    func testDeletionsOnlyGoInOnePartDatedToday() async throws {
        let source = FakeChangeSource([AnchoredChanges(deletedUUIDs: ["OLD-1"], newAnchor: Data("a1".utf8))])
        let uploader = FakeUploader()
        let anchors = InMemoryAnchorStore()
        var report = SyncReport()
        try await makeSync(source, uploader, anchors).sync(type, report: &report)

        let parts = try XCTUnwrap(uploader.calls.first)
        XCTAssertEqual(parts.count, 1)
        XCTAssertEqual(parts[0].type, type)
        XCTAssertEqual(parts[0].day, "2026-03-05", "today in the sync's zone")
        XCTAssertEqual(try lines(of: parts[0]), [deletion("OLD-1")])
        XCTAssertEqual(anchors.anchorData(for: type), Data("a1".utf8))
        XCTAssertEqual(report.deletionsUploaded, 1)
        XCTAssertEqual(report.linesUploaded, 0)
    }

    // The anchor moves only after the samples and the deletions of the round are uploaded:
    // a failed upload leaves it where it was, so the next sync sends both again.
    func testFailedUploadKeepsTheAnchor() async throws {
        for round in [
            AnchoredChanges(deletedUUIDs: ["OLD-1"], newAnchor: Data("a1".utf8)),
            AnchoredChanges(lines: [weight(78.0, at: "2026-03-01T07:00:00Z", uuid: "NEW-1")],
                            deletedUUIDs: ["OLD-1"], newAnchor: Data("a1".utf8)),
        ] {
            let anchors = InMemoryAnchorStore()
            anchors.setAnchorData(Data("a0".utf8), for: type)
            let uploader = FakeUploader()
            uploader.failing = [0]
            var report = SyncReport()
            do {
                try await makeSync(FakeChangeSource([round]), uploader, anchors).sync(type, report: &report)
                XCTFail("the upload error must reach the caller")
            } catch is UploadFailed {}
            XCTAssertEqual(anchors.anchorData(for: type), Data("a0".utf8))

            // Next sync: the same round is read from the same anchor and sent again.
            let source = FakeChangeSource([round])
            try await makeSync(source, FakeUploader(), anchors).sync(type, report: &report)
            XCTAssertEqual(source.anchorsAsked, [Data("a0".utf8)])
            XCTAssertEqual(anchors.anchorData(for: type), Data("a1".utf8))
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

    func testLinesForEveryPartOpenEachSplitPart() throws {
        let line: JSONObject = ["quantityType": "HKQuantityTypeIdentifierHeartRate", "value": 60]
        let size = JSONValue.object(line).compactJSONData().count + 1
        var builder = try AppleHealthPartBuilder(batch: "b", maxPartBytes: size * 2)
        builder.addToEveryPart(line: DeletionLine(uuid: "OLD").jsonObject())
        XCTAssertTrue(builder.isEmpty, "lines for every part make no part on their own")
        XCTAssertTrue(builder.build().isEmpty)
        for _ in 0..<3 {
            try builder.add(type: "HKQuantityTypeIdentifierHeartRate", day: "2026-01-10", line: line)
        }
        let parts = builder.build()
        XCTAssertGreaterThan(parts.count, 1)
        for part in parts {
            XCTAssertEqual(try lines(of: part).first, deletion("OLD"))
        }
        XCTAssertEqual(parts.reduce(0) { $0 + $1.lineCount }, 3 + parts.count)
    }
}
