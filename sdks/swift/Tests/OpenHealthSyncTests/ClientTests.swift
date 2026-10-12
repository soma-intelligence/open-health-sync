// SPDX-License-Identifier: MIT

import Foundation
import XCTest
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
@testable import OpenHealthSync

let provenanceJSON = #""provider":"apple_health","source":"apple_health","device":"Apple Watch","ingested_at":"2026-01-10T14:31:02.418Z""#

final class ClientRequestTests: XCTestCase {
    func testHeadersAndPath() async throws {
        let transport = FakeTransport([ok(#"{"data":[],"next_cursor":null}"#)])
        let client = makeClient(transport)
        _ = try await client.listSamples(
            userId: "3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11", type: "heart_rate",
            start: "2026-03-01T00:00:00+01:00", end: OHSTime(Date(timeIntervalSince1970: 1_772_409_600)),
            limit: 500)
        let request = try XCTUnwrap(transport.requests.first)
        XCTAssertEqual(request.httpMethod, "GET")
        XCTAssertEqual(request.url?.path, "/v1/users/3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11/samples")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer ohs_sk_test")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Accept"), "application/json")
        XCTAssertEqual(request.value(forHTTPHeaderField: "User-Agent"), "open-health-sync-swift/0.1.0")
        XCTAssertEqual(queryPairs(request), [
            "type": "heart_rate", "start": "2026-03-01T00:00:00+01:00",
            "end": "2026-03-02T00:00:00.000Z", "limit": "500",
        ])
        // '+' must be percent-encoded, or the server reads a space.
        XCTAssertTrue(request.url?.absoluteString.contains("%2B01%3A00") ?? false)
        XCTAssertFalse(request.url?.absoluteString.contains("cursor") ?? true)
    }

    func testPathSegmentsAreEncoded() async throws {
        let transport = FakeTransport([ok(#"{"id":"a/b","external_ref":null,"status":"active","created_at":"2026-01-10T14:30:00Z"}"#)])
        _ = try await makeClient(transport).getUser(userId: "a/b")
        XCTAssertTrue(transport.requests[0].url?.absoluteString.hasSuffix("/v1/users/a%2Fb") ?? false)
    }

    func testCreateUserSendsIdempotencyKeyAndBody() async throws {
        let transport = FakeTransport([ok(#"{"id":"u1","external_ref":"crm-42","status":"active","created_at":"2026-01-10T14:30:00Z"}"#, status: 201)])
        let user = try await makeClient(transport).createUser(externalRef: "crm-42", idempotencyKey: "key-1")
        XCTAssertEqual(user.externalRef, "crm-42")
        let request = transport.requests[0]
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.path, "/v1/users")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Idempotency-Key"), "key-1")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Content-Type"), "application/json")
        XCTAssertEqual(try JSONValue.parse(try XCTUnwrap(request.httpBody)), ["external_ref": "crm-42"])
    }

    func testMintUserToken() async throws {
        let transport = FakeTransport([ok(#"{"token":"ohs_ut_x","expires_at":"2026-01-10T15:30:00Z","scopes":["upload","read"]}"#, status: 201)])
        let token = try await makeClient(transport).mintUserToken(userId: "u1", scopes: [.upload, .read], ttlSeconds: 600)
        XCTAssertEqual(token.scopes, ["upload", "read"])
        XCTAssertEqual(transport.requests[0].url?.path, "/v1/users/u1/tokens")
        XCTAssertEqual(try JSONValue.parse(try XCTUnwrap(transport.requests[0].httpBody)), ["scopes": ["upload", "read"], "ttl_seconds": 600])
    }

    func testConnectionCalls() async throws {
        let transport = FakeTransport([
            ok(#"{"authorize_url":"https://provider.example/authorize?state=s"}"#),
            .http(status: 204, body: "", headers: [:]),
        ])
        let client = makeClient(transport)
        let url = try await client.startConnection(userId: "u1", provider: "oura", redirectUri: "myapp://done")
        XCTAssertEqual(url, "https://provider.example/authorize?state=s")
        try await client.deleteConnection(userId: "u1", provider: "oura")
        XCTAssertEqual(transport.requests[0].url?.path, "/v1/users/u1/connections/oura")
        XCTAssertEqual(try JSONValue.parse(try XCTUnwrap(transport.requests[0].httpBody)), ["redirect_uri": "myapp://done"])
        XCTAssertEqual(transport.requests[1].httpMethod, "DELETE")
    }

    func testDeleteUserAndDeletionJob() async throws {
        let transport = FakeTransport([
            ok(#"{"job_id":"j1"}"#, status: 202),
            ok(#"{"job_id":"j1","user_id":"u1","status":"done","created_at":"2026-01-10T14:30:00Z","finished_at":"2026-01-10T14:30:05.5Z"}"#),
        ])
        let client = makeClient(transport)
        let jobId = try await client.deleteUser(userId: "u1")
        XCTAssertEqual(jobId, "j1")
        let job = try await client.getDeletion(jobId: jobId)
        XCTAssertEqual(job.status, "done")
        XCTAssertNotNil(job.finishedAt)
        XCTAssertEqual(transport.requests[1].url?.path, "/v1/deletions/j1")
    }

    func testTokenProviderIsCalledForEveryRequest() async throws {
        let counter = Counter()
        let transport = FakeTransport([ok(#"{"data":[]}"#), ok(#"{"data":[]}"#)])
        let client = OHSClient(
            baseURL: URL(string: "https://ohs.example.org")!,
            tokenProvider: { "token-\(counter.next())" },
            transport: transport)
        _ = try await client.listProviders()
        _ = try await client.listProviders()
        XCTAssertEqual(transport.requests.map { $0.value(forHTTPHeaderField: "Authorization") ?? "" }, ["Bearer token-1", "Bearer token-2"])
    }

    func testPaginationWalksEveryPage() async throws {
        let transport = FakeTransport([
            ok(#"{"data":[{"id":"u1","external_ref":null,"status":"active","created_at":"2026-01-10T14:30:00Z"}],"next_cursor":"c2"}"#),
            ok(#"{"data":[{"id":"u2","external_ref":null,"status":"active","created_at":"2026-01-10T14:30:00Z"}],"next_cursor":"c3"}"#),
            ok(#"{"data":[{"id":"u3","external_ref":"x","status":"deleting","created_at":"2026-01-10T14:30:00Z"}],"next_cursor":null}"#),
        ])
        let client = makeClient(transport)
        var ids: [String] = []
        for try await user in OHSClient.items({ cursor in try await client.listUsers(cursor: cursor, limit: 1) }) {
            ids.append(user.id)
        }
        XCTAssertEqual(ids, ["u1", "u2", "u3"])
        XCTAssertEqual(transport.requests.map { queryPairs($0)["cursor"] ?? "-" }, ["-", "c2", "c3"])

        let pages = FakeTransport([ok(#"{"data":[],"next_cursor":"n"}"#), ok(#"{"data":[],"next_cursor":null}"#)])
        let other = makeClient(pages)
        var count = 0
        for try await _ in OHSClient.pages({ cursor in try await other.listSleep(userId: "u", cursor: cursor) }) {
            count += 1
        }
        XCTAssertEqual(count, 2)
    }

    func testFHIRBundle() async throws {
        let bundle = #"""
        {"resourceType":"Bundle","type":"searchset",
         "link":[{"relation":"self","url":"https://ohs.example.org/v1/users/u1/fhir/Observation?limit=1"},
                 {"relation":"next","url":"https://ohs.example.org/v1/users/u1/fhir/Observation?cursor=abc%3D%3D&limit=1"}],
         "entry":[{"fullUrl":"https://ohs.example.org/v1/users/u1/fhir/Observation/qs-1",
                   "resource":{"resourceType":"Observation","id":"qs-1","status":"final",
                               "code":{"coding":[{"system":"http://loinc.org","code":"8867-4"}]},
                               "subject":{"reference":"Patient/u1"},
                               "valueQuantity":{"value":72.5,"unit":"/min","system":"http://unitsofmeasure.org","code":"/min"}}}]}
        """#
        let transport = FakeTransport([ok(bundle), ok(#"{"resourceType":"Observation","id":"qs-1","status":"final"}"#)])
        let client = makeClient(transport)
        let page = try await client.searchFHIRObservations(userId: "u1", code: "http://loinc.org|8867-4", limit: 1)
        XCTAssertEqual(page.nextCursor, "abc==")
        XCTAssertEqual(page.observations.count, 1)
        XCTAssertEqual(page.observations[0]["valueQuantity"]?["value"], .number(72.5))
        XCTAssertEqual(page.raw["resourceType"], "Bundle")
        XCTAssertEqual(transport.requests[0].value(forHTTPHeaderField: "Accept"), "application/fhir+json")
        XCTAssertEqual(queryPairs(transport.requests[0])["code"], "http://loinc.org|8867-4")

        let observation = try await client.readFHIRObservation(userId: "u1", observationId: "qs-1")
        XCTAssertEqual(observation["id"], "qs-1")
        XCTAssertEqual(transport.requests[1].url?.path, "/v1/users/u1/fhir/Observation/qs-1")

        XCTAssertNil(FHIRBundle(raw: ["resourceType": "Bundle", "link": [["relation": "self", "url": "https://x/?limit=1"]]]).nextCursor)
    }
}

final class ClientErrorTests: XCTestCase {
    func testProblemJSON() async throws {
        let transport = FakeTransport([.http(
            status: 404,
            body: #"{"type":"about:blank","title":"Not Found","status":404,"detail":"no such user","request_id":"req-1"}"#,
            headers: ["Content-Type": "application/problem+json", "X-Request-Id": "req-header"])])
        do {
            _ = try await makeClient(transport).getUser(userId: "u1")
            XCTFail("expected an error")
        } catch let error as OHSAPIError {
            XCTAssertEqual(error, OHSAPIError(status: 404, title: "Not Found", detail: "no such user", type: "about:blank", requestId: "req-1"))
            XCTAssertFalse(error.description.contains("ohs_sk_test"))
        }
    }

    func testNonJSONBody() async throws {
        let transport = FakeTransport([.http(status: 502, body: "<html>bad gateway</html>", headers: ["Content-Type": "text/html", "X-Request-Id": "req-2"])])
        do {
            _ = try await makeClient(transport).listProviders()
            XCTFail("expected an error")
        } catch let error as OHSAPIError {
            XCTAssertEqual(error.status, 502)
            XCTAssertNil(error.title)
            XCTAssertEqual(error.requestId, "req-2")
        }
    }

    func testBadSuccessBodyIsInvalidResponse() async throws {
        let transport = FakeTransport([ok(#"{"id":"u1"}"#)])
        do {
            _ = try await makeClient(transport).getUser(userId: "u1")
            XCTFail("expected an error")
        } catch let error as OHSError {
            guard case .invalidResponse = error else {
                return XCTFail("unexpected \(error)")
            }
        }
    }
}

final class UploadTests: XCTestCase {
    func part() throws -> AppleHealthPart {
        var builder = try AppleHealthPartBuilder(batch: "batch-1")
        try builder.add(type: "HKQuantityTypeIdentifierHeartRate", day: "2026-01-10", line: ["value": 72])
        return builder.build()[0]
    }

    let stored = #"{"key":"raw/t/u/apple_health/2026-01-10/batch-1_0.ndjson.gz","duplicate":false}"#

    func testUploadHeaders() async throws {
        let transport = FakeTransport([ok(stored)])
        let p = try part()
        let result = try await makeClient(transport).uploadAppleHealthPart(p)
        XCTAssertFalse(result.duplicate)
        let request = transport.requests[0]
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.path, "/v1/ingest/apple-health")
        XCTAssertEqual(request.value(forHTTPHeaderField: "X-OHS-Type"), "HKQuantityTypeIdentifierHeartRate")
        XCTAssertEqual(request.value(forHTTPHeaderField: "X-OHS-Day"), "2026-01-10")
        XCTAssertEqual(request.value(forHTTPHeaderField: "X-OHS-Batch"), "batch-1")
        XCTAssertEqual(request.value(forHTTPHeaderField: "X-OHS-Part"), "0")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Content-Type"), "application/gzip")
        XCTAssertEqual(request.httpBody, p.body)
    }

    func testRetriesAfter503WithRetryAfter() async throws {
        let sleeps = SleepRecorder()
        let transport = FakeTransport([
            .http(status: 503, body: #"{"title":"Service Unavailable","status":503}"#, headers: ["Retry-After": "7"]),
            ok(stored),
        ])
        let result = try await makeClient(transport, sleeps: sleeps).uploadAppleHealthPart(try part())
        XCTAssertEqual(result.key, "raw/t/u/apple_health/2026-01-10/batch-1_0.ndjson.gz")
        XCTAssertEqual(transport.requests.count, 2)
        XCTAssertEqual(sleeps.delays, [7])
    }

    func testRetriesNetworkErrorsAnd429() async throws {
        let sleeps = SleepRecorder()
        let transport = FakeTransport([
            .failure(URLError(.notConnectedToInternet)),
            .http(status: 429, body: "", headers: [:]),
            ok(stored),
        ])
        _ = try await makeClient(transport, sleeps: sleeps).uploadAppleHealthPart(try part())
        XCTAssertEqual(transport.requests.count, 3)
        XCTAssertEqual(sleeps.delays, [1, 2])
    }

    func testDoesNotRetry400() async throws {
        let sleeps = SleepRecorder()
        let transport = FakeTransport([.http(status: 400, body: #"{"title":"Bad Request","status":400,"detail":"X-OHS-Day is invalid"}"#, headers: [:])])
        do {
            _ = try await makeClient(transport, sleeps: sleeps).uploadAppleHealthPart(try part())
            XCTFail("expected an error")
        } catch let error as OHSAPIError {
            XCTAssertEqual(error.status, 400)
        }
        XCTAssertEqual(transport.requests.count, 1)
        XCTAssertEqual(sleeps.delays, [])
    }

    func testGivesUpAfterFiveAttempts() async throws {
        let sleeps = SleepRecorder()
        let transport = FakeTransport(Array(repeating: .http(status: 500, body: "", headers: [:]), count: 6))
        do {
            _ = try await makeClient(transport, sleeps: sleeps).uploadAppleHealthPart(try part())
            XCTFail("expected an error")
        } catch let error as OHSAPIError {
            XCTAssertEqual(error.status, 500)
        }
        XCTAssertEqual(transport.requests.count, 5)
        XCTAssertEqual(sleeps.delays, [1, 2, 4, 8])
    }

    func testReadsAreNotRetried() async throws {
        let sleeps = SleepRecorder()
        let transport = FakeTransport([.http(status: 503, body: "", headers: [:]), ok(#"{"data":[]}"#)])
        do {
            _ = try await makeClient(transport, sleeps: sleeps).listProviders()
            XCTFail("expected an error")
        } catch let error as OHSAPIError {
            XCTAssertEqual(error.status, 503)
        }
        XCTAssertEqual(transport.requests.count, 1)
    }

    func testBackoffIsCapped() {
        XCTAssertEqual((1...8).map { OHSClient.backoff(attempt: $0) }, [1, 2, 4, 8, 16, 30, 30, 30])
    }

    func testWaitUntilProcessed() async throws {
        let sleeps = SleepRecorder()
        let transport = FakeTransport([
            ok(#"{"pending_jobs":2,"parked_jobs":0,"rejected_records":0,"truncated":false,"days":[]}"#),
            ok(#"{"pending_jobs":0,"parked_jobs":0,"rejected_records":0,"truncated":false,"days":[{"provider":"apple_health","date":"2026-01-10","records":1,"metrics":{"heart_rate":1},"last_synced_at":"2026-01-10T14:31:02.418123Z"}]}"#),
        ])
        let coverage = try await makeClient(transport, sleeps: sleeps).waitUntilProcessed(userId: "u1", timeout: 60, pollInterval: 0.5)
        XCTAssertEqual(coverage.days.first?.metrics["heart_rate"], 1)
        XCTAssertEqual(sleeps.delays, [0.5])
    }
}

final class Counter: @unchecked Sendable {
    private let lock = NSLock()
    private var value = 0

    func next() -> Int {
        lock.lock()
        defer { lock.unlock() }
        value += 1
        return value
    }
}
