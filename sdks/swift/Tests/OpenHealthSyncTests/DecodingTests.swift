// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
import XCTest
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
@testable import OpenHealthSync

/// Decodes a realistic answer for every record type. Values are invented.
final class DecodingTests: XCTestCase {
    func page(_ record: String, next: String = "null") -> String {
        return #"{"data":["# + record + #"],"next_cursor":"# + next + "}"
    }

    func client(_ body: String) -> (OHSClient, FakeTransport) {
        let transport = FakeTransport([ok(body)])
        return (makeClient(transport), transport)
    }

    func testSample() async throws {
        let (c, _) = client(page(#"""
        {"id":9007199254740993,"type":"heart_rate","value":72.5,"unit":"bpm",
         "start_date":"2026-01-10T14:30:00.123456789Z","end_date":"2026-01-10T14:30:00Z",
         "source_name":"Apple Watch","source_bundle_id":"com.apple.health.ABC","device_model":"Watch",
         "product_type":"Watch7,1","external_id":"","is_primary_source":false,"was_user_entered":false,
         "metadata":{"timezone":"Europe/Paris","original_value":72.5,"original_unit":"count/min"},
         "provider":"apple_health","source":null,"device":"Apple Watch","ingested_at":"2026-01-10T14:31:02.418Z",
         "field_added_later":1}
        """#, next: #""c2""#))
        let p = try await c.listSamples(userId: "u", type: "heart_rate")
        XCTAssertEqual(p.nextCursor, "c2")
        let s = try XCTUnwrap(p.data.first)
        XCTAssertEqual(s.id, 9_007_199_254_740_993)
        XCTAssertEqual(s.value, 72.5)
        XCTAssertEqual(s.unit, "bpm")
        XCTAssertEqual(s.startDate.timeIntervalSince1970, 1_768_055_400.123456789, accuracy: 1e-6)
        XCTAssertEqual(s.metadata["timezone"], "Europe/Paris")
        XCTAssertEqual(s.metadata["original_value"], .number(72.5))
        XCTAssertNil(s.source)
        XCTAssertEqual(s.wasUserEntered, false)
        XCTAssertEqual(s.provider, "apple_health")
    }

    func testCategorySample() async throws {
        let (c, t) = client(page(#"{"id":3,"type":"menstrual_flow","value_int":2,"value_label":"light","start_date":"2026-01-10T00:00:00Z","end_date":null,"source_name":null,"external_id":null,"metadata":{},"# + provenanceJSON + "}"))
        let p = try await c.listCategorySamples(userId: "u", type: "menstrual_flow", start: "2026-01-01")
        XCTAssertEqual(p.data[0].valueInt, 2)
        XCTAssertEqual(p.data[0].valueLabel, "light")
        XCTAssertNil(p.data[0].endDate)
        XCTAssertEqual(t.requests[0].url?.path, "/v1/users/u/category-samples")
    }

    func testSleepAndPhases() async throws {
        let (c, _) = client(page(#"{"id":77,"bedtime_start":"2026-01-09T22:30:00Z","bedtime_end":"2026-01-10T06:30:00Z","total_sleep_seconds":27000,"deep_sleep_seconds":5400,"rem_sleep_seconds":6300,"light_sleep_seconds":15300,"awake_seconds":1800,"sleep_score":82,"hrv_avg_ms":48.5,"resting_hr":52,"respiratory_rate":14.2,"spo2_avg":96.5,"external_id":"s-1","metadata":{"timezone":"Europe/Paris"},"# + provenanceJSON + "}"))
        let s = try await c.listSleep(userId: "u").data[0]
        XCTAssertEqual(s.id, 77)
        XCTAssertEqual(s.totalSleepSeconds, 27000)
        XCTAssertEqual(s.hrvAvgMs, 48.5)
        XCTAssertEqual(s.spo2Avg, 96.5)

        let (c2, t2) = client(page(#"{"id":1,"session_id":77,"phase":"deep","start_date":"2026-01-10T01:00:00Z","end_date":"2026-01-10T01:30:00Z","duration_s":1800,"# + provenanceJSON + "}"))
        let phase = try await c2.listSleepPhases(userId: "u", sessionId: 77).data[0]
        XCTAssertEqual(phase.phase, "deep")
        XCTAssertEqual(phase.durationS, 1800)
        XCTAssertEqual(t2.requests[0].url?.path, "/v1/users/u/sleep/77/phases")
    }

    func testWorkoutAndRoute() async throws {
        let (c, t) = client(page(#"{"id":12,"activity_type":"running","start_date":"2026-01-10T07:00:00Z","end_date":"2026-01-10T07:45:00Z","duration_s":2700,"active_energy_kcal":480,"distance_m":7200.5,"avg_heart_rate":132,"max_heart_rate":165,"avg_speed_mps":2.67,"elevation_gain_m":35,"vo2_max":null,"external_id":null,"metadata":{"minHeartRate":95},"# + provenanceJSON + "}"))
        let w = try await c.listWorkouts(userId: "u", type: "running").data[0]
        XCTAssertEqual(w.activityType, "running")
        XCTAssertEqual(w.durationS, 2700)
        XCTAssertEqual(w.distanceM, 7200.5)
        XCTAssertNil(w.vo2Max)
        XCTAssertEqual(queryPairs(t.requests[0])["type"], "running")

        let (c2, t2) = client(page(#"{"id":5,"workout_id":12,"timestamp":"2026-01-10T07:00:01Z","latitude":48.8566,"longitude":2.3522,"altitude_m":35.2,"speed_mps":2.7,"course_deg":null,"# + provenanceJSON + "}"))
        let point = try await c2.getWorkoutRoute(userId: "u", workoutId: 12).data[0]
        XCTAssertEqual(point.latitude, 48.8566)
        XCTAssertNil(point.courseDeg)
        XCTAssertEqual(t2.requests[0].url?.path, "/v1/users/u/workouts/12/route")
    }

    func testNutritionAndDaily() async throws {
        let (c, _) = client(page(#"{"id":8,"start_date":"2026-01-10T12:00:00Z","end_date":null,"meal_type":"lunch","energy_kcal":650,"protein_g":32,"carbs_g":70,"fat_g":21,"fiber_g":8,"sugar_g":12,"sodium_mg":900,"nutrients":{"caffeine":{"value":80,"unit":"mg"}},"external_id":null,"metadata":{},"# + provenanceJSON + "}"))
        let n = try await c.listNutrition(userId: "u").data[0]
        XCTAssertEqual(n.mealType, "lunch")
        XCTAssertEqual(n.nutrients?["caffeine"]?["value"], .number(80))

        let (c2, _) = client(page(#"{"id":4,"date":"2026-01-10","readiness_score":null,"activity_score":null,"sleep_score":null,"recovery_score":null,"strain_score":null,"step_count":10234,"active_kcal":540.5,"total_kcal":2310,"distance_m":7800,"floors_climbed":9,"resting_hr":55,"hrv_avg_ms":null,"metadata":{"timezone":"Europe/Paris"},"# + provenanceJSON + "}"))
        let d = try await c2.listDaily(userId: "u", start: "2026-01-01", end: "2026-02-01").data[0]
        XCTAssertEqual(d.date, "2026-01-10")
        XCTAssertEqual(d.stepCount, 10234)
        XCTAssertNil(d.readinessScore)
    }

    func testCoverageFreshnessConnectionsProviders() async throws {
        let (c, t) = client(#"{"pending_jobs":0,"parked_jobs":1,"rejected_records":3,"truncated":false,"days":[{"provider":"apple_health","date":"2026-01-10","records":5,"metrics":{"heart_rate":4,"step_count":1},"last_synced_at":"2026-01-10T14:31:02Z"}]}"#)
        let coverage = try await c.getCoverage(userId: "u", provider: "apple_health")
        XCTAssertEqual(coverage.rejectedRecords, 3)
        XCTAssertEqual(coverage.days[0].metrics, ["heart_rate": 4, "step_count": 1])
        XCTAssertEqual(queryPairs(t.requests[0]), ["provider": "apple_health"])

        let (c2, _) = client(#"{"as_of":"2026-01-10T15:00:00.5Z","data":[{"provider":"oura","kind":"oauth","status":"active","state":"fresh","synced_through":"2026-01-10T14:00:00Z","lag_seconds":3600,"last_sync_at":"2026-01-10T14:05:00Z","last_event_at":null,"push_healthy":true,"pull_every_seconds":21600,"next_pull_at":"2026-01-10T20:05:00Z","last_attempt_at":"2026-01-10T14:05:00Z","last_error":null,"consecutive_failures":0}]}"#)
        let freshness = try await c2.getFreshness(userId: "u")
        XCTAssertEqual(freshness.data[0].state, "fresh")
        XCTAssertEqual(freshness.data[0].lagSeconds, 3600)
        XCTAssertNil(freshness.data[0].lastEventAt)

        let (c3, _) = client(#"{"data":[{"provider":"oura","status":"active","scopes":["daily","heartrate"],"last_sync_at":null,"initial_backfill_at":"2026-01-09T10:00:00Z","created_at":"2026-01-09T09:59:00Z","updated_at":"2026-01-10T14:05:00Z"}]}"#)
        let connections = try await c3.listConnections(userId: "u")
        XCTAssertEqual(connections[0].scopes ?? [], ["daily", "heartrate"])

        let (c4, _) = client(#"{"data":[{"slug":"apple_health","kinds":["device"]},{"slug":"oura","kinds":["oauth","push"]}]}"#)
        let providers = try await c4.listProviders()
        XCTAssertEqual(providers.map { $0.slug }, ["apple_health", "oura"])
    }
}
