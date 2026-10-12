// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import OpenHealthSync
import OpenHealthSyncHealthKit

/// Errors of the example itself.
enum ExampleError: LocalizedError {
    case missingToken
    case healthUnavailable
    case noTypes

    var errorDescription: String? {
        switch self {
        case .missingToken:
            return "No user token. Paste one in Settings."
        case .healthUnavailable:
            return "Health data is not available on this device."
        case .noTypes:
            return "Pick at least one type to sync."
        }
    }
}

/// Turns an error into lines to show on screen. Never includes the token or a health value:
/// the SDK errors hold neither.
enum ErrorText {
    static func lines(for error: Error) -> [String] {
        if let api = error as? OHSAPIError {
            var lines = ["HTTP \(api.status)" + (api.title.map { ": \($0)" } ?? "")]
            if let detail = api.detail, !detail.isEmpty {
                lines.append(detail)
            }
            if let requestId = api.requestId {
                lines.append("Request id: \(requestId)")
            }
            if api.status == 401 {
                lines.append("The token is wrong or expired. Mint a new one and paste it in Settings.")
            } else if api.status == 403 {
                lines.append("The token needs the upload scope.")
            }
            return lines
        }
        if let sdk = error as? OHSError {
            return [sdk.description]
        }
        if let health = error as? HealthKitSyncError, case .refused(let what) = health {
            return ["HealthKit refused the \(what)."]
        }
        if let network = error as? URLError {
            return [
                "Network error: \(network.localizedDescription)",
                "Check the server URL in Settings and that the server is running.",
            ]
        }
        return [error.localizedDescription]
    }
}
