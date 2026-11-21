// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.reactnative

import android.content.Context
import android.os.RemoteException
import java.io.IOException
import java.time.Instant
import org.json.JSONArray
import org.json.JSONObject
import org.openhealthsync.sdk.OhsApiException
import org.openhealthsync.sdk.OhsClient
import org.openhealthsync.sdk.healthconnect.HealthConnectAvailability
import org.openhealthsync.sdk.healthconnect.HealthConnectSync
import org.openhealthsync.sdk.healthconnect.HealthConnectSyncFactory
import org.openhealthsync.sdk.healthconnect.SyncReport

/** No user token is kept: the app has not called `configure`, or cleared its credentials. */
internal class MissingCredentialsException : IOException("no user token is configured")

/**
 * What the module and the background job share: how to build the Kotlin SDK's
 * [HealthConnectSync] from the kept credentials, and how its results become the JSON the
 * JavaScript side reads (src/health/report.ts).
 */
internal object HealthSyncRuntime {
    /** Receives each background result as JSON while a React Native instance is alive. */
    @Volatile
    var listener: ((String) -> Unit)? = null

    /**
     * Tells the Kotlin SDK how to build the sync of a background run and where its result
     * goes. Called at process start by [OpenHealthSyncInitProvider]; calling it again is
     * harmless. An app that set its own factory keeps it.
     */
    fun install(context: Context) {
        val app = context.applicationContext
        if (HealthConnectSync.backgroundFactory == null) {
            HealthConnectSync.backgroundFactory = HealthConnectSyncFactory { createSync(it) }
        }
        HealthConnectSync.onBackgroundSync = { result ->
            val json = backgroundJson(result)
            CredentialStore(app).lastBackgroundSync = json
            listener?.invoke(json)
        }
    }

    /** The sync over the kept credentials; null when there are none or Health Connect is unusable. */
    fun createSync(context: Context): HealthConnectSync? {
        val app = context.applicationContext
        if (HealthConnectSync.availability(app) != HealthConnectAvailability.AVAILABLE) return null
        val store = CredentialStore(app)
        val baseUrl = store.baseUrl ?: return null
        if (store.token() == null) return null
        // The token is read at each request, so a token renewed by `configure` is used at once.
        val client = OhsClient(baseUrl, tokenProvider = { store.token() ?: throw MissingCredentialsException() })
        return HealthConnectSync.create(app, client)
    }

    // JSON

    fun reportJson(report: SyncReport): JSONObject = JSONObject()
        .put("batchIds", JSONArray(report.batchIds))
        .put("partsUploaded", report.partsUploaded)
        .put("duplicateParts", report.duplicateParts)
        .put("partsPending", report.partsPending)
        .put("partsDropped", report.partsDropped)
        .put("linesByType", JSONObject(report.linesByType))
        .put("deletionsByType", JSONObject(report.deletionsByType))
        .put("skippedRecords", report.skippedRecords)
        .put("typesWithoutPermission", JSONArray(report.typesWithoutPermission.map { it.wireName }))
        .put("expiredTokenTypes", JSONArray(report.expiredTokenTypes.map { it.wireName }))
        .put("uploadError", report.uploadError?.let(::uploadErrorJson) ?: JSONObject.NULL)

    private fun uploadErrorJson(error: Exception): JSONObject {
        val json = JSONObject()
        when (error) {
            is OhsApiException -> {
                json.put("code", when {
                    error.status == 401 -> "unauthorized"
                    error.status == 403 -> "forbidden"
                    error.status == 429 -> "rate_limited"
                    error.status >= 500 -> "server"
                    else -> "api"
                })
                // The SDK builds this message from the problem body: no token, no health value.
                json.put("message", error.message.orEmpty())
                json.put("status", error.status)
                error.requestId?.let { json.put("requestId", it) }
            }
            is MissingCredentialsException -> json.put("code", "unauthorized").put("message", error.message.orEmpty())
            is IOException -> json.put("code", "network").put("message", "network error (${error.javaClass.simpleName})")
            else -> json.put("code", "unknown").put("message", error.javaClass.simpleName)
        }
        return json
    }

    /** The code and message a failed call rejects with. The message never quotes the exception's own. */
    fun failure(error: Throwable): Pair<String, String> = when (error) {
        is SecurityException -> "permission_denied" to "Health Connect refused the read: a permission is missing"
        is IOException, is RemoteException -> "health_store" to "Health Connect could not be read (${error.javaClass.simpleName})"
        is IllegalArgumentException -> "invalid_argument" to "invalid argument"
        else -> "unknown" to "health sync failed (${error.javaClass.simpleName})"
    }

    private fun backgroundJson(result: Result<SyncReport>): String {
        val json = JSONObject().put("at", Instant.now().toString())
        result.fold(
            onSuccess = { json.put("ok", true).put("report", reportJson(it)) },
            onFailure = {
                val (code, message) = failure(it)
                json.put("ok", false).put("error", JSONObject().put("code", code).put("message", message))
            },
        )
        return json.toString()
    }
}
