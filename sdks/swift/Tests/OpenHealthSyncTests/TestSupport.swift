// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import XCTest
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
#if canImport(Compression)
import Compression
#endif
@testable import OpenHealthSync

/// A transport that records requests and answers from a queue.
final class FakeTransport: HTTPTransport, @unchecked Sendable {
    enum Reply {
        case http(status: Int, body: String, headers: [String: String])
        case failure(Error)
    }

    private let lock = NSLock()
    private var replies: [Reply]
    private var recorded: [URLRequest] = []

    init(_ replies: [Reply]) {
        self.replies = replies
    }

    var requests: [URLRequest] {
        lock.lock()
        defer { lock.unlock() }
        return recorded
    }

    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        lock.lock()
        recorded.append(request)
        let reply: Reply = replies.isEmpty ? .http(status: 200, body: "{}", headers: [:]) : replies.removeFirst()
        lock.unlock()
        switch reply {
        case .failure(let error):
            throw error
        case .http(let status, let body, let headers):
            var fields = headers
            if fields["Content-Type"] == nil {
                fields["Content-Type"] = "application/json"
            }
            let response = HTTPURLResponse(
                url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: fields)!
            return (Data(body.utf8), response)
        }
    }
}

func ok(_ body: String, status: Int = 200) -> FakeTransport.Reply {
    return .http(status: status, body: body, headers: [:])
}

/// Records the waits of the retry loop instead of sleeping.
final class SleepRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var recorded: [Double] = []

    var delays: [Double] {
        lock.lock()
        defer { lock.unlock() }
        return recorded
    }

    var sleeper: Sleeper {
        return { [self] seconds in
            self.lock.lock()
            self.recorded.append(seconds)
            self.lock.unlock()
        }
    }
}

func makeClient(_ transport: FakeTransport, sleeps: SleepRecorder = SleepRecorder()) -> OHSClient {
    return OHSClient(
        baseURL: URL(string: "https://ohs.example.org/")!,
        token: "ohs_sk_test",
        transport: transport,
        sleeper: sleeps.sleeper)
}

/// Splits a request URL's query into (name, value) pairs, decoded.
func queryPairs(_ request: URLRequest) -> [String: String] {
    var out: [String: String] = [:]
    guard let url = request.url, let components = URLComponents(url: url, resolvingAgainstBaseURL: false) else {
        return out
    }
    for item in components.queryItems ?? [] {
        out[item.name] = item.value ?? ""
    }
    return out
}

enum TestGunzip {
    enum Failure: Error {
        case bad(String)
    }

    /// Decompresses one gzip member made by `Gzip.compress` and checks its CRC and size.
    static func gunzip(_ data: Data) throws -> Data {
        let bytes = [UInt8](data)
        guard bytes.count >= 18, bytes[0] == 0x1f, bytes[1] == 0x8b, bytes[2] == 8 else {
            throw Failure.bad("not gzip")
        }
        guard bytes[3] == 0 else {
            throw Failure.bad("unexpected header flags")
        }
        let body = Array(bytes[10..<(bytes.count - 8)])
        let trailer = Array(bytes[(bytes.count - 8)...])
        let out = try inflate(body)
        let crc = UInt32(trailer[0]) | UInt32(trailer[1]) << 8 | UInt32(trailer[2]) << 16 | UInt32(trailer[3]) << 24
        let size = UInt32(trailer[4]) | UInt32(trailer[5]) << 8 | UInt32(trailer[6]) << 16 | UInt32(trailer[7]) << 24
        guard crc == Gzip.crc32(out) else { throw Failure.bad("crc mismatch") }
        guard size == UInt32(truncatingIfNeeded: out.count) else { throw Failure.bad("size mismatch") }
        return out
    }

    static func inflate(_ body: [UInt8]) throws -> Data {
        if !body.isEmpty && (body[0] & 0x06) == 0 {
            return try inflateStored(body)
        }
        #if canImport(Compression)
        var capacity = max(1024, body.count * 20)
        while capacity < 1 << 30 {
            var out = [UInt8](repeating: 0, count: capacity)
            let cap = capacity
            let n = body.withUnsafeBufferPointer { src -> Int in
                out.withUnsafeMutableBufferPointer { dst -> Int in
                    compression_decode_buffer(dst.baseAddress!, cap, src.baseAddress!, body.count, nil, COMPRESSION_ZLIB)
                }
            }
            if n > 0 && n < capacity {
                return Data(out[0..<n])
            }
            capacity *= 4
        }
        throw Failure.bad("could not inflate")
        #else
        throw Failure.bad("compressed deflate blocks need the Compression framework")
        #endif
    }

    /// Reads a deflate stream made only of stored blocks.
    static func inflateStored(_ body: [UInt8]) throws -> Data {
        var out = Data()
        var i = 0
        while true {
            guard i + 5 <= body.count else { throw Failure.bad("truncated block header") }
            let header = body[i]
            guard (header & 0x06) == 0 else { throw Failure.bad("not a stored block") }
            let len = Int(body[i + 1]) | Int(body[i + 2]) << 8
            let nlen = Int(body[i + 3]) | Int(body[i + 4]) << 8
            guard len ^ 0xffff == nlen else { throw Failure.bad("LEN and NLEN disagree") }
            i += 5
            guard i + len <= body.count else { throw Failure.bad("truncated block") }
            out.append(contentsOf: body[i..<(i + len)])
            i += len
            if header & 1 == 1 {
                break
            }
        }
        guard i == body.count else { throw Failure.bad("bytes after the final block") }
        return out
    }

    /// Runs the system gunzip on `data` when there is one; nil when it is not installed.
    static func systemGunzip(_ data: Data) throws -> Data? {
        #if os(macOS) || os(Linux)
        let candidates = ["/usr/bin/gzip", "/bin/gzip"]
        guard let tool = candidates.first(where: { FileManager.default.isExecutableFile(atPath: $0) }) else {
            return nil
        }
        let file = FileManager.default.temporaryDirectory
            .appendingPathComponent("ohs-swift-\(UUID().uuidString).gz")
        try data.write(to: file)
        defer { try? FileManager.default.removeItem(at: file) }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: tool)
        process.arguments = ["-dc", file.path]
        let output = Pipe()
        process.standardOutput = output
        try process.run()
        let out = output.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        guard process.terminationStatus == 0 else {
            throw Failure.bad("gzip -dc exited with \(process.terminationStatus)")
        }
        return out
        #else
        return nil
        #endif
    }
}
