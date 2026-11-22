// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

// The routes an app needs beyond the base reads: the hosted connection page, statistics,
// heartbeat series (RR intervals, HRV), ECG recordings, time tags and self reports. Each
// type mirrors a schema of api/openapi.yaml, like Models.swift.

// MARK: - Connect sessions

/// A link to the hosted connection page (`POST /v1/users/{id}/connect-sessions`).
public struct ConnectSession: Codable, Sendable, Equatable {
    /// The page to open for the user. The token in it is the page's only credential: show
    /// it to this user only and keep it out of logs.
    public var url: String
    public var expiresAt: Date

    enum CodingKeys: String, CodingKey {
        case url
        case expiresAt = "expires_at"
    }
}

// MARK: - Statistics

/// The width of the intervals of `sampleStats`.
public enum StatsInterval: String, Codable, Sendable, CaseIterable {
    case hour
    case day
}

/// Which sources a read keeps: the best ranked one per record or interval, or all of them.
public enum SourceView: String, Codable, Sendable, CaseIterable {
    case merged
    case all
}

/// One interval of `SampleStats`, for one provider, source and unit.
public struct SampleStatsBucket: Codable, Sendable, Equatable {
    /// The start of the interval.
    public var start: Date
    public var provider: String
    /// The app, bundle or device that recorded the samples; empty when the provider names none.
    public var source: String
    public var unit: String?
    public var count: Int
    public var sum: Double
    public var avg: Double
    public var min: Double
    public var max: Double
}

/// Count, sum, mean, min and max of one quantity type per hour or local day.
public struct SampleStats: Codable, Sendable, Equatable {
    public var type: String
    /// `hour` or `day`.
    public var interval: String
    /// The IANA time zone of the intervals.
    public var tz: String
    /// `merged` or `all`.
    public var view: String
    /// The range after widening to whole intervals.
    public var start: Date
    public var end: Date
    /// True when more than 50000 rows matched; `buckets` then holds the earliest.
    public var truncated: Bool
    /// The intervals that hold samples.
    public var buckets: [SampleStatsBucket]
}

// MARK: - Heartbeat series

/// One beat to beat (RR) interval.
public struct RRInterval: Codable, Sendable, Equatable {
    /// The beat that ends the interval.
    public var timestamp: Date
    /// The interval from the previous beat, in milliseconds.
    public var rrMs: Double
    public var seriesId: String
    public var provider: String
    public var ingestedAt: Date

    enum CodingKeys: String, CodingKey {
        case timestamp
        case rrMs = "rr_ms"
        case seriesId = "series_id"
        case provider
        case ingestedAt = "ingested_at"
    }
}

/// The heart rate variability of one heartbeat series. The measures are nil when too few
/// intervals were accepted to compute them.
public struct HRVSeries: Codable, Sendable, Equatable {
    public var seriesId: String
    public var provider: String
    /// The series' first beat.
    public var startDate: Date
    /// The series' last beat.
    public var endDate: Date
    /// The stored intervals of the series.
    public var intervals: Int
    /// The intervals kept after the plausibility and ectopic checks.
    public var acceptedIntervals: Int
    public var successivePairs: Int
    public var meanRrMs: Double?
    public var meanHeartRateBpm: Double?
    public var sdnnMs: Double?
    public var rmssdMs: Double?
    public var pnn50Percent: Double?

    enum CodingKeys: String, CodingKey {
        case seriesId = "series_id"
        case provider
        case startDate = "start_date"
        case endDate = "end_date"
        case intervals
        case acceptedIntervals = "accepted_intervals"
        case successivePairs = "successive_pairs"
        case meanRrMs = "mean_rr_ms"
        case meanHeartRateBpm = "mean_heart_rate_bpm"
        case sdnnMs = "sdnn_ms"
        case rmssdMs = "rmssd_ms"
        case pnn50Percent = "pnn50_percent"
    }
}

// MARK: - ECG

/// An ECG recording, without its voltage samples.
public struct ECGRecording: Codable, Sendable, Equatable {
    public var id: Int64
    public var startDate: Date
    public var endDate: Date?
    /// The label the recording device gave, in its maker's vocabulary (`sinus_rhythm`,
    /// `atrial_fibrillation`...), passed through as recorded; nil when the stored value is
    /// not a known label.
    public var classification: String?
    /// The stored value the label comes from.
    public var classificationValue: Int
    /// `not_set`, `none` or `present`.
    public var symptomsStatus: String?
    /// Beats per minute, as the device reported it.
    public var averageHeartRate: Double?
    /// Samples per second; 0 when the provider gives none.
    public var samplingFrequencyHz: Double
    public var numberOfMeasurements: Int
    public var metadata: JSONObject
    public var provider: String
    public var source: String?
    public var device: String?
    public var ingestedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case startDate = "start_date"
        case endDate = "end_date"
        case classification
        case classificationValue = "classification_value"
        case symptomsStatus = "symptoms_status"
        case averageHeartRate = "average_heart_rate"
        case samplingFrequencyHz = "sampling_frequency_hz"
        case numberOfMeasurements = "number_of_measurements"
        case metadata, provider, source, device
        case ingestedAt = "ingested_at"
    }
}

/// The voltage samples of one recording, single lead, as two parallel arrays.
public struct ECGSamples: Codable, Sendable, Equatable {
    /// How many samples were returned.
    public var count: Int
    /// True when more samples are stored than the 100000 returned.
    public var truncated: Bool
    /// Seconds from the recording's `startDate`, in order.
    public var offsetsS: [Double]
    /// Microvolts, one per offset.
    public var voltagesUv: [Double]

    enum CodingKeys: String, CodingKey {
        case count, truncated
        case offsetsS = "offsets_s"
        case voltagesUv = "voltages_uv"
    }
}

/// One ECG recording with its voltage samples.
public struct ECGRecordingDetail: Codable, Sendable, Equatable {
    public var recording: ECGRecording
    public var samples: ECGSamples

    enum CodingKeys: String, CodingKey {
        case samples
    }

    public init(recording: ECGRecording, samples: ECGSamples) {
        self.recording = recording
        self.samples = samples
    }

    // The recording's fields and `samples` are members of the same JSON object.
    public init(from decoder: Decoder) throws {
        recording = try ECGRecording(from: decoder)
        samples = try decoder.container(keyedBy: CodingKeys.self).decode(ECGSamples.self, forKey: .samples)
    }

    public func encode(to encoder: Encoder) throws {
        try recording.encode(to: encoder)
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(samples, forKey: .samples)
    }
}

// MARK: - Time tags

/// The kinds of time tags.
public enum TimeTagKind: String, Codable, Sendable, CaseIterable {
    case illness
    case injury
    case travel
    case jetLag = "jet_lag"
    case vaccination
    case medication
    case alcohol
    case other
}

/// An interval that gives context to the user's data: an illness, a trip, a vaccination.
public struct TimeTag: Codable, Sendable, Equatable {
    public var id: String
    /// A `TimeTagKind` raw value; kept as a string so a kind added later still decodes.
    public var kind: String
    public var label: String?
    public var startAt: Date
    /// Exclusive end; nil while the tag is ongoing.
    public var endAt: Date?
    /// An IANA time zone.
    public var timezone: String?
    public var note: String?
    /// Who recorded the tag; `user` unless the creator named itself.
    public var source: String
    public var createdAt: Date
    public var updatedAt: Date

    enum CodingKeys: String, CodingKey {
        case id, kind, label
        case startAt = "start_at"
        case endAt = "end_at"
        case timezone, note, source
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }
}

/// A time tag to record. `label` and `note` are free text and count as health data.
public struct NewTimeTag: Sendable, Equatable {
    public var kind: TimeTagKind
    /// An RFC 3339 time or a `YYYY-MM-DD` date (midnight UTC).
    public var startAt: OHSTime
    /// After `startAt`; nil for a tag still ongoing.
    public var endAt: OHSTime?
    public var label: String?
    /// An IANA time zone.
    public var timeZone: String?
    public var note: String?
    /// Who records the tag, for example your app's name: 1 to 64 letters, digits, `.`, `_`,
    /// `:` or `-`. nil means `user`.
    public var source: String?

    public init(
        kind: TimeTagKind, startAt: OHSTime, endAt: OHSTime? = nil, label: String? = nil,
        timeZone: String? = nil, note: String? = nil, source: String? = nil
    ) {
        self.kind = kind
        self.startAt = startAt
        self.endAt = endAt
        self.label = label
        self.timeZone = timeZone
        self.note = note
        self.source = source
    }

    func jsonObject() -> JSONObject {
        var body: JSONObject = ["kind": .string(kind.rawValue), "start_at": .string(startAt.value)]
        if let endAt = endAt { body["end_at"] = .string(endAt.value) }
        if let label = label { body["label"] = .string(label) }
        if let timeZone = timeZone { body["timezone"] = .string(timeZone) }
        if let note = note { body["note"] = .string(note) }
        if let source = source { body["source"] = .string(source) }
        return body
    }
}

/// What a change does to one member that can be cleared.
public enum FieldChange<Value: Sendable & Equatable>: Sendable, Equatable {
    /// Leave the member as it is (it is left out of the request).
    case keep
    /// Clear it (sent as null).
    case clear
    case set(Value)
}

/// The members of a time tag to change. The ones left alone stay as they are.
public struct TimeTagChange: Sendable, Equatable {
    public var kind: TimeTagKind?
    public var startAt: OHSTime?
    /// `.clear` reopens the tag.
    public var endAt: FieldChange<OHSTime> = .keep
    public var label: FieldChange<String> = .keep
    public var timeZone: FieldChange<String> = .keep
    public var note: FieldChange<String> = .keep

    public init() {}

    func jsonObject() -> JSONObject {
        var body: JSONObject = [:]
        if let kind = kind { body["kind"] = .string(kind.rawValue) }
        if let startAt = startAt { body["start_at"] = .string(startAt.value) }
        func put(_ key: String, _ change: FieldChange<String>) {
            switch change {
            case .keep:
                break
            case .clear:
                body[key] = .null
            case .set(let value):
                body[key] = .string(value)
            }
        }
        switch endAt {
        case .keep:
            break
        case .clear:
            body["end_at"] = .null
        case .set(let value):
            body["end_at"] = .string(value.value)
        }
        put("label", label)
        put("timezone", timeZone)
        put("note", note)
        return body
    }
}

// MARK: - Self reports

/// What a self report rates. `feeling`, `mood`, `stress`, `energy`, `sleepQuality` and
/// `muscleSoreness` go from 1 to 5; `exertion` and `pain` from 0 to 10.
public enum SelfReportKind: String, Codable, Sendable, CaseIterable {
    case feeling
    case mood
    case stress
    case energy
    case sleepQuality = "sleep_quality"
    case muscleSoreness = "muscle_soreness"
    case exertion
    case pain
}

/// A rating to record.
public struct NewSelfReport: Sendable, Equatable {
    /// Your id of the report (a UUID). Sending it again replaces the stored value. nil lets
    /// the server give one; a retry then records the report twice unless it carries the
    /// same idempotency key.
    public var id: String?
    public var kind: SelfReportKind
    /// A whole number on the kind's scale.
    public var value: Int
    /// When the rating applies.
    public var startAt: Date
    /// After `startAt`, for the rating of a period (a training session).
    public var endAt: Date?
    /// The IANA time zone the person was in.
    public var timeZone: String?

    public init(
        id: String? = nil, kind: SelfReportKind, value: Int, startAt: Date, endAt: Date? = nil,
        timeZone: String? = nil
    ) {
        self.id = id
        self.kind = kind
        self.value = value
        self.startAt = startAt
        self.endAt = endAt
        self.timeZone = timeZone
    }

    func jsonObject() -> JSONObject {
        var body: JSONObject = [
            "kind": .string(kind.rawValue),
            "value": .number(Double(value)),
            "start_at": .string(RFC3339.format(startAt)),
        ]
        if let id = id { body["id"] = .string(id) }
        if let endAt = endAt { body["end_at"] = .string(RFC3339.format(endAt)) }
        if let timeZone = timeZone { body["timezone"] = .string(timeZone) }
        return body
    }
}

/// A self report as stored.
public struct SelfReport: Codable, Sendable, Equatable {
    public var id: String
    /// A `SelfReportKind` raw value.
    public var kind: String
    /// The quantity type it is stored as, `reported_<kind>`.
    public var type: String
    public var value: Int
    public var startAt: Date
    public var endAt: Date?
    public var timezone: String?

    enum CodingKeys: String, CodingKey {
        case id, kind, type, value
        case startAt = "start_at"
        case endAt = "end_at"
        case timezone
    }
}

/// The answer of `createSelfReports`.
public struct SelfReportsResult: Codable, Sendable, Equatable {
    /// The reports as stored, in request order, each with its id.
    public var reports: [SelfReport]
    /// True when these exact reports had already been received.
    public var duplicate: Bool
}

// MARK: - Calls

extension OHSClient {
    /// Creates a link to the hosted connection page, where the user picks a provider and
    /// goes through its consent. Call it from your server with the tenant key and hand the
    /// URL to the app, which opens it (`ASWebAuthenticationSession`, or the browser).
    /// - Parameters:
    ///   - redirectUri: where the user lands after each consent, with
    ///     `?status=connected|error&provider=<slug>`; nil shows the built-in result page.
    ///   - providers: the provider slugs the page offers, in this order; nil offers every one.
    ///   - appName: shown as the page title.
    ///   - logoUrl: an https image shown above the title.
    ///   - ttlSeconds: 60 to 3600; nil means 1800.
    public func createConnectSession(
        userId: String, redirectUri: String? = nil, providers: [String]? = nil,
        appName: String? = nil, logoUrl: String? = nil, ttlSeconds: Int? = nil
    ) async throws -> ConnectSession {
        var body: JSONObject = [:]
        if let redirectUri = redirectUri { body["redirect_uri"] = .string(redirectUri) }
        if let providers = providers { body["providers"] = .array(providers.map { JSONValue.string($0) }) }
        if let appName = appName { body["app_name"] = .string(appName) }
        if let logoUrl = logoUrl { body["logo_url"] = .string(logoUrl) }
        if let ttl = ttlSeconds { body["ttl_seconds"] = .number(Double(ttl)) }
        var spec = Spec(method: "POST", path: "/v1/users/" + pathSegment(userId) + "/connect-sessions")
        spec.json(body)
        return try decode(ConnectSession.self, from: try await send(spec))
    }

    /// Count, sum, mean, min and max of one quantity type per hour or per local day. Not paged.
    /// - Parameters:
    ///   - start: inclusive, an RFC 3339 time or a `YYYY-MM-DD` date (midnight in `timeZone`).
    ///   - end: exclusive; at most 10000 intervals after `start`.
    ///   - interval: nil means a day.
    ///   - timeZone: the IANA zone of the intervals; nil means UTC.
    ///   - view: nil means `merged`, one row per interval from the best ranked source.
    public func sampleStats(
        userId: String, type: String, start: OHSTime, end: OHSTime,
        interval: StatsInterval? = nil, timeZone: String? = nil, provider: String? = nil,
        view: SourceView? = nil
    ) async throws -> SampleStats {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/samples/stats")
        spec.query.add("type", type)
        spec.query.add("interval", interval?.rawValue)
        spec.query.add("tz", timeZone)
        spec.query.add("start", start.value)
        spec.query.add("end", end.value)
        spec.query.add("provider", provider)
        spec.query.add("view", view?.rawValue)
        return try decode(SampleStats.self, from: try await send(spec))
    }

    /// The stored RR intervals of the user's heartbeat series, by beat time.
    public func listRRIntervals(
        userId: String, seriesId: String? = nil, start: OHSTime? = nil, end: OHSTime? = nil,
        cursor: String? = nil, limit: Int? = nil
    ) async throws -> Page<RRInterval> {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/rr-intervals")
        spec.query.add("series_id", seriesId)
        spec.query.range(start, end, cursor, limit)
        return try decode(Page<RRInterval>.self, from: try await send(spec))
    }

    /// Heart rate variability, one record per heartbeat series, by the series' first beat.
    public func listHRV(
        userId: String, seriesId: String? = nil, start: OHSTime? = nil, end: OHSTime? = nil,
        cursor: String? = nil, limit: Int? = nil
    ) async throws -> Page<HRVSeries> {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/hrv")
        spec.query.add("series_id", seriesId)
        spec.query.range(start, end, cursor, limit)
        return try decode(Page<HRVSeries>.self, from: try await send(spec))
    }

    /// The user's ECG recordings by start time, without their voltage samples.
    public func listECGRecordings(
        userId: String, start: OHSTime? = nil, end: OHSTime? = nil,
        cursor: String? = nil, limit: Int? = nil
    ) async throws -> Page<ECGRecording> {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/ecg")
        spec.query.range(start, end, cursor, limit)
        return try decode(Page<ECGRecording>.self, from: try await send(spec))
    }

    /// One ECG recording with its voltage samples (at most 100000).
    public func getECGRecording(userId: String, ecgId: Int64) async throws -> ECGRecordingDetail {
        let spec = Spec(
            method: "GET", path: "/v1/users/" + pathSegment(userId) + "/ecg/" + String(ecgId))
        return try decode(ECGRecordingDetail.self, from: try await send(spec))
    }

    /// Records a time tag. `idempotencyKey` makes a retry return the first answer.
    public func createTimeTag(
        userId: String, tag: NewTimeTag, idempotencyKey: String? = nil
    ) async throws -> TimeTag {
        var spec = Spec(method: "POST", path: "/v1/users/" + pathSegment(userId) + "/tags")
        spec.json(tag.jsonObject())
        if let key = idempotencyKey {
            spec.headers.append(("Idempotency-Key", key))
        }
        return try decode(TimeTag.self, from: try await send(spec))
    }

    /// The user's time tags by start; with `start` and `end`, the ones that overlap
    /// `[start, end)`.
    public func listTimeTags(
        userId: String, kind: TimeTagKind? = nil, start: OHSTime? = nil, end: OHSTime? = nil,
        cursor: String? = nil, limit: Int? = nil
    ) async throws -> Page<TimeTag> {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/tags")
        spec.query.add("kind", kind?.rawValue)
        spec.query.range(start, end, cursor, limit)
        return try decode(Page<TimeTag>.self, from: try await send(spec))
    }

    public func getTimeTag(userId: String, tagId: String) async throws -> TimeTag {
        let spec = Spec(
            method: "GET", path: "/v1/users/" + pathSegment(userId) + "/tags/" + pathSegment(tagId))
        return try decode(TimeTag.self, from: try await send(spec))
    }

    /// Changes a time tag and returns it after the change.
    public func updateTimeTag(userId: String, tagId: String, change: TimeTagChange) async throws -> TimeTag {
        var spec = Spec(
            method: "PATCH", path: "/v1/users/" + pathSegment(userId) + "/tags/" + pathSegment(tagId))
        spec.json(change.jsonObject())
        return try decode(TimeTag.self, from: try await send(spec))
    }

    public func deleteTimeTag(userId: String, tagId: String) async throws {
        let spec = Spec(
            method: "DELETE", path: "/v1/users/" + pathSegment(userId) + "/tags/" + pathSegment(tagId))
        _ = try await send(spec)
    }

    /// Records 1 to 100 of the user's own ratings. Works with the tenant key, or with a user
    /// token holding the `upload` scope for its own user. The reports are processed like an
    /// upload and read back as quantity samples of type `reported_<kind>`.
    public func createSelfReports(
        userId: String, reports: [NewSelfReport], idempotencyKey: String? = nil
    ) async throws -> SelfReportsResult {
        guard (1...100).contains(reports.count) else {
            throw OHSError.invalidArgument("reports must hold 1 to 100 items")
        }
        var spec = Spec(method: "POST", path: "/v1/users/" + pathSegment(userId) + "/self-reports")
        spec.json(["reports": .array(reports.map { JSONValue.object($0.jsonObject()) })])
        if let key = idempotencyKey {
            spec.headers.append(("Idempotency-Key", key))
        }
        return try decode(SelfReportsResult.self, from: try await send(spec))
    }
}
