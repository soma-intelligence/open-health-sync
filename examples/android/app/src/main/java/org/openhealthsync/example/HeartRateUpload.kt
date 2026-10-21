// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.example

import java.io.IOException
import java.time.Instant
import java.time.ZoneId
import java.time.temporal.ChronoUnit
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.openhealthsync.sdk.AppleHealthPartBuilder
import org.openhealthsync.sdk.OhsApiException
import org.openhealthsync.sdk.OhsClient

/** What the server said about one uploaded part. */
data class PartOutcome(
    val partNumber: Int,
    val type: String,
    val day: String,
    val duplicate: Boolean,
)

/** The state of the upload screen. */
sealed interface UploadState {
    data object Idle : UploadState
    data object Running : UploadState
    data class Done(val parts: List<PartOutcome>) : UploadState
    data class Failed(
        val message: String,
        val status: Int? = null,
        val title: String? = null,
        val requestId: String? = null,
    ) : UploadState
}

private const val HEART_RATE = "HKQuantityTypeIdentifierHeartRate"

/** Accepted range for the manual entry, in beats per minute. */
val BPM_RANGE = 20..300

/**
 * Builds one heart rate line stamped now, in the device's time zone, and uploads it with the
 * user token. Never throws for an API or network failure: it returns [UploadState.Failed].
 * Nothing here logs the token or the value.
 */
suspend fun uploadHeartRate(serverUrl: String, token: String, bpm: Int): UploadState {
    val now = Instant.now().truncatedTo(ChronoUnit.SECONDS)
    val zone = ZoneId.systemDefault()

    val builder = AppleHealthPartBuilder(AppleHealthPartBuilder.newBatchId("android"))
    builder.add(
        type = HEART_RATE,
        day = AppleHealthPartBuilder.localDay(now, zone),
        line = buildJsonObject {
            put("quantityType", HEART_RATE)
            put("startDate", now.toString())
            put("endDate", now.toString())
            put("value", bpm)
            put("unit", "count/min")
            put("sourceName", "Android example")
            put("metadata", buildJsonObject { put("HKTimeZone", zone.id) })
        },
    )
    val parts = builder.build()

    return uploadState {
        OhsClient(serverUrl, token).uploadParts(parts).let { results ->
            parts.zip(results) { part, result -> PartOutcome(part.partNumber, part.type, part.day, result.duplicate) }
        }
    }
}

/**
 * Runs an upload and returns what the screen shows: [UploadState.Done], or [UploadState.Failed]
 * for an API, network or settings error.
 */
suspend fun uploadState(upload: suspend () -> List<PartOutcome>): UploadState =
    try {
        UploadState.Done(upload())
    } catch (e: OhsApiException) {
        UploadState.Failed(
            message = "The server refused the upload.",
            status = e.status,
            title = e.title,
            requestId = e.requestId,
        )
    } catch (e: IOException) {
        UploadState.Failed("Network error (${e.javaClass.simpleName}). Is the server running and the URL right?")
    } catch (e: IllegalArgumentException) {
        UploadState.Failed(e.message ?: "Invalid settings.")
    }
