// SPDX-License-Identifier: MIT

import Foundation

/// A non-2xx answer from the server. It carries the RFC 9457 fields of the problem body when
/// there is one, and never the request's credentials.
public struct OHSAPIError: Error, Sendable, Equatable, CustomStringConvertible {
    /// The HTTP status.
    public let status: Int
    /// The problem `title`.
    public let title: String?
    /// The problem `detail`.
    public let detail: String?
    /// The problem `type`.
    public let type: String?
    /// The request id to quote when reporting the error: the body's `request_id`, else the
    /// `X-Request-Id` header.
    public let requestId: String?
    /// The `Retry-After` header in seconds, when the server sent one.
    public let retryAfterSeconds: Int?

    public init(
        status: Int,
        title: String? = nil,
        detail: String? = nil,
        type: String? = nil,
        requestId: String? = nil,
        retryAfterSeconds: Int? = nil
    ) {
        self.status = status
        self.title = title
        self.detail = detail
        self.type = type
        self.requestId = requestId
        self.retryAfterSeconds = retryAfterSeconds
    }

    public var description: String {
        var s = "Open Health Sync API error \(status)"
        if let title = title, !title.isEmpty { s += " \(title)" }
        if let detail = detail, !detail.isEmpty { s += ": \(detail)" }
        if let requestId = requestId, !requestId.isEmpty { s += " (request id \(requestId))" }
        return s
    }
}

/// An error raised by the SDK itself, before or after talking to the server.
public enum OHSError: Error, Sendable, Equatable, CustomStringConvertible {
    /// An argument was rejected before any request was sent.
    case invalidArgument(String)
    /// A 2xx answer whose body is not what the API describes.
    case invalidResponse(String)
    /// A wait (for example a polling loop) ran out of time.
    case timeout(String)

    public var description: String {
        switch self {
        case .invalidArgument(let m):
            return "invalid argument: \(m)"
        case .invalidResponse(let m):
            return "invalid response: \(m)"
        case .timeout(let m):
            return "timeout: \(m)"
        }
    }
}
