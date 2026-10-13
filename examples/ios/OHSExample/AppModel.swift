// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import HealthKit
import OpenHealthSync
import OpenHealthSyncHealthKit

/// The result of one sync, shown on the main screen.
struct SyncOutcome: Sendable {
    let date: Date
    let report: SyncReport?
    let errorLines: [String]

    static func success(_ report: SyncReport) -> SyncOutcome {
        return SyncOutcome(date: Date(), report: report, errorLines: [])
    }

    static func failure(_ error: Error) -> SyncOutcome {
        return SyncOutcome(date: Date(), report: nil, errorLines: ErrorText.lines(for: error))
    }
}

/// Holds the settings and drives `HealthKitSync`. The server URL and the picked types live in
/// `UserDefaults`; the token lives in the Keychain only.
@MainActor
final class AppModel: ObservableObject {
    /// One model for the whole app, shared by the app delegate and the views.
    static let shared = AppModel()
    static let defaultServerURL = "http://localhost:8080"

    private enum Keys {
        static let serverURL = "OHSExample.serverURL"
        static let selectedTypes = "OHSExample.selectedTypes"
        static let backgroundDelivery = "OHSExample.backgroundDelivery"
    }

    @Published private(set) var serverURL: String
    @Published private(set) var hasToken: Bool
    @Published private(set) var selectedTypes: Set<String>
    @Published private(set) var backgroundDelivery: Bool
    @Published private(set) var isBusy = false
    @Published private(set) var notice: String?
    @Published private(set) var lastSync: SyncOutcome?
    @Published private(set) var lastBackgroundSync: SyncOutcome?

    private let defaults = UserDefaults.standard
    private let healthStore: HKHealthStore
    private var healthSync: HealthKitSync

    private init() {
        let stored = UserDefaults.standard
        let url = stored.string(forKey: Keys.serverURL) ?? AppModel.defaultServerURL
        let store = HKHealthStore()
        serverURL = url
        hasToken = KeychainStore.read(account: KeychainStore.userTokenAccount) != nil
        if let picked = stored.stringArray(forKey: Keys.selectedTypes) {
            selectedTypes = Set(picked)
        } else {
            selectedTypes = Set(HealthDataType.all.map { $0.id })
        }
        backgroundDelivery = stored.bool(forKey: Keys.backgroundDelivery)
        healthStore = store
        healthSync = AppModel.makeSync(store: store, serverURL: url)
    }

    /// A client that holds only the upload token, read from the Keychain before each request.
    private static func makeSync(store: HKHealthStore, serverURL: String) -> HealthKitSync {
        let url = URL(string: serverURL) ?? URL(string: defaultServerURL)!
        let client = OHSClient(baseURL: url, tokenProvider: {
            guard let token = KeychainStore.read(account: KeychainStore.userTokenAccount) else {
                throw ExampleError.missingToken
            }
            return token
        })
        return HealthKitSync(store: store, client: client, batchPrefix: "ohsexample")
    }

    private var selectedSampleTypes: Set<HKSampleType> {
        return Set(HealthDataType.all.filter { selectedTypes.contains($0.id) }.map { $0.sampleType })
    }

    // MARK: - Types

    func isSelected(_ type: HealthDataType) -> Bool {
        return selectedTypes.contains(type.id)
    }

    func setSelected(_ type: HealthDataType, _ on: Bool) {
        if on {
            selectedTypes.insert(type.id)
        } else {
            selectedTypes.remove(type.id)
        }
        defaults.set(Array(selectedTypes).sorted(), forKey: Keys.selectedTypes)
        restartBackgroundDelivery()
    }

    // MARK: - Actions

    func requestHealthAccess() async {
        guard HKHealthStore.isHealthDataAvailable() else {
            notice = ExampleError.healthUnavailable.errorDescription
            return
        }
        isBusy = true
        defer { isBusy = false }
        do {
            let types = Set(HealthDataType.all.map { $0.sampleType })
            #if DEBUG
            if let heartRates = UITestSeed.heartRates {
                try await UITestSeed.authorizeAndWrite(heartRates, store: healthStore, read: types)
            }
            #endif
            try await healthSync.requestAuthorization(for: types)
            notice = "Health access requested. iOS never tells an app which types you allowed: a declined type just syncs nothing."
        } catch {
            notice = ErrorText.lines(for: error).joined(separator: "\n")
        }
    }

    func syncNow() async {
        guard hasToken else {
            lastSync = .failure(ExampleError.missingToken)
            return
        }
        let types = selectedSampleTypes
        guard !types.isEmpty else {
            lastSync = .failure(ExampleError.noTypes)
            return
        }
        isBusy = true
        defer { isBusy = false }
        do {
            let report = try await healthSync.sync(types: types)
            lastSync = .success(report)
        } catch {
            lastSync = .failure(error)
        }
    }

    func setBackgroundDelivery(_ on: Bool) async {
        if !on {
            healthSync.stopObserving()
            await disableAllBackgroundDelivery()
            backgroundDelivery = false
            defaults.set(false, forKey: Keys.backgroundDelivery)
            notice = "Background delivery is off."
            return
        }
        guard HKHealthStore.isHealthDataAvailable() else {
            notice = ExampleError.healthUnavailable.errorDescription
            return
        }
        guard hasToken else {
            notice = ExampleError.missingToken.errorDescription
            return
        }
        do {
            try await startBackgroundDelivery()
            backgroundDelivery = true
            defaults.set(true, forKey: Keys.backgroundDelivery)
            notice = "Background delivery is on. iOS decides when to wake the app; steps arrive at most hourly."
        } catch {
            notice = ErrorText.lines(for: error).joined(separator: "\n")
        }
    }

    /// Called at every launch: HealthKit forgets the observer queries when the app quits.
    func resumeBackgroundDelivery() {
        guard backgroundDelivery, HKHealthStore.isHealthDataAvailable() else {
            return
        }
        Task {
            do {
                try await startBackgroundDelivery()
            } catch {
                notice = ErrorText.lines(for: error).joined(separator: "\n")
            }
        }
    }

    // MARK: - Settings

    /// Saves the settings and returns a message when something is wrong. An empty token keeps
    /// the saved one.
    func saveSettings(serverURL newURL: String, token newToken: String) -> String? {
        let url = newURL.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let parsed = URL(string: url),
              let scheme = parsed.scheme?.lowercased(),
              scheme == "http" || scheme == "https",
              parsed.host != nil
        else {
            return "Enter a URL such as http://localhost:8080."
        }
        let token = newToken.trimmingCharacters(in: .whitespacesAndNewlines)
        if !token.isEmpty {
            guard token.hasPrefix("ohs_ut_") else {
                return "A user token starts with ohs_ut_. Never paste the tenant key (ohs_sk_) into an app."
            }
            guard KeychainStore.write(token, account: KeychainStore.userTokenAccount) else {
                return "Could not save the token in the Keychain."
            }
            hasToken = true
        }
        if url != serverURL {
            healthSync.stopObserving()
            serverURL = url
            defaults.set(url, forKey: Keys.serverURL)
            healthSync = AppModel.makeSync(store: healthStore, serverURL: url)
            restartBackgroundDelivery()
        }
        return nil
    }

    func removeToken() {
        KeychainStore.delete(account: KeychainStore.userTokenAccount)
        hasToken = false
    }

    /// The next sync uploads every sample of every type again (the server stores each once).
    func resetAnchors() {
        for type in HealthDataType.all {
            healthSync.resetAnchor(for: type.sampleType)
        }
        notice = "Anchors reset. The next sync uploads the whole history of each type."
    }

    // MARK: - Background delivery

    private func startBackgroundDelivery() async throws {
        let types = selectedSampleTypes
        guard !types.isEmpty else {
            throw ExampleError.noTypes
        }
        try await healthSync.enableBackgroundDelivery(for: types, frequency: .immediate) { result in
            let outcome: SyncOutcome
            switch result {
            case .success(let report):
                outcome = .success(report)
            case .failure(let error):
                outcome = .failure(error)
            }
            Task { @MainActor in
                self.lastBackgroundSync = outcome
            }
        }
    }

    private func restartBackgroundDelivery() {
        guard backgroundDelivery else {
            return
        }
        Task {
            healthSync.stopObserving()
            await disableAllBackgroundDelivery()
            do {
                try await startBackgroundDelivery()
            } catch {
                backgroundDelivery = false
                defaults.set(false, forKey: Keys.backgroundDelivery)
                notice = ErrorText.lines(for: error).joined(separator: "\n")
            }
        }
    }

    private func disableAllBackgroundDelivery() async {
        let store = healthStore
        await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
            store.disableAllBackgroundDelivery { _, _ in
                continuation.resume()
            }
        }
    }
}
