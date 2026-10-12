// SPDX-License-Identifier: MIT

import Foundation
import XCTest
@testable import OpenHealthSync

final class RFC3339Tests: XCTestCase {
    func testParsesEveryPrecision() throws {
        let base = 1_768_055_400.0  // 2026-01-10T14:30:00Z
        XCTAssertEqual(RFC3339.parse("2026-01-10T14:30:00Z")?.timeIntervalSince1970, base)
        XCTAssertEqual(try XCTUnwrap(RFC3339.parse("2026-01-10T14:30:00.5Z")).timeIntervalSince1970, base + 0.5, accuracy: 1e-6)
        XCTAssertEqual(try XCTUnwrap(RFC3339.parse("2026-01-10T14:30:00.123Z")).timeIntervalSince1970, base + 0.123, accuracy: 1e-6)
        XCTAssertEqual(try XCTUnwrap(RFC3339.parse("2026-01-10T14:30:00.123456Z")).timeIntervalSince1970, base + 0.123456, accuracy: 1e-6)
        XCTAssertEqual(try XCTUnwrap(RFC3339.parse("2026-01-10T14:30:00.123456789Z")).timeIntervalSince1970, base + 0.123456789, accuracy: 1e-6)
        XCTAssertEqual(RFC3339.parse("2026-01-10T15:30:00+01:00")?.timeIntervalSince1970, base)
        XCTAssertEqual(RFC3339.parse("2026-01-10T09:30:00-05:00")?.timeIntervalSince1970, base)
        XCTAssertEqual(RFC3339.parse("1970-01-01T00:00:00Z")?.timeIntervalSince1970, 0)
    }

    func testRejectsMalformedTimes() {
        XCTAssertNil(RFC3339.parse("2026-01-10"))
        XCTAssertNil(RFC3339.parse("2026-01-10T14:30:00"))
        XCTAssertNil(RFC3339.parse("2026-13-10T14:30:00Z"))
        XCTAssertNil(RFC3339.parse("2026-02-30T14:30:00Z"))
        XCTAssertNil(RFC3339.parse("2026-01-10T14:30:00.Z"))
        XCTAssertNil(RFC3339.parse("2026-01-10T14:30:00Zjunk"))
    }

    func testFormatsWithMilliseconds() {
        XCTAssertEqual(RFC3339.format(Date(timeIntervalSince1970: 1_768_055_400)), "2026-01-10T14:30:00.000Z")
        XCTAssertEqual(RFC3339.format(Date(timeIntervalSince1970: 1_768_055_400.25)), "2026-01-10T14:30:00.250Z")
        XCTAssertEqual(RFC3339.format(Date(timeIntervalSince1970: 951_782_400)), "2000-02-29T00:00:00.000Z")
        XCTAssertEqual(RFC3339.format(Date(timeIntervalSince1970: -1)), "1969-12-31T23:59:59.000Z")
    }

    func testLocalDay() throws {
        let instant = try XCTUnwrap(RFC3339.parse("2026-01-10T23:30:00Z"))
        XCTAssertEqual(AppleHealthPartBuilder.localDay(instant, timeZone: try XCTUnwrap(TimeZone(identifier: "UTC"))), "2026-01-10")
        XCTAssertEqual(AppleHealthPartBuilder.localDay(instant, timeZone: try XCTUnwrap(TimeZone(identifier: "Europe/Paris"))), "2026-01-11")
        XCTAssertEqual(AppleHealthPartBuilder.localDay(instant, timeZone: try XCTUnwrap(TimeZone(identifier: "America/New_York"))), "2026-01-10")
    }

    func testDayValidation() {
        XCTAssertTrue(RFC3339.isValidDay("2026-01-10"))
        XCTAssertTrue(RFC3339.isValidDay("2024-02-29"))
        XCTAssertFalse(RFC3339.isValidDay("2026-02-29"))
        XCTAssertFalse(RFC3339.isValidDay("2026-1-10"))
        XCTAssertFalse(RFC3339.isValidDay("20260110"))
    }
}

final class JSONValueTests: XCTestCase {
    func testCompactSortedOutput() {
        let value: JSONValue = ["b": 120, "a": 72.5, "c": ["x", true, nil], "d": "q\"\n"]
        XCTAssertEqual(value.compactJSONString(), #"{"a":72.5,"b":120,"c":["x",true,null],"d":"q\"\n"}"#)
    }

    func testRoundTrip() throws {
        let text = #"{"n":0.97,"i":-3,"s":"Europe/Paris","b":false,"z":null,"o":{"k":[1,2]}}"#
        let value = try JSONValue.parse(Data(text.utf8))
        XCTAssertEqual(value["n"], .number(0.97))
        XCTAssertEqual(value["b"], .bool(false))
        XCTAssertEqual(value["z"], .null)
        XCTAssertEqual(value["o"]?["k"], [1, 2])
        XCTAssertEqual(try JSONValue.parse(value.compactJSONData()), value)
    }
}

final class GzipTests: XCTestCase {
    func testCRC32() {
        XCTAssertEqual(Gzip.crc32(Data("123456789".utf8)), 0xcbf4_3926)
        XCTAssertEqual(Gzip.crc32(Data("hello".utf8)), 0x3610_a686)
        XCTAssertEqual(Gzip.crc32(Data()), 0)
    }

    func testStoredBlockVector() {
        XCTAssertEqual([UInt8](Gzip.deflateStored(Data("hello".utf8))), [0x01, 0x05, 0x00, 0xfa, 0xff, 0x68, 0x65, 0x6c, 0x6c, 0x6f])
        XCTAssertEqual([UInt8](Gzip.deflateStored(Data())), [0x01, 0x00, 0x00, 0xff, 0xff])
    }

    func testStoredBlocksSplitAt65535() throws {
        let input = Data((0..<200_000).map { UInt8(truncatingIfNeeded: $0 &* 31) })
        let body = Gzip.deflateStored(input)
        XCTAssertEqual(body.count, input.count + 4 * 5)
        XCTAssertEqual(try TestGunzip.inflateStored([UInt8](body)), input)
    }

    func testHeaderAndTrailer() throws {
        let input = Data("hello".utf8)
        let gz = [UInt8](Gzip.compress(input))
        XCTAssertEqual(Array(gz[0..<4]), [0x1f, 0x8b, 0x08, 0x00])
        XCTAssertEqual(Array(gz[(gz.count - 8)...]), [0x86, 0xa6, 0x10, 0x36, 0x05, 0x00, 0x00, 0x00])
        XCTAssertEqual(try TestGunzip.gunzip(Data(gz)), input)
    }

    func testRoundTripLargeInput() throws {
        var text = ""
        for i in 0..<20_000 {
            text += #"{"quantityType":"HKQuantityTypeIdentifierHeartRate","value":"# + String(60 + i % 40) + "}\n"
        }
        let input = Data(text.utf8)
        let gz = Gzip.compress(input)
        XCTAssertEqual(try TestGunzip.gunzip(gz), input)
        if let viaSystem = try TestGunzip.systemGunzip(gz) {
            XCTAssertEqual(viaSystem, input)
        }
    }

    func testEmptyInput() throws {
        let gz = Gzip.compress(Data())
        XCTAssertEqual(try TestGunzip.gunzip(gz), Data())
        if let viaSystem = try TestGunzip.systemGunzip(gz) {
            XCTAssertEqual(viaSystem, Data())
        }
    }
}
