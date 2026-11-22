// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import XCTest
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
@testable import OpenHealthSync

final class AppRoutesTests: XCTestCase {
    func body(_ request: URLRequest) throws -> JSONValue {
        return try JSONValue.parse(try XCTUnwrap(request.httpBody))
    }

    func testCreateConnectSession() async throws {
        let transport = FakeTransport([
            ok(#"{"url":"https://ohs.example.org/connect?session=tok","expires_at":"2026-01-10T15:00:00Z"}"#, status: 201),
            ok(#"{"url":"https://ohs.example.org/connect?session=tok2","expires_at":"2026-01-10T15:00:00Z"}"#, status: 201),
        ])
        let client = makeClient(transport)
        let session = try await client.createConnectSession(
            userId: "u1", redirectUri: "myapp://connected", providers: ["oura", "withings"],
            appName: "My App", logoUrl: "https://example.org/logo.png", ttlSeconds: 600)
        XCTAssertEqual(session.url, "https://ohs.example.org/connect?session=tok")
        XCTAssertEqual(session.expiresAt, RFC3339.parse("2026-01-10T15:00:00Z"))
        let request = transport.requests[0]
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.path, "/v1/users/u1/connect-sessions")
        XCTAssertEqual(try body(request), [
            "redirect_uri": "myapp://connected", "providers": ["oura", "withings"],
            "app_name": "My App", "logo_url": "https://example.org/logo.png", "ttl_seconds": 600,
        ])

        // Nothing asked for: an empty object, no null member (the schema refuses unknown ones).
        _ = try await client.createConnectSession(userId: "u1")
        XCTAssertEqual(try body(transport.requests[1]), [:])
    }

    func testSampleStats() async throws {
        let transport = FakeTransport([ok(#"""
        {"type":"step_count","interval":"day","tz":"Europe/Paris","view":"merged",
         "start":"2026-03-01T00:00:00+01:00","end":"2026-03-03T00:00:00+01:00","truncated":false,
         "buckets":[{"start":"2026-03-01T00:00:00+01:00","provider":"apple_health","source":"Watch",
                     "unit":"count","count":12,"sum":8040,"avg":670,"min":10,"max":2100},
                    {"start":"2026-03-02T00:00:00+01:00","provider":"apple_health","source":"",
                     "unit":null,"count":1,"sum":5,"avg":5,"min":5,"max":5}]}
        """#)])
        let stats = try await makeClient(transport).sampleStats(
            userId: "u1", type: "step_count", start: "2026-03-01", end: "2026-03-03",
            interval: .day, timeZone: "Europe/Paris", provider: "apple_health", view: .merged)
        XCTAssertEqual(stats.buckets.count, 2)
        XCTAssertEqual(stats.buckets[0].sum, 8040)
        XCTAssertEqual(stats.buckets[0].start, RFC3339.parse("2026-02-28T23:00:00Z"))
        XCTAssertNil(stats.buckets[1].unit)
        XCTAssertFalse(stats.truncated)
        let request = transport.requests[0]
        XCTAssertEqual(request.url?.path, "/v1/users/u1/samples/stats")
        XCTAssertEqual(queryPairs(request), [
            "type": "step_count", "interval": "day", "tz": "Europe/Paris",
            "start": "2026-03-01", "end": "2026-03-03", "provider": "apple_health", "view": "merged",
        ])
    }

    func testRRIntervalsAndHRV() async throws {
        let transport = FakeTransport([
            ok(#"{"data":[{"timestamp":"2026-03-01T07:00:01Z","rr_ms":812.5,"series_id":"S1","provider":"apple_health","ingested_at":"2026-03-01T08:00:00Z"}],"next_cursor":"c2"}"#),
            ok(#"""
            {"data":[{"series_id":"S1","provider":"apple_health","start_date":"2026-03-01T07:00:00Z",
              "end_date":"2026-03-01T07:01:00Z","intervals":70,"accepted_intervals":68,"successive_pairs":66,
              "mean_rr_ms":820.1,"mean_heart_rate_bpm":73.2,"sdnn_ms":41.0,"rmssd_ms":35.5,"pnn50_percent":12.0},
             {"series_id":"S2","provider":"apple_health","start_date":"2026-03-02T07:00:00Z",
              "end_date":"2026-03-02T07:00:03Z","intervals":3,"accepted_intervals":0,"successive_pairs":0,
              "mean_rr_ms":null,"mean_heart_rate_bpm":null,"sdnn_ms":null,"rmssd_ms":null,"pnn50_percent":null}],
             "next_cursor":null}
            """#),
        ])
        let client = makeClient(transport)
        let rr = try await client.listRRIntervals(userId: "u1", seriesId: "S1", start: "2026-03-01", limit: 10)
        XCTAssertEqual(rr.data[0].rrMs, 812.5)
        XCTAssertEqual(rr.nextCursor, "c2")
        XCTAssertEqual(transport.requests[0].url?.path, "/v1/users/u1/rr-intervals")
        XCTAssertEqual(queryPairs(transport.requests[0]), ["series_id": "S1", "start": "2026-03-01", "limit": "10"])

        let hrv = try await client.listHRV(userId: "u1", cursor: "c9")
        XCTAssertEqual(hrv.data[0].rmssdMs, 35.5)
        XCTAssertEqual(hrv.data[0].acceptedIntervals, 68)
        XCTAssertNil(hrv.data[1].sdnnMs)
        XCTAssertEqual(transport.requests[1].url?.path, "/v1/users/u1/hrv")
        XCTAssertEqual(queryPairs(transport.requests[1]), ["cursor": "c9"])
    }

    func testECGRecordings() async throws {
        let recording = #""id":7,"start_date":"2026-03-01T07:00:00Z","end_date":"2026-03-01T07:00:30Z","classification":"sinus_rhythm","classification_value":1,"symptoms_status":"none","average_heart_rate":61.5,"sampling_frequency_hz":512,"number_of_measurements":15360,"metadata":{"timezone":"Europe/Paris"},"#
        let transport = FakeTransport([
            ok("{\"data\":[{\(recording)\(provenanceJSON)}],\"next_cursor\":null}"),
            ok("{\(recording)\(provenanceJSON),\"samples\":{\"count\":3,\"truncated\":false,\"offsets_s\":[0,0.001953125,0.00390625],\"voltages_uv\":[12.5,-3,40]}}"),
        ])
        let client = makeClient(transport)
        let page = try await client.listECGRecordings(userId: "u1", start: "2026-03-01", end: "2026-03-02")
        XCTAssertEqual(page.data[0].id, 7)
        XCTAssertEqual(page.data[0].classification, "sinus_rhythm")
        XCTAssertEqual(page.data[0].samplingFrequencyHz, 512)
        XCTAssertEqual(page.data[0].provider, "apple_health")
        XCTAssertEqual(transport.requests[0].url?.path, "/v1/users/u1/ecg")

        let detail = try await client.getECGRecording(userId: "u1", ecgId: 7)
        XCTAssertEqual(detail.recording.numberOfMeasurements, 15360)
        XCTAssertEqual(detail.samples.offsetsS.count, 3)
        XCTAssertEqual(detail.samples.voltagesUv, [12.5, -3, 40])
        XCTAssertFalse(detail.samples.truncated)
        XCTAssertEqual(transport.requests[1].url?.path, "/v1/users/u1/ecg/7")
    }

    let tagJSON = #"{"id":"0b6f0c58-3a55-4f0e-9d39-2f1f6d1c2a10","kind":"travel","label":"Tokyo","start_at":"2026-03-01T00:00:00Z","end_at":null,"timezone":"Asia/Tokyo","note":null,"source":"my-app","created_at":"2026-03-01T09:00:00Z","updated_at":"2026-03-01T09:00:00Z"}"#

    func testTimeTags() async throws {
        let transport = FakeTransport([
            ok(tagJSON, status: 201),
            ok("{\"data\":[\(tagJSON)],\"next_cursor\":null}"),
            ok(tagJSON),
            ok(tagJSON),
            .http(status: 204, body: "", headers: [:]),
        ])
        let client = makeClient(transport)
        let created = try await client.createTimeTag(
            userId: "u1",
            tag: NewTimeTag(kind: .travel, startAt: "2026-03-01", label: "Tokyo", timeZone: "Asia/Tokyo", source: "my-app"),
            idempotencyKey: "k1")
        XCTAssertEqual(created.kind, "travel")
        XCTAssertNil(created.endAt)
        XCTAssertEqual(transport.requests[0].httpMethod, "POST")
        XCTAssertEqual(transport.requests[0].url?.path, "/v1/users/u1/tags")
        XCTAssertEqual(transport.requests[0].value(forHTTPHeaderField: "Idempotency-Key"), "k1")
        XCTAssertEqual(try body(transport.requests[0]), [
            "kind": "travel", "start_at": "2026-03-01", "label": "Tokyo", "timezone": "Asia/Tokyo", "source": "my-app",
        ])

        let page = try await client.listTimeTags(userId: "u1", kind: .travel, start: "2026-03-01", end: "2026-04-01")
        XCTAssertEqual(page.data.count, 1)
        XCTAssertEqual(queryPairs(transport.requests[1]), ["kind": "travel", "start": "2026-03-01", "end": "2026-04-01"])

        _ = try await client.getTimeTag(userId: "u1", tagId: created.id)
        XCTAssertEqual(transport.requests[2].url?.path, "/v1/users/u1/tags/0b6f0c58-3a55-4f0e-9d39-2f1f6d1c2a10")

        // Absent members stay; a cleared one is sent as null.
        var change = TimeTagChange()
        change.endAt = .set("2026-03-10T00:00:00Z")
        change.note = .clear
        _ = try await client.updateTimeTag(userId: "u1", tagId: created.id, change: change)
        XCTAssertEqual(transport.requests[3].httpMethod, "PATCH")
        XCTAssertEqual(try body(transport.requests[3]), ["end_at": "2026-03-10T00:00:00Z", "note": .null])

        try await client.deleteTimeTag(userId: "u1", tagId: created.id)
        XCTAssertEqual(transport.requests[4].httpMethod, "DELETE")
    }

    func testCreateSelfReports() async throws {
        let transport = FakeTransport([ok(#"""
        {"reports":[{"id":"9a1b2c3d-0000-4000-8000-000000000001","kind":"mood","type":"reported_mood","value":4,
                     "start_at":"2026-03-01T07:00:00Z","end_at":null,"timezone":"Europe/Paris"}],"duplicate":false}
        """#, status: 202)])
        let client = makeClient(transport)
        let at = RFC3339.parse("2026-03-01T07:00:00Z")!
        let result = try await client.createSelfReports(
            userId: "u1",
            reports: [NewSelfReport(kind: .mood, value: 4, startAt: at, timeZone: "Europe/Paris")],
            idempotencyKey: "k2")
        XCTAssertEqual(result.reports[0].type, "reported_mood")
        XCTAssertEqual(result.reports[0].value, 4)
        XCTAssertFalse(result.duplicate)
        let request = transport.requests[0]
        XCTAssertEqual(request.url?.path, "/v1/users/u1/self-reports")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Idempotency-Key"), "k2")
        XCTAssertEqual(try body(request), ["reports": [
            ["kind": "mood", "value": 4, "start_at": .string(RFC3339.format(at)), "timezone": "Europe/Paris"],
        ]])

        do {
            _ = try await client.createSelfReports(userId: "u1", reports: [])
            XCTFail("an empty list must be refused before any request")
        } catch OHSError.invalidArgument {
        }
        XCTAssertEqual(transport.requests.count, 1)
    }
}
