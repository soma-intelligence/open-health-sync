// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import Security

/// Where to upload and with which user token, kept between launches because HealthKit wakes
/// the app for a background sync when no Dart code has run yet.
///
/// The token is a generic password in the keychain, readable after the first unlock (a
/// background sync runs while the device is locked again) and never copied to another
/// device. The base URL is in `UserDefaults`. Nothing here is logged.
final class CredentialStore: @unchecked Sendable {
    private let defaults: UserDefaults
    private let service = "org.openhealthsync.flutter.health"
    private let account = "user_token"
    private let baseURLKey = "OpenHealthSync.flutter.baseURL"

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    /// The base URL of the server; nil when no credentials are stored.
    var baseURL: String? {
        return defaults.string(forKey: baseURLKey)
    }

    /// The user token; nil when none is stored or the keychain cannot be read yet (before
    /// the first unlock after a restart).
    var token: String? {
        var query = baseQuery()
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data
        else {
            return nil
        }
        return String(data: data, encoding: .utf8)
    }

    /// Stores the credentials. Returns false when the keychain refused the token.
    func save(baseURL: String, token: String) -> Bool {
        SecItemDelete(baseQuery() as CFDictionary)
        var item = baseQuery()
        item[kSecValueData as String] = Data(token.utf8)
        item[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        guard SecItemAdd(item as CFDictionary, nil) == errSecSuccess else {
            return false
        }
        defaults.set(baseURL, forKey: baseURLKey)
        return true
    }

    func clear() {
        SecItemDelete(baseQuery() as CFDictionary)
        defaults.removeObject(forKey: baseURLKey)
    }

    private func baseQuery() -> [String: Any] {
        return [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }
}
