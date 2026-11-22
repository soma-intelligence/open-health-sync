// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import HealthKit
import OpenHealthSync
import OpenHealthSyncHealthKit

/// A failure handed back to JavaScript: a code of src/health/report.ts and a message that
/// holds no token and no health value.
private struct BridgeFailure: Error {
    let code: String
    let message: String
}

/// No user token is kept: the app has not called `configure`, or cleared its credentials.
private struct MissingCredentials: Error {}

/// The Swift side of the React Native module. Every call goes to the Swift SDK's
/// `HealthKitSync`: this class only turns JavaScript arguments into its types and its
/// results into JSON (src/health/report.ts reads it), and keeps what a background wake
/// needs between runs. It is a singleton because HealthKit's observer queries must outlive
/// a JavaScript reload.
@objc(OHSHealthBridge)
public final class OHSHealthBridge: NSObject, @unchecked Sendable {
    /// Receives a value, or an error code and message.
    public typealias Completion = @Sendable (_ value: String?, _ errorCode: String?, _ errorMessage: String?) -> Void

    @objc public static let shared = OHSHealthBridge()

    private let store = HKHealthStore()
    private let anchors = UserDefaultsAnchorStore()
    private let defaults = UserDefaults.standard
    private let lock = NSLock()
    private var healthSync: HealthKitSync?
    private var healthSyncBaseURL: String?
    private var backgroundSyncHandler: ((String) -> Void)?

    private enum Key {
        static let background = "OpenHealthSync.reactNative.backgroundSync"
        static let pendingTypes = "OpenHealthSync.reactNative.pendingTypes"
        static let lastBackgroundSync = "OpenHealthSync.reactNative.lastBackgroundSync"
    }

    private static let everyType = "*"

    // MARK: - Availability and credentials

    @objc public func availability() -> String {
        return HKHealthStore.isHealthDataAvailable() ? "available" : "unavailable"
    }

    /// Keeps the base URL and the user token in the Keychain. Background delivery, when it
    /// was on, starts again against the new server.
    @objc public func configure(baseUrl: String, userToken: String, completion: @escaping Completion) {
        guard let url = URL(string: baseUrl), let scheme = url.scheme?.lowercased(), scheme == "http" || scheme == "https",
              !userToken.isEmpty
        else {
            completion(nil, "invalid_argument", "baseUrl must be an http or https URL and userToken must not be empty")
            return
        }
        guard Keychain.set(baseUrl, for: Keychain.baseURL), Keychain.set(userToken, for: Keychain.userToken) else {
            completion(nil, "storage", "the Keychain could not keep the credentials")
            return
        }
        if dropSync(ifBaseURLIsNot: baseUrl) {
            resumeBackgroundSync()
        }
        completion(nil, nil, nil)
    }

    @objc public func isConfigured() -> Bool {
        return Keychain.get(Keychain.baseURL) != nil && Keychain.get(Keychain.userToken) != nil
    }

    /// Forgets the credentials, stops background delivery, and forgets the anchors, so the
    /// next user of this device starts from a full read.
    @objc public func clearCredentials() {
        disableBackgroundSync()
        for type in HealthKitSync.allSupportedTypes {
            anchors.setAnchorData(nil, for: type.identifier)
        }
        defaults.removeObject(forKey: Key.pendingTypes)
        defaults.removeObject(forKey: Key.lastBackgroundSync)
        Keychain.delete(Keychain.userToken)
        Keychain.delete(Keychain.baseURL)
        _ = dropSync(ifBaseURLIsNot: nil)
    }

    // MARK: - Authorization and sync

    @objc public func requestAuthorization(types: [String], completion: @escaping Completion) {
        run(completion) {
            guard HKHealthStore.isHealthDataAvailable() else {
                throw BridgeFailure(code: "unavailable", message: Self.unavailable)
            }
            let resolved = try Self.resolve(types)
            // The sheet needs no server: the SDK's own request is used when credentials are
            // kept, and the same read set is asked directly when they are not yet.
            if let sync = try? self.currentSync() {
                try await sync.requestAuthorization(for: resolved.types)
            } else {
                try await self.store.requestAuthorization(toShare: [], read: HealthKitSync.authorizationTypes(for: resolved.types))
            }
            // HealthKit never tells an app whether read access was granted.
            return Self.json(["granted": NSNull(), "background": NSNull(), "history": NSNull()])
        }
    }

    @objc public func sync(types: [String], completion: @escaping Completion) {
        run(completion) {
            let resolved = try Self.resolve(types)
            return Self.json(try await self.sync(resolved.types, unavailable: resolved.unavailable))
        }
    }

    /// Syncs the types an earlier sync left behind after an upload failure. The Swift SDK
    /// has no queue on disk: their anchors did not move, so this reads them again.
    @objc public func flush(completion: @escaping Completion) {
        run(completion) {
            let pending = Set(self.pendingTypes().compactMap(HealthKitSync.sampleType(forIdentifier:)))
            if pending.isEmpty {
                // Nothing is left behind, and nothing may be: no need for credentials.
                return Self.json(Self.reportJSON(SyncReport()))
            }
            return Self.json(try await self.sync(pending, unavailable: []))
        }
    }

    /// One `HealthKitSync.sync` per type, so that an upload failure on one type keeps the
    /// report of the types before it, and the types after it are remembered for `flush`.
    private func sync(_ types: Set<HKSampleType>, unavailable: [String]) async throws -> [String: Any] {
        let healthSync = try currentSync()
        let sorted = types.sorted { $0.identifier < $1.identifier }
        var total = SyncReport()
        var uploadError: [String: Any]?
        var left: [String] = []
        for (index, type) in sorted.enumerated() {
            do {
                Self.add(try await healthSync.sync(types: [type]), to: &total)
                forgetPending(type.identifier)
            } catch {
                left = sorted[index...].map { $0.identifier }
                rememberPending(left)
                guard let upload = Self.uploadErrorJSON(error) else {
                    throw error
                }
                // The network or the server: the types after this one would fail the same way.
                uploadError = upload
                break
            }
        }
        var json = Self.reportJSON(total)
        json["pendingTypes"] = left
        json["unavailableTypes"] = unavailable
        json["uploadError"] = uploadError ?? NSNull()
        return json
    }

    // MARK: - Background sync

    /// Turns on HealthKit background delivery for `types` and remembers the setting, so it
    /// is turned on again at every launch (`resumeBackgroundSync`).
    @objc public func enableBackgroundSync(types: [String], frequency: String, completion: @escaping Completion) {
        run(completion) {
            _ = try Self.resolve(types)
            guard Self.frequency(frequency) != nil else {
                throw BridgeFailure(code: "invalid_argument", message: "unknown frequency")
            }
            try await self.startObserving(types: types, frequency: frequency)
            self.defaults.set(["types": types, "frequency": frequency], forKey: Key.background)
            return nil
        }
    }

    @objc public func disableBackgroundSync() {
        defaults.removeObject(forKey: Key.background)
        lock.lock()
        let sync = healthSync
        lock.unlock()
        sync?.stopObserving()
        if HKHealthStore.isHealthDataAvailable() {
            store.disableAllBackgroundDelivery { _, _ in }
        }
    }

    /// Starts the observer queries again when background sync was turned on in an earlier
    /// run. Called at the end of every app launch (OpenHealthSyncModule.mm), which is when
    /// HealthKit expects them, including a launch in the background to deliver samples.
    @objc public func resumeBackgroundSync() {
        guard let setting = defaults.dictionary(forKey: Key.background),
              let types = setting["types"] as? [String],
              let frequency = setting["frequency"] as? String,
              isConfigured()
        else {
            return
        }
        Task {
            do {
                try await self.startObserving(types: types, frequency: frequency)
            } catch {
                self.deliver(.failure(error))
            }
        }
    }

    @objc public func lastBackgroundSync() -> String {
        return defaults.string(forKey: Key.lastBackgroundSync) ?? ""
    }

    /// The handler that hands a background result to JavaScript; nil when no React Native
    /// instance is alive.
    @objc public func setBackgroundSyncHandler(_ handler: ((String) -> Void)?) {
        lock.lock()
        backgroundSyncHandler = handler
        lock.unlock()
    }

    private func startObserving(types: [String], frequency: String) async throws {
        let healthSync = try currentSync()
        let resolved = try Self.resolve(types)
        healthSync.stopObserving()
        try await healthSync.enableBackgroundDelivery(
            for: resolved.types,
            frequency: Self.frequency(frequency) ?? .immediate
        ) { [weak self] result in
            self?.deliver(result)
        }
    }

    /// Keeps a background result and hands it to JavaScript when it is listening.
    private func deliver(_ result: Result<SyncReport, Error>) {
        var event: [String: Any] = ["at": ISO8601DateFormatter().string(from: Date())]
        switch result {
        case .success(let report):
            event["ok"] = true
            event["report"] = Self.reportJSON(report)
        case .failure(let error):
            if let upload = Self.uploadErrorJSON(error) {
                // Like Android: an upload that failed is a report with data left behind.
                var report = Self.reportJSON(SyncReport())
                report["uploadError"] = upload
                event["ok"] = true
                event["report"] = report
            } else {
                let failure = Self.failure(error)
                event["ok"] = false
                event["error"] = ["code": failure.code, "message": failure.message]
            }
        }
        guard let json = Self.json(event) else {
            return
        }
        defaults.set(json, forKey: Key.lastBackgroundSync)
        lock.lock()
        let handler = backgroundSyncHandler
        lock.unlock()
        handler?(json)
    }

    // MARK: - The SDK's sync

    /// The `HealthKitSync` over the kept credentials. The token is read from the Keychain at
    /// each request, so a token renewed by `configure` is used at once.
    private func currentSync() throws -> HealthKitSync {
        guard HKHealthStore.isHealthDataAvailable() else {
            throw BridgeFailure(code: "unavailable", message: Self.unavailable)
        }
        guard let base = Keychain.get(Keychain.baseURL), let url = URL(string: base),
              Keychain.get(Keychain.userToken) != nil
        else {
            throw BridgeFailure(code: "not_configured", message: "call configure with a base URL and a user token first")
        }
        lock.lock()
        defer { lock.unlock() }
        if let sync = healthSync, healthSyncBaseURL == base {
            return sync
        }
        healthSync?.stopObserving()
        let client = OHSClient(baseURL: url, tokenProvider: {
            guard let token = Keychain.get(Keychain.userToken) else {
                throw MissingCredentials()
            }
            return token
        })
        let sync = HealthKitSync(store: store, client: client, anchors: anchors)
        healthSync = sync
        healthSyncBaseURL = base
        return sync
    }

    /// Drops the current sync when it talks to another server than `baseURL` (nil: always).
    /// Returns whether one was dropped.
    private func dropSync(ifBaseURLIsNot baseURL: String?) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        guard let sync = healthSync, baseURL == nil || healthSyncBaseURL != baseURL else {
            return false
        }
        sync.stopObserving()
        healthSync = nil
        healthSyncBaseURL = nil
        return true
    }

    // MARK: - Pending types

    private func pendingTypes() -> [String] {
        return defaults.stringArray(forKey: Key.pendingTypes) ?? []
    }

    private func rememberPending(_ identifiers: [String]) {
        lock.lock()
        defer { lock.unlock() }
        var pending = pendingTypes()
        for identifier in identifiers where !pending.contains(identifier) {
            pending.append(identifier)
        }
        defaults.set(pending, forKey: Key.pendingTypes)
    }

    private func forgetPending(_ identifier: String) {
        lock.lock()
        defer { lock.unlock() }
        let pending = pendingTypes()
        if pending.contains(identifier) {
            defaults.set(pending.filter { $0 != identifier }, forKey: Key.pendingTypes)
        }
    }

    // MARK: - Arguments

    private struct ResolvedTypes {
        var types = Set<HKSampleType>()
        /// Types the server stores that this OS version does not have.
        var unavailable: [String] = []
    }

    /// The HealthKit types of the names JavaScript sent: an identifier of the SDK's type
    /// table, a group of it (`@nutrition`, `@workouts`, `@heartRhythm`), or `*` for all.
    private static func resolve(_ names: [String]) throws -> ResolvedTypes {
        var out = ResolvedTypes()
        for name in names {
            switch name {
            case everyType:
                out.types.formUnion(HealthKitSync.allSupportedTypes)
            case "@nutrition":
                out.types.formUnion(HealthKitSync.nutritionTypes)
            case "@workouts":
                out.types.formUnion(HealthKitSync.workoutTypes)
            case "@heartRhythm":
                out.types.formUnion(HealthKitSync.heartRhythmTypes)
            default:
                if let type = HealthKitSync.sampleType(forIdentifier: name) {
                    out.types.insert(type)
                } else if AppleHealthTypes.kind(of: name) != nil {
                    out.unavailable.append(name)
                } else {
                    throw BridgeFailure(code: "invalid_argument", message: "unknown HealthKit type")
                }
            }
        }
        if out.types.isEmpty && out.unavailable.isEmpty {
            throw BridgeFailure(code: "invalid_argument", message: "types must not be empty")
        }
        return out
    }

    private static func frequency(_ name: String) -> HKUpdateFrequency? {
        switch name {
        case "immediate": return .immediate
        case "hourly": return .hourly
        case "daily": return .daily
        case "weekly": return .weekly
        default: return nil
        }
    }

    // MARK: - Results

    private static let unavailable = "HealthKit is not available on this device"

    private static func add(_ report: SyncReport, to total: inout SyncReport) {
        total.batchIds += report.batchIds
        total.partsUploaded += report.partsUploaded
        total.duplicateParts += report.duplicateParts
        total.linesByType.merge(report.linesByType, uniquingKeysWith: +)
        total.deletionsByType.merge(report.deletionsByType, uniquingKeysWith: +)
        total.deletionsPendingByType.merge(report.deletionsPendingByType, uniquingKeysWith: +)
        total.skippedSamples += report.skippedSamples
    }

    private static func reportJSON(_ report: SyncReport) -> [String: Any] {
        return [
            "batchIds": report.batchIds,
            "partsUploaded": report.partsUploaded,
            "duplicateParts": report.duplicateParts,
            "linesByType": report.linesByType,
            "deletionsByType": report.deletionsByType,
            "deletionsPendingByType": report.deletionsPendingByType,
            "skippedRecords": report.skippedSamples,
            "uploadError": NSNull(),
        ]
    }

    /// The `uploadError` of a report when `error` is an upload that failed (no network, a
    /// refusal of the server); nil when it is something else.
    private static func uploadErrorJSON(_ error: Error) -> [String: Any]? {
        if let api = error as? OHSAPIError {
            let code: String
            switch api.status {
            case 401: code = "unauthorized"
            case 403: code = "forbidden"
            case 429: code = "rate_limited"
            case 500...: code = "server"
            default: code = "api"
            }
            // The SDK builds this text from the problem body: no token, no health value.
            var json: [String: Any] = ["code": code, "message": api.description, "status": api.status]
            if let requestId = api.requestId, !requestId.isEmpty {
                json["requestId"] = requestId
            }
            return json
        }
        if error is MissingCredentials {
            return ["code": "unauthorized", "message": "no user token is configured"]
        }
        if let sdk = error as? OHSError {
            return ["code": "unknown", "message": sdk.description]
        }
        let ns = error as NSError
        if error is URLError || ns.domain == NSURLErrorDomain {
            return ["code": "network", "message": "network error (\(ns.code))"]
        }
        return nil
    }

    /// The code and message a failed call rejects with.
    private static func failure(_ error: Error) -> BridgeFailure {
        if let failure = error as? BridgeFailure {
            return failure
        }
        if let upload = uploadErrorJSON(error) {
            // An upload failure outside a sync (none today); kept so nothing is swallowed.
            return BridgeFailure(code: "unknown", message: (upload["message"] as? String) ?? "upload failed")
        }
        let ns = error as NSError
        if ns.domain == HKErrorDomain {
            // HealthKit's own text: it names the problem (a missing entitlement, a locked
            // device) and never a sample.
            let denied = ns.code == HKError.Code.errorAuthorizationDenied.rawValue
                || ns.code == HKError.Code.errorAuthorizationNotDetermined.rawValue
            return BridgeFailure(code: denied ? "permission_denied" : "health_store", message: ns.localizedDescription)
        }
        if let refused = error as? HealthKitSyncError, case .refused(let what) = refused {
            return BridgeFailure(code: "health_store", message: "HealthKit refused the \(what)")
        }
        return BridgeFailure(code: "unknown", message: "health sync failed (\(ns.domain) \(ns.code))")
    }

    private static func json(_ object: [String: Any]) -> String? {
        guard let data = try? JSONSerialization.data(withJSONObject: object) else {
            return nil
        }
        return String(data: data, encoding: .utf8)
    }

    /// Runs `work` and hands its value, or its failure as a code and a message, to `completion`.
    private func run(_ completion: @escaping Completion, _ work: @escaping @Sendable () async throws -> String?) {
        Task {
            do {
                completion(try await work(), nil, nil)
            } catch {
                let failure = Self.failure(error)
                completion(nil, failure.code, failure.message)
            }
        }
    }
}

/// The base URL and the user token, in the app's Keychain. `AfterFirstUnlockThisDeviceOnly`:
/// readable when HealthKit wakes the app in the background once the device has been unlocked
/// since boot, and never copied to a backup or another device. Nothing here is ever logged.
private enum Keychain {
    static let service = "org.openhealthsync.reactnative"
    static let baseURL = "baseUrl"
    static let userToken = "userToken"

    private static func query(_ account: String) -> [String: Any] {
        return [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }

    static func set(_ value: String, for account: String) -> Bool {
        SecItemDelete(query(account) as CFDictionary)
        var item = query(account)
        item[kSecValueData as String] = Data(value.utf8)
        item[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        return SecItemAdd(item as CFDictionary, nil) == errSecSuccess
    }

    static func get(_ account: String) -> String? {
        var item = query(account)
        item[kSecReturnData as String] = true
        item[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: AnyObject?
        guard SecItemCopyMatching(item as CFDictionary, &result) == errSecSuccess, let data = result as? Data else {
            return nil
        }
        return String(data: data, encoding: .utf8)
    }

    static func delete(_ account: String) {
        SecItemDelete(query(account) as CFDictionary)
    }
}
