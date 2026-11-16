// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.reactnative

import androidx.activity.ComponentActivity
import androidx.activity.result.ActivityResultLauncher
import androidx.health.connect.client.permission.HealthPermission
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReadableArray
import com.facebook.react.bridge.UiThreadUtil
import java.time.Duration
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import org.json.JSONArray
import org.json.JSONObject
import org.openhealthsync.sdk.healthconnect.HealthConnectAvailability
import org.openhealthsync.sdk.healthconnect.HealthConnectRecordType
import org.openhealthsync.sdk.healthconnect.HealthConnectSync

/**
 * The Turbo Native Module `OpenHealthSync` on Android. Every call goes to the Kotlin SDK's
 * [HealthConnectSync]: this class only turns JavaScript arguments into its types and its
 * results into JSON (src/health/report.ts reads it).
 */
class OpenHealthSyncModule(reactContext: ReactApplicationContext) : NativeOpenHealthSyncSpec(reactContext) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private val store = CredentialStore(reactContext)
    private val authorizing = AtomicBoolean(false)

    init {
        HealthSyncRuntime.install(reactContext)
        HealthSyncRuntime.listener = { json ->
            emitOnBackgroundSync(Arguments.createMap().apply { putString("json", json) })
        }
    }

    override fun invalidate() {
        HealthSyncRuntime.listener = null
        scope.cancel()
        super.invalidate()
    }

    override fun availability(promise: Promise) {
        promise.resolve(
            when (HealthConnectSync.availability(reactApplicationContext)) {
                HealthConnectAvailability.AVAILABLE -> "available"
                HealthConnectAvailability.PROVIDER_UPDATE_REQUIRED -> "providerUpdateRequired"
                HealthConnectAvailability.UNAVAILABLE -> "unavailable"
            },
        )
    }

    override fun configure(baseUrl: String, userToken: String, promise: Promise) {
        if (!(baseUrl.startsWith("http://") || baseUrl.startsWith("https://")) || userToken.isEmpty()) {
            promise.reject("invalid_argument", "baseUrl must be an http or https URL and userToken must not be empty")
            return
        }
        try {
            store.save(baseUrl, userToken)
            promise.resolve(null)
        } catch (e: Exception) {
            promise.reject("storage", "the Android Keystore could not keep the credentials (${e.javaClass.simpleName})")
        }
    }

    override fun clearCredentials(promise: Promise) {
        run(promise) {
            HealthConnectSync.stopObserving(reactApplicationContext)
            // What was read for this user must not go up under the next one's token, and the
            // next user starts from a full backfill.
            HealthSyncRuntime.createSync(reactApplicationContext)?.let { sync ->
                sync.queue.clear()
                HealthConnectSync.allSupportedTypes.forEach(sync::resetAnchor)
            }
            store.clear()
            store.clearLastBackgroundSync()
            null
        }
    }

    override fun isConfigured(promise: Promise) {
        promise.resolve(store.isConfigured)
    }

    override fun requestAuthorization(types: ReadableArray, background: Boolean, history: Boolean, promise: Promise) {
        val context = reactApplicationContext
        val recordTypes = recordTypes(types, promise) ?: return
        if (HealthConnectSync.availability(context) != HealthConnectAvailability.AVAILABLE) {
            promise.reject("unavailable", UNAVAILABLE)
            return
        }
        val activity = context.currentActivity as? ComponentActivity
        if (activity == null) {
            promise.reject("no_activity", "no activity is in front to show the Health Connect permission screen")
            return
        }
        // Asking for a permission this device's Health Connect does not have would fail the request.
        val askBackground = background && HealthConnectSync.isBackgroundReadAvailable(context)
        val askHistory = history && HealthConnectSync.isHistoryReadAvailable(context)
        val permissions = HealthConnectSync.authorizationPermissions(recordTypes, askBackground, askHistory)
        if (!authorizing.compareAndSet(false, true)) {
            promise.reject("in_progress", "a permission request is already on screen")
            return
        }
        UiThreadUtil.runOnUiThread {
            var launcher: ActivityResultLauncher<Set<String>>? = null
            try {
                launcher = activity.activityResultRegistry.register(
                    AUTHORIZATION_KEY,
                    HealthConnectSync.requestAuthorizationContract(),
                ) { granted ->
                    launcher?.unregister()
                    authorizing.set(false)
                    val json = JSONObject()
                        .put("granted", JSONArray(recordTypes.filter { it.readPermission in granted }.map { it.wireName }))
                        .put("background", if (background) HealthPermission.PERMISSION_READ_HEALTH_DATA_IN_BACKGROUND in granted else JSONObject.NULL)
                        .put("history", if (history) HealthPermission.PERMISSION_READ_HEALTH_DATA_HISTORY in granted else JSONObject.NULL)
                    promise.resolve(json.toString())
                }
                launcher.launch(permissions)
            } catch (e: Exception) {
                launcher?.unregister()
                authorizing.set(false)
                promise.reject("health_store", "the Health Connect permission screen could not be shown (${e.javaClass.simpleName})")
            }
        }
    }

    override fun sync(types: ReadableArray, promise: Promise) {
        val recordTypes = recordTypes(types, promise) ?: return
        run(promise) { HealthSyncRuntime.reportJson(sync().sync(recordTypes)).toString() }
    }

    override fun flush(force: Boolean, promise: Promise) {
        run(promise) { HealthSyncRuntime.reportJson(sync().flush(force)).toString() }
    }

    override fun enableBackgroundSync(
        types: ReadableArray,
        intervalMinutes: Double,
        requireUnmeteredNetwork: Boolean,
        frequency: String,
        promise: Promise,
    ) {
        // `frequency` is the iOS setting; Health Connect has no push, so the job polls.
        val recordTypes = recordTypes(types, promise) ?: return
        run(promise) {
            sync() // rejects when not configured or unavailable
            HealthConnectSync.enableBackgroundDelivery(
                reactApplicationContext,
                recordTypes,
                Duration.ofMinutes(intervalMinutes.toLong().coerceAtLeast(1)),
                requireUnmeteredNetwork,
            )
            null
        }
    }

    override fun disableBackgroundSync(promise: Promise) {
        run(promise) {
            HealthConnectSync.stopObserving(reactApplicationContext)
            null
        }
    }

    override fun pendingParts(promise: Promise) {
        run(promise) { (HealthSyncRuntime.createSync(reactApplicationContext)?.pendingParts ?: 0).toDouble() }
    }

    override fun lastBackgroundSync(promise: Promise) {
        promise.resolve(store.lastBackgroundSync)
    }

    // Helpers

    private class Rejection(val code: String, override val message: String) : Exception(message)

    private fun sync(): HealthConnectSync {
        val context = reactApplicationContext
        if (HealthConnectSync.availability(context) != HealthConnectAvailability.AVAILABLE) throw Rejection("unavailable", UNAVAILABLE)
        return HealthSyncRuntime.createSync(context)
            ?: throw Rejection("not_configured", "call configure with a base URL and a user token first")
    }

    /** Runs [block] off the main thread and settles [promise] with its result or its failure. */
    private fun run(promise: Promise, block: suspend () -> Any?) {
        scope.launch {
            try {
                promise.resolve(block())
            } catch (e: CancellationException) {
                throw e
            } catch (e: Rejection) {
                promise.reject(e.code, e.message)
            } catch (e: Exception) {
                val (code, message) = HealthSyncRuntime.failure(e)
                promise.reject(code, message)
            }
        }
    }

    /** The record types of the names JavaScript sent; rejects [promise] and returns null on a bad name. */
    private fun recordTypes(names: ReadableArray, promise: Promise): Set<HealthConnectRecordType>? {
        val out = LinkedHashSet<HealthConnectRecordType>()
        for (i in 0 until names.size()) {
            val name = names.getString(i)
            if (name == EVERY_TYPE) return HealthConnectSync.allSupportedTypes
            val type = name?.let(HealthConnectRecordType::fromWireName)
            if (type == null) {
                promise.reject("invalid_argument", "unknown Health Connect record type")
                return null
            }
            out += type
        }
        if (out.isEmpty()) {
            promise.reject("invalid_argument", "types must not be empty")
            return null
        }
        return out
    }

    companion object {
        const val NAME = NativeOpenHealthSyncSpec.NAME

        private const val EVERY_TYPE = "*"
        private const val AUTHORIZATION_KEY = "org.openhealthsync.reactnative.authorization"
        private const val UNAVAILABLE = "Health Connect is not available on this device"
    }
}
