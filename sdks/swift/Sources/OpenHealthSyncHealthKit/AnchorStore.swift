// SPDX-License-Identifier: AGPL-3.0-only

import Foundation

/// Keeps one HealthKit query anchor per sample type identifier, as archived bytes.
/// `HealthKitSync` saves a type's anchor only after every part of that type was uploaded,
/// so an interrupted sync starts again from the last anchor that is fully on the server.
public protocol AnchorStore: AnyObject {
    func anchorData(for typeIdentifier: String) -> Data?
    func setAnchorData(_ data: Data?, for typeIdentifier: String)
}

/// Stores anchors in `UserDefaults`, under `prefix` + the type identifier.
public final class UserDefaultsAnchorStore: AnchorStore, @unchecked Sendable {
    private let defaults: UserDefaults
    private let prefix: String

    public init(defaults: UserDefaults = .standard, prefix: String = "OpenHealthSync.anchor.") {
        self.defaults = defaults
        self.prefix = prefix
    }

    public func anchorData(for typeIdentifier: String) -> Data? {
        return defaults.data(forKey: prefix + typeIdentifier)
    }

    public func setAnchorData(_ data: Data?, for typeIdentifier: String) {
        if let data = data {
            defaults.set(data, forKey: prefix + typeIdentifier)
        } else {
            defaults.removeObject(forKey: prefix + typeIdentifier)
        }
    }
}

/// Keeps anchors in memory only; for tests and previews.
public final class InMemoryAnchorStore: AnchorStore, @unchecked Sendable {
    private let lock = NSLock()
    private var anchors: [String: Data] = [:]

    public init() {}

    public func anchorData(for typeIdentifier: String) -> Data? {
        lock.lock()
        defer { lock.unlock() }
        return anchors[typeIdentifier]
    }

    public func setAnchorData(_ data: Data?, for typeIdentifier: String) {
        lock.lock()
        defer { lock.unlock() }
        anchors[typeIdentifier] = data
    }
}
