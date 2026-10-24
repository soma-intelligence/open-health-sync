// SPDX-License-Identifier: AGPL-3.0-only

import Foundation

/// One upload part for `POST /v1/ingest/apple-health`: the lines of one HealthKit type and
/// one local day, as gzip compressed NDJSON.
public struct AppleHealthPart: Sendable, Equatable {
    /// `X-OHS-Type`: the HealthKit type identifier of every line.
    public var type: String
    /// `X-OHS-Day`: `YYYY-MM-DD`.
    public var day: String
    /// `X-OHS-Batch`.
    public var batch: String
    /// `X-OHS-Part`: unique within the batch, across types.
    public var partNumber: Int
    /// The gzip bytes.
    public var body: Data
    /// How many lines the part holds.
    public var lineCount: Int
    /// The NDJSON size before compression.
    public var uncompressedBytes: Int

    public init(
        type: String, day: String, batch: String, partNumber: Int, body: Data,
        lineCount: Int = 0, uncompressedBytes: Int = 0
    ) {
        self.type = type
        self.day = day
        self.batch = batch
        self.partNumber = partNumber
        self.body = body
        self.lineCount = lineCount
        self.uncompressedBytes = uncompressedBytes
    }
}

/// Turns upload lines into parts the way `ohs import apple-health` does: one part per
/// HealthKit type and local day, at most `maxPartBytes` of NDJSON before compression,
/// gzip compressed, numbered from 0 across the whole batch. Lines added with
/// `addToEveryPart(line:)` open every part.
public struct AppleHealthPartBuilder: Sendable {
    public static let defaultMaxPartBytes = 8 * 1024 * 1024

    public let batch: String
    public let maxPartBytes: Int

    private struct GroupKey: Hashable, Sendable {
        let type: String
        let day: String
    }

    private var order: [GroupKey] = []
    private var groups: [GroupKey: [Data]] = [:]
    private var shared: [Data] = []

    /// - Parameters:
    ///   - batch: the batch id, letters, digits, `_` and `-`, 1 to 128 characters. Use a
    ///     fresh one per run (`newBatchId`).
    ///   - maxPartBytes: the most NDJSON bytes per part before compression.
    public init(batch: String, maxPartBytes: Int = AppleHealthPartBuilder.defaultMaxPartBytes) throws {
        guard AppleHealthPartBuilder.isValidToken(batch) else {
            throw OHSError.invalidArgument("batch must be 1 to 128 letters, digits, '_' or '-'")
        }
        guard maxPartBytes > 0 else {
            throw OHSError.invalidArgument("maxPartBytes must be positive")
        }
        self.batch = batch
        self.maxPartBytes = maxPartBytes
    }

    /// Whether no line was added with `add`. Lines for every part alone make no part.
    public var isEmpty: Bool { order.isEmpty }

    /// Adds one line, a JSON object in the server's line format.
    /// - Parameters:
    ///   - type: the HealthKit type identifier (`HKQuantityTypeIdentifierHeartRate`).
    ///   - day: the local day of the record, `YYYY-MM-DD`.
    public mutating func add(type: String, day: String, line: JSONObject) throws {
        guard AppleHealthPartBuilder.isValidToken(type) else {
            throw OHSError.invalidArgument("type must be 1 to 128 letters, digits, '_' or '-'")
        }
        guard RFC3339.isValidDay(day) else {
            throw OHSError.invalidArgument("day must be a YYYY-MM-DD date")
        }
        var bytes = JSONValue.object(line).compactJSONData()
        bytes.append(0x0a)
        let key = GroupKey(type: type, day: day)
        if groups[key] == nil {
            order.append(key)
            groups[key] = [bytes]
        } else {
            groups[key]?.append(bytes)
        }
    }

    /// Adds a line that opens every part built, whatever its type and day: a deletion, which
    /// names no day, goes with every part of its query round. The server applies the
    /// deletions of a part before its records, so an edited sample (HealthKit deletes the old
    /// one and adds a new one) replaces the stored copy whichever part is processed first.
    public mutating func addToEveryPart(line: JSONObject) {
        var bytes = JSONValue.object(line).compactJSONData()
        bytes.append(0x0a)
        shared.append(bytes)
    }

    /// Adds a typed line; its day is the local day of its start in the line's `HKTimeZone`,
    /// or in `defaultTimeZone` when the line has none.
    public mutating func add<Line: AppleHealthLine>(_ line: Line, defaultTimeZone: TimeZone = .current) throws {
        let zone = line.timeZoneIdentifier.flatMap { TimeZone(identifier: $0) } ?? defaultTimeZone
        try add(
            type: line.typeIdentifier,
            day: AppleHealthPartBuilder.localDay(line.startDate, timeZone: zone),
            line: line.jsonObject())
    }

    /// The parts, groups in the order their first line was added. Part numbers start at 0.
    public func build() -> [AppleHealthPart] {
        var parts: [AppleHealthPart] = []
        var number = 0
        for key in order {
            let lines = groups[key] ?? []
            let opening = shared.reduce(into: Data()) { $0.append($1) }
            var current = opening
            var count = 0
            func flush() {
                guard count > 0 else { return }
                parts.append(AppleHealthPart(
                    type: key.type, day: key.day, batch: batch, partNumber: number,
                    body: Gzip.compress(current), lineCount: shared.count + count,
                    uncompressedBytes: current.count))
                number += 1
                current = opening
                count = 0
            }
            for line in lines {
                if count > 0 && current.count + line.count > maxPartBytes {
                    flush()
                }
                current.append(line)
                count += 1
            }
            flush()
        }
        return parts
    }

    /// The calendar day (`YYYY-MM-DD`) of `instant` in `timeZone`.
    public static func localDay(_ instant: Date, timeZone: TimeZone) -> String {
        return RFC3339.localDay(instant, timeZone: timeZone)
    }

    /// A fresh batch id: `prefix`, `-`, then the current UTC time and random characters.
    /// Characters of `prefix` the server does not accept are dropped.
    public static func newBatchId(prefix: String = "swift") -> String {
        var cleaned = ""
        var kept = 0
        for scalar in prefix.unicodeScalars where isTokenScalar(scalar) && kept < 64 {
            cleaned.unicodeScalars.append(scalar)
            kept += 1
        }
        if cleaned.isEmpty {
            cleaned = "batch"
        }
        let stamp = RFC3339.format(Date())
            .replacingOccurrences(of: "-", with: "")
            .replacingOccurrences(of: ":", with: "")
            .replacingOccurrences(of: ".", with: "")
        let random = UUID().uuidString.lowercased().replacingOccurrences(of: "-", with: "").prefix(12)
        return "\(cleaned)-\(stamp)-\(random)"
    }

    static func isValidToken(_ s: String) -> Bool {
        let scalars = s.unicodeScalars
        guard !scalars.isEmpty, scalars.count <= 128 else { return false }
        return scalars.allSatisfy { isTokenScalar($0) }
    }

    static func isTokenScalar(_ c: Unicode.Scalar) -> Bool {
        switch c.value {
        case 48...57, 65...90, 97...122, 95, 45:  // 0-9 A-Z a-z _ -
            return true
        default:
            return false
        }
    }
}
