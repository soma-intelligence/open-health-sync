// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import XCTest
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
@testable import OpenHealthSync

final class BluetoothTests: XCTestCase {
    // 2026-10-16T19:04:05.120Z
    let arrived = Date(timeIntervalSince1970: 1_792_177_445.120)
    let paris = TimeZone(secondsFromGMT: 2 * 3600)!

    func testLineMatchesTheUploadContract() throws {
        var r = try BluetoothRecorder()
        let strap = BluetoothDevice(name: "Polar H10", manufacturer: "Polar Electro Oy", model: "H10", firmware: "5.0.0")
        XCTAssertTrue(try r.record(
            characteristic: "00002A37-0000-1000-8000-00805F9B34FB",
            value: Data([0x10, 0x48, 0x00, 0x04, 0x33, 0x03, 0x00, 0x04]),
            session: "9f2c4e1a7b3d5c60", device: strap, at: arrived, timeZone: paris))
        let part = try XCTUnwrap(try r.makePart(batch: "bt1-test"))
        XCTAssertTrue(r.isEmpty)
        XCTAssertEqual(part.day, "2026-10-16")
        XCTAssertEqual(part.batch, "bt1-test")
        XCTAssertEqual(part.partNumber, 0)
        XCTAssertEqual(part.lineCount, 1)
        let ndjson = try TestGunzip.gunzip(part.body)
        XCTAssertEqual(ndjson.last, 0x0a)
        XCTAssertEqual(try JSONValue.parse(ndjson.dropLast()), [
            "at": "2026-10-16T21:04:05.120+02:00",
            "char": "2a37",
            "value": "1048000433030004",
            "session": "9f2c4e1a7b3d5c60",
            "device": ["name": "Polar H10", "manufacturer": "Polar Electro Oy", "model": "H10", "firmware": "5.0.0"],
        ])
    }

    func testDeviceUserAndDayOfTheFirstLine() throws {
        var r = try BluetoothRecorder(deviceUser: 1)
        let late = TimeZone(secondsFromGMT: -5 * 3600)!
        // 2026-10-16 22:00 at UTC-5 is 2026-10-17 in UTC.
        let at = Date(timeIntervalSince1970: 1_792_206_000)
        try r.record(characteristic: "2a35", value: Data([0x00]), session: "abcdefgh", at: at, timeZone: late)
        try r.record(characteristic: "2A9D", value: Data([0x00, 0x10, 0x27]), session: "abcdefgh", at: at.addingTimeInterval(86_400), timeZone: late)
        let part = try XCTUnwrap(try r.makePart(batch: "bt1-x"))
        XCTAssertEqual(part.day, "2026-10-17")
        XCTAssertEqual(part.lineCount, 2)
        let lines = String(decoding: try TestGunzip.gunzip(part.body), as: UTF8.self).split(separator: "\n")
        XCTAssertEqual(try JSONValue.parse(Data(lines[0].utf8)), [
            "at": "2026-10-16T22:00:00.000-05:00", "char": "2a35", "value": "00", "session": "abcdefgh", "device_user": 1,
        ])
        XCTAssertNil(try r.makePart())
    }

    func testValuesThatAreNotUploaded() throws {
        var r = try BluetoothRecorder()
        XCTAssertFalse(try r.record(characteristic: BluetoothGATT.batteryLevel, value: Data([80]), session: "abcdefgh"))
        XCTAssertFalse(try r.record(characteristic: "6a4e2401-667b-11e3-949a-0800200c9a66", value: Data([1]), session: "abcdefgh"))
        XCTAssertTrue(r.isEmpty)
        XCTAssertThrowsError(try r.record(characteristic: "2a37", value: Data(), session: "abcdefgh"))
        XCTAssertThrowsError(try r.record(characteristic: "2a37", value: Data(count: 513), session: "abcdefgh"))
        XCTAssertThrowsError(try r.record(characteristic: "2a37", value: Data([0]), session: "short"))
        XCTAssertThrowsError(try BluetoothRecorder(deviceUser: 255))
        XCTAssertThrowsError(try r.makePart(batch: "bad batch"))
    }

    func testSessionsAndBatches() {
        let s = BluetoothRecorder.newSession()
        XCTAssertEqual(s.count, 16)
        XCTAssertTrue(BluetoothRecorder.isValidSession(s))
        XCTAssertNotEqual(s, BluetoothRecorder.newSession())
        XCTAssertTrue(BluetoothRecorder.newBatchId().hasPrefix("bt1-"))
    }

    func testShortForms() {
        XCTAssertEqual(BluetoothGATT.shortForm("2A37"), "2a37")
        XCTAssertEqual(BluetoothGATT.shortForm("00002a52-0000-1000-8000-00805f9b34fb"), "2a52")
        XCTAssertNil(BluetoothGATT.shortForm("6a4e2401-667b-11e3-949a-0800200c9a66"))
        XCTAssertNil(BluetoothGATT.shortForm("2a3"))
        XCTAssertTrue(BluetoothGATT.isMeasurement("2A5F"))
        XCTAssertFalse(BluetoothGATT.isMeasurement("2A19"))
    }

    func testRecordAccessControlPoint() {
        XCTAssertEqual(BluetoothGATT.reportStoredRecords(after: nil), Data([0x01, 0x01]))
        XCTAssertEqual(BluetoothGATT.reportStoredRecords(after: 41), Data([0x01, 0x03, 0x01, 42, 0]))
        XCTAssertEqual(BluetoothGATT.reportStoredRecords(after: 0x01ff), Data([0x01, 0x03, 0x01, 0x00, 0x02]))
        XCTAssertEqual(BluetoothGATT.reportStoredRecords(after: UInt16.max), Data([0x01, 0x01]))
        XCTAssertEqual(BluetoothGATT.glucoseSequenceNumber(Data([0x02, 0x2a, 0x00, 0xea])), 42)
        XCTAssertNil(BluetoothGATT.glucoseSequenceNumber(Data([0x02, 0x2a])))
    }

    func testCurrentTime() {
        // Friday 2026-10-16 21:04:05.5 in UTC+2.
        let date = Date(timeIntervalSince1970: 1_792_177_445.5)
        XCTAssertEqual(
            BluetoothGATT.currentTimeValue(date, timeZone: paris),
            Data([0xea, 0x07, 10, 16, 21, 4, 5, 5, 128, 0x01]))
    }

    func testNamesAndDeviceText() {
        XCTAssertEqual(BluetoothGATT.publicName("Polar H10 0A1B2C3D"), "Polar H10")
        XCTAssertEqual(BluetoothGATT.publicName("BLEsmart_00000397"), "BLEsmart")
        XCTAssertEqual(BluetoothGATT.publicName("TICKR 1A2B"), "TICKR")
        XCTAssertEqual(BluetoothGATT.publicName("Edge 1040"), "Edge 1040")
        XCTAssertEqual(BluetoothGATT.publicName("HRM-Pro"), "HRM-Pro")
        XCTAssertEqual(BluetoothGATT.deviceText(Data("OMRON HEALTHCARE\0\0".utf8)), "OMRON HEALTHCARE")
        XCTAssertEqual(BluetoothGATT.deviceText(Data("  H10\r\n".utf8)), "H10")
        XCTAssertEqual(BluetoothGATT.deviceText(Data(String(repeating: "é", count: 200).utf8)).count, 128)
    }

    func testUploadBluetoothPart() async throws {
        let transport = FakeTransport([ok(#"{"key":"raw/t/u/bluetooth/2026-10-16/bt1-test_0.ndjson.gz","duplicate":false}"#)])
        var r = try BluetoothRecorder()
        try r.record(characteristic: "2a37", value: Data([0x00, 72]), session: "abcdefgh", at: arrived, timeZone: paris)
        let part = try XCTUnwrap(try r.makePart(batch: "bt1-test"))
        let result = try await makeClient(transport).uploadBluetoothPart(part)
        XCTAssertFalse(result.duplicate)
        let request = transport.requests[0]
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.path, "/v1/ingest/bluetooth")
        XCTAssertEqual(request.value(forHTTPHeaderField: "X-OHS-Type"), "gatt")
        XCTAssertEqual(request.value(forHTTPHeaderField: "X-OHS-Day"), "2026-10-16")
        XCTAssertEqual(request.value(forHTTPHeaderField: "X-OHS-Batch"), "bt1-test")
        XCTAssertEqual(request.value(forHTTPHeaderField: "X-OHS-Part"), "0")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Content-Type"), "application/gzip")
        XCTAssertEqual(request.httpBody, part.body)
    }

    func testPartsSurviveARoundTrip() throws {
        var r = try BluetoothRecorder()
        try r.record(characteristic: "2a37", value: Data([0x00, 72]), session: "abcdefgh", at: arrived)
        let part = try XCTUnwrap(try r.makePart(batch: "bt1-test"))
        let saved = try JSONEncoder().encode(part)
        XCTAssertEqual(try JSONDecoder().decode(BluetoothPart.self, from: saved), part)
    }
}
