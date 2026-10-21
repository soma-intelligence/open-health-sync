// SPDX-License-Identifier: AGPL-3.0-only

import Foundation

/// The Bluetooth SIG numbers of the standard health profiles, and the few bytes a client
/// writes to a device. Values the device sends are not decoded here: they go up as the
/// bytes it sent, and the server decodes them (`docs/connectors/bluetooth.md`).
public enum BluetoothGATT {
    // Services, in the 16-bit form `CBUUID(string:)` accepts.
    public static let heartRateService = "180D"
    public static let bloodPressureService = "1810"
    public static let weightScaleService = "181D"
    public static let bodyCompositionService = "181B"
    public static let glucoseService = "1808"
    public static let healthThermometerService = "1809"
    public static let pulseOximeterService = "1822"
    public static let deviceInformationService = "180A"
    public static let batteryService = "180F"
    public static let currentTimeService = "1805"

    /// The health services to scan for.
    public static let healthServices = [
        heartRateService, bloodPressureService, weightScaleService, bodyCompositionService,
        glucoseService, healthThermometerService, pulseOximeterService,
    ]

    // Characteristics.
    public static let heartRateMeasurement = "2A37"
    public static let bloodPressureMeasurement = "2A35"
    public static let weightMeasurement = "2A9D"
    public static let bodyCompositionMeasurement = "2A9C"
    public static let glucoseMeasurement = "2A18"
    public static let temperatureMeasurement = "2A1C"
    public static let plxSpotCheckMeasurement = "2A5E"
    public static let plxContinuousMeasurement = "2A5F"
    public static let recordAccessControlPoint = "2A52"
    public static let batteryLevel = "2A19"
    public static let currentTime = "2A2B"
    public static let manufacturerName = "2A29"
    public static let modelNumber = "2A24"
    public static let firmwareRevision = "2A26"

    /// The characteristics whose values are uploaded: subscribe to these.
    public static let measurementCharacteristics = [
        heartRateMeasurement, bloodPressureMeasurement, weightMeasurement,
        bodyCompositionMeasurement, glucoseMeasurement, temperatureMeasurement,
        plxSpotCheckMeasurement, plxContinuousMeasurement,
    ]

    static let baseUUIDSuffix = "-0000-1000-8000-00805f9b34fb"

    /// The 16-bit form, lowercase, of a UUID string: `2A37`, `2a37` or
    /// `00002A37-0000-1000-8000-00805F9B34FB` give `2a37`. Nil for any other UUID.
    public static func shortForm(_ uuid: String) -> String? {
        var s = uuid.lowercased()
        if s.count == 36 && s.hasPrefix("0000") && s.hasSuffix(baseUUIDSuffix) {
            s = String(s.dropFirst(4).prefix(4))
        }
        guard s.count == 4, s.unicodeScalars.allSatisfy({ isHexDigit($0) }) else { return nil }
        return s
    }

    /// Whether a characteristic's values are uploaded.
    public static func isMeasurement(_ uuid: String) -> Bool {
        guard let s = shortForm(uuid) else { return false }
        return measurementCharacteristics.contains { $0.lowercased() == s }
    }

    /// The Record Access Control Point request for a glucose meter: report all stored
    /// records (`after` nil), or those whose sequence number is at least `after` + 1.
    public static func reportStoredRecords(after: UInt16?) -> Data {
        guard let after = after, after < UInt16.max else {
            return Data([0x01, 0x01])
        }
        let from = after + 1
        return Data([0x01, 0x03, 0x01, UInt8(from & 0xff), UInt8(from >> 8)])
    }

    /// The sequence number of a Glucose Measurement value.
    public static func glucoseSequenceNumber(_ value: Data) -> UInt16? {
        let b = [UInt8](value)
        guard b.count >= 3 else { return nil }
        return UInt16(b[1]) | UInt16(b[2]) << 8
    }

    /// The Current Time value for `date` as wall time in `timeZone` (a device clock has
    /// no zone, so it is set to the local time, as the device's own app does).
    public static func currentTimeValue(_ date: Date, timeZone: TimeZone = .current) -> Data {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = timeZone
        let c = calendar.dateComponents([.year, .month, .day, .hour, .minute, .second, .nanosecond, .weekday], from: date)
        let year = UInt16(c.year ?? 2000)
        // Calendar weekdays run from 1 (Sunday); the characteristic's from 1 (Monday).
        let weekday: Int = ((c.weekday ?? 1) + 5) % 7 + 1
        let fractions: Int = min(255, (c.nanosecond ?? 0) * 256 / 1_000_000_000)
        var out = Data([UInt8(year & 0xff), UInt8(year >> 8)])
        let fields: [Int] = [c.month ?? 1, c.day ?? 1, c.hour ?? 0, c.minute ?? 0, c.second ?? 0, weekday, fractions]
        out.append(contentsOf: fields.map { UInt8($0) })
        out.append(0x01)  // adjust reason: manual time update
        return out
    }

    /// An advertised name without a last word that looks like a serial number: hex digits
    /// after a space or an underscore, at least six, or at least four with a letter among
    /// them. "Polar H10 0A1B2C3D" is sent as "Polar H10"; "Edge 1040" is kept.
    public static func publicName(_ name: String) -> String {
        let trimmed = name.trimmingCharacters(in: .whitespaces)
        guard let cut = trimmed.lastIndex(where: { $0 == " " || $0 == "_" }),
              cut > trimmed.startIndex
        else { return trimmed }
        let tail = trimmed[trimmed.index(after: cut)...].unicodeScalars
        let letter = tail.contains { ("a"..."f").contains($0) || ("A"..."F").contains($0) }
        guard tail.allSatisfy({ isHexDigit($0) }), tail.count >= 6 || (tail.count >= 4 && letter) else {
            return trimmed
        }
        return trimmed[..<cut].trimmingCharacters(in: .whitespaces)
    }

    /// A device string as a device sends it (UTF-8, often padded with NULs), cleaned: no
    /// control characters, trimmed, at most 128 characters.
    public static func deviceText(_ raw: Data) -> String {
        let s = String(decoding: raw, as: UTF8.self)
        let kept = String(String.UnicodeScalarView(s.unicodeScalars.filter { !CharacterSet.controlCharacters.contains($0) }))
        return String(kept.trimmingCharacters(in: .whitespaces).prefix(128)).trimmingCharacters(in: .whitespaces)
    }

    static func isHexDigit(_ c: Unicode.Scalar) -> Bool {
        switch c.value {
        case 48...57, 65...70, 97...102: return true
        default: return false
        }
    }
}

/// What a device says about itself: never an address or a serial number.
public struct BluetoothDevice: Sendable, Equatable, Codable {
    /// The advertised name, without a serial-like last word (`BluetoothGATT.publicName`).
    public var name: String
    /// Device Information service strings, cleaned with `BluetoothGATT.deviceText`.
    public var manufacturer: String
    public var model: String
    public var firmware: String

    public init(name: String = "", manufacturer: String = "", model: String = "", firmware: String = "") {
        self.name = name
        self.manufacturer = manufacturer
        self.model = model
        self.firmware = firmware
    }

    var isEmpty: Bool { name.isEmpty && manufacturer.isEmpty && model.isEmpty && firmware.isEmpty }
}

/// One upload part for `POST /v1/ingest/bluetooth`: gzip NDJSON, one line per value.
/// Keep a part (it is `Codable`) until the server answers 200, and send the same part
/// again on a retry: the server then answers `duplicate: true`.
public struct BluetoothPart: Sendable, Equatable, Codable {
    /// `X-OHS-Type`, always `gatt`.
    public static let type = "gatt"
    /// `X-OHS-Day`: the UTC day of the first line.
    public var day: String
    /// `X-OHS-Batch`.
    public var batch: String
    /// `X-OHS-Part`.
    public var partNumber: Int
    /// The gzip bytes.
    public var body: Data
    public var lineCount: Int

    public init(day: String, batch: String, partNumber: Int = 0, body: Data, lineCount: Int) {
        self.day = day
        self.batch = batch
        self.partNumber = partNumber
        self.body = body
        self.lineCount = lineCount
    }
}

/// Collects the values devices send, as upload lines, and turns them into parts.
/// Feed it from CoreBluetooth (`OpenHealthSyncBluetooth`), Android's `BluetoothGatt` or
/// anything that receives characteristic values. It does not decode them.
public struct BluetoothRecorder: Sendable {
    /// The longest value a line may carry, the ATT maximum attribute length.
    public static let maxValueBytes = 512

    /// This person's user index (0 to 254) on a shared scale or cuff: readings of other
    /// users are then not stored.
    public let deviceUser: Int?

    private var lines = Data()
    private var firstAt: Date?
    public private(set) var lineCount = 0

    public init(deviceUser: Int? = nil) throws {
        if let u = deviceUser, !(0...254).contains(u) {
            throw OHSError.invalidArgument("deviceUser must be 0 to 254")
        }
        self.deviceUser = deviceUser
    }

    /// Whether no value was recorded since the last part.
    public var isEmpty: Bool { lineCount == 0 }

    /// Records one value as it arrived. Values of characteristics that are not uploaded
    /// (battery level, a vendor service) are ignored: the result is false.
    /// - Parameters:
    ///   - characteristic: the characteristic UUID, 16-bit or full form.
    ///   - value: the bytes as the device sent them.
    ///   - session: one id per connection to one device (`newSession()`).
    ///   - at: when the value arrived, written with the UTC offset of `timeZone`.
    @discardableResult
    public mutating func record(
        characteristic: String, value: Data, session: String, device: BluetoothDevice? = nil,
        at: Date = Date(), timeZone: TimeZone = .current
    ) throws -> Bool {
        guard let char = BluetoothGATT.shortForm(characteristic), BluetoothGATT.isMeasurement(char) else {
            return false
        }
        guard !value.isEmpty, value.count <= BluetoothRecorder.maxValueBytes else {
            throw OHSError.invalidArgument("a value holds 1 to 512 bytes")
        }
        guard BluetoothRecorder.isValidSession(session) else {
            throw OHSError.invalidArgument("session must be 8 to 64 letters, digits, '_' or '-'")
        }
        var line: [String: JSONValue] = [
            "at": .string(RFC3339.format(at, timeZone: timeZone)),
            "char": .string(char),
            "value": .string(value.map { String(format: "%02x", $0) }.joined()),
            "session": .string(session),
        ]
        if let d = device, !d.isEmpty {
            var o: [String: JSONValue] = [:]
            for (k, v) in [("name", d.name), ("manufacturer", d.manufacturer), ("model", d.model), ("firmware", d.firmware)]
            where !v.isEmpty {
                o[k] = .string(String(v.prefix(128)))
            }
            line["device"] = .object(o)
        }
        if let u = deviceUser {
            line["device_user"] = .number(Double(u))
        }
        lines.append(JSONValue.object(line).compactJSONData())
        lines.append(0x0a)
        lineCount += 1
        if firstAt == nil {
            firstAt = at
        }
        return true
    }

    /// The recorded lines as one part, and an empty recorder. Nil when nothing was
    /// recorded.
    public mutating func makePart(batch: String = BluetoothRecorder.newBatchId()) throws -> BluetoothPart? {
        guard AppleHealthPartBuilder.isValidToken(batch) else {
            throw OHSError.invalidArgument("batch must be 1 to 128 letters, digits, '_' or '-'")
        }
        guard let first = firstAt, lineCount > 0 else { return nil }
        let part = BluetoothPart(
            day: RFC3339.localDay(first, timeZone: TimeZone(identifier: "UTC")!),
            batch: batch, body: Gzip.compress(lines), lineCount: lineCount)
        lines = Data()
        lineCount = 0
        firstAt = nil
        return part
    }

    /// A new session id: 16 hex digits.
    public static func newSession() -> String {
        return String(UUID().uuidString.lowercased().replacingOccurrences(of: "-", with: "").prefix(16))
    }

    /// A fresh batch id: `bt1-`, the current UTC time and random characters.
    public static func newBatchId() -> String {
        return AppleHealthPartBuilder.newBatchId(prefix: "bt1")
    }

    static func isValidSession(_ s: String) -> Bool {
        let n = s.unicodeScalars.count
        return (8...64).contains(n) && AppleHealthPartBuilder.isValidToken(s)
    }
}
