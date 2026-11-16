// SPDX-License-Identifier: AGPL-3.0-only

import Flutter
import Foundation

/// The iOS side of the plugin: every call of the Dart `OpenHealthSyncHealth` is passed to
/// `HealthSyncBridge`, which runs the Swift SDK's `HealthKitSync`.
public class OpenHealthSyncHealthPlugin: NSObject, FlutterPlugin, FlutterStreamHandler {
    static let methodChannel = "org.openhealthsync/health"
    static let eventChannel = "org.openhealthsync/health/background"

    private let bridge = HealthSyncBridge.shared

    public static func register(with registrar: FlutterPluginRegistrar) {
        let instance = OpenHealthSyncHealthPlugin()
        let methods = FlutterMethodChannel(name: methodChannel, binaryMessenger: registrar.messenger())
        registrar.addMethodCallDelegate(instance, channel: methods)
        let events = FlutterEventChannel(name: eventChannel, binaryMessenger: registrar.messenger())
        events.setStreamHandler(instance)
        // Plugins are registered during application(_:didFinishLaunchingWithOptions:), which
        // is when HealthKit wants background delivery turned on again.
        Task {
            await HealthSyncBridge.shared.resumeBackgroundSync()
        }
    }

    // MARK: - Calls

    public func handle(_ call: FlutterMethodCall, result: @escaping FlutterResult) {
        let arguments = call.arguments as? [String: Any] ?? [:]
        let names = arguments["types"] as? [String]
        let bridge = self.bridge
        switch call.method {
        case "availability":
            result(bridge.availabilityName)
        case "setCredentials":
            reply(result) {
                try await bridge.setCredentials(
                    baseURL: arguments["baseUrl"] as? String ?? "",
                    token: arguments["userToken"] as? String ?? "")
                return nil
            }
        case "clearCredentials":
            bridge.clearCredentials()
            result(nil)
        case "requestAuthorization":
            reply(result) { try await bridge.requestAuthorization(names) }
        case "sync":
            reply(result) { try await bridge.sync(names) }
        case "flush":
            // The Swift SDK keeps no queue: a failed upload leaves the anchor where it was.
            result(HealthSyncBridge.emptyReport)
        case "pendingParts":
            result(0)
        case "enableBackgroundSync":
            let interval = (arguments["intervalSeconds"] as? NSNumber)?.intValue ?? 3600
            reply(result) {
                try await bridge.enableBackgroundSync(names, intervalSeconds: interval)
                return nil
            }
        case "disableBackgroundSync":
            reply(result) {
                try await bridge.disableBackgroundSync()
                return nil
            }
        default:
            result(FlutterMethodNotImplemented)
        }
    }

    /// Runs `work` off the main thread and answers `result` on it.
    private func reply(_ result: @escaping FlutterResult, _ work: @escaping () async throws -> Any?) {
        Task {
            let answer: Any?
            do {
                answer = try await work()
            } catch {
                let map = HealthSyncBridge.errorMap(error)
                answer = FlutterError(
                    code: map["code"] as? String ?? "health_store_error",
                    message: map["message"] as? String,
                    details: map)
            }
            DispatchQueue.main.async {
                result(answer)
            }
        }
    }

    // MARK: - Background results

    public func onListen(
        withArguments arguments: Any?, eventSink events: @escaping FlutterEventSink
    ) -> FlutterError? {
        bridge.attach { event in events(event) }
        return nil
    }

    public func onCancel(withArguments arguments: Any?) -> FlutterError? {
        bridge.detach()
        return nil
    }
}
