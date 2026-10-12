// SPDX-License-Identifier: MIT
package org.openhealthsync.sdk

import java.io.IOException
import java.time.Instant
import java.util.zip.GZIPInputStream
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put

/** Answers each request with the next queued response and records what was sent. */
private class FakeTransport(vararg responses: Any) : HttpTransport {
    val queue = ArrayDeque(responses.toList())
    val requests = mutableListOf<HttpRequest>()

    override fun send(request: HttpRequest): HttpResponse {
        requests += request
        return when (val next = queue.removeFirst()) {
            is HttpResponse -> next
            is IOException -> throw next
            else -> error("bad fake response $next")
        }
    }
}

private fun ok(body: String, status: Int = 200, headers: Map<String, String> = emptyMap()) =
    HttpResponse(status, headers.mapKeys { it.key.lowercase() }, body.toByteArray())

private const val USER = "3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11"
private const val PROV = """"provider":"apple_health","source":null,"device":"Apple Watch","ingested_at":"2026-01-10T15:00:00.123456789Z""""

class OhsClientTest {
    private fun client(t: FakeTransport, sleeps: MutableList<Long> = mutableListOf()) =
        OhsClient("https://ohs.example.org/", { "ohs_ut_test" }, t, sleep = { sleeps += it })

    @Test
    fun sendsAuthAndDecodesSamplesWithNanoseconds() = runTest {
        val t = FakeTransport(
            ok("""{"data":[{"id":1,"type":"heart_rate","value":72.5,"unit":"bpm","start_date":"2026-01-10T14:30:00Z","end_date":"2026-01-10T14:30:00.5Z","source_name":"Apple Watch","source_bundle_id":null,"device_model":"Watch","product_type":"Watch7,1","external_id":null,"is_primary_source":true,"was_user_entered":false,"metadata":{"timezone":"Europe/Paris"},$PROV,"future_field":1}],"next_cursor":"abc"}"""),
        )
        val page = client(t).listSamples(USER, "heart_rate", Range(start = "2026-01-10", limit = 10))
        val req = t.requests.single()
        assertEquals("GET", req.method)
        assertEquals("https://ohs.example.org/v1/users/$USER/samples?type=heart_rate&start=2026-01-10&limit=10", req.url)
        assertEquals("Bearer ohs_ut_test", req.headers["Authorization"])
        assertEquals("application/json", req.headers["Accept"])
        assertTrue(req.headers["User-Agent"]!!.startsWith("open-health-sync-kotlin/"))
        assertEquals("abc", page.nextCursor)
        val s = page.data.single()
        assertEquals(72.5, s.value)
        assertEquals(Instant.parse("2026-01-10T15:00:00.123456789Z"), s.ingestedAt)
        assertEquals(Instant.parse("2026-01-10T14:30:00.500Z"), s.endDate)
        assertEquals("Europe/Paris", s.metadata["timezone"]!!.jsonPrimitive.content)
        assertEquals("apple_health", s.provider)
    }

    @Test
    fun decodesEveryRecordType() = runTest {
        val t = FakeTransport(
            ok("""{"data":[{"id":2,"type":"sleep_analysis","value_int":4,"value_label":"asleep_deep","start_date":"2026-01-10T01:00:00Z","end_date":null,"metadata":{},$PROV}],"next_cursor":null}"""),
            ok("""{"data":[{"id":3,"bedtime_start":"2026-01-09T22:30:00Z","bedtime_end":"2026-01-10T06:30:00Z","total_sleep_seconds":27000,"deep_sleep_seconds":5400,"rem_sleep_seconds":6000,"light_sleep_seconds":15600,"awake_seconds":1800,"sleep_score":null,"hrv_avg_ms":45.5,"resting_hr":52,"respiratory_rate":14.2,"spo2_avg":96.5,"external_id":null,"metadata":{},$PROV}],"next_cursor":null}"""),
            ok("""{"data":[{"id":4,"session_id":3,"phase":"deep","start_date":"2026-01-09T23:00:00Z","end_date":"2026-01-09T23:30:00Z","duration_s":1800,$PROV}],"next_cursor":null}"""),
            ok("""{"data":[{"id":5,"activity_type":"running","start_date":"2026-01-10T14:30:00Z","end_date":"2026-01-10T15:00:00Z","duration_s":1800,"active_energy_kcal":200,"distance_m":2000.5,"avg_heart_rate":120,"max_heart_rate":150,"avg_speed_mps":1.11,"elevation_gain_m":null,"vo2_max":null,"external_id":null,"metadata":{},$PROV}],"next_cursor":null}"""),
            ok("""{"data":[{"id":6,"workout_id":5,"timestamp":"2026-01-10T14:31:00Z","latitude":48.85,"longitude":2.35,"altitude_m":35,"speed_mps":2.9,"course_deg":null,$PROV}],"next_cursor":null}"""),
            ok("""{"data":[{"id":7,"start_date":"2026-01-10T12:00:00Z","end_date":null,"meal_type":"lunch","energy_kcal":650,"protein_g":30,"carbs_g":70,"fat_g":20,"fiber_g":8,"sugar_g":10,"sodium_mg":800,"nutrients":{"vitamin_c_mg":12},"external_id":null,"metadata":{},$PROV}],"next_cursor":null}"""),
            ok("""{"data":[{"id":8,"date":"2026-01-10","readiness_score":null,"activity_score":null,"sleep_score":null,"recovery_score":null,"strain_score":null,"step_count":9720,"active_kcal":410.5,"total_kcal":2200,"distance_m":7100,"floors_climbed":12,"resting_hr":52,"hrv_avg_ms":44,"metadata":{"timezone":"Europe/Paris"},$PROV}],"next_cursor":null}"""),
            ok("""{"pending_jobs":0,"parked_jobs":0,"rejected_records":1,"truncated":false,"days":[{"provider":"apple_health","date":"2026-01-10","records":42,"metrics":{"heart_rate":40,"step_count":2},"last_synced_at":"2026-01-10T15:00:00Z"}]}"""),
        )
        val c = client(t)
        assertEquals("asleep_deep", c.listCategorySamples(USER).data.single().valueLabel)
        assertEquals(27000, c.listSleep(USER).data.single().totalSleepSeconds)
        assertEquals("deep", c.listSleepPhases(USER, 3).data.single().phase)
        assertEquals(2000.5, c.listWorkouts(USER, "running").data.single().distanceM)
        assertEquals(48.85, c.getWorkoutRoute(USER, 5).data.single().latitude)
        assertEquals(12, c.listNutrition(USER).data.single().nutrients!!["vitamin_c_mg"]!!.jsonPrimitive.int)
        assertEquals(9720, c.listDaily(USER).data.single().stepCount)
        val cov = c.getCoverage(USER, provider = "apple_health")
        assertEquals(40, cov.days.single().metrics["heart_rate"])
        assertEquals(1, cov.rejectedRecords)
        val urls = t.requests.map { it.url.removePrefix("https://ohs.example.org/v1/users/$USER") }
        assertEquals(
            listOf("/category-samples", "/sleep", "/sleep/3/phases", "/workouts?type=running", "/workouts/5/route", "/nutrition", "/daily", "/coverage?provider=apple_health"),
            urls,
        )
    }

    @Test
    fun usersTokensAndConnections() = runTest {
        val t = FakeTransport(
            ok("""{"id":"$USER","external_ref":"demo","status":"active","created_at":"2026-01-10T15:00:00Z"}""", 201),
            ok("""{"token":"ohs_ut_secret","expires_at":"2026-01-10T16:00:00Z","scopes":["upload","read"]}""", 201),
            ok("""{"authorize_url":"https://provider.example/authorize?state=x"}"""),
            ok("", 204),
            ok("""{"job_id":"7d1e2c3b-0000-4000-8000-000000000001"}""", 202),
            ok("""{"as_of":"2026-01-10T15:00:00Z","data":[{"provider":"oura","kind":"oauth","status":"active","state":"fresh","synced_through":"2026-01-10T14:00:00Z","lag_seconds":3600,"push_healthy":true,"pull_every_seconds":21600,"consecutive_failures":0}]}"""),
        )
        val c = client(t)
        assertEquals("demo", c.createUser("demo", idempotencyKey = "k1").externalRef)
        val token = c.mintUserToken(USER, listOf("upload", "read"), ttlSeconds = 600)
        assertFalse(token.toString().contains("ohs_ut_secret"))
        assertEquals("https://provider.example/authorize?state=x", c.startConnection(USER, "oura", "myapp://done"))
        c.deleteConnection(USER, "oura")
        assertEquals("7d1e2c3b-0000-4000-8000-000000000001", c.deleteUser(USER))
        assertEquals("fresh", c.getFreshness(USER).data.single().state)

        val (create, mint, start, del) = t.requests
        assertEquals("k1", create.headers["Idempotency-Key"])
        assertEquals("""{"external_ref":"demo"}""", String(create.body!!))
        assertEquals(Json.parseToJsonElement("""{"scopes":["upload","read"],"ttl_seconds":600}"""), Json.parseToJsonElement(String(mint.body!!)))
        assertEquals("""{"redirect_uri":"myapp://done"}""", String(start.body!!))
        assertEquals("DELETE", del.method)
        assertTrue(del.url.endsWith("/connections/oura"))
    }

    @Test
    fun problemBecomesException() = runTest {
        val t = FakeTransport(
            ok("""{"type":"about:blank","title":"Not Found","status":404,"detail":"no such user","request_id":"req-1"}""", 404),
            ok("<html>bad gateway</html>", 502, mapOf("X-Request-Id" to "req-2")),
        )
        val c = client(t)
        val e = assertFailsWith<OhsApiException> { c.getUser(USER) }
        assertEquals(404, e.status)
        assertEquals("no such user", e.detail)
        assertEquals("req-1", e.requestId)
        assertFalse(e.message!!.contains("ohs_ut_test"))
        val e2 = assertFailsWith<OhsApiException> { c.getUser(USER) }
        assertEquals(502, e2.status)
        assertNull(e2.title)
        assertEquals("req-2", e2.requestId)
    }

    @Test
    fun walksEveryPage() = runTest {
        val user = """{"id":"$USER","external_ref":null,"status":"active","created_at":"2026-01-10T15:00:00Z"}"""
        val t = FakeTransport(ok("""{"data":[$user,$user],"next_cursor":"p2"}"""), ok("""{"data":[$user],"next_cursor":null}"""))
        val c = client(t)
        val all = allPages { c.listUsers(cursor = it, limit = 2) }.toList()
        assertEquals(3, all.size)
        assertTrue(t.requests[1].url.endsWith("/v1/users?cursor=p2&limit=2"))
    }

    @Test
    fun tokenProviderIsCalledForEachRequest() = runTest {
        var n = 0
        val t = FakeTransport(ok("""{"data":[]}"""), ok("""{"data":[]}"""))
        val c = OhsClient("https://ohs.example.org", { "ohs_ut_${++n}" }, t)
        c.listProviders()
        c.listProviders()
        assertEquals(listOf("Bearer ohs_ut_1", "Bearer ohs_ut_2"), t.requests.map { it.headers["Authorization"] })
    }

    @Test
    fun fhirBundleCursor() = runTest {
        val t = FakeTransport(
            ok(
                """{"resourceType":"Bundle","type":"searchset","link":[{"relation":"self","url":"https://ohs.example.org/v1/users/$USER/fhir/Observation"},{"relation":"next","url":"https://ohs.example.org/v1/users/$USER/fhir/Observation?category=vital-signs&cursor=a%2Bb%3D&limit=1"}],"entry":[{"fullUrl":"https://ohs.example.org/v1/users/$USER/fhir/Observation/qs-1","resource":{"resourceType":"Observation","id":"qs-1","status":"final"}}]}""",
                headers = mapOf("Content-Type" to "application/fhir+json"),
            ),
            ok("""{"resourceType":"Observation","id":"qs-1","status":"final"}"""),
        )
        val c = client(t)
        val b = c.searchFhirObservations(USER, category = "vital-signs", code = "http://loinc.org|8867-4", range = Range(limit = 1))
        assertEquals("a+b=", b.nextCursor)
        assertEquals("qs-1", b.observations.single()["id"]!!.jsonPrimitive.content)
        assertEquals("application/fhir+json", t.requests[0].headers["Accept"])
        assertTrue(t.requests[0].url.contains("code=http%3A%2F%2Floinc.org%7C8867-4"))
        assertEquals("final", c.readFhirObservation(USER, "qs-1")["status"]!!.jsonPrimitive.content)
    }

    @Test
    fun uploadRetriesHonouringRetryAfter() = runTest {
        val sleeps = mutableListOf<Long>()
        val t = FakeTransport(
            ok("""{"type":"about:blank","title":"Service Unavailable","status":503}""", 503, mapOf("Retry-After" to "7")),
            IOException("reset"),
            ok("""{"key":"raw/t/u/apple_health/2026-01-10/b_0.ndjson.gz","duplicate":false}"""),
        )
        val part = AppleHealthPartBuilder("b").add("HKQuantityTypeIdentifierHeartRate", "2026-01-10", hr()).build().single()
        val res = client(t, sleeps).uploadAppleHealthPart(part)
        assertFalse(res.duplicate)
        assertEquals(listOf(7000L, 2000L), sleeps)
        val req = t.requests.last()
        assertEquals("https://ohs.example.org/v1/ingest/apple-health", req.url)
        assertEquals("application/gzip", req.headers["Content-Type"])
        assertEquals("HKQuantityTypeIdentifierHeartRate", req.headers["X-OHS-Type"])
        assertEquals("2026-01-10", req.headers["X-OHS-Day"])
        assertEquals("b", req.headers["X-OHS-Batch"])
        assertEquals("0", req.headers["X-OHS-Part"])
    }

    @Test
    fun uploadDoesNotRetryClientErrorsAndGivesUpAfterFive() = runTest {
        val part = AppleHealthPartBuilder("b").add("HKQuantityTypeIdentifierHeartRate", "2026-01-10", hr()).build().single()
        val t400 = FakeTransport(ok("""{"title":"Bad Request","status":400}""", 400))
        assertEquals(400, assertFailsWith<OhsApiException> { client(t400).uploadAppleHealthPart(part) }.status)
        assertEquals(1, t400.requests.size)

        val sleeps = mutableListOf<Long>()
        val t503 = FakeTransport(*Array(5) { ok("", 503) })
        assertEquals(503, assertFailsWith<OhsApiException> { client(t503, sleeps).uploadAppleHealthPart(part) }.status)
        assertEquals(5, t503.requests.size)
        assertEquals(listOf(1000L, 2000L, 4000L, 8000L), sleeps)
    }

    private fun hr(value: Double = 72.0, at: String = "2026-01-10T14:30:00Z"): JsonObject = buildJsonObject {
        put("quantityType", "HKQuantityTypeIdentifierHeartRate")
        put("startDate", at)
        put("endDate", at)
        put("value", value)
        put("unit", "count/min")
        put("metadata", buildJsonObject { put("HKTimeZone", "UTC") })
    }

    @Test
    fun partBuilderGroupsSplitsAndNumbers() {
        val b = AppleHealthPartBuilder("batch_1", maxPartBytes = 400)
        repeat(3) { b.add("HKQuantityTypeIdentifierHeartRate", "2026-01-10", hr(70.0 + it)) }
        b.add("HKQuantityTypeIdentifierStepCount", "2026-01-10", buildJsonObject { put("quantityType", "HKQuantityTypeIdentifierStepCount") })
        b.add("HKQuantityTypeIdentifierHeartRate", "2026-01-11", hr(80.0, "2026-01-11T08:00:00Z"))
        val parts = b.build()
        // Each heart rate line is about 170 bytes: two fit in 400, the third starts a new part.
        assertEquals(listOf(0, 1, 2, 3), parts.map { it.partNumber })
        assertEquals(listOf(2, 1, 1, 1), parts.map { it.lines })
        assertEquals(listOf("2026-01-10", "2026-01-10", "2026-01-10", "2026-01-11"), parts.map { it.day })
        assertEquals("HKQuantityTypeIdentifierStepCount", parts[2].type)

        val ndjson = GZIPInputStream(parts[0].body.inputStream()).readBytes().toString(Charsets.UTF_8)
        val lines = ndjson.split("\n")
        assertEquals(3, lines.size)
        assertEquals("", lines[2])
        assertEquals(70.0, Json.parseToJsonElement(lines[0]).jsonObject["value"]!!.jsonPrimitive.content.toDouble())
        assertTrue(ndjson.length <= 400)
    }

    @Test
    fun partBuilderValidates() {
        assertFailsWith<IllegalArgumentException> { AppleHealthPartBuilder("bad batch") }
        val b = AppleHealthPartBuilder(AppleHealthPartBuilder.newBatchId("ios"))
        assertFailsWith<IllegalArgumentException> { b.add("HK Type", "2026-01-10", hr()) }
        assertFailsWith<IllegalArgumentException> { b.add("HKQuantityTypeIdentifierHeartRate", "10/01/2026", hr()) }
        assertEquals(
            "2026-01-11",
            AppleHealthPartBuilder.localDay(Instant.parse("2026-01-10T23:30:00Z"), java.time.ZoneId.of("Europe/Paris")),
        )
        assertTrue(Regex("^ios-[a-z0-9]{16}$").matches(AppleHealthPartBuilder.newBatchId("ios")))
    }
}
