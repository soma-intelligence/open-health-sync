// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.flutter.health

import android.content.Context
import android.os.Handler
import android.os.Looper
import io.flutter.plugin.common.EventChannel
import java.io.File
import java.io.IOException
import org.openhealthsync.sdk.OhsApiException
import org.openhealthsync.sdk.OhsClient
import org.openhealthsync.sdk.healthconnect.HealthConnectAvailability
import org.openhealthsync.sdk.healthconnect.HealthConnectSync
import org.openhealthsync.sdk.healthconnect.HealthConnectSyncFactory
import org.openhealthsync.sdk.healthconnect.SyncReport
import org.openhealthsync.sdk.healthconnect.UploadQueue

/** An error the Dart side gets as a `HealthSyncException` with this [code]. */
internal class BridgeException(val code: String, message: String) : Exception(message)

/** No user token is stored. An `IOException`, so the SDK keeps the part queued. */
internal class MissingTokenException : IOException("no upload token is stored")

/**
 * What the plugin does with the Kotlin SDK outside of the channel itself: building the sync
 * from the stored credentials, turning its reports into channel values, and passing the
 * results of background runs to the Dart side.
 */
internal object HealthSyncBridge {
    private const val USER_AGENT = "open-health-sync-flutter/0.1.0 (android)"

    /** How many background results are kept while the Dart side does not listen. */
    private const val MAX_BUFFERED = 20

    private val main = Handler(Looper.getMainLooper())

    // The two below are only touched on the main thread.
    private var sink: EventChannel.EventSink? = null
    private val buffered = ArrayDeque<Map<String, Any?>>()

    /** Tells the Kotlin SDK how a background run builds its sync and where its result goes. */
    fun install() {
        HealthConnectSync.backgroundFactory = HealthConnectSyncFactory { context -> createSync(context) }
        HealthConnectSync.onBackgroundSync = { result -> publish(resultMap(result)) }
    }

    fun availabilityName(context: Context): String = when (HealthConnectSync.availability(context)) {
        HealthConnectAvailability.AVAILABLE -> "available"
        HealthConnectAvailability.PROVIDER_UPDATE_REQUIRED -> "updateRequired"
        HealthConnectAvailability.UNAVAILABLE -> "unavailable"
    }

    /** The sync of this device with the stored credentials; null when there are none or no Health Connect. */
    fun createSync(context: Context): HealthConnectSync? {
        val store = CredentialStore(context)
        val baseUrl = store.baseUrl ?: return null
        if (store.token() == null) return null
        if (HealthConnectSync.availability(context) != HealthConnectAvailability.AVAILABLE) return null
        // The token is read again before each request, so a token stored later is used at once.
        val client = OhsClient(
            baseUrl = baseUrl,
            tokenProvider = { store.token() ?: throw MissingTokenException() },
            userAgent = USER_AGENT,
        )
        return HealthConnectSync.create(context, client)
    }

    /** [createSync], or the error that says what is missing. */
    fun requireSync(context: Context): HealthConnectSync {
        if (HealthConnectSync.availability(context) != HealthConnectAvailability.AVAILABLE) {
            throw BridgeException("unavailable", "Health Connect is not available on this device")
        }
        return createSync(context) ?: throw BridgeException("not_configured", "call setCredentials first")
    }

    /** How many parts wait in the SDK's queue on disk. Needs no credentials. */
    fun pendingParts(context: Context): Int =
        UploadQueue(File(HealthConnectSync.defaultDirectory(context.applicationContext), "queue")).size

    fun reportMap(report: SyncReport): Map<String, Any?> = mapOf(
        "batchIds" to report.batchIds,
        "partsUploaded" to report.partsUploaded,
        "duplicateParts" to report.duplicateParts,
        "partsPending" to report.partsPending,
        "partsDropped" to report.partsDropped,
        "linesByType" to report.linesByType,
        "deletionsByType" to report.deletionsByType,
        "deletionsPendingByType" to emptyMap<String, Int>(),
        "skippedRecords" to report.skippedRecords,
        "typesWithoutPermission" to report.typesWithoutPermission.map { it.wireName },
        "expiredTokenTypes" to report.expiredTokenTypes.map { it.wireName },
        "uploadError" to report.uploadError?.let(::errorMap),
    )

    /** An error as the Dart side reads it. The text is the SDK's or the platform's, never a token or a record. */
    fun errorMap(error: Throwable): Map<String, Any?> = when (error) {
        is BridgeException -> mapOf("code" to error.code, "message" to error.message)
        is MissingTokenException -> mapOf("code" to "not_configured", "message" to error.message)
        is OhsApiException -> mapOf(
            "code" to "api_error",
            "message" to error.message,
            "status" to error.status,
            "requestId" to error.requestId,
        )
        is SecurityException -> mapOf("code" to "permission_denied", "message" to error.message)
        is IOException -> mapOf("code" to "network_error", "message" to (error.message ?: error.javaClass.simpleName))
        is IllegalArgumentException -> mapOf("code" to "invalid_argument", "message" to error.message)
        else -> mapOf("code" to "health_store_error", "message" to (error.message ?: error.javaClass.simpleName))
    }

    private fun resultMap(result: Result<SyncReport>): Map<String, Any?> {
        val at = System.currentTimeMillis()
        return result.fold(
            onSuccess = { mapOf("at" to at, "report" to reportMap(it)) },
            onFailure = { mapOf("at" to at, "error" to errorMap(it)) },
        )
    }

    /** Sends [event] to the Dart side, or keeps it until it listens. */
    private fun publish(event: Map<String, Any?>) {
        main.post {
            val current = sink
            if (current != null) {
                current.success(event)
            } else {
                buffered.addLast(event)
                while (buffered.size > MAX_BUFFERED) buffered.removeFirst()
            }
        }
    }

    /** The Dart side listens: it gets what was kept, then every new result. Main thread. */
    fun attach(events: EventChannel.EventSink) {
        sink = events
        while (buffered.isNotEmpty()) events.success(buffered.removeFirst())
    }

    /** The Dart side stopped listening. Main thread. */
    fun detach() {
        sink = null
    }
}
