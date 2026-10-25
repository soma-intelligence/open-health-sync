// SPDX-License-Identifier: AGPL-3.0-only

import Foundation

// One type per schema of api/openapi.yaml. JSON names are snake_case; Swift names are
// camelCase through explicit coding keys (a key strategy would also rewrite the keys of
// `metadata`). Fields the server adds later are ignored. Times are `Date`, parsed from
// RFC 3339 with up to nine fractional digits; calendar days stay `YYYY-MM-DD` strings.

/// A free-form JSON object (`metadata`, `nutrients`, FHIR resources).
public typealias JSONObject = [String: JSONValue]

/// One page of a list. `nextCursor` is nil on the last page.
public struct Page<T: Codable & Sendable>: Codable, Sendable {
    public var data: [T]
    public var nextCursor: String?

    public init(data: [T], nextCursor: String?) {
        self.data = data
        self.nextCursor = nextCursor
    }

    enum CodingKeys: String, CodingKey {
        case data
        case nextCursor = "next_cursor"
    }
}

/// A connector enabled on the instance.
public struct Provider: Codable, Sendable, Equatable {
    public var slug: String
    /// `oauth`, `push` or `device`.
    public var kinds: [String]
}

/// An end user of the tenant.
public struct User: Codable, Sendable, Equatable {
    public var id: String
    public var externalRef: String?
    /// `active` or `deleting`.
    public var status: String
    public var createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case externalRef = "external_ref"
        case status
        case createdAt = "created_at"
    }
}

/// A user deletion job.
public struct DeletionJob: Codable, Sendable, Equatable {
    public var jobId: String
    public var userId: String
    /// `pending`, `running`, `done` or `failed`.
    public var status: String
    public var createdAt: Date
    public var finishedAt: Date?

    enum CodingKeys: String, CodingKey {
        case jobId = "job_id"
        case userId = "user_id"
        case status
        case createdAt = "created_at"
        case finishedAt = "finished_at"
    }
}

/// The scopes a user token can carry.
public enum TokenScope: String, Codable, Sendable, CaseIterable {
    case upload
    case connect
    case read
}

/// A minted user token. `token` is shown once: keep it out of logs.
public struct UserToken: Codable, Sendable, Equatable {
    public var token: String
    public var expiresAt: Date
    public var scopes: [String]

    enum CodingKeys: String, CodingKey {
        case token
        case expiresAt = "expires_at"
        case scopes
    }
}

/// A provider connection of a user.
public struct Connection: Codable, Sendable, Equatable {
    public var provider: String
    /// `active`, `needs_reauth` or `disconnected`.
    public var status: String
    public var scopes: [String]?
    public var lastSyncAt: Date?
    public var initialBackfillAt: Date?
    public var createdAt: Date
    public var updatedAt: Date?

    enum CodingKeys: String, CodingKey {
        case provider
        case status
        case scopes
        case lastSyncAt = "last_sync_at"
        case initialBackfillAt = "initial_backfill_at"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }
}

/// How up to date one source of a user is.
public struct SourceFreshness: Codable, Sendable, Equatable {
    public var provider: String
    /// `oauth` or `device`.
    public var kind: String
    public var status: String
    /// `fresh`, `stale`, `failing`, `syncing`, `needs_reauth` or `disconnected`.
    public var state: String
    public var syncedThrough: Date?
    public var lagSeconds: Int64?
    public var lastSyncAt: Date?
    public var lastEventAt: Date?
    public var pushHealthy: Bool
    public var pullEverySeconds: Int64
    public var nextPullAt: Date?
    public var lastAttemptAt: Date?
    public var lastError: String?
    public var consecutiveFailures: Int

    enum CodingKeys: String, CodingKey {
        case provider
        case kind
        case status
        case state
        case syncedThrough = "synced_through"
        case lagSeconds = "lag_seconds"
        case lastSyncAt = "last_sync_at"
        case lastEventAt = "last_event_at"
        case pushHealthy = "push_healthy"
        case pullEverySeconds = "pull_every_seconds"
        case nextPullAt = "next_pull_at"
        case lastAttemptAt = "last_attempt_at"
        case lastError = "last_error"
        case consecutiveFailures = "consecutive_failures"
    }
}

/// The answer of `getFreshness`.
public struct Freshness: Codable, Sendable, Equatable {
    public var asOf: Date
    public var data: [SourceFreshness]

    enum CodingKeys: String, CodingKey {
        case asOf = "as_of"
        case data
    }
}

/// A quantity sample (heart rate, steps...), in the storage unit of its type.
public struct Sample: Codable, Sendable, Equatable {
    public var id: Int64
    public var type: String
    public var value: Double
    public var unit: String?
    public var startDate: Date
    public var endDate: Date?
    public var sourceName: String?
    public var sourceBundleId: String?
    public var deviceModel: String?
    public var productType: String?
    public var externalId: String?
    public var isPrimarySource: Bool?
    public var wasUserEntered: Bool?
    public var metadata: JSONObject
    public var provider: String
    public var source: String?
    public var device: String?
    public var ingestedAt: Date

    enum CodingKeys: String, CodingKey {
        case id, type, value, unit
        case startDate = "start_date"
        case endDate = "end_date"
        case sourceName = "source_name"
        case sourceBundleId = "source_bundle_id"
        case deviceModel = "device_model"
        case productType = "product_type"
        case externalId = "external_id"
        case isPrimarySource = "is_primary_source"
        case wasUserEntered = "was_user_entered"
        case metadata, provider, source, device
        case ingestedAt = "ingested_at"
    }
}

/// A category sample (an event with an integer value).
public struct CategorySample: Codable, Sendable, Equatable {
    public var id: Int64
    public var type: String
    public var valueInt: Int64?
    public var valueLabel: String?
    public var startDate: Date
    public var endDate: Date?
    public var sourceName: String?
    public var externalId: String?
    public var metadata: JSONObject
    public var provider: String
    public var source: String?
    public var device: String?
    public var ingestedAt: Date

    enum CodingKeys: String, CodingKey {
        case id, type
        case valueInt = "value_int"
        case valueLabel = "value_label"
        case startDate = "start_date"
        case endDate = "end_date"
        case sourceName = "source_name"
        case externalId = "external_id"
        case metadata, provider, source, device
        case ingestedAt = "ingested_at"
    }
}

/// A sleep session.
public struct SleepSession: Codable, Sendable, Equatable {
    public var id: Int64
    public var bedtimeStart: Date
    public var bedtimeEnd: Date?
    public var totalSleepSeconds: Int64?
    public var deepSleepSeconds: Int64?
    public var remSleepSeconds: Int64?
    public var lightSleepSeconds: Int64?
    public var awakeSeconds: Int64?
    public var sleepScore: Double?
    public var hrvAvgMs: Double?
    public var restingHr: Double?
    public var respiratoryRate: Double?
    public var spo2Avg: Double?
    public var externalId: String?
    public var metadata: JSONObject
    public var provider: String
    public var source: String?
    public var device: String?
    public var ingestedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case bedtimeStart = "bedtime_start"
        case bedtimeEnd = "bedtime_end"
        case totalSleepSeconds = "total_sleep_seconds"
        case deepSleepSeconds = "deep_sleep_seconds"
        case remSleepSeconds = "rem_sleep_seconds"
        case lightSleepSeconds = "light_sleep_seconds"
        case awakeSeconds = "awake_seconds"
        case sleepScore = "sleep_score"
        case hrvAvgMs = "hrv_avg_ms"
        case restingHr = "resting_hr"
        case respiratoryRate = "respiratory_rate"
        case spo2Avg = "spo2_avg"
        case externalId = "external_id"
        case metadata, provider, source, device
        case ingestedAt = "ingested_at"
    }
}

/// One phase of a sleep session.
public struct SleepPhase: Codable, Sendable, Equatable {
    public var id: Int64
    public var sessionId: Int64
    /// `deep`, `rem`, `light`, `awake` or `in_bed`.
    public var phase: String
    public var startDate: Date
    public var endDate: Date?
    public var durationS: Int64
    public var provider: String
    public var source: String?
    public var device: String?
    public var ingestedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case sessionId = "session_id"
        case phase
        case startDate = "start_date"
        case endDate = "end_date"
        case durationS = "duration_s"
        case provider, source, device
        case ingestedAt = "ingested_at"
    }
}

/// A workout.
public struct Workout: Codable, Sendable, Equatable {
    public var id: Int64
    public var activityType: String
    public var startDate: Date
    public var endDate: Date?
    public var durationS: Int64?
    public var activeEnergyKcal: Double?
    public var distanceM: Double?
    public var avgHeartRate: Double?
    public var maxHeartRate: Double?
    public var avgSpeedMps: Double?
    public var elevationGainM: Double?
    public var vo2Max: Double?
    public var externalId: String?
    public var metadata: JSONObject
    public var provider: String
    public var source: String?
    public var device: String?
    public var ingestedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case activityType = "activity_type"
        case startDate = "start_date"
        case endDate = "end_date"
        case durationS = "duration_s"
        case activeEnergyKcal = "active_energy_kcal"
        case distanceM = "distance_m"
        case avgHeartRate = "avg_heart_rate"
        case maxHeartRate = "max_heart_rate"
        case avgSpeedMps = "avg_speed_mps"
        case elevationGainM = "elevation_gain_m"
        case vo2Max = "vo2_max"
        case externalId = "external_id"
        case metadata, provider, source, device
        case ingestedAt = "ingested_at"
    }
}

/// One GPS point of a workout route.
public struct RoutePoint: Codable, Sendable, Equatable {
    public var id: Int64
    public var workoutId: Int64
    public var timestamp: Date
    public var latitude: Double
    public var longitude: Double
    public var altitudeM: Double?
    public var speedMps: Double?
    public var courseDeg: Double?
    public var provider: String
    public var source: String?
    public var device: String?
    public var ingestedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case workoutId = "workout_id"
        case timestamp, latitude, longitude
        case altitudeM = "altitude_m"
        case speedMps = "speed_mps"
        case courseDeg = "course_deg"
        case provider, source, device
        case ingestedAt = "ingested_at"
    }
}

/// A nutrition log.
public struct NutritionLog: Codable, Sendable, Equatable {
    public var id: Int64
    public var startDate: Date
    public var endDate: Date?
    public var mealType: String?
    public var energyKcal: Double?
    public var proteinG: Double?
    public var carbsG: Double?
    public var fatG: Double?
    public var fiberG: Double?
    public var sugarG: Double?
    public var sodiumMg: Double?
    /// Extended nutrients by name.
    public var nutrients: JSONObject?
    public var externalId: String?
    public var metadata: JSONObject
    public var provider: String
    public var source: String?
    public var device: String?
    public var ingestedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case startDate = "start_date"
        case endDate = "end_date"
        case mealType = "meal_type"
        case energyKcal = "energy_kcal"
        case proteinG = "protein_g"
        case carbsG = "carbs_g"
        case fatG = "fat_g"
        case fiberG = "fiber_g"
        case sugarG = "sugar_g"
        case sodiumMg = "sodium_mg"
        case nutrients
        case externalId = "external_id"
        case metadata, provider, source, device
        case ingestedAt = "ingested_at"
    }
}

/// One row per provider and local calendar day.
public struct DailySummary: Codable, Sendable, Equatable {
    public var id: Int64
    /// The user's local calendar day, `YYYY-MM-DD`.
    public var date: String
    public var readinessScore: Double?
    public var activityScore: Double?
    public var sleepScore: Double?
    public var recoveryScore: Double?
    public var strainScore: Double?
    public var stepCount: Int64?
    public var activeKcal: Double?
    public var totalKcal: Double?
    public var distanceM: Double?
    public var floorsClimbed: Int64?
    public var restingHr: Double?
    public var hrvAvgMs: Double?
    public var metadata: JSONObject
    public var provider: String
    public var source: String?
    public var device: String?
    public var ingestedAt: Date

    enum CodingKeys: String, CodingKey {
        case id, date
        case readinessScore = "readiness_score"
        case activityScore = "activity_score"
        case sleepScore = "sleep_score"
        case recoveryScore = "recovery_score"
        case strainScore = "strain_score"
        case stepCount = "step_count"
        case activeKcal = "active_kcal"
        case totalKcal = "total_kcal"
        case distanceM = "distance_m"
        case floorsClimbed = "floors_climbed"
        case restingHr = "resting_hr"
        case hrvAvgMs = "hrv_avg_ms"
        case metadata, provider, source, device
        case ingestedAt = "ingested_at"
    }
}

/// Record counts of one provider on one local day.
public struct CoverageDay: Codable, Sendable, Equatable {
    public var provider: String
    /// `YYYY-MM-DD`.
    public var date: String
    public var records: Int64
    public var metrics: [String: Int64]
    public var lastSyncedAt: Date

    enum CodingKeys: String, CodingKey {
        case provider, date, records, metrics
        case lastSyncedAt = "last_synced_at"
    }
}

/// What has landed, per provider and day, and what is still processing.
public struct Coverage: Codable, Sendable, Equatable {
    public var pendingJobs: Int64
    public var parkedJobs: Int64
    public var rejectedRecords: Int64
    public var truncated: Bool
    public var days: [CoverageDay]

    enum CodingKeys: String, CodingKey {
        case pendingJobs = "pending_jobs"
        case parkedJobs = "parked_jobs"
        case rejectedRecords = "rejected_records"
        case truncated, days
    }
}

/// One page of FHIR R4 Observations, kept as raw JSON.
public struct FHIRBundle: Sendable, Equatable {
    /// The whole Bundle as the server sent it.
    public var raw: JSONObject
    /// The `cursor` query parameter of the `next` link, nil on the last page.
    public var nextCursor: String?
    /// The `resource` of every entry, in order.
    public var observations: [JSONObject]

    public init(raw: JSONObject) {
        self.raw = raw
        var observations: [JSONObject] = []
        if let entries = raw["entry"]?.arrayValue {
            for entry in entries {
                if let resource = entry["resource"]?.objectValue {
                    observations.append(resource)
                }
            }
        }
        self.observations = observations
        var next: String?
        if let links = raw["link"]?.arrayValue {
            for link in links where link["relation"]?.stringValue == "next" {
                if let url = link["url"]?.stringValue {
                    next = FHIRBundle.cursor(fromURL: url)
                }
            }
        }
        self.nextCursor = next
    }

    static func cursor(fromURL url: String) -> String? {
        guard let components = URLComponents(string: url) else { return nil }
        for item in components.queryItems ?? [] where item.name == "cursor" {
            if let value = item.value, !value.isEmpty {
                return value
            }
        }
        return nil
    }
}

/// The answer of an Apple Health part upload.
public struct UploadResult: Codable, Sendable, Equatable {
    /// The raw object key.
    public var key: String
    /// True when the same bytes were already stored under this key.
    public var duplicate: Bool
    /// What the server applies beyond the base line format (`deletion_lines`); nil from a
    /// server older than the field.
    public var features: [String]?

    public init(key: String, duplicate: Bool, features: [String]? = nil) {
        self.key = key
        self.duplicate = duplicate
        self.features = features
    }

    /// The feature of the deletion line.
    public static let deletionLines = "deletion_lines"

    /// Whether the server applies deletion lines.
    public var acceptsDeletionLines: Bool { features?.contains(UploadResult.deletionLines) ?? false }
}
