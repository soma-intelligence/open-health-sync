// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import XCTest
@testable import OpenHealthSync
@testable import OpenHealthSyncHealthKit

final class SeriesLineTests: XCTestCase {
    let t0 = RFC3339.parse("2026-03-01T07:00:00Z")!

    // The field names are the ones connectors/applehealth reads (route.go, ecg.go,
    // heartbeat.go, audiogram.go, stateofmind.go).
    func testRoutePointLine() {
        let full = RoutePointLine(
            timestamp: t0, latitude: 48.8566, longitude: 2.3522, altitude: 35, speed: 3.2, course: 270)
        XCTAssertEqual(JSONValue.object(full.jsonObject()), [
            "timestamp": "2026-03-01T07:00:00.000Z", "latitude": 48.8566, "longitude": 2.3522,
            "altitude": 35, "speed": 3.2, "course": 270,
        ])
        let bare = RoutePointLine(timestamp: t0, latitude: 1, longitude: 2)
        XCTAssertEqual(Set(bare.jsonObject().keys), ["timestamp", "latitude", "longitude"])
    }

    func testECGLines() {
        let line = ECGLine(
            startDate: t0, endDate: t0.addingTimeInterval(30), classification: 1, symptomsStatus: 1,
            averageHeartRate: 61.5, samplingFrequency: 512, numberOfVoltageMeasurements: 15360,
            provenance: LineProvenance(sourceName: "ECG", uuid: "ECG-1"),
            metadata: ["HKTimeZone": "Europe/Paris"])
        XCTAssertEqual(line.typeIdentifier, "HKDataTypeIdentifierElectrocardiogram")
        XCTAssertEqual(JSONValue.object(line.jsonObject()), [
            "startDate": "2026-03-01T07:00:00.000Z", "endDate": "2026-03-01T07:00:30.000Z",
            "classification": 1, "symptomsStatus": 1, "averageHeartRate": 61.5,
            "samplingFrequency": 512, "numberOfVoltageMeasurements": 15360,
            "sourceName": "ECG", "uuid": "ECG-1", "metadata": ["HKTimeZone": "Europe/Paris"],
        ])
        // No field of another line kind: the server dispatches on them before `classification`.
        for key in ["quantityType", "categoryType", "workoutActivityType", "correlationType", "value"] {
            XCTAssertNil(line.jsonObject()[key])
        }
        XCTAssertEqual(
            JSONValue.object(ECGVoltageLine(timeSinceSampleStart: 0.25, volts: 0.000125).jsonObject()),
            ["timeSinceSampleStart": 0.25, "value": 0.000125])
    }

    func testHeartbeatSeriesLine() {
        let line = HeartbeatSeriesLine(
            startDate: t0, endDate: t0.addingTimeInterval(2),
            beats: [Heartbeat(timeSinceSeriesStart: 0), Heartbeat(timeSinceSeriesStart: 0.81, precededByGap: true)],
            provenance: LineProvenance(uuid: "HB-1"))
        XCTAssertEqual(line.typeIdentifier, "HKDataTypeIdentifierHeartbeatSeries")
        XCTAssertEqual(JSONValue.object(line.jsonObject()), [
            "startDate": "2026-03-01T07:00:00.000Z", "endDate": "2026-03-01T07:00:02.000Z",
            "uuid": "HB-1", "metadata": [:],
            "heartbeats": [
                ["timeSinceSeriesStart": 0, "precededByGap": false],
                ["timeSinceSeriesStart": 0.81, "precededByGap": true],
            ],
        ])
    }

    // A cut repeats the last beat, so every interval of the series is still between two
    // beats of one line, and none is in two lines.
    func testHeartbeatSeriesSplit() {
        let beats = (0..<10).map { Heartbeat(timeSinceSeriesStart: Double($0), precededByGap: $0 == 4) }
        let line = HeartbeatSeriesLine(
            startDate: t0, endDate: t0.addingTimeInterval(9), beats: beats, provenance: LineProvenance(uuid: "HB-2"))
        XCTAssertEqual(line.split(maxBeats: 10), [line])
        let parts = line.split(maxBeats: 4)
        XCTAssertEqual(parts.map { $0.beats.map { Int($0.timeSinceSeriesStart) } }, [[0, 1, 2, 3], [3, 4, 5, 6], [6, 7, 8, 9]])
        XCTAssertTrue(parts.allSatisfy { $0.startDate == t0 && $0.provenance.uuid == "HB-2" })

        func intervals(_ lines: [HeartbeatSeriesLine]) -> [[Int]] {
            var out: [[Int]] = []
            for l in lines {
                for (i, beat) in l.beats.enumerated() where i > 0 && !beat.precededByGap {
                    out.append([Int(l.beats[i - 1].timeSinceSeriesStart), Int(beat.timeSinceSeriesStart)])
                }
            }
            return out
        }
        XCTAssertEqual(intervals(parts), intervals([line]))
        XCTAssertEqual(line.split(maxBeats: 9).map { $0.beats.count }, [9, 2])
    }

    func testAudiogramAndStateOfMindLines() {
        let audiogram = AudiogramLine(
            startDate: t0, endDate: t0,
            sensitivityPoints: [AudiogramPoint(frequency: 500, leftEarSensitivity: 10, rightEarSensitivity: 15),
                                AudiogramPoint(frequency: 1000, leftEarSensitivity: 5)],
            provenance: LineProvenance(uuid: "AU-1"))
        let a = audiogram.jsonObject()
        XCTAssertEqual(a["quantity_type"], "HKDataTypeIdentifierAudiogramSample")
        XCTAssertNil(a["quantityType"], "a quantityType would send the line to the quantity mapper")
        XCTAssertEqual(a["_raw"], ["sensitivityPoints": [
            ["frequency": 500, "leftEarSensitivity": 10, "rightEarSensitivity": 15],
            ["frequency": 1000, "leftEarSensitivity": 5],
        ]])
        XCTAssertEqual(a["uuid"], "AU-1")
        XCTAssertEqual(audiogram.typeIdentifier, "HKDataTypeIdentifierAudiogram")

        let mind = StateOfMindLine(
            startDate: t0, endDate: t0, kind: 2, valence: -0.25, labels: [2, 5], associations: [1],
            provenance: LineProvenance(uuid: "SM-1"))
        let m = mind.jsonObject()
        XCTAssertEqual(m["quantity_type"], "HKStateOfMindType")
        XCTAssertEqual(m["_raw"], ["kind": 2, "valence": -0.25, "labels": [2, 5], "associations": [1]])
        XCTAssertEqual(mind.typeIdentifier, "HKDataTypeStateOfMind")
    }
}

final class AppleHealthTypesTests: XCTestCase {
    func testKinds() {
        XCTAssertEqual(AppleHealthTypes.kind(of: "HKQuantityTypeIdentifierHeartRate"), .quantity)
        XCTAssertEqual(AppleHealthTypes.kind(of: "HKQuantityTypeIdentifierDietaryProtein"), .quantity)
        XCTAssertEqual(AppleHealthTypes.kind(of: "HKCategoryTypeIdentifierSleepAnalysis"), .category)
        XCTAssertEqual(AppleHealthTypes.kind(of: "HKWorkoutTypeIdentifier"), .workout)
        XCTAssertEqual(AppleHealthTypes.kind(of: "HKWorkoutRouteTypeIdentifier"), .workoutRoute)
        XCTAssertEqual(AppleHealthTypes.kind(of: "HKDataTypeIdentifierElectrocardiogram"), .electrocardiogram)
        XCTAssertEqual(AppleHealthTypes.kind(of: "HKDataTypeIdentifierHeartbeatSeries"), .heartbeatSeries)
        XCTAssertEqual(AppleHealthTypes.kind(of: "HKCorrelationTypeIdentifierBloodPressure"), .correlation)
        XCTAssertNil(AppleHealthTypes.kind(of: "HKQuantityTypeIdentifierMadeUp"))
        XCTAssertNil(AppleHealthTypes.kind(of: "HKCategoryTypeIdentifierMadeUp"))
    }

    func testAllIdentifiers() {
        let all = AppleHealthTypes.allIdentifiers
        XCTAssertEqual(all, all.sorted())
        XCTAssertEqual(Set(all).count, all.count)
        XCTAssertTrue(all.allSatisfy { AppleHealthTypes.kind(of: $0) != nil })
        for identifier in AppleHealthUnits.quantityTypeIdentifiers + AppleHealthTypes.categoryIdentifiers
            + AppleHealthTypes.workoutIdentifiers + AppleHealthTypes.heartRhythmIdentifiers
            + [AppleHealthTypes.audiogram, AppleHealthTypes.stateOfMind] {
            XCTAssertTrue(all.contains(identifier), identifier)
        }
        // A correlation's members are synced as quantity samples; both would store food twice.
        for identifier in AppleHealthTypes.correlationIdentifiers {
            XCTAssertFalse(all.contains(identifier), identifier)
        }
    }

    // Compares the table with the server's own, when the test runs inside the repository.
    func testTableMatchesTheServerMapper() throws {
        let root = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
        let file = root.appendingPathComponent("connectors/applehealth/types.go")
        guard let source = try? String(contentsOf: file, encoding: .utf8) else {
            throw XCTSkip("connectors/applehealth/types.go is not next to the SDK")
        }
        func keys(of variable: String, prefix: String) throws -> Set<String> {
            let start = try XCTUnwrap(source.range(of: "var \(variable) = map[string]string{"))
            let end = try XCTUnwrap(source.range(of: "\n}", range: start.upperBound..<source.endIndex))
            var out = Set<String>()
            for line in source[start.upperBound..<end.lowerBound].split(separator: "\n") {
                let parts = line.split(separator: "\"")
                if parts.count >= 2, parts[1].hasPrefix(prefix) {
                    out.insert(String(parts[1]))
                }
            }
            return out
        }
        XCTAssertEqual(
            try keys(of: "categoryTypeCanonical", prefix: "HKCategoryTypeIdentifier"),
            Set(AppleHealthTypes.categoryIdentifiers))
        XCTAssertEqual(
            try keys(of: "quantityTypeCanonical", prefix: "HKQuantityTypeIdentifier"),
            Set(AppleHealthTypes.quantityIdentifiers))
    }
}

/// A reader that fails after its batches.
final class FailingReader: SeriesLineReader, @unchecked Sendable {
    var batches: [[JSONObject]]
    var cancelled = false

    init(_ batches: [[JSONObject]]) {
        self.batches = batches
    }

    func next() async throws -> [JSONObject]? {
        if batches.isEmpty {
            throw UploadFailed()
        }
        return batches.removeFirst()
    }

    func cancel() {
        cancelled = true
    }
}

final class SeriesSyncTests: XCTestCase {
    let route = AppleHealthTypes.workoutRoute
    let ecg = AppleHealthTypes.electrocardiogram
    let paris = TimeZone(identifier: "Europe/Paris")!
    let t0 = RFC3339.parse("2026-03-01T23:30:00Z")!

    func point(_ i: Int) -> JSONObject {
        return RoutePointLine(
            timestamp: t0.addingTimeInterval(Double(i)), latitude: 48.001 + Double(i) / 1000, longitude: 2.5).jsonObject()
    }

    var workout: JSONObject {
        return WorkoutLine(
            workoutActivityType: 37, startDate: t0, endDate: t0.addingTimeInterval(600), durationS: 600,
            provenance: LineProvenance(uuid: "W-1")).jsonObject()
    }

    func lines(of part: AppleHealthPart) throws -> [JSONValue] {
        let text = String(decoding: try TestGunzip.gunzip(part.body), as: UTF8.self)
        return try text.split(separator: "\n").map { try JSONValue.parse(Data($0.utf8)) }
    }

    func makeSync(
        _ source: FakeChangeSource, _ uploader: FakeUploader, _ anchors: AnchorStore, partBytes: Int
    ) -> AnchoredSync {
        return AnchoredSync(
            source: source, uploader: uploader, anchors: anchors, queryLimit: 10,
            seriesPartBytes: partBytes, timeZone: paris, now: { RFC3339.parse("2026-03-04T23:30:00Z")! })
    }

    // A long route goes out in several parts of one batch; each starts with the workout line,
    // so the server finds the one parent of the points in every part.
    func testLongRouteIsChunkedAndEveryPartCarriesTheWorkout() async throws {
        let points = (0..<10).map { point($0) }
        let lineBytes = points.map { JSONValue.object($0).compactJSONData().count + 1 }.max()!
        let headerBytes = JSONValue.object(workout).compactJSONData().count + 1
        let reader = ArraySeriesLineReader(batches: [Array(points[0..<4]), Array(points[4..<10])])
        let source = FakeChangeSource([AnchoredChanges(
            series: [SeriesUpload(startDate: t0, timeZoneIdentifier: "Europe/Paris", header: workout, reader: reader)],
            newAnchor: Data("a1".utf8))])
        let uploader = FakeUploader()
        let anchors = InMemoryAnchorStore()
        var report = SyncReport()
        // Room for the header and three points.
        try await makeSync(source, uploader, anchors, partBytes: headerBytes + 3 * lineBytes)
            .sync(route, report: &report)

        let parts = uploader.calls.flatMap { $0 }
        XCTAssertEqual(uploader.calls.map { $0.count }, [1, 1, 1, 1], "one part in memory at a time")
        XCTAssertEqual(parts.map { $0.partNumber }, [0, 1, 2, 3])
        XCTAssertEqual(Set(parts.map { $0.batch }).count, 1)
        XCTAssertTrue(parts.allSatisfy { $0.type == route && $0.day == "2026-03-02" }, "the local day of the start")
        var seen: [JSONValue] = []
        for part in parts {
            let decoded = try lines(of: part)
            XCTAssertEqual(decoded.first, .object(workout))
            XCTAssertLessThanOrEqual(part.uncompressedBytes, headerBytes + 3 * lineBytes)
            XCTAssertEqual(part.lineCount, decoded.count)
            seen.append(contentsOf: decoded.dropFirst())
        }
        XCTAssertEqual(seen, points.map { JSONValue.object($0) }, "every point once, in order")
        XCTAssertEqual(report.linesByType, [route: 10], "the workout is counted by its own type")
        XCTAssertEqual(report.partsUploaded, 4)
        XCTAssertEqual(report.batchIds.count, 1)
        XCTAssertEqual(anchors.anchorData(for: route), Data("a1".utf8))
        XCTAssertTrue(reader.isCancelled, "the reader is released once the series is uploaded")
    }

    // An ECG is its recording line then its voltages, in one part when it fits.
    func testECGRecordingAndVoltagesShareAPart() async throws {
        let recording = ECGLine(
            startDate: t0, endDate: t0.addingTimeInterval(30), classification: 1,
            numberOfVoltageMeasurements: 3, provenance: LineProvenance(uuid: "ECG-1")).jsonObject()
        let voltages = (0..<3).map { ECGVoltageLine(timeSinceSampleStart: Double($0) / 512, volts: 0.0001).jsonObject() }
        let source = FakeChangeSource([AnchoredChanges(
            series: [
                SeriesUpload(startDate: t0, header: recording, headerIsOwnRecord: true,
                             reader: ArraySeriesLineReader(batches: [voltages])),
                // A recording whose voltages cannot be read still goes up, alone.
                SeriesUpload(startDate: t0, header: recording, headerIsOwnRecord: true,
                             reader: ArraySeriesLineReader(batches: [])),
            ],
            deletedUUIDs: ["ECG-0"], newAnchor: Data("a1".utf8))])
        let uploader = FakeUploader()
        let anchors = InMemoryAnchorStore()
        var report = SyncReport()
        try await makeSync(source, uploader, anchors, partBytes: 1 << 20).sync(ecg, report: &report)

        XCTAssertEqual(uploader.calls.count, 3, "two recordings, then the deletion part")
        XCTAssertEqual(try lines(of: uploader.calls[0][0]), [.object(recording)] + voltages.map { JSONValue.object($0) })
        XCTAssertEqual(try lines(of: uploader.calls[1][0]), [.object(recording)])
        XCTAssertNotEqual(uploader.calls[0][0].batch, uploader.calls[1][0].batch, "one batch per series")
        XCTAssertEqual(try lines(of: uploader.calls[2][0]), [.object(["deletedUuid": "ECG-0"])])
        XCTAssertEqual(report.linesByType, [ecg: 5])
        XCTAssertEqual(report.deletionsByType, [ecg: 1])
    }

    // A route without a point sends nothing: its workout is the workout type's to upload.
    func testEmptyRouteSendsNothing() async throws {
        let source = FakeChangeSource([AnchoredChanges(
            series: [SeriesUpload(startDate: t0, header: workout, reader: ArraySeriesLineReader(batches: []))],
            newAnchor: Data("a1".utf8))])
        let uploader = FakeUploader()
        let anchors = InMemoryAnchorStore()
        var report = SyncReport()
        try await makeSync(source, uploader, anchors, partBytes: 1 << 20).sync(route, report: &report)
        XCTAssertTrue(uploader.calls.isEmpty)
        XCTAssertTrue(report.batchIds.isEmpty)
        XCTAssertEqual(anchors.anchorData(for: route), Data("a1".utf8))
    }

    // An upload or a read that fails in the middle of a series leaves the anchor where it
    // was: the next sync reads the same series again, and the server stores each line once.
    func testInterruptedSeriesKeepsTheAnchorAndResumes() async throws {
        let points = (0..<4).map { point($0) }
        func round() -> AnchoredChanges {
            return AnchoredChanges(
                series: [SeriesUpload(startDate: t0, header: workout,
                                      reader: ArraySeriesLineReader(batches: [points]))],
                newAnchor: Data("a2".utf8))
        }
        let lineBytes = points.map { JSONValue.object($0).compactJSONData().count + 1 }.max()!
        let headerBytes = JSONValue.object(workout).compactJSONData().count + 1
        let source = FakeChangeSource([round(), round()])
        let uploader = FakeUploader()
        uploader.failing = [1]
        let anchors = InMemoryAnchorStore()
        anchors.setAnchorData(Data("a1".utf8), for: route)
        var report = SyncReport()
        let sync = makeSync(source, uploader, anchors, partBytes: headerBytes + 2 * lineBytes)
        do {
            try await sync.sync(route, report: &report)
            XCTFail("the second part fails")
        } catch is UploadFailed {
        }
        XCTAssertEqual(anchors.anchorData(for: route), Data("a1".utf8))

        try await sync.sync(route, report: &report)
        XCTAssertEqual(source.anchorsAsked, [Data("a1".utf8), Data("a1".utf8)])
        XCTAssertEqual(uploader.calls.count, 4, "both parts are sent again")
        XCTAssertEqual(anchors.anchorData(for: route), Data("a2".utf8))

        let failing = FailingReader([points])
        let source2 = FakeChangeSource([AnchoredChanges(
            series: [SeriesUpload(startDate: t0, header: workout, reader: failing)], newAnchor: Data("a3".utf8))])
        do {
            try await makeSync(source2, FakeUploader(), anchors, partBytes: 1 << 20).sync(route, report: &report)
            XCTFail("the read fails")
        } catch is UploadFailed {
        }
        XCTAssertEqual(anchors.anchorData(for: route), Data("a2".utf8))
        XCTAssertTrue(failing.cancelled)
    }
}

final class SeriesLinePipeTests: XCTestCase {
    func batch(_ i: Int) -> [JSONObject] {
        return [["i": .number(Double(i))]]
    }

    // The producer is held back while `capacity` batches wait, and is only started when the
    // reader first asks.
    func testBackpressureAndOrder() async throws {
        let produced = TestCounter()
        let pipe = SeriesLinePipe(capacity: 2) { pipe in
            Thread.detachNewThread {
                for i in 0..<50 {
                    guard pipe.send(self.batch(i)) else { return }
                    produced.increment()
                }
                pipe.finish()
            }
        }
        try await Task.sleep(nanoseconds: 20_000_000)
        XCTAssertEqual(produced.value, 0, "nothing is read before the first next()")

        var seen: [Int] = []
        let first = try await pipe.next()
        seen.append(first?[0]["i"]?.intValue ?? -1)
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertLessThanOrEqual(produced.value, 4, "the producer waits for the reader")
        while let lines = try await pipe.next() {
            seen.append(lines[0]["i"]?.intValue ?? -1)
        }
        XCTAssertEqual(seen, Array(0..<50))
    }

    func testFailureReachesTheReaderAfterTheWaitingBatches() async throws {
        let pipe = SeriesLinePipe(capacity: 4) { pipe in
            pipe.send(self.batch(1))
            pipe.finish(throwing: UploadFailed())
        }
        let first = try await pipe.next()
        XCTAssertEqual(first?.count, 1)
        do {
            _ = try await pipe.next()
            XCTFail("the failure must be thrown")
        } catch is UploadFailed {
        }
    }

    func testCancelReleasesABlockedProducer() async throws {
        let stopped = TestCounter()
        let done = expectation(description: "producer returned")
        let pipe = SeriesLinePipe(capacity: 1) { pipe in
            Thread.detachNewThread {
                var i = 0
                while pipe.send(self.batch(i)) {
                    i += 1
                }
                done.fulfill()
            }
        }
        pipe.setOnCancel { stopped.increment() }
        _ = try await pipe.next()
        pipe.cancel()
        await fulfillment(of: [done], timeout: 5)
        XCTAssertEqual(stopped.value, 1)
        let after = try await pipe.next()
        XCTAssertNil(after)
    }
}

final class TestCounter: @unchecked Sendable {
    private let lock = NSLock()
    private var count = 0

    var value: Int {
        lock.lock()
        defer { lock.unlock() }
        return count
    }

    func increment() {
        lock.lock()
        count += 1
        lock.unlock()
    }
}

#if canImport(HealthKit)
import CoreLocation
import HealthKit

@available(macOS 13.0, *)
final class HealthKitTypeTableTests: XCTestCase {
    // Every identifier of the table is spelled the way HealthKit spells it.
    func testIdentifiersAreHealthKitIdentifiers() throws {
        var missing: [String] = []
        for identifier in AppleHealthTypes.allIdentifiers + AppleHealthTypes.correlationIdentifiers {
            guard let type = HealthKitSync.sampleType(forIdentifier: identifier) else {
                missing.append(identifier)
                continue
            }
            XCTAssertEqual(type.identifier, identifier)
        }
        // A type this OS version does not have is left out; none of the special ones may be.
        for identifier in AppleHealthTypes.workoutIdentifiers + AppleHealthTypes.heartRhythmIdentifiers
            + AppleHealthTypes.correlationIdentifiers + [AppleHealthTypes.audiogram, AppleHealthTypes.sleepAnalysis] {
            XCTAssertFalse(missing.contains(identifier), identifier)
        }
        if #available(macOS 15.0, iOS 18.0, watchOS 11.0, *) {
            // The server lists one name HealthKit does not use.
            XCTAssertEqual(missing, ["HKCategoryTypeIdentifierEnvironmentalAudioExposureEvent"])
        }
        XCTAssertNil(HealthKitSync.sampleType(forIdentifier: "HKQuantityTypeIdentifierMadeUp"))
    }

    func testAllSupportedTypes() {
        let all = HealthKitSync.allSupportedTypes
        XCTAssertTrue(all.contains(HKSeriesType.workoutRoute()))
        XCTAssertTrue(all.contains(HKObjectType.workoutType()))
        XCTAssertTrue(all.contains(HKObjectType.electrocardiogramType()))
        XCTAssertTrue(all.contains(HKSeriesType.heartbeat()))
        XCTAssertTrue(all.contains(HKObjectType.audiogramSampleType()))
        XCTAssertFalse(all.contains { $0 is HKCorrelationType }, "HealthKit refuses to authorize a correlation type")
        XCTAssertTrue(all.allSatisfy { AppleHealthTypes.kind(of: $0.identifier) != nil })
        let groups = HealthKitSync.quantityTypes.union(HealthKitSync.nutritionTypes)
            .union(HealthKitSync.categoryTypes).union(HealthKitSync.workoutTypes)
            .union(HealthKitSync.heartRhythmTypes)
        XCTAssertTrue(groups.isSubset(of: all))
        XCTAssertEqual(HealthKitSync.heartRhythmTypes.count, 2)
    }

    func testAuthorizationReplacesCorrelationsByTheirMembers() throws {
        let pressure = try XCTUnwrap(HKObjectType.correlationType(forIdentifier: .bloodPressure))
        let food = try XCTUnwrap(HKObjectType.correlationType(forIdentifier: .food))
        let read = HealthKitSync.authorizationTypes(for: [pressure, HKObjectType.workoutType()])
        XCTAssertEqual(Set(read.map { $0.identifier }), [
            "HKQuantityTypeIdentifierBloodPressureSystolic", "HKQuantityTypeIdentifierBloodPressureDiastolic",
            "HKWorkoutTypeIdentifier",
        ])
        let foodRead = HealthKitSync.authorizationTypes(for: [food])
        XCTAssertEqual(Set(foodRead.map { $0.identifier }), Set(AppleHealthTypes.nutritionIdentifiers))
    }

    // HealthKit throws NSInvalidArgumentException when a series type is asked without its
    // parent: "Authorization for HKQuantityTypeIdentifierHeartRateVariabilitySDNN should
    // also be requested when requesting authorization to read HKDataTypeIdentifierHeartbeatSeries".
    func testAuthorizationAddsTheParentOfASeriesType() {
        let read = HealthKitSync.authorizationTypes(for: [HKSeriesType.heartbeat(), HKSeriesType.workoutRoute()])
        XCTAssertEqual(Set(read.map { $0.identifier }), [
            "HKDataTypeIdentifierHeartbeatSeries", "HKQuantityTypeIdentifierHeartRateVariabilitySDNN",
            "HKWorkoutRouteTypeIdentifier", "HKWorkoutTypeIdentifier",
        ])
    }

    func testRoutePointLeavesOutInvalidValues() {
        let at = Date(timeIntervalSince1970: 1_772_348_400)
        let good = CLLocation(
            coordinate: CLLocationCoordinate2D(latitude: 48.85, longitude: 2.35), altitude: 35,
            horizontalAccuracy: 5, verticalAccuracy: 3, course: 90, speed: 2.5, timestamp: at)
        XCTAssertEqual(
            HealthKitLines.routePointLine(for: good),
            RoutePointLine(timestamp: at, latitude: 48.85, longitude: 2.35, altitude: 35, speed: 2.5, course: 90))
        let poor = CLLocation(
            coordinate: CLLocationCoordinate2D(latitude: 48.85, longitude: 2.35), altitude: 0,
            horizontalAccuracy: 5, verticalAccuracy: -1, course: -1, speed: -1, timestamp: at)
        XCTAssertEqual(
            HealthKitLines.routePointLine(for: poor),
            RoutePointLine(timestamp: at, latitude: 48.85, longitude: 2.35))
    }

    func testSeriesKinds() {
        XCTAssertTrue(HealthKitChangeSource.readsSeries(HKSeriesType.workoutRoute()))
        XCTAssertTrue(HealthKitChangeSource.readsSeries(HKObjectType.electrocardiogramType()))
        XCTAssertTrue(HealthKitChangeSource.readsSeries(HKSeriesType.heartbeat()))
        XCTAssertFalse(HealthKitChangeSource.readsSeries(HKObjectType.workoutType()))
    }
}
#endif
