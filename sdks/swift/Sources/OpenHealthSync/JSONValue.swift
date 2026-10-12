// SPDX-License-Identifier: MIT

import Foundation

/// A JSON value of any shape. Used for free-form maps (`metadata`, `nutrients`), FHIR
/// resources and the upload lines built for the Apple Health route.
public enum JSONValue: Sendable, Equatable, Hashable {
    case null
    case bool(Bool)
    case number(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    /// The member `key` of an object, or nil for any other value.
    public subscript(key: String) -> JSONValue? {
        if case .object(let members) = self {
            return members[key]
        }
        return nil
    }

    public var stringValue: String? {
        if case .string(let s) = self { return s }
        return nil
    }

    public var doubleValue: Double? {
        if case .number(let d) = self { return d }
        return nil
    }

    public var intValue: Int? {
        if case .number(let d) = self, d.isFinite, d == d.rounded(.towardZero),
           abs(d) <= 9_007_199_254_740_992 {
            return Int(d)
        }
        return nil
    }

    public var boolValue: Bool? {
        if case .bool(let b) = self { return b }
        return nil
    }

    public var arrayValue: [JSONValue]? {
        if case .array(let a) = self { return a }
        return nil
    }

    public var objectValue: [String: JSONValue]? {
        if case .object(let o) = self { return o }
        return nil
    }

    public var isNull: Bool {
        if case .null = self { return true }
        return false
    }
}

// MARK: - Codable

extension JSONValue: Codable {
    public init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if container.decodeNil() {
            self = .null
        } else if let b = try? container.decode(Bool.self) {
            self = .bool(b)
        } else if let d = try? container.decode(Double.self) {
            self = .number(d)
        } else if let s = try? container.decode(String.self) {
            self = .string(s)
        } else if let a = try? container.decode([JSONValue].self) {
            self = .array(a)
        } else if let o = try? container.decode([String: JSONValue].self) {
            self = .object(o)
        } else {
            throw DecodingError.dataCorruptedError(
                in: container, debugDescription: "not a JSON value")
        }
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case .null:
            try container.encodeNil()
        case .bool(let b):
            try container.encode(b)
        case .number(let d):
            try container.encode(d)
        case .string(let s):
            try container.encode(s)
        case .array(let a):
            try container.encode(a)
        case .object(let o):
            try container.encode(o)
        }
    }
}

// MARK: - Literals

extension JSONValue: ExpressibleByNilLiteral {
    public init(nilLiteral: ()) { self = .null }
}

extension JSONValue: ExpressibleByBooleanLiteral {
    public init(booleanLiteral value: Bool) { self = .bool(value) }
}

extension JSONValue: ExpressibleByIntegerLiteral {
    public init(integerLiteral value: Int) { self = .number(Double(value)) }
}

extension JSONValue: ExpressibleByFloatLiteral {
    public init(floatLiteral value: Double) { self = .number(value) }
}

extension JSONValue: ExpressibleByStringLiteral {
    public init(stringLiteral value: String) { self = .string(value) }
}

extension JSONValue: ExpressibleByArrayLiteral {
    public init(arrayLiteral elements: JSONValue...) { self = .array(elements) }
}

extension JSONValue: ExpressibleByDictionaryLiteral {
    public init(dictionaryLiteral elements: (String, JSONValue)...) {
        var members: [String: JSONValue] = [:]
        for (key, value) in elements {
            members[key] = value
        }
        self = .object(members)
    }
}

// MARK: - Serialization

extension JSONValue {
    /// Parses one JSON document.
    public static func parse(_ data: Data) throws -> JSONValue {
        return try JSONDecoder().decode(JSONValue.self, from: data)
    }

    /// Compact JSON text: no whitespace, object keys sorted, integral numbers written
    /// without a fraction (`120`, not `120.0`), non-finite numbers written as `null`.
    public func compactJSONString() -> String {
        var out = ""
        JSONValue.write(self, into: &out)
        return out
    }

    /// `compactJSONString()` as UTF-8 bytes.
    public func compactJSONData() -> Data {
        return Data(compactJSONString().utf8)
    }

    static func write(_ value: JSONValue, into out: inout String) {
        switch value {
        case .null:
            out += "null"
        case .bool(let b):
            out += b ? "true" : "false"
        case .number(let d):
            out += formatNumber(d)
        case .string(let s):
            writeString(s, into: &out)
        case .array(let items):
            out += "["
            var first = true
            for item in items {
                if !first { out += "," }
                first = false
                write(item, into: &out)
            }
            out += "]"
        case .object(let members):
            out += "{"
            var first = true
            for key in members.keys.sorted() {
                if !first { out += "," }
                first = false
                writeString(key, into: &out)
                out += ":"
                write(members[key] ?? .null, into: &out)
            }
            out += "}"
        }
    }

    static func formatNumber(_ d: Double) -> String {
        guard d.isFinite else { return "null" }
        if d == d.rounded(.towardZero) && abs(d) < 1e15 {
            return String(Int64(d))
        }
        return "\(d)"
    }

    static func writeString(_ s: String, into out: inout String) {
        out += "\""
        for scalar in s.unicodeScalars {
            switch scalar {
            case "\"":
                out += "\\\""
            case "\\":
                out += "\\\\"
            case "\n":
                out += "\\n"
            case "\r":
                out += "\\r"
            case "\t":
                out += "\\t"
            default:
                if scalar.value < 0x20 {
                    let hex = String(scalar.value, radix: 16)
                    out += "\\u" + String(repeating: "0", count: 4 - hex.count) + hex
                } else {
                    out.unicodeScalars.append(scalar)
                }
            }
        }
        out += "\""
    }
}
