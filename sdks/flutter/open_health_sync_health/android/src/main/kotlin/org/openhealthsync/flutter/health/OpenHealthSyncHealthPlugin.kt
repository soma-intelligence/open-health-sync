// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.flutter.health

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import androidx.activity.result.contract.ActivityResultContract
import androidx.health.connect.client.permission.HealthPermission
import io.flutter.embedding.engine.plugins.FlutterPlugin
import io.flutter.embedding.engine.plugins.activity.ActivityAware
import io.flutter.embedding.engine.plugins.activity.ActivityPluginBinding
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel
import io.flutter.plugin.common.PluginRegistry
import java.time.Duration
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancelChildren
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.openhealthsync.sdk.healthconnect.HealthConnectAvailability
import org.openhealthsync.sdk.healthconnect.HealthConnectRecordType
import org.openhealthsync.sdk.healthconnect.HealthConnectSync

/**
 * The Android side of the plugin: every call of the Dart `OpenHealthSyncHealth` is passed to
 * the Kotlin SDK's [HealthConnectSync], which reads Health Connect, maps the records and
 * uploads. Nothing is read or mapped here.
 */
public class OpenHealthSyncHealthPlugin :
    FlutterPlugin,
    MethodChannel.MethodCallHandler,
    EventChannel.StreamHandler,
    ActivityAware,
    PluginRegistry.ActivityResultListener {

    private var context: Context? = null
    private var methods: MethodChannel? = null
    private var events: EventChannel? = null
    private var activityBinding: ActivityPluginBinding? = null
    private var authorization: PendingAuthorization? = null
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)

    /** A permission screen that is open, and the call waiting for its answer. */
    private class PendingAuthorization(
        val result: MethodChannel.Result,
        val contract: ActivityResultContract<Set<String>, Set<String>>,
        val types: Set<HealthConnectRecordType>,
        val notDeclared: List<String>,
        val background: Boolean,
        val history: Boolean,
    )

    // FlutterPlugin

    override fun onAttachedToEngine(binding: FlutterPlugin.FlutterPluginBinding) {
        context = binding.applicationContext
        // Already done at process start by BackgroundInitializer, unless the app removed
        // the androidx.startup provider from its manifest.
        if (HealthConnectSync.backgroundFactory == null) HealthSyncBridge.install()
        methods = MethodChannel(binding.binaryMessenger, METHOD_CHANNEL).also { it.setMethodCallHandler(this) }
        events = EventChannel(binding.binaryMessenger, EVENT_CHANNEL).also { it.setStreamHandler(this) }
    }

    override fun onDetachedFromEngine(binding: FlutterPlugin.FlutterPluginBinding) {
        methods?.setMethodCallHandler(null)
        events?.setStreamHandler(null)
        methods = null
        events = null
        HealthSyncBridge.detach()
        // A sync cut here is safe: the SDK saves a changes token only after its parts are queued.
        scope.coroutineContext.cancelChildren()
        context = null
    }

    // Background results

    override fun onListen(arguments: Any?, events: EventChannel.EventSink) {
        HealthSyncBridge.attach(events)
    }

    override fun onCancel(arguments: Any?) {
        HealthSyncBridge.detach()
    }

    // Calls

    override fun onMethodCall(call: MethodCall, result: MethodChannel.Result) {
        val app = context
        if (app == null) {
            result.error("unavailable", "the plugin is not attached to an engine", null)
            return
        }
        when (call.method) {
            "availability" -> result.success(HealthSyncBridge.availabilityName(app))
            "setCredentials" -> reply(result) {
                val baseUrl = call.argument<String>("baseUrl").orEmpty()
                val token = call.argument<String>("userToken").orEmpty()
                if (!(baseUrl.startsWith("https://") || baseUrl.startsWith("http://")) || token.isEmpty()) {
                    throw BridgeException("invalid_argument", "baseUrl must be an http or https URL and userToken not empty")
                }
                CredentialStore(app).save(baseUrl, token)
                null
            }
            "clearCredentials" -> reply(result) {
                CredentialStore(app).clear()
                null
            }
            "requestAuthorization" -> requestAuthorization(app, call, result)
            "sync" -> reply(result) {
                val types = types(call)
                HealthSyncBridge.reportMap(HealthSyncBridge.requireSync(app).sync(types))
            }
            "flush" -> reply(result) {
                val force = call.argument<Boolean>("force") ?: false
                HealthSyncBridge.reportMap(HealthSyncBridge.requireSync(app).flush(force))
            }
            "pendingParts" -> reply(result) { HealthSyncBridge.pendingParts(app) }
            "enableBackgroundSync" -> reply(result) {
                val types = types(call)
                val seconds = call.argument<Number>("intervalSeconds")?.toLong() ?: 3600L
                val unmetered = call.argument<Boolean>("requireUnmeteredNetwork") ?: false
                // Checked now, so a schedule that could never upload is refused.
                HealthSyncBridge.requireSync(app)
                HealthConnectSync.enableBackgroundDelivery(app, types, Duration.ofSeconds(seconds), unmetered)
                null
            }
            "disableBackgroundSync" -> reply(result) {
                HealthConnectSync.stopObserving(app)
                null
            }
            else -> result.notImplemented()
        }
    }

    /** Runs [block] off the main thread and answers [result] on it. */
    private fun reply(result: MethodChannel.Result, block: suspend () -> Any?) {
        scope.launch {
            try {
                result.success(withContext(Dispatchers.Default) { block() })
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                val error = HealthSyncBridge.errorMap(e)
                result.error(error["code"] as String, error["message"] as String?, error)
            }
        }
    }

    /** The record types of a call; every type the SDK reads when the call names none. */
    private fun types(call: MethodCall): Set<HealthConnectRecordType> {
        val names = call.argument<List<String>>("types") ?: return HealthConnectSync.allSupportedTypes
        val types = names.mapNotNull(HealthConnectRecordType::fromWireName).toSet()
        if (types.isEmpty()) throw BridgeException("invalid_argument", "no Health Connect record type in types")
        return types
    }

    // Authorization

    private fun requestAuthorization(app: Context, call: MethodCall, result: MethodChannel.Result) {
        if (HealthConnectSync.availability(app) != HealthConnectAvailability.AVAILABLE) {
            result.error("unavailable", "Health Connect is not available on this device", null)
            return
        }
        val activity = activityBinding?.activity
        if (activity == null) {
            result.error("no_activity", "the permission screen needs the app to be in the foreground", null)
            return
        }
        if (authorization != null) {
            result.error("authorization_in_progress", "a permission screen is already open", null)
            return
        }
        val types = try {
            types(call)
        } catch (e: BridgeException) {
            result.error(e.code, e.message, null)
            return
        }
        val background = call.argument<Boolean>("background") ?: false
        val history = call.argument<Boolean>("history") ?: false

        // Health Connect only offers the permissions the app's manifest declares.
        val declared = declaredPermissions(app)
        val ask = HealthConnectSync.authorizationPermissions(types, background, history).filterTo(LinkedHashSet()) { it in declared }
        val notDeclared = types.filter { it.readPermission !in declared }.map { it.wireName }
        if (ask.isEmpty()) {
            result.success(answer(types, emptySet(), notDeclared, background, history))
            return
        }
        val contract = HealthConnectSync.requestAuthorizationContract()
        authorization = PendingAuthorization(result, contract, types, notDeclared, background, history)
        try {
            activity.startActivityForResult(contract.createIntent(activity, ask), REQUEST_CODE)
        } catch (e: ActivityNotFoundException) {
            authorization = null
            result.error("unavailable", "Health Connect has no permission screen on this device", null)
        }
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?): Boolean {
        if (requestCode != REQUEST_CODE) return false
        val pending = authorization ?: return true
        authorization = null
        val granted = pending.contract.parseResult(resultCode, data)
        pending.result.success(answer(pending.types, granted, pending.notDeclared, pending.background, pending.history))
        return true
    }

    private fun answer(
        types: Set<HealthConnectRecordType>,
        granted: Set<String>,
        notDeclared: List<String>,
        background: Boolean,
        history: Boolean,
    ): Map<String, Any?> = mapOf(
        "granted" to types.filter { it.readPermission in granted }.map { it.wireName },
        "notDeclared" to notDeclared,
        "backgroundGranted" to if (background) HealthPermission.PERMISSION_READ_HEALTH_DATA_IN_BACKGROUND in granted else null,
        "historyGranted" to if (history) HealthPermission.PERMISSION_READ_HEALTH_DATA_HISTORY in granted else null,
    )

    private fun declaredPermissions(app: Context): Set<String> {
        val info = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            app.packageManager.getPackageInfo(
                app.packageName,
                PackageManager.PackageInfoFlags.of(PackageManager.GET_PERMISSIONS.toLong()),
            )
        } else {
            @Suppress("DEPRECATION")
            app.packageManager.getPackageInfo(app.packageName, PackageManager.GET_PERMISSIONS)
        }
        return info.requestedPermissions?.toSet().orEmpty()
    }

    // ActivityAware

    override fun onAttachedToActivity(binding: ActivityPluginBinding) {
        activityBinding = binding
        binding.addActivityResultListener(this)
    }

    override fun onReattachedToActivityForConfigChanges(binding: ActivityPluginBinding) {
        onAttachedToActivity(binding)
    }

    override fun onDetachedFromActivityForConfigChanges() {
        activityBinding?.removeActivityResultListener(this)
        activityBinding = null
    }

    override fun onDetachedFromActivity() {
        onDetachedFromActivityForConfigChanges()
        authorization?.result?.error("no_activity", "the activity was closed before the user answered", null)
        authorization = null
    }

    private companion object {
        const val METHOD_CHANNEL = "org.openhealthsync/health"
        const val EVENT_CHANNEL = "org.openhealthsync/health/background"

        /** The request code of the Health Connect permission screen. */
        const val REQUEST_CODE = 0x4F48
    }
}
