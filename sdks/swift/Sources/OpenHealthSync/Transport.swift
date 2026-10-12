// SPDX-License-Identifier: MIT

import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

/// Sends one HTTP request and returns the body and the response, whatever the status.
/// Throw only for a network failure (no response at all). Tests inject their own.
public protocol HTTPTransport: Sendable {
    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse)
}

/// The default transport, on `URLSession`. It wraps `dataTask` rather than the async
/// `data(for:)`, which older Foundation releases on Linux do not have.
public final class URLSessionTransport: HTTPTransport, @unchecked Sendable {
    public let session: URLSession

    public init(session: URLSession = .shared) {
        self.session = session
    }

    public func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let box = DataTaskBox()
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<(Data, HTTPURLResponse), Error>) in
                let task = session.dataTask(with: request) { data, response, error in
                    if let error = error {
                        continuation.resume(throwing: error)
                        return
                    }
                    guard let http = response as? HTTPURLResponse else {
                        continuation.resume(throwing: URLError(.badServerResponse))
                        return
                    }
                    continuation.resume(returning: (data ?? Data(), http))
                }
                box.set(task)
                task.resume()
            }
        } onCancel: {
            box.cancel()
        }
    }
}

/// Holds the running data task so a cancelled Swift task can cancel it.
private final class DataTaskBox: @unchecked Sendable {
    private let lock = NSLock()
    private var task: URLSessionDataTask?
    private var cancelled = false

    func set(_ task: URLSessionDataTask) {
        lock.lock()
        self.task = task
        let wasCancelled = cancelled
        lock.unlock()
        if wasCancelled {
            task.cancel()
        }
    }

    func cancel() {
        lock.lock()
        cancelled = true
        let task = self.task
        lock.unlock()
        task?.cancel()
    }
}

/// Waits the given number of seconds. Injected so tests of the retry logic do not sleep.
public typealias Sleeper = @Sendable (_ seconds: Double) async throws -> Void

/// Returns the bearer credential for the next request. Called before every request, so it
/// can hand out a fresh user token when the previous one expired.
public typealias TokenProvider = @Sendable () async throws -> String
