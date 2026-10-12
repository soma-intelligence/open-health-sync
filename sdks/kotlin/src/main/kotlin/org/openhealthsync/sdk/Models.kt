// SPDX-License-Identifier: MIT
package org.openhealthsync.sdk

import java.time.Instant
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter
import kotlinx.serialization.KSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.descriptors.PrimitiveKind
import kotlinx.serialization.descriptors.PrimitiveSerialDescriptor
import kotlinx.serialization.descriptors.SerialDescriptor
import kotlinx.serialization.encoding.Decoder
import kotlinx.serialization.encoding.Encoder
import kotlinx.serialization.json.JsonObject

// The schemas of api/openapi.yaml, field for field. Dates (`date`) stay "YYYY-MM-DD" strings:
// they are the user's local calendar day, not an instant.

/** RFC 3339 with any number of fractional digits and any offset, to [Instant]. */
public object InstantSerializer : KSerializer<Instant> {
    override val descriptor: SerialDescriptor = PrimitiveSerialDescriptor("Rfc3339Instant", PrimitiveKind.STRING)

    override fun deserialize(decoder: Decoder): Instant = parseTimestamp(decoder.decodeString())

    override fun serialize(encoder: Encoder, value: Instant) {
        encoder.encodeString(DateTimeFormatter.ISO_INSTANT.format(value))
    }
}

/** Parses an RFC 3339 time as the server writes it (nanoseconds and offsets accepted). */
public fun parseTimestamp(value: String): Instant =
    OffsetDateTime.parse(value, DateTimeFormatter.ISO_OFFSET_DATE_TIME).toInstant()

/** One page of a list: pass [nextCursor] back as `cursor` until it is null. */
public data class Page<T>(val data: List<T>, val nextCursor: String?)

@Serializable
internal data class PageDto<T>(
    val data: List<T>,
    @SerialName("next_cursor") val nextCursor: String? = null,
)

@Serializable
internal data class DataDto<T>(val data: List<T>)

@Serializable
public data class Provider(
    val slug: String,
    val kinds: List<String> = emptyList(),
)

@Serializable
public data class User(
    val id: String,
    @SerialName("external_ref") val externalRef: String? = null,
    val status: String,
    @SerialName("created_at") @Serializable(with = InstantSerializer::class) val createdAt: Instant,
)

@Serializable
public data class DeletionJob(
    @SerialName("job_id") val jobId: String,
    @SerialName("user_id") val userId: String,
    /** pending, running, done or failed. */
    val status: String,
    @SerialName("created_at") @Serializable(with = InstantSerializer::class) val createdAt: Instant,
    @SerialName("finished_at") @Serializable(with = InstantSerializer::class) val finishedAt: Instant? = null,
)

/** A user token, shown only once: keep it in memory, never in a log. */
@Serializable
public data class UserToken(
    val token: String,
    @SerialName("expires_at") @Serializable(with = InstantSerializer::class) val expiresAt: Instant,
    val scopes: List<String>,
) {
    override fun toString(): String = "UserToken(token=<redacted>, expiresAt=$expiresAt, scopes=$scopes)"
}

@Serializable
public data class Connection(
    val provider: String,
    /** active, needs_reauth or disconnected. */
    val status: String,
    val scopes: List<String> = emptyList(),
    @SerialName("last_sync_at") @Serializable(with = InstantSerializer::class) val lastSyncAt: Instant? = null,
    @SerialName("initial_backfill_at") @Serializable(with = InstantSerializer::class)
    val initialBackfillAt: Instant? = null,
    @SerialName("created_at") @Serializable(with = InstantSerializer::class) val createdAt: Instant,
    @SerialName("updated_at") @Serializable(with = InstantSerializer::class) val updatedAt: Instant? = null,
)

@Serializable
public data class Freshness(
    @SerialName("as_of") @Serializable(with = InstantSerializer::class) val asOf: Instant,
    val data: List<SourceFreshness>,
)

@Serializable
public data class SourceFreshness(
    val provider: String,
    /** oauth or device. */
    val kind: String,
    val status: String,
    /** fresh, stale, failing, syncing, needs_reauth or disconnected. */
    val state: String,
    @SerialName("synced_through") @Serializable(with = InstantSerializer::class) val syncedThrough: Instant? = null,
    @SerialName("lag_seconds") val lagSeconds: Long? = null,
    @SerialName("last_sync_at") @Serializable(with = InstantSerializer::class) val lastSyncAt: Instant? = null,
    @SerialName("last_event_at") @Serializable(with = InstantSerializer::class) val lastEventAt: Instant? = null,
    @SerialName("push_healthy") val pushHealthy: Boolean,
    @SerialName("pull_every_seconds") val pullEverySeconds: Long,
    @SerialName("next_pull_at") @Serializable(with = InstantSerializer::class) val nextPullAt: Instant? = null,
    @SerialName("last_attempt_at") @Serializable(with = InstantSerializer::class) val lastAttemptAt: Instant? = null,
    @SerialName("last_error") val lastError: String? = null,
    @SerialName("consecutive_failures") val consecutiveFailures: Int,
)

@Serializable
public data class Sample(
    val id: Long,
    val type: String,
    val value: Double,
    val unit: String? = null,
    @SerialName("start_date") @Serializable(with = InstantSerializer::class) val startDate: Instant,
    @SerialName("end_date") @Serializable(with = InstantSerializer::class) val endDate: Instant? = null,
    @SerialName("source_name") val sourceName: String? = null,
    @SerialName("source_bundle_id") val sourceBundleId: String? = null,
    @SerialName("device_model") val deviceModel: String? = null,
    @SerialName("product_type") val productType: String? = null,
    @SerialName("external_id") val externalId: String? = null,
    @SerialName("is_primary_source") val isPrimarySource: Boolean? = null,
    @SerialName("was_user_entered") val wasUserEntered: Boolean? = null,
    val metadata: JsonObject = JsonObject(emptyMap()),
    val provider: String,
    val source: String? = null,
    val device: String? = null,
    @SerialName("ingested_at") @Serializable(with = InstantSerializer::class) val ingestedAt: Instant,
)

@Serializable
public data class CategorySample(
    val id: Long,
    val type: String,
    @SerialName("value_int") val valueInt: Long? = null,
    @SerialName("value_label") val valueLabel: String? = null,
    @SerialName("start_date") @Serializable(with = InstantSerializer::class) val startDate: Instant,
    @SerialName("end_date") @Serializable(with = InstantSerializer::class) val endDate: Instant? = null,
    @SerialName("source_name") val sourceName: String? = null,
    @SerialName("external_id") val externalId: String? = null,
    val metadata: JsonObject = JsonObject(emptyMap()),
    val provider: String,
    val source: String? = null,
    val device: String? = null,
    @SerialName("ingested_at") @Serializable(with = InstantSerializer::class) val ingestedAt: Instant,
)

@Serializable
public data class SleepSession(
    val id: Long,
    @SerialName("bedtime_start") @Serializable(with = InstantSerializer::class) val bedtimeStart: Instant,
    @SerialName("bedtime_end") @Serializable(with = InstantSerializer::class) val bedtimeEnd: Instant? = null,
    @SerialName("total_sleep_seconds") val totalSleepSeconds: Long? = null,
    @SerialName("deep_sleep_seconds") val deepSleepSeconds: Long? = null,
    @SerialName("rem_sleep_seconds") val remSleepSeconds: Long? = null,
    @SerialName("light_sleep_seconds") val lightSleepSeconds: Long? = null,
    @SerialName("awake_seconds") val awakeSeconds: Long? = null,
    @SerialName("sleep_score") val sleepScore: Double? = null,
    @SerialName("hrv_avg_ms") val hrvAvgMs: Double? = null,
    @SerialName("resting_hr") val restingHr: Double? = null,
    @SerialName("respiratory_rate") val respiratoryRate: Double? = null,
    @SerialName("spo2_avg") val spo2Avg: Double? = null,
    @SerialName("external_id") val externalId: String? = null,
    val metadata: JsonObject = JsonObject(emptyMap()),
    val provider: String,
    val source: String? = null,
    val device: String? = null,
    @SerialName("ingested_at") @Serializable(with = InstantSerializer::class) val ingestedAt: Instant,
)

@Serializable
public data class SleepPhase(
    val id: Long,
    @SerialName("session_id") val sessionId: Long,
    /** deep, rem, light or awake. */
    val phase: String,
    @SerialName("start_date") @Serializable(with = InstantSerializer::class) val startDate: Instant,
    @SerialName("end_date") @Serializable(with = InstantSerializer::class) val endDate: Instant? = null,
    @SerialName("duration_s") val durationS: Long,
    val provider: String,
    val source: String? = null,
    val device: String? = null,
    @SerialName("ingested_at") @Serializable(with = InstantSerializer::class) val ingestedAt: Instant,
)

@Serializable
public data class Workout(
    val id: Long,
    @SerialName("activity_type") val activityType: String,
    @SerialName("start_date") @Serializable(with = InstantSerializer::class) val startDate: Instant,
    @SerialName("end_date") @Serializable(with = InstantSerializer::class) val endDate: Instant? = null,
    @SerialName("duration_s") val durationS: Long? = null,
    @SerialName("active_energy_kcal") val activeEnergyKcal: Double? = null,
    @SerialName("distance_m") val distanceM: Double? = null,
    @SerialName("avg_heart_rate") val avgHeartRate: Double? = null,
    @SerialName("max_heart_rate") val maxHeartRate: Double? = null,
    @SerialName("avg_speed_mps") val avgSpeedMps: Double? = null,
    @SerialName("elevation_gain_m") val elevationGainM: Double? = null,
    @SerialName("vo2_max") val vo2Max: Double? = null,
    @SerialName("external_id") val externalId: String? = null,
    val metadata: JsonObject = JsonObject(emptyMap()),
    val provider: String,
    val source: String? = null,
    val device: String? = null,
    @SerialName("ingested_at") @Serializable(with = InstantSerializer::class) val ingestedAt: Instant,
)

@Serializable
public data class RoutePoint(
    val id: Long,
    @SerialName("workout_id") val workoutId: Long,
    @Serializable(with = InstantSerializer::class) val timestamp: Instant,
    val latitude: Double,
    val longitude: Double,
    @SerialName("altitude_m") val altitudeM: Double? = null,
    @SerialName("speed_mps") val speedMps: Double? = null,
    @SerialName("course_deg") val courseDeg: Double? = null,
    val provider: String,
    val source: String? = null,
    val device: String? = null,
    @SerialName("ingested_at") @Serializable(with = InstantSerializer::class) val ingestedAt: Instant,
)

@Serializable
public data class NutritionLog(
    val id: Long,
    @SerialName("start_date") @Serializable(with = InstantSerializer::class) val startDate: Instant,
    @SerialName("end_date") @Serializable(with = InstantSerializer::class) val endDate: Instant? = null,
    @SerialName("meal_type") val mealType: String? = null,
    @SerialName("energy_kcal") val energyKcal: Double? = null,
    @SerialName("protein_g") val proteinG: Double? = null,
    @SerialName("carbs_g") val carbsG: Double? = null,
    @SerialName("fat_g") val fatG: Double? = null,
    @SerialName("fiber_g") val fiberG: Double? = null,
    @SerialName("sugar_g") val sugarG: Double? = null,
    @SerialName("sodium_mg") val sodiumMg: Double? = null,
    val nutrients: JsonObject? = null,
    @SerialName("external_id") val externalId: String? = null,
    val metadata: JsonObject = JsonObject(emptyMap()),
    val provider: String,
    val source: String? = null,
    val device: String? = null,
    @SerialName("ingested_at") @Serializable(with = InstantSerializer::class) val ingestedAt: Instant,
)

@Serializable
public data class DailySummary(
    val id: Long,
    /** The user's local calendar day, YYYY-MM-DD. */
    val date: String,
    @SerialName("readiness_score") val readinessScore: Double? = null,
    @SerialName("activity_score") val activityScore: Double? = null,
    @SerialName("sleep_score") val sleepScore: Double? = null,
    @SerialName("recovery_score") val recoveryScore: Double? = null,
    @SerialName("strain_score") val strainScore: Double? = null,
    @SerialName("step_count") val stepCount: Long? = null,
    @SerialName("active_kcal") val activeKcal: Double? = null,
    @SerialName("total_kcal") val totalKcal: Double? = null,
    @SerialName("distance_m") val distanceM: Double? = null,
    @SerialName("floors_climbed") val floorsClimbed: Long? = null,
    @SerialName("resting_hr") val restingHr: Double? = null,
    @SerialName("hrv_avg_ms") val hrvAvgMs: Double? = null,
    val metadata: JsonObject = JsonObject(emptyMap()),
    val provider: String,
    val source: String? = null,
    val device: String? = null,
    @SerialName("ingested_at") @Serializable(with = InstantSerializer::class) val ingestedAt: Instant,
)

@Serializable
public data class Coverage(
    @SerialName("pending_jobs") val pendingJobs: Int,
    @SerialName("parked_jobs") val parkedJobs: Int,
    @SerialName("rejected_records") val rejectedRecords: Int,
    val truncated: Boolean,
    val days: List<CoverageDay>,
)

@Serializable
public data class CoverageDay(
    val provider: String,
    /** Local calendar day, YYYY-MM-DD. */
    val date: String,
    val records: Long,
    val metrics: Map<String, Long> = emptyMap(),
    @SerialName("last_synced_at") @Serializable(with = InstantSerializer::class) val lastSyncedAt: Instant,
)

/** The answer to an upload: the raw object key, and whether these bytes were already stored. */
@Serializable
public data class UploadResult(val key: String, val duplicate: Boolean)

/**
 * A FHIR R4 searchset Bundle, kept as JSON. [observations] are the entries' resources and
 * [nextCursor] the cursor of the `next` link, null on the last page.
 */
public data class FhirBundle(
    val raw: JsonObject,
    val observations: List<JsonObject>,
    val nextCursor: String?,
)
