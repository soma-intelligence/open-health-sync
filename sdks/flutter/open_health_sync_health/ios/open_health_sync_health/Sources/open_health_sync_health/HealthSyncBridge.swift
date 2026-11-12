// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import HealthKit
import OpenHealthSync
import OpenHealthSyncHealthKit

/// An error the Dart side gets as a `HealthSyncException` with this `code`.
struct BridgeError: Error, Equatable {
    let code: String
    let message: String

    static let notConfigured = BridgeError(code: "not_configured", message: "call setCredentials first")
    static let unavailable = BridgeError(
        code: "unavailable", message: "HealthKit is not available on this device")
}

/// What the plugin does with the Swift SDK outside of the channel itself: building the
/// `HealthKitSync` from the stored credentials, resolving the type names the Dart side sends,
/// turning reports into channel values, and keeping background delivery on across launches.
///
/// Nothing is read from HealthKit or mapped here: that is `HealthKitSync`.
final class HealthSyncBridge: @unchecked Sendable {
    static let shared = HealthSyncBridge()

    /// How many background results are kept while the Dart side does not listen.
    static let maxBuffered = 20
    static let userAgent = "open-health-sync-flutter/0.1.0 (ios)"

    private let credentials: CredentialStore
    private let defaults: UserDefaults
    private let backgroundKey = "OpenHealthSync.flutter.background"
    private let store = HKHealthStore()

    private let lock = NSLock()
    private var sync: HealthKitSync?
    private var syncBaseURL: String?
    /// Whether the current sync runs observer queries.
    private var observing = false

    // The two below are only touched on the main thread.
    private var listener: (([String: Any]) -> Void)?
    private var buffered: [[String: Any]] = []

    init(credentials: CredentialStore = CredentialStore(), defaults: UserDefaults = .standard) {
        self.credentials = credentials
        self.defaults = defaults
    }

    // MARK: - Availability and credentials

    var availabilityName: String {
        return HKHealthStore.isHealthDataAvailable() ? "available" : "unavailable"
    }

    func setCredentials(baseURL: String, token: String) async throws {
        guard let url = URL(string: baseURL), let scheme = url.scheme?.lowercased(),
              scheme == "https" || scheme == "http", !token.isEmpty
        else {
            throw BridgeError(
                code: "invalid_argument",
                message: "baseUrl must be an http or https URL and userToken not empty")
        }
        guard credentials.save(baseURL: baseURL, token: token) else {
            throw BridgeError(code: "health_store_error", message: "the keychain refused the token")
        }
        // A sync built for another server keeps its observers: stop them. Then background
        // delivery starts again when it was asked for and does not run, which is also the
        // case after a sign out. A new token for the same server changes nothing here.
        takeSync(unlessFor: baseURL)?.stopObserving()
        if !isObserving {
            await resumeBackgroundSync()
        }
    }

    func clearCredentials() {
        credentials.clear()
        takeSync(unlessFor: nil)?.stopObserving()
    }

    // MARK: - The sync

    /// The `HealthKitSync` of the stored credentials, built once per base URL. Its client reads
    /// the token again before each request, so a token stored later is used at once.
    func healthSync() throws -> HealthKitSync {
        guard HKHealthStore.isHealthDataAvailable() else {
            throw BridgeError.unavailable
        }
        guard let base = credentials.baseURL, let url = URL(string: base), credentials.token != nil else {
            throw BridgeError.notConfigured
        }
        lock.lock()
        defer { lock.unlock() }
        if let sync = sync, syncBaseURL == base {
            return sync
        }
        let credentials = self.credentials
        let client = OHSClient(
            baseURL: url,
            tokenProvider: {
                guard let token = credentials.token else {
                    throw BridgeError.notConfigured
                }
                return token
            },
            userAgent: HealthSyncBridge.userAgent)
        let created = HealthKitSync(store: store, client: client)
        sync = created
        syncBaseURL = base
        return created
    }

    private var isObserving: Bool {
        lock.lock()
        defer { lock.unlock() }
        return observing
    }

    private func setObserving(_ value: Bool, for sync: HealthKitSync) {
        lock.lock()
        defer { lock.unlock() }
        if self.sync === sync {
            observing = value
        }
    }

    /// Forgets the current sync unless it was built for `baseURL`, and returns it.
    private func takeSync(unlessFor baseURL: String?) -> HealthKitSync? {
        lock.lock()
        defer { lock.unlock() }
        guard let current = sync, syncBaseURL != baseURL else {
            return nil
        }
        sync = nil
        syncBaseURL = nil
        observing = false
        return current
    }

    /// The HealthKit types of the names the Dart side sends: identifiers of the Swift SDK's
    /// `AppleHealthTypes`, or one of its groups (`@quantities`, `@nutrition`, `@categories`).
    /// nil is every type the SDK syncs. A name the SDK or this OS version does not know is
    /// left out.
    static func sampleTypes(_ names: [String]?) -> Set<HKSampleType> {
        guard let names = names else {
            return HealthKitSync.allSupportedTypes
        }
        var types = Set<HKSampleType>()
        for name in names {
            switch name {
            case "@quantities":
                types.formUnion(HealthKitSync.quantityTypes)
            case "@nutrition":
                types.formUnion(HealthKitSync.nutritionTypes)
            case "@categories":
                types.formUnion(HealthKitSync.categoryTypes)
            default:
                if let type = HealthKitSync.sampleType(forIdentifier: name) {
                    types.insert(type)
                }
            }
        }
        return types
    }

    private static func requireTypes(_ names: [String]?) throws -> Set<HKSampleType> {
        let types = sampleTypes(names)
        guard !types.isEmpty else {
            throw BridgeError(code: "invalid_argument", message: "no HealthKit type in types")
        }
        return types
    }

    /// Shows the HealthKit sheet for the types of `names`. HealthKit does not say what the
    /// user granted, so the answer has no `granted`.
    func requestAuthorization(_ names: [String]?) async throws -> [String: Any] {
        guard HKHealthStore.isHealthDataAvailable() else {
            throw BridgeError.unavailable
        }
        // Without this key HealthKit stops the app instead of returning an error.
        guard Bundle.main.object(forInfoDictionaryKey: "NSHealthShareUsageDescription") != nil else {
            throw BridgeError(
                code: "missing_usage_description",
                message: "add NSHealthShareUsageDescription to Info.plist")
        }
        let read = HealthKitSync.authorizationTypes(for: try HealthSyncBridge.requireTypes(names))
        try await store.requestAuthorization(toShare: [], read: read)
        return ["notDeclared": [String]()]
    }

    func sync(_ names: [String]?) async throws -> [String: Any] {
        let types = try HealthSyncBridge.requireTypes(names)
        return HealthSyncBridge.reportMap(try await healthSync().sync(types: types))
    }

    // MARK: - Background delivery

    /// Turns HealthKit background delivery on for the types of `names` and remembers the
    /// request, so `resumeBackgroundSync` can turn it on again at the next launch.
    func enableBackgroundSync(_ names: [String]?, intervalSeconds: Int) async throws {
        let types = try HealthSyncBridge.requireTypes(names)
        let sync = try healthSync()
        // Called again with other types: the observers of the first call must not stay.
        sync.stopObserving()
        setObserving(false, for: sync)
        try await sync.enableBackgroundDelivery(
            for: types, frequency: HealthSyncBridge.frequency(intervalSeconds: intervalSeconds)
        ) { [weak self] result in
            self?.publish(result)
        }
        setObserving(true, for: sync)
        var request: [String: Any] = ["intervalSeconds": intervalSeconds]
        if let names = names {
            request["types"] = names
        }
        defaults.set(request, forKey: backgroundKey)
    }

    func disableBackgroundSync() async throws {
        defaults.removeObject(forKey: backgroundKey)
        takeSync(unlessFor: nil)?.stopObserving()
        guard HKHealthStore.isHealthDataAvailable() else {
            return
        }
        try await store.disableAllBackgroundDelivery()
    }

    /// Turns background delivery on again when it was enabled before. HealthKit asks for
    /// this at every launch, before the end of `application(_:didFinishLaunchingWithOptions:)`;
    /// the plugin calls it when it is registered.
    func resumeBackgroundSync() async {
        guard let request = defaults.dictionary(forKey: backgroundKey) else {
            return
        }
        let names = request["types"] as? [String]
        let interval = request["intervalSeconds"] as? Int ?? 3600
        do {
            try await enableBackgroundSync(names, intervalSeconds: interval)
        } catch let error as BridgeError where error == .notConfigured {
            // Signed out: nothing to run until the next setCredentials.
        } catch {
            publish(.failure(error))
        }
    }

    /// The HealthKit frequency closest to an interval: immediate under an hour, then hourly,
    /// daily, weekly.
    static func frequency(intervalSeconds: Int) -> HKUpdateFrequency {
        switch intervalSeconds {
        case ..<3600:
            return .immediate
        case ..<86400:
            return .hourly
        case ..<604_800:
            return .daily
        default:
            return .weekly
        }
    }

    // MARK: - Background results

    /// Sends the outcome of a background sync to the Dart side, or keeps it until it listens.
    private func publish(_ result: Result<SyncReport, Error>) {
        var event: [String: Any] = ["at": Int64(Date().timeIntervalSince1970 * 1000)]
        switch result {
        case .success(let report):
            event["report"] = HealthSyncBridge.reportMap(report)
        case .failure(let error):
            event["error"] = HealthSyncBridge.errorMap(error)
        }
        DispatchQueue.main.async { [self] in
            if let listener = listener {
                listener(event)
            } else {
                buffered.append(event)
                if buffered.count > HealthSyncBridge.maxBuffered {
                    buffered.removeFirst(buffered.count - HealthSyncBridge.maxBuffered)
                }
            }
        }
    }

    /// The Dart side listens: it gets what was kept, then every new result. Main thread.
    func attach(_ listener: @escaping ([String: Any]) -> Void) {
        self.listener = listener
        let kept = buffered
        buffered = []
        kept.forEach(listener)
    }

    /// The Dart side stopped listening. Main thread.
    func detach() {
        listener = nil
    }

    // MARK: - Channel values

    /// A report as the Dart side reads it. HealthKit has no upload queue and no permission
    /// answer, so those members are empty.
    static func reportMap(_ report: SyncReport) -> [String: Any] {
        return [
            "batchIds": report.batchIds,
            "partsUploaded": report.partsUploaded,
            "duplicateParts": report.duplicateParts,
            "partsPending": 0,
            "partsDropped": 0,
            "linesByType": report.linesByType,
            "deletionsByType": report.deletionsByType,
            "deletionsPendingByType": report.deletionsPendingByType,
            "skippedRecords": report.skippedSamples,
            "typesWithoutPermission": [String](),
            "expiredTokenTypes": [String](),
        ]
    }

    static let emptyReport = reportMap(SyncReport())

    /// An error as the Dart side reads it. The text is the SDK's or the system's, never a
    /// token or a sample.
    static func errorMap(_ error: Error) -> [String: Any] {
        if let error = error as? BridgeError {
            return ["code": error.code, "message": error.message]
        }
        if let error = error as? OHSAPIError {
            var map: [String: Any] = [
                "code": "api_error", "message": error.description, "status": error.status,
            ]
            if let requestId = error.requestId {
                map["requestId"] = requestId
            }
            return map
        }
        if let error = error as? OHSError {
            return ["code": "invalid_argument", "message": error.description]
        }
        if let error = error as? HKError {
            let code: String
            switch error.code {
            case .errorAuthorizationDenied, .errorAuthorizationNotDetermined:
                code = "permission_denied"
            case .errorDatabaseInaccessible:
                code = "store_locked"
            case .errorHealthDataUnavailable, .errorHealthDataRestricted:
                code = "unavailable"
            default:
                code = "health_store_error"
            }
            return ["code": code, "message": error.localizedDescription]
        }
        if let error = error as? URLError {
            return ["code": "network_error", "message": error.localizedDescription]
        }
        return ["code": "health_store_error", "message": String(describing: type(of: error))]
    }
}
