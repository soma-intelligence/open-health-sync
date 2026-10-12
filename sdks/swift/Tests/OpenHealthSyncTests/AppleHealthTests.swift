// SPDX-License-Identifier: MIT

import Foundation
import XCTest
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
@testable import OpenHealthSync

final class PartBuilderTests: XCTestCase {
    func line(_ i: Int) -> JSONObject {
        return [
            "quantityType": "HKQuantityTypeIdentifierHeartRate",
            "startDate": "2026-01-10T14:30:00.000Z",
            "value": .number(Double(60 + i)),
            "unit": "count/min",
        ]
    }

    func lines(of part: AppleHealthPart) throws -> [JSONValue] {
        let text = String(decoding: try TestGunzip.gunzip(part.body), as: UTF8.self)
        XCTAssertTrue(text.hasSuffix("\n"))
        return try text.split(separator: "\n").map { try JSONValue.parse(Data($0.utf8)) }
    }

    func testGroupsByTypeAndDayInFirstSeenOrder() throws {
        var builder = try AppleHealthPartBuilder(batch: "b-1")
        try builder.add(type: "HKQuantityTypeIdentifierHeartRate", day: "2026-01-10", line: line(0))
        try builder.add(type: "HKQuantityTypeIdentifierStepCount", day: "2026-01-10", line: ["value": 1])
        try builder.add(type: "HKQuantityTypeIdentifierHeartRate", day: "2026-01-11", line: line(1))
        try builder.add(type: "HKQuantityTypeIdentifierHeartRate", day: "2026-01-10", line: line(2))
        let parts = builder.build()
        XCTAssertEqual(parts.map { $0.type }, [
            "HKQuantityTypeIdentifierHeartRate", "HKQuantityTypeIdentifierStepCount", "HKQuantityTypeIdentifierHeartRate",
        ])
        XCTAssertEqual(parts.map { $0.day }, ["2026-01-10", "2026-01-10", "2026-01-11"])
        XCTAssertEqual(parts.map { $0.partNumber }, [0, 1, 2])
        XCTAssertEqual(parts.map { $0.batch }, ["b-1", "b-1", "b-1"])
        XCTAssertEqual(parts.map { $0.lineCount }, [2, 1, 1])
        let first = try lines(of: parts[0])
        XCTAssertEqual(first, [.object(line(0)), .object(line(2))])
    }

    func testSplitsAtMaxPartBytes() throws {
        let size = JSONValue.object(line(0)).compactJSONData().count + 1
        var builder = try AppleHealthPartBuilder(batch: "split", maxPartBytes: size * 2)
        for i in 0..<5 {
            try builder.add(type: "HKQuantityTypeIdentifierHeartRate", day: "2026-01-10", line: line(i % 10))
        }
        try builder.add(type: "HKQuantityTypeIdentifierStepCount", day: "2026-01-10", line: ["value": 1])
        let parts = builder.build()
        XCTAssertEqual(parts.map { $0.lineCount }, [2, 2, 1, 1])
        XCTAssertEqual(parts.map { $0.partNumber }, [0, 1, 2, 3])
        for part in parts {
            XCTAssertLessThanOrEqual(part.uncompressedBytes, size * 2)
        }
        var all: [JSONValue] = []
        for part in parts.prefix(3) {
            all.append(contentsOf: try lines(of: part))
        }
        XCTAssertEqual(all, (0..<5).map { JSONValue.object(line($0)) })
    }

    func testOversizedLineGoesAlone() throws {
        var builder = try AppleHealthPartBuilder(batch: "big", maxPartBytes: 10)
        try builder.add(type: "T", day: "2026-01-10", line: line(0))
        try builder.add(type: "T", day: "2026-01-10", line: line(1))
        let parts = builder.build()
        XCTAssertEqual(parts.count, 2)
        XCTAssertEqual(parts.map { $0.lineCount }, [1, 1])
    }

    func testValidation() throws {
        XCTAssertThrowsError(try AppleHealthPartBuilder(batch: ""))
        XCTAssertThrowsError(try AppleHealthPartBuilder(batch: "has space"))
        XCTAssertThrowsError(try AppleHealthPartBuilder(batch: String(repeating: "a", count: 129)))
        var builder = try AppleHealthPartBuilder(batch: "ok_batch-1")
        XCTAssertThrowsError(try builder.add(type: "bad/type", day: "2026-01-10", line: [:]))
        XCTAssertThrowsError(try builder.add(type: "T", day: "2026-1-10", line: [:]))
        XCTAssertThrowsError(try builder.add(type: "T", day: "2026-02-30", line: [:]))
        XCTAssertTrue(builder.isEmpty)
    }

    func testNewBatchId() {
        let a = AppleHealthPartBuilder.newBatchId(prefix: "my app!")
        let b = AppleHealthPartBuilder.newBatchId(prefix: "my app!")
        XCTAssertNotEqual(a, b)
        XCTAssertTrue(a.hasPrefix("myapp-"))
        XCTAssertTrue(AppleHealthPartBuilder.isValidToken(a))
    }

    func testTypedLineUsesItsTimeZoneForTheDay() throws {
        let start = try XCTUnwrap(RFC3339.parse("2026-01-10T23:30:00Z"))
        var builder = try AppleHealthPartBuilder(batch: "tz")
        try builder.add(QuantityLine(
            quantityType: "HKQuantityTypeIdentifierStepCount", startDate: start, endDate: start,
            value: 10, unit: "count", metadata: ["HKTimeZone": "Europe/Paris"]))
        try builder.add(
            QuantityLine(quantityType: "HKQuantityTypeIdentifierStepCount", startDate: start, endDate: start, value: 11, unit: "count"),
            defaultTimeZone: try XCTUnwrap(TimeZone(identifier: "UTC")))
        XCTAssertEqual(builder.build().map { $0.day }, ["2026-01-11", "2026-01-10"])
    }
}

/// Keeps the SDK's line encoding aligned with the lines the server's connector is tested
/// against (connectors/applehealth/testdata/ios_upload_lines.ndjson).
final class LineContractTests: XCTestCase {
    static let fallbackLines = [
        #"{"device":"Apple Watch","deviceModel":"Watch","endDate":"2026-01-10T14:30:00.000Z","metadata":{"HKTimeZone":"Europe/Paris"},"productType":"Watch7,1","quantityType":"HKQuantityTypeIdentifierHeartRate","sourceBundleId":"com.apple.health.ABC","sourceName":"Apple Watch","startDate":"2026-01-10T14:30:00.000Z","unit":"count/min","value":72.5,"wasUserEntered":false}"#,
        #"{"device":"Apple Watch","deviceModel":"Watch","endDate":"2026-01-10T14:30:00.000Z","metadata":{"HKTimeZone":"Europe/Paris"},"productType":"Watch7,1","quantityType":"HKQuantityTypeIdentifierOxygenSaturation","sourceBundleId":"com.apple.health.ABC","sourceName":"Apple Watch","startDate":"2026-01-10T14:30:00.000Z","unit":"%","value":0.97,"wasUserEntered":false}"#,
        #"{"device":"Apple Watch","deviceModel":"Watch","endDate":"2026-01-10T14:31:00.000Z","metadata":{"HKTimeZone":"Europe/Paris"},"productType":"Watch7,1","quantityType":"HKQuantityTypeIdentifierStepCount","sourceBundleId":"com.apple.health.ABC","sourceName":"Apple Watch","startDate":"2026-01-10T14:30:00.000Z","unit":"count","value":120,"wasUserEntered":false}"#,
    ]

    /// The server's fixture when the repository is around the package, else a copy of it.
    func fixtureLines() throws -> [JSONValue] {
        var root = URL(fileURLWithPath: #filePath)
        for _ in 0..<5 {
            root = root.deletingLastPathComponent()
        }
        let file = root.appendingPathComponent("connectors/applehealth/testdata/ios_upload_lines.ndjson")
        let texts: [String]
        if let data = try? Data(contentsOf: file) {
            texts = String(decoding: data, as: UTF8.self).split(separator: "\n").map(String.init)
        } else {
            texts = LineContractTests.fallbackLines
        }
        return try texts.map { try JSONValue.parse(Data($0.utf8)) }
    }

    let watch = LineProvenance(
        sourceName: "Apple Watch", sourceBundleId: "com.apple.health.ABC", device: "Apple Watch",
        deviceModel: "Watch", productType: "Watch7,1", wasUserEntered: false)
    let paris: JSONObject = ["HKTimeZone": "Europe/Paris"]

    func time(_ s: String) -> Date {
        return RFC3339.parse(s)!
    }

    func assertSameObject(_ got: JSONObject, _ want: JSONValue, line: UInt = #line) {
        guard let wantObject = want.objectValue else {
            XCTFail("fixture line is not an object", line: line)
            return
        }
        let keys = Set(got.keys).union(wantObject.keys)
        for key in keys.sorted() {
            XCTAssertEqual(got[key], wantObject[key], "field \(key)", line: line)
        }
    }

    func testQuantityLinesMatchTheServerFixture() throws {
        let fixture = try fixtureLines()
        XCTAssertGreaterThanOrEqual(fixture.count, 3)
        let t0 = time("2026-01-10T14:30:00Z")

        let hrUnit = try XCTUnwrap(AppleHealthUnits.wireUnit(forQuantityType: "HKQuantityTypeIdentifierHeartRate"))
        let spo2Unit = try XCTUnwrap(AppleHealthUnits.wireUnit(forQuantityType: "HKQuantityTypeIdentifierOxygenSaturation"))
        let stepUnit = try XCTUnwrap(AppleHealthUnits.wireUnit(forQuantityType: "HKQuantityTypeIdentifierStepCount"))

        let heartRate = QuantityLine(
            quantityType: "HKQuantityTypeIdentifierHeartRate", startDate: t0, endDate: t0,
            value: 72.5, unit: hrUnit, provenance: watch, metadata: paris)
        let oxygen = QuantityLine(
            quantityType: "HKQuantityTypeIdentifierOxygenSaturation", startDate: t0, endDate: t0,
            value: 0.97, unit: spo2Unit, provenance: watch, metadata: paris)
        let steps = QuantityLine(
            quantityType: "HKQuantityTypeIdentifierStepCount", startDate: t0,
            endDate: time("2026-01-10T14:31:00Z"), value: 120, unit: stepUnit,
            provenance: watch, metadata: paris)

        assertSameObject(heartRate.jsonObject(), fixture[0])
        assertSameObject(oxygen.jsonObject(), fixture[1])
        assertSameObject(steps.jsonObject(), fixture[2])
    }

    func testOtherLineKindsMatchTheServerFixture() throws {
        let fixture = try fixtureLines()
        guard fixture.count >= 7 else {
            throw XCTSkip("the server fixture is not next to the package")
        }
        let sleep = CategoryLine(
            categoryType: "HKCategoryTypeIdentifierSleepAnalysis",
            startDate: time("2026-01-10T14:30:00Z"), endDate: time("2026-01-10T22:30:00Z"),
            value: 0, provenance: watch, metadata: paris)
        let deep = CategoryLine(
            categoryType: "HKCategoryTypeIdentifierSleepAnalysis",
            startDate: time("2026-01-10T15:30:00Z"), endDate: time("2026-01-10T16:00:00Z"),
            value: 4, provenance: watch, metadata: paris)
        let walk = WorkoutLine(
            workoutActivityType: 52, startDate: time("2026-01-10T14:30:00Z"),
            endDate: time("2026-01-10T15:00:00Z"), durationS: 1800, totalDistance: 2000.5,
            totalEnergyBurned: 200, averageHeartRate: 120, minHeartRate: 90, maxHeartRate: 150,
            provenance: watch, metadata: paris)
        let t0 = time("2026-01-10T14:30:00Z")
        let pressure = CorrelationLine(
            correlationType: AppleHealthUnits.bloodPressureCorrelationIdentifier, startDate: t0, endDate: t0,
            objects: [
                CorrelationObject(sampleType: "HKQuantityTypeIdentifierBloodPressureSystolic", startDate: t0, endDate: t0, value: 110, unit: "mmHg"),
                CorrelationObject(sampleType: "HKQuantityTypeIdentifierBloodPressureDiastolic", startDate: t0, endDate: t0, value: 70, unit: "mmHg"),
            ],
            provenance: watch, metadata: paris)

        assertSameObject(sleep.jsonObject(), fixture[3])
        assertSameObject(deep.jsonObject(), fixture[4])
        assertSameObject(walk.jsonObject(), fixture[5])
        assertSameObject(pressure.jsonObject(), fixture[6])
        XCTAssertEqual(walk.typeIdentifier, "HKWorkoutTypeIdentifier")
    }

    func testWireUnits() {
        XCTAssertEqual(AppleHealthUnits.wireUnit(forQuantityType: "HKQuantityTypeIdentifierBodyMass"), "kg")
        XCTAssertEqual(AppleHealthUnits.wireUnit(forQuantityType: "HKQuantityTypeIdentifierBodyTemperature"), "degC")
        XCTAssertEqual(AppleHealthUnits.wireUnit(forQuantityType: "HKQuantityTypeIdentifierDietaryProtein"), "g")
        XCTAssertNil(AppleHealthUnits.wireUnit(forQuantityType: "HKQuantityTypeIdentifierNotAType"))
        XCTAssertNil(AppleHealthUnits.wireUnit(forQuantityType: "HKQuantityTypeIdentifierDietaryNotANutrient"))
    }
}
