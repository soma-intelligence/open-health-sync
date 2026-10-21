// SPDX-License-Identifier: AGPL-3.0-only

import Foundation

/// RFC 3339 times without `DateFormatter`: the server writes up to nine fractional digits
/// (Go's RFC3339Nano), which `ISO8601DateFormatter` does not read, and the same code has to
/// behave the same way on Apple platforms and on Linux.
public enum RFC3339 {
    /// Parses `2026-01-10T14:30:00Z`, `2026-01-10T14:30:00.123456789Z` or
    /// `2026-01-10T15:30:00.5+01:00` (0 to 9 or more fractional digits; digits past the
    /// ninth are ignored). Returns nil for anything else.
    public static func parse(_ string: String) -> Date? {
        let b = Array(string.utf8)
        guard b.count >= 20 else { return nil }

        func digits(_ start: Int, _ count: Int) -> Int? {
            guard start + count <= b.count else { return nil }
            var v = 0
            for i in start..<(start + count) {
                let c = b[i]
                guard c >= 48 && c <= 57 else { return nil }
                v = v * 10 + Int(c - 48)
            }
            return v
        }

        guard let year = digits(0, 4), b[4] == 45,  // '-'
              let month = digits(5, 2), b[7] == 45,
              let day = digits(8, 2),
              b[10] == 84 || b[10] == 116 || b[10] == 32,  // 'T', 't', ' '
              let hour = digits(11, 2), b[13] == 58,  // ':'
              let minute = digits(14, 2), b[16] == 58,
              let second = digits(17, 2)
        else { return nil }

        guard (1...12).contains(month),
              day >= 1, day <= daysInMonth(year: year, month: month),
              hour <= 23, minute <= 59, second <= 60
        else { return nil }

        var i = 19
        var nanos = 0
        if i < b.count && b[i] == 46 {  // '.'
            i += 1
            var n = 0
            var scale = 100_000_000
            while i < b.count && b[i] >= 48 && b[i] <= 57 {
                if scale > 0 {
                    nanos += Int(b[i] - 48) * scale
                    scale /= 10
                }
                n += 1
                i += 1
            }
            guard n > 0 else { return nil }
        }

        guard i < b.count else { return nil }
        var offset = 0
        if b[i] == 90 || b[i] == 122 {  // 'Z', 'z'
            i += 1
        } else if b[i] == 43 || b[i] == 45 {  // '+', '-'
            let sign = b[i] == 43 ? 1 : -1
            guard let oh = digits(i + 1, 2), i + 3 < b.count, b[i + 3] == 58,
                  let om = digits(i + 4, 2), oh <= 23, om <= 59
            else { return nil }
            offset = sign * (oh * 3600 + om * 60)
            i += 6
        } else {
            return nil
        }
        guard i == b.count else { return nil }

        let days = daysFromCivil(year: year, month: month, day: day)
        let seconds = Int64(days) * 86_400 + Int64(hour * 3600 + minute * 60 + second) - Int64(offset)
        return Date(timeIntervalSince1970: Double(seconds) + Double(nanos) / 1_000_000_000)
    }

    /// Formats in UTC with milliseconds, the way HealthKit apps write upload lines:
    /// `2026-01-10T14:30:00.000Z`.
    public static func format(_ date: Date) -> String {
        let totalMs = Int64((date.timeIntervalSince1970 * 1000).rounded())
        let secs = floorDiv(totalMs, 1000)
        let ms = Int(totalMs - secs * 1000)
        let days = floorDiv(secs, 86_400)
        let secOfDay = Int(secs - days * 86_400)
        let (y, m, d) = civilFromDays(Int(days))
        return pad(y, 4) + "-" + pad(m, 2) + "-" + pad(d, 2) + "T"
            + pad(secOfDay / 3600, 2) + ":" + pad((secOfDay % 3600) / 60, 2) + ":"
            + pad(secOfDay % 60, 2) + "." + pad(ms, 3) + "Z"
    }

    /// Formats with milliseconds and the UTC offset `timeZone` has at `date`:
    /// `2026-10-16T21:04:05.120+02:00` (`+00:00` for UTC).
    public static func format(_ date: Date, timeZone: TimeZone) -> String {
        let offset = timeZone.secondsFromGMT(for: date)
        let local = format(date.addingTimeInterval(TimeInterval(offset)))
        let minutes = abs(offset) / 60
        return String(local.dropLast()) + (offset < 0 ? "-" : "+") + pad(minutes / 60, 2) + ":" + pad(minutes % 60, 2)
    }

    /// The calendar day (`YYYY-MM-DD`) of `date` in `timeZone`.
    public static func localDay(_ date: Date, timeZone: TimeZone) -> String {
        let offset = Int64(timeZone.secondsFromGMT(for: date))
        let secs = Int64(date.timeIntervalSince1970.rounded(.down)) + offset
        let (y, m, d) = civilFromDays(Int(floorDiv(secs, 86_400)))
        return pad(y, 4) + "-" + pad(m, 2) + "-" + pad(d, 2)
    }

    /// Whether `string` is a valid `YYYY-MM-DD` calendar date.
    public static func isValidDay(_ string: String) -> Bool {
        let b = Array(string.utf8)
        guard b.count == 10, b[4] == 45, b[7] == 45 else { return false }
        for i in [0, 1, 2, 3, 5, 6, 8, 9] where !(b[i] >= 48 && b[i] <= 57) {
            return false
        }
        let year = Int(string.prefix(4)) ?? 0
        let month = Int(string.dropFirst(5).prefix(2)) ?? 0
        let day = Int(string.dropFirst(8).prefix(2)) ?? 0
        guard (1...12).contains(month) else { return false }
        return day >= 1 && day <= daysInMonth(year: year, month: month)
    }

    // MARK: - Calendar arithmetic (proleptic Gregorian, days since 1970-01-01)

    static func daysInMonth(year: Int, month: Int) -> Int {
        switch month {
        case 2:
            let leap = (year % 4 == 0 && year % 100 != 0) || year % 400 == 0
            return leap ? 29 : 28
        case 4, 6, 9, 11:
            return 30
        default:
            return 31
        }
    }

    static func daysFromCivil(year: Int, month: Int, day: Int) -> Int {
        let y = month <= 2 ? year - 1 : year
        let era = (y >= 0 ? y : y - 399) / 400
        let yoe = y - era * 400
        let mp = month > 2 ? month - 3 : month + 9
        let doy = (153 * mp + 2) / 5 + day - 1
        let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy
        return era * 146_097 + doe - 719_468
    }

    static func civilFromDays(_ days: Int) -> (Int, Int, Int) {
        let z = days + 719_468
        let era = (z >= 0 ? z : z - 146_096) / 146_097
        let doe = z - era * 146_097
        let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365
        let doy = doe - (365 * yoe + yoe / 4 - yoe / 100)
        let mp = (5 * doy + 2) / 153
        let d = doy - (153 * mp + 2) / 5 + 1
        let m = mp < 10 ? mp + 3 : mp - 9
        let y = yoe + era * 400 + (m <= 2 ? 1 : 0)
        return (y, m, d)
    }

    static func floorDiv(_ a: Int64, _ b: Int64) -> Int64 {
        let q = a / b
        return (a % b != 0 && (a < 0) != (b < 0)) ? q - 1 : q
    }

    static func pad(_ value: Int, _ width: Int) -> String {
        let s = String(value)
        return s.count >= width ? s : String(repeating: "0", count: width - s.count) + s
    }
}
