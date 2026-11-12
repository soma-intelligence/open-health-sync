// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.sdk

import java.io.IOException
import java.util.zip.GZIPInputStream
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertTrue
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put

class HealthConnectPartsTest {
    private fun steps(id: String, count: Long = 10): JsonObject = buildJsonObject {
        put("recordType", "Steps")
        put("metadata", buildJsonObject { put("id", id) })
        put("startTime", "2026-03-02T08:00:00Z")
        put("endTime", "2026-03-02T09:00:00Z")
        put("count", count)
    }

    private fun ndjson(part: HealthConnectPart): String =
        GZIPInputStream(part.body.inputStream()).readBytes().toString(Charsets.UTF_8)

    @Test
    fun groupsByTypeAndDayAndNumbersAcrossDrains() {
        val b = HealthConnectPartBuilder("hc_1", maxPartBytes = 300)
        // Each Steps line is about 130 bytes: two fit in 300, the third starts a new part.
        repeat(3) { assertTrue(b.add("Steps", "2026-03-02", steps("st-$it"))) }
        b.add("Steps", "2026-03-03", steps("st-9"))
        assertTrue(b.addDeletion("2026-03-03", "st-0"))
        assertTrue(b.bufferedBytes > 0)
        val first = b.drain()
        assertEquals(listOf(0, 1, 2, 3), first.map { it.partNumber })
        assertEquals(listOf(2, 1, 1, 1), first.map { it.lines })
        assertEquals(listOf("Steps", "Steps", "Steps", "Deletion"), first.map { it.type })
        assertEquals(listOf("2026-03-02", "2026-03-02", "2026-03-03", "2026-03-03"), first.map { it.day })
        assertEquals(0, b.bufferedBytes)
        assertTrue(b.drain().isEmpty())

        val text = ndjson(first[0])
        assertTrue(text.endsWith("\n"))
        assertTrue(text.length <= 300)
        assertEquals("st-0", Json.parseToJsonElement(text.lines()[0]).jsonObject["metadata"]!!.jsonObject["id"]!!.jsonPrimitive.content)
        assertEquals("""{"recordType":"Deletion","recordId":"st-0"}""" + "\n", ndjson(first[3]))

        // A later drain keeps counting, so (batch, part) stays unique.
        b.add("Weight", "2026-03-02", buildJsonObject { put("recordType", "Weight") })
        assertEquals(listOf(4), b.build().map { it.partNumber })
    }

    @Test
    fun validates() {
        assertFailsWith<IllegalArgumentException> { HealthConnectPartBuilder("bad batch") }
        assertFailsWith<IllegalArgumentException> { HealthConnectPartBuilder("b", maxPartBytes = 0) }
        val b = HealthConnectPartBuilder("b")
        assertFailsWith<IllegalArgumentException> { b.add("Blood Glucose", "2026-03-02", steps("x")) }
        assertFailsWith<IllegalArgumentException> { b.add("Steps", "02/03/2026", steps("x")) }
        // The server refuses a part with a line that has no recordType.
        assertFailsWith<IllegalArgumentException> { b.add("Steps", "2026-03-02", buildJsonObject { put("count", 1) }) }
        assertFailsWith<IllegalArgumentException> { b.add("Steps", "2026-03-02", buildJsonObject { put("recordType", 3) }) }
        assertFailsWith<IllegalArgumentException> { HealthConnectPartBuilder.deletionLine("") }
        assertTrue(b.drain().isEmpty())
    }

    @Test
    fun refusesALineLongerThanTheServerReads() {
        val b = HealthConnectPartBuilder("b")
        val huge = buildJsonObject {
            put("recordType", "ExerciseSession")
            put("title", "x".repeat(HealthConnectPartBuilder.MAX_LINE_BYTES))
        }
        assertFalse(b.add("ExerciseSession", "2026-03-02", huge))
        assertEquals(0, b.bufferedBytes)
        assertTrue(b.drain().isEmpty())
    }

    @Test
    fun uploadsToTheHealthConnectRouteWithRetries() = runTest {
        val requests = mutableListOf<HttpRequest>()
        val answers = ArrayDeque<Any>(
            listOf(
                HttpResponse(503, mapOf("retry-after" to "5"), ByteArray(0)),
                IOException("reset"),
                HttpResponse(200, emptyMap(), """{"key":"raw/t/u/health_connect/2026-03-02/hc_0.ndjson.gz","duplicate":true}""".toByteArray()),
            ),
        )
        val transport = HttpTransport { req ->
            requests += req
            when (val next = answers.removeFirst()) {
                is HttpResponse -> next
                is IOException -> throw next
                else -> error("bad fake response")
            }
        }
        val sleeps = mutableListOf<Long>()
        val client = OhsClient("https://ohs.example.org", { "ohs_ut_test" }, transport, sleep = { sleeps += it })
        val part = HealthConnectPartBuilder("hc").apply { add("Steps", "2026-03-02", steps("st-1")) }.build().single()
        val res = client.uploadHealthConnectPart(part)
        assertTrue(res.duplicate)
        assertEquals(listOf(5000L, 2000L), sleeps)
        val req = requests.last()
        assertEquals("POST", req.method)
        assertEquals("https://ohs.example.org/v1/ingest/health-connect", req.url)
        assertEquals("application/gzip", req.headers["Content-Type"])
        assertEquals("Steps", req.headers["X-OHS-Type"])
        assertEquals("2026-03-02", req.headers["X-OHS-Day"])
        assertEquals("hc", req.headers["X-OHS-Batch"])
        assertEquals("0", req.headers["X-OHS-Part"])
        assertEquals("Bearer ohs_ut_test", req.headers["Authorization"])
    }
}
